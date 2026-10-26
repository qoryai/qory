package cmd

import (
	"slices"
	"testing"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render"
)

// runsBare are the runtimes qory starts that are not in Forager's runtimes.json: Forager
// runs each as a bare runtime until its descriptor is added there.
var runsBare = []string{"codex", "gemini", "opencode", "amp", "cursor", "copilot"}

// listsDiffer are the lines on which listed, the runtimes of Forager's runtimes.json,
// starts, the runtimes qory renders with a launch template, and bare, [runsBare],
// disagree, each with its fix first.
func listsDiffer(listed, starts, bare []string) []string {
	var lines []string
	for _, name := range listed {
		if !slices.Contains(starts, name) {
			lines = append(lines, "add a render package with a launch template for "+name+" under internal/render: it is in Forager's runtimes.json")
		}
		if slices.Contains(bare, name) {
			lines = append(lines, "delete "+name+" from runsBare: it is in Forager's runtimes.json")
		}
	}
	for _, name := range starts {
		if !slices.Contains(listed, name) && !slices.Contains(bare, name) {
			lines = append(lines, "add a descriptor for "+name+" to Forager, or add "+name+" to runsBare: qory starts it, and it is not in Forager's runtimes.json")
		}
	}
	for _, name := range bare {
		if !slices.Contains(starts, name) {
			lines = append(lines, "delete "+name+" from runsBare: qory starts no runtime "+name)
		}
	}
	return lines
}

// launchers are the registered runtimes with a launch template: those qory starts.
func launchers() []string {
	var out []string
	for _, name := range render.Names() {
		if rt, err := render.Lookup(name); err == nil {
			if _, ok := rt.(render.Launcher); ok {
				out = append(out, name)
			}
		}
	}
	return out
}

// TestForagerAndQoryListTheSameRuntimes compares Forager's runtimes.json, at the pinned
// Forager module, with the runtimes qory starts: each runtime in the list has a render
// package with a launch template, and each runtime qory starts is in the list or in
// [runsBare]. any and goose have no launch template and are left out.
func TestForagerAndQoryListTheSameRuntimes(t *testing.T) {
	listed, err := foragerRuntimes()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) == 0 {
		t.Fatal("Forager's runtimes.json has no runtime")
	}
	for _, line := range listsDiffer(listed, launchers(), runsBare) {
		t.Error(line)
	}
}

// TestTheRuntimeListsDifferInEitherDirection is [listsDiffer] on lists that disagree:
// a runtime in the list that qory does not start, a runtime qory starts that is not in
// the list, a bare runtime that is in it, and a bare runtime qory does not start.
func TestTheRuntimeListsDifferInEitherDirection(t *testing.T) {
	got := listsDiffer([]string{"claude", "codex", "example"}, []string{"claude", "codex", "gemini"}, []string{"codex", "old"})
	want := []string{
		"delete codex from runsBare: it is in Forager's runtimes.json",
		"add a render package with a launch template for example under internal/render: it is in Forager's runtimes.json",
		"add a descriptor for gemini to Forager, or add gemini to runsBare: qory starts it, and it is not in Forager's runtimes.json",
		"delete old from runsBare: qory starts no runtime old",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := listsDiffer([]string{"claude"}, []string{"claude", "codex"}, []string{"codex"}); got != nil {
		t.Errorf("lists that agree: %q", got)
	}
}

// TestEveryAgentHomeIsTheOneAgentEnvMakes is each [render.AgentHomer]'s variable:
// qory run agent-env makes the agent's home fresh for [preparation.EnvAgentHome] alone.
func TestEveryAgentHomeIsTheOneAgentEnvMakes(t *testing.T) {
	for _, name := range render.Names() {
		rt, _ := render.Lookup(name)
		if h, ok := rt.(render.AgentHomer); ok && h.AgentHomeVariable() != preparation.EnvAgentHome {
			t.Errorf("%s reads its configuration directory from %s, and qory run agent-env makes the agent's home for %s alone", name, h.AgentHomeVariable(), preparation.EnvAgentHome)
		}
	}
}
