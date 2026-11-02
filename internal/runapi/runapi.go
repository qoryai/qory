// Package runapi is the client of Qory Apiary's run API, which a run uses to read its
// work item and the work item's artifacts, and to push artifacts and its verdict.
//
// The client sends every request to a base URL, the run's address on its gateway,
// `<gateway>/v1/runs/<run id>`. It holds no key and sends no signature: the gateway adds
// the signature of its access key and forwards the request. The caller gives the base
// URL and the *http.Client; this package reads no environment variable.
package runapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// MaxBody is the largest request body of the run API in bytes, once encoded: about
// 24 MiB of files in one version, whatever the size limit of the artifact type.
const MaxBody = 32 << 20

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

// Client sends a run's requests to the run API.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for baseURL, `<gateway>/v1/runs/<run id>`, that sends its requests
// with client. A base URL with a trailing slash is the same URL.
func New(baseURL string, client *http.Client) (*Client, error) {
	if client == nil {
		return nil, errors.New("Pass an HTTP client to runapi.New. The client is nil.")
	}

	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("Set the run API's base URL to <gateway>/v1/runs/<run id>. It does not parse: %w", err)
	}

	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf(
			"Set the run API's base URL to <gateway>/v1/runs/<run id>, with http or https and no "+
				"user, query or fragment. The value is %s.", u.Redacted())
	}

	return &Client{base: strings.TrimRight(baseURL, "/"), http: client}, nil
}

// VersionRef is one version of an artifact.
type VersionRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
}

// Snapshot is the work item as the run reads it. It has the members that qory acts on;
// Raw is the whole response, as the run API returned it.
type Snapshot struct {
	WorkItem struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
		URL    string `json:"url"`
	} `json:"work_item"`
	// Workflow and Round are nil for a run that has neither.
	Run struct {
		ID       string  `json:"id"`
		Workflow *string `json:"workflow"`
		Round    *int    `json:"round"`
	} `json:"run"`
	// Name, Round, Rounds and State are nil for a work item that is in no stage.
	Stage struct {
		ID     *string `json:"id"`
		Name   *string `json:"name"`
		Round  *int    `json:"round"`
		Rounds *int    `json:"rounds"`
		State  *string `json:"state"`
	} `json:"stage"`
	Inputs  []Input  `json:"inputs"`
	Outputs []Output `json:"outputs"`
	// Verdict is the work item's newest verdict, nil for none.
	Verdict *GivenVerdict `json:"verdict"`
	// Gaps is the stage's gaps, nil for none.
	Gaps *string `json:"gaps"`

	Raw json.RawMessage `json:"-"`
}

// Input is one input of the work item's stage. Version is nil when the input has no
// version yet.
type Input struct {
	Type     string      `json:"type"`
	Required bool        `json:"required"`
	Version  *VersionRef `json:"version"`
}

// Output is one output of the work item's stage, with its newest version, nil for none,
// and its check: `passed`, `missing` or `not_checked`.
type Output struct {
	Type     string      `json:"type"`
	Required bool        `json:"required"`
	Version  *VersionRef `json:"version"`
	Check    string      `json:"check"`
}

// GivenVerdict is a verdict that the work item has.
type GivenVerdict struct {
	Verdict string      `json:"verdict"`
	Gaps    *string     `json:"gaps"`
	Version *VersionRef `json:"version"`
}

// Index is the work item's artifacts with their versions. Raw is the whole response, as
// the run API returned it.
type Index struct {
	Artifacts []Artifact `json:"artifacts"`

	Raw json.RawMessage `json:"-"`
}

// Artifact is one artifact of a work item. ApprovedVersion is the id of the newest
// version that a gate approved, nil for none; NewestVersion is the number of the newest
// version.
type Artifact struct {
	Type                 string    `json:"type"`
	Name                 string    `json:"name"`
	StageID              *string   `json:"stage_id"`
	ApprovedVersion      *string   `json:"approved_version"`
	NewestVersion        int       `json:"newest_version"`
	ChangedAfterApproval bool      `json:"changed_after_approval"`
	Versions             []Version `json:"versions"`
}

// Version is one version of an artifact.
type Version struct {
	ID        string     `json:"id"`
	Number    int        `json:"number"`
	Writer    Writer     `json:"writer"`
	StageID   *string    `json:"stage_id"`
	Note      *string    `json:"note"`
	At        string     `json:"at"`
	Files     []File     `json:"files"`
	Approvals []Approval `json:"approvals"`
}

// Writer is who wrote a version: Kind `run` with RunID, or `person` with Name.
type Writer struct {
	Kind  string `json:"kind"`
	RunID string `json:"run_id,omitempty"`
	Name  string `json:"name,omitempty"`
}

// File is one file of a version, as the index lists it. Filename comes from the server:
// check it before using it as a path.
type File struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

// Approval is a gate's approval of a version.
type Approval struct {
	By struct {
		Name string `json:"name"`
	} `json:"by"`
	StageID *string `json:"stage_id"`
	At      string  `json:"at"`
}

// Push is the body of a new version of an artifact. Name and Note are left out of the
// request when they are empty; the run API uses the name `main`.
type Push struct {
	Type  string     `json:"type"`
	Name  string     `json:"name,omitempty"`
	Note  string     `json:"note,omitempty"`
	Files []PushFile `json:"files"`
}

// PushFile is one file of a [Push]. encoding/json writes Content as standard base64 with
// padding, the form that the run API reads.
type PushFile struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Content     []byte `json:"content_base64"`
}

// Pushed is the version that a push or a verdict stored.
type Pushed struct {
	Type    string     `json:"type"`
	Name    string     `json:"name"`
	Version VersionRef `json:"version"`
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
// and checks them against the size and the SHA-256 in the index. On an error, w may hold
// part of the file or a file that fails the check: discard what it holds.
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

	size, err := io.Copy(io.MultiWriter(w, digest), io.LimitReader(response.Body, file.Size+1))
	if err != nil {
		return fmt.Errorf("Pull %s again. %s failed after %d bytes: %w", file.Filename, request, size, err)
	}

	if size != file.Size {
		return fmt.Errorf("Pull %s again. %s returned %d bytes or more, and the index lists %d.",
			file.Filename, request, size, file.Size)
	}

	if sum := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(sum, file.SHA256) {
		return fmt.Errorf("Pull %s again. %s returned bytes with SHA-256 %s, and the index lists %s.",
			file.Filename, request, sum, file.SHA256)
	}

	return nil
}

// PushArtifact stores a new version of an artifact of the run's work item. A version over
// [MaxBody] encoded returns an [Error] with the code [CodePayloadTooLarge] and sends
// nothing.
func (c *Client) PushArtifact(ctx context.Context, push Push) (*Pushed, error) {
	encoded := 0
	for _, file := range push.Files {
		encoded += base64.StdEncoding.EncodedLen(len(file.Content))
	}

	if encoded > MaxBody {
		return nil, versionTooLarge(push.Type, encoded)
	}

	body, err := json.Marshal(push)
	if err != nil {
		return nil, fmt.Errorf("Check the files of the version of %s. Encoding the request failed: %w", push.Type, err)
	}

	if len(body) > MaxBody {
		return nil, versionTooLarge(push.Type, len(body))
	}

	return c.post(ctx, "/artifacts", body)
}

// PushVerdict stores the run's verdict, one of the Verdict constants. Empty gaps are left
// out of the request.
func (c *Client) PushVerdict(ctx context.Context, verdict, gaps string) (*Pushed, error) {
	body, err := json.Marshal(struct {
		Verdict string `json:"verdict"`
		Gaps    string `json:"gaps,omitempty"`
	}{verdict, gaps})
	if err != nil {
		return nil, fmt.Errorf("Check the verdict's text. Encoding the request failed: %w", err)
	}

	return c.post(ctx, "/verdict", body)
}

func (c *Client) post(ctx context.Context, path string, body []byte) (*Pushed, error) {
	var stored struct {
		Artifact Pushed `json:"artifact"`
	}

	if _, err := c.exchange(ctx, http.MethodPost, path, body, http.StatusCreated, &stored); err != nil {
		return nil, err
	}

	return &stored.Artifact, nil
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

	var head struct {
		Version int `json:"version"`
	}

	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, fmt.Errorf("Check the run's gateway. The response to %s is not the run API's JSON: %w", request, err)
	}

	if head.Version != wireVersion {
		return nil, fmt.Errorf("Update qory. This qory reads version %d of the run API, and %s returned version %d.",
			wireVersion, request, head.Version)
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
// empty, no dot segment, no separator and no NUL.
func pathSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}
