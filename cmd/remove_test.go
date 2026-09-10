package cmd_test

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/ui"
)

// TestRemoveTakesTheLinksAndTheQoryDir checks that remove after a compose prints what it
// removed, leaves neither link nor qory directory, and puts the checkout's own files back
// the way they were.
func TestRemoveTakesTheLinksAndTheQoryDir(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	writeFile(t, filepath.Join(root, "README.md"), "The checkout's own README.\n")
	before := snapshot(t, root)

	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "harness", "remove")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app", "removed .claude", "removed .qory")

	gone(t, root, ".claude", ".qory")
	if after := snapshot(t, root); !maps.Equal(after, before) {
		t.Errorf("the checkout after remove is not the checkout before compose:\nbefore %v\nafter  %v", before, after)
	}
}

// TestRemoveKeepsTheCheckoutsOwnAgentsFile composes for the runtime that links AGENTS.md at
// the checkout root, where the checkout ships its own. Compose keeps that file and says so,
// and remove takes its own links only.
func TestRemoveKeepsTheCheckoutsOwnAgentsFile(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	own := filepath.Join(root, "AGENTS.md")
	writeFile(t, own, "The checkout's own instructions.\n")

	out, err := run(t, "harness", "compose", "--runtime", "any")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · any opus")
	wantsRow(t, out, "any", ".agents/skills")
	wantsRow(t, out, "kept", "AGENTS.md  (the checkout's own; not linked)")
	ownedByCheckout(t, own, "The checkout's own instructions.\n")
	linkTarget(t, root, filepath.Join(".agents", "skills"))

	out, err = run(t, "harness", "remove")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed .agents/skills", "removed .qory")
	lacks(t, out, "removed AGENTS.md")
	gone(t, root, ".qory", ".agents")
	ownedByCheckout(t, own, "The checkout's own instructions.\n")
}

// ownedByCheckout checks that path is still a regular file holding what the checkout wrote,
// and not a link into a home.
func ownedByCheckout(t *testing.T, path, content string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is %s, want a regular file", path, info.Mode())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("%s holds %q, want %q", path, data, content)
	}
}
