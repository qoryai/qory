package cmd_test

import (
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestRunSeedIsHidden is the seed verb a pool's prebuild runs inside a container: in
// the tree, hidden from the help and the command reference.
func TestRunSeedIsHidden(t *testing.T) {
	if c, _, _ := cmd.Root().Find([]string{"run", "seed"}); c == nil || c.Name() != "seed" || !c.Hidden {
		t.Error("qory run seed is not a hidden verb")
	}
	out, err := run(t, "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, "seed")
}

// TestRunPrebuildAndFetchOnlyAreGone checks that qory run has no prebuild verb and no
// --fetch-only flag, and qory run prepare no --fetch-only flag: a pool fetches and
// builds a prebuild in its process.
func TestRunPrebuildAndFetchOnlyAreGone(t *testing.T) {
	if c, _, _ := cmd.Root().Find([]string{"run", "prebuild"}); c != nil && c.Name() == "prebuild" {
		t.Error("qory run prebuild is a verb")
	}
	for _, path := range [][]string{{"run"}, {"run", "prepare"}} {
		c, _, err := cmd.Root().Find(path)
		if err != nil {
			t.Fatal(err)
		}
		if c.Flags().Lookup("fetch-only") != nil {
			t.Errorf("qory %v has --fetch-only", path)
		}
	}
	if _, err := run(t, "run", "--fetch-only"); err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("qory run --fetch-only: %v, exit %d", err, cmd.ExitCode(err))
	}
}
