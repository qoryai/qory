package cmd_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestComposeWritesTheEffortInTheDescriptorsPlace composes for claude with --effort:
// the report records the effort, qory harness launch passes it as the flag --effort of
// Forager's claude descriptor, before the template's arguments, and qory harness inspect
// prints it. A compose without the flag takes harness.effort, and the flag wins over the
// key.
func TestComposeWritesTheEffortInTheDescriptorsPlace(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "claude", tierAgents)
	out, err := run(t, "harness", "compose", "--effort", "high")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := readReport(t, root).Target.Effort; got != "high" {
		t.Errorf("the report's effort is %q, want high", got)
	}
	out, err = run(t, "harness", "launch")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "claude --effort high --plugin-dir ")
	out, err = run(t, "harness", "inspect")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "effort", "high")

	machineConfig(t, "harness:\n  effort: low\n")
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := readReport(t, root).Target.Effort; got != "low" {
		t.Errorf("harness.effort: the report's effort is %q, want low", got)
	}
	if out, err := run(t, "harness", "compose", "--effort", "max"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := readReport(t, root).Target.Effort; got != "max" {
		t.Errorf("--effort over harness.effort: the report's effort is %q, want max", got)
	}
}

// TestComposeStopsAtAnEffortTheDescriptorDoesNotList stops with exit status 2 and the fix
// first at an effort value Forager's claude descriptor does not list for a run, from the
// flag and from harness.effort, with the values it lists. Nothing is composed.
func TestComposeStopsAtAnEffortTheDescriptorDoesNotList(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "claude", tierAgents)
	out, err := run(t, "harness", "compose", "--effort", "ultra")
	if cmd.ExitCode(err) != 2 {
		t.Fatalf("exit %d, want 2: %v\n%s", cmd.ExitCode(err), err, out)
	}
	if want := "set --effort to one of low, medium, high, xhigh, max for claude; ultra is not one"; err.Error() != want {
		t.Errorf("--effort: %q\nwant %q", err, want)
	}
	machineConfig(t, "harness:\n  effort: \"Hi\\n\"\n")
	_, err = run(t, "harness", "compose")
	if want := `~/.config/qory/qory.yaml: set harness.effort to one of low, medium, high, xhigh, max for claude; "Hi\n" is not one`; cmd.ExitCode(err) != 2 || err.Error() != want {
		t.Errorf("harness.effort: %v (exit %d)\nwant %q", err, cmd.ExitCode(err), want)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".qory", "harness-report.json")); statErr == nil {
		t.Error("a compose that stopped wrote a report")
	}
}

// TestComposeSkipsTheEffortForARuntimeWithoutAPlace composes for claude and codex with
// --effort: Forager has no descriptor with a place for codex's effort, so a row shows
// that qory writes none for codex, and claude gets the effort.
func TestComposeSkipsTheEffortForARuntimeWithoutAPlace(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "[claude, codex]", tierAgents)
	out, err := run(t, "harness", "compose", "--effort", "high")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var skipped []string
	for _, row := range fieldRows(out)["skipped"] {
		if strings.HasPrefix(row, "effort ") {
			skipped = append(skipped, row)
		}
	}
	if want := []string{"effort high for codex  (qory writes no effort for codex)"}; !reflect.DeepEqual(skipped, want) {
		t.Errorf("rows %q, want %q\n%s", skipped, want, out)
	}
	if got := readReport(t, root).Target.Effort; got != "high" {
		t.Errorf("the report's effort is %q, want high", got)
	}
}

// TestCheckComparesTheEffort finds the home stale when the effort of the compose differs
// from the one the report records, since a launch passes it and no file of the home
// contains it: changed, extra when the compose has none, and missing when only the
// compose has one.
func TestCheckComparesTheEffort(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "claude", tierAgents)
	if out, err := run(t, "harness", "compose", "--effort", "high"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := run(t, "harness", "compose", "--check", "--effort", "high"); err != nil {
		t.Fatalf("the same effort: %v\n%s", err, out)
	}
	for _, c := range []struct {
		args []string
		what string
	}{
		{[]string{"--effort", "low"}, "changed"},
		{nil, "extra"},
	} {
		out, err := run(t, append([]string{"harness", "compose", "--check"}, c.args...)...)
		if cmd.ExitCode(err) != cmd.ExitStale {
			t.Fatalf("%v: exit %d, want %d\n%s", c.args, cmd.ExitCode(err), cmd.ExitStale, out)
		}
		wantsRow(t, out, "target/effort", c.what)
	}
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "harness", "compose", "--check", "--effort", "high")
	if cmd.ExitCode(err) != cmd.ExitStale {
		t.Fatalf("exit %d, want %d\n%s", cmd.ExitCode(err), cmd.ExitStale, out)
	}
	wantsRow(t, out, "target/effort", "missing")
}

// TestRunPassesTheEffortToItsCompose runs with --effort: the run's compose records it, and
// the agent starts with --effort and the value before the launch's other arguments.
func TestRunPassesTheEffortToItsCompose(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "claude", tierAgents)
	fakeLaunch(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "claude", "--effort", "xhigh")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, " with --effort xhigh --settings ")
	if got := readReport(t, root).Target.Effort; got != "xhigh" {
		t.Errorf("the report's effort is %q, want xhigh", got)
	}
	out, err = run(t, "run", "claude", "--effort", "ultra")
	if cmd.ExitCode(err) != 2 || !strings.Contains(err.Error(), "set --effort to one of low, medium, high, xhigh, max for claude; ultra is not one") {
		t.Errorf("--effort ultra: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
}
