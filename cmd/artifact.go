package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/qoryai/forager/wall"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/sourcetext"
	"github.com/qoryai/qory/internal/verdictformat"
)

// The commands of a work item: qory work-item show, qory artifact pull and push, and qory
// run verdict. In a run they use the run API through the gateway. Outside a run they use
// the checkout's .qory directory, so a skill runs the same commands on a laptop. Their
// files are in the store, [artifactStore].

// envRunAPI is the variable that a run with a control plane sets to the run's address on
// its gateway. Its absence means that the command runs outside such a run.
const envRunAPI = "QORY_RUN_API"

// runAPITimeout bounds one request, past the gateway's 10 minutes for a file.
const runAPITimeout = 11 * time.Minute

// The files in the checkout's .qory directory.
const (
	workItemFile  = "work-item.json"
	artifactsDir  = "artifacts"
	indexFile     = "index.json"
	verdictFile   = "verdict.json"
	verdictType   = "verdict"
	defaultName   = "main"
	stdinArgument = "-"
)

// tempStoreDir is the store of a run whose checkout qory cannot write, in the writable
// root of the agent's container. A variable for the tests.
var tempStoreDir = preparation.TempStore

// checkoutWritable reports whether a run may write the checkout at a path. A variable
// for the tests, which root runs and a mode does not stop.
var checkoutWritable = canWrite

// runStartFolder returns the folder that the run's container started in. Forager starts
// the container with --workdir at the checkout or a folder in it, and the container's
// first process stays there, so no cd in the agent's shell changes the value. The value
// is empty outside a wall, and on an image with nested Docker, where process 1 runs as
// root and the agent's user cannot read its link; a read run has no nested Docker. A
// process is in a wall when it is the qory at [wall.HelperPath]; elsewhere process 1 is
// the machine's. A variable for the tests.
var runStartFolder = func() string {
	if exe, err := os.Executable(); err != nil || exe != wall.HelperPath {
		return ""
	}

	start, err := os.Readlink("/proc/1/cwd")
	if err != nil {
		return ""
	}

	return start
}

// maxPointsFile is the most read of the file of --points: the 64 KiB of the request's
// body.
const maxPointsFile = 64 << 10

// noWorkItemText is what qory work-item show prints outside a run with no work item file.
const noWorkItemText = "No work item. Write .qory/work-item.json, or pass the task to the skill."

// noWorkItemInRunText is the error of qory artifact push when the run API has no work item
// for the run.
const noWorkItemInRunText = "Start the run from a work item in Qory Apiary: Qory Apiary found no work item for this run."

// noWorkItemError is the error of qory work-item show with no work item. [ExitCode] gives
// it [ExitNoWorkItem].
type noWorkItemError struct{}

// Error returns the line for the person.
func (noWorkItemError) Error() string { return noWorkItemText }

// artifactRefused reports whether err is a run API error for a type, a limit or a
// decision ([decisionRefusedError]), for a push check that did not pass, a refused
// withdrawal ([withdrawRefusedError]), or a refused verdict ([verdictRefusedError]).
// [ExitCode] maps it to [ExitArtifactRefused].
func artifactRefused(err error) bool {
	var refused withdrawRefusedError
	var verdict verdictRefusedError
	if errors.As(err, &refused) || errors.As(err, &verdict) {
		return true
	}

	for _, code := range []string{
		runapi.CodeUnknownType, runapi.CodeTooManyFiles, runapi.CodeTooLarge,
		runapi.CodePayloadTooLarge, runapi.CodePushCheckFailed, runapi.CodeUnknownDecision,
	} {
		if runapi.HasCode(err, code) {
			return true
		}
	}

	return false
}

// runAPIClient returns the client of the run the command runs in, or nil outside a run. The
// default transport reaches the gateway through HTTP_PROXY.
func runAPIClient() (*runapi.Client, error) {
	base := os.Getenv(envRunAPI)
	if base == "" {
		return nil, nil
	}

	return runapi.New(base, &http.Client{Timeout: runAPITimeout})
}

func newWorkItem() *cobra.Command {
	c := &cobra.Command{
		Use:   "work-item",
		Short: "Read the work item of this run",
		Long: `Read the work item that this run works on.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item`,
	}
	c.AddCommand(newWorkItemShow())

	return c
}

func newWorkItemShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the work item as JSON",
		Long: `Print the work item as JSON: the ticket, the stage's name, the round, the stage's inputs and
outputs with each output's check, the newest verdict and points, and notes from people.

In a run, qory reads the work item from Qory Apiary through the gateway. Outside a run,
qory prints .qory/work-item.json, and exits ` + strconv.Itoa(ExitNoWorkItem) + ` when that file does not exist.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item`,
		Example:     `  qory work-item show > work-item.json`,
		Args:        noArgs,
		Annotations: map[string]string{inWall: "true"},
		RunE: func(c *cobra.Command, _ []string) error {
			client, err := runAPIClient()
			if err != nil {
				return err
			}

			if client != nil {
				snapshot, err := client.WorkItem(c.Context())
				if err != nil {
					return err
				}

				shown, err := sourcetext.EscapeJSON(snapshot.Raw)
				if err != nil {
					return fmt.Errorf("Contact support: the work item in the run API's response has a string that is not valid UTF-8 or JSON text, so qory cannot write its hidden characters visibly: %w", err)
				}

				_, err = fmt.Fprintln(c.OutOrStdout(), string(shown))

				return err
			}

			return showLocalWorkItem(c)
		},
	}
}

// showLocalWorkItem prints the checkout's .qory/work-item.json, with each hidden character
// written as `<U+XXXX>` as [sourcetext.EscapeJSON] does, so it prints what a run prints.
func showLocalWorkItem(c *cobra.Command) error {
	root, err := checkoutRoot()
	if err != nil {
		return err
	}

	path := filepath.Join(checkout.QoryDir(root), workItemFile)

	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return noWorkItemError{}
	}

	if err != nil {
		return fmt.Errorf("Check %s. Reading it failed: %w", path, err)
	}

	if !json.Valid(raw) {
		return input(fmt.Errorf("Fix %s. The file is not JSON.", path))
	}

	shown, err := sourcetext.EscapeJSON(bytes.TrimSpace(raw))
	if err != nil {
		return input(fmt.Errorf("Fix %s. It has a string that is not valid UTF-8 or JSON text: %w", path, err))
	}

	_, err = fmt.Fprintln(c.OutOrStdout(), string(shown))

	return err
}

func newArtifact() *cobra.Command {
	c := &cobra.Command{
		Use:   "artifact",
		Short: "Pull and push the artifacts of a work item",
		Long: `Pull the artifacts of this run's work item into the store, and push new versions. The store
is .qory/artifacts, or /tmp/qory-artifacts when the run's checkout is read-only.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item`,
	}
	c.AddCommand(newArtifactPull(), newArtifactPush())

	return c
}

func newArtifactPull() *cobra.Command {
	var version int

	c := &cobra.Command{
		Use:   "pull [<type>]",
		Short: "Fetch a work item's artifacts and print their directory",
		Long: `Fetch the work item's artifacts into the store, under <type>/<name>/v<N>/, one directory per
version with its files, and write index.json there: the types, names, versions, who wrote
each, and approvals. Then print the store's directory. Read the files from that directory:
it is .qory/artifacts, or /tmp/qory-artifacts when the run's checkout is read-only.

With no --version, qory fetches the newest version of each output of the stage, and the
version of each input that its gate approved. For any other artifact, it fetches the
approved version, or the newest when none is approved. The newest version is the newest
that the index does not mark cancelled: a version of a cancelled run counts for no stage.
qory skips a name whose newest such version is withdrawn, and an artifact with no such
version gets no directory. <type> limits the fetch to one type. --version needs <type>,
and fetches that version of each artifact of the type, whether or not it is cancelled.

Outside a run, qory fetches nothing, and says so.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item`,
		Example: `  qory artifact pull                          # every input and output
  qory artifact pull demo-video --version 2   # one version of one type`,
		Args: func(c *cobra.Command, args []string) error {
			return input(cobra.MaximumNArgs(1)(c, args))
		},
		Annotations: map[string]string{inWall: "true"},
		RunE: func(c *cobra.Command, args []string) error {
			if c.Flags().Changed("version") && version < 1 {
				return input(fmt.Errorf("Set --version to a version number from 1. The value is %d.", version))
			}

			if version != 0 && len(args) == 0 {
				return input(errors.New("Name the artifact type: qory artifact pull <type> --version <N>."))
			}

			client, err := runAPIClient()
			if err != nil {
				return err
			}

			if client == nil {
				_, err := fmt.Fprintln(c.OutOrStdout(), "Nothing pulled: outside a Qory Apiary run, qory uses only the local folder .qory/artifacts.")

				return err
			}

			artifactType := ""
			if len(args) == 1 {
				artifactType = args[0]
			}

			return pullArtifacts(c, client, artifactType, version)
		},
	}
	c.Flags().IntVar(&version, "version", 0, "fetch this version number of each artifact of <type>")

	return c
}

// pullArtifacts fetches what [chosenVersion] chooses into the store, then writes the
// index and prints the directory. The agent reads the store, so the index and the files
// have each hidden character written as `<U+XXXX>`; the push check's store keeps the
// run API's bytes.
func pullArtifacts(c *cobra.Command, client *runapi.Client, artifactType string, version int) error {
	var snapshot *runapi.Snapshot

	if version == 0 {
		var err error

		if snapshot, err = client.WorkItem(c.Context()); err != nil {
			return err
		}
	}

	index, err := client.Artifacts(c.Context())
	if err != nil {
		return err
	}

	store, err := openArtifactStore()
	if err != nil {
		return err
	}

	found := false

	for _, artifact := range index.Artifacts {
		if artifactType != "" && artifact.Type != artifactType {
			continue
		}

		chosen := chosenVersion(artifact, snapshot, version)
		if chosen == nil {
			continue
		}

		found = true

		if err := store.fetch(c.Context(), client, artifact, *chosen); err != nil {
			return err
		}
	}

	shown, err := sourcetext.EscapeJSON(index.Raw)
	if err != nil {
		return fmt.Errorf("Contact support: the artifact index in the run API's response has a string that is not valid UTF-8 or JSON text, so qory cannot write its hidden characters visibly: %w", err)
	}

	if err := store.writeIndex(shown); err != nil {
		return err
	}

	if version != 0 && !found {
		return input(fmt.Errorf("Choose a version that %s lists. %s has no version %d.",
			store.shown(filepath.Join(store.dir, indexFile)), exports.Value(artifactType, false), version))
	}

	_, err = fmt.Fprintln(c.OutOrStdout(), store.shown(store.dir)+string(filepath.Separator))

	return err
}

// chosenVersion returns the version of artifact to fetch, nil for none: version when it is
// not 0, the newest for an output of the stage, the version the stage has for an input,
// else its approved version; and for any other artifact the approved version or the
// newest. The newest is the version that [newestNotCancelled] returns. A name whose newest
// version is withdrawn has none. A type that is an output and an input is an output: the
// stage's round works on its newest version.
func chosenVersion(artifact runapi.Artifact, snapshot *runapi.Snapshot, version int) *runapi.Version {
	byNumber := func(number int) *runapi.Version {
		for i := range artifact.Versions {
			if artifact.Versions[i].Number == number {
				return &artifact.Versions[i]
			}
		}

		return nil
	}
	byID := func(id string) *runapi.Version {
		for i := range artifact.Versions {
			if artifact.Versions[i].ID == id {
				return &artifact.Versions[i]
			}
		}

		return nil
	}
	approved := func() *runapi.Version {
		if artifact.ApprovedVersion == nil {
			return nil
		}

		return byID(*artifact.ApprovedVersion)
	}

	if version != 0 {
		return byNumber(version)
	}

	// A name whose newest version is withdrawn is absent: no stage reads it.
	if withdrawn(artifact) {
		return nil
	}

	for _, output := range snapshot.Outputs {
		if output.Type == artifact.Type {
			return newestNotCancelled(artifact)
		}
	}

	isInput := false

	for _, in := range snapshot.Inputs {
		if in.Type != artifact.Type {
			continue
		}

		isInput = true

		if in.Version != nil {
			if chosen := byID(in.Version.ID); chosen != nil {
				return chosen
			}
		}
	}

	if isInput {
		return approved()
	}

	if chosen := approved(); chosen != nil {
		return chosen
	}

	return newestNotCancelled(artifact)
}

// newestNotCancelled returns the version of artifact with the highest number that the
// index does not mark cancelled, nil for none and for a name whose newest such version is
// a withdrawal. A version that a cancelled run made counts for no stage, and a withdrawn
// name counts as absent.
func newestNotCancelled(artifact runapi.Artifact) *runapi.Version {
	if newest := newestStored(artifact); newest != nil && !newest.Withdrawn {
		return newest
	}

	return nil
}

// newestStored returns the version of artifact with the highest number that the index
// does not mark cancelled, nil for none. It may be a withdrawal.
func newestStored(artifact runapi.Artifact) *runapi.Version {
	var newest *runapi.Version

	for i := range artifact.Versions {
		if !artifact.Versions[i].Cancelled && (newest == nil || artifact.Versions[i].Number > newest.Number) {
			newest = &artifact.Versions[i]
		}
	}

	return newest
}

// newestBeforeWithdrawal returns the version of artifact with the highest number that is
// neither cancelled nor a withdrawal, nil for none: for a withdrawn name, the version
// before the withdrawal.
func newestBeforeWithdrawal(artifact runapi.Artifact) *runapi.Version {
	var newest *runapi.Version

	for i := range artifact.Versions {
		version := &artifact.Versions[i]
		if !version.Cancelled && !version.Withdrawn && (newest == nil || version.Number > newest.Number) {
			newest = version
		}
	}

	return newest
}

// withdrawn reports whether the newest version of artifact that the index does not mark
// cancelled is a withdrawal.
func withdrawn(artifact runapi.Artifact) bool {
	newest := newestStored(artifact)

	return newest != nil && newest.Withdrawn
}

func newArtifactPush() *cobra.Command {
	var name, note, decision string

	var withdraw bool

	c := &cobra.Command{
		Use:   "push <type> [<file>...]",
		Short: "Upload files as one new version of an artifact",
		Long: `Upload the files as one new version of the artifact <type>/<name>, keep a copy in the store
under <type>/<name>/v<N>/, and print the version. - reads one file from standard input.
--name separates several artifacts of one type, such as mockups of two screens. In a run,
the types questions and review-comments take only the name main. The store is
.qory/artifacts, or /tmp/qory-artifacts when the run's checkout is read-only.

qory exits ` + strconv.Itoa(ExitArtifactRefused) + ` with the type and the limit for a type that the workspace does not have,
for too many files, and for files over the type's size limit. One version is at most about
24 MiB and 100 files through the run API, whatever the type's limit. In a walled run, qory
also exits ` + strconv.Itoa(ExitArtifactRefused) + ` with the fix line and the findings when the harness's push check for <type>
does not pass.

--withdraw stores a version of <type>/<name> with no files and the mark withdrawn, in place of
uploading files. A gate, a stage input and the work item's pages leave out a name whose
newest version is withdrawn. A later push of files lists the name again.

qory exits ` + strconv.Itoa(ExitArtifactRefused) + ` with a fix line, and sends nothing, for a withdrawal of a name with no version,
a name whose newest version is already a withdrawal, an upload by a person, or a version
that a member approved at a gate, and for files given with --withdraw. A run cannot
withdraw questions, review-comments, verdict or proposal, which Qory Apiary reads itself,
or a type that the process template sets withdrawable: false for. In a run, qory sends
that push, Qory Apiary reads the template's artifact types and refuses it with 422, and
qory exits ` + strconv.Itoa(ExitArtifactRefused) + ` with that refusal. Outside a run, qory refuses those types itself, reads the
template from the factory modules that the checkout exports, and has no approvals to read.

--decision sends the id of the person's note that asked for this version, such as
gd_01jc4t9…, from the notes that qory work-item show prints: a request for changes, a move
or an escalation. Later runs read this version in place of the approved one, and the next
approval by a person lists it. A withdrawal takes --decision too. qory exits ` + strconv.Itoa(ExitArtifactRefused) + ` with a
fix line when the id is not the id of a person's note on the run's work item.

Outside a run, qory keeps the files, or the record of a withdrawal, in .qory/artifacts and
sends nothing. The store keeps no note and no decision.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item`,
		Example: `  qory artifact push screenshot shots/checkout.png --name checkout --note "After the fix."
  qory artifact push walkthrough -               # read one file from standard input
  qory artifact push screenshot shots/checkout.png --name checkout --decision gd_01jc4t9k2m8x5q7w
  qory artifact push demo-script --name scenario-2 --withdraw --note "demo-scenarios v3 removed scenario-2."`,
		Args: func(c *cobra.Command, args []string) error {
			if withdraw {
				// More than the type is a refusal with exit 4, which RunE words.
				return input(cobra.MinimumNArgs(1)(c, args))
			}

			return input(cobra.MinimumNArgs(2)(c, args))
		},
		Annotations: map[string]string{inWall: "true"},
		RunE: func(c *cobra.Command, args []string) error {
			artifactType, files := args[0], args[1:]

			if err := artifactNameError("type", artifactType); err != nil {
				return input(err)
			}

			if name == "" {
				name = defaultName
			} else if err := artifactNameError("--name", name); err != nil {
				return input(err)
			}

			if withdraw {
				return withdrawArtifact(c, runapi.Push{Type: artifactType, Name: name, Note: note, Decision: decision, Withdraw: true}, files)
			}

			sources, err := readPushSources(c, files)
			if err != nil {
				return err
			}

			client, err := runAPIClient()
			if err != nil {
				return err
			}

			if client == nil {
				return keepPush(c, artifactType, name, sources)
			}

			return sendPush(c, client, runapi.Push{Type: artifactType, Name: name, Note: note, Decision: decision}, sources)
		},
	}
	c.Flags().StringVar(&name, "name", "", "the artifact's name, for several artifacts of one type (default main; in a run, only main for questions and review-comments)")
	c.Flags().StringVar(&note, "note", "", "a note on this version, for a person to read")
	c.Flags().StringVar(&decision, "decision", "", "the id of the person's note that asked for this version, from qory work-item show")
	c.Flags().BoolVar(&withdraw, "withdraw", false, "store a version of <type>/<name> with no files, which withdraws the name")

	return c
}

// pushSource is one file of a push: a file on disk at path, or the bytes of standard input.
type pushSource struct {
	filename string
	path     string
	size     int64
	content  []byte
}

// readPushSources checks the arguments of a push: one - at most, no directory, no two
// files with one name. It reads standard input, at most one byte past the largest version.
func readPushSources(c *cobra.Command, files []string) ([]pushSource, error) {
	sources := make([]pushSource, 0, len(files))
	seen := map[string]bool{}

	for _, file := range files {
		var source pushSource

		if file == stdinArgument {
			if seen[stdinArgument] {
				return nil, input(errors.New("Pass - once: standard input is one file."))
			}

			seen[stdinArgument] = true

			content, err := io.ReadAll(io.LimitReader(c.InOrStdin(), runapi.MaxBody+1))
			if err != nil {
				return nil, fmt.Errorf("Check standard input. Reading it failed: %w", err)
			}

			source = pushSource{filename: stdinFilename(content), size: int64(len(content)), content: content}
		} else {
			info, err := os.Stat(file)
			if err != nil {
				return nil, input(fmt.Errorf("Pass a file that exists. %w", exports.HideValueError(err, file)))
			}

			if !info.Mode().IsRegular() {
				return nil, input(fmt.Errorf("Pass a file, not a directory or a device. %s is neither a file nor a link to one.", exports.Value(file, false)))
			}

			source = pushSource{filename: filepath.Base(file), path: file, size: info.Size()}
		}

		if seen["name:"+source.filename] {
			return nil, input(fmt.Errorf("Rename one of the files named %s. The files of one version have different names.", exports.Value(source.filename, false)))
		}

		seen["name:"+source.filename] = true
		sources = append(sources, source)
	}

	return sources, nil
}

// stdinFilename returns the name of a file read from standard input, with the extension
// of its content, because the run API checks a file's format by its name and bytes.
func stdinFilename(content []byte) string {
	kind := contentType(content)

	switch {
	case json.Valid(content):
		return "stdin.json"
	case strings.HasPrefix(kind, "text/"):
		return "stdin.md"
	}

	extensions := map[string]string{
		"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp",
		"video/mp4": ".mp4", "video/webm": ".webm",
	}

	return "stdin" + extensions[kind]
}

// contentType returns the plain type/subtype of content, which the run API reads; it
// stores the type that the bytes have.
func contentType(content []byte) string {
	kind, _, _ := strings.Cut(http.DetectContentType(content), ";")

	return strings.TrimSpace(kind)
}

// copyTo writes the content of source to w.
func (s pushSource) copyTo(w io.Writer) error {
	if s.path == "" {
		_, err := w.Write(s.content)

		return err
	}

	file, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("Check %s. Reading it failed: %w", exports.Value(s.path, false), exports.HideValueError(err, s.path))
	}
	defer file.Close()

	_, err = io.Copy(w, file)

	return err
}

// bytesOf returns the content of source.
func (s pushSource) bytesOf() ([]byte, error) {
	if s.path == "" {
		return s.content, nil
	}

	content, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("Check %s. Reading it failed: %w", exports.Value(s.path, false), exports.HideValueError(err, s.path))
	}

	return content, nil
}

// sendPush sends the version, keeps a copy of its files, and prints the version. A store
// that cannot be opened sends nothing. A copy that fails after the send prints a line on
// standard error: the version is stored, so the push worked.
func sendPush(c *cobra.Command, client *runapi.Client, push runapi.Push, sources []pushSource) error {
	sizes := make([]int64, len(sources))
	for i, source := range sources {
		sizes[i] = source.size
	}

	if err := runapi.CheckVersion(push.Type, sizes); err != nil {
		return err
	}

	store, err := openArtifactStore()
	if err != nil {
		return err
	}

	for _, source := range sources {
		content, err := source.bytesOf()
		if err != nil {
			return err
		}

		push.Files = append(push.Files, runapi.PushFile{
			Filename: source.filename, ContentType: contentType(content), Content: content,
		})
	}

	pushed, err := postVersion(c.Context(), client, push)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(c.OutOrStdout(), "Pushed %s/%s version %d.\n", push.Type, push.Name, pushed.Version.Number); err != nil {
		return err
	}

	// The type and name are the checked ones of the command line, never the response's.
	if _, err := store.keep(push.Type, push.Name, pushed.Version.Number, sources); err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "Check %s: the version is stored in Qory Apiary, and the local copy failed: %v\n", store.shown(store.dir), err)
	}

	return nil
}

// postVersion sends push to the run API. A run API with no work item for the run is
// [noWorkItemInRunText], and a 422 unknown_decision is a [decisionRefusedError].
func postVersion(ctx context.Context, client *runapi.Client, push runapi.Push) (*runapi.Pushed, error) {
	pushed, err := client.PushArtifact(ctx, push)
	// A not_found with a message comes from the gateway's route, and never reached Qory Apiary.
	var apiError *runapi.Error
	if errors.As(err, &apiError) && apiError.Code == runapi.CodeNotFound && apiError.Message == "" {
		return nil, errors.New(noWorkItemInRunText)
	}

	if errors.As(err, &apiError) && apiError.Code == runapi.CodeUnknownDecision {
		return nil, decisionRefusedError{fmt.Sprintf(textDecisionRefused, shownOrShort(push.Decision)), err}
	}

	return pushed, err
}

// textDecisionRefused is the text of a push for which the run API returns 422
// unknown_decision, with the value of --decision. The run API's line names the request's
// member, not the flag, so qory words the text itself.
const textDecisionRefused = "Set --decision to the id of a person's note on this work item, or drop the flag.\n" +
	"  %s is not such an id. qory work-item show prints the open notes with their ids."

// decisionRefusedError is a push for which the run API returns 422 unknown_decision.
// [ExitCode] gives it [ExitArtifactRefused]. It unwraps to the run API's error.
type decisionRefusedError struct {
	text string
	err  error
}

// Error returns the line for the person.
func (e decisionRefusedError) Error() string { return e.text }

// Unwrap returns the run API's error.
func (e decisionRefusedError) Unwrap() error { return e.err }

// keepPush keeps the version in the store, outside a run. Standard input has no limit
// there, but qory reads it only up to [runapi.MaxBody], so longer input stops the push.
func keepPush(c *cobra.Command, artifactType, name string, sources []pushSource) error {
	for _, source := range sources {
		if source.path == "" && source.size > runapi.MaxBody {
			return input(fmt.Errorf("Pass a file in place of -. Standard input is read up to %d bytes, and this input is longer.", runapi.MaxBody))
		}
	}

	store, err := openArtifactStore()
	if err != nil {
		return err
	}

	number, err := store.nextVersion(artifactType, name)
	if err != nil {
		return err
	}

	dir, err := store.keep(artifactType, name, number, sources)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(c.OutOrStdout(), "Kept in %s%c; not sent: outside a Qory Apiary run, qory keeps versions only in .qory/artifacts.\n",
		store.shown(dir), filepath.Separator)

	return err
}

func newRunVerdict() *cobra.Command {
	var pointsFile string

	c := &cobra.Command{
		Use:   "verdict passed|changes-requested|escalate",
		Short: "Store the run's verdict",
		Long: `Store the run's verdict as a version of the artifact verdict. Qory Apiary acts on it when the
run ends.

escalate needs --points, a file with a JSON array of 1 to 20 points for a person. Each
point is {text, action}: text is one sentence on one line, 1 to 500 characters, on what
is wrong or missing, with no action, and action is answer or run_again. A point with the
action answer also has question, the id of an open question. A point with the action
run_again may have fix, one line of 1 to 500 characters on how a person fixes the
point. qory checks the file against contracts/harness/v1/verdict.schema.json before it
sends or keeps anything.

Any verdict may propose a track for the work item. Pass --track with the id of a track that
the gate of the run's stage offers, such as small-change, and --track-reason with the
reason, one sentence on one line of 1 to 300 characters. Pass both or neither.
qory checks the two before it sends or keeps anything. It exits ` + strconv.Itoa(ExitInput) + ` for a mistake in
the id, the reason or the pair. It exits ` + strconv.Itoa(ExitArtifactRefused) + ` when Qory Apiary refuses the verdict
with 422 invalid_artifact, which includes a track that the gate does not offer. Line 1 of
the error is the fix, and Qory Apiary's text follows.

Outside a run, qory prints the verdict and keeps it in .qory/artifacts/verdict/; nothing
acts on it.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item`,
		Example: `  qory run verdict passed
  qory run verdict passed --track small-change --track-reason "The change touches one file."
  qory run verdict escalate --points points.json`,
		Args: func(c *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(c, args); err != nil {
				return input(err)
			}

			if !slices.Contains([]string{runapi.VerdictPassed, runapi.VerdictChangesRequested, runapi.VerdictEscalate}, args[0]) {
				return input(fmt.Errorf("Pass passed, changes-requested or escalate. The verdict is %s.", exports.Value(args[0], true)))
			}

			return nil
		},
		Annotations: map[string]string{inWall: "true"},
		RunE: func(c *cobra.Command, args []string) error {
			track, err := readTrack(c)
			if err != nil {
				return err
			}

			points, err := readPoints(args[0], pointsFile, c.Flags().Changed("points"))
			if err != nil {
				return err
			}

			client, err := runAPIClient()
			if err != nil {
				return err
			}

			if client == nil {
				return keepVerdict(c, args[0], points, track)
			}

			pushed, err := client.PushVerdict(c.Context(), args[0], points, track)
			if err != nil {
				return verdictError(err)
			}

			_, err = fmt.Fprintf(c.OutOrStdout(), "Stored the verdict %s as version %d.\n", args[0], pushed.Version.Number)

			return err
		},
	}
	c.Flags().StringVar(&pointsFile, "points", "", "read the points of escalate, a JSON array, from this file")
	c.Flags().String("track", "", "propose the track with this id, one that the gate of the run's stage offers; pass it with --track-reason")
	c.Flags().String("track-reason", "", "give the reason for --track, one sentence on one line of 1 to 300 characters; pass it with --track")

	return c
}

// Line 1 of a refused verdict when the run API's line 1 is over [maxFirstLine] characters
// or, for a track, has no flag.
const (
	textTrackRefused   = "Set --track to a track that the gate of the stage offers, or leave out the track flags."
	textVerdictRefused = "Fix the refused part of the verdict, then run the command again."
)

// verdictRefusedError is a verdict that the run API refused with 422 invalid_artifact.
// [ExitCode] gives it [ExitArtifactRefused]. It unwraps to the run API's error.
type verdictRefusedError struct {
	text string
	err  error
}

// Error returns the line for the person.
func (e verdictRefusedError) Error() string { return e.text }

// Unwrap returns the run API's error.
func (e verdictRefusedError) Unwrap() error { return e.err }

// maxFirstLine is the most characters of line 1 of an error: the 2 that qory prints before
// it ("✗ ") keep the line within 100.
const maxFirstLine = 98

// isTrackMember reports whether name is the member track of the verdict or one of its
// members, such as track.reason. The name tracks is not.
func isTrackMember(name string) bool { return name == "track" || strings.HasPrefix(name, "track.") }

// verdictError returns a 422 invalid_artifact as a [verdictRefusedError] with a line 1 of
// at most 98 characters and the fix, then the run API's text on line 2. Another error
// stays as it is.
func verdictError(err error) error {
	var apiError *runapi.Error
	if !errors.As(err, &apiError) || apiError.Code != runapi.CodeInvalidArtifact {
		return err
	}

	text := apiError.Error()
	first, _, _ := strings.Cut(text, "\n")

	switch {
	case slices.ContainsFunc(apiError.Names, isTrackMember):
		first = textTrackRefused
	case utf8.RuneCountInString(first) > maxFirstLine:
		first = textVerdictRefused
	default:
		return verdictRefusedError{text, err}
	}

	return verdictRefusedError{first + "\n  " + strings.ReplaceAll(text, "\n", "\n  "), err}
}

// readTrack returns the track of the flags of c, or nil for none. A wrong id, reason or pair
// is an input error (exit 2).
func readTrack(c *cobra.Command) (*runapi.Track, error) {
	flags := c.Flags()
	id, _ := flags.GetString("track")
	reason, _ := flags.GetString("track-reason")
	idGiven, reasonGiven := flags.Changed("track"), flags.Changed("track-reason")

	switch {
	case !idGiven && !reasonGiven:
		return nil, nil
	case !reasonGiven:
		return nil, input(errors.New("Pass --track-reason <text> with --track: a track needs the reason for it."))
	case !idGiven:
		return nil, input(errors.New("Pass --track <id> with --track-reason: a reason needs the track it is for."))
	case !verdictformat.ValidTrackID(id):
		return nil, input(fmt.Errorf("Set --track to lower-case words joined by hyphens, such as small-change, at most %d characters.\n"+
			"  The value is %s.", verdictformat.MaxTrackID, shownOrShort(id)))
	}

	if err := checkLine("--track-reason", "reason", "one sentence on one line", reason, verdictformat.MaxReason); err != nil {
		return nil, input(err)
	}

	track := &runapi.Track{ID: id, Reason: reason}
	if err := verdictformat.CheckTrack(*track); err != nil {
		return nil, input(err)
	}

	return track, nil
}

// checkLine checks the value of flag, which the message calls what and describes as want:
// 1 to limit characters on one line, with none that the verdict format excludes.
func checkLine(flag, what, want, value string, limit int) error {
	length := utf8.RuneCountInString(value)

	switch {
	case length == 0:
		return fmt.Errorf("Set %s to %s, 1 to %d characters. The %s is empty.", flag, want, limit, what)
	case length > limit:
		return fmt.Errorf("Shorten %s to at most %d characters. The %s has %d.", flag, limit, what, length)
	}

	for _, r := range value {
		switch {
		case r == '\t':
			return fmt.Errorf("Write %s on one line. The %s has a tab (%U).", flag, what, r)
		case r == '\n' || r == '\r':
			return fmt.Errorf("Write %s on one line. The %s has a line break (%U).", flag, what, r)
		case verdictformat.Excluded(r):
			return fmt.Errorf("Remove the character %U from %s. The verdict format excludes it.", r, flag)
		}
	}

	return nil
}

// shortValue returns value quoted, cut to 40 characters.
func shortValue(value string) string {
	if runes := []rune(value); len(runes) > 40 {
		return strconv.Quote(string(runes[:40])) + " and more"
	}

	return strconv.Quote(value)
}

// shownOrShort is value as an error prints it: the phrase [exports.Value] prints for a
// value that contains a key or a code, such as qrk_, and [shortValue] for any other.
func shownOrShort(value string) string {
	if shown := exports.Value(value, false); shown != value {
		return shown
	}

	return shortValue(value)
}

// readPoints returns the points of verdict from path, the file of --points; given is true
// when the command line has --points. escalate needs the file, and the other verdicts
// take none.
func readPoints(verdict, path string, given bool) ([]runapi.Point, error) {
	if verdict != runapi.VerdictEscalate {
		if given {
			return nil, input(fmt.Errorf("Leave out --points with %s: only escalate has points.", verdict))
		}

		return nil, nil
	}

	if !given {
		return nil, input(errors.New("Pass --points <file> with escalate: a JSON array of the points a person acts on."))
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, input(fmt.Errorf("Pass a file that exists to --points. %w", exports.HideValueError(err, path)))
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxPointsFile+1))
	if err != nil {
		return nil, input(fmt.Errorf("Check the file of --points. Reading it failed: %w", exports.HideValueError(err, path)))
	}

	if len(data) > maxPointsFile {
		return nil, input(fmt.Errorf("Shorten %s to at most %d bytes. The file is larger.", exports.Value(path, false), maxPointsFile))
	}

	points, err := verdictformat.Points(data)
	switch {
	case errors.Is(err, verdictformat.ErrByteOrderMark):
		return nil, input(fmt.Errorf("Save %s without a byte order mark.", exports.Value(path, false)))
	case errors.Is(err, verdictformat.ErrNotUTF8):
		return nil, input(fmt.Errorf("Save %s as UTF-8.", exports.Value(path, false)))
	case errors.Is(err, verdictformat.ErrLoneSurrogate):
		return nil, input(fmt.Errorf("Remove the escape of a lone surrogate, such as \\ud800, from %s.", exports.Value(path, false)))
	case err != nil:
		return nil, input(err)
	}

	return points, nil
}

// keepVerdict keeps the verdict in the store as the run API stores it, outside a run.
func keepVerdict(c *cobra.Command, verdict string, points []runapi.Point, track *runapi.Track) error {
	store, err := openArtifactStore()
	if err != nil {
		return err
	}

	number, err := store.nextVersion(verdictType, defaultName)
	if err != nil {
		return err
	}

	content, err := runapi.VerdictBody(verdict, points, track)
	if err != nil {
		return err
	}

	dir, err := store.keep(verdictType, defaultName, number, []pushSource{{filename: verdictFile, content: content}})
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(c.OutOrStdout(), "Verdict %s, kept in %s%c; nothing acts on it outside a run.\n",
		verdict, store.shown(dir), filepath.Separator)

	return err
}

// artifactNameError returns the error of value, an artifact's type or name, nil when it
// has the run API's form. The form also makes value safe as a directory name.
func artifactNameError(what, value string) error {
	if !runapi.ArtifactName(value) {
		return fmt.Errorf("Set the %s to lowercase letters and digits in groups joined by single hyphens, at most 64 bytes. The value is %s.", what, exports.Value(value, true))
	}

	return nil
}

// artifactStore is the checkout's .qory/artifacts directory. Its .gitignore keeps it out of
// git: a run's checkout has no exclude line for .qory, and a later commit of the run must
// not take the artifacts in. In a run whose checkout qory cannot write, it is
// [tempStoreDir], with no .gitignore.
type artifactStore struct {
	root string
	dir  string
	// fresh marks the store of a push check, a new folder: [artifactStore.fetch] creates
	// each file new, and a file that is there already is an error.
	fresh bool
}

// checkoutRoot returns the root of the checkout that the working directory is in.
func checkoutRoot() (string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("Change to a folder that exists. Reading the working directory failed: %w", err)
	}

	return checkout.Root(workingDir)
}

// storeRoot returns the checkout of the store. In a wall it is the checkout of
// [runStartFolder], so each command of the run finds one store from any folder. On an
// image with nested Docker, and with no wall, it is the checkout that the working
// directory is in.
func storeRoot() (string, error) {
	if os.Getenv(envRunAPI) != "" {
		if start := runStartFolder(); start != "" {
			return checkout.Root(start)
		}
	}

	return checkoutRoot()
}

// openArtifactStore returns the store of the checkout of [storeRoot], or in a run whose
// checkout qory cannot write, as a read-only mount stops every write, the store at
// [tempStoreDir]. Outside a run a checkout that qory cannot write fails here.
func openArtifactStore() (*artifactStore, error) {
	root, err := storeRoot()
	if err != nil {
		return nil, err
	}

	if os.Getenv(envRunAPI) != "" && !checkoutWritable(root) {
		return openTempStore()
	}

	store := &artifactStore{root: root, dir: filepath.Join(checkout.QoryDir(root), artifactsDir)}

	if _, err := store.realDir(); err != nil {
		return nil, err
	}

	if err := store.write(store.dir, ".gitignore", func(w io.Writer) error {
		_, err := io.WriteString(w, "*\n")

		return err
	}); err != nil {
		return nil, err
	}

	return store, nil
}

// openTempStore returns the store at [tempStoreDir], made by [preparation.FreshHome] or
// here. A folder that another user owns or can open stops the command, because a run's
// artifact files must reach no other user on a host that shares /tmp.
func openTempStore() (*artifactStore, error) {
	store := &artifactStore{root: filepath.Dir(tempStoreDir), dir: tempStoreDir}

	if err := os.Mkdir(store.dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("Check %s. Making the directory failed: %w", store.dir, err)
	}

	if _, err := store.realDir(); err != nil {
		return nil, err
	}

	info, err := os.Lstat(store.dir)
	if err != nil {
		return nil, fmt.Errorf("Check %s. Reading it failed: %w", store.dir, err)
	}

	if owner, user := folderOwner(info); owner != user || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("Remove %s, then run the command again. qory keeps a run's artifact files only in a folder "+
			"that this user owns and no other user can open. The folder's owner is uid %d, this user is uid %d, and its mode is %o.",
			store.dir, owner, user, info.Mode().Perm())
	}

	return store, nil
}

// write writes the file name in dir, a directory of [artifactStore.realDir], through a
// temporary file. So a failed write leaves no file under name, and a link at name is
// replaced, never followed. An error of fill comes back as it is.
func (s *artifactStore) write(dir, name string, fill func(io.Writer) error) error {
	temp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return fmt.Errorf("Check %s. Making a temporary file failed: %w", dir, err)
	}
	defer os.Remove(temp.Name())

	err = fill(temp)
	if closed := temp.Close(); err == nil && closed != nil {
		err = fmt.Errorf("Check %s. Writing it failed: %w", filepath.Join(dir, name), closed)
	}

	if err != nil {
		return err
	}

	if err := os.Chmod(temp.Name(), 0o644); err != nil {
		return fmt.Errorf("Check %s. Setting its mode failed: %w", filepath.Join(dir, name), err)
	}

	if err := os.Rename(temp.Name(), filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("Check %s. Writing it failed: %w", filepath.Join(dir, name), err)
	}

	return nil
}

// realDir makes the directory parts under the store, and returns it. It stops at a link
// from the root down: a branch could place one to send qory's writes outside the checkout.
func (s *artifactStore) realDir(parts ...string) (string, error) {
	relative, err := filepath.Rel(s.root, s.dir)
	if err != nil {
		return "", err
	}

	at := s.root

	for _, part := range append(strings.Split(relative, string(filepath.Separator)), parts...) {
		at = filepath.Join(at, part)

		if err := os.Mkdir(at, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("Check %s. Making the directory failed: %w", at, err)
		}

		info, err := os.Lstat(at)
		if err != nil {
			return "", fmt.Errorf("Check %s. Reading it failed: %w", at, err)
		}

		if !info.IsDir() {
			return "", fmt.Errorf("Remove %s: it must be a directory, not a link or a file.", at)
		}
	}

	return at, nil
}

// shown returns path for a line of output: in a run the whole path, because Claude Code
// can reset the shell to another folder between two commands, and elsewhere relative to
// the working directory.
func (s *artifactStore) shown(path string) string {
	if os.Getenv(envRunAPI) != "" {
		return path
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return path
	}

	relative, err := filepath.Rel(workingDir, path)
	if err != nil {
		return path
	}

	return relative
}

// nextVersion returns the number after the newest version directory of type and name.
func (s *artifactStore) nextVersion(artifactType, name string) (int, error) {
	dir, err := s.realDir(artifactType, name)
	if err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("Check %s. Reading it failed: %w", dir, err)
	}

	newest := 0

	for _, entry := range entries {
		number, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "v"))
		if err == nil && strings.HasPrefix(entry.Name(), "v") && number > newest {
			newest = number
		}
	}

	return newest + 1, nil
}

// versionDir makes the directory of one version and returns it.
func (s *artifactStore) versionDir(artifactType, name string, number int) (string, error) {
	if number < 1 {
		return "", fmt.Errorf("Check the version of %s/%s. %d is no version number.", artifactType, name, number)
	}

	return s.realDir(artifactType, name, "v"+strconv.Itoa(number))
}

// keep writes sources into the directory of one version and returns the directory.
func (s *artifactStore) keep(artifactType, name string, number int, sources []pushSource) (string, error) {
	dir, err := s.versionDir(artifactType, name, number)
	if err != nil {
		return "", err
	}

	for _, source := range sources {
		if err := s.write(dir, source.filename, source.copyTo); err != nil {
			return "", err
		}
	}

	return dir, nil
}

// fetch downloads the files of version into its directory.
//
// In a fresh store, the store of a push check, each file keeps the run API's bytes and
// name, and a file that is there is an error: on a file system that ignores case or the
// form of an accent, two files of the version whose names differ in those alone are one
// file.
//
// In any other store, the agent reads the files, so each name and each text file has every
// hidden character written as `<U+XXXX>` ([escapedFile], [sourcetext.Escape]). A file
// that is there with the index's digest stays; an escaped file has another digest, so qory
// downloads it again on each pull. Two files of the version whose names are the same once
// escaped are an error.
func (s *artifactStore) fetch(ctx context.Context, client *runapi.Client, artifact runapi.Artifact, version runapi.Version) error {
	for what, value := range map[string]string{"type": artifact.Type, "name": artifact.Name} {
		if err := artifactNameError(what, value); err != nil {
			return fmt.Errorf("Check the artifact index of this run. %w", err)
		}
	}

	dir, err := s.versionDir(artifact.Type, artifact.Name, version.Number)
	if err != nil {
		return err
	}

	named := map[string]string{} // escaped name to the filename in the index

	for i, file := range version.Files {
		if !runapi.LocalFilename(file.Filename) {
			return fmt.Errorf("Check the artifact index of this run. %q cannot be a file's name in a directory.", file.Filename)
		}

		path := filepath.Join(dir, file.Filename)
		if s.fresh {
			err := writeFresh(path, func(w io.Writer) error {
				return client.Download(ctx, version.ID, file, w)
			})
			if errors.Is(err, fs.ErrExist) {
				return oneFileError(dir, version, i)
			}

			if err != nil {
				return err
			}

			continue
		}

		name := sourcetext.Escape(file.Filename)
		if !runapi.LocalFilename(name) {
			return fmt.Errorf("Check the artifact index of this run. %q cannot be a file's name in a directory, once its hidden characters are written as <U+XXXX>.", file.Filename)
		}

		if other, ok := named[name]; ok && other != file.Filename {
			return fmt.Errorf("Check the artifact index of this run. The files %q and %q of the version %s have the same name, %q, once their hidden characters are written as <U+XXXX>.", other, file.Filename, version.ID, name)
		}

		named[name] = file.Filename

		if sameFile(filepath.Join(dir, name), file) {
			continue
		}

		// shortcut: the file is held in memory, and qory sets no limit on a download but
		// the size in the index; replace it with a stream if files of more than 24 MiB
		// (the most of a push) are pulled.
		var raw bytes.Buffer
		if err := client.Download(ctx, version.ID, file, &raw); err != nil {
			return err
		}

		content := escapedFile(file.Filename, raw.Bytes())
		if err := s.write(dir, name, func(w io.Writer) error {
			_, err := w.Write(content)

			return err
		}); err != nil {
			return err
		}
	}

	return nil
}

// escapedFile returns content, the checked bytes of the file filename, as the agent
// reads it. A filename that ends in .json, in any case, gets [sourcetext.EscapeJSON],
// which keeps the whitespace between tokens. Other content that is valid UTF-8 with no
// NUL byte gets [sourcetext.Escape], and so does a .json file that is not JSON text. Any
// other content is binary, and stays as it is.
func escapedFile(filename string, content []byte) []byte {
	if strings.EqualFold(filepath.Ext(filename), ".json") {
		if escaped, err := sourcetext.EscapeJSON(content); err == nil {
			return escaped
		}
	}

	if utf8.Valid(content) && !bytes.Contains(content, []byte{0}) {
		text := string(content)
		if escaped := sourcetext.Escape(text); escaped != text {
			return []byte(escaped)
		}
	}

	return content
}

// writeFresh writes the new file at path with fill. A file that is there is an error
// that is [fs.ErrExist], and so is one whose name differs from the file's only in case
// or in the form of an accent, on a file system that ignores those. An error of fill
// comes back as it is.
func writeFresh(path string, fill func(io.Writer) error) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("Check %s. Making the file failed: %w", path, err)
	}

	err = fill(file)
	if closed := file.Close(); err == nil && closed != nil {
		err = fmt.Errorf("Check %s. Writing it failed: %w", path, closed)
	}

	return err
}

// oneFileError is the error of the file i of version, which is one file in dir with a
// file of the version before it.
func oneFileError(dir string, version runapi.Version, i int) error {
	name := version.Files[i].Filename
	other := "a file before it"

	if at, err := os.Stat(filepath.Join(dir, name)); err == nil {
		for _, before := range version.Files[:i] {
			if info, err := os.Stat(filepath.Join(dir, before.Filename)); err == nil && os.SameFile(at, info) {
				other = fmt.Sprintf("%q", before.Filename)
			}
		}
	}

	return fmt.Errorf("the files %s and %q of the version %s are one file on this machine, whose file system is insensitive to case or to the form of an accent", other, name, version.ID)
}

// sameFile reports whether the file at path has the size and the digest of file.
func sameFile(path string, file runapi.File) bool {
	content, err := os.Open(path)
	if err != nil {
		return false
	}
	defer content.Close()

	digest := sha256.New()

	size, err := io.Copy(digest, content)

	return err == nil && size == file.Size && strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), file.SHA256)
}

// writeIndex writes raw, the index's bytes, as the caller made them.
func (s *artifactStore) writeIndex(raw []byte) error {
	return s.write(s.dir, indexFile, func(w io.Writer) error {
		_, err := w.Write(raw)

		return err
	})
}
