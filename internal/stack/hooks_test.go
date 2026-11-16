package stack

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
// the contract, a point name two workflows declare, limits that let no hook in, a
// timeout that is not whole seconds of 1s or more, or empty, and hooks in a stack file,
// which is a checkout's key. Each refusal is a fix line of at most 100 characters, then a
// cause line with the file's path.
func TestLoadRefusesABrokenHookPoint(t *testing.T) {
	isolate(t)
	const fixName = "Name the hook point with a lower-case letter, then up to 54 lower-case letters, digits and hyphens."
	const fixAt = "Set at of the hook point to end, start, or inside."
	const fixTimeout = "Set extending.hooks.timeout to whole seconds, 1s or more, such as 10m."
	for _, tc := range []struct{ body, fix, cause string }{
		{withWorkflows("  build:\n    hook_points:\n      After-Plan: {at: end, kinds: [check]}\n"), fixName, `{path} lists "After-Plan" under workflows.build.hook_points.`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: middle, kinds: [check]}\n"), fixAt, `{path} sets workflows.build.hook_points.after-plan.at to "middle".`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {kinds: [check]}\n"), fixAt, `{path} sets no workflows.build.hook_points.after-plan.at.`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: end, kinds: []}\n"), "Add one or more kinds to the hook point: check, review, or enrich.", `{path} sets no kind in workflows.build.hook_points.after-plan.kinds.`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: end, kinds: [lint]}\n"), "Set each kind of the hook point to check, review, or enrich.", `{path} lists "lint" in workflows.build.hook_points.after-plan.kinds.`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: end, kinds: [check, check]}\n"), "List each kind of the hook point once.", `{path} lists check twice in workflows.build.hook_points.after-plan.kinds.`},
		{withWorkflows("  build:\n    hook_points:\n      after-plan: {at: end, kinds: [check]}\n  plan:\n    hook_points:\n      after-plan: {at: end, kinds: [check]}\n"), "Rename one of the two hook points in qory-stack.yaml: a point name is unique in a stack.", "Workflows build and plan both declare after-plan."},
		{valid + "extending:\n  hooks: {per_point: 0}\n", "Set extending.hooks.per_point to 1 or more.", "{path} sets 0, which lets no hook in."},
		{valid + "extending:\n  hooks: {per_workflow: -1}\n", "Set extending.hooks.per_workflow to 1 or more.", "{path} sets -1, which lets no hook in."},
		{valid + "extending:\n  hooks: {timeout: soon}\n", fixTimeout, `{path} sets "soon".`},
		{valid + "extending:\n  hooks: {timeout: 500ms}\n", fixTimeout, `{path} sets "500ms".`},
		{valid + "extending:\n  hooks: {timeout: 1500ms}\n", fixTimeout, `{path} sets "1500ms".`},
		{valid + "extending:\n  hooks: {timeout: \"\"}\n", fixTimeout, `{path} sets "".`},
	} {
		path := write(t, tc.body)
		_, err := Load(path)
		want := tc.fix + "\n  " + strings.ReplaceAll(tc.cause, "{path}", path)
		if err == nil || err.Error() != want {
			t.Errorf("%s:\nerr = %v\nwant %s", tc.body, err, want)
		}
		if len(tc.fix) > 100 {
			t.Errorf("the fix line has %d characters, more than 100: %s", len(tc.fix), tc.fix)
		}
	}
	_, err := Load(write(t, valid+"hooks:\n  - {at: after-plan, kind: check, run: x.sh}\n"))
	if err == nil || !strings.Contains(err.Error(), `key "hooks" is not one qory-stack.yaml reads`) {
		t.Errorf("hooks in a stack file: %v", err)
	}
	p, err := Load(write(t, valid+"extending:\n  hooks: {timeout: 1m30s}\n"))
	if err != nil || p.Extending.Hooks.Timeout != 90*time.Second {
		t.Errorf("a timeout of whole seconds: %+v, %v", p, err)
	}
}

// text is a pointer to s, for a key of a hook that is present.
func text(s string) *string { return &s }

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
		{Hook{NameText: text("licences"), Agent: "licence-reviewer"}, "licences", true},
	} {
		if tc.h.Name() != tc.name || tc.h.Required() != tc.required {
			t.Errorf("%+v: name %q required %v, want %q %v", tc.h, tc.h.Name(), tc.h.Required(), tc.name, tc.required)
		}
	}
}

// TestValidateRefusesABrokenHook refuses each hook of a checkout's document that breaks
// a rule that needs no base, with a fix line of at most 100 characters and a cause line,
// and sets the program, the agent and the timeout of a hook that breaks none. A key that
// is present with an empty value is refused, not read as left out.
func TestValidateRefusesABrokenHook(t *testing.T) {
	const fixName = "Set hooks[0].name to letters, digits, dots, hyphens and underscores, 64 at most."
	const fixTimeout = "Set hooks[0].timeout to whole seconds, 1s or more, such as 90s or 5m."
	for _, tc := range []struct {
		h          Hook
		fix, cause string
	}{
		{Hook{Kind: "check", RunText: text("x.sh")}, "Set hooks[0].at to a hook point of the base stack.", "The file sets no hooks[0].at."},
		{Hook{At: "after-plan", RunText: text("x.sh")}, "Set hooks[0].kind to check, review, or enrich.", "The file sets no hooks[0].kind."},
		{Hook{At: "after-plan", Kind: "lint", RunText: text("x.sh")}, "Set hooks[0].kind to check, review, or enrich.", `The file sets it to "lint".`},
		{Hook{At: "after-plan", Kind: "check", RunText: text("x.sh"), AgentText: text("a")}, "Set one of hooks[0].run and hooks[0].agent, not both.", "A hook runs a program or an agent, and hooks[0] in the file sets both."},
		{Hook{At: "after-plan", Kind: "check"}, "Set hooks[0].run or hooks[0].agent.", "A hook runs a program or an agent, and hooks[0] in the file sets neither."},
		{Hook{At: "after-plan", Kind: "check", RunText: text("  ")}, "Remove hooks[0].run, or give it a value.", "The file sets hooks[0].run to an empty value."},
		{Hook{At: "after-plan", Kind: "review", RunText: text(""), AgentText: text("licence-reviewer")}, "Remove hooks[0].run, or give it a value.", "The file sets hooks[0].run to an empty value."},
		{Hook{At: "after-plan", Kind: "review", AgentText: text("")}, "Remove hooks[0].agent, or give it a value.", "The file sets hooks[0].agent to an empty value."},
		{Hook{At: "after-plan", Kind: "check", RunText: text("x.sh"), NameText: text("")}, "Remove hooks[0].name, or give it a value.", "The file sets hooks[0].name to an empty value."},
		{Hook{At: "after-plan", Kind: "check", RunText: text("x.sh"), TimeoutText: text("")}, "Remove hooks[0].timeout, or give it a value.", "The file sets hooks[0].timeout to an empty value."},
		{Hook{At: "after-plan", Kind: "review", RunText: text("x.sh")}, "Set hooks[0].agent in place of hooks[0].run.", "A review hook runs an agent, and hooks[0] in the file runs a program."},
		{Hook{At: "after-plan", Kind: "review", AgentText: text("a/b")}, "Set hooks[0].agent to the name of an agent.", `The file sets it to "a/b", which is not one path segment.`},
		{Hook{At: "after-plan", Kind: "check", RunText: text("/usr/bin/true")}, "Set hooks[0].run to a program by its relative path, with forward slashes.", `The file sets the program "/usr/bin/true"; the path is from the directory of the file.`},
		{Hook{At: "after-plan", Kind: "check", RunText: text("../other/x.sh")}, "Set hooks[0].run to a program inside the checkout.", `The file sets the program "../other/x.sh", which leads out of the checkout.`},
		{Hook{At: "after-plan", Kind: "check", RunText: text("x.sh"), NameText: text("-x")}, fixName, `The file sets "-x"; a name starts with a letter or digit.`},
		{Hook{At: "after-plan", Kind: "check", RunText: text("scripts/.check")}, fixName, `The hook has no name, and the program's file name ".check" is not one; a name starts with a letter or digit.`},
		{Hook{At: "after-plan", Kind: "check", RunText: text("x.sh"), TimeoutText: text("0s")}, fixTimeout, `The file sets "0s".`},
		{Hook{At: "after-plan", Kind: "check", RunText: text("x.sh"), TimeoutText: text("500ms")}, fixTimeout, `The file sets "500ms".`},
		{Hook{At: "after-plan", Kind: "check", RunText: text("x.sh"), TimeoutText: text("2.5s")}, fixTimeout, `The file sets "2.5s".`},
	} {
		h := tc.h
		err := h.validate(0)
		if want := tc.fix + "\n  " + tc.cause; err == nil || err.Error() != want {
			t.Errorf("%+v:\nerr = %v\nwant %s", tc.h, err, want)
		}
		if len(tc.fix) > 100 {
			t.Errorf("the fix line has %d characters, more than 100: %s", len(tc.fix), tc.fix)
		}
	}
	h := Hook{At: "after-plan", Kind: "enrich", RunText: text("scripts/notes.sh  --all"), TimeoutText: text("1m30s")}
	if err := h.validate(0); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.Command, "|") != "scripts/notes.sh|--all" || h.Timeout != 90*time.Second || h.Agent != "" {
		t.Errorf("command %q, agent %q, timeout %v", h.Command, h.Agent, h.Timeout)
	}
	h = Hook{At: "after-plan", Kind: "review", AgentText: text("licence-reviewer")}
	if err := h.validate(0); err != nil || h.Agent != "licence-reviewer" || h.Command != nil {
		t.Errorf("an agent hook: %+v, %v", h, err)
	}
}

// TestHookReadsANullAsSet reads an entry of harness.hooks with a key set to null as a key
// that is present, which validate refuses as an empty value; a key the entry leaves out
// is not in Null, and a key a hook does not have is the decoder's unknown field, with its
// line.
func TestHookReadsANullAsSet(t *testing.T) {
	for _, tc := range []struct{ body, key string }{
		{"{at: after-plan, kind: check, run: ~}", "run"},
		{"{at: after-plan, kind: review, agent: null}", "agent"},
		{"at: after-plan\nkind: check\nrun: x.sh\nname:\n", "name"},
		{"at: after-plan\nkind: check\nrun: x.sh\ntimeout:\n", "timeout"},
		{"{at: after-plan, kind: check, run: x.sh, required: null}", "required"},
	} {
		var h Hook
		if err := yaml.Unmarshal([]byte(tc.body), &h); err != nil {
			t.Fatalf("%s: %v", tc.body, err)
		}
		if len(h.Null) != 1 || h.Null[0] != tc.key {
			t.Errorf("%s: null %q, want [%s]", tc.body, h.Null, tc.key)
		}
		want := "Remove hooks[0]." + tc.key + ", or give it a value.\n  The file sets hooks[0]." + tc.key + " to an empty value."
		if err := h.validate(0); err == nil || err.Error() != want {
			t.Errorf("%s:\nerr = %v\nwant %s", tc.body, err, want)
		}
	}
	var h Hook
	if err := yaml.Unmarshal([]byte("{at: after-plan, kind: check, run: x.sh, required: false}"), &h); err != nil || h.Null != nil || h.Required() || *h.RunText != "x.sh" {
		t.Errorf("no null: %+v, %v", h, err)
	}
	err := yaml.Unmarshal([]byte("at: after-plan\nkind: check\nwhen: always\n"), &h)
	if err == nil || !strings.Contains(err.Error(), "line 3: field when not found in type stack.Hook") {
		t.Errorf("an unknown key: %v", err)
	}
}

// TestHookErrorTakesTheFile is a refusal read through a file: the file's name goes into
// the fix line and its path into the cause line, and a % in the path stays as it is.
func TestHookErrorTakesTheFile(t *testing.T) {
	err := hookErrorf("Remove harness.hooks from {file}.", "{path} defines a stack, and %s has %d.", "x", 2)
	got := err.In("/repo/100%/qory.yaml").Error()
	if want := "Remove harness.hooks from qory.yaml.\n  /repo/100%/qory.yaml defines a stack, and x has 2."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestAHookErrorIndentsItsCause is a refusal of a hook with and without its file: the
// cause line follows the fix line, indented by two spaces, as the compose's refusals
// indent theirs, and Fix is the fix line alone.
func TestAHookErrorIndentsItsCause(t *testing.T) {
	err := hookErrorf("Set extending.hooks.per_point to 1 or more.", "{path} sets %d, which lets no hook in.", 0)
	for _, c := range []struct {
		err  *HookError
		want string
	}{
		{err, "Set extending.hooks.per_point to 1 or more.\n  The file sets 0, which lets no hook in."},
		{err.In("/work/qory-stack.yaml").(*HookError), "Set extending.hooks.per_point to 1 or more.\n  /work/qory-stack.yaml sets 0, which lets no hook in."},
	} {
		if got := c.err.Error(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
		if got := c.err.Fix(); got != "Set extending.hooks.per_point to 1 or more." {
			t.Errorf("fix %q", got)
		}
	}
}
