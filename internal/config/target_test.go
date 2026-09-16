package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// composeTarget writes a qory.yaml whose harness section holds the given target block
// under an own stack, or beside extends, and reads it as the checkout's document.
func composeTarget(t *testing.T, target string, extends bool) (*stack.Stack, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qory.yaml")
	doc := "apiVersion: qory.dev/v1alpha1\nharness:\n"
	if extends {
		doc += "  extends: {path: ../base}\n"
	}
	doc += "  target:\n" + target + "  modules:\n    - name: core\n"
	writeRaw(t, path, doc)
	return config.Compose(path)
}

// TestTargetTakesOneRuntimeOrAList is the two spellings of target.runtime in a
// checkout's document, read the same under an own stack and beside extends.
func TestTargetTakesOneRuntimeOrAList(t *testing.T) {
	for _, c := range []struct {
		name   string
		target string
		want   string
	}{
		{"one name", "    runtime: claude\n", "claude"},
		{"one name quoted", "    runtime: \"claude\"\n", "claude"},
		{"a flow list", "    runtime: [claude, codex]\n", "claude, codex"},
		{"a block list", "    runtime:\n      - claude\n      - codex\n", "claude, codex"},
		{"a list of one", "    runtime: [codex]\n", "codex"},
		{"the file's order", "    runtime: [codex, claude]\n", "codex, claude"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, extends := range []bool{false, true} {
				p, err := composeTarget(t, c.target, extends)
				if err != nil {
					t.Fatalf("extends %v: %v", extends, err)
				}
				if got := p.Target.Runtimes.String(); got != c.want {
					t.Errorf("extends %v: runtimes = %q, want %q", extends, got, c.want)
				}
				if p.Target.Runtimes.First() != strings.Split(c.want, ", ")[0] {
					t.Errorf("extends %v: First = %q", extends, p.Target.Runtimes.First())
				}
			}
		})
	}
}

// TestTargetRefuses covers every target a checkout's document may not carry: under an
// own stack a target without a runtime is the section's own error, and under extends the
// runtime may be left out but not be misspelled.
func TestTargetRefuses(t *testing.T) {
	for _, c := range []struct {
		name    string
		target  string
		extends bool
		want    string
	}{
		{"no runtime", "    model: opus\n", false, "harness names no target.runtime and no stack to extend"},
		{"an empty list", "    runtime: []\n", false, "harness names no target.runtime and no stack to extend"},
		{"an empty name", "    runtime: [claude, \"\"]\n", false, "target.runtime names an empty runtime"},
		{"the same runtime twice", "    runtime: [claude, codex, claude]\n", false, "target.runtime names claude twice"},
		{"a mapping", "    runtime: {claude: opus}\n", false, "one runtime name or a list of them"},
		{"an empty name beside extends", "    runtime: [claude, \"\"]\n", true, "target.runtime names an empty runtime"},
		{"the same runtime twice beside extends", "    runtime: [claude, claude]\n", true, "target.runtime names claude twice"},
		{"a mapping beside extends", "    runtime: {claude: opus}\n", true, "one runtime name or a list of them"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := composeTarget(t, c.target, c.extends)
			if err == nil {
				t.Fatal("the document was accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to name %q", err, c.want)
			}
		})
	}
}

// TestTargetBesideExtendsMayLeaveTheRuntimeOut is a document that extends a stack and
// names a model alone, or no target at all: both compose, and the runtime is left to
// the machine's configuration or the flag.
func TestTargetBesideExtendsMayLeaveTheRuntimeOut(t *testing.T) {
	p, err := composeTarget(t, "    model: opus\n", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Target.Runtimes) != 0 || p.Target.Model != "opus" {
		t.Errorf("a model alone: %+v", p.Target)
	}
	path := filepath.Join(t.TempDir(), "qory.yaml")
	writeRaw(t, path, composeDoc)
	if p, err = config.Compose(path); err != nil || len(p.Target.Runtimes) != 0 || p.Target.Model != "" {
		t.Errorf("no target: %+v, %v", p.Target, err)
	}
}
