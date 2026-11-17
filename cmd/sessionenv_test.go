package cmd_test

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// fixedEnvironment is every environment variable the cmd tests keep from the process
// that starts them. A variable that is not listed is removed by [startFromFixedEnvironment],
// so a token in a developer's shell reaches no session spec and no test output.
var fixedEnvironment = []string{
	// The programs the tests start (git, sh, the fake runtimes) are found through PATH.
	"PATH",
	// HOME and TMPDIR are the paths git and the temporary directories start from, before
	// emptyDir and t.TempDir point the tests elsewhere.
	"HOME", "TMPDIR",
	// LANG and TERM keep the messages of git and the terminal checks the same as in a shell.
	"LANG", "TERM",
	// GOCOVERDIR is where a test binary built with -cover writes its coverage data.
	"GOCOVERDIR",
	// QORY_TEST_DOCKER=1 switches on the tests that start containers, and
	// QORY_WALL_HELPER names the Linux build of qory that TestRunBehindARealWall mounts;
	// without them those tests skip.
	"QORY_TEST_DOCKER", "QORY_WALL_HELPER",
	// The docker command finds its engine and its configuration through these.
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
	// The go build in realWallImage finds the toolchain, its caches and its settings
	// through these; HOME no longer leads to them once emptyDir has moved it.
	"GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOTOOLCHAIN",
}

// startFromFixedEnvironment removes every variable of the process's environment that
// [fixedEnvironment] does not list, so a session spec that qory run builds from the
// process's environment (SessionSpec.Env) holds only those and the variables a test sets.
func startFromFixedEnvironment() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(fixedEnvironment, name) {
			os.Unsetenv(name)
		}
	}
}

// sessionEnvChild is the argument that makes a test of this file run as the checked
// process instead of starting it.
const sessionEnvChild = "--session-env-child"

// inSessionEnvChild reports whether this process is the one a test of this file started
// with the environment env.
func inSessionEnvChild() bool { return slices.Contains(os.Args, sessionEnvChild) }

// startWithEnvironment starts this test binary to run only the test named test, with
// exactly the environment env, and fails t unless that test passes in it.
func startWithEnvironment(t *testing.T, test string, env ...string) {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.v", "-test.run=^"+test+"$", "--", sessionEnvChild)
	c.Env = env
	out, err := c.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+test+" ") {
		t.Fatalf("the process started with that environment failed: %v\n%s", err, out)
	}
}

// TestNoParentTokenReachesASessionSpec starts this test binary with GITHUB_TOKEN and
// ANTHROPIC_API_KEY set to a canary in its environment, and in that process runs qory
// run claude and checks that no entry of the session spec contains the canary. The
// process's environment is the one the tests start from, so the check fails when
// [startFromFixedEnvironment] is not called or lists a token's name.
func TestNoParentTokenReachesASessionSpec(t *testing.T) {
	const canary = "canary-token-value"
	if !inSessionEnvChild() {
		startWithEnvironment(t, "TestNoParentTokenReachesASessionSpec",
			"PATH="+os.Getenv("PATH"), "GITHUB_TOKEN="+canary, "ANTHROPIC_API_KEY="+canary)
		return
	}
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	var specs []session.SessionSpec
	defer cmd.SetJoinSession(func(s session.SessionSpec) { specs = append(specs, s) })()
	_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{})
	defer restore()
	if out, err := run(t, "run", "claude"); !errors.Is(err, cmd.ErrJoinedNowhere) {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if len(specs) == 0 {
		t.Fatal("qory run claude built no session spec")
	}
	for _, s := range specs {
		for _, kv := range s.Env {
			if strings.Contains(kv, canary) {
				name, _, _ := strings.Cut(kv, "=")
				t.Errorf("the session spec's environment contains the value of %s", name)
			}
		}
	}
}

// TestTheFixedEnvironmentKeepsTheSwitchesAndPathsTheTestsRead starts this test binary
// with a token, QORY_TEST_DOCKER and GOCACHE in its environment, and in that process
// checks that the tests start with the switch and the path and without the token, so an
// opt-in test is not skipped for want of its switch. It also checks that no name in
// [fixedEnvironment] can hold a credential.
func TestTheFixedEnvironmentKeepsTheSwitchesAndPathsTheTestsRead(t *testing.T) {
	const test = "TestTheFixedEnvironmentKeepsTheSwitchesAndPathsTheTestsRead"
	if !inSessionEnvChild() {
		for _, name := range fixedEnvironment {
			for _, part := range []string{"TOKEN", "KEY", "SECRET", "PASSWORD", "CREDENTIAL"} {
				if strings.Contains(name, part) {
					t.Errorf("%s in the fixed environment can hold a credential", name)
				}
			}
		}
		startWithEnvironment(t, test, "PATH="+os.Getenv("PATH"), "GITHUB_TOKEN=canary-token-value",
			"QORY_TEST_DOCKER=1", "GOCACHE=/cache/example")
		return
	}
	for name, want := range map[string]string{"QORY_TEST_DOCKER": "1", "GOCACHE": "/cache/example", "GITHUB_TOKEN": ""} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s is %q at the start of the tests, want %q", name, got, want)
		}
	}
}
