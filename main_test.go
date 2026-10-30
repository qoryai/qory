//go:build linux || darwin

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/statusfd"
)

// envChild, set in a child of a test, makes the test binary run main as qory, in the
// mode its value names, in place of the tests: "fail" with a hardening that fails,
// "harden" with gateway.HardenProcess, "mask" with a hardening that fails with an
// error that contains a value of the mask list and a signed URL, and "statusorder" as
// qory run with an update check that opens the file envStatusFile names first thing.
const envChild = "QORY_TEST_MAIN_CHILD"

// resultPrefix starts the one line on which a child reports.
const resultPrefix = "main-test-result: "

// childResult is what a child reports. In "fail", the state the hardening found when
// main called it: whether main had set the hello example, whether QORY_RUN_KEY was still
// in the environment, and whether the server's variables were taken. In "harden", what
// main left: the core size limit, and PR_GET_DUMPABLE on Linux, -1 elsewhere.
type childResult struct {
	ExampleSet  bool
	RunKeyInEnv bool
	Taken       bool
	CoreCur     uint64
	CoreMax     uint64
	Dumpable    int
}

func TestMain(m *testing.M) {
	switch os.Getenv(envChild) {
	case "":
		os.Exit(m.Run())
	case "fail":
		hardenProcess = func() error {
			report(childResult{
				ExampleSet:  cmd.Example != nil,
				RunKeyInEnv: os.Getenv(config.EnvRunKey) != "",
				Taken:       config.TakenServerVariables() != (config.ServerVariables{}),
			})
			// Two failures joined, as gateway.HardenProcess returns them: one line each,
			// each starting with its fix.
			return errors.Join(errors.New("allow the test's first call: it failed on purpose"), errors.New("allow the test's second call: it failed too"))
		}
		os.Args = []string{"qory", "version", "--json"}
		main()
		os.Stdout.WriteString("main returned\n")
		os.Exit(0)
	case "mask":
		secretmask.Default().Add(maskedValue)
		hardenProcess = func() error {
			return errors.New("allow the test's call: it failed with " + maskedValue + " and https://blob.example/b?X-Amz-Signature=abc123\n  then " + maskedValue)
		}
		os.Args = []string{"qory", "version", "--json"}
		main()
		os.Exit(0)
	case "statusorder":
		startUpdateCheck = func(io.Writer, []string) func() {
			fd, err := unix.Open(os.Getenv(envStatusFile), unix.O_RDWR, 0)
			fmt.Printf("%sfd=%d err=%v\n", statusPrefix, fd, err)
			return func() {}
		}
		os.Args = []string{"qory", "run", "--gateway", "https://gateway.example"}
		main()
		os.Exit(0)
	case "statusflag":
		os.Args = []string{"qory", "run", "--no-such-flag"}
		main()
		os.Exit(0)
	case "statusharden":
		hardenProcess = func() error { return errors.New("allow the test's call: it failed with " + maskedValue) }
		secretmask.Default().Add(maskedValue)
		os.Args = []string{"qory", "run"}
		main()
		os.Exit(0)
	case "harden":
		os.Args = []string{"qory", "version", "--json"}
		main()
		var l unix.Rlimit
		if err := unix.Getrlimit(unix.RLIMIT_CORE, &l); err != nil {
			os.Stdout.WriteString("RLIMIT_CORE: " + err.Error() + "\n")
			os.Exit(3)
		}
		report(childResult{CoreCur: l.Cur, CoreMax: l.Max, Dumpable: dumpable()})
		os.Exit(0)
	}
}

// envStatusFile is the file that the update check of the child in "statusorder" opens,
// and statusPrefix starts the line on which it reports the descriptor it got.
const (
	envStatusFile = "QORY_TEST_STATUS_FILE"
	statusPrefix  = "status-file: "
)

// maskedValue is the value that the child in "mask" adds to the mask list.
const maskedValue = "masked-value-0123"

// report writes r to stdout on one line after resultPrefix.
func report(r childResult) {
	data, _ := json.Marshal(r)
	os.Stdout.WriteString(resultPrefix + string(data) + "\n")
}

// runChild runs this test binary as qory in mode, with a home, configuration and state
// under a temporary directory, QORY_RUN_KEY set and env added, and returns its stdout,
// its stderr, the result it reported and its exit status.
func runChild(t *testing.T, mode string, env ...string) (stdout, stderr string, r childResult, status int) {
	t.Helper()
	return runChildWith(t, mode, nil, env...)
}

// runChildWith is [runChild] with files as the child's descriptors 3 and on.
func runChildWith(t *testing.T, mode string, files []*os.File, env ...string) (stdout, stderr string, r childResult, status int) {
	t.Helper()
	dir := t.TempDir()
	c := exec.Command(os.Args[0], "-test.run=^$")
	c.Env = append(os.Environ(),
		envChild+"="+mode,
		"HOME="+dir,
		"XDG_CONFIG_HOME="+filepath.Join(dir, "config"),
		"XDG_STATE_HOME="+filepath.Join(dir, "state"),
		"XDG_CACHE_HOME="+filepath.Join(dir, "cache"),
		"XDG_DATA_HOME="+filepath.Join(dir, "data"),
		"QORY_NO_UPDATE_CHECK=1",
		config.EnvRunKey+"=not-a-run-key",
	)
	c.Env = append(c.Env, env...)
	c.ExtraFiles = files
	var out, errOut strings.Builder
	c.Stdout, c.Stderr = &out, &errOut
	err := c.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		status = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if rest, ok := strings.CutPrefix(line, resultPrefix); ok {
			if err := json.Unmarshal([]byte(rest), &r); err != nil {
				t.Fatalf("%s: %v", line, err)
			}
		}
	}
	return out.String(), errOut.String(), r, status
}

// TestMainHardensTheProcessFirst is main calling its hardening before anything else: the
// hello example is not set, QORY_RUN_KEY is still in the environment, and the server's
// variables are not taken. When the hardening fails twice, qory prints both failures,
// the first on the line that starts with ✗, so that line starts with a fix, exits with
// 1, and runs no command.
func TestMainHardensTheProcessFirst(t *testing.T) {
	stdout, stderr, r, status := runChild(t, "fail")
	if r.ExampleSet || !r.RunKeyInEnv || r.Taken {
		t.Errorf("main ran something before the hardening: %+v\n%s", r, stdout)
	}
	if status != 1 {
		t.Errorf("exit status %d, want 1\n%s%s", status, stdout, stderr)
	}
	if want := "✗ allow the test's first call: it failed on purpose\n  allow the test's second call: it failed too\n"; !strings.HasSuffix(stderr, want) {
		t.Errorf("stderr %q, want it to end with %q", stderr, want)
	}
	if strings.Contains(stdout, "main returned") || strings.Contains(stdout, "\"version\"") {
		t.Errorf("qory ran the command after the hardening failed:\n%s", stdout)
	}
}

// TestMainMasksTheLastErrorLine is the error main prints, of several lines: each copy
// of a value of the mask list, and the signature of a URL, is printed as ***.
func TestMainMasksTheLastErrorLine(t *testing.T) {
	stdout, stderr, _, status := runChild(t, "mask")
	if status != 1 {
		t.Errorf("exit status %d, want 1\n%s%s", status, stdout, stderr)
	}
	if want := "✗ allow the test's call: it failed with *** and https://blob.example/b?X-Amz-Signature=***\n    then ***\n"; !strings.HasSuffix(stderr, want) {
		t.Errorf("stderr %q, want it to end with %q", stderr, want)
	}
}

// TestMainClaimsTheStatusDescriptorFirst is qory run with QORY_STATUS_FD=3 and no
// descriptor 3 passed, whose update check opens a file as it starts and gets descriptor
// 3: qory checked the descriptor before that, so it reports that descriptor 3 is not
// open, and writes nothing to the file.
func TestMainClaimsTheStatusDescriptorFirst(t *testing.T) {
	file := filepath.Join(t.TempDir(), "opened")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, _, _ := runChild(t, "statusorder", statusfd.Env+"=3", envStatusFile+"="+file)
	if !strings.Contains(stdout, statusPrefix+"fd=3 err=<nil>\n") {
		t.Skipf("the update check did not get descriptor 3:\n%s", stdout)
	}
	if want := fmt.Sprintf("qory run: open descriptor 3 for qory run, or unset QORY_STATUS_FD: %v; qory run writes its lines to stderr only\n", unix.EBADF); !strings.Contains(stderr, want) {
		t.Errorf("stderr %q, want it to contain %q", stderr, want)
	}
	if data, err := os.ReadFile(file); err != nil || len(data) != 0 {
		t.Errorf("the file the update check opened: %q, %v", data, err)
	}
}

// TestMainDeniesCoreDumps is qory after start: the core size limit is 0, soft and hard,
// and on Linux the process is not dumpable, PR_GET_DUMPABLE 0. The command runs.
func TestMainDeniesCoreDumps(t *testing.T) {
	stdout, stderr, r, status := runChild(t, "harden")
	if status != 0 {
		t.Fatalf("exit status %d\n%s%s", status, stdout, stderr)
	}
	if !strings.Contains(stdout, resultPrefix) || !strings.Contains(stdout, "\"version\"") {
		t.Fatalf("qory version did not run, or the child did not report:\n%s%s", stdout, stderr)
	}
	if r.CoreCur != 0 || r.CoreMax != 0 {
		t.Errorf("RLIMIT_CORE %d soft and %d hard, want 0 and 0", r.CoreCur, r.CoreMax)
	}
	want := -1
	if isLinux {
		want = 0
	}
	if r.Dumpable != want {
		t.Errorf("PR_GET_DUMPABLE %d, want %d", r.Dumpable, want)
	}
}

// statusLines runs the child in mode with QORY_STATUS_FD=3 and descriptor 3 the write
// end of a pipe, and returns what reached the pipe and the child's stderr.
func statusLines(t *testing.T, mode string) (status, stderr string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, stderr, _, _ = runChildWith(t, mode, []*os.File{w}, statusfd.Env+"=3")
	w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), stderr
}

// TestMainWritesAFlagErrorToTheStatusDescriptor is qory run with a flag it does not
// have and QORY_STATUS_FD=3: the error line reaches descriptor 3 as well as stderr.
func TestMainWritesAFlagErrorToTheStatusDescriptor(t *testing.T) {
	status, stderr := statusLines(t, "statusflag")
	if !strings.HasPrefix(status, "✗ ") || !strings.Contains(status, "--no-such-flag") || !strings.Contains(stderr, "--no-such-flag") {
		t.Errorf("descriptor 3 got %q; stderr %q", status, stderr)
	}
}

// TestMainWritesAHardeningErrorToTheStatusDescriptor is qory run whose hardening fails
// with QORY_STATUS_FD=3: the error line reaches descriptor 3, its value of the mask list
// as ***.
func TestMainWritesAHardeningErrorToTheStatusDescriptor(t *testing.T) {
	status, stderr := statusLines(t, "statusharden")
	if want := "✗ allow the test's call: it failed with ***\n"; status != want {
		t.Errorf("descriptor 3 got %q, want %q; stderr %q", status, want, stderr)
	}
}
