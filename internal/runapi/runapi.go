// Package runapi is the client of Qory Apiary's run API, which a run uses to read its
// work item and the work item's artifacts, and to push artifacts and its verdict.
//
// The client sends every request to the base URL http://run-api.qory.invalid/v1/runs/<run
// id>, the same value in the wall and on the host. The client has no key and sends no
// signature: the gateway adds the signature of its access key and forwards the request.
// The caller gives the base URL and the *http.Client that reaches the gateway: in the wall
// a client that uses HTTP_PROXY, on the host the one that Forager's run returns. This
// package reads no environment variable.
//
// The client never retries. A POST whose response was lost may have stored its version, so
// a caller reads [Client.Artifacts] before it sends the POST again.
package runapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// MaxBody is the largest body of POST artifacts in bytes, once encoded: about 24 MiB of
// files in one version, whatever the size limit of the artifact type.
const MaxBody = 32 << 20

// maxVerdictBody is the largest body of POST verdict in bytes.
const maxVerdictBody = 64 << 10

// MaxFiles is the most files of one version in POST artifacts, whatever the maximum of the
// artifact type. The run API returns 400 past it, with no type or limit in the error.
const MaxFiles = 100

// The limits of what the client reads of a response other than a file. maxResponse is
// above the 768 KiB of a work item snapshot and leaves room for a long artifact index.
const (
	maxResponse      = 16 << 20
	maxErrorResponse = 64 << 10
)

// wireVersion is the `version` member of the run API's responses that this client reads.
const wireVersion = 1

// The verdicts of [Client.PushVerdict].
const (
	VerdictPassed           = "passed"
	VerdictChangesRequested = "changes-requested"
	VerdictEscalate         = "escalate"
)

// The actions of a [Point].
const (
	ActionAnswer   = "answer"
	ActionRunAgain = "run_again"
)

// Client sends a run's requests to the run API.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for baseURL, http://run-api.qory.invalid/v1/runs/<run id>, that
// sends its requests with client. A base URL with a trailing slash is the same URL. Its
// errors contain no user, password, query or fragment of baseURL.
func New(baseURL string, client *http.Client) (*Client, error) {
	if client == nil {
		return nil, errors.New("Pass an HTTP client to runapi.New. The client is nil.")
	}

	const fix = "Set the run API's base URL to http://run-api.qory.invalid/v1/runs/<run id>."

	u, err := url.Parse(baseURL)
	if err != nil {
		// No part of the error: it can contain the value, or a part of a password.
		return nil, errors.New(fix + " The value does not parse as a URL.")
	}

	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		strings.ContainsAny(baseURL, "?#") {
		shown := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}

		return nil, fmt.Errorf("%s It needs http or https and no user, query or fragment. "+
			"The value without those is %q.", fix, shown.String())
	}

	return &Client{base: strings.TrimRight(baseURL, "/"), http: client}, nil
}

// ProcessTypes are the four artifact types that Qory Apiary reads itself to drive the
// process: questions, review-comments, verdict and proposal. A run cannot withdraw a name
// of these types.
var ProcessTypes = []string{"questions", "review-comments", "verdict", "proposal"}

// VersionRef is one version of an artifact.
type VersionRef struct {
	ID     string `json:"id,case:strict"`
	Number int    `json:"number,case:strict"`
}

// Snapshot is the work item as the run reads it. It has the members that qory acts on;
// Raw is the whole response, as the run API returned it.
type Snapshot struct {
	WorkItem struct {
		ID     string `json:"id,case:strict"`
		Number int    `json:"number,case:strict"`
		URL    string `json:"url,case:strict"`
	} `json:"work_item,case:strict"`
	// Number is the run's number on the server, from 1, and 0 in a response with none.
	// Workflow and Round are nil for a run that has neither.
	Run struct {
		ID       string  `json:"id,case:strict"`
		Number   int     `json:"number,case:strict"`
		Workflow *string `json:"workflow,case:strict"`
		Round    *int    `json:"round,case:strict"`
	} `json:"run,case:strict"`
	// Name, Round, Rounds and State are nil for a work item that is in no stage.
	Stage struct {
		ID     *string `json:"id,case:strict"`
		Name   *string `json:"name,case:strict"`
		Round  *int    `json:"round,case:strict"`
		Rounds *int    `json:"rounds,case:strict"`
		State  *string `json:"state,case:strict"`
	} `json:"stage,case:strict"`
	Inputs  []Input  `json:"inputs,case:strict"`
	Outputs []Output `json:"outputs,case:strict"`
	// Verdict is the work item's newest verdict, nil for none.
	Verdict *GivenVerdict `json:"verdict,case:strict"`
	// PushedCommits is every commit that an earlier run of the work item pushed, ordered by
	// run number and then push order, without the commits of the requesting run; nil for none.
	PushedCommits []PushedEntry `json:"pushed_commits,case:strict"`
	// Notes is the work item's notes, oldest first, byte for byte as the run API returned
	// them: a JSON array, or null or empty for a response with no notes.
	Notes json.RawMessage `json:"notes,case:strict"`

	Raw json.RawMessage `json:"-"`
}

// PushedEntry is one commit that a run pushed: the repository, the commit, the run, whether a
// member cancelled that run, and when the push was recorded.
type PushedEntry struct {
	System    string `json:"system,case:strict"`
	Path      string `json:"path,case:strict"`
	Commit    string `json:"commit,case:strict"`
	RunID     string `json:"run_id,case:strict"`
	Cancelled bool   `json:"cancelled,case:strict"`
	At        string `json:"at,case:strict"`
}

// Input is one input of the work item's stage. Version is nil when the input has no
// version yet.
type Input struct {
	Type     string      `json:"type,case:strict"`
	Required bool        `json:"required,case:strict"`
	Version  *VersionRef `json:"version,case:strict"`
}

// Output is one output of the work item's stage, with its newest version, nil for none,
// and its check: `passed`, `missing` or `not_checked`.
type Output struct {
	Type     string      `json:"type,case:strict"`
	Required bool        `json:"required,case:strict"`
	Version  *VersionRef `json:"version,case:strict"`
	Check    string      `json:"check,case:strict"`
}

// GivenVerdict is a verdict that the work item has.
type GivenVerdict struct {
	Verdict string      `json:"verdict,case:strict"`
	Version *VersionRef `json:"version,case:strict"`
}

// Point is one point of a verdict that [Client.PushVerdict] sends: what is wrong or
// missing, and its action. Question is the id of a question for the action
// answer, and empty for run_again. Fix is what a person does to fix the point, for
// run_again only: Qory Apiary shows it in place of the action's sentence. Fix is empty
// for none. The contract is contracts/harness/v1/verdict.schema.json.
type Point struct {
	Text     string `json:"text,case:strict"`
	Action   string `json:"action,case:strict"`
	Question string `json:"question,omitempty,case:strict"`
	Fix      string `json:"fix,omitempty,case:strict"`
}

// Track is the track that a verdict proposes for the work item, with the reason. ID is
// the id of a track that the gate of the run's stage offers. The contract is
// contracts/harness/v1/verdict.schema.json.
type Track struct {
	ID     string `json:"id,case:strict"`
	Reason string `json:"reason,case:strict"`
}

// Index is the work item's artifacts with their versions. Raw is the whole response, as
// the run API returned it.
type Index struct {
	Artifacts []Artifact `json:"artifacts,case:strict"`

	Raw json.RawMessage `json:"-"`
}

// Artifact is one artifact of a work item. ApprovedVersion is the id of the newest
// version that a gate approved, nil for none; NewestVersion is the number of the newest
// version.
type Artifact struct {
	Type                 string    `json:"type,case:strict"`
	Name                 string    `json:"name,case:strict"`
	StageID              *string   `json:"stage_id,case:strict"`
	ApprovedVersion      *string   `json:"approved_version,case:strict"`
	NewestVersion        int       `json:"newest_version,case:strict"`
	ChangedAfterApproval bool      `json:"changed_after_approval,case:strict"`
	Versions             []Version `json:"versions,case:strict"`
}

// Version is one version of an artifact. Cancelled is true when a member cancelled the
// run that wrote the version, and Withdrawn when the version is a withdrawal: it has no
// files and removes its name from gates, stage inputs and the work item's pages. An
// index without either field reads it as false.
type Version struct {
	ID        string     `json:"id,case:strict"`
	Number    int        `json:"number,case:strict"`
	Writer    Writer     `json:"writer,case:strict"`
	Cancelled bool       `json:"cancelled,case:strict"`
	Withdrawn bool       `json:"withdrawn,case:strict"`
	StageID   *string    `json:"stage_id,case:strict"`
	Note      *string    `json:"note,case:strict"`
	At        string     `json:"at,case:strict"`
	Files     []File     `json:"files,case:strict"`
	Approvals []Approval `json:"approvals,case:strict"`
}

// Writer is who wrote a version: Kind `run` with RunID, or `person` with Name.
type Writer struct {
	Kind  string `json:"kind,case:strict"`
	RunID string `json:"run_id,omitempty,case:strict"`
	Name  string `json:"name,omitempty,case:strict"`
}

// File is one file of a version, as the index lists it. Filename comes from the server:
// check it before using it as a path.
type File struct {
	Filename    string `json:"filename,case:strict"`
	ContentType string `json:"content_type,case:strict"`
	Size        int64  `json:"size,case:strict"`
	SHA256      string `json:"sha256,case:strict"`
}

// Approval is a gate's approval of a version. Automatic is true for the approval that a
// gate with no approver records when the stage passes it, and then By has no name; it is
// false for the approval of a member. An index without the field reads as false.
type Approval struct {
	By struct {
		Name string `json:"name,case:strict"`
	} `json:"by,case:strict"`
	Automatic bool    `json:"automatic,case:strict"`
	StageID   *string `json:"stage_id,case:strict"`
	At        string  `json:"at,case:strict"`
}

// Push is the body of a new version of an artifact. Name, Note and Decision are left out
// of the request when they are empty, and the run API then uses the name `main`.
// Decision is the id of the person's note on the work item that asked for the version.
// Withdraw makes the version a withdrawal, which has no Files.
type Push struct {
	Type     string     `json:"type"`
	Name     string     `json:"name,omitempty"`
	Note     string     `json:"note,omitempty"`
	Decision string     `json:"decision,omitempty"`
	Withdraw bool       `json:"withdraw,omitempty"`
	Files    []PushFile `json:"files"`
}

// PushFile is one file of a [Push]. encoding/json writes Content as standard base64 with
// padding, the form that the run API reads; [Client.PushArtifact] sends nil Content as an
// empty file.
type PushFile struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Content     []byte `json:"content_base64"`
}

// Pushed is the version that a push or a verdict stored.
type Pushed struct {
	Type    string     `json:"type,case:strict"`
	Name    string     `json:"name,case:strict"`
	Version VersionRef `json:"version,case:strict"`
}

// WorkItem returns the work item snapshot.
func (c *Client) WorkItem(ctx context.Context) (*Snapshot, error) {
	var snapshot Snapshot

	raw, err := c.exchange(ctx, http.MethodGet, "/work-item", nil, http.StatusOK, &snapshot)
	if err != nil {
		return nil, err
	}

	snapshot.Raw = raw

	return &snapshot, nil
}

// Artifacts returns the work item's artifacts with their versions.
func (c *Client) Artifacts(ctx context.Context) (*Index, error) {
	var index Index

	raw, err := c.exchange(ctx, http.MethodGet, "/artifacts", nil, http.StatusOK, &index)
	if err != nil {
		return nil, err
	}

	index.Raw = raw

	return &index, nil
}

// Download writes the bytes of file, a file of the version versionID in the index, to w,
// and checks them against the size and the SHA-256 in the index. The gateway stops a file
// that fails its check by ending the connection, so a read error is a failed download. On
// an error, w may contain part of the file or bytes that fail the check: discard them.
func (c *Client) Download(ctx context.Context, versionID string, file File, w io.Writer) error {
	for _, segment := range []string{versionID, file.Filename} {
		if !pathSegment(segment) {
			return fmt.Errorf(
				"Pull the file by a version id and filename from the artifact index. %q cannot be a part of a path.",
				segment)
		}
	}

	path := "/artifacts/" + url.PathEscape(versionID) + "/files/" + url.PathEscape(file.Filename)
	request := http.MethodGet + " " + path

	response, err := c.send(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return readError(response, request)
	}

	// One byte past the size is enough to show that the file is longer than the index says.
	digest := sha256.New()
	target := &writeRecorder{w: w}

	size, err := io.Copy(io.MultiWriter(target, digest), io.LimitReader(response.Body, file.Size+1))
	if target.err != nil {
		return fmt.Errorf("Check where %s is written, then pull it again. Writing it failed after %d bytes: %w",
			file.Filename, size, target.err)
	}

	if err != nil {
		return fmt.Errorf("Pull %s again. %s failed after %d bytes: %w", file.Filename, request, size, err)
	}

	if size > file.Size {
		return fmt.Errorf("Pull %s again. %s returned more bytes than the %d that the index lists.",
			file.Filename, request, file.Size)
	}

	if size < file.Size {
		return fmt.Errorf("Pull %s again. %s returned %d bytes, and the index lists %d.",
			file.Filename, request, size, file.Size)
	}

	if sum := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(sum, file.SHA256) {
		return fmt.Errorf("Pull %s again. %s returned bytes with SHA-256 %s, and the index lists %s.",
			file.Filename, request, sum, file.SHA256)
	}

	return nil
}

// PushArtifact stores a new version of an artifact of the run's work item. A version that
// [CheckVersion] stops returns its [Error] and sends nothing.
func (c *Client) PushArtifact(ctx context.Context, push Push) (*Pushed, error) {
	// nil would encode as null, and the run API reads a string and a list.
	files := make([]PushFile, len(push.Files))
	sizes := make([]int64, len(push.Files))

	for i, file := range push.Files {
		if file.Content == nil {
			file.Content = []byte{}
		}

		files[i] = file
		sizes[i] = int64(len(file.Content))
	}

	if err := CheckVersion(push.Type, sizes); err != nil {
		return nil, err
	}

	push.Files = files

	body, err := encode(push)
	if err != nil {
		return nil, fmt.Errorf("Check the files of the version of %s. Encoding the request failed: %w", push.Type, err)
	}

	if len(body) > MaxBody {
		return nil, versionTooLarge(push.Type, int64(len(body)))
	}

	return c.post(ctx, "/artifacts", body)
}

// CheckVersion returns an [Error] for a version with files of sizes bytes that the run API
// cannot take, with the code [CodeTooManyFiles] past [MaxFiles], and
// [CodePayloadTooLarge] past [MaxBody] once encoded; else nil. A caller checks with it
// before it reads the files.
func CheckVersion(artifactType string, sizes []int64) error {
	if len(sizes) > MaxFiles {
		return &Error{
			Code:    CodeTooManyFiles,
			Type:    artifactType,
			Limit:   MaxFiles,
			Message: fmt.Sprintf("Push at most %d files in one version of %s. This version has %d.", MaxFiles, artifactType, len(sizes)),
		}
	}

	var encoded int64
	for _, size := range sizes {
		encoded += (size + 2) / 3 * 4
	}

	if encoded > MaxBody {
		return versionTooLarge(artifactType, encoded)
	}

	return nil
}

// PushVerdict stores the run's verdict, one of the Verdict constants, with the body of
// [VerdictBody]. It sends nothing for a body over the limit of POST verdict.
func (c *Client) PushVerdict(ctx context.Context, verdict string, points []Point, track *Track) (*Pushed, error) {
	body, err := VerdictBody(verdict, points, track)
	if err != nil {
		return nil, err
	}

	if len(body) > maxVerdictBody {
		return nil, &Error{Code: CodePayloadTooLarge, Request: "POST /verdict", Limit: maxVerdictBody}
	}

	return c.post(ctx, "/verdict", body)
}

// VerdictBody is the document of a verdict, as POST verdict reads it and as the file of
// a version of the artifact type verdict: verdict, points when there are any, and track
// when track is not nil.
func VerdictBody(verdict string, points []Point, track *Track) ([]byte, error) {
	body, err := encode(struct {
		Verdict string  `json:"verdict"`
		Points  []Point `json:"points,omitempty"`
		Track   *Track  `json:"track,omitempty"`
	}{verdict, points, track})
	if err != nil {
		return nil, fmt.Errorf("Check the verdict's points and track. Encoding the request failed: %w", err)
	}

	return body, nil
}

// The limits of one pushed-commits call: MaxPushedCommits repositories, and a body of
// MaxPushedCommitsBytes bytes. The gateway returns 413 `payload_too_large` for a larger
// body, and the step fails.
const (
	MaxPushedCommits      = 64
	MaxPushedCommitsBytes = 64 * 1024
)

// PushedCommit is a repository that a push moved, and the commit its branch is at now: the
// full id, 40 or 64 lowercase hexadecimal characters.
type PushedCommit struct {
	System string `json:"system"`
	Path   string `json:"path"`
	Commit string `json:"commit"`
}

// PushedCommitsBody is the body of one pushed-commits call, the report of the repositories
// that a push moved, in the order the push moved them, as POST pushed-commits of the run
// API reads it. The call goes over the link of the run's gateway, not through this client.
// Qory Apiary takes a repeat of a commit it has as a no-op, so a caller may send the same
// body again. A list with no repository, with more than [MaxPushedCommits], or with a commit
// that is not a full lowercase commit id returns an [Error] with the code
// [CodeInvalidRequest]. PushedCommitsBody does not check the size of the body: split a long
// list with [SplitPushedCommits] first, one body for each list.
func PushedCommitsBody(commits []PushedCommit) ([]byte, error) {
	if len(commits) == 0 || len(commits) > MaxPushedCommits {
		return nil, &Error{
			Code: CodeInvalidRequest, Names: []string{"repositories"},
			Message: fmt.Sprintf("Report 1 to %d repositories in one call. This call has %d.", MaxPushedCommits, len(commits)),
		}
	}

	for i, commit := range commits {
		if !fullCommit(commit.Commit) {
			return nil, &Error{
				Code: CodeInvalidRequest, Names: []string{fmt.Sprintf("repositories[%d].commit", i)},
				Message: "Send each commit as a full id of 40 or 64 lowercase hex characters.",
			}
		}
	}

	body, err := encodePushedCommits(commits)
	if err != nil {
		return nil, fmt.Errorf("Check the pushed commits. Encoding the request failed: %w", err)
	}

	return body, nil
}

// encodePushedCommits is the body of a pushed-commits call for commits. It is the one
// place that encodes the body, so the size that [SplitPushedCommits] measures is the size sent.
func encodePushedCommits(commits []PushedCommit) ([]byte, error) {
	return encode(struct {
		Repositories []PushedCommit `json:"repositories"`
	}{commits})
}

// SplitPushedCommits divides commits into the lists that [PushedCommitsBody] encodes, one call
// each, in order: each list has at most [MaxPushedCommits] repositories and encodes to a body
// of at most [MaxPushedCommitsBytes] bytes. A repository that alone exceeds the byte limit has
// a list to itself. It returns nil for no commits.
func SplitPushedCommits(commits []PushedCommit) [][]PushedCommit {
	var calls [][]PushedCommit

	for len(commits) > 0 {
		n := 1

		for n < len(commits) && n < MaxPushedCommits {
			body, err := encodePushedCommits(commits[:n+1])
			if err != nil || len(body) > MaxPushedCommitsBytes {
				break
			}

			n++
		}

		calls = append(calls, commits[:n:n])
		commits = commits[n:]
	}

	return calls
}

// fullCommit reports whether value is a commit id of 40 or 64 lowercase hexadecimal characters.
func fullCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}

	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}

	return true
}

// storedAnswer is the answer to a push or a verdict: the version that it stored.
type storedAnswer struct {
	Artifact Pushed `json:"artifact,case:strict"`
}

func (c *Client) post(ctx context.Context, path string, body []byte) (*Pushed, error) {
	var stored storedAnswer

	if _, err := c.exchange(ctx, http.MethodPost, path, body, http.StatusCreated, &stored); err != nil {
		return nil, err
	}

	return &stored.Artifact, nil
}

// answerHead is the member of every answer that qory reads first: the version of the run
// API.
type answerHead struct {
	Version *int `json:"version,case:strict"`
}

// exchange sends a request and decodes a response with the status want into target. It
// returns the response's body.
func (c *Client) exchange(
	ctx context.Context, method, path string, body []byte, want int, target any,
) (json.RawMessage, error) {
	request := method + " " + path

	response, err := c.send(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != want {
		return nil, readError(response, request)
	}

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("Try again. Reading the response to %s failed: %w", request, err)
	}

	if len(raw) > maxResponse {
		return nil, fmt.Errorf("Check the run's gateway. The response to %s is over %d bytes.", request, maxResponse)
	}

	var head answerHead

	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("Check the run's gateway. The response to %s is not the run API's JSON: %w", request, err)
	}

	if head.Version == nil {
		return nil, fmt.Errorf("Check the run's gateway. The response to %s has no version.", request)
	}

	if *head.Version != wireVersion {
		return nil, fmt.Errorf("Update qory. This qory reads version %d of the run API, and %s returned version %d.",
			wireVersion, request, *head.Version)
	}

	if err := json.Unmarshal(raw, target); err != nil {
		return nil, fmt.Errorf("Check the run's gateway. The response to %s is not the run API's JSON: %w", request, err)
	}

	return raw, nil
}

// send sends a request, with a JSON body when body is not nil. Only the caller's
// context stops it; the *http.Client sets any other time limit.
func (c *Client) send(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return nil, fmt.Errorf("Check the run API's base URL. Making the request %s %s failed: %w", method, path, err)
	}

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Run the command again. %s %s stopped: %w", method, path, ctx.Err())
		}

		return nil, fmt.Errorf("Check that the run's gateway is reachable at %s. %s %s failed: %w",
			c.base, method, path, err)
	}

	return response, nil
}

// pathSegment reports whether value can be one segment of a path, once escaped: not
// empty, no dot segment, no separator and no control character, as the gateway reads one.
func pathSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\`) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

// encode returns value as JSON without encoding/json's escape of `<`, `>` and `&`, which
// makes a body longer and could push a valid verdict over its limit.
func encode(value any) ([]byte, error) {
	var body bytes.Buffer

	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(value); err != nil {
		return nil, err
	}

	return bytes.TrimSuffix(body.Bytes(), []byte("\n")), nil
}

// writeRecorder keeps the error of a write, so that a failed write is told apart from a
// failed read of the response.
type writeRecorder struct {
	w   io.Writer
	err error
}

// Write writes p to the writer and records an error.
func (r *writeRecorder) Write(p []byte) (int, error) {
	n, err := r.w.Write(p)
	if err != nil {
		r.err = err
	}

	return n, err
}
