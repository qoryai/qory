package pushcheck_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/pushcheck"
	"github.com/qoryai/qory/internal/render"
)

// engine stands in for the docker command. It records each command line, keeps a copy
// of the two folders the container mounts as they are at the create, runs start for the
// start, and prints state for the inspect.
type engine struct {
	mu       sync.Mutex
	commands [][]string
	// create, when set, is the create's standard error and error.
	create func(stderr io.Writer) error
	// start writes the program's output and returns the start's error; nil exits 0. It
	// gets the start's context.
	start func(ctx context.Context, stdout, stderr io.Writer) error
	state string
	// pulled and pushed are the files of the working folder and of the pushed files'
	// folder at the create, by path relative to the folder.
	pulled, pushed map[string]string
	// mounts are the --mount values of the create, and modes the mode of each source.
	mounts []string
	modes  map[string]fs.FileMode
}

func (e *engine) run(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
	e.mu.Lock()
	e.commands = append(e.commands, argv)
	e.mu.Unlock()
	switch argv[1] {
	case "create":
		e.modes = map[string]fs.FileMode{}
		for i, a := range argv {
			if i > 0 && argv[i-1] == "--mount" {
				e.mounts = append(e.mounts, a)
				src := strings.TrimPrefix(strings.Split(a, ",")[1], "src=")
				if info, err := os.Stat(src); err == nil {
					e.modes[src] = info.Mode().Perm()
				}
				switch {
				case strings.Contains(a, "dst="+pushcheck.WorkDir+","):
					e.pulled = tree(src)
				case strings.Contains(a, "dst="+pushcheck.FilesDir+","):
					e.pushed = tree(src)
				}
			}
		}
		if e.create != nil {
			return e.create(stderr)
		}
		fmt.Fprintln(stdout, "c0ffee")
	case "start":
		if e.start != nil {
			return e.start(ctx, stdout, stderr)
		}
	case "inspect":
		state := e.state
		if state == "" {
			state = `{"Status":"exited","ExitCode":0,"Error":""}`
		}
		fmt.Fprintln(stdout, state)
	}
	return nil
}

// verbs are the engine's commands in order, by their first word.
func (e *engine) verbs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, c := range e.commands {
		out = append(out, c[1])
	}
	return out
}

// created is the create's command line.
func (e *engine) created(t *testing.T) []string {
	t.Helper()
	for _, c := range e.commands {
		if c[1] == "create" {
			return c
		}
	}
	t.Fatalf("no create among %v", e.commands)
	return nil
}

// tree is the files under dir, by path relative to dir, with their contents.
func tree(dir string) map[string]string {
	out := map[string]string{}
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		data, _ := os.ReadFile(path)
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	return out
}

// exit is an engine start that writes out to standard output and errOut to standard
// error, and an inspect of a container that exited with code.
func (e *engine) exit(code int, out, errOut string) {
	e.start = func(_ context.Context, stdout, stderr io.Writer) error {
		io.WriteString(stdout, out)
		io.WriteString(stderr, errOut)
		if code != 0 {
			return errors.New("exit status")
		}
		return nil
	}
	e.state = fmt.Sprintf(`{"Status":"exited","ExitCode":%d,"Error":""}`, code)
}

// fixture is a home with a push check for spec that reads questions, a program at
// program, and a checker whose fetch records the types it was given and writes an
// index, and whose engine is e.
type fixture struct {
	home    string
	program string
	e       *engine
	c       *pushcheck.Checker
	fetched [][]string
}

func newFixture(t *testing.T, shebang string) *fixture {
	t.Helper()
	home := t.TempDir()
	program := filepath.Join(home, "modules", "core", "checks.js")
	if err := os.MkdirAll(filepath.Dir(program), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte(shebang+"\nprocess.exit(0)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fixture{home: home, program: program, e: &engine{}}
	f.c = &pushcheck.Checker{
		Home:   home,
		Checks: []compose.PushCheck{{Type: "spec", Module: "core", Command: []string{program, "push", "spec"}, Reads: []string{"questions"}}},
		Image:  "registry.example/agent@sha256:" + strings.Repeat("a", 64),
		User:   fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		Engine: pushcheck.Engine{Command: "/usr/local/bin/docker", Run: f.e.run},
		Fetch: func(_ context.Context, dir string, types []string) error {
			f.fetched = append(f.fetched, types)
			return os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"version":1,"artifacts":[]}`), 0o644)
		},
	}
	return f
}

// pushBody is a POST of /artifacts of artifactType with files, name to content.
func pushBody(t *testing.T, artifactType string, files ...[2]string) []byte {
	t.Helper()
	var items []map[string]any
	for _, f := range files {
		items = append(items, map[string]any{"filename": f[0], "content_type": "text/markdown", "content_base64": []byte(f[1])})
	}
	body, err := json.Marshal(map[string]any{"type": artifactType, "name": "main", "files": items})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// post checks a POST of path with body through the checker's RunAPICheck.
func (f *fixture) post(t *testing.T, path string, body []byte) (*session.RunAPIRefusal, error) {
	t.Helper()
	return f.c.RunAPICheck(context.Background(), session.RunAPIRequest{Path: path, Body: body})
}

// TestAHomeWithoutPushChecksHasNone is a home with no push-checks.json: Load returns
// no check and no error.
func TestAHomeWithoutPushChecksHasNone(t *testing.T) {
	checks, err := pushcheck.Load(t.TempDir())
	if err != nil || checks != nil {
		t.Fatalf("Load = %v, %v, want nil, nil", checks, err)
	}
}

// TestLoadReadsTheHomesPushChecks reads push-checks.json as the compose writes it.
func TestLoadReadsTheHomesPushChecks(t *testing.T) {
	home := t.TempDir()
	data, _ := json.Marshal(render.PushChecks{PushChecks: []compose.PushCheck{{Type: "spec", Module: "core", Command: []string{"/home/modules/core/c.js", "push"}, Reads: []string{"questions"}}}})
	if err := os.WriteFile(filepath.Join(home, render.PushChecksFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	checks, err := pushcheck.Load(home)
	if err != nil || len(checks) != 1 || checks[0].Type != "spec" || checks[0].Reads[0] != "questions" {
		t.Fatalf("Load = %+v, %v", checks, err)
	}
	if err := os.WriteFile(filepath.Join(home, render.PushChecksFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "Compose the harness again, and run again. The push checks in " + filepath.Join(home, render.PushChecksFile) + " could not be read: it is not JSON in the form the compose writes: unexpected end of JSON input."
	if _, err := pushcheck.Load(home); err == nil || err.Error() != want {
		t.Errorf("Load of a broken file = %v, want %q", err, want)
	}
}

// TestNoCheckRunsWithoutChecks is a checker with no push check: every push passes, and
// nothing is fetched or started.
func TestNoCheckRunsWithoutChecks(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.Checks = nil
	if r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"})); r != nil || err != nil {
		t.Fatalf("got %+v, %v, want nil, nil", r, err)
	}
	if len(f.fetched) != 0 || len(f.e.commands) != 0 {
		t.Errorf("fetched %v and ran %v, want nothing", f.fetched, f.e.commands)
	}
}

// TestOnlyAPushOfArtifactsIsChecked is a POST of /verdict and one of another path:
// both pass at once.
func TestOnlyAPushOfArtifactsIsChecked(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	for _, path := range []string{"/verdict", "/artifacts/av_1", "/artifactsx"} {
		if r, err := f.post(t, path, pushBody(t, "spec", [2]string{"spec.md", "x"})); r != nil || err != nil {
			t.Errorf("%s: got %+v, %v, want nil, nil", path, r, err)
		}
	}
	if len(f.fetched) != 0 || len(f.e.commands) != 0 {
		t.Errorf("fetched %v and ran %v, want nothing", f.fetched, f.e.commands)
	}
}

// wantRefusal fails the test unless r is a 422 push_check_failed with message and no
// finding, with no error.
func wantRefusal(t *testing.T, what string, r *session.RunAPIRefusal, err error, message string) {
	t.Helper()
	if err != nil || r == nil {
		t.Errorf("%s: got %+v, %v, want a refusal", what, r, err)
		return
	}
	if r.Status != 422 || r.Code != "push_check_failed" || r.Message != message || len(r.Findings) != 0 {
		t.Errorf("%s: the refusal is %d %s %q %q, want 422 push_check_failed %q", what, r.Status, r.Code, r.Message, r.Findings, message)
	}
}

// TestABodyThatIsNoPushGets422 is a body that is not a push in the run API's form:
// in a run whose home has push checks it gets 422 with the fix and the cause, whatever
// its type, and runs no check; in a run whose home has none it passes.
func TestABodyThatIsNoPushGets422(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	fix := "Send the push as qory artifact push sends it, and push again. The body is not a push: "
	for body, cause := range map[string]string{
		``:                            "it is not a JSON object.",
		`not json`:                    "it is not a JSON object.",
		`[]`:                          "it is not a JSON object.",
		`{"type":"spec"`:              "it is not JSON: unexpected end of JSON input.",
		`{"type":"spec"}`:             "files is missing or not a list.",
		`{"type":3,"files":[]}`:       "type is missing or not a string.",
		`{"type":"spec","files":{}}`:  "files is missing or not a list.",
		`{"type":"spec","files":[1]}`: "files[0]: it is not a JSON object.",
		`{"type":"spec","files":[{"content_base64":""}]}`:                              "files[0].filename is missing or not a string.",
		`{"type":"spec","files":[{"filename":"a.md","content_base64":"not base64!"}]}`: "files[0].content_base64 is missing or not base64.",
		`{"type":"spec","files":[]} trailing`:                                          "it has more after the JSON object.",
		`{"type":"questions","files":{}}`:                                              "files is missing or not a list.",
	} {
		r, err := f.post(t, "/artifacts", []byte(body))
		wantRefusal(t, fmt.Sprintf("%q", body), r, err, fix+cause)
	}
	if len(f.e.commands) != 0 || len(f.fetched) != 0 {
		t.Errorf("fetched %v and ran %v, want nothing", f.fetched, f.e.commands)
	}
	f.c.Checks = nil
	if r, err := f.post(t, "/artifacts", []byte(`not json`)); r != nil || err != nil {
		t.Errorf("a home with no push check: got %+v, %v, want nil, nil", r, err)
	}
}

// TestABodyReadInTwoWaysGets422 is a body with a member twice or a member whose name
// differs from the push's only in case: 422 with the fix.
func TestABodyReadInTwoWaysGets422(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	for _, body := range []string{
		`{"type":"spec","type":"other","files":[]}`,
		`{"type":"other","Type":"spec","files":[]}`,
		`{"type":"spec","files":[{"filename":"a.md","FileName":"b.md","content_base64":""}]}`,
		`{"type":"spec","files":[{"filename":"a.md","filename":"b.md","content_base64":""}]}`,
	} {
		r, err := f.post(t, "/artifacts", []byte(body))
		wantRefusal(t, body, r, err, "Send each member of the push once, in lower case, and push again.")
	}
	if len(f.e.commands) != 0 {
		t.Errorf("ran %v, want nothing", f.e.commands)
	}
}

// TestATypeWithoutACheckPasses is a push of a type the home has no check for.
func TestATypeWithoutACheckPasses(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	if r, err := f.post(t, "/artifacts", pushBody(t, "questions", [2]string{"q.json", "{}"})); r != nil || err != nil {
		t.Fatalf("got %+v, %v, want nil, nil", r, err)
	}
	if len(f.fetched) != 0 || len(f.e.commands) != 0 {
		t.Errorf("fetched %v and ran %v, want nothing", f.fetched, f.e.commands)
	}
}

// TestAPassingCheckLetsThePushGoOn is a check that exits 0: nil, nil, the body as it
// was, the pushed files decoded under their names, the fetch of the type and its reads,
// and the container removed after the inspect.
func TestAPassingCheckLetsThePushGoOn(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	body := pushBody(t, "spec", [2]string{"spec.md", "# Spec\n"}, [2]string{"notes.md", "notes\n"})
	sent := bytes.Clone(body)
	r, err := f.post(t, "/artifacts", body)
	if r != nil || err != nil {
		t.Fatalf("got %+v, %v, want nil, nil", r, err)
	}
	if !bytes.Equal(body, sent) {
		t.Errorf("the check changed the body:\n%s\nwant\n%s", body, sent)
	}
	if want := []string{"spec", "questions"}; len(f.fetched) != 1 || !slices.Equal(f.fetched[0], want) {
		t.Errorf("fetched %v, want %v", f.fetched, want)
	}
	if want := map[string]string{"spec.md": "# Spec\n", "notes.md": "notes\n"}; fmt.Sprint(f.e.pushed) != fmt.Sprint(want) {
		t.Errorf("the pushed files are %v, want %v", f.e.pushed, want)
	}
	if want := map[string]string{"index.json": `{"version":1,"artifacts":[]}`}; fmt.Sprint(f.e.pulled) != fmt.Sprint(want) {
		t.Errorf("the working folder has %v, want %v", f.e.pulled, want)
	}
	if want := []string{"create", "start", "inspect", "rm"}; !slices.Equal(f.e.verbs(), want) {
		t.Errorf("the engine ran %v, want %v", f.e.verbs(), want)
	}
}

// TestTheContainerIsClosedOff is the check's container: from the run's image, as the
// run's user, with no network, no capability, no variable passed, the home read-only at
// its path, the working folder and the files read-only, and the program with its
// arguments and the files' paths.
func TestTheContainerIsClosedOff(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.Label = "dev.qory.run=run-1"
	if _, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}, [2]string{"b.md", "y"})); err != nil {
		t.Fatal(err)
	}
	c := f.e.created(t)
	line := strings.Join(c, " ")
	if c[0] != "/usr/local/bin/docker" {
		t.Errorf("the engine's command is %s, want the wall's", c[0])
	}
	if slices.Contains(c, "--cpus") || slices.Contains(c, "--memory") {
		t.Errorf("a wall with no limits gives the check limits:\n%s", line)
	}
	for _, want := range []string{
		"--label dev.qory.run=run-1", "--pids-limit 512",
		"--network none", "--user " + f.c.User, "--cap-drop ALL", "--security-opt no-new-privileges", "--pull never",
		"--workdir " + pushcheck.WorkDir, "--entrypoint " + f.program + " " + f.c.Image + " push spec " + pushcheck.FilesDir + "/spec.md " + pushcheck.FilesDir + "/b.md",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the create lacks %q:\n%s", want, line)
		}
	}
	for _, flag := range []string{"-e", "--env", "--env-file", "--privileged", "--volume", "-v", "--network host"} {
		if slices.Contains(c, flag) || strings.Contains(line, " "+flag+" ") {
			t.Errorf("the create has %s:\n%s", flag, line)
		}
	}
	if len(f.e.mounts) != 3 {
		t.Fatalf("the mounts are %v, want 3", f.e.mounts)
	}
	if want := "type=bind,src=" + f.home + ",dst=" + f.home + ",readonly"; f.e.mounts[0] != want {
		t.Errorf("the home's mount is %s, want %s", f.e.mounts[0], want)
	}
	for _, m := range f.e.mounts {
		if !strings.HasSuffix(m, ",readonly") {
			t.Errorf("the mount %s is not read-only", m)
		}
	}
	// The folders are this user's alone, as the run's user is this user.
	for _, m := range f.e.mounts[1:] {
		src := strings.TrimPrefix(strings.Split(m, ",")[1], "src=")
		if mode := f.e.modes[src]; mode != 0o700 {
			t.Errorf("%s has mode %o, want 700", src, mode)
		}
		if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is left after the check: %v", src, err)
		}
	}
}

// TestTheWallsLimitsAreTheChecks is a wall with limits: the check's container gets its
// CPUs, its memory and its PIDs.
func TestTheWallsLimitsAreTheChecks(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.Limits = wall.Limits{CPUs: "1.5", Memory: "2g", PIDs: 64, ShmSize: "1g"}
	if _, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"})); err != nil {
		t.Fatal(err)
	}
	line := strings.Join(f.e.created(t), " ")
	for _, want := range []string{"--cpus 1.5", "--memory 2g", "--pids-limit 64"} {
		if !strings.Contains(line, want) {
			t.Errorf("the create lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "--shm-size") {
		t.Errorf("the create has --shm-size:\n%s", line)
	}
}

// TestTheTemporaryFoldersAreInTheCheckersDir is a checker with a Dir: both folders are
// made in it, and are gone after the check.
func TestTheTemporaryFoldersAreInTheCheckersDir(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.Dir = t.TempDir()
	if _, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"})); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.e.mounts[1:] {
		src := strings.TrimPrefix(strings.Split(m, ",")[1], "src=")
		if filepath.Dir(src) != f.c.Dir {
			t.Errorf("%s is not in %s", src, f.c.Dir)
		}
	}
	if left, _ := os.ReadDir(f.c.Dir); len(left) != 0 {
		t.Errorf("%s has %v after the check", f.c.Dir, left)
	}
}

// TestAnotherUserGetsFoldersItCanRead is a run whose user is not this process's, under
// the umask 077: the two folders, the folders in them and their files open to it.
func TestAnotherUserGetsFoldersItCanRead(t *testing.T) {
	defer syscall.Umask(syscall.Umask(0o077))
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.User = fmt.Sprintf("%d:%d", os.Getuid()+1, os.Getgid())
	f.c.Fetch = func(_ context.Context, dir string, _ []string) error {
		if err := os.MkdirAll(filepath.Join(dir, "spec", "main", "v1"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "spec", "main", "v1", "spec.md"), []byte("old"), 0o644)
	}
	var modes map[string]fs.FileMode
	f.e.start = func(context.Context, io.Writer, io.Writer) error {
		modes = map[string]fs.FileMode{}
		for _, m := range f.e.mounts[1:] {
			src := strings.TrimPrefix(strings.Split(m, ",")[1], "src=")
			filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
				if err == nil {
					info, _ := d.Info()
					rel, _ := filepath.Rel(src, path)
					modes[filepath.Base(src)+"/"+filepath.ToSlash(rel)] = info.Mode().Perm()
				}
				return err
			})
		}
		return nil
	}
	if _, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"})); err != nil {
		t.Fatal(err)
	}
	if len(modes) != 7 {
		t.Errorf("the folders have %v, want 7 entries", modes)
	}
	for path, mode := range modes {
		want := fs.FileMode(0o755)
		if strings.HasSuffix(path, ".md") {
			want = 0o644
		}
		if mode != want {
			t.Errorf("%s has mode %o, want %o", path, mode, want)
		}
	}
}

// TestAFindingStopsThePush is a check that exits 1 with findings on stdout: 422
// push_check_failed with the fix line, each line without its control characters, cut
// to 1000 characters and masked, and no empty line; stderr is not read.
func TestAFindingStopsThePush(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.Mask = func(s string) string { return strings.ReplaceAll(s, "s3cr3t-value", "***") }
	long := strings.Repeat("é", 1200)
	f.e.exit(1, "spec.md: the section Goal is missing\r\n\n  \x1b[31mred\x1b[0m line\tend\nkey s3cr3t-value\n"+long+"\n", "stderr line\n")
	r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
	if err != nil || r == nil {
		t.Fatalf("got %+v, %v, want a refusal", r, err)
	}
	if r.Status != 422 || r.Code != "push_check_failed" {
		t.Errorf("the refusal is %d %s, want 422 push_check_failed", r.Status, r.Code)
	}
	if want := "Fix each finding below and push again. The check for spec failed:"; r.Message != want {
		t.Errorf("the message is %q, want %q", r.Message, want)
	}
	want := []string{"spec.md: the section Goal is missing", "  [31mred[0m lineend", "key ***", strings.Repeat("é", 1000)}
	if !slices.Equal(r.Findings, want) {
		t.Errorf("the findings are %q, want %q", r.Findings, want)
	}
}

// TestStderrIsReadWhenStdoutIsEmpty is a check that prints its findings on stderr
// alone: the first finding says so, and the lines of stderr follow it, masked and
// without control characters, at most 200 findings with that line. A check that prints
// more than 200 lines on stdout has 200 findings.
func TestStderrIsReadWhenStdoutIsEmpty(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.Mask = func(s string) string { return strings.ReplaceAll(s, "s3cr3t-value", "***") }
	f.e.exit(2, "\n  \n", "first\x1b[0m\nkey s3cr3t-value\n")
	r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
	want := []string{"The check exited with 2 and printed nothing on stdout. Its stderr:", "first[0m", "key ***"}
	if err != nil || r == nil || !slices.Equal(r.Findings, want) {
		t.Fatalf("got %+v, %v, want %q", r, err, want)
	}
	f.e.exit(1, "", strings.Repeat("line\n", 250))
	r, err = f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
	if err != nil || r == nil || len(r.Findings) != 200 || r.Findings[0] != "The check exited with 1 and printed nothing on stdout. Its stderr:" || r.Findings[199] != "line" {
		t.Fatalf("got %d findings %q, %v, want the line and 199 of stderr", len(r.Findings), r.Findings, err)
	}
	f.e.exit(1, strings.Repeat("line\n", 250), "")
	r, err = f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
	if err != nil || r == nil || len(r.Findings) != 200 {
		t.Fatalf("got %d findings, %v, want 200", len(r.Findings), err)
	}
}

// TestACheckThatPrintsNothingHasOneFinding is a check that exits 3 and prints nothing,
// or blank lines alone: one finding with its exit.
func TestACheckThatPrintsNothingHasOneFinding(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	for _, out := range [][2]string{{"", ""}, {"\n \n", "\x1b\n"}} {
		f.e.exit(3, out[0], out[1])
		r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
		if want := []string{"The check exited with 3 and printed nothing."}; err != nil || r == nil || !slices.Equal(r.Findings, want) {
			t.Errorf("%q: got %+v, %v, want %q", out, r, err, want)
		}
	}
}

// TestThePushStepGetsItsLines is the line of each failure for the push step: the fix
// without "push again", then the cause; for a failed check, the cause alone.
func TestThePushStepGetsItsLines(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	files := []pushcheck.File{{Name: "spec.md", Content: []byte("x")}}
	f.e.exit(1, "a finding\n", "")
	failed, err := f.c.Check(context.Background(), "spec", files)
	if err != nil || failed == nil || failed.Step != "The check for spec failed:" || !slices.Equal(failed.Findings, []string{"a finding"}) {
		t.Errorf("a failed check: %+v, %v", failed, err)
	}
	f.e.exit(127, "", "/usr/bin/env: 'node': No such file or directory\n")
	failed, err = f.c.Check(context.Background(), "spec", files)
	if want := "Install node, or run in an image built FROM qory-agent. The check for spec could not start: /usr/bin/env: 'node': No such file or directory."; err != nil || failed == nil || failed.Step != want {
		t.Errorf("a check that did not start: %+v, %v, want %q", failed, err, want)
	}
	f.c.Limit = 50 * time.Millisecond
	f.e.start = func(ctx context.Context, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	failed, err = f.c.Check(context.Background(), "spec", files)
	if want := "Make the check for spec finish within 30 s. It ran longer and was stopped."; err != nil || failed == nil || failed.Step != want {
		t.Errorf("a check past its limit: %+v, %v, want %q", failed, err, want)
	}
}

// TestOutputIsCappedAt64KiB is a check that prints more than 64 KiB: what is past it is
// read and left out.
func TestOutputIsCappedAt64KiB(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.e.exit(1, strings.Repeat("x", 64<<10)+"past the cap\n", "")
	r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
	if err != nil || r == nil || len(r.Findings) != 1 || strings.Contains(r.Findings[0], "past") {
		t.Fatalf("got %+v, %v", r, err)
	}
}

// TestAProgramThatCannotStartStopsThePush is a program the container's init cannot
// start or execute, a node the image lacks, and an image the engine does not hold: 422 with the fix line
// for node only for a program whose #! line is for node.
func TestAProgramThatCannotStartStopsThePush(t *testing.T) {
	cases := []struct {
		name, shebang string
		set           func(e *engine)
		want          string
	}{
		{"no node", "#!/usr/bin/env node", func(e *engine) {
			e.exit(127, "", "/usr/bin/env: 'node': No such file or directory\n")
		}, "Install node, or run in an image built FROM qory-agent, and push again. The check for spec could not start: /usr/bin/env: 'node': No such file or directory."},
		{"no program", "#!/bin/sh", func(e *engine) {
			e.exit(127, "", "[FATAL tini (7)] exec /home/c failed: No such file or directory\n")
		}, "Install the program PROGRAM, and push again. The check for spec could not start: exec /home/c failed: No such file or directory."},
		{"not executable", "#!/bin/sh", func(e *engine) {
			e.exit(126, "", "[FATAL tini (7)] exec /home/c failed: Permission denied\n")
		}, "Install the program PROGRAM, and push again. The check for spec could not start: exec /home/c failed: Permission denied."},
		{"busybox env", "#!/usr/bin/env node", func(e *engine) {
			e.exit(127, "", "env: can't execute 'node': No such file or directory\n")
		}, "Install node, or run in an image built FROM qory-agent, and push again. The check for spec could not start: env: can't execute 'node': No such file or directory."},
		{"interpreter", "#!/usr/local/bin/deno run", func(e *engine) {
			e.exit(127, "", "sh: /usr/local/bin/deno: not found\n")
		}, "Install the program PROGRAM, and push again. The check for spec could not start: sh: /usr/local/bin/deno: not found."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.shebang)
			tc.set(f.e)
			r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
			if err != nil || r == nil {
				t.Fatalf("got %+v, %v, want a refusal", r, err)
			}
			want := strings.ReplaceAll(tc.want, "PROGRAM", f.program)
			if r.Status != 422 || r.Code != "push_check_failed" || r.Message != want || len(r.Findings) != 0 {
				t.Errorf("the refusal is %d %s %q %q, want 422 push_check_failed %q", r.Status, r.Code, r.Message, r.Findings, want)
			}
			if verbs := f.e.verbs(); verbs[len(verbs)-1] != "rm" {
				t.Errorf("the engine ran %v, want rm last", verbs)
			}
		})
	}
}

// TestAnExit126Or127OfTheCheckIsAFinding is a check that exits 126 or 127 for another
// reason than the start of its program, such as a program it runs that is not there:
// a failed check with its stderr as findings.
func TestAnExit126Or127OfTheCheckIsAFinding(t *testing.T) {
	for _, tc := range []struct {
		code   int
		stderr string
	}{
		{127, "/home/modules/core/checks.sh: 3: markdownlint: not found\n"},
		{127, "node:internal/modules/cjs/loader:1228\n  throw err;\nError: Cannot find module '/home/x.js'\n"},
		{126, "/home/modules/core/checks.sh: line 2: ./lint: Permission denied\n"},
		{126, ""},
	} {
		f := newFixture(t, "#!/bin/sh")
		f.e.exit(tc.code, "", tc.stderr)
		r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
		if err != nil || r == nil || r.Message != "Fix each finding below and push again. The check for spec failed:" || len(r.Findings) == 0 || !strings.HasPrefix(r.Findings[0], fmt.Sprintf("The check exited with %d and printed nothing", tc.code)) {
			t.Errorf("%d %q: got %+v, %v, want a failed check", tc.code, tc.stderr, r, err)
		}
	}
}

// TestACheckPastItsLimitIsStopped is a check that runs past its limit: 422 with the fix
// line of the limit, and the container removed whole.
func TestACheckPastItsLimitIsStopped(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.Limit = 50 * time.Millisecond
	f.e.start = func(ctx context.Context, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
	if err != nil || r == nil {
		t.Fatalf("got %+v, %v, want a refusal", r, err)
	}
	if want := "Make the check for spec finish within 30 s, and push again. It ran longer and was stopped."; r.Status != 422 || r.Code != "push_check_failed" || r.Message != want {
		t.Errorf("the refusal is %d %s %q, want %q", r.Status, r.Code, r.Message, want)
	}
	if want := []string{"create", "start", "rm"}; !slices.Equal(f.e.verbs(), want) {
		t.Errorf("the engine ran %v, want %v", f.e.verbs(), want)
	}
	rm := f.e.commands[len(f.e.commands)-1]
	if !slices.Contains(rm, "--force") || !strings.HasPrefix(rm[len(rm)-1], "qory-push-check-") || rm[len(rm)-1] != f.e.created(t)[3] {
		t.Errorf("the removal is %v, want docker rm --force of the container %s", rm, f.e.created(t)[3])
	}
}

// TestAFailureOfQorysWorkIsAnError is a fetch that fails, an engine whose create or
// inspect fails, a container whose init did not start, and a check whose caller's time ran out: errors, so the forwarder
// returns 503, with the container removed.
func TestAFailureOfQorysWorkIsAnError(t *testing.T) {
	cases := map[string]func(f *fixture){
		"fetch": func(f *fixture) {
			f.c.Fetch = func(context.Context, string, []string) error { return errors.New("GET /artifacts failed") }
		},
		"create": func(f *fixture) {
			f.e.create = func(stderr io.Writer) error {
				io.WriteString(stderr, "failed to connect to the docker API at unix:///var/run/docker.sock\n")
				return errors.New("exit status 1")
			}
		},
		"inspect": func(f *fixture) {
			f.e.state = "not json"
		},
		"running": func(f *fixture) {
			f.e.state = `{"Status":"running","ExitCode":0,"Error":""}`
		},
		"init": func(f *fixture) {
			f.e.state = `{"Status":"created","ExitCode":127,"Error":"failed to create task for container: OCI runtime create failed: error mounting: unknown"}`
		},
		"no image": func(f *fixture) {
			f.e.create = func(stderr io.Writer) error {
				io.WriteString(stderr, "Error response from daemon: No such image: registry.example/agent\n")
				return errors.New("exit status 1")
			}
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "#!/usr/bin/env node")
			set(f)
			r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
			if err == nil || r != nil {
				t.Fatalf("got %+v, %v, want an error", r, err)
			}
		})
	}
	f := newFixture(t, "#!/usr/bin/env node")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	f.e.start = func(ctx context.Context, _, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	}
	r, err := f.c.RunAPICheck(ctx, session.RunAPIRequest{Path: "/artifacts", Body: pushBody(t, "spec", [2]string{"spec.md", "x"})})
	if err == nil || r != nil {
		t.Fatalf("a check whose caller's time ran out got %+v, %v, want an error", r, err)
	}
	if verbs := f.e.verbs(); verbs[len(verbs)-1] != "rm" {
		t.Errorf("the engine ran %v, want rm last", verbs)
	}
}

// TestAFileNameTheCheckCannotWriteGets422 is a push of a type with a check whose
// file name is no name in a folder, is longer than 255 bytes, or repeats one: 422 with
// the fix and the name.
func TestAFileNameTheCheckCannotWriteGets422(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	fix := "Give each file a different name of at most 255 bytes, other than . and .., with no slash, backslash or control character, and push again. "
	long := strings.Repeat("a", 253) + ".md"
	for _, tc := range []struct {
		files [][2]string
		cause string
	}{
		{[][2]string{{"../x.md", "x"}}, `"../x.md" is not one.`},
		{[][2]string{{"a\\b.md", "x"}}, `"a\\b.md" is not one.`},
		{[][2]string{{"..", "x"}}, `".." is not one.`},
		{[][2]string{{"", "x"}}, `"" is not one.`},
		{[][2]string{{"a\nb.md", "x"}}, `"a\nb.md" is not one.`},
		{[][2]string{{long, "x"}}, `"` + long + `" is not one.`},
		{[][2]string{{"a.md", "x"}, {"a.md", "y"}}, `"a.md" is the name of two files.`},
	} {
		r, err := f.post(t, "/artifacts", pushBody(t, "spec", tc.files...))
		wantRefusal(t, fmt.Sprint(tc.files), r, err, fix+tc.cause)
	}
	if len(f.e.commands) != 0 {
		t.Errorf("ran %v, want nothing", f.e.commands)
	}
	if r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{strings.Repeat("a", 252) + ".md", "x"})); r != nil || err != nil {
		t.Errorf("a name of 255 bytes: got %+v, %v, want nil, nil", r, err)
	}
}

// oneFileHere reports whether a and b are one file in a folder of this machine's
// temporary folder, whose file system may ignore case or the form of an accent.
func oneFileHere(t *testing.T, a, b string) bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, a), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, b))
	return err == nil
}

// TestNamesThatAreOneFileHereGet422 is a push of A.md and a.md, and of café.md
// with its é as one character and as e and an accent: on a file system that ignores
// case, or the form of an accent, 422 with the fix and both names, and no check; on
// one that does not, the check gets both files.
func TestNamesThatAreOneFileHereGet422(t *testing.T) {
	nfc, nfd := "caf\u00e9.md", "cafe\u0301.md"
	for _, pair := range [][2]string{{"A.md", "a.md"}, {nfc, nfd}} {
		f := newFixture(t, "#!/usr/bin/env node")
		r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{pair[0], "one"}, [2]string{pair[1], "two"}))
		if !oneFileHere(t, pair[0], pair[1]) {
			if r != nil || err != nil || len(f.e.pushed) != 2 {
				t.Errorf("%q on a file system that keeps them apart: got %+v, %v, pushed %v", pair, r, err, f.e.pushed)
			}
			continue
		}
		wantRefusal(t, fmt.Sprintf("%q", pair), r, err, fmt.Sprintf("Give each file a different name, also when you ignore case and accents, and push again. %q and %q are one file on this machine.", pair[0], pair[1]))
		if len(f.e.commands) != 0 || len(f.fetched) != 0 {
			t.Errorf("%q: fetched %v and ran %v, want nothing", pair, f.fetched, f.e.commands)
		}
	}
}

// TestTheEngineRunsWithoutTheAccessKey runs the engine's command on this machine: it
// gets no access key variable and no run key.
func TestTheEngineRunsWithoutTheAccessKey(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "env")
	docker := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nenv >> " + log + "\ncase \"$1\" in\ncreate) echo c ;;\ninspect) echo '{\"Status\":\"exited\",\"ExitCode\":0,\"Error\":\"\"}' ;;\nesac\n"
	if err := os.WriteFile(docker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QORY_RUN_KEY", "qrk_test")
	t.Setenv("QORY_ACCESS_KEY_SECRET", "qak_test")
	t.Setenv("DOCKER_HOST", "unix:///test.sock")
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.Engine = pushcheck.Engine{Command: docker}
	if r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"})); r != nil || err != nil {
		t.Fatalf("got %+v, %v, want nil, nil", r, err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "qrk_test") || strings.Contains(string(data), "qak_test") {
		t.Errorf("the engine's command got a key:\n%s", data)
	}
	if !strings.Contains(string(data), "DOCKER_HOST=unix:///test.sock") {
		t.Errorf("the engine's command lacks the engine's selection:\n%s", data)
	}

	// With the wall's selection, the command gets it in place of this process's.
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONTEXT", "other")
	f.c.Engine.Env = []string{"DOCKER_HOST=unix:///wall.sock", "DOCKER_CONFIG=/wall/config"}
	if r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"})); r != nil || err != nil {
		t.Fatalf("got %+v, %v, want nil, nil", r, err)
	}
	if data, err = os.ReadFile(log); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DOCKER_HOST=unix:///wall.sock\n", "DOCKER_CONFIG=/wall/config\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the engine's command lacks %q:\n%s", want, data)
		}
	}
	for _, unwanted := range []string{"test.sock", "DOCKER_CONTEXT", "qrk_test", "qak_test"} {
		if strings.Contains(string(data), unwanted) {
			t.Errorf("the engine's command has %q:\n%s", unwanted, data)
		}
	}
}

// realImage is the image of [TestARealCheckRunsInTheEngine], which has /bin/sh and no
// node.
const realImage = "public.ecr.aws/docker/library/alpine:3.22"

// TestARealCheckRunsInTheEngine runs checks in containers of this machine's engine: one
// that reads the working folder and the pushed file and passes, one that prints a
// finding, one whose node the image lacks, and one past its limit, whose container is
// removed. It runs only with QORY_TEST_DOCKER=1, docker on the PATH and the image
// present.
func TestARealCheckRunsInTheEngine(t *testing.T) {
	if os.Getenv("QORY_TEST_DOCKER") != "1" {
		t.Skip("QORY_TEST_DOCKER=1 runs it")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	if err := exec.Command("docker", "image", "inspect", realImage).Run(); err != nil {
		t.Skipf("the engine does not hold %s", realImage)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		path := filepath.Join(home, "modules", "core", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	checker := func(program string) *pushcheck.Checker {
		return &pushcheck.Checker{
			Home:   home,
			Checks: []compose.PushCheck{{Type: "spec", Module: "core", Command: []string{program, "push"}}},
			Image:  realImage,
			User:   fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			Label:  "dev.qory.run=run-push-check-test",
			Limits: wall.Limits{CPUs: "1", Memory: "256m"},
			Fetch: func(_ context.Context, dir string, _ []string) error {
				if err := os.MkdirAll(filepath.Join(dir, "spec", "main", "v1"), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(dir, "spec", "main", "v1", "spec.md"), []byte("old\n"), 0o644); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"version":1}`), 0o644)
			},
		}
	}
	body := pushBody(t, "spec", [2]string{"spec.md", "# Spec\n"})
	check := func(c *pushcheck.Checker) *session.RunAPIRefusal {
		t.Helper()
		r, err := c.RunAPICheck(context.Background(), session.RunAPIRequest{Path: "/artifacts", Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	passes := write("passes", "#!/bin/sh\nset -e\ntest \"$(pwd)\" = "+pushcheck.WorkDir+"\ngrep -q version index.json\ngrep -q old spec/main/v1/spec.md\ntest \"$1\" = push\ngrep -q Spec \"$2\"\n! touch \"$2\" 2>/dev/null\n! wget -q -T 2 http://example.com 2>/dev/null\n")
	if r := check(checker(passes)); r != nil {
		t.Errorf("the passing check got %+v", r)
	}
	finds := write("finds", "#!/bin/sh\necho \"$(basename \"$2\"): a finding\"\nexit 1\n")
	if r := check(checker(finds)); r == nil || len(r.Findings) != 1 || r.Findings[0] != "spec.md: a finding" {
		t.Errorf("the failing check got %+v", r)
	}
	node := write("node", "#!/usr/bin/env node\n")
	if r := check(checker(node)); r == nil || !strings.HasPrefix(r.Message, "Install node, or run in an image built FROM qory-agent, and push again. The check for spec could not start: ") {
		t.Errorf("the check without node got %+v", r)
	}
	lint := write("lint", "#!/bin/sh\nnosuchlinter \"$2\"\n")
	if r := check(checker(lint)); r == nil || r.Message != "Fix each finding below and push again. The check for spec failed:" || len(r.Findings) != 2 || r.Findings[0] != "The check exited with 127 and printed nothing on stdout. Its stderr:" || !strings.Contains(r.Findings[1], "nosuchlinter") {
		t.Errorf("the check whose linter is not there got %+v", r)
	}
	missing := filepath.Join(home, "modules", "core", "missing")
	if r := check(checker(missing)); r == nil || !strings.HasPrefix(r.Message, "Install the program "+missing+", and push again. The check for spec could not start: exec "+missing+" failed: No such file or directory.") {
		t.Errorf("the missing program got %+v", r)
	}
	slow := write("slow", "#!/bin/sh\nsleep 30\n")
	c := checker(slow)
	c.Limit = 2 * time.Second
	if r := check(c); r == nil || r.Message != "Make the check for spec finish within 30 s, and push again. It ran longer and was stopped." {
		t.Errorf("the slow check got %+v", r)
	}
	out, err := exec.Command("docker", "ps", "--all", "--quiet", "--filter", "name=qory-push-check-").Output()
	if err != nil || len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("containers of checks are left: %q, %v", out, err)
	}
}
