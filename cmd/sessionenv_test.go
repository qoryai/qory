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
	// QORY_WALL_HELPER is the path of the Linux build of qory that TestRunBehindARealWall
	// mounts. Without them those tests skip.
	"QORY_TEST_DOCKER", "QORY_WALL_HELPER",
	// The docker command finds its engine and its configuration through these.
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG",
	// The go build in realWallImage finds the toolchain, its caches and its settings
	// through these; HOME no longer leads to them once emptyDir has moved it.
	"GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOTOOLCHAIN",
}

// startFromFixedEnvironment removes every variable of the process's environment that
// [fixedEnvironment] does not list, so a session spec that qory run builds from the
// process's environment (SessionSpec.Env) contains only those and the variables a test sets.
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

// valueFromTheParent is the value of every variable that the tests below set in the
// environment of the process that starts the tests.
const valueFromTheParent = "value-from-the-parent"

// TestNoParentTokenReachesASessionSpec starts this test binary with GITHUB_TOKEN,
// ANTHROPIC_API_KEY and EXAMPLE_SETTING set to [valueFromTheParent], runs qory run claude
// in that process and checks that no entry of the session spec contains the value. The
// check fails when [startFromFixedEnvironment] is not called, lists the name of one of the
// variables, or removes only the names that look like a token.
func TestNoParentTokenReachesASessionSpec(t *testing.T) {
	if !inSessionEnvChild() {
		startWithEnvironment(t, "TestNoParentTokenReachesASessionSpec", "PATH="+os.Getenv("PATH"),
			"GITHUB_TOKEN="+valueFromTheParent, "ANTHROPIC_API_KEY="+valueFromTheParent,
			"EXAMPLE_SETTING="+valueFromTheParent)
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
			if strings.Contains(kv, valueFromTheParent) {
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
// [fixedEnvironment] contains a word that marks a credential.
func TestTheFixedEnvironmentKeepsTheSwitchesAndPathsTheTestsRead(t *testing.T) {
	const test = "TestTheFixedEnvironmentKeepsTheSwitchesAndPathsTheTestsRead"
	if !inSessionEnvChild() {
		for _, name := range fixedEnvironment {
			for _, part := range []string{"TOKEN", "KEY", "SECRET", "PASS", "AUTH", "COOKIE", "CREDENTIAL"} {
				if strings.Contains(name, part) {
					t.Errorf("%s in the fixed environment contains %s, so its value may be a credential", name, part)
				}
			}
		}
		startWithEnvironment(t, test, "PATH="+os.Getenv("PATH"), "GITHUB_TOKEN="+valueFromTheParent,
			"QORY_TEST_DOCKER=1", "GOCACHE=/cache/example")
		return
	}
	for name, want := range map[string]string{"QORY_TEST_DOCKER": "1", "GOCACHE": "/cache/example", "GITHUB_TOKEN": ""} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s is %q at the start of the tests, want %q", name, got, want)
		}
	}
}
