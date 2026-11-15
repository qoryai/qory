package stack

import (
	"strings"
	"testing"
	"time"
)

// TestLoadReadsTheHookPoints reads each workflow's hook points and the limits under
// extending.hooks, and a block that leaves a limit out as its default: 3 per point, 10
// per workflow and 10 minutes.
func TestLoadReadsTheHookPoints(t *testing.T) {
	isolate(t)
	p, err := Load(write(t, withWorkflows("  review-changes:\n    hook_points:\n      after-review-round: {at: end, kinds: [check, review]}\n  verify:\n    hook_points:\n      before-matrix: {at: start, kinds: [check]}\nextending:\n  hooks: {per_point: 2, timeout: 5m}\n")))
	if err != nil {
		t.Fatal(err)
	}
	hp := p.Workflows["review-changes"].HookPoints["after-review-round"]
	if hp.At != HookAtEnd || strings.Join(hp.Kinds, " ") != "check review" {
		t.Errorf("after-review-round: %+v", hp)
	}
	if hp := p.Workflows["verify"].HookPoints["before-matrix"]; hp.At != HookAtStart {
		t.Errorf("before-matrix: %+v", hp)
	}
	l := p.Extending.Hooks
	if l.PerPoint() != 2 || l.PerWorkflow() != DefaultHooksPerWorkflow || l.Timeout != 5*time.Minute || l.TimeoutShown() != "5m" {
		t.Errorf("limits: per point %d, per workflow %d, timeout %v (%s)", l.PerPoint(), l.PerWorkflow(), l.Timeout, l.TimeoutShown())
	}
	p, err = Load(write(t, valid+"extending:\n  hooks: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	l = p.Extending.Hooks
	if l.PerPoint() != 3 || l.PerWorkflow() != 10 || l.Timeout != 10*time.Minute || l.TimeoutShown() != "10m" {
		t.Errorf("defaults: per point %d, per workflow %d, timeout %v (%s)", l.PerPoint(), l.PerWorkflow(), l.Timeout, l.TimeoutShown())
	}
}

// TestLoadRefusesABrokenHookPoint refuses a hook point whose name, place or kinds break
// the contract, a point name two workflows declare, limits that let no hook in or a
// timeout that is no duration, and harness.hooks in a stack file, which is a checkout's
// key.
func TestLoadRefusesABrokenHookPoint(t *testing.T) {
	isolate(t)
	for _, tc := range []struct{ body, want string }{
		{withWorkflows("  build:\n    hook_points:\n      After-Plan: {at: end, kinds: [check]}\n"), `name the hook point "After-Plan" with a lower-case letter`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: middle, kinds: [check]}\n"), `set workflows.build.hook_points."after-plan".at to end, start or inside: "middle" is none of them`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {kinds: [check]}\n"), `set workflows.build.hook_points."after-plan".at to end, start or inside: the point sets none`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: end, kinds: []}\n"), `.kinds to one or more of check, review and enrich: the point takes no kind`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: end, kinds: [lint]}\n"), `.kinds to kinds from check, review and enrich: "lint" is none of them`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: end, kinds: [check, check]}\n"), `list check once in workflows.build.hook_points."after-plan".kinds`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: end, kinds: [check]}\n  plan:\n    hook_points:\n      after-plan: {at: end, kinds: [check]}\n"), `rename the hook point after-plan of workflow build or of workflow plan: both declare it, and a hook selects a point by its name alone`},
		{valid + "extending:\n  hooks: {per_point: 0}\n", `set extending.hooks.per_point to 1 or more: 0 lets no hook in`},
		{valid + "extending:\n  hooks: {per_workflow: -1}\n", `set extending.hooks.per_workflow to 1 or more: -1 lets no hook in`},
		{valid + "extending:\n  hooks: {timeout: soon}\n", `set extending.hooks.timeout to a duration above zero, such as 10m: "soon" is not one`},
		{valid + "hooks:\n  - {at: after-plan, kind: check, run: x.sh}\n", `key "hooks" is not one qory-stack.yaml reads`},
	} {
		_, err := Load(write(t, tc.body))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s:\nerr = %v\nwant %q", tc.body, err, tc.want)
		}
	}
}

// TestHookDefaults is a hook's name and whether it is required when qory.yaml leaves
// them out: the agent's name or the program's file name, and required.
func TestHookDefaults(t *testing.T) {
	no := false
	for _, tc := range []struct {
		h        Hook
		name     string
		required bool
	}{
		{Hook{Command: []string{"scripts/check-licences.sh", "--all"}}, "check-licences.sh", true},
		{Hook{Agent: "licence-reviewer", RequiredValue: &no}, "licence-reviewer", false},
		{Hook{NameText: "licences", Agent: "licence-reviewer"}, "licences", true},
	} {
		if tc.h.Name() != tc.name || tc.h.Required() != tc.required {
			t.Errorf("%+v: name %q required %v, want %q %v", tc.h, tc.h.Name(), tc.h.Required(), tc.name, tc.required)
		}
	}
}

// TestValidateRefusesABrokenHook refuses each hook of a checkout's document that breaks
// a rule that needs no base, with the fix first, and sets the program and timeout of a
// hook that breaks none.
func TestValidateRefusesABrokenHook(t *testing.T) {
	for _, tc := range []struct {
		h    Hook
		want string
	}{
		{Hook{Kind: "check", Run: "x.sh"}, "set hooks[0].at to a hook point of the base stack: the hook sets none"},
		{Hook{At: "after-plan", Run: "x.sh"}, "set hooks[0].kind to check, review or enrich: the hook sets none"},
		{Hook{At: "after-plan", Kind: "lint", Run: "x.sh"}, `set hooks[0].kind to check, review or enrich: "lint" is none of them`},
		{Hook{At: "after-plan", Kind: "check", Run: "x.sh", Agent: "a"}, "set hooks[0].run or hooks[0].agent, not both: a hook runs a program or an agent"},
		{Hook{At: "after-plan", Kind: "check", Run: "  "}, "set hooks[0].run or hooks[0].agent: a hook runs a program or an agent, and this one sets neither"},
		{Hook{At: "after-plan", Kind: "review", Run: "x.sh"}, "set hooks[0].agent in place of run: a review hook runs an agent"},
		{Hook{At: "after-plan", Kind: "review", Agent: "a/b"}, `set hooks[0].agent to the name of an agent: "a/b" is not one path segment`},
		{Hook{At: "after-plan", Kind: "check", Run: "/usr/bin/true"}, `set hooks[0].run to a program by its path from the directory of qory.yaml, with forward slashes: "/usr/bin/true" is not one`},
		{Hook{At: "after-plan", Kind: "check", Run: "../other/x.sh"}, `set hooks[0].run to a program inside the repository: "../other/x.sh" leads out of it`},
		{Hook{At: "after-plan", Kind: "check", Run: "x.sh", NameText: "-x"}, `set hooks[0].name to a letter or digit, then up to 63 letters, digits, dots, hyphens and underscores: "-x" is not one`},
		{Hook{At: "after-plan", Kind: "check", Run: "scripts/.check"}, `the hook has no name, and the program's file name ".check" is not one`},
		{Hook{At: "after-plan", Kind: "check", Run: "x.sh", TimeoutText: "0s"}, `set hooks[0].timeout to a duration above zero, such as 5m: "0s" is not one`},
	} {
		h := tc.h
		if err := h.validate(0); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v:\nerr = %v\nwant %q", tc.h, err, tc.want)
		}
	}
	h := Hook{At: "after-plan", Kind: "enrich", Run: "scripts/notes.sh  --all", TimeoutText: "90s"}
	if err := h.validate(0); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.Command, "|") != "scripts/notes.sh|--all" || h.Timeout != 90*time.Second {
		t.Errorf("command %q, timeout %v", h.Command, h.Timeout)
	}
}
