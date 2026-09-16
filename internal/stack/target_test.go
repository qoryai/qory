package stack

import (
	"path/filepath"
	"strings"
	"testing"
)

// compose validates a document built in Go as the harness section of a checkout's
// qory.yaml, the way the configuration package hands it to [NewCompose].
func compose(t *testing.T, p *Stack) (*Stack, error) {
	t.Helper()
	if p.APIVersion == "" {
		p.APIVersion = APIVersion
	}
	return NewCompose(filepath.Join(t.TempDir(), "qory.yaml"), p)
}

// TestNewComposeReadsTheCheckoutsOwnStack is a document with a target and modules and
// no extends, this repository's own stack: it composes with the target it names, and
// the same document without target.runtime is refused as before, since only a document
// that extends a stack may leave the runtime to the machine.
func TestNewComposeReadsTheCheckoutsOwnStack(t *testing.T) {
	p, err := compose(t, &Stack{Target: Target{Runtimes: Runtimes{"codex"}, Model: "o3"}, Modules: []Module{{Name: "app"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Target.Runtimes.String() != "codex" || p.Target.Model != "o3" || len(p.Modules) != 1 {
		t.Fatalf("own stack: %+v", p)
	}
	for _, c := range []struct {
		name   string
		target Target
		want   string
	}{
		{"no runtime", Target{Model: "opus"}, "target.runtime is required"},
		{"an empty list", Target{Runtimes: Runtimes{}}, "target.runtime is required"},
		{"an empty name", Target{Runtimes: Runtimes{"claude", ""}}, "target.runtime names an empty runtime"},
		{"the same runtime twice", Target{Runtimes: Runtimes{"claude", "codex", "claude"}}, "target.runtime names claude twice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := compose(t, &Stack{Target: c.target, Modules: []Module{{Name: "app"}}})
			if err == nil {
				t.Fatal("the document was accepted")
			}
			if !strings.HasSuffix(err.Error(), ": "+c.want) {
				t.Errorf("error = %q, want it to end with %q", err, c.want)
			}
		})
	}
	_, err = compose(t, &Stack{Target: Target{Runtimes: Runtimes{"claude"}}})
	if want := "modules is empty; a stack names at least one module"; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("an own stack without modules: err = %v, want %q", err, want)
	}
}

// TestNewComposeReadsATargetBesideExtends is a document that extends a stack and says
// what it composes it for: the target is kept beside extends, a document with extends
// and a target alone composes, a runtime named twice is refused with the runtime
// message, and a target of a model alone is kept for the machine's runtime.
func TestNewComposeReadsATargetBesideExtends(t *testing.T) {
	base := Source{Path: "../base"}
	p, err := compose(t, &Stack{Extends: base, Target: Target{Runtimes: Runtimes{"claude", "codex"}, Model: "opus"}, Modules: []Module{{Name: "app"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Extends != base || p.Target.Runtimes.String() != "claude, codex" || p.Target.Model != "opus" {
		t.Fatalf("with modules: %+v", p)
	}
	if p, err = compose(t, &Stack{Extends: base, Target: Target{Runtimes: Runtimes{"claude"}}}); err != nil || p.Target.Runtimes.String() != "claude" || len(p.Modules) != 0 {
		t.Fatalf("a target alone: %+v, %v", p, err)
	}
	if p, err = compose(t, &Stack{Extends: base, Target: Target{Model: "opus"}}); err != nil || p.Target.Model != "opus" || len(p.Target.Runtimes) != 0 {
		t.Fatalf("a model alone: %+v, %v", p, err)
	}
	for _, c := range []struct {
		name   string
		target Target
		want   string
	}{
		{"an empty name", Target{Runtimes: Runtimes{"claude", ""}}, "target.runtime names an empty runtime"},
		{"the same runtime twice", Target{Runtimes: Runtimes{"claude", "claude"}}, "target.runtime names claude twice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := compose(t, &Stack{Extends: base, Target: c.target})
			if err == nil {
				t.Fatal("the document was accepted")
			}
			if !strings.HasSuffix(err.Error(), ": "+c.want) {
				t.Errorf("error = %q, want it to end with %q", err, c.want)
			}
		})
	}
}

// TestRuntimesValidateIsCalledOnTheFlagToo pins that the command module can validate what
// --runtime put in place of the document's target.
func TestRuntimesValidateIsCalledOnTheFlagToo(t *testing.T) {
	if err := (Runtimes{"claude", "codex"}).Validate(); err != nil {
		t.Errorf("two runtimes: %v", err)
	}
	if err := (Runtimes{}).Validate(); err == nil {
		t.Error("an empty list was accepted")
	}
	if err := (Runtimes{"claude", "claude"}).Validate(); err == nil {
		t.Error("a repeated runtime was accepted")
	}
	if (Runtimes{}).First() != "" {
		t.Error("First of an empty list is not empty")
	}
}
