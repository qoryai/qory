package cmd_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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

	if out != "Not signed in to Qory Apiary; .qory/artifacts/ has the local versions only.\n" {
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

	if out != ".qory/artifacts/\n" {
		t.Errorf("out = %q", out)
	}

	store := filepath.Join(root, ".qory", "artifacts")
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

		want := fmt.Sprintf("Kept in .qory/artifacts/demo-video/main/v%d/; not sent, because you are not signed in to Qory Apiary.\n", number)
		if out != want {
			t.Errorf("out = %q", out)
		}
	}

	wantFile(t, filepath.Join(root, ".qory", "artifacts", "demo-video", "main", "v2", "a.md"), "bytes")
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

func TestRunVerdictStoresTheVerdictInARun(t *testing.T) {
	root := inCheckout(t)
	server := startRunAPI(t)
	server.response = `{"version":1,"artifact":{"type":"verdict","name":"main","version":{"id":"av_4","number":4}}}`

	gaps := filepath.Join(root, "gaps.md")
	if err := os.WriteFile(gaps, []byte("Add the empty cart."), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "run", "verdict", "changes-requested", "--gaps", gaps)
	if err != nil {
		t.Fatal(err)
	}

	if out != "Stored the verdict changes-requested as version 4.\n" {
		t.Errorf("out = %q", out)
	}

	if want := `{"verdict":"changes-requested","gaps":"Add the empty cart."}`; server.verdicts[0] != want {
		t.Errorf("body = %s", server.verdicts[0])
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
}

func TestRunVerdictChecksItsArguments(t *testing.T) {
	root := inCheckout(t)
	t.Setenv("QORY_RUN_API", "")

	long := filepath.Join(root, "long.md")
	if err := os.WriteFile(long, make([]byte, 64<<10+1), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "verdict", "maybe"}, "Pass passed, changes-requested or escalate."},
		{[]string{"run", "verdict"}, "accepts 1 arg"},
		{[]string{"run", "verdict", "passed", "--gaps", filepath.Join(root, "absent")}, "Pass a file that exists to --gaps."},
		{[]string{"run", "verdict", "passed", "--gaps", long}, "Shorten "},
	} {
		_, err := run(t, c.args...)
		wantExit(t, err, cmd.ExitInput, c.want)
	}
}
