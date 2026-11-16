package cmd_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// runAPIServer stands in for the gateway's route to the run API: it serves the snapshot,
// the index and the files it is set to, and records what is pushed.
type runAPIServer struct {
	*httptest.Server
	mu        sync.Mutex
	snapshot  string
	index     string
	files     map[string]string
	downloads int
	pushes    []map[string]any
	verdicts  []string
	status    int
	response  string
	headers   http.Header
	// indexAfterPush, when set, replaces the index once a push arrives: another writer
	// changed the artifacts between the index that qory read and its push.
	indexAfterPush string
}

// startRunAPI starts the server and sets QORY_RUN_API to the run's address on it.
func startRunAPI(t *testing.T) *runAPIServer {
	t.Helper()

	s := &runAPIServer{
		snapshot: `{"version":1,"work_item":{"id":"wi_1","number":12},"inputs":[],"outputs":[]}`,
		index:    `{"version":1,"artifacts":[]}`,
		files:    map[string]string{},
		status:   http.StatusCreated,
		response: `{"version":1,"artifact":{"type":"screenshot","name":"checkout","version":{"id":"av_3","number":3}}}`,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	t.Setenv("QORY_RUN_API", s.URL+"/v1/runs/run-1")

	return s
}

func (s *runAPIServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.headers = r.Header
	path := strings.TrimPrefix(r.URL.Path, "/v1/runs/run-1")

	switch {
	case path == "/work-item":
		_, _ = io.WriteString(w, s.snapshot)
	case path == "/artifacts" && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, s.index)
	case path == "/artifacts":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.pushes = append(s.pushes, body)
		if s.indexAfterPush != "" {
			s.index = s.indexAfterPush
		}
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.response)
	case path == "/verdict":
		raw, _ := io.ReadAll(r.Body)
		s.verdicts = append(s.verdicts, string(raw))
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.response)
	default:
		content, ok := s.files[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found"}`)

			return
		}

		s.downloads++
		_, _ = io.WriteString(w, content)
	}
}

// fileEntry returns an index's entry of a file with content.
func fileEntry(name, content string) string {
	sum := sha256.Sum256([]byte(content))

	return fmt.Sprintf(`{"filename":%q,"content_type":"text/plain","size":%d,"sha256":%q}`,
		name, len(content), hex.EncodeToString(sum[:]))
}

// versionEntry returns an index's entry of a version with files.
func versionEntry(id string, number int, files ...string) string {
	return fmt.Sprintf(`{"id":%q,"number":%d,"writer":{"kind":"run","run_id":"r"},"stage_id":null,"note":null,`+
		`"at":"2026-11-02T09:00:00Z","files":[%s],"approvals":[]}`, id, number, strings.Join(files, ","))
}

// cancelled returns the version entry with "cancelled": true, a version of a cancelled run.
func cancelled(version string) string {
	return strings.Replace(version, `"writer":`, `"cancelled":true,"writer":`, 1)
}

// withdrawn returns the version entry with "withdrawn": true and no files, a withdrawal.
func withdrawn(version string) string {
	return strings.Replace(version, `"writer":`, `"withdrawn":true,"writer":`, 1)
}

// byPerson returns the version entry written by the member Ana, not by a run.
func byPerson(version string) string {
	return strings.Replace(version, `{"kind":"run","run_id":"r"}`, `{"kind":"person","name":"Ana"}`, 1)
}

// artifactEntry returns an index's entry of an artifact whose newest version is newest.
func artifactEntry(kind, name, approved string, newest int, versions ...string) string {
	approval := "null"
	if approved != "" {
		approval = fmt.Sprintf("%q", approved)
	}

	return fmt.Sprintf(`{"type":%q,"name":%q,"stage_id":null,"approved_version":%s,"newest_version":%d,`+
		`"changed_after_approval":false,"versions":[%s]}`, kind, name, approval, newest, strings.Join(versions, ","))
}

// indexOf returns the index of artifacts.
func indexOf(artifacts ...string) string {
	return `{"version":1,"artifacts":[` + strings.Join(artifacts, ",") + `]}`
}

// inCheckout changes to a new git checkout and returns it.
func inCheckout(t *testing.T) string {
	t.Helper()

	root := emptyDir(t)
	runGit(t, root, "init", "-q")
	t.Chdir(root)

	return root
}

// wantFile fails unless the file at path has content.
func wantFile(t *testing.T, path, content string) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != content {
		t.Errorf("%s = %q, want %q", path, got, content)
	}
}

// wantExit fails unless err exits with status and starts with text.
func wantExit(t *testing.T, err error, status int, text string) {
	t.Helper()

	if err == nil {
		t.Fatalf("no error, want %q", text)
	}

	if got := cmd.ExitCode(err); got != status {
		t.Errorf("exit status = %d, want %d, err = %v", got, status, err)
	}

	if !strings.HasPrefix(err.Error(), text) {
		t.Errorf("err = %q, want %q", err, text)
	}
}

func TestWorkItemShowPrintsTheSnapshotInARun(t *testing.T) {
	inCheckout(t)
	server := startRunAPI(t)
	server.snapshot = `{"version":1,"ticket":{"title":"A title"},"notes":[]}`

	out, err := run(t, "work-item", "show")
	if err != nil {
		t.Fatal(err)
	}

	if out != server.snapshot+"\n" {
		t.Errorf("out = %q", out)
	}

	for name := range server.headers {
		if strings.HasPrefix(name, "X-Qory") || name == "Authorization" {
			t.Errorf("the command sent %s", name)
		}
	}
}

func TestWorkItemShowReadsTheLocalFileOutsideARun(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	_, err := run(t, "work-item", "show")
	wantExit(t, err, cmd.ExitNoWorkItem, "No work item. Write .qory/work-item.json, or pass the task to the skill.")

	if err := os.Mkdir(filepath.Join(root, ".qory"), 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, ".qory", "work-item.json")
	if err := os.WriteFile(path, []byte("{\"title\":\"Local\"}\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "work-item", "show")
	if err != nil {
		t.Fatal(err)
	}

	if out != "{\"title\":\"Local\"}\n" {
		t.Errorf("out = %q", out)
	}

	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = run(t, "work-item", "show")
	wantExit(t, err, cmd.ExitInput, "Fix ")
}

func TestABadRunAPIAddressIsReported(t *testing.T) {
	inCheckout(t)
	t.Setenv("QORY_RUN_API", "gateway/v1/runs/1")

	_, err := run(t, "work-item", "show")
	wantExit(t, err, 1, "Set the run API's base URL")
}

func TestArtifactPullOutsideARunFetchesNothing(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	out, err := run(t, "artifact", "pull")
	if err != nil {
		t.Fatal(err)
	}

	if out != "Nothing pulled: outside a Qory Apiary run, qory uses only the local folder .qory/artifacts.\n" {
		t.Errorf("out = %q", out)
	}

	if _, err := os.Stat(filepath.Join(root, ".qory")); !os.IsNotExist(err) {
		t.Errorf(".qory: %v", err)
	}
}

// pullFixture sets the server to a stage with the input brief and the output demo, and
// an artifact of neither.
func pullFixture(server *runAPIServer) {
	server.snapshot = `{"version":1,"inputs":[{"type":"brief","required":true,"version":{"id":"av_b1","number":1}}],` +
		`"outputs":[{"type":"demo","required":true,"version":null,"check":"missing"}]}`
	server.index = indexOf(
		artifactEntry("brief", "main", "av_b1", 2,
			versionEntry("av_b1", 1, fileEntry("brief.md", "approved brief")),
			versionEntry("av_b2", 2, fileEntry("brief.md", "newer brief"))),
		artifactEntry("demo", "main", "av_d1", 2,
			versionEntry("av_d1", 1, fileEntry("demo.md", "old demo")),
			versionEntry("av_d2", 2, fileEntry("demo.md", "new demo"))),
		artifactEntry("notes", "main", "", 1, versionEntry("av_n1", 1, fileEntry("notes.md", "the notes"))),
	)
	server.files = map[string]string{
		"/artifacts/av_b1/files/brief.md": "approved brief",
		"/artifacts/av_b2/files/brief.md": "newer brief",
		"/artifacts/av_d1/files/demo.md":  "old demo",
		"/artifacts/av_d2/files/demo.md":  "new demo",
		"/artifacts/av_n1/files/notes.md": "the notes",
	}
}

func TestArtifactPullFetchesTheChosenVersions(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	pullFixture(server)

	out, err := run(t, "artifact", "pull")
	if err != nil {
		t.Fatal(err)
	}

	store := filepath.Join(root, ".qory", "artifacts")
	if out != store+"/\n" {
		t.Errorf("out = %q", out)
	}

	wantFile(t, filepath.Join(store, "brief", "main", "v1", "brief.md"), "approved brief")
	wantFile(t, filepath.Join(store, "demo", "main", "v2", "demo.md"), "new demo")
	wantFile(t, filepath.Join(store, "notes", "main", "v1", "notes.md"), "the notes")
	wantFile(t, filepath.Join(store, "index.json"), server.index)

	for _, absent := range []string{"brief/main/v2", "demo/main/v1"} {
		if _, err := os.Stat(filepath.Join(store, absent)); !os.IsNotExist(err) {
			t.Errorf("%s: %v", absent, err)
		}
	}

	// A run's checkout has no exclude line for .qory, and a later commit takes no artifact in.
	status, err := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}

	if len(status) != 0 {
		t.Errorf("git status = %s", status)
	}
}

func TestArtifactPullKeepsAFileThatMatchesTheIndex(t *testing.T) {
	inCheckout(t)
	server := startRunAPI(t)
	pullFixture(server)

	for range 2 {
		if _, err := run(t, "artifact", "pull"); err != nil {
			t.Fatal(err)
		}
	}

	if server.downloads != 3 {
		t.Errorf("downloads = %d", server.downloads)
	}
}

func TestArtifactPullOfAVersion(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	pullFixture(server)

	if _, err := run(t, "artifact", "pull", "demo", "--version", "1"); err != nil {
		t.Fatal(err)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "demo", "main", "v1", "demo.md"), "old demo")

	if _, err := os.Stat(filepath.Join(root, ".qory", "artifacts", "brief")); !os.IsNotExist(err) {
		t.Errorf("brief: %v", err)
	}

	_, err := run(t, "artifact", "pull", "demo", "--version", "9")
	wantExit(t, err, cmd.ExitInput, "Choose a version")

	_, err = run(t, "artifact", "pull", "--version", "1")
	wantExit(t, err, cmd.ExitInput, "Name the artifact type")

	for _, value := range []string{"-1", "0"} {
		_, err = run(t, "artifact", "pull", "demo", "--version="+value)
		wantExit(t, err, cmd.ExitInput, "Set --version")
	}
}

func TestArtifactPullWritesNothingOutsideItsDirectory(t *testing.T) {
	for name, entry := range map[string]string{
		"a filename": artifactEntry("demo", "main", "av_1", 1, versionEntry("av_1", 1, fileEntry("../../../../x", "bytes"))),
		"a type":     artifactEntry("../demo", "main", "av_1", 1, versionEntry("av_1", 1, fileEntry("a", "bytes"))),
		"a name":     artifactEntry("demo", "../../x", "av_1", 1, versionEntry("av_1", 1, fileEntry("a", "bytes"))),
	} {
		t.Run(name, func(t *testing.T) {
			root := inCheckout(t)
			server := startRunAPI(t)
			server.index = indexOf(entry)

			_, err := run(t, "artifact", "pull")
			wantExit(t, err, 1, "Check the artifact index of this run.")

			_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, _ error) error {
				if entry.Name() == "x" || entry.Name() == "a" {
					t.Errorf("wrote %s", path)
				}

				return nil
			})
		})
	}
}

func TestArtifactPullKeepsAFileNamedGitignore(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.index = indexOf(artifactEntry("demo", "main", "av_1", 1, versionEntry("av_1", 1, fileEntry(".gitignore", "bytes"))))
	server.files["/artifacts/av_1/files/.gitignore"] = "bytes"

	if _, err := run(t, "artifact", "pull"); err != nil {
		t.Fatal(err)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "demo", "main", "v1", ".gitignore"), "bytes")
}

func TestArtifactPullChoosesAVersionPerArtifact(t *testing.T) {
	for name, c := range map[string]struct {
		snapshot string
		artifact string
		version  string
	}{
		"an input with a version newer than the approved one": {
			`{"version":1,"inputs":[{"type":"demo","required":true,"version":{"id":"av_2","number":2}}],"outputs":[]}`,
			artifactEntry("demo", "main", "av_1", 3, versionEntry("av_1", 1, fileEntry("v", "one")),
				versionEntry("av_2", 2, fileEntry("v", "two")), versionEntry("av_3", 3, fileEntry("v", "three"))),
			"v2",
		},
		"another artifact of an input's type takes its approved version": {
			`{"version":1,"inputs":[{"type":"demo","required":true,"version":{"id":"av_9","number":9}}],"outputs":[]}`,
			artifactEntry("demo", "main", "av_1", 2, versionEntry("av_1", 1, fileEntry("v", "one")),
				versionEntry("av_2", 2, fileEntry("v", "two"))),
			"v1",
		},
		"an input with no approved version is not fetched": {
			`{"version":1,"inputs":[{"type":"demo","required":true,"version":null}],"outputs":[]}`,
			artifactEntry("demo", "main", "", 1, versionEntry("av_1", 1, fileEntry("v", "one"))),
			"",
		},
		"a type that is an input and an output is an output": {
			`{"version":1,"inputs":[{"type":"demo","required":true,"version":{"id":"av_1","number":1}}],` +
				`"outputs":[{"type":"demo","required":true,"version":null,"check":"missing"}]}`,
			artifactEntry("demo", "main", "av_1", 2, versionEntry("av_1", 1, fileEntry("v", "one")),
				versionEntry("av_2", 2, fileEntry("v", "two"))),
			"v2",
		},
		"another artifact with no approved version takes its newest": {
			`{"version":1,"inputs":[],"outputs":[]}`,
			artifactEntry("demo", "main", "", 2, versionEntry("av_1", 1, fileEntry("v", "one")),
				versionEntry("av_2", 2, fileEntry("v", "two"))),
			"v2",
		},
		"an output takes its newest version that is not cancelled": {
			`{"version":1,"inputs":[],"outputs":[{"type":"demo","required":true,"version":null,"check":"missing"}]}`,
			artifactEntry("demo", "main", "", 3, versionEntry("av_1", 1, fileEntry("v", "one")),
				versionEntry("av_2", 2, fileEntry("v", "two")), cancelled(versionEntry("av_3", 3, fileEntry("v", "three")))),
			"v2",
		},
		"another artifact takes its newest version that is not cancelled": {
			`{"version":1,"inputs":[],"outputs":[]}`,
			artifactEntry("demo", "main", "", 3, versionEntry("av_1", 1, fileEntry("v", "one")),
				versionEntry("av_2", 2, fileEntry("v", "two")), cancelled(versionEntry("av_3", 3, fileEntry("v", "three")))),
			"v2",
		},
		"an output whose versions are all cancelled is not fetched": {
			`{"version":1,"inputs":[],"outputs":[{"type":"demo","required":true,"version":null,"check":"missing"}]}`,
			artifactEntry("demo", "main", "", 2, cancelled(versionEntry("av_1", 1, fileEntry("v", "one"))),
				cancelled(versionEntry("av_2", 2, fileEntry("v", "two")))),
			"",
		},
		"an output whose newest version is withdrawn is not fetched": {
			`{"version":1,"inputs":[],"outputs":[{"type":"demo","required":true,"version":null,"check":"missing"}]}`,
			artifactEntry("demo", "main", "", 2, versionEntry("av_1", 1, fileEntry("v", "one")),
				withdrawn(versionEntry("av_2", 2))),
			"",
		},
		"another artifact whose newest version is withdrawn is not fetched": {
			`{"version":1,"inputs":[],"outputs":[]}`,
			artifactEntry("demo", "main", "", 2, versionEntry("av_1", 1, fileEntry("v", "one")),
				withdrawn(versionEntry("av_2", 2))),
			"",
		},
		"an input whose newest version is withdrawn is not fetched": {
			`{"version":1,"inputs":[{"type":"demo","required":true,"version":{"id":"av_1","number":1}}],"outputs":[]}`,
			artifactEntry("demo", "main", "av_1", 2, versionEntry("av_1", 1, fileEntry("v", "one")),
				withdrawn(versionEntry("av_2", 2))),
			"",
		},
		"a name whose withdrawal is newer than a cancelled version is not fetched": {
			`{"version":1,"inputs":[],"outputs":[]}`,
			artifactEntry("demo", "main", "", 3, versionEntry("av_1", 1, fileEntry("v", "one")),
				withdrawn(versionEntry("av_2", 2)), cancelled(versionEntry("av_3", 3, fileEntry("v", "three")))),
			"",
		},
		"a push of files after a withdrawal lists the name again": {
			`{"version":1,"inputs":[],"outputs":[]}`,
			artifactEntry("demo", "main", "", 3, versionEntry("av_1", 1, fileEntry("v", "one")),
				withdrawn(versionEntry("av_2", 2)), versionEntry("av_3", 3, fileEntry("v", "three"))),
			"v3",
		},
		"another artifact whose versions are all cancelled is not fetched": {
			`{"version":1,"inputs":[],"outputs":[]}`,
			artifactEntry("demo", "main", "", 2, cancelled(versionEntry("av_1", 1, fileEntry("v", "one"))),
				cancelled(versionEntry("av_2", 2, fileEntry("v", "two")))),
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := inCheckout(t)
			server := startRunAPI(t)
			server.snapshot = c.snapshot
			server.index = indexOf(c.artifact)
			server.files = map[string]string{
				"/artifacts/av_1/files/v": "one", "/artifacts/av_2/files/v": "two", "/artifacts/av_3/files/v": "three",
			}

			if _, err := run(t, "artifact", "pull"); err != nil {
				t.Fatal(err)
			}

			entries, err := os.ReadDir(filepath.Join(root, ".qory", "artifacts", "demo", "main"))
			if c.version == "" {
				if err == nil {
					t.Errorf("entries = %v", entries)
				}

				return
			}

			if err != nil || len(entries) != 1 || entries[0].Name() != c.version {
				t.Errorf("entries = %v, %v, want %s", entries, err, c.version)
			}
		})
	}
}

func TestArtifactPullLeavesNoFileOfAFailedDownload(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.index = indexOf(artifactEntry("demo", "main", "av_1", 1, versionEntry("av_1", 1, fileEntry("a.md", "the bytes"))))
	server.files["/artifacts/av_1/files/a.md"] = "other bytes"

	_, err := run(t, "artifact", "pull")
	wantExit(t, err, 1, "Pull a.md again.")

	entries, err := os.ReadDir(filepath.Join(root, ".qory", "artifacts", "demo", "main", "v1"))
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Errorf("entries = %v", entries)
	}
}

func TestADirectoryLinkInTheStoreStopsAWrite(t *testing.T) {
	for name, link := range map[string]string{
		"at .qory":     ".qory",
		"at artifacts": ".qory/artifacts",
		"at a type":    ".qory/artifacts/demo",
		"at a name":    ".qory/artifacts/demo/main",
	} {
		t.Run(name, func(t *testing.T) {
			root := inCheckout(t)
			t.Setenv("QORY_RUN_API", "")

			outside := t.TempDir()
			at := filepath.Join(root, filepath.FromSlash(link))

			if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(outside, at); err != nil {
				t.Fatal(err)
			}

			file := filepath.Join(root, "a.md")
			if err := os.WriteFile(file, []byte("bytes"), 0o644); err != nil {
				t.Fatal(err)
			}

			_, err := run(t, "artifact", "push", "demo", file)
			wantExit(t, err, 1, "Remove ")

			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Errorf("wrote %v outside the checkout", entries)
			}
		})
	}
}

func TestALinkAtAVersionStopsAPull(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.index = indexOf(artifactEntry("demo", "main", "av_1", 1, versionEntry("av_1", 1, fileEntry("a.md", "bytes"))))
	server.files["/artifacts/av_1/files/a.md"] = "bytes"

	outside := t.TempDir()
	version := filepath.Join(root, ".qory", "artifacts", "demo", "main", "v1")

	if err := os.MkdirAll(filepath.Dir(version), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, version); err != nil {
		t.Fatal(err)
	}

	_, err := run(t, "artifact", "pull")
	wantExit(t, err, 1, "Remove ")

	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("wrote %v outside the checkout", entries)
	}
}

func TestALinkInTheStoreStopsAPushBeforeItSends(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)

	if err := os.Symlink(t.TempDir(), filepath.Join(root, ".qory")); err != nil {
		t.Fatal(err)
	}

	_, err := runIn(t, "text", "artifact", "push", "doc", "-")
	wantExit(t, err, 1, "Remove ")

	if len(server.pushes) != 0 {
		t.Errorf("pushes = %d", len(server.pushes))
	}
}

func TestAWriteReplacesALinkAtAFilesPath(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.index = indexOf()
	store := filepath.Join(root, ".qory", "artifacts")
	outside := filepath.Join(t.TempDir(), "target")

	if err := os.WriteFile(outside, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	version := filepath.Join(store, "doc", "main", "v3")
	if err := os.MkdirAll(version, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{filepath.Join(store, ".gitignore"), filepath.Join(store, "index.json"), filepath.Join(version, "stdin.md")} {
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := runIn(t, "# Text\n", "artifact", "push", "doc", "-"); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, "artifact", "pull"); err != nil {
		t.Fatal(err)
	}

	wantFile(t, outside, "mine")
	wantFile(t, filepath.Join(version, "stdin.md"), "# Text\n")
}

func TestAResponseOfAnotherTypeWritesNoOtherPath(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.response = `{"version":1,"artifact":{"type":"../../evil","name":"main","version":{"id":"av_3","number":3}}}`

	if _, err := runIn(t, "# Text\n", "artifact", "push", "doc", "-"); err != nil {
		t.Fatal(err)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "doc", "main", "v3", "stdin.md"), "# Text\n")

	if _, err := os.Stat(filepath.Join(root, ".qory", "evil")); !os.IsNotExist(err) {
		t.Errorf("evil: %v", err)
	}
}

func TestArtifactPushSendsOneVersionAndKeepsACopy(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)

	first := filepath.Join(root, "checkout.md")
	second := filepath.Join(root, "other.md")

	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("content of "+filepath.Base(path)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, err := run(t, "artifact", "push", "screenshot", first, second, "--name", "checkout", "--note", "After the fix.")
	if err != nil {
		t.Fatal(err)
	}

	if out != "Pushed screenshot/checkout version 3.\n" {
		t.Errorf("out = %q", out)
	}

	body := server.pushes[0]
	if body["type"] != "screenshot" || body["name"] != "checkout" || body["note"] != "After the fix." {
		t.Errorf("body = %v", body)
	}

	files := body["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}

	entry := files[0].(map[string]any)
	want := base64.StdEncoding.EncodeToString([]byte("content of checkout.md"))

	if entry["filename"] != "checkout.md" || entry["content_type"] != "text/plain" || entry["content_base64"] != want {
		t.Errorf("file = %v", entry)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "screenshot", "checkout", "v3", "other.md"), "content of other.md")
}

func TestArtifactPushDefaultsToTheNameMain(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.response = `{"version":1,"artifact":{"type":"doc","name":"main","version":{"id":"av_1","number":1}}}`

	if _, err := runIn(t, "# A heading\n", "artifact", "push", "doc", "-"); err != nil {
		t.Fatal(err)
	}

	if server.pushes[0]["name"] != "main" {
		t.Errorf("body = %v", server.pushes[0])
	}

	// A file from standard input has the extension of its content.
	wantFile(t, filepath.Join(root, ".qory", "artifacts", "doc", "main", "v1", "stdin.md"), "# A heading\n")
}

func TestArtifactPushExitStatuses(t *testing.T) {
	for code, c := range map[string]struct {
		status int
		exit   int
	}{
		"unknown_type":      {http.StatusUnprocessableEntity, cmd.ExitArtifactRefused},
		"too_many_files":    {http.StatusUnprocessableEntity, cmd.ExitArtifactRefused},
		"too_large":         {http.StatusUnprocessableEntity, cmd.ExitArtifactRefused},
		"payload_too_large": {http.StatusRequestEntityTooLarge, cmd.ExitArtifactRefused},
		"invalid_artifact":  {http.StatusUnprocessableEntity, 1},
		"run_ended":         {http.StatusConflict, 1},
		"answer_unverified": {http.StatusBadGateway, 1},
	} {
		t.Run(code, func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)
			server.status = c.status
			server.response = fmt.Sprintf(`{"error":%q,"message":"The line of the run API."}`, code)

			_, err := runIn(t, "text", "artifact", "push", "screenshot", "-")
			wantExit(t, err, c.exit, "The line of the run API.")
		})
	}
}

// TestArtifactPushOfAFailedPushCheck is a push that a walled run's push check stops:
// 422 push_check_failed exits 4 with the message, then each finding on a separate line,
// and keeps no copy; 503 push_check_unavailable exits 1 with the message.
func TestArtifactPushOfAFailedPushCheck(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.status = http.StatusUnprocessableEntity
	server.response = `{"error":"push_check_failed","message":"Fix each finding below and push again. The check for spec failed:",` +
		`"findings":["spec.md: the section Goal is missing","spec.md: 2 links point nowhere"]}`

	_, err := runIn(t, "# Spec\n", "artifact", "push", "spec", "-")
	want := "Fix each finding below and push again. The check for spec failed:\n" +
		"spec.md: the section Goal is missing\nspec.md: 2 links point nowhere"
	wantExit(t, err, cmd.ExitArtifactRefused, want)

	if err != nil && err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}

	if _, err := os.Stat(filepath.Join(root, ".qory", "artifacts", "spec")); !os.IsNotExist(err) {
		t.Errorf("the store has a copy of the version: %v", err)
	}

	server.status = http.StatusServiceUnavailable
	server.response = `{"error":"push_check_unavailable","message":"Run the push again: the check before the push did not finish."}`

	_, err = runIn(t, "# Spec\n", "artifact", "push", "spec", "-")
	wantExit(t, err, 1, "Run the push again: the check before the push did not finish.")
}

func TestArtifactPushOverTheRunAPILimitReadsNoFile(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)

	path := filepath.Join(root, "video.mp4")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Truncate(path, 1<<30); err != nil {
		t.Fatal(err)
	}

	// A read of the file would fail with another error.
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}

	_, err := run(t, "artifact", "push", "demo-video", path)
	wantExit(t, err, cmd.ExitArtifactRefused, "Shrink the files of this version of demo-video")

	if len(server.pushes) != 0 {
		t.Errorf("pushes = %d", len(server.pushes))
	}
}

func TestArtifactPushInARunWithNoWorkItem(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"Qory Apiary's response": {
			`{"error":"not_found"}`,
			"Start the run from a work item in Qory Apiary: Qory Apiary found no work item for this run.",
		},
		"the gateway's response": {`{"error":"not_found","message":"Check the run id."}`, "Check the run id."},
	} {
		t.Run(name, func(t *testing.T) {
			root := inCheckout(t)
			server := startRunAPI(t)
			server.status = http.StatusNotFound
			server.response = c.body

			_, err := runIn(t, "text", "artifact", "push", "doc", "-")
			wantExit(t, err, 1, c.want)

			if _, err := os.Stat(filepath.Join(root, ".qory", "artifacts", "doc")); !os.IsNotExist(err) {
				t.Errorf("kept a copy: %v", err)
			}
		})
	}
}

func TestArtifactPushOverTheFilesOfTheRunAPI(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)

	args := []string{"artifact", "push", "screenshot"}

	for number := range 101 {
		path := filepath.Join(root, fmt.Sprintf("%d.md", number))
		if err := os.WriteFile(path, []byte("bytes"), 0o644); err != nil {
			t.Fatal(err)
		}

		args = append(args, path)
	}

	_, err := run(t, args...)
	wantExit(t, err, cmd.ExitArtifactRefused, "Push at most 100 files in one version of screenshot.")

	if len(server.pushes) != 0 {
		t.Errorf("pushes = %d", len(server.pushes))
	}
}

func TestArtifactPushCutsNoInputOutsideARun(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	_, err := runIn(t, strings.Repeat("a", 32<<20+1), "artifact", "push", "doc", "-")
	wantExit(t, err, cmd.ExitInput, "Pass a file in place of -.")

	if _, err := os.Stat(filepath.Join(root, ".qory")); !os.IsNotExist(err) {
		t.Errorf(".qory: %v", err)
	}
}

func TestArtifactPushOutsideARunKeepsNewVersions(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	path := filepath.Join(root, "a.md")
	if err := os.WriteFile(path, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	for number := 1; number <= 2; number++ {
		out, err := run(t, "artifact", "push", "demo-video", path)
		if err != nil {
			t.Fatal(err)
		}

		want := fmt.Sprintf("Kept in .qory/artifacts/demo-video/main/v%d/; not sent: outside a Qory Apiary run, qory keeps versions only in .qory/artifacts.\n", number)
		if out != want {
			t.Errorf("out = %q", out)
		}
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "demo-video", "main", "v2", "a.md"), "bytes")
}

// withdrawIndex is the index of a run that has the artifact demo-script/scenario-2 at v2,
// written by a run, and the artifact demo-script/scenario-3 at v1, written by Ana.
func withdrawIndex() string {
	return indexOf(
		artifactEntry("demo-script", "scenario-2", "", 2, versionEntry("av_1", 1, fileEntry("s.json", "one")),
			versionEntry("av_2", 2, fileEntry("s.json", "two"))),
		artifactEntry("demo-script", "scenario-3", "", 1, byPerson(versionEntry("av_3", 1, fileEntry("s.json", "x")))),
	)
}

func TestArtifactPushWithdrawSendsAWithdrawal(t *testing.T) {
	inCheckout(t)
	server := startRunAPI(t)
	server.index = withdrawIndex()
	server.response = `{"version":1,"artifact":{"type":"demo-script","name":"scenario-2","version":{"id":"av_4","number":3}}}`

	out, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw", "--note", "Removed.")
	if err != nil {
		t.Fatal(err)
	}

	if out != "Withdrew demo-script/scenario-2 in version 3.\n" {
		t.Errorf("out = %q", out)
	}

	if len(server.pushes) != 1 {
		t.Fatalf("pushes = %v", server.pushes)
	}

	body := server.pushes[0]
	if body["type"] != "demo-script" || body["name"] != "scenario-2" || body["note"] != "Removed." || body["withdraw"] != true {
		t.Errorf("body = %v", body)
	}

	if files, ok := body["files"].([]any); !ok || len(files) != 0 {
		t.Errorf("files = %v, want an empty list", body["files"])
	}
}

func TestArtifactPushWithoutWithdrawSendsNoWithdrawMember(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)

	path := filepath.Join(root, "a.md")
	if err := os.WriteFile(path, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, "artifact", "push", "demo-script", path); err != nil {
		t.Fatal(err)
	}

	if _, has := server.pushes[0]["withdraw"]; has {
		t.Errorf("body = %v", server.pushes[0])
	}
}

// TestArtifactPushWithdrawRefusals is each refusal of a withdrawal: exit 4, the text of
// decision 654 (c), and no POST.
func TestArtifactPushWithdrawRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		args  []string
		index string
		want  string
	}{
		"a name with no version": {
			[]string{"demo-script", "--name", "scenario-9"}, withdrawIndex(),
			"Withdraw only a stored artifact.\ndemo-script/scenario-9 has no version outside cancelled runs.",
		},
		"a name whose versions are all cancelled": {
			[]string{"demo-script", "--name", "scenario-5"},
			indexOf(artifactEntry("demo-script", "scenario-5", "", 1, cancelled(versionEntry("av_1", 1, fileEntry("s.json", "x"))))),
			"Withdraw only a stored artifact.\ndemo-script/scenario-5 has no version outside cancelled runs.",
		},
		"a file argument": {
			[]string{"demo-script", "scenario-2.json", "--name", "scenario-2"}, withdrawIndex(),
			"Pass either files or --withdraw.\nA withdrawal stores no files, and the command has 1 file: scenario-2.json.",
		},
		"two file arguments": {
			[]string{"demo-script", "a.json", "-", "--name", "scenario-2"}, withdrawIndex(),
			"Pass either files or --withdraw.\nA withdrawal stores no files, and the command has 2 files: a.json, -.",
		},
		"a name already withdrawn": {
			[]string{"demo-script", "--name", "scenario-4"},
			indexOf(artifactEntry("demo-script", "scenario-4", "", 3, versionEntry("av_1", 1, fileEntry("s.json", "x")),
				versionEntry("av_2", 2, fileEntry("s.json", "y")), withdrawn(versionEntry("av_3", 3)))),
			"Leave demo-script/scenario-4 as it is.\nIts newest version, v3, is already a withdrawal.",
		},
		"a withdrawal under a cancelled version": {
			[]string{"demo-script", "--name", "scenario-4"},
			indexOf(artifactEntry("demo-script", "scenario-4", "", 3, versionEntry("av_1", 1, fileEntry("s.json", "x")),
				withdrawn(versionEntry("av_2", 2)), cancelled(versionEntry("av_3", 3, fileEntry("s.json", "y"))))),
			"Leave demo-script/scenario-4 as it is.\nIts newest version, v2, is already a withdrawal.",
		},
		"a person's upload": {
			[]string{"demo-script", "--name", "scenario-3"}, withdrawIndex(),
			"Leave demo-script/scenario-3 to a member.\nIts newest version, v1, is an upload by Ana, and a run withdraws only a name whose newest version a run wrote.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)
			server.index = c.index

			_, err := run(t, append([]string{"artifact", "push", "--withdraw"}, c.args...)...)
			wantExit(t, err, cmd.ExitArtifactRefused, c.want)

			if err.Error() != c.want {
				t.Errorf("err = %q, want %q", err, c.want)
			}

			if len(server.pushes) != 0 {
				t.Errorf("pushes = %v", server.pushes)
			}
		})
	}
}

// TestArtifactPushWithdrawOfARefusalAfterTheIndex is a withdrawal that the run API refuses
// after qory read an index that allowed it, because the artifacts changed between the
// two. Each code that depends on the artifacts exits 4 with qory's text for what the index
// shows now; with an index that shows nothing, with the run API's message.
func TestArtifactPushWithdrawOfARefusalAfterTheIndex(t *testing.T) {
	changed := map[string]string{
		"no_version": indexOf(),
		"already_withdrawn": indexOf(artifactEntry("demo-script", "scenario-2", "", 3,
			versionEntry("av_1", 1, fileEntry("s.json", "one")), withdrawn(versionEntry("av_3", 3)))),
		"person_version": indexOf(artifactEntry("demo-script", "scenario-2", "", 3,
			versionEntry("av_1", 1, fileEntry("s.json", "one")), byPerson(versionEntry("av_3", 3, fileEntry("s.json", "x"))))),
	}
	texts := map[string]string{
		"no_version":        "Withdraw only a stored artifact.\ndemo-script/scenario-2 has no version outside cancelled runs.",
		"already_withdrawn": "Leave demo-script/scenario-2 as it is.\nIts newest version, v3, is already a withdrawal.",
		"person_version": "Leave demo-script/scenario-2 to a member.\nIts newest version, v3, is an upload by Ana, " +
			"and a run withdraws only a name whose newest version a run wrote.",
	}

	for code, index := range changed {
		t.Run(code, func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)
			server.index = withdrawIndex()
			server.indexAfterPush = index
			server.status = http.StatusConflict
			server.response = fmt.Sprintf(`{"error":%q,"message":"The line of the run API."}`, code)

			_, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw")
			wantExit(t, err, cmd.ExitArtifactRefused, texts[code])
		})
	}

	// The statuses are the ones of the run API's contract.
	for code, status := range map[string]int{
		"no_version": http.StatusConflict, "already_withdrawn": http.StatusConflict, "person_version": http.StatusConflict,
		"withdrawal_with_files": http.StatusUnprocessableEntity, "type_not_withdrawable": http.StatusUnprocessableEntity,
	} {
		t.Run(code+" with an index that shows no cause", func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)
			server.index = withdrawIndex()
			server.status = status
			server.response = fmt.Sprintf(`{"error":%q,"message":"The line of the run API."}`, code)

			_, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw")
			wantExit(t, err, cmd.ExitArtifactRefused, "The line of the run API.")
		})
	}
}

// TestArtifactPushWithdrawOfATypeTheWorkspaceLost is a 422 unknown_type for a withdrawal: the
// workspace has no such artifact type any more, or hides it. It exits 4 with its own text, not
// the text for a name with no version, and with the type.
func TestArtifactPushWithdrawOfATypeTheWorkspaceLost(t *testing.T) {
	inCheckout(t)
	server := startRunAPI(t)
	server.index = withdrawIndex()
	server.status = http.StatusUnprocessableEntity
	server.response = `{"error":"unknown_type","type":"demo-script","message":"Define the artifact type demo-script in the workspace's Artifact types, then run the round again."}`

	_, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw")
	wantExit(t, err, cmd.ExitArtifactRefused, "Define or switch on the artifact type demo-script in the workspace's Artifact types.\n"+
		"Then run the round again. The workspace has no such type, or hides it.")

	if len(server.pushes) != 1 {
		t.Errorf("pushes = %v", server.pushes)
	}
}

func TestArtifactPushWithdrawOutsideARunKeepsTheWithdrawal(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	path := filepath.Join(root, "s.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw")
	wantExit(t, err, cmd.ExitArtifactRefused, "Withdraw only a stored artifact.\ndemo-script/scenario-2 has no version outside cancelled runs.")

	// A refusal creates nothing, not even the store.
	if _, err := os.Stat(filepath.Join(root, ".qory")); !os.IsNotExist(err) {
		t.Errorf(".qory after a refusal: %v", err)
	}

	if _, err := run(t, "artifact", "push", "demo-script", path, "--name", "scenario-2"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw")
	if err != nil {
		t.Fatal(err)
	}

	want := "Kept the withdrawal in .qory/artifacts/demo-script/scenario-2/v2/. Outside a Qory Apiary run, qory sends nothing.\n"
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}

	entries, err := os.ReadDir(filepath.Join(root, ".qory", "artifacts", "demo-script", "scenario-2", "v2"))
	if err != nil || len(entries) != 0 {
		t.Errorf("v2 = %v, %v, want a directory with no files", entries, err)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "demo-script", "scenario-2", "v1", "s.json"), "{}")

	before := listTree(t, filepath.Join(root, ".qory"))

	_, err = run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw")
	wantExit(t, err, cmd.ExitArtifactRefused, "Leave demo-script/scenario-2 as it is.\nIts newest version, v2, is already a withdrawal.")

	_, err = run(t, "artifact", "push", "demo-script", "--name", "scenario-9", "--withdraw")
	wantExit(t, err, cmd.ExitArtifactRefused, "Withdraw only a stored artifact.\ndemo-script/scenario-9 has no version outside cancelled runs.")

	if after := listTree(t, filepath.Join(root, ".qory")); !slices.Equal(before, after) {
		t.Errorf("a refusal changed the store:\n%v\nafter\n%v", before, after)
	}

	// A push of files after the withdrawal is the next version.
	if _, err := run(t, "artifact", "push", "demo-script", path, "--name", "scenario-2"); err != nil {
		t.Fatal(err)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "demo-script", "scenario-2", "v3", "s.json"), "{}")
}

// TestArtifactPushWithdrawOutsideARunStopsAtALink is a withdrawal outside a run whose
// artifact folder, or whose newest version folder, is a link: the store's error, and
// nothing created beside the link.
func TestArtifactPushWithdrawOutsideARunStopsAtALink(t *testing.T) {
	for name, link := range map[string]string{
		"the name":    filepath.Join("demo-script", "scenario-2"),
		"the type":    "demo-script",
		"the version": filepath.Join("demo-script", "scenario-2", "v1"),
	} {
		t.Run(name, func(t *testing.T) {
			root := inCheckout(t)
			t.Setenv("QORY_RUN_API", "")

			store := filepath.Join(root, ".qory", "artifacts")
			elsewhere := t.TempDir()

			if err := os.MkdirAll(filepath.Join(store, filepath.Dir(link)), 0o755); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(elsewhere, filepath.Join(store, link)); err != nil {
				t.Fatal(err)
			}

			// What the link leads to has a version, so a link that is followed finds one.
			target := map[string]string{"the name": "v1", "the type": filepath.Join("scenario-2", "v1"), "the version": "inside"}[name]
			if err := os.MkdirAll(filepath.Join(elsewhere, target), 0o755); err != nil {
				t.Fatal(err)
			}

			before := listTree(t, store)

			_, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw")
			if err == nil || !strings.HasPrefix(err.Error(), "Remove "+filepath.Join(store, link)+": it must be a directory, not a link or a file.") {
				t.Errorf("err = %v", err)
			}

			if after := listTree(t, store); !slices.Equal(before, after) {
				t.Errorf("the store changed:\n%v\nafter\n%v", before, after)
			}

			entries, _ := os.ReadDir(elsewhere)
			for _, entry := range entries {
				if entry.Name() == "v2" {
					t.Errorf("the link's target has %s", entry.Name())
				}
			}
		})
	}
}

// listTree returns every path under dir, sorted, folders included.
func listTree(t *testing.T, dir string) []string {
	t.Helper()

	var paths []string

	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		paths = append(paths, path)

		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return paths
}

// memberApproved returns the version entry with an approval of the member dana at a gate,
// and automatically returns it with the approval that a gate with no approver records.
func memberApproved(version string) string {
	return strings.Replace(version, `"approvals":[]`,
		`"approvals":[{"by":{"name":"dana"},"automatic":false,"stage_id":"st_1","at":"2026-11-02T09:00:00Z"}]`, 1)
}

func automatically(version string) string {
	return strings.Replace(version, `"approvals":[]`,
		`"approvals":[{"by":null,"automatic":true,"stage_id":"st_1","at":"2026-11-02T09:00:00Z"}]`, 1)
}

// TestArtifactPushWithdrawOfAnApprovedName is a withdrawal of a name whose newest version a
// member approved at a gate: exit 4 before any POST, with the text of Qory Apiary's
// type_not_withdrawable. An automatic approval, an approval of an older version, and an
// approval of a version that a cancelled run wrote do not count.
func TestArtifactPushWithdrawOfAnApprovedName(t *testing.T) {
	older := versionEntry("av_1", 1, fileEntry("s.json", "one"))
	newer := versionEntry("av_2", 2, fileEntry("s.json", "two"))

	for name, c := range map[string]struct {
		versions []string
		refused  bool
	}{
		"a member approved the newest version":           {[]string{older, memberApproved(newer)}, true},
		"a member approved the only version":             {[]string{memberApproved(older)}, true},
		"a gate with no approver approved it":            {[]string{older, automatically(newer)}, false},
		"a member approved an older version":             {[]string{memberApproved(older), newer}, false},
		"a member approved a version of a cancelled run": {[]string{older, cancelled(memberApproved(newer))}, false},
		"nothing is approved":                            {[]string{older, newer}, false},
	} {
		t.Run(name, func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)
			server.index = indexOf(artifactEntry("demo-script", "scenario-2", "", len(c.versions), c.versions...))

			out, err := run(t, "artifact", "push", "demo-script", "--name", "scenario-2", "--withdraw")
			if !c.refused {
				if err != nil || out != "Withdrew demo-script/scenario-2 in version 3.\n" || len(server.pushes) != 1 {
					t.Errorf("out = %q, err = %v, pushes = %v", out, err, server.pushes)
				}

				return
			}

			want := fmt.Sprintf("Push a new version of demo-script/scenario-2 instead.\nA member approved its newest version, v%d, so a run cannot withdraw the name.", len(c.versions))
			wantExit(t, err, cmd.ExitArtifactRefused, want)

			if err.Error() != want || len(server.pushes) != 0 {
				t.Errorf("err = %q, pushes = %v", err, server.pushes)
			}
		})
	}
}

// TestArtifactPushWithdrawOfARefusedType is a 422 type_not_withdrawable for a withdrawal:
// the run API's message, with its fix alone on line 1, and exit 4. In a run qory reads no
// template, so it sends the push and maps the refusal, also for an approval that came after
// the index.
func TestArtifactPushWithdrawOfARefusedType(t *testing.T) {
	for name, c := range map[string]struct{ name, message, want string }{
		"a type with withdrawable false": {
			"acceptance-lock",
			"Push a new version of acceptance-lock instead. The artifact type acceptance-lock has withdrawable set to false, so a run cannot withdraw its names.",
			"Push a new version of acceptance-lock instead.\nThe artifact type acceptance-lock has withdrawable set to false, so a run cannot withdraw its names.",
		},
		"an approval after the index": {
			"acceptance-lock",
			"Push a new version of acceptance-lock/main instead. A member approved its newest version, v1, so a run cannot withdraw the name.",
			"Push a new version of acceptance-lock/main instead.\nA member approved its newest version, v1, so a run cannot withdraw the name.",
		},
	} {
		t.Run(name, func(t *testing.T) {
			inCheckout(t)
			server := startRunAPI(t)
			server.index = indexOf(artifactEntry(c.name, "main", "", 1, versionEntry("av_1", 1, fileEntry("lock.json", "x"))))
			server.status = http.StatusUnprocessableEntity
			server.response = fmt.Sprintf(`{"error":"type_not_withdrawable","type":%q,"message":%q}`, c.name, c.message)

			_, err := run(t, "artifact", "push", c.name, "--withdraw")
			wantExit(t, err, cmd.ExitArtifactRefused, c.want)

			if err.Error() != c.want || len(server.pushes) != 1 {
				t.Errorf("err = %q, pushes = %v", err, server.pushes)
			}
		})
	}
}

// TestArtifactPushWithdrawOutsideARunReadsTheTemplate is a withdrawal outside a run in a
// checkout that exports a factory module whose process.yaml sets withdrawable: false for
// acceptance-lock and reply-draft: exit 4 with the type's refusal, before the other
// refusals and with nothing created. The local store has no approvals, so no name is
// refused for one.
func TestArtifactPushWithdrawOutsideARunReadsTheTemplate(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")
	copyFixture(t, "factory-process-artifact-type-withdrawable", root)

	for _, kind := range []string{"acceptance-lock", "reply-draft"} {
		want := "Push a new version of " + kind + " instead.\nThe artifact type " + kind + " has withdrawable set to false, so a run cannot withdraw its names."

		_, err := run(t, "artifact", "push", "--withdraw", kind, "a.json")
		wantExit(t, err, cmd.ExitArtifactRefused, want)

		if err.Error() != want {
			t.Errorf("err = %q, want %q", err, want)
		}
	}

	if _, err := os.Stat(filepath.Join(root, ".qory")); !os.IsNotExist(err) {
		t.Errorf(".qory after a refusal: %v", err)
	}

	// A type with withdrawable true, a standard type with no entry and a type that the
	// template does not list are not refused for the type.
	for _, kind := range []string{"screenshot", "build-ledger", "demo-script"} {
		_, err := run(t, "artifact", "push", "--withdraw", kind)
		wantExit(t, err, cmd.ExitArtifactRefused, "Withdraw only a stored artifact.\n"+kind+"/main has no version outside cancelled runs.")
	}
}

// TestArtifactPushWithdrawOutsideARunReadsAnInvalidTemplate is a process.yaml that
// qory refuses: the withdrawal stops with the file and the fix, and keeps nothing.
func TestArtifactPushWithdrawOutsideARunReadsAnInvalidTemplate(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")
	copyFixture(t, "factory-process-artifact-type-withdrawable-not-boolean-fails", root)

	_, err := run(t, "artifact", "push", "--withdraw", "acceptance-lock")
	if err == nil || !strings.Contains(err.Error(), "process.yaml: set withdrawable of artifact type acceptance-lock to true or false") {
		t.Errorf("err = %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, ".qory")); !os.IsNotExist(err) {
		t.Errorf(".qory after a refusal: %v", err)
	}
}

func TestArtifactPushChecksItsArguments(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	path := filepath.Join(root, "a.md")
	if err := os.WriteFile(path, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(root, "sub")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}

	same := filepath.Join(other, "a.md")
	if err := os.WriteFile(same, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"artifact", "push", "Demo", path}, "Set the type"},
		{[]string{"artifact", "push", "../demo", path}, "Set the type"},
		{[]string{"artifact", "push", "demo", path, "--name", "a/b"}, "Set the --name"},
		{[]string{"artifact", "push", "demo", filepath.Join(root, "absent")}, "Pass a file that exists."},
		{[]string{"artifact", "push", "demo", other}, "Pass a file, not a directory"},
		{[]string{"artifact", "push", "demo", path, same}, "Rename one of the files named a.md."},
		{[]string{"artifact", "push", "demo", "-", "-"}, "Pass - once"},
		{[]string{"artifact", "push", "demo"}, "requires at least 2 arg"},
	} {
		_, err := run(t, c.args...)
		wantExit(t, err, cmd.ExitInput, c.want)
	}
}

// verdictPoints is a file of --points with a point to answer and a point to run again,
// and verdictPointsBody is the same points as qory sends and keeps them, with < and &
// unescaped, as encoding/json's Marshal would not write them.
const (
	verdictPoints = `[
  {"text": "The spec does not say which customers see the new price.", "action": "answer", "question": "q4"},
  {"text": "The checks script failed on the spec's rule a < b & c.", "action": "run_again"}
]
`
	verdictPointsBody = `{"verdict":"escalate","points":[` +
		`{"text":"The spec does not say which customers see the new price.","action":"answer","question":"q4"},` +
		`{"text":"The checks script failed on the spec's rule a < b & c.","action":"run_again"}]}`
)

// writePoints writes content to points.json in dir and returns its path.
func writePoints(t *testing.T, dir, content string) string {
	t.Helper()

	path := filepath.Join(dir, "points.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestRunVerdictStoresTheVerdictInARun(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.response = `{"version":1,"artifact":{"type":"verdict","name":"main","version":{"id":"av_4","number":4}}}`

	out, err := run(t, "run", "verdict", "escalate", "--points", writePoints(t, root, verdictPoints))
	if err != nil {
		t.Fatal(err)
	}

	if out != "Stored the verdict escalate as version 4.\n" {
		t.Errorf("out = %q", out)
	}

	if out, err = run(t, "run", "verdict", "changes-requested"); err != nil || out != "Stored the verdict changes-requested as version 4.\n" {
		t.Fatalf("changes-requested: %q, %v", out, err)
	}

	if want := []string{verdictPointsBody, `{"verdict":"changes-requested"}`}; !slices.Equal(server.verdicts, want) {
		t.Errorf("bodies = %q, want %q", server.verdicts, want)
	}
}

func TestRunVerdictOutsideARunKeepsTheVerdict(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	out, err := run(t, "run", "verdict", "passed")
	if err != nil {
		t.Fatal(err)
	}

	if out != "Verdict passed, kept in .qory/artifacts/verdict/main/v1/; nothing acts on it outside a run.\n" {
		t.Errorf("out = %q", out)
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "verdict", "main", "v1", "verdict.json"), `{"verdict":"passed"}`)

	out, err = run(t, "run", "verdict", "escalate", "--points", writePoints(t, root, verdictPoints))
	if err != nil {
		t.Fatal(err)
	}

	if out != "Verdict escalate, kept in .qory/artifacts/verdict/main/v2/; nothing acts on it outside a run.\n" {
		t.Errorf("out = %q", out)
	}

	// The same document as the run API receives in a run.
	wantFile(t, filepath.Join(root, ".qory", "artifacts", "verdict", "main", "v2", "verdict.json"), verdictPointsBody)
}

// TestRunVerdictChecksItsArguments stops each wrong command line or file of --points with
// exit 2 and the error, whose line starts with the fix, before anything is sent: in a
// run, no verdict reaches the run API, and outside a run, the store keeps none.
func TestRunVerdictChecksItsArguments(t *testing.T) {
	root := inCheckout(t)

	long := filepath.Join(root, "long.json")
	if err := os.WriteFile(long, make([]byte, 64<<10+1), 0o644); err != nil {
		t.Fatal(err)
	}

	absent := filepath.Join(root, "absent")
	points := func(content string) string {
		dir := t.TempDir()

		return writePoints(t, dir, content)
	}

	withMark := points("\xef\xbb\xbf" + verdictPoints)
	notUTF8 := points("[{\"text\": \"The checks failed \xff.\", \"action\": \"run_again\"}]")
	lone := points(`[{"text": "The checks failed \ud800.", "action": "run_again"}]`)

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "verdict", "maybe"}, `Pass passed, changes-requested or escalate. The verdict is "maybe".`},
		{[]string{"run", "verdict"}, "accepts 1 arg(s), received 0"},
		{[]string{"run", "verdict", "escalate"}, "Pass --points <file> with escalate: a JSON array of the points a person acts on."},
		{[]string{"run", "verdict", "passed", "--points", points(verdictPoints)}, "Leave out --points with passed: only escalate has points."},
		{[]string{"run", "verdict", "changes-requested", "--points", points(verdictPoints)}, "Leave out --points with changes-requested: only escalate has points."},
		{[]string{"run", "verdict", "escalate", "--points", absent}, "Pass a file that exists to --points. open " + absent + ": no such file or directory"},
		{[]string{"run", "verdict", "escalate", "--points", long}, "Shorten " + long + " to at most 65536 bytes. The file is larger."},
		{[]string{"run", "verdict", "escalate", "--points", points(`[{"text": "The checks failed.", "action": "run_again"}, {"text": "The checks failed."}]`)},
			"Write each point as {text, action}: point 2 has no action."},
		{[]string{"run", "verdict", "escalate", "--points", points(`[]`)}, "Write at least one point. The array is empty."},
		{[]string{"run", "verdict", "escalate", "--points", withMark}, "Save " + withMark + " without a byte order mark."},
		{[]string{"run", "verdict", "escalate", "--points", notUTF8}, "Save " + notUTF8 + " as UTF-8."},
		{[]string{"run", "verdict", "escalate", "--points", lone}, `Remove the escape of a lone surrogate, such as \ud800, from ` + lone + "."},
		{[]string{"run", "verdict", "escalate", "--points", points(`[{"text": "The spec does not say who sees the price.", "action": "answer"}]`)},
			"Add question, the id of an open question, to point 1: a point with the action answer has one."},
		{[]string{"run", "verdict", "escalate", "--points", points(`{"verdict": "escalate", "gaps": "Add the empty cart."}`)},
			`Write the points as a JSON array, such as [{"text": "…", "action": "run_again"}]. The file is a JSON object.`},
		{[]string{"run", "verdict", "passed", "--gaps", points(verdictPoints)}, "unknown flag: --gaps"},
	} {
		for _, inRun := range []bool{true, false} {
			var server *runAPIServer
			if inRun {
				server = startRunAPI(t)
			} else {
				t.Setenv("QORY_RUN_API", "")
			}

			_, err := run(t, c.args...)
			wantExit(t, err, cmd.ExitInput, c.want)

			if err != nil && strings.SplitN(err.Error(), "\n", 2)[0] != c.want {
				t.Errorf("%v: the first line is %q, want %q", c.args, err, c.want)
			}

			if server != nil && len(server.verdicts) != 0 {
				t.Errorf("%v: sent %q", c.args, server.verdicts)
			}
		}
	}

	if _, err := os.Stat(filepath.Join(root, ".qory", "artifacts", "verdict")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the store has a verdict: %v", err)
	}
}
