package cmd

import (
	"bytes"
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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/runapi"
)

// The commands of a work item: qory work-item show, qory artifact pull and push, and qory
// run verdict. In a run they use the run API through the gateway. Outside a run they use
// the checkout's .qory directory, so a skill runs the same commands on a laptop.

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

// maxGapsFile is the most read of the file of --gaps: the 64 KiB of the request's body.
const maxGapsFile = 64 << 10

// noWorkItemText is what qory work-item show prints outside a run with no work item file.
const noWorkItemText = "No work item. Write .qory/work-item.json, or pass the task to the skill."

// noWorkItemError is the error of qory work-item show with no work item. [ExitCode] gives
// it [ExitNoWorkItem].
type noWorkItemError struct{}

// Error returns the line for the person.
func (noWorkItemError) Error() string { return noWorkItemText }

// artifactRefused reports whether err is the run API's refusal of a type or a limit.
// [ExitCode] gives it [ExitArtifactRefused].
func artifactRefused(err error) bool {
	for _, code := range []string{
		runapi.CodeTypeNotWritable, runapi.CodeUnknownType, runapi.CodeTooManyFiles,
		runapi.CodeTooLarge, runapi.CodePayloadTooLarge,
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
outputs with each output's check, the newest verdict and gaps, answers, and notes from
people.

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

				_, err = fmt.Fprintln(c.OutOrStdout(), string(snapshot.Raw))

				return err
			}

			return showLocalWorkItem(c)
		},
	}
}

// showLocalWorkItem prints the checkout's .qory/work-item.json.
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

	_, err = fmt.Fprintln(c.OutOrStdout(), string(bytes.TrimSpace(raw)))

	return err
}

func newArtifact() *cobra.Command {
	c := &cobra.Command{
		Use:   "artifact",
		Short: "Pull and push the artifacts of a work item",
		Long: `Pull the artifacts of this run's work item into .qory/artifacts, and push new versions.

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
		Short: "Fetch a work item's artifacts into .qory/artifacts",
		Long: `Fetch the work item's artifacts into .qory/artifacts/<type>/<name>/v<N>/, one directory per
version with its files, and write .qory/artifacts/index.json: the types, names, versions,
who wrote each, and approvals. Then print the directory.

With no --version, qory fetches the newest version of each output of the stage, and the
version of each input that its gate approved. For any other artifact, it fetches the
approved version, or the newest when none is approved. <type> limits the fetch to one type.
--version needs <type>, and fetches that version of each artifact of the type.

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
			if version != 0 && len(args) == 0 {
				return input(errors.New("Name the artifact type: qory artifact pull <type> --version <N>."))
			}

			if version < 0 {
				return input(fmt.Errorf("Set --version to a version number from 1. The value is %d.", version))
			}

			client, err := runAPIClient()
			if err != nil {
				return err
			}

			if client == nil {
				_, err := fmt.Fprintln(c.OutOrStdout(), "Not signed in to Qory Apiary; .qory/artifacts/ has the local versions only.")

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
// index and prints the directory.
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

		if err := store.fetch(c, client, artifact, *chosen); err != nil {
			return err
		}
	}

	if version != 0 && !found {
		return input(fmt.Errorf("Choose a version that .qory/artifacts/index.json lists. %s has no version %d.",
			artifactType, version))
	}

	if err := store.writeIndex(index.Raw); err != nil {
		return err
	}

	_, err = fmt.Fprintln(c.OutOrStdout(), store.shown(store.dir)+string(filepath.Separator))

	return err
}

// chosenVersion returns the version of artifact to fetch, nil for none: version when it is
// not 0, the newest for an output of the stage, the version the stage has for an input,
// else its approved version; and for any other artifact the approved version or the
// newest. A type that is an output and an input is an output: the stage's round works on
// its newest version.
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

	for _, output := range snapshot.Outputs {
		if output.Type == artifact.Type {
			return byNumber(artifact.NewestVersion)
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

	return byNumber(artifact.NewestVersion)
}

func newArtifactPush() *cobra.Command {
	var name, note string

	c := &cobra.Command{
		Use:   "push <type> <file>...",
		Short: "Upload files as one new version of an artifact",
		Long: `Upload the files as one new version of the artifact <type>/<name>, keep a copy in
.qory/artifacts/<type>/<name>/v<N>/, and print the version. - reads one file from standard
input. --name separates several artifacts of one type, such as mockups of two screens.

qory exits ` + strconv.Itoa(ExitArtifactRefused) + ` with the type and the limit for a type that the workspace does not have or the
workflow may not write, for too many files, and for files over the type's size limit. One
version is at most about 24 MiB through the run API, whatever the type's limit.

Outside a run, qory keeps the files in .qory/artifacts and sends nothing.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item`,
		Example: `  qory artifact push screenshot shots/checkout.png --name checkout --note "After the fix."
  qory artifact push explainer -                 # read one file from standard input`,
		Args: func(c *cobra.Command, args []string) error {
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

			return sendPush(c, client, runapi.Push{Type: artifactType, Name: name, Note: note}, sources)
		},
	}
	c.Flags().StringVar(&name, "name", "", "the artifact's name, for several artifacts of one type (default main)")
	c.Flags().StringVar(&note, "note", "", "a note on this version, for a person to read")

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
				return nil, input(fmt.Errorf("Pass a file that exists. %w", err))
			}

			if !info.Mode().IsRegular() {
				return nil, input(fmt.Errorf("Pass a file, not a directory or a device. %s is neither a file nor a link to one.", file))
			}

			source = pushSource{filename: filepath.Base(file), path: file, size: info.Size()}
		}

		if seen["name:"+source.filename] {
			return nil, input(fmt.Errorf("Rename one of the files named %s. The files of one version have different names.", source.filename))
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

// bytesOf returns the content of source.
func (s pushSource) bytesOf() ([]byte, error) {
	if s.path == "" {
		return s.content, nil
	}

	content, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("Check %s. Reading it failed: %w", s.path, err)
	}

	return content, nil
}

// sendPush sends the version, keeps a copy of its files, and prints the version. A copy
// that fails prints a line on standard error: the version is stored, so the push worked.
func sendPush(c *cobra.Command, client *runapi.Client, push runapi.Push, sources []pushSource) error {
	sizes := make([]int64, len(sources))
	for i, source := range sources {
		sizes[i] = source.size
	}

	if err := runapi.CheckVersionSize(push.Type, sizes); err != nil {
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

	pushed, err := client.PushArtifact(c.Context(), push)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(c.OutOrStdout(), "Pushed %s/%s version %d.\n", pushed.Type, pushed.Name, pushed.Version.Number); err != nil {
		return err
	}

	store, err := openArtifactStore()
	if err == nil {
		_, err = store.keep(pushed.Type, pushed.Name, pushed.Version.Number, sources)
	}

	if err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "Check .qory/artifacts: the version is stored in Qory Apiary, and the local copy failed: %v\n", err)
	}

	return nil
}

// keepPush keeps the version in the store, outside a run.
func keepPush(c *cobra.Command, artifactType, name string, sources []pushSource) error {
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

	_, err = fmt.Fprintf(c.OutOrStdout(), "Kept in %s%c; not sent, because you are not signed in to Qory Apiary.\n",
		store.shown(dir), filepath.Separator)

	return err
}

func newRunVerdict() *cobra.Command {
	var gapsFile string

	c := &cobra.Command{
		Use:   "verdict passed|changes-requested|escalate",
		Short: "Store the run's verdict",
		Long: `Store the run's verdict as a version of the artifact verdict. Qory Apiary acts on it when the
run ends. --gaps reads the text of what is missing from a file.

Outside a run, qory prints the verdict and keeps it in .qory/artifacts/verdict/; nothing
acts on it.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#commands-of-a-work-item`,
		Example: `  qory run verdict passed
  qory run verdict changes-requested --gaps gaps.md`,
		Args: func(c *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(c, args); err != nil {
				return input(err)
			}

			if !slices.Contains([]string{runapi.VerdictPassed, runapi.VerdictChangesRequested, runapi.VerdictEscalate}, args[0]) {
				return input(fmt.Errorf("Pass passed, changes-requested or escalate. The verdict is %q.", args[0]))
			}

			return nil
		},
		Annotations: map[string]string{inWall: "true"},
		RunE: func(c *cobra.Command, args []string) error {
			gaps, err := readGaps(gapsFile)
			if err != nil {
				return err
			}

			client, err := runAPIClient()
			if err != nil {
				return err
			}

			if client == nil {
				return keepVerdict(c, args[0], gaps)
			}

			pushed, err := client.PushVerdict(c.Context(), args[0], gaps)
			if err != nil {
				return err
			}

			_, err = fmt.Fprintf(c.OutOrStdout(), "Stored the verdict %s as version %d.\n", args[0], pushed.Version.Number)

			return err
		},
	}
	c.Flags().StringVar(&gapsFile, "gaps", "", "read the text of what is missing from this file")

	return c
}

// readGaps returns the text of the file of --gaps, empty for no file.
func readGaps(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	file, err := os.Open(path)
	if err != nil {
		return "", input(fmt.Errorf("Pass a file that exists to --gaps. %w", err))
	}
	defer file.Close()

	gaps, err := io.ReadAll(io.LimitReader(file, maxGapsFile+1))
	if err != nil {
		return "", input(fmt.Errorf("Check the file of --gaps. Reading it failed: %w", err))
	}

	if len(gaps) > maxGapsFile {
		return "", input(fmt.Errorf("Shorten %s to at most 20000 characters. The file is over %d bytes.", path, maxGapsFile))
	}

	return string(gaps), nil
}

// keepVerdict keeps the verdict in the store as the run API stores it, outside a run.
func keepVerdict(c *cobra.Command, verdict, gaps string) error {
	store, err := openArtifactStore()
	if err != nil {
		return err
	}

	number, err := store.nextVersion(verdictType, defaultName)
	if err != nil {
		return err
	}

	content, err := json.Marshal(struct {
		Verdict string `json:"verdict"`
		Gaps    string `json:"gaps,omitempty"`
	}{verdict, gaps})
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

// artifactNamePattern is the form of an artifact type and name in the run API.
var artifactNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// artifactNameError returns the error of value, an artifact's type or name, nil when it
// has the run API's form. The form also makes value safe as a directory name.
func artifactNameError(what, value string) error {
	if len(value) > 64 || !artifactNamePattern.MatchString(value) {
		return fmt.Errorf("Set the %s to lowercase letters and digits in groups joined by single hyphens, at most 64 bytes. The value is %q.", what, value)
	}

	return nil
}

// artifactStore is the checkout's .qory/artifacts directory. Its .gitignore keeps it out of
// git: a run's checkout has no exclude line for .qory, and a later commit of the run must
// not take the artifacts in.
type artifactStore struct {
	root string
	dir  string
}

// checkoutRoot returns the root of the checkout that the working directory is in.
func checkoutRoot() (string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("Change to a folder that exists. Reading the working directory failed: %w", err)
	}

	return checkout.Root(workingDir)
}

// openArtifactStore returns the store of the checkout that the working directory is in.
func openArtifactStore() (*artifactStore, error) {
	root, err := checkoutRoot()
	if err != nil {
		return nil, err
	}

	store := &artifactStore{root: root, dir: filepath.Join(checkout.QoryDir(root), artifactsDir)}

	if _, err := store.realDir(); err != nil {
		return nil, err
	}

	ignore := filepath.Join(store.dir, ".gitignore")
	if err := os.WriteFile(ignore, []byte("*\n"), 0o644); err != nil {
		return nil, fmt.Errorf("Check %s. Writing it failed: %w", ignore, err)
	}

	return store, nil
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

// shown returns path for a line of output: relative to the working directory.
func (s *artifactStore) shown(path string) string {
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
	return s.realDir(artifactType, name, "v"+strconv.Itoa(number))
}

// keep writes sources into the directory of one version and returns the directory.
func (s *artifactStore) keep(artifactType, name string, number int, sources []pushSource) (string, error) {
	dir, err := s.versionDir(artifactType, name, number)
	if err != nil {
		return "", err
	}

	for _, source := range sources {
		content, err := source.bytesOf()
		if err != nil {
			return "", err
		}

		path := filepath.Join(dir, source.filename)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return "", fmt.Errorf("Check %s. Writing it failed: %w", path, err)
		}
	}

	return dir, nil
}

// fetch downloads the files of version into its directory. A file that is there with the
// index's digest stays; a download goes to a temporary file first, so a failed one leaves
// no file under the file's name.
func (s *artifactStore) fetch(c *cobra.Command, client *runapi.Client, artifact runapi.Artifact, version runapi.Version) error {
	for what, value := range map[string]string{"type": artifact.Type, "name": artifact.Name} {
		if err := artifactNameError(what, value); err != nil {
			return fmt.Errorf("Check the artifact index of this run. %w", err)
		}
	}

	dir, err := s.versionDir(artifact.Type, artifact.Name, version.Number)
	if err != nil {
		return err
	}

	for _, file := range version.Files {
		if !localFilename(file.Filename) {
			return fmt.Errorf("Check the artifact index of this run. %q cannot be a file's name in a directory.", file.Filename)
		}

		path := filepath.Join(dir, file.Filename)
		if sameFile(path, file) {
			continue
		}

		temp, err := os.CreateTemp(dir, ".pull-*")
		if err != nil {
			return fmt.Errorf("Check %s. Making a temporary file failed: %w", dir, err)
		}

		err = client.Download(c.Context(), version.ID, file, temp)
		if closed := temp.Close(); err == nil {
			err = closed
		}

		if err == nil {
			err = os.Rename(temp.Name(), path)
		}

		if err != nil {
			os.Remove(temp.Name())

			return err
		}
	}

	return nil
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

// localFilename reports whether name is safe as one file name in a directory.
func localFilename(name string) bool {
	return name != "" && name != "." && name != ".." && name != ".gitignore" &&
		!strings.ContainsAny(name, `/\`) && strings.IndexFunc(name, unicode.IsControl) < 0
}

// writeIndex writes the run API's index, as it returned it.
func (s *artifactStore) writeIndex(raw []byte) error {
	path := filepath.Join(s.dir, indexFile)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("Check %s. Writing it failed: %w", path, err)
	}

	return nil
}
