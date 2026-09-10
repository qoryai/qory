package compose_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/stack"
)

// moduleWithVariants writes a module whose skills come from a different directory per variant,
// with the given manifest tail, and a stack targeting the given runtimes.
func moduleWithVariants(t *testing.T, manifest, runtimes string) (*stack.Stack, error) {
	t.Helper()
	dir := t.TempDir()
	module := filepath.Join(dir, "modules", "core")
	for _, v := range []string{"claude", "codex"} {
		skill := filepath.Join(module, "skills-"+v, "review")
		if err := os.MkdirAll(skill, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: review\n---\n"+v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(module, "qory-module.yaml"), []byte(
		"apiVersion: "+stack.APIVersion+"\nname: core\nvariants:\n"+manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, stack.FileName)
	doc := "apiVersion: " + stack.APIVersion +
		"\ntarget:\n  runtime: " + runtimes + "\nmodules:\n  - name: core\n    source: {path: modules/core}\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return stack.Load(file)
}

// TestComposeRefusesAModuleThatReadsDifferentlyPerRuntime is the one case a target of several
// runtimes cannot render: the composed tree holds one copy of each entry.
func TestComposeRefusesAModuleThatReadsDifferentlyPerRuntime(t *testing.T) {
	p, err := moduleWithVariants(t,
		"  claude: {skills: skills-claude}\n  codex: {skills: skills-codex}\n", "[claude, codex]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = compose.Compose(p)
	if err == nil {
		t.Fatal("the compose was accepted")
	}
	for _, want := range []string{"module core", "variant claude", "variant codex", "one runtime at a time"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestComposeAcceptsAModuleThatReadsTheSameForEveryRuntime is the common case: a variant for
// one runtime and a default the others share.
func TestComposeAcceptsAModuleThatReadsTheSameForEveryRuntime(t *testing.T) {
	p, err := moduleWithVariants(t,
		"  claude: {skills: skills-claude}\n  default: claude\n", "[claude, codex]")
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Name != "review" {
		t.Fatalf("entries = %v", res.Entries)
	}
	if res.Modules[0].Variant != "claude" {
		t.Errorf("variant = %q, want claude for both runtimes", res.Modules[0].Variant)
	}
	data, err := os.ReadFile(filepath.Join(res.Entries[0].Path, "SKILL.md"))
	if err != nil || !strings.Contains(string(data), "claude") {
		t.Errorf("entry reads %q (%v)", data, err)
	}
}

// TestComposeForOneRuntimeStillPicksItsOwnVariant keeps the single-runtime behaviour intact.
func TestComposeForOneRuntimeStillPicksItsOwnVariant(t *testing.T) {
	p, err := moduleWithVariants(t,
		"  claude: {skills: skills-claude}\n  codex: {skills: skills-codex}\n", "codex")
	if err != nil {
		t.Fatal(err)
	}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Modules[0].Variant != "codex" {
		t.Errorf("variant = %q, want codex", res.Modules[0].Variant)
	}
}
