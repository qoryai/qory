package compose_test

import (
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// TestExtendKeepsTheDocumentsTarget is a checkout whose qory.yaml extends a stack and
// sets a target beside extends: the stack carries none, so the composed stack is for the
// document's target, model included, and the compose records the base.
func TestExtendKeepsTheDocumentsTarget(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"base/" + stack.FileName:              "apiVersion: qory.dev/v1alpha1\nname: core\nextending:\n  kinds: [skills]\nmodules:\n  - name: a\n    source:\n      path: modules/a\n",
		"base/modules/a/qory-module.yaml":     "apiVersion: qory.dev/v1alpha1\nname: a\n",
		"base/modules/a/commands/ship.md":     "ship\n",
		"app/" + config.FileName:              "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {path: ../base}\n  target:\n    runtime: codex\n    model: gpt-5\n",
		"app/modules/b/qory-module.yaml":      "apiVersion: qory.dev/v1alpha1\nname: b\n",
		"app/modules/b/skills/greet/SKILL.md": "# greet\n",
	})
	p, err := config.Compose(filepath.Join(dir, "app", config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	merged, base, err := compose.LoadBase(p, "", compose.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if base == nil || base.Name != "core" {
		t.Fatalf("base = %+v, want core", base)
	}
	if merged.Target.Runtimes.String() != "codex" || merged.Target.Model != "gpt-5" {
		t.Fatalf("target = %+v, want the document's, codex with gpt-5", merged.Target)
	}
	res, err := compose.ComposeWith(merged, compose.Options{Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stack.Target.Runtimes.String() != "codex" || res.Stack.Target.Model != "gpt-5" {
		t.Errorf("composed target = %+v, want the document's", res.Stack.Target)
	}
	if len(res.Entries) != 1 || res.Entries[0].Module != "a" {
		t.Errorf("entries = %+v, want the base's command alone", res.Entries)
	}
}
