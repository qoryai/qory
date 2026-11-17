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

// sessionEnvChild is the argument that makes [TestNoParentTokenReachesASessionSpec] run
// as the checked process instead of starting it.
const sessionEnvChild = "--session-env-child"

// TestNoParentTokenReachesASessionSpec starts this test binary with GITHUB_TOKEN and
// ANTHROPIC_API_KEY set to a canary in its environment, and in that process runs qory
// run claude and checks that no entry of the session spec contains the canary. The
// process's environment is the one the tests start from, so the check fails when
// [startFromFixedEnvironment] is not called or lists a token's name.
func TestNoParentTokenReachesASessionSpec(t *testing.T) {
	const canary = "canary-token-value"
	if !slices.Contains(os.Args, sessionEnvChild) {
		c := exec.Command(os.Args[0], "-test.v", "-test.run=^TestNoParentTokenReachesASessionSpec$", "--", sessionEnvChild)
		c.Env = []string{"PATH=" + os.Getenv("PATH"), "GITHUB_TOKEN=" + canary, "ANTHROPIC_API_KEY=" + canary}
		out, err := c.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestNoParentTokenReachesASessionSpec ") {
			t.Fatalf("the process started with the tokens failed: %v\n%s", err, out)
		}
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
