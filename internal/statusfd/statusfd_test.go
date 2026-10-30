//go:build unix

package statusfd_test

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/statusfd"
)

// envHelper, set in a child of a test, makes the test binary the helper process: it
// writes to the descriptor its value is set to and prints the result, and whether
// QORY_STATUS_FD is set.
const envHelper = "STATUSFD_TEST_HELPER"

func TestMain(m *testing.M) {
	if v := os.Getenv(envHelper); v != "" {
		n, _ := strconv.Atoi(v)
		_, err := unix.Write(n, []byte("from the child\n"))
		_, set := os.LookupEnv(statusfd.Env)
		fmt.Printf("write=%v env=%t\n", err, set)
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "statusfd-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", dir)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		os.Setenv(name, filepath.Join(dir, strings.ToLower(name)))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// statusFD is the number of the descriptor the tests give qory: high, so the helper
// process has no other file open at that number.
const statusFD = 237

// descriptor opens a pipe whose write end is statusFD, sets QORY_STATUS_FD to it and
// returns the read end. The test closes the descriptor when it ends.
func descriptor(t *testing.T) *os.File {
	t.Helper()
	if _, err := unix.FcntlInt(statusFD, unix.F_GETFD, 0); err == nil {
		t.Fatalf("descriptor %d is open already", statusFD)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Dup2(int(w.Fd()), statusFD); err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() {
		statusfd.Close()
		r.Close()
	})
	t.Setenv(statusfd.Env, strconv.Itoa(statusFD))
	return r
}

// readAll closes the descriptor and returns what reached the read end r.
func readAll(t *testing.T, r *os.File) string {
	t.Helper()
	statusfd.Close()
	r.SetReadDeadline(time.Now().Add(10 * time.Second))
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestLinesReachTheDescriptorMasked is qory's lines after Setup: each reaches the
// descriptor as one line, with a value of the mask list as ***, through Line and through
// a Tee, which also writes them to stderr as they were given. QORY_STATUS_FD is gone
// from the environment, and Setup printed nothing.
func TestLinesReachTheDescriptorMasked(t *testing.T) {
	r := descriptor(t)
	var errOut strings.Builder
	statusfd.Setup(&errOut)
	if _, set := os.LookupEnv(statusfd.Env); set {
		t.Errorf("%s is still in the environment", statusfd.Env)
	}
	if !statusfd.Active() {
		t.Fatal("Setup did not open the descriptor")
	}
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	secretmask.Default().Add("status-secret-1")
	statusfd.Line("✗ the first line status-secret-1\n  its continuation")
	var stderr strings.Builder
	tee := statusfd.Tee(&stderr)
	io.WriteString(tee, "qory run: fetched ")
	io.WriteString(tee, "core at 0123 ?token=abc\nqory run: no line end")
	want := "✗ the first line ***\n  its continuation\nqory run: fetched core at 0123 ?token=***\n"
	if got := readAll(t, r); got != want {
		t.Errorf("the descriptor got %q, want %q", got, want)
	}
	if got := stderr.String(); got != "qory run: fetched core at 0123 ?token=abc\nqory run: no line end" {
		t.Errorf("stderr got %q", got)
	}
	if errOut.Len() != 0 {
		t.Errorf("Setup printed %q", errOut.String())
	}
}

// TestClaimKeepsTheDescriptorUntilSetup is the main package's Claim before qory run's
// Setup: Claim removes the variable and keeps the descriptor without writing to it, and
// Setup starts the writes.
func TestClaimKeepsTheDescriptorUntilSetup(t *testing.T) {
	r := descriptor(t)
	statusfd.Claim()
	if _, set := os.LookupEnv(statusfd.Env); set || statusfd.Active() {
		t.Fatalf("after Claim, the variable is set: %t; the descriptor is in use: %t", set, statusfd.Active())
	}
	statusfd.Line("before Setup")
	var errOut strings.Builder
	statusfd.Setup(&errOut)
	statusfd.Line("after Setup")
	if got := readAll(t, r); got != "after Setup\n" {
		t.Errorf("the descriptor got %q", got)
	}
	if errOut.Len() != 0 {
		t.Errorf("Setup printed %q", errOut.String())
	}
}

// TestATeeWritesALongLineInParts is a line longer than MaxLine through a Tee: once
// MaxLine bytes of it wait, they go to the descriptor, so the Tee keeps no more.
func TestATeeWritesALongLineInParts(t *testing.T) {
	r := descriptor(t)
	statusfd.Setup(io.Discard)
	got := make(chan string)
	go func() {
		r.SetReadDeadline(time.Now().Add(20 * time.Second))
		data, _ := io.ReadAll(r)
		got <- string(data)
	}()
	tee := statusfd.Tee(io.Discard)
	long := strings.Repeat("a", statusfd.MaxLine+1)
	io.WriteString(tee, long)
	io.WriteString(tee, "b\n")
	statusfd.Close()
	if data := <-got; data != long+"\nb\n" {
		t.Errorf("the descriptor got %d bytes, ending %q", len(data), data[max(0, len(data)-10):])
	}
}

// TestAChildDoesNotReceiveTheDescriptor starts a program after Setup: its write to the
// descriptor fails with EBADF, since the descriptor is close-on-exec, and it does not
// see QORY_STATUS_FD. Nothing it wrote reaches the descriptor.
func TestAChildDoesNotReceiveTheDescriptor(t *testing.T) {
	r := descriptor(t)
	statusfd.Setup(io.Discard)
	c := exec.Command(os.Args[0], "-test.run=^$")
	c.Env = append(os.Environ(), envHelper+"="+strconv.Itoa(statusFD))
	out, err := c.Output()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := fmt.Sprintf("write=%v env=false\n", unix.EBADF); string(out) != want {
		t.Errorf("the child printed %q, want %q", out, want)
	}
	statusfd.Line("after the child")
	if got := readAll(t, r); got != "after the child\n" {
		t.Errorf("the descriptor got %q", got)
	}
}

// TestADescriptorThatCannotBeUsed is QORY_STATUS_FD set to a number of no open
// descriptor, to 0, 1 and 2, and to a word: Setup prints one line, starting with the
// fix, removes the variable, and Line writes nowhere.
func TestADescriptorThatCannotBeUsed(t *testing.T) {
	for _, c := range []struct{ value, want string }{
		{strconv.Itoa(statusFD), fmt.Sprintf("qory run: open descriptor %d for qory run, or unset QORY_STATUS_FD: %v; qory run writes its lines to stderr only\n", statusFD, unix.EBADF)},
		{"0", "qory run: set QORY_STATUS_FD to a file descriptor above 2, or unset it: 0 is stdin, stdout or stderr; qory run writes its lines to stderr only\n"},
		{"1", "qory run: set QORY_STATUS_FD to a file descriptor above 2, or unset it: 1 is stdin, stdout or stderr; qory run writes its lines to stderr only\n"},
		{"2", "qory run: set QORY_STATUS_FD to a file descriptor above 2, or unset it: 2 is stdin, stdout or stderr; qory run writes its lines to stderr only\n"},
		{"three", "qory run: set QORY_STATUS_FD to the number of an open file descriptor above 2, or unset it: \"three\" is not one; qory run writes its lines to stderr only\n"},
	} {
		t.Setenv(statusfd.Env, c.value)
		var errOut strings.Builder
		statusfd.Setup(&errOut)
		if errOut.String() != c.want {
			t.Errorf("%s: Setup printed %q, want %q", c.value, errOut.String(), c.want)
		}
		if _, set := os.LookupEnv(statusfd.Env); set || statusfd.Active() {
			t.Errorf("%s: the variable is set: %t; the descriptor is in use: %t", c.value, set, statusfd.Active())
		}
		statusfd.Line("nowhere")
	}
}

// TestAFailedWriteStopsTheWrites is a descriptor whose reader closed: the first write
// fails, so Line prints one line on stderr and writes nothing more there, and the
// caller goes on.
func TestAFailedWriteStopsTheWrites(t *testing.T) {
	r := descriptor(t)
	var errOut strings.Builder
	statusfd.Setup(&errOut)
	r.Close()
	statusfd.Line("first")
	statusfd.Line("second")
	if statusfd.Active() {
		t.Error("the descriptor is still in use")
	}
	if got := errOut.String(); strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, fmt.Sprintf("qory run: check what reads QORY_STATUS_FD: a write to descriptor %d failed, so qory run writes its lines to stderr only from now on: ", statusFD)) || !strings.Contains(got, unix.EPIPE.Error()) {
		t.Errorf("stderr got %q", got)
	}
}

// TestATeeMasksAValueSplitAtEachByte writes a line with a value of the mask list
// through a Tee in two writes, split at each byte of the line in turn: the descriptor
// gets the line with the value as *** each time, and stderr gets it as it was.
func TestATeeMasksAValueSplitAtEachByte(t *testing.T) {
	r := descriptor(t)
	statusfd.Setup(io.Discard)
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	const value = "tee-secret-value-1"
	secretmask.Default().Add(value)
	text := "qory run: before " + value + " after\n"
	var stderr strings.Builder
	for i := 0; i <= len(text); i++ {
		tee := statusfd.Tee(&stderr)
		io.WriteString(tee, text[:i])
		io.WriteString(tee, text[i:])
		tee.Flush()
	}
	want := strings.Repeat("qory run: before *** after\n", len(text)+1)
	if got := readAll(t, r); got != want {
		t.Errorf("the descriptor got %q, want %q", got, want)
	}
	if got := stderr.String(); got != strings.Repeat(text, len(text)+1) {
		t.Errorf("stderr got %q", got)
	}
}

// TestATeeMasksAValueAcrossTheCutOfALongLine is a line longer than MaxLine whose value
// of the mask list starts before MaxLine bytes and ends after: the descriptor gets the
// value as ***, since the Tee masks the bytes before it cuts the line.
func TestATeeMasksAValueAcrossTheCutOfALongLine(t *testing.T) {
	r := descriptor(t)
	statusfd.Setup(io.Discard)
	old := secretmask.SetDefault(secretmask.New())
	t.Cleanup(func() { secretmask.SetDefault(old) })
	const value = "tee-secret-value-2"
	secretmask.Default().Add(value)
	got := make(chan string)
	go func() {
		r.SetReadDeadline(time.Now().Add(20 * time.Second))
		data, _ := io.ReadAll(r)
		got <- string(data)
	}()
	tee := statusfd.Tee(io.Discard)
	long := strings.Repeat("a", statusfd.MaxLine-2)
	io.WriteString(tee, long+value[:5])
	io.WriteString(tee, value[5:]+"\n")
	tee.Flush()
	statusfd.Close()
	data := <-got
	if strings.Contains(data, value[:5]) || data != long+"***\n" {
		t.Errorf("the descriptor got %d bytes, ending %q", len(data), data[max(0, len(data)-30):])
	}
}

// TestATeeWritesItsLastLineOnFlush is a Tee whose last line has no newline: Flush
// writes it to the descriptor.
func TestATeeWritesItsLastLineOnFlush(t *testing.T) {
	r := descriptor(t)
	statusfd.Setup(io.Discard)
	tee := statusfd.Tee(io.Discard)
	io.WriteString(tee, "qory run: one\nqory run: no line end")
	tee.Flush()
	if got := readAll(t, r); got != "qory run: one\nqory run: no line end\n" {
		t.Errorf("the descriptor got %q", got)
	}
}

// TestShownIsTrueForAWriterThatGoesToStderr is Shown after Setup got stderr: stderr and
// a mask writer over it go there, and a Tee over it, another writer and nil do not.
// Before Setup gets stderr, stderr does not go there.
func TestShownIsTrueForAWriterThatGoesToStderr(t *testing.T) {
	var stderr strings.Builder
	if statusfd.Shown(&stderr) {
		t.Error("Shown is true before Setup")
	}
	descriptor(t)
	statusfd.Setup(&stderr)
	defer statusfd.Close()
	masked := secretmask.New().Writer(&stderr)
	for _, c := range []struct {
		name string
		w    io.Writer
		want bool
	}{
		{"stderr", &stderr, true},
		{"a mask writer over stderr", masked, true},
		{"a Tee over stderr", statusfd.Tee(&stderr), false},
		{"a Tee over a mask writer over stderr", statusfd.Tee(masked), false},
		{"another writer", &strings.Builder{}, false},
		{"io.Discard", io.Discard, false},
		{"nil", nil, false},
	} {
		if got := statusfd.Shown(c.w); got != c.want {
			t.Errorf("%s: Shown is %v, want %v", c.name, got, c.want)
		}
	}
}

// TestTheDescriptorGetsNoColour is a coloured line, as a ui on a terminal prints it,
// through Line and through a Tee: the descriptor gets the line without its escape
// sequences, and the Tee's writer gets it as it was.
func TestTheDescriptorGetsNoColour(t *testing.T) {
	r := descriptor(t)
	statusfd.Setup(io.Discard)
	const coloured = "\x1b[1;32m✓\x1b[0m composed"
	statusfd.Line(coloured)
	var stderr strings.Builder
	tee := statusfd.Tee(&stderr)
	io.WriteString(tee, "\x1b[1;33m⚠\x1b[0m a warning\n")
	tee.Flush()
	if got := readAll(t, r); got != "✓ composed\n⚠ a warning\n" {
		t.Errorf("the descriptor got %q", got)
	}
	if got := stderr.String(); got != "\x1b[1;33m⚠\x1b[0m a warning\n" {
		t.Errorf("stderr got %q", got)
	}
}
