package modelplace_test

import (
	"slices"
	"testing"

	"github.com/qoryai/qory/internal/modelplace"
)

// five are the effort values Forager's claude descriptor lists, for a run and for an agent.
var five = []string{"low", "medium", "high", "xhigh", "max"}

// TestClaudeReadsTheDescriptor reads claude's effort places from Forager's claude
// descriptor: the run's as the flag --effort, an agent's in the effort frontmatter, five
// values each. render.TierFront writes a tier's effort under the key effort, so this test
// fails when the descriptor lists another key.
func TestClaudeReadsTheDescriptor(t *testing.T) {
	run, ok := modelplace.RunEffort("claude")
	if !ok || run.Flag != "--effort" || run.Variable != "" || !slices.Equal(run.Values, five) {
		t.Errorf("run effort %+v, %v", run, ok)
	}
	agent, ok := modelplace.AgentEffort("claude")
	if !ok || agent.Frontmatter != "effort" || !slices.Equal(agent.Values, five) {
		t.Errorf("agent effort %+v, %v", agent, ok)
	}
}

// TestNoPlace reports false for a runtime whose descriptor has no model section and for a
// name Forager does not take.
func TestNoPlace(t *testing.T) {
	for _, name := range []string{"codex", "opencode", "", "Claude", "a b"} {
		if _, ok := modelplace.RunEffort(name); ok {
			t.Errorf("%q has a run effort place", name)
		}
		if _, ok := modelplace.AgentEffort(name); ok {
			t.Errorf("%q has an agent effort place", name)
		}
		if args, env := modelplace.Launch(name, "high"); args != nil || env != nil {
			t.Errorf("%q launches with %q, %q", name, args, env)
		}
	}
}

// TestLaunchPutsTheEffortInItsPlace writes the effort as the flag then the value, two
// arguments, the value as given, and nothing for no effort.
func TestLaunchPutsTheEffortInItsPlace(t *testing.T) {
	if args, env := modelplace.Launch("claude", "a;$`b"); !slices.Equal(args, []string{"--effort", "a;$`b"}) || env != nil {
		t.Errorf("args %q, env %q", args, env)
	}
	if args, env := modelplace.Launch("claude", ""); args != nil || env != nil {
		t.Errorf("no effort: %q, %q", args, env)
	}
}

// TestOfReturnsACopy changes the lists one call returns, and the next call returns the
// descriptor's values unchanged.
func TestOfReturnsACopy(t *testing.T) {
	p, ok := modelplace.Of("claude")
	if !ok || p.Effort == nil || p.Effort.Agent == nil {
		t.Fatalf("claude: %+v, %v", p, ok)
	}
	p.Effort.Run.Values[0] = "changed"
	p.Effort.Agent.Values[0] = "changed"
	p.Effort.Agent.Frontmatter = "changed"
	if run, _ := modelplace.RunEffort("claude"); !slices.Equal(run.Values, five) {
		t.Errorf("run values %q after a change to a copy", run.Values)
	}
	if agent, _ := modelplace.AgentEffort("claude"); !slices.Equal(agent.Values, five) || agent.Frontmatter != "effort" {
		t.Errorf("agent place %+v after a change to a copy", agent)
	}
}
