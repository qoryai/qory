package cmd_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestCollisionOutput composes the collision fixture from a checkout and checks that the
// output names what happened and prints the stack lines that fix it.
func TestCollisionOutput(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "collision-fails", root)
	out, err := run(t, "harness", "compose")
	if !errors.Is(err, cmd.ErrReported) {
		t.Fatalf("err = %v, want one marked reported", err)
	}
	if cmd.ExitCode(err) != cmd.ExitCollision {
		t.Errorf("exit code %d, want %d", cmd.ExitCode(err), cmd.ExitCollision)
	}
	for _, want := range []string{"skills/test is provided by 3 modules", "Fix", "- name: core", "- name: nextjs", "skills: [test]"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "- name: team") {
		t.Errorf("the kept module got an exclude:\n%s", out)
	}
}
