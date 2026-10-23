package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/report"
)

// TestTierEffortGoesWithTheTiersModel composes with --tier and --tier-effort for one tier
// that the machine's map sets as well: the command line's entry wins whole, so the agent
// gets the flag's model and the flag's effort, in place of the effort its file sets, and
// the row shows both flags as its source. With --tier alone the entry has no effort, so the
// machine's effort is not merged in and the agent keeps the effort its file sets.
func TestTierEffortGoesWithTheTiersModel(t *testing.T) {
	root := tierCheckout(t)
	machineConfig(t, "harness:\n  tiers:\n    light: {model: claude-opus-5-5, effort: max}\n")
	tierModule(t, root, "claude", tierAgents)
	out, err := run(t, "harness", "compose", "--tier", "light=claude-haiku-5-5", "--tier-effort", "light=xhigh")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := fieldRows(out)["tier"][0]; got != "light  claude-haiku-5-5, effort xhigh  (agent explorer; from --tier and --tier-effort)" {
		t.Errorf("tier row %q\n%s", got, out)
	}
	explorer := filepath.Join(root, ".qory", "harness", "claude", "agents", "explorer.md")
	if got, want := readText(t, explorer), "---\nname: explorer\nmodel: claude-haiku-5-5\neffort: xhigh\n---\nExplores.\n"; got != want {
		t.Errorf("agents/explorer.md:\n%s\nwant:\n%s", got, want)
	}
	if got := readReport(t, root).Tiers["light"]; got != (report.Tier{Model: "claude-haiku-5-5", Effort: "xhigh", From: "--tier"}) {
		t.Errorf("the report's light tier is %+v", got)
	}

	out, err = run(t, "harness", "compose", "--tier", "light=claude-haiku-5-5")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := fieldRows(out)["tier"][0]; got != "light  claude-haiku-5-5  (agent explorer; from --tier)" {
		t.Errorf("--tier alone: tier row %q\n%s", got, out)
	}
	if got, want := readText(t, explorer), "---\nname: explorer\nmodel: claude-haiku-5-5\neffort: low\n---\nExplores.\n"; got != want {
		t.Errorf("--tier alone: agents/explorer.md:\n%s\nwant:\n%s", got, want)
	}
}

// TestTierEffortMistakes stops at each mistake of --tier-effort with exit status 2, the
// fix first, before anything is written: a value of another form, a name that is not a tier,
// a tier given twice, an effort for a tier --tier does not map, and an effort a claude
// agent does not take, whose error shows --tier-effort as its source.
func TestTierEffortMistakes(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--tier-effort", "light"}, "--tier-effort light: write --tier-effort <tier>=<effort>, such as --tier-effort thinking=high"},
		{[]string{"--tier-effort", "light="}, "--tier-effort light=: write --tier-effort <tier>=<effort>, such as --tier-effort thinking=high"},
		{[]string{"--tier-effort", "light=lo\nw"}, `--tier-effort light=lo\nw: write --tier-effort <tier>=<effort>, such as --tier-effort thinking=high`},
		{[]string{"--tier-effort", "heavy=low"}, "--tier-effort heavy=low: use one of the tiers light, fast and thinking; heavy is not a tier"},
		{[]string{"--tier", "light=a", "--tier-effort", "light=low", "--tier-effort", "light=high"}, "--tier-effort light is given twice; give each tier once"},
		{[]string{"--tier-effort", "light=low"}, "--tier-effort light=low: give --tier light=<model id> as well; the effort is for the model that --tier maps the tier to"},
		{[]string{"--tier", "thinking=claude-opus-5-5", "--tier-effort", "thinking=ultra"}, "tier thinking: set effort to one of low, medium, high, xhigh, max for claude agents; ultra is not one (from --tier-effort)"},
	} {
		root := tierCheckout(t)
		machineConfig(t, "harness:\n  tiers:\n    light: claude-haiku-5-5\n")
		tierModule(t, root, "claude", tierAgents)
		out, err := run(t, append([]string{"harness", "compose"}, c.args...)...)
		if cmd.ExitCode(err) != 2 || err == nil || err.Error() != c.want {
			t.Errorf("%q: exit %d, err %v\nwant %s\n%s", c.args, cmd.ExitCode(err), err, c.want, out)
		}
		if _, err := os.Stat(filepath.Join(root, ".qory")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%q: .qory was written", c.args)
		}
	}
}

// TestTierEffortForATierNoAgentSets composes with --tier and --tier-effort for a tier no
// agent sets: nothing is written for it, no row contains it, and its effort is not checked,
// as for an entry of harness.tiers that no agent uses.
func TestTierEffortForATierNoAgentSets(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "claude", map[string]string{"planner": tierAgents["planner"]})
	out, err := run(t, "harness", "compose", "--tier", "thinking=claude-opus-5-5", "--tier", "fast=claude-haiku-5-5", "--tier-effort", "fast=ultra")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := fieldRows(out)["tier"], []string{"thinking  claude-opus-5-5  (agent planner; from --tier)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tier rows %q, want %q\n%s", got, want, out)
	}
	if _, ok := readReport(t, root).Tiers["fast"]; ok {
		t.Error("the report lists the tier fast, which no agent sets")
	}
	lacks(t, out, "ultra")
}

// TestRunPassesTheTierEffortToItsCompose runs with --tier and --tier-effort: the run's
// compose writes the tier's model and effort into the agent, and stops at an effort for a
// tier --tier does not map before the agent starts.
func TestRunPassesTheTierEffortToItsCompose(t *testing.T) {
	root := tierCheckout(t)
	tierModule(t, root, "claude", tierAgents)
	fakeLaunch(t, fakeRuntime(t))
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "claude", "--tier", "thinking=claude-opus-5-5", "--tier-effort", "thinking=max")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	planner := readText(t, filepath.Join(root, ".qory", "harness", "claude", "agents", "planner.md"))
	wants(t, planner, "model: claude-opus-5-5\n", "effort: max\n")
	out, err = run(t, "run", "claude", "--tier-effort", "thinking=max")
	if cmd.ExitCode(err) != 2 || !strings.Contains(err.Error(), "--tier-effort thinking=max: give --tier thinking=<model id> as well") {
		t.Errorf("--tier-effort alone: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "hello from")
}
