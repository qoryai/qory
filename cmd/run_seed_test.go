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

// TestRunPrebuildStepIsHidden is the verb a prebuild from a run code runs behind the
// wall of its run: in the tree, hidden from the help and the command reference. A wrong
// argument is an input error, before anything is built.
func TestRunPrebuildStepIsHidden(t *testing.T) {
	if c, _, _ := cmd.Root().Find([]string{"run", "prebuild-step"}); c == nil || c.Name() != "prebuild-step" || !c.Hidden {
		t.Error("qory run prebuild-step is not a hidden verb")
	}
	out, err := run(t, "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, "prebuild-step")
	if _, err := run(t, "run", "prebuild-step", "--out", "relative"); err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("a relative --out: %v, exit %d", err, cmd.ExitCode(err))
	}
	if _, err := run(t, "run", "prebuild-step", "--tag", "x"); err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("an unknown flag: %v, exit %d", err, cmd.ExitCode(err))
	}
}
