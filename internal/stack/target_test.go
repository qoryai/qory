package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStack writes a stack whose target block is the given YAML and loads it.
func writeStack(t *testing.T, target string) (*Stack, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "modules", "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, FileName)
	doc := "apiVersion: " + APIVersion + "\ntarget:\n" + target +
		"modules:\n  - name: core\n    source: {path: modules/core}\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(file)
}

// TestTargetTakesOneRuntimeOrAList is the two spellings of target.runtime.
func TestTargetTakesOneRuntimeOrAList(t *testing.T) {
	for _, c := range []struct {
		name   string
		target string
		want   string
	}{
		{"one name", "  runtime: claude\n", "claude"},
		{"one name quoted", "  runtime: \"claude\"\n", "claude"},
		{"a flow list", "  runtime: [claude, codex]\n", "claude, codex"},
		{"a block list", "  runtime:\n    - claude\n    - codex\n", "claude, codex"},
		{"a list of one", "  runtime: [codex]\n", "codex"},
		{"the file's order", "  runtime: [codex, claude]\n", "codex, claude"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := writeStack(t, c.target)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Target.Runtimes.String(); got != c.want {
				t.Errorf("runtimes = %q, want %q", got, c.want)
			}
			if p.Target.Runtimes.First() != strings.Split(c.want, ", ")[0] {
				t.Errorf("First = %q", p.Target.Runtimes.First())
			}
		})
	}
}

// TestTargetRefuses covers every target a stack may not carry.
func TestTargetRefuses(t *testing.T) {
	for _, c := range []struct {
		name   string
		target string
		want   string
	}{
		{"no runtime", "  model: opus\n", "target.runtime is required"},
		{"an empty list", "  runtime: []\n", "target.runtime is required"},
		{"an empty name", "  runtime: [claude, \"\"]\n", "target.runtime names an empty runtime"},
		{"the same runtime twice", "  runtime: [claude, codex, claude]\n", "target.runtime names claude twice"},
		{"a mapping", "  runtime: {claude: opus}\n", "one runtime name or a list of them"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := writeStack(t, c.target)
			if err == nil {
				t.Fatal("the stack was accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to name %q", err, c.want)
			}
		})
	}
}

// TestRuntimesValidateIsCalledOnTheFlagToo pins that the command module can validate what
// --runtime put in place of the stack's target.
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
