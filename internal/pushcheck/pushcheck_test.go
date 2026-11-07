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
	"testing"
	"time"

	"github.com/qoryai/forager/session"

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
	if _, err := pushcheck.Load(home); err == nil || !strings.HasPrefix(err.Error(), "Compose the harness again.") {
		t.Errorf("Load of a broken file = %v, want the fix first", err)
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

// TestABodyThatIsNoPushPasses is a body the run API returns an error for: it passes
// unchecked, so the run API returns that error.
func TestABodyThatIsNoPushPasses(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	for _, body := range []string{
		``, `not json`, `[]`, `{"type":"spec"}`, `{"type":"spec","files":{}}`,
		`{"type":"spec","files":[{"filename":"a.md","content_base64":"not base64!"}]}`,
		`{"type":"spec","files":[]} trailing`,
	} {
		if r, err := f.post(t, "/artifacts", []byte(body)); r != nil || err != nil {
			t.Errorf("%q: got %+v, %v, want nil, nil", body, r, err)
		}
	}
	if len(f.e.commands) != 0 {
		t.Errorf("ran %v, want nothing", f.e.commands)
	}
}

// TestABodyReadInTwoWaysFails is a body with a member twice or a member whose name
// differs from the push's only in case: an error, so the forwarder returns 503.
func TestABodyReadInTwoWaysFails(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	for _, body := range []string{
		`{"type":"spec","type":"other","files":[]}`,
		`{"type":"other","Type":"spec","files":[]}`,
		`{"type":"spec","files":[{"filename":"a.md","FileName":"b.md","content_base64":""}]}`,
	} {
		if r, err := f.post(t, "/artifacts", []byte(body)); r != nil || err == nil {
			t.Errorf("%q: got %+v, %v, want an error", body, r, err)
		}
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
	if _, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}, [2]string{"b.md", "y"})); err != nil {
		t.Fatal(err)
	}
	c := f.e.created(t)
	line := strings.Join(c, " ")
	if c[0] != "/usr/local/bin/docker" {
		t.Errorf("the engine's command is %s, want the wall's", c[0])
	}
	for _, want := range []string{
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

// TestAnotherUserGetsFoldersItCanRead is a run whose user is not this process's: the
// two folders open to it.
func TestAnotherUserGetsFoldersItCanRead(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.c.User = fmt.Sprintf("%d:%d", os.Getuid()+1, os.Getgid())
	if _, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"})); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.e.mounts[1:] {
		src := strings.TrimPrefix(strings.Split(m, ",")[1], "src=")
		if mode := f.e.modes[src]; mode != 0o755 {
			t.Errorf("%s has mode %o, want 755", src, mode)
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
// alone, and one that prints more than 200 lines.
func TestStderrIsReadWhenStdoutIsEmpty(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	f.e.exit(2, "\n", "first\nsecond\n")
	r, err := f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
	if err != nil || r == nil || !slices.Equal(r.Findings, []string{"first", "second"}) {
		t.Fatalf("got %+v, %v, want the two lines of stderr", r, err)
	}
	f.e.exit(1, strings.Repeat("line\n", 250), "")
	r, err = f.post(t, "/artifacts", pushBody(t, "spec", [2]string{"spec.md", "x"}))
	if err != nil || r == nil || len(r.Findings) != 200 {
		t.Fatalf("got %d findings, %v, want 200", len(r.Findings), err)
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
			e.exit(126, "", "")
		}, "Install the program PROGRAM, and push again. The check for spec could not start: it exited with 126."},
		{"no image", "#!/usr/bin/env node", func(e *engine) {
			e.create = func(stderr io.Writer) error {
				io.WriteString(stderr, "Error response from daemon: No such image: registry.example/agent\n")
				return errors.New("exit status 1")
			}
		}, "Install node, or run in an image built FROM qory-agent, and push again. The check for spec could not start: Error response from daemon: No such image: registry.example/agent."},
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

// TestAFileNameTheCheckCannotWriteIsAnError is a push of a type with a check whose
// file name is no name in a folder, or repeats one.
func TestAFileNameTheCheckCannotWriteIsAnError(t *testing.T) {
	f := newFixture(t, "#!/usr/bin/env node")
	for _, files := range [][][2]string{{{"../x.md", "x"}}, {{"a.md", "x"}, {"a.md", "y"}}} {
		if r, err := f.post(t, "/artifacts", pushBody(t, "spec", files...)); err == nil || r != nil {
			t.Errorf("%v: got %+v, %v, want an error", files, r, err)
		}
	}
	if len(f.e.commands) != 0 {
		t.Errorf("ran %v, want nothing", f.e.commands)
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
