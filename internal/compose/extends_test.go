package compose_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestLoadBaseStopsABaseLinkedOutOfItsClone is a checkout's qory.yaml that extends a
// git base whose qory-stack.yaml, or whose repository's qory.yaml, is a link to a file
// outside the repository: the base is not read, and the error shows the base by its
// address, since the harness is shown.
func TestLoadBaseStopsABaseLinkedOutOfItsClone(t *testing.T) {
	for _, c := range []struct{ link, want string }{
		{"stacks/web/" + stack.FileName, ": qory-stack.yaml links outside the repository"},
		{config.FileName, ": qory.yaml at the root of its repository links outside the repository"},
	} {
		t.Run(c.link, func(t *testing.T) {
			isolateAll(t)
			outside := filepath.Join(t.TempDir(), "elsewhere.yaml")
			if err := os.WriteFile(outside, []byte("apiVersion: qory.dev/v1alpha1\nname: elsewhere\nextending: {}\nmodules:\n  - name: core\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			url := gitRepo(t, "", map[string]string{
				"stacks/web/" + stack.FileName:  "apiVersion: qory.dev/v1alpha1\nname: web\nextending: {}\nmodules:\n  - name: core\n",
				"modules/core/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: core\n",
			})
			dir := strings.TrimPrefix(url, "file://")
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(c.link))); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, filepath.FromSlash(c.link))); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "-q", "-m", "link"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git: %v\n%s", err, out)
				}
			}
			checkout := writeTree(t, map[string]string{
				config.FileName: "apiVersion: qory.dev/v1alpha1\nharness:\n  extends: {git: \"" + url + "\", ref: main, path: stacks/web}\n",
			})
			p, err := config.Compose(filepath.Join(checkout, config.FileName))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = compose.LoadBase(p, "", compose.Options{Cache: t.TempDir()})
			if want := "extends " + url + "#main:stacks/web" + c.want; err == nil || err.Error() != want {
				t.Fatalf("got %v\nwant %s", err, want)
			}
		})
	}
}
