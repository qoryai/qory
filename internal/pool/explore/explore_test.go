package explore_test

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
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
)

// The fake qory's script and reports are in the runs' configuration directory, the
// one place of the test's that its allow-listed environment names.
const (
	scriptFile = "fake.json"
	readyFile  = "ready"
)

// script is what the fake qory does.
type script struct {
	// RunID is the run whose record qory run makes, with Record.
	RunID string
	// KeySum is the SHA-256 of the key qory run expects on descriptor 3, so the
	// script holds no key for the leak sweep to find.
	KeySum string
	// Inputs are what qory run expects in inputs.json.
	Inputs string
	// Compose is qory harness compose's exit status.
	Compose int
	// ComposeRecord makes qory harness compose make the run's record, so qory run
	// finds one there before it.
	ComposeRecord bool
	// Exit is qory run's exit status.
	Exit int
	// Record makes qory run make the run's record.
	Record bool
	// Mode is "", exit at once; "block", wait for SIGTERM and exit 1, as qory run
	// does; "kill", end on SIGKILL.
	Mode string
}

// report is what the fake qory saw, written to report-<verb>.json.
type report struct {
	Env       []string
	Args      []string
	Dir       string
	FD3Pipe   bool
	KeyOK     bool
	StdinNull bool
	InputsOK  bool
	Problems  []string
}

// fd3Pipe reports whether descriptor 3 is open and a pipe, as the key's is. The fake
// checks it before it opens any file. Without a key, descriptor 3 may be open with
// something else: on Linux the Go runtime opens the cgroup's cpu.max as it starts,
// and takes the lowest free descriptor.
func fd3Pipe() bool {
	var st syscall.Stat_t
	return syscall.Fstat(child.KeyFD, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFIFO
}

func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "init" {
		os.Exit(fakeGit())
	}
	if len(os.Args) > 1 && (os.Args[1] == "harness" || os.Args[1] == "run") {
		os.Exit(fakeQory(fd3Pipe()))
	}
	os.Exit(m.Run())
}

// fakeGit is git init --quiet <dir>: it reports its environment beside the fake qory's
// and makes <dir>/.git.
func fakeGit() int {
	rep, _ := json.Marshal(report{Env: os.Environ(), Args: os.Args[1:]})
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "report-init.json"), rep, 0o600); err != nil {
		return 95
	}
	if err := os.MkdirAll(filepath.Join(os.Args[len(os.Args)-1], ".git"), 0o700); err != nil {
		return 96
	}
	return 0
}

// fakeQory is qory harness compose or qory run, as the script in $XDG_CONFIG_HOME says.
func fakeQory(fd3 bool) int {
	config := os.Getenv("XDG_CONFIG_HOME")
	b, err := os.ReadFile(filepath.Join(config, scriptFile))
	if err != nil {
		return 90
	}
	var s script
	if err := json.Unmarshal(b, &s); err != nil {
		return 91
	}
	verb := os.Args[1]
	cwd, _ := os.Getwd()
	rep := report{Env: os.Environ(), Args: os.Args[1:], Dir: cwd}
	if st, err := os.Stdin.Stat(); err == nil {
		if null, err := os.Stat(os.DevNull); err == nil {
			rep.StdinNull = os.SameFile(st, null)
		}
	}
	rep.FD3Pipe = fd3
	for _, a := range os.Args {
		if accesskey.ContainsSecret(a) {
			rep.Problems = append(rep.Problems, "an argument holds a secret")
		}
	}
	for _, kv := range rep.Env {
		if accesskey.ContainsSecret(kv) {
			rep.Problems = append(rep.Problems, "a variable holds a secret")
		}
	}
	code := s.Compose
	if verb == "run" {
		code = s.Exit
		if rep.FD3Pipe {
			got := make(chan []byte, 1)
			go func() {
				k, _ := io.ReadAll(os.NewFile(uintptr(child.KeyFD), "key"))
				got <- k
			}()
			select {
			case k := <-got:
				rep.KeyOK = sum(k) == s.KeySum
			case <-time.After(5 * time.Second):
				rep.Problems = append(rep.Problems, "descriptor 3 never reached its end")
			}
		}
		if i := slices.Index(os.Args, "--add-dir"); i > 0 {
			in, err := os.ReadFile(filepath.Join(os.Args[i+1], "inputs.json"))
			rep.InputsOK = err == nil && string(in) == s.Inputs
		}
		if s.Record {
			if err := os.MkdirAll(fakeRecord(cwd, s.RunID), 0o700); err != nil {
				return 94
			}
		}
	} else {
		if i := slices.Index(os.Args, "--home"); i > 0 {
			os.MkdirAll(filepath.Join(os.Args[i+1], "claude"), 0o700)
		}
		if s.ComposeRecord {
			os.MkdirAll(fakeRecord(cwd, s.RunID), 0o700)
		}
	}
	out, _ := json.Marshal(rep)
	if err := os.WriteFile(filepath.Join(config, "report-"+verb+".json"), out, 0o600); err != nil {
		return 92
	}
	fmt.Println(verb, "says hello")
	fmt.Fprintln(os.Stderr, verb, "warns")
	if verb != "run" {
		return code
	}
	switch s.Mode {
	case "block":
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		os.WriteFile(filepath.Join(config, readyFile), nil, 0o600)
		<-c
		return 1
	case "kill":
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
		time.Sleep(time.Minute)
	}
	return code
}

// sum is the SHA-256 of b, in hex.
func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeRecord is the folder of the record of the run runID started in cwd, as qory run's
// documentation says it, computed apart from the launcher's: under $XDG_STATE_HOME/qory
// when that is an absolute path, else under $HOME/.local/state/qory.
func fakeRecord(cwd, runID string) string {
	state := filepath.Join(os.Getenv("HOME"), ".local", "state", "qory")
	if base := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(base) {
		state = filepath.Join(base, "qory")
	}
	real, _ := filepath.EvalSymlinks(cwd)
	return filepath.Join(state, "runs", filepath.Base(real)+"-"+sum([]byte(real))[:12], runID)
}

// testKey is a run key's shape, with no meaning.
const testKey = "qrk_eyJ0ZXN0IjoidGhlIGV4cGxvcmUgbGF1bmNoZXIifQ.c2lnbmF0dXJlLW9mLW5vdGhpbmc"

// testRunID is a run id.
const testRunID = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"

// testRef is a harness ref, a commit id.
const testRef = "708c43c0c0ffee0000000000000000000000beef"

// testInputs are an explore run's inputs.
const testInputs = `{"version":1,"work_item":{"title":"Add a search box to the shop"}}`

// holder is a key holder, as the provisioner client's.
type holder struct {
	b       []byte
	cleared int
}

func (h *holder) Bytes() []byte { return h.b }
func (h *holder) Clear()        { clear(h.b); h.cleared++ }

// env is one launch's directories and log.
type env struct {
	settings explore.Settings
	home     string
	// elsewhere is the program's XDG_STATE_HOME, which no command may get.
	elsewhere string
	mu        sync.Mutex
	logs      []string
}

func (e *env) log(line string) { e.mu.Lock(); e.logs = append(e.logs, line); e.mu.Unlock() }

// lines is what was logged.
func (e *env) lines() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.logs)
}

// newEnv is a launch's directories, with HOME at one of them, and the fake qory's
// script s, and the program's environment holding what no command may get.
func newEnv(t *testing.T, s script) *env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{home: filepath.Join(root, "home"), elsewhere: filepath.Join(root, "elsewhere")}
	e.settings = explore.Settings{
		State: filepath.Join(root, "state"), HarnessRef: testRef, RunConfig: filepath.Join(root, "runconfig"),
		Qory: os.Args[0], Log: e.log, Grace: time.Second,
	}
	for _, d := range []string{e.home, e.settings.RunConfig} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", e.home)
	t.Setenv("QORY_RUN_KEY", testKey)
	t.Setenv("QORY_ACCESS_KEY_SECRET", "qak_notakey")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("XDG_STATE_HOME", e.elsewhere)
	if s.RunID == "" {
		s.RunID = testRunID
	}
	s.KeySum, s.Inputs = sum([]byte(testKey)), testInputs
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(e.settings.RunConfig, scriptFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

// launch runs Launch with the test's run, and cancels ctx once the fake qory run waits
// when stop is set.
func (e *env) launch(t *testing.T, stop bool) (explore.Result, error, *holder) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if stop {
		go func() {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(e.settings.RunConfig, readyFile)); err == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			cancel()
		}()
	}
	h := &holder{b: []byte(testKey)}
	res, err := explore.Launch(ctx, e.settings, explore.Run{RunID: testRunID, Key: h, Inputs: []byte(testInputs)})
	if h.cleared == 0 || !bytes.Equal(h.b, make([]byte, len(testKey))) {
		t.Error("the key's holder was not cleared")
	}
	return res, err, h
}

// report is the fake qory's report of the verb, ok false when it did not run.
func (e *env) report(t *testing.T, verb string) (report, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.settings.RunConfig, "report-"+verb+".json"))
	if err != nil {
		return report{}, false
	}
	var r report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r, true
}

// noLeak fails when the key, or what starts a secret, is in a log line, in err, in a
// command's arguments or environment, or in a file under the launch's directories.
func (e *env) noLeak(t *testing.T, err error) {
	t.Helper()
	texts := e.lines()
	if err != nil {
		texts = append(texts, err.Error())
	}
	for _, verb := range []string{"harness", "run"} {
		if r, ok := e.report(t, verb); ok {
			texts = append(append(texts, r.Args...), r.Env...)
			for _, p := range r.Problems {
				t.Errorf("qory %s: %s", verb, p)
			}
		}
	}
	for _, s := range texts {
		if strings.Contains(s, testKey[4:24]) || accesskey.ContainsSecret(s) {
			t.Errorf("a secret is in %q", s)
		}
	}
	// What qory keeps of a run is in the run directory's state/ alone: nothing under
	// the user's state directory, or the program's.
	for _, dir := range []string{filepath.Join(e.home, ".local", "state"), e.elsewhere} {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Errorf("%s was made: %v", dir, err)
		}
	}
	for _, dir := range []string{e.settings.State, e.settings.RunConfig, e.home, e.elsewhere} {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s: %v", path, err)
				return nil
			}
			if bytes.Contains(b, []byte(testKey[4:24])) || accesskey.ContainsSecret(string(b)) {
				t.Errorf("a secret is in the file %s", path)
			}
			return nil
		})
	}
}

func TestLaunchRows(t *testing.T) {
	released := explore.Ending{Release: true, Retry: false, ReleaseReason: "the run could not be started on this machine"}
	for _, c := range []struct {
		name   string
		script script
		stop   bool
		record bool
		run    child.Result
		want   explore.Ending
		err    bool
	}{
		{"exit 0", script{Record: true}, false, true, child.Result{}, explore.Ending{Post: true, State: "succeeded"}, false},
		{"time limit", script{Record: true, Exit: 124}, false, true, child.Result{ExitCode: 124}, explore.Ending{Post: true, State: "failed", Reason: "time_limit"}, false},
		{"stopped", script{Record: true, Mode: "block"}, true, true, child.Result{ExitCode: 1, Stopped: true}, explore.Ending{Post: true, State: "cancelled", Reason: "pool_stopped"}, false},
		{"exit 1 by itself", script{Record: true, Exit: 1}, false, true, child.Result{ExitCode: 1}, explore.Ending{Post: true, State: "failed"}, false},
		{"other exit", script{Record: true, Exit: 3}, false, true, child.Result{ExitCode: 3}, explore.Ending{Post: true, State: "failed"}, false},
		{"signal", script{Record: true, Mode: "kill"}, false, true, child.Result{ExitCode: -1, Signal: syscall.SIGKILL}, explore.Ending{Post: true, State: "failed"}, false},
		{"refused, no record", script{Exit: 2}, false, false, child.Result{ExitCode: 2}, released, false},
		{"exit 0, no record", script{}, false, false, child.Result{}, released, false},
		{"stopped, no record", script{Mode: "block"}, true, false, child.Result{ExitCode: 1, Stopped: true}, released, false},
		{"compose failed", script{Compose: 1, Record: true}, false, false, child.Result{}, released, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, c.script)
			res, err, _ := e.launch(t, c.stop)
			if (err != nil) != c.err {
				t.Fatalf("err %v", err)
			}
			if res.Record != c.record || res.Run != c.run || res.Ending != c.want {
				t.Errorf("result %+v, want record %v, run %+v, ending %+v", res, c.record, c.run, c.want)
			}
			if _, ran := e.report(t, "run"); ran == c.err {
				t.Errorf("qory run ran: %v", ran)
			}
			e.noLeak(t, err)
		})
	}
}

// TestLaunchCommands runs qory harness compose and qory run with their arguments, in
// work, with the allowed environment, whose PATH has no entry in the pool's runs folder
// and keeps the others as written.
func TestLaunchCommands(t *testing.T) {
	t.Setenv("DOCKER_CONTEXT", "orbstack")
	e := newEnv(t, script{Record: true})
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join([]string{filepath.Join(explore.RunsDir(e.settings.State), testRunID, "work"), "/usr/bin", "", "bin", "/bin"}, sep))
	res, err, _ := e.launch(t, false)
	if err != nil {
		t.Fatal(err)
	}
	dir := explore.Dir(e.settings.State, testRunID)
	if res.Dir != dir {
		t.Errorf("dir %s, want %s", res.Dir, dir)
	}
	in := filepath.Join(dir, "in")
	run, _ := e.report(t, "run")
	want := []string{
		"run", "claude", "--home", filepath.Join(dir, "homes"), "--run-key-fd", "3", "--wall", "docker",
		"--mount", in + ":ro", "--timeout", "12m", "--", "--max-turns", "40", "--add-dir", in, "-p",
		"Explore the ticket described in " + in + "/inputs.json, following the explore-an-item skill. End with the explore v1 JSON document alone as your final response.",
	}
	if !slices.Equal(run.Args, want) {
		t.Errorf("qory run's arguments\n%q\nwant\n%q", run.Args, want)
	}
	if !run.FD3Pipe || !run.KeyOK || !run.StdinNull || !run.InputsOK || run.Dir != filepath.Join(dir, "work") {
		t.Errorf("qory run's report %+v", run)
	}
	compose, _ := e.report(t, "harness")
	if want := []string{"harness", "compose", "-f", filepath.Join(dir, "stack", "qory-stack.yaml"), "--runtime", "claude", "--home", filepath.Join(dir, "homes")}; !slices.Equal(compose.Args, want) {
		t.Errorf("qory harness compose's arguments %q, want %q", compose.Args, want)
	}
	if compose.FD3Pipe || !compose.StdinNull || compose.Dir != filepath.Join(dir, "work") {
		t.Errorf("qory harness compose's report %+v", compose)
	}
	for _, r := range []report{run, compose} {
		var names []string
		for _, kv := range r.Env {
			names = append(names, kv[:strings.IndexByte(kv, '=')])
		}
		for _, n := range names {
			if !slices.Contains([]string{"PATH", "HOME", "TMPDIR", "LANG", "DOCKER_HOST", "DOCKER_CONTEXT", "XDG_CONFIG_HOME", "XDG_STATE_HOME"}, n) {
				t.Errorf("%s got %s", r.Args[0], n)
			}
		}
		if !slices.Contains(r.Env, "XDG_CONFIG_HOME="+e.settings.RunConfig) || !slices.Contains(r.Env, "XDG_STATE_HOME="+filepath.Join(dir, "state")) || !slices.Contains(r.Env, "DOCKER_CONTEXT=orbstack") ||
			!slices.Contains(r.Env, "PATH=/usr/bin"+sep+"/bin") {
			t.Errorf("%s's environment %q", r.Args[0], r.Env)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "work", ".git")); err != nil {
		t.Errorf("work is no git working tree: %v", err)
	}
	for _, m := range []struct {
		path string
		mode fs.FileMode
	}{{filepath.Join(e.settings.State, "pool"), 0o700}, {filepath.Join(e.settings.State, "pool", "runs"), 0o700}, {dir, 0o700}, {in, 0o700}, {filepath.Join(dir, "stack"), 0o700}, {filepath.Join(dir, "state"), 0o700}, {filepath.Join(in, "inputs.json"), 0o600}, {filepath.Join(dir, "stack", "qory-stack.yaml"), 0o600}} {
		st, err := os.Stat(m.path)
		if err != nil || st.Mode().Perm() != m.mode {
			t.Errorf("%s: mode %v, %v; want %v", m.path, st.Mode().Perm(), err, m.mode)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(in, "inputs.json")); string(b) != testInputs {
		t.Errorf("inputs.json %q", b)
	}
	stack, _ := os.ReadFile(filepath.Join(dir, "stack", "qory-stack.yaml"))
	wantStack := "apiVersion: qory.dev/v1alpha1\nname: explore\nmodules:\n    - name: explore-an-item\n      source:\n        path: harness/explore-an-item\n        git: https://github.com/qoryai/qory-github\n        ref: " + testRef + "\n"
	if string(stack) != wantStack {
		t.Errorf("the stack\n%s\nwant\n%s", stack, wantStack)
	}
	lines := e.lines()
	for _, want := range []string{testRunID + ": harness says hello", testRunID + ": harness warns", testRunID + ": run says hello", testRunID + ": run warns"} {
		if !slices.Contains(lines, want) {
			t.Errorf("no log line %q in %q", want, lines)
		}
	}
	e.noLeak(t, err)
}

// TestHostProgramsByNameAreFoundOnTheCleanedPATH finds git, when Git is not set, and
// qory, when Qory is a name alone, on the PATH every command gets: with an empty file of
// each name in an entry in the runs folder alone, neither is found, the launch fails
// with exec's error at that program's step, and no later step runs.
func TestHostProgramsByNameAreFoundOnTheCleanedPATH(t *testing.T) {
	for _, c := range []struct {
		name, want string
		edit       func(*explore.Settings)
		ran        []string
	}{
		{"git", "git init: ", func(s *explore.Settings) { s.Git = "" }, nil},
		{"qory", "qory harness compose could not be started: ", func(s *explore.Settings) { s.Git, s.Qory = os.Args[0], "qory" }, []string{"init"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, script{Record: true})
			c.edit(&e.settings)
			hostbin := filepath.Join(explore.RunsDir(e.settings.State), "hostbin")
			if err := os.MkdirAll(hostbin, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, n := range []string{"git", "qory"} {
				if err := os.WriteFile(filepath.Join(hostbin, n), nil, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", hostbin)
			_, err, _ := e.launch(t, false)
			if !errors.Is(err, exec.ErrNotFound) || !strings.HasPrefix(err.Error(), c.want) {
				t.Errorf("%v, want exec.ErrNotFound after %q", err, c.want)
			}
			for _, step := range []string{"init", "harness", "run"} {
				if _, ran := e.report(t, step); ran != slices.Contains(c.ran, step) {
					t.Errorf("%s ran %v", step, ran)
				}
			}
		})
	}
}

func TestGitInit(t *testing.T) {
	e := newEnv(t, script{Record: true})
	e.settings.Git = os.Args[0]
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join([]string{explore.RunsDir(e.settings.State), "/usr/bin", "bin"}, sep))
	if _, err, _ := e.launch(t, false); err != nil {
		t.Fatal(err)
	}
	dir := explore.Dir(e.settings.State, testRunID)
	r, ok := e.report(t, "init")
	if !ok {
		t.Fatal("git init did not run")
	}
	if want := []string{"init", "--quiet", filepath.Join(dir, "work")}; !slices.Equal(r.Args, want) {
		t.Errorf("git's arguments %q, want %q", r.Args, want)
	}
	for _, want := range []string{"XDG_CONFIG_HOME=" + e.settings.RunConfig, "XDG_STATE_HOME=" + filepath.Join(dir, "state"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "PATH=/usr/bin"} {
		if !slices.Contains(r.Env, want) {
			t.Errorf("git's environment has no %s: %q", want, r.Env)
		}
	}
	for _, kv := range r.Env {
		n := kv[:strings.IndexByte(kv, '=')]
		if !slices.Contains([]string{"PATH", "HOME", "TMPDIR", "LANG", "DOCKER_HOST", "DOCKER_CONTEXT", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL"}, n) {
			t.Errorf("git got %s", n)
		}
	}
	e.noLeak(t, nil)
}

func TestLaunchHarnessPath(t *testing.T) {
	e := newEnv(t, script{Record: true})
	e.settings.HarnessRef, e.settings.HarnessPath = "", "/src/qory-github"
	if _, err, _ := e.launch(t, false); err != nil {
		t.Fatal(err)
	}
	stack, _ := os.ReadFile(filepath.Join(explore.Dir(e.settings.State, testRunID), "stack", "qory-stack.yaml"))
	want := "apiVersion: qory.dev/v1alpha1\nname: explore\nmodules:\n    - name: explore-an-item\n      source:\n        path: /src/qory-github/harness/explore-an-item\n"
	if string(stack) != want {
		t.Errorf("the stack\n%s\nwant\n%s", stack, want)
	}
}

func TestLaunchRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*explore.Settings, *explore.Run)
	}{
		{"run id", func(_ *explore.Settings, r *explore.Run) { r.RunID = "../escape" }},
		{"upper-case run id", func(_ *explore.Settings, r *explore.Run) { r.RunID = strings.ToUpper(testRunID) }},
		{"both harnesses", func(s *explore.Settings, _ *explore.Run) { s.HarnessPath = "/src/qory-github" }},
		{"no harness", func(s *explore.Settings, _ *explore.Run) { s.HarnessRef = "" }},
		{"branch ref", func(s *explore.Settings, _ *explore.Run) { s.HarnessRef = "main" }},
		{"ref with a newline", func(s *explore.Settings, _ *explore.Run) { s.HarnessRef = "v1.2.3\npath: x" }},
		{"relative harness path", func(s *explore.Settings, _ *explore.Run) { s.HarnessRef, s.HarnessPath = "", "qory-github" }},
		{"relative state", func(s *explore.Settings, _ *explore.Run) { s.State = "state" }},
		{"relative run config", func(s *explore.Settings, _ *explore.Run) { s.RunConfig = "runconfig" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, script{Record: true})
			h := &holder{b: []byte(testKey)}
			r := explore.Run{RunID: testRunID, Key: h, Inputs: []byte(testInputs)}
			c.edit(&e.settings, &r)
			res, err := explore.Launch(context.Background(), e.settings, r)
			if err == nil {
				t.Fatal("launched")
			}
			if res.Dir != "" || res.Record || !res.Ending.Release {
				t.Errorf("result %+v", res)
			}
			if h.cleared == 0 {
				t.Error("the key's holder was not cleared")
			}
			if _, err := os.Stat(filepath.Join(e.settings.State, "pool")); err == nil {
				t.Error("a run directory was made")
			}
			e.noLeak(t, err)
		})
	}
}

func TestLaunchRecordThereAlready(t *testing.T) {
	// The run directory is new, so the record's folder is there before qory run only
	// when a step before it made it: here the compose.
	e := newEnv(t, script{Record: true, ComposeRecord: true})
	res, err, _ := e.launch(t, false)
	if err == nil || res.Record || !res.Ending.Release {
		t.Fatalf("result %+v, err %v", res, err)
	}
	if _, ran := e.report(t, "run"); ran {
		t.Error("qory run ran")
	}
}

func TestLaunchTwice(t *testing.T) {
	e := newEnv(t, script{Record: true})
	if _, err, _ := e.launch(t, false); err != nil {
		t.Fatal(err)
	}
	if _, err, _ := e.launch(t, false); err == nil {
		t.Fatal("a run directory was used twice")
	}
}

func TestRecordDir(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "elsewhere"))
	work := filepath.Join(root, "work")
	os.Mkdir(work, 0o700)
	link := filepath.Join(root, "link")
	os.Symlink(work, link)
	state := filepath.Join(root, "run", "state")
	want := filepath.Join(state, "qory", "runs", "work-"+sum([]byte(work))[:12], testRunID)
	for _, w := range []string{work, link} {
		if got, err := explore.RecordDir(state, w, testRunID); err != nil || got != want {
			t.Errorf("RecordDir(%s) %s, %v; want %s", w, got, err, want)
		}
	}
	if _, err := explore.RecordDir("state", work, testRunID); err == nil {
		t.Error("a record folder under a relative state directory")
	}
}

func TestRemove(t *testing.T) {
	var dropped []string
	defer explore.SetDropPlace(func(dir string) { dropped = append(dropped, dir) })()
	e := newEnv(t, script{Record: true})
	if _, err, _ := e.launch(t, false); err != nil {
		t.Fatal(err)
	}
	dir := explore.Dir(e.settings.State, testRunID)
	if err := explore.Remove(e.settings.State, testRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the run directory is still there: %v", err)
	}
	if !slices.Equal(dropped, []string{dir}) {
		t.Errorf("places dropped %q, want %q", dropped, dir)
	}
	if err := explore.Remove(e.settings.State, "../pool"); err == nil {
		t.Error("removed a directory by a path that is no run id")
	}
	if _, err := os.Stat(filepath.Join(e.settings.State, "pool")); err != nil {
		t.Errorf("the pool's directory went: %v", err)
	}
}

func TestEndingOf(t *testing.T) {
	released := explore.Ending{Release: true, Retry: explore.RetryNotStarted, ReleaseReason: explore.ReasonNotStarted}
	failed := explore.Ending{Post: true, State: explore.StateFailed}
	for _, c := range []struct {
		record bool
		run    child.Result
		want   explore.Ending
	}{
		{true, child.Result{}, explore.Ending{Post: true, State: explore.StateSucceeded}},
		{true, child.Result{Stopped: true}, explore.Ending{Post: true, State: explore.StateSucceeded}},
		{true, child.Result{ExitCode: 124}, explore.Ending{Post: true, State: explore.StateFailed, Reason: explore.ReasonTimeLimit}},
		{true, child.Result{ExitCode: 124, Stopped: true}, explore.Ending{Post: true, State: explore.StateFailed, Reason: explore.ReasonTimeLimit}},
		{true, child.Result{ExitCode: 1, Stopped: true}, explore.Ending{Post: true, State: explore.StateCancelled, Reason: explore.ReasonPoolStopped}},
		{true, child.Result{ExitCode: 1}, failed},
		{true, child.Result{ExitCode: 2}, failed},
		{true, child.Result{ExitCode: 2, Stopped: true}, failed},
		{true, child.Result{ExitCode: -1, Signal: syscall.SIGKILL}, failed},
		{true, child.Result{Signal: syscall.SIGTERM}, failed},
		{true, child.Result{ExitCode: -1, Signal: syscall.SIGKILL, Stopped: true}, failed},
		{false, child.Result{}, released},
		{false, child.Result{ExitCode: 2}, released},
		{false, child.Result{ExitCode: 1, Stopped: true}, released},
	} {
		if got := explore.EndingOf(c.record, c.run); got != c.want {
			t.Errorf("EndingOf(%v, %+v) %+v, want %+v", c.record, c.run, got, c.want)
		}
	}
	if explore.RetryNotStarted || released.Post || failed.Release {
		t.Error("a run that did not start is released with retry, or a run that started is released")
	}
}
