package child_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/qoryai/qory/internal/pool/child"
)

// fakeArg is the first argument that makes the test binary the fake command.
const fakeArg = "fake-child"

// script is what the fake command does, read from the file its second argument names.
type script struct {
	// Key is the key it expects on descriptor 3; empty expects descriptor 3 closed.
	Key string
	// Code is the run code it expects in the file of descriptor 3, which no name links
	// to.
	Code string
	// Exit is the status it exits with.
	Exit int
	// Mode is "", exit at once; "block", wait for SIGTERM and exit 1; "ignore", ignore
	// SIGTERM and wait to be killed; "kill", end on SIGKILL.
	Mode string
	// Report is the file it writes its report to.
	Report string
	// Say is a line it writes to stdout, and to stderr.
	Say string
}

// report is what the fake command saw.
type report struct {
	Env       []string
	Args      []string
	KeyOK     bool
	KeyLen    int
	FD3Pipe   bool
	StdinNull bool
	// CodeOK reports whether descriptor 3 is a regular file that no name links to, and
	// contains the script's Code.
	CodeOK   bool
	Problems []string
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
	if len(os.Args) > 2 && os.Args[1] == fakeArg {
		os.Exit(fake(os.Args[2], fd3Pipe()))
	}
	os.Exit(m.Run())
}

// fake is the fake command: it reads its script, checks what it was given, writes its
// report, and ends as the script says.
func fake(file string, fd3 bool) int {
	b, err := os.ReadFile(file)
	if err != nil {
		return 90
	}
	var s script
	if err := json.Unmarshal(b, &s); err != nil {
		return 91
	}
	rep := report{Env: os.Environ(), Args: os.Args}
	if st, err := os.Stdin.Stat(); err == nil {
		if null, err := os.Stat(os.DevNull); err == nil {
			rep.StdinNull = os.SameFile(st, null)
		}
	}
	rep.FD3Pipe = fd3
	if s.Code != "" {
		var st syscall.Stat_t
		if syscall.Fstat(child.KeyFD, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Nlink == 0 {
			b, _ := io.ReadAll(os.NewFile(uintptr(child.KeyFD), "code"))
			rep.CodeOK = string(b) == s.Code
		}
	}
	if s.Key != "" && rep.FD3Pipe {
		got := make(chan []byte, 1)
		go func() {
			b, _ := io.ReadAll(os.NewFile(uintptr(child.KeyFD), "key"))
			got <- b
		}()
		select {
		case k := <-got:
			rep.KeyOK, rep.KeyLen = string(k) == s.Key, len(k)
		case <-time.After(5 * time.Second):
			rep.Problems = append(rep.Problems, "descriptor 3 never reached its end")
		}
	}
	for _, a := range os.Args {
		if child.ContainsSecret(a) {
			rep.Problems = append(rep.Problems, "an argument contains a secret")
		}
	}
	out, _ := json.Marshal(rep)
	if err := os.WriteFile(s.Report, out, 0o600); err != nil {
		return 92
	}
	if s.Say != "" {
		fmt.Println(s.Say)
		fmt.Fprintln(os.Stderr, s.Say)
	}
	switch s.Mode {
	case "block":
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		os.WriteFile(s.Report+".ready", nil, 0o600)
		<-c
		return 1
	case "ignore":
		signal.Ignore(syscall.SIGTERM)
		os.WriteFile(s.Report+".ready", nil, 0o600)
		time.Sleep(time.Minute)
		return 93
	case "kill":
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
		time.Sleep(time.Minute)
	}
	return s.Exit
}

// holder is a key holder, as the provisioner client's.
type holder struct {
	mu      sync.Mutex
	b       []byte
	cleared int
}

func (h *holder) Bytes() []byte { return h.b }

func (h *holder) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	clear(h.b)
	h.cleared++
}

// wasCleared reports whether Clear ran, and the bytes are zero.
func (h *holder) wasCleared() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cleared > 0 && bytes.Equal(h.b, make([]byte, len(h.b)))
}

// testKey is a run key's shape, with no meaning, testCode a run code's, and
// testEnrolment an enrolment code's, with its prefix in mixed case.
const (
	testKey       = "qrk_eyJ0ZXN0IjoidGhlIHBvb2wncyBjaGlsZCJ9.c2lnbmF0dXJlLW9mLW5vdGhpbmc"
	testCode      = "qecr_0123456789abcdefghjkmnpqrs.0123456789abcdefghjkmnpqrstvwxyz0"
	testEnrolment = "QeCp_0123456789ABCDEFGHJKMNPQRS.0123456789abcdefghjkmnpqrstvwxyz0"
)

// lines collects the lines a LineWriter passes on.
type lines struct {
	mu  sync.Mutex
	all []string
}

func (l *lines) add(s string) { l.mu.Lock(); l.all = append(l.all, s); l.mu.Unlock() }
func (l *lines) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.all)
}

// start runs the fake command with s, and returns its result, its report and its
// output lines.
func start(t *testing.T, ctx context.Context, dir string, s script, key child.Secret, grace time.Duration, edit ...func(*child.Command)) (child.Result, error, report, []string) {
	t.Helper()
	s.Report = filepath.Join(dir, "report.json")
	b, _ := json.Marshal(s)
	file := filepath.Join(dir, "script.json")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var got lines
	args := []string{fakeArg, file}
	if key != nil {
		args = append(args, "--run-key-fd", "3")
	}
	c := child.Command{
		Path: os.Args[0], Args: args, Dir: dir, ConfigHome: filepath.Join(dir, "config"),
		Key: key, Output: child.NewLineWriter("p: ", got.add), Grace: grace,
	}
	for _, f := range edit {
		f(&c)
	}
	res, err := child.Run(ctx, c)
	var rep report
	if b, readErr := os.ReadFile(s.Report); readErr == nil {
		if err := json.Unmarshal(b, &rep); err != nil {
			t.Fatal(err)
		}
	}
	return res, err, rep, got.get()
}

// noKey fails when the key's bytes, or what starts a secret, is in any of texts.
func noKey(t *testing.T, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if strings.Contains(s, testKey) || strings.Contains(s, testKey[4:20]) || strings.Contains(s, testCode[5:20]) || child.ContainsSecret(s) {
			t.Errorf("a secret is in %q", s)
		}
	}
}

func TestKeyThroughDescriptor(t *testing.T) {
	// The environment the program has: what a command must never get, beside what it
	// gets.
	t.Setenv("QORY_RUN_KEY", testKey)
	t.Setenv("QORY_ACCESS_KEY_SECRET", "qak_notakey")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("OTHER", "x")
	t.Setenv("DOCKER_HOST", "unix:///tmp/docker.sock")
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("XDG_STATE_HOME", "/elsewhere")
	// The program's stdin is a pipe here, so a command given it is seen.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	was := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = was }()
	h := &holder{b: []byte(testKey)}
	res, err, rep, out := start(t, context.Background(), t.TempDir(), script{Key: testKey, Exit: 7, Say: "hello"}, h, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res != (child.Result{ExitCode: 7}) {
		t.Errorf("result %+v, want exit 7", res)
	}
	if !rep.FD3Pipe || !rep.KeyOK || len(rep.Problems) > 0 {
		t.Errorf("report %+v", rep)
	}
	if !rep.StdinNull {
		t.Error("stdin is not /dev/null")
	}
	if !h.wasCleared() {
		t.Error("the holder was not cleared")
	}
	var names []string
	for _, kv := range rep.Env {
		names = append(names, kv[:strings.IndexByte(kv, '=')])
	}
	for _, n := range names {
		if !slices.Contains([]string{"PATH", "HOME", "TMPDIR", "LANG", "DOCKER_HOST", "DOCKER_CONTEXT", "XDG_CONFIG_HOME", "XDG_STATE_HOME"}, n) {
			t.Errorf("the command got %s", n)
		}
	}
	for _, want := range []string{"LANG=C.UTF-8", "DOCKER_HOST=unix:///tmp/docker.sock"} {
		if !slices.Contains(rep.Env, want) {
			t.Errorf("the command did not get %s: %q", want, rep.Env)
		}
	}
	if slices.ContainsFunc(rep.Env, func(kv string) bool { return strings.HasPrefix(kv, "XDG_STATE_HOME=") }) {
		t.Error("XDG_STATE_HOME was passed with no state directory named")
	}
	if slices.ContainsFunc(rep.Env, func(kv string) bool { return strings.HasPrefix(kv, "DOCKER_CONTEXT=") }) {
		t.Error("an empty DOCKER_CONTEXT was passed")
	}
	if !slices.ContainsFunc(rep.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "XDG_CONFIG_HOME=") && strings.HasSuffix(kv, "/config")
	}) {
		t.Errorf("XDG_CONFIG_HOME is not the runs' directory: %q", rep.Env)
	}
	if want := []string{"p: hello", "p: hello"}; !slices.Equal(out, want) {
		t.Errorf("lines %q, want %q", out, want)
	}
	noKey(t, append(append(out, rep.Args...), rep.Env...)...)
}

func TestKeyClearedWhileRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	h := &holder{b: []byte(testKey)}
	cleared := make(chan bool, 1)
	go stopWhenReady(dir, func() {
		cleared <- h.wasCleared()
		cancel()
	})
	_, err, rep, _ := start(t, ctx, dir, script{Key: testKey, Mode: "block"}, h, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.KeyOK {
		t.Errorf("report %+v", rep)
	}
	if !<-cleared {
		t.Error("the holder kept the key while the command ran")
	}
}

func TestNoKeyNoDescriptor(t *testing.T) {
	res, err, rep, _ := start(t, context.Background(), t.TempDir(), script{}, nil, 0)
	if err != nil || res != (child.Result{}) {
		t.Fatal(res, err)
	}
	if rep.FD3Pipe {
		t.Error("descriptor 3 is a pipe without a key")
	}
}

func TestArgumentWithSecretRefused(t *testing.T) {
	h := &holder{b: []byte(testKey)}
	_, err := child.Run(context.Background(), child.Command{Path: os.Args[0], Args: []string{fakeArg, testKey}, ConfigHome: t.TempDir(), Key: h})
	if err == nil {
		t.Fatal("an argument with a run key was passed")
	}
	if !h.wasCleared() {
		t.Error("the holder was not cleared")
	}
	noKey(t, err.Error())
}

// TestRunCodeThroughAnUnlinkedFile passes the run code as descriptor 3: a file opened
// for reading and unlinked, which the command reads to its end. Started is called once
// the command started, and the file is closed once Run returns.
func TestRunCodeThroughAnUnlinkedFile(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "code")
	if err := os.WriteFile(name, []byte(testCode), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	started := 0
	res, err, rep, _ := start(t, context.Background(), dir, script{Code: testCode}, nil, 0, func(c *child.Command) {
		c.File, c.Started = f, func() { started++ }
		c.Args = append(c.Args, "--run-code-file", "/dev/fd/3")
	})
	if err != nil || res != (child.Result{}) {
		t.Fatal(res, err)
	}
	if !rep.CodeOK || rep.FD3Pipe || len(rep.Problems) > 0 {
		t.Errorf("report %+v", rep)
	}
	if started != 1 {
		t.Errorf("Started was called %d times, want 1", started)
	}
	if err := f.Close(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("the file was not closed: %v", err)
	}
}

// TestArgumentWithARunCodeRefused starts no command whose argument contains a run code,
// in any case, and closes the file it was given.
func TestArgumentWithARunCodeRefused(t *testing.T) {
	for _, code := range []string{testCode, strings.ToUpper(testCode)} {
		f, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		started := false
		_, err = child.Run(context.Background(), child.Command{Path: os.Args[0], Args: []string{fakeArg, code}, ConfigHome: t.TempDir(), File: f, Started: func() { started = true }})
		if err == nil || started {
			t.Fatal("an argument with a run code was passed")
		}
		if err := f.Close(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("the file was not closed: %v", err)
		}
		noKey(t, err.Error())
	}
}

func TestStartFailureClearsKey(t *testing.T) {
	h := &holder{b: []byte(testKey)}
	_, err := child.Run(context.Background(), child.Command{Path: filepath.Join(t.TempDir(), "none"), ConfigHome: t.TempDir(), Key: h})
	if err == nil {
		t.Fatal("a missing program started")
	}
	if !h.wasCleared() {
		t.Error("the holder was not cleared")
	}
	noKey(t, err.Error())
}

func TestStopSIGTERM(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	go stopWhenReady(dir, cancel)
	res, err, _, _ := start(t, ctx, dir, script{Mode: "block"}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res != (child.Result{ExitCode: 1, Stopped: true}) {
		t.Errorf("result %+v, want exit 1, stopped", res)
	}
}

func TestStopSIGKILLAfterGrace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	go stopWhenReady(dir, cancel)
	began := time.Now()
	res, err, _, _ := start(t, ctx, dir, script{Mode: "ignore"}, nil, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if res != (child.Result{ExitCode: -1, Signal: syscall.SIGKILL, Stopped: true}) {
		t.Errorf("result %+v, want SIGKILL, stopped", res)
	}
	if d := time.Since(began); d > 20*time.Second {
		t.Errorf("the stop took %s", d)
	}
}

func TestSignalNotSent(t *testing.T) {
	res, err, _, _ := start(t, context.Background(), t.TempDir(), script{Mode: "kill"}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res != (child.Result{ExitCode: -1, Signal: syscall.SIGKILL}) {
		t.Errorf("result %+v, want SIGKILL, not stopped", res)
	}
}

func TestEndedContextNotStarted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &holder{b: []byte(testKey)}
	if _, err := child.Run(ctx, child.Command{Path: os.Args[0], ConfigHome: t.TempDir(), Key: h}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if !h.wasCleared() {
		t.Error("the holder was not cleared")
	}
}

// stopWhenReady calls cancel once the fake command started in dir is waiting.
func stopWhenReady(dir string, cancel func()) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "report.json.ready")); err == nil {
			cancel()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
}

func TestLineWriter(t *testing.T) {
	var got lines
	w := child.NewLineWriter("x: ", got.add)
	io.WriteString(w, "one\ntw")
	io.WriteString(w, "o\r\nthe key "+testKey+" here\nQRK_upper\nthe code "+testCode+"\nQECR_upper\n")
	io.WriteString(w, "n QeCn_x\np qECP_x\ng QecG_x\ni qEcI_x\n")
	io.WriteString(w, strings.Repeat("a", 20<<10))
	io.WriteString(w, "\nlast")
	w.Flush()
	all := got.get()
	left := "x: (a line that contains a secret, left out)"
	want := []string{"x: one", "x: two", left, left, left, left, left, left, left, left, "x: " + strings.Repeat("a", 16<<10), "x: " + strings.Repeat("a", 4<<10), "x: last"}
	if !slices.Equal(all, want) {
		t.Errorf("lines %q", all)
	}
	noKey(t, all...)
}

// TestLineWriterKeyAcrossTheSplit puts a run key, a run code, then an enrolment code,
// across the 16 KiB split of a long line, at every offset from before its start to past
// its end, in one write and a byte at a time: every part of the line is passed on as
// dots alone or left out, so no piece of the key or the code is passed on, and the next
// line is passed on as it is.
func TestLineWriterKeyAcrossTheSplit(t *testing.T) {
	const split = 16 << 10
	for _, testKey := range []string{testKey, testCode, testEnrolment} {
		for off := -2; off <= len(testKey)+2; off++ {
			for _, bytewise := range []bool{false, true} {
				var got lines
				w := child.NewLineWriter("x: ", got.add)
				text := strings.Repeat(".", split-off) + testKey + strings.Repeat(".", 20<<10) + "\nafter\n"
				if bytewise {
					for i := range len(text) {
						io.WriteString(w, text[i:i+1])
					}
				} else {
					io.WriteString(w, text)
				}
				w.Flush()
				all := got.get()
				withheld := 0
				for _, l := range all {
					switch body := strings.TrimPrefix(l, "x: "); {
					case body == "(a line that contains a secret, left out)":
						withheld++
					case body == "after", strings.Trim(body, ".") == "":
					default:
						t.Fatalf("offset %d, a byte at a time %v: a part of the key was passed on, at the end of %q", off, bytewise, body[max(0, len(body)-24):])
					}
				}
				if withheld == 0 || all[len(all)-1] != "x: after" {
					t.Errorf("offset %d, a byte at a time %v: lines %d, %d left out, last %q", off, bytewise, len(all), withheld, all[len(all)-1])
				}
			}
		}
	}
}

// TestLineWriterRunCodeOnThePartsLastByte puts a run code whose q is the last byte of
// the first 16 KiB part of a long line: the part is read with the 4 bytes after it,
// qecr_ whole, so it and the rest of the line are left out. With 3 bytes, the part
// would show qecr and the next would start with ecr_, and the code would be passed on.
func TestLineWriterRunCodeOnThePartsLastByte(t *testing.T) {
	const split = 16 << 10
	var got lines
	w := child.NewLineWriter("x: ", got.add)
	io.WriteString(w, strings.Repeat(".", split-1)+testCode+strings.Repeat(".", 20<<10)+"\nafter\n")
	w.Flush()
	all := got.get()
	left := "x: (a line that contains a secret, left out)"
	if len(all) < 2 || all[len(all)-1] != "x: after" || slices.ContainsFunc(all[:len(all)-1], func(l string) bool { return l != left }) {
		t.Errorf("lines %d, starting %.80q", len(all), all)
	}
	noKey(t, all...)
}

func TestEnv(t *testing.T) {
	t.Setenv("PATH", "/bin")
	t.Setenv("HOME", "/home/x")
	t.Setenv("TMPDIR", "")
	t.Setenv("LANG", "")
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "orbstack")
	t.Setenv("QORY_ANYTHING", "1")
	t.Setenv("XDG_STATE_HOME", "/elsewhere")
	got := child.Env("/c", "", "")
	want := []string{"PATH=/bin", "HOME=/home/x", "DOCKER_CONTEXT=orbstack", "XDG_CONFIG_HOME=/c"}
	if !slices.Equal(got, want) {
		t.Errorf("env %q, want %q", got, want)
	}
	got = child.Env("/c", "/s", "")
	if want = append(want, "XDG_STATE_HOME=/s"); !slices.Equal(got, want) {
		t.Errorf("env %q, want %q", got, want)
	}
}

// TestCleanPath drops from PATH the empty and relative entries, and each entry that, its
// links and ".." resolved, lands in the runs folder: the folder itself, a folder in it,
// one that is not there yet, a link into it, a link to a folder that holds it, one not
// there yet behind that link, and ".." back into it. The others stay as written, in their order: one with a trailing slash,
// one that is not there, and one through a link that lands elsewhere. Env gives a
// command the same PATH.
func TestCleanPath(t *testing.T) {
	top := t.TempDir()
	runs := filepath.Join(top, "pool", "runs")
	for _, d := range []string{filepath.Join(runs, "0192f0c1", "bin"), filepath.Join(top, "pool", "other"), filepath.Join(top, "kept"), filepath.Join(top, "elsewhere")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, to := range map[string]string{"into-runs": filepath.Join(runs, "0192f0c1", "bin"), "to-pool": filepath.Join(top, "pool"), "to-elsewhere": filepath.Join(top, "elsewhere")} {
		if err := os.Symlink(to, filepath.Join(top, link)); err != nil {
			t.Fatal(err)
		}
	}
	sep := string(os.PathListSeparator)
	dropped := []string{
		"", "bin", "./bin", "../bin",
		runs,
		filepath.Join(runs, "0192f0c1", "bin"),
		filepath.Join(runs, "not-there-yet", "bin"),
		filepath.Join(top, "into-runs"),
		filepath.Join(top, "to-pool", "runs", "0192f0c1"),
		filepath.Join(top, "to-pool", "runs", "later", "bin"),
		top + "/pool/other/../runs/0192f0c1/bin",
		top + "/kept/../pool/runs",
	}
	kept := []string{
		"/usr/bin",
		filepath.Join(top, "kept") + "/",
		filepath.Join(top, "not-there"),
		filepath.Join(top, "to-elsewhere"),
		filepath.Join(top, "pool", "other"),
		top + "/pool/runs-not",
	}
	var all []string
	for i := range max(len(dropped), len(kept)) {
		if i < len(dropped) {
			all = append(all, dropped[i])
		}
		if i < len(kept) {
			all = append(all, kept[i])
		}
	}
	path := strings.Join(all, sep)
	want := strings.Join(kept, sep)
	if got := child.CleanPath(path, runs); got != want {
		t.Errorf("the PATH\n%q\nwant\n%q", strings.Split(got, sep), kept)
	}
	// The runs folder named through a link to the folder above it is the same folder.
	if got := child.CleanPath(path, filepath.Join(top, "to-pool", "runs")); got != want {
		t.Errorf("with the runs folder through a link, the PATH\n%q\nwant\n%q", strings.Split(got, sep), kept)
	}
	t.Setenv("PATH", path)
	if env := child.Env("/c", "", runs); !slices.Contains(env, "PATH="+want) {
		t.Errorf("Env gives %q", env)
	}
	t.Setenv("PATH", strings.Join(dropped, sep))
	if env := child.Env("/c", "", runs); slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "PATH=") }) {
		t.Errorf("Env gives a PATH with no entry kept: %q", env)
	}
}

// TestLookPath finds a program on the cleaned PATH alone: a file of its name in an
// entry linked into the runs folder is passed over for the next entry's, a file that is
// not executable or not a regular file is passed over, and with none found the error is
// exec's. No file is run.
func TestLookPath(t *testing.T) {
	top := t.TempDir()
	runs := filepath.Join(top, "pool", "runs")
	inRuns, plain, notExec, kept := filepath.Join(runs, "r", "bin"), filepath.Join(top, "dir"), filepath.Join(top, "notexec"), filepath.Join(top, "kept")
	for _, d := range []string{inRuns, filepath.Join(plain, "prog"), notExec, kept} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(inRuns, filepath.Join(top, "link")); err != nil {
		t.Fatal(err)
	}
	for f, mode := range map[string]os.FileMode{filepath.Join(inRuns, "prog"): 0o700, filepath.Join(notExec, "prog"): 0o600, filepath.Join(kept, "prog"): 0o700} {
		if err := os.WriteFile(f, nil, mode); err != nil {
			t.Fatal(err)
		}
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join([]string{filepath.Join(top, "link"), inRuns, plain, notExec, kept}, sep))
	if got, err := child.LookPath("prog", runs); err != nil || got != filepath.Join(kept, "prog") {
		t.Errorf("found %q, %v; want %q", got, err, filepath.Join(kept, "prog"))
	}
	t.Setenv("PATH", strings.Join([]string{filepath.Join(top, "link"), inRuns}, sep))
	if got, err := child.LookPath("prog", runs); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("found %q, %v; want exec.ErrNotFound", got, err)
	}
}

// TestProgram takes a program named with a path separator as it is, and finds one named
// alone on the cleaned PATH: an empty file of that name in an entry linked into the
// runs folder is passed over for the next entry's, and with none found the error is
// exec's. Run starts a program named alone the same way, so with the name in the runs
// folder alone it starts nothing. No file is run.
func TestProgram(t *testing.T) {
	top := t.TempDir()
	runs := filepath.Join(top, "pool", "runs")
	inRuns, kept := filepath.Join(runs, "hostbin"), filepath.Join(top, "kept")
	for _, d := range []string{inRuns, kept} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "prog"), nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(inRuns, filepath.Join(top, "link")); err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", strings.Join([]string{filepath.Join(top, "link"), kept}, sep))
	for _, named := range []string{"/opt/x/prog", "./prog", "sub/prog"} {
		if got, err := child.Program(named, runs); err != nil || got != named {
			t.Errorf("%s is %q, %v", named, got, err)
		}
	}
	if got, err := child.Program("prog", runs); err != nil || got != filepath.Join(kept, "prog") {
		t.Errorf("prog is %q, %v; want %q", got, err, filepath.Join(kept, "prog"))
	}
	t.Setenv("PATH", strings.Join([]string{filepath.Join(top, "link"), inRuns}, sep))
	if got, err := child.Program("prog", runs); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("prog is %q, %v; want exec.ErrNotFound", got, err)
	}
	var got lines
	res, err := child.Run(context.Background(), child.Command{
		Path: "prog", Dir: top, ConfigHome: filepath.Join(top, "config"), Runs: runs, Output: child.NewLineWriter("p: ", got.add),
	})
	if !errors.Is(err, exec.ErrNotFound) || res != (child.Result{}) || len(got.get()) != 0 {
		t.Errorf("Run of prog: %+v, %v, lines %q; want exec.ErrNotFound", res, err, got.get())
	}
}

func TestStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/elsewhere")
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	_, err, rep, _ := start(t, context.Background(), dir, script{}, nil, 0, func(c *child.Command) { c.StateHome = state })
	if err != nil {
		t.Fatal(err)
	}
	var set []string
	for _, kv := range rep.Env {
		if strings.HasPrefix(kv, "XDG_STATE_HOME=") {
			set = append(set, kv)
		}
	}
	if want := []string{"XDG_STATE_HOME=" + state}; !slices.Equal(set, want) {
		t.Errorf("XDG_STATE_HOME %q, want %q", set, want)
	}
	if _, err := child.Run(context.Background(), child.Command{Path: os.Args[0], Args: []string{fakeArg, filepath.Join(dir, "none.json")}, ConfigHome: dir, StateHome: "state"}); err == nil {
		t.Error("a relative state directory was taken")
	}
}
