package prebuild_test

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/testenv"
)

// TestTheTestsStartFromTheFixedEnvironment checks that no variable outside
// [testenv.FixedEnvironment] is in the environment of the tests, and that a child of this
// test binary started with a secret in its environment starts without it, so TestMain
// calls [testenv.StartFromFixedEnvironment].
func TestTheTestsStartFromTheFixedEnvironment(t *testing.T) {
	const test = "TestTheTestsStartFromTheFixedEnvironment"
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !slices.Contains(testenv.FixedEnvironment, name) {
			t.Errorf("%s is in the environment of the tests but not in the fixed environment", name)
		}
	}
	if slices.Contains(os.Args, "--fixed-env-child") {
		return
	}
	c := exec.Command(os.Args[0], "-test.v", "-test.run=^"+test+"$", "--", "--fixed-env-child")
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "QORY_PROBE_SECRET=probe-value"}
	out, err := c.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+test+" ") {
		t.Fatalf("the test binary started with a variable that is not fixed failed: %v\n%s", err, out)
	}
}
