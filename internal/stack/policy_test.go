package stack

import (
	"strings"
	"testing"
)

// policyStack wraps an extending.target block into a stack document.
func policyStack(block string) string {
	return "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\nextending:\n  target:\n    " + strings.ReplaceAll(block, "\n", "\n    ") + "\n"
}

// TestLoadReadsExtendingTarget is the two spellings of each key under
// extending.target: one name or a list, in the file's order, either key alone, and a
// stack without the block, whose policy is nil.
func TestLoadReadsExtendingTarget(t *testing.T) {
	for _, c := range []struct {
		name     string
		block    string
		runtimes string
		models   string
	}{
		{"one name each", "runtime: claude\nmodel: opus", "claude", "opus"},
		{"one name quoted", "runtime: \"claude\"", "claude", ""},
		{"flow lists", "runtime: [claude, codex]\nmodel: [opus, sonnet]", "claude codex", "opus sonnet"},
		{"a block list in the file's order", "model:\n  - sonnet\n  - opus", "", "sonnet opus"},
		{"a list of one", "runtime: [codex]", "codex", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := Load(write(t, policyStack(c.block)))
			if err != nil {
				t.Fatal(err)
			}
			policy := p.Extending.Target
			if policy == nil {
				t.Fatal("no policy was read")
			}
			if got := strings.Join(policy.Runtime, " "); got != c.runtimes {
				t.Errorf("runtime = %q, want %q", got, c.runtimes)
			}
			if got := strings.Join(policy.Model, " "); got != c.models {
				t.Errorf("model = %q, want %q", got, c.models)
			}
		})
	}
	p, err := Load(write(t, "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\nextending:\n  kinds: [skills]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Extending.Target != nil {
		t.Errorf("a stack without the block has policy %+v", p.Extending.Target)
	}
}

// TestLoadRefusesABadExtendingTarget is every block a stack may not carry: one naming
// neither runtime nor model, an empty name and a name given twice, under either key,
// and a value that is neither a name nor a list.
func TestLoadRefusesABadExtendingTarget(t *testing.T) {
	for _, c := range []struct{ block, want string }{
		{"{}", "List a runtime, a model, or both in extending.target.\n  The block lists neither."},
		{"runtime: []", "List a runtime, a model, or both in extending.target.\n  The block lists neither."},
		{"runtime: [claude, \"\"]", "Remove the empty name from extending.target.runtime.\n  A name is not empty."},
		{"model: \"\"", "Remove the empty name from extending.target.model.\n  A name is not empty."},
		{"runtime: [claude, codex, claude]", "List claude once in extending.target.runtime.\n  extending.target.runtime lists it twice."},
		{"model: [opus, opus]", "List opus once in extending.target.model.\n  extending.target.model lists it twice."},
		{"runtime: {claude: opus}", "Set extending.target.runtime and extending.target.model each to one name or a list of names.\n  The decoder reports: "},
	} {
		_, err := Load(write(t, policyStack(c.block)))
		if err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want it to start with %q", c.block, err, c.want)
		}
	}
}

// TestTargetPolicyCheck is the policy against a resolved target: a runtime outside the
// list, a model outside it, and no model when models are listed are refused naming the
// base, a target inside both lists passes, a policy of runtimes alone takes any model or
// none, and a nil policy accepts everything.
func TestTargetPolicyCheck(t *testing.T) {
	policy := &TargetPolicy{Runtime: Names{"claude"}, Model: Names{"opus", "sonnet"}}
	base := "nextjs@v2.4.0"
	if err := policy.Check(Target{Runtimes: Runtimes{"claude"}, Model: "sonnet"}, base); err != nil {
		t.Errorf("a target inside the policy: %v", err)
	}
	for _, c := range []struct {
		name   string
		target Target
		want   string
	}{
		{"a runtime outside", Target{Runtimes: Runtimes{"claude", "codex"}, Model: "opus"}, "Set the target runtime to one that the base stack lists.\n  Runtime codex is not one the base stack nextjs@v2.4.0 is written for.\n  Runtimes: claude."},
		{"a model outside", Target{Runtimes: Runtimes{"claude"}, Model: "haiku"}, "Set the target model to one that the base stack lists.\n  Model haiku is not one the base stack nextjs@v2.4.0 is written for.\n  Models: opus, sonnet."},
		{"no model with models listed", Target{Runtimes: Runtimes{"claude"}}, "Set the target model to one that the base stack lists.\n  The target sets no model, and the base stack nextjs@v2.4.0 is written for one of these.\n  Models: opus, sonnet."},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := policy.Check(c.target, base)
			if err == nil || err.Error() != c.want {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
	runtimes := &TargetPolicy{Runtime: Names{"claude", "codex"}}
	for _, target := range []Target{{Runtimes: Runtimes{"codex", "claude"}}, {Runtimes: Runtimes{"claude"}, Model: "haiku"}} {
		if err := runtimes.Check(target, base); err != nil {
			t.Errorf("runtimes alone refused %+v: %v", target, err)
		}
	}
	var none *TargetPolicy
	for _, target := range []Target{{}, {Runtimes: Runtimes{"gemini"}}, {Runtimes: Runtimes{"claude"}, Model: "haiku"}} {
		if err := none.Check(target, base); err != nil {
			t.Errorf("a nil policy refused %+v: %v", target, err)
		}
	}
}
