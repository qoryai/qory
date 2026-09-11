package render_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestDiffNamesEveryWayTwoTreesDiffer builds the tree a check would render and the home
// it would compare with, one difference of each kind between them, and checks the list:
// a file whose bytes changed, a path only the render holds, one only the home holds, a
// link pointing elsewhere, and a file where the render holds a link; a directory in both
// and an identical file in both are not listed, and the list is sorted by path.
func TestDiffNamesEveryWayTwoTreesDiffer(t *testing.T) {
	want, have := t.TempDir(), t.TempDir()
	for _, dir := range []string{want, have} {
		write(t, filepath.Join(dir, "same.md"), "same\n")
		write(t, filepath.Join(dir, "claude", "CLAUDE.md"), "rules\n")
		if err := os.MkdirAll(filepath.Join(dir, "claude", "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(want, "AGENTS.md"), "revised\n")
	write(t, filepath.Join(have, "AGENTS.md"), "old\n")
	write(t, filepath.Join(want, "claude", "skills", "release"), "")
	write(t, filepath.Join(have, "claude", "settings.local.json"), "{}")
	symlink(t, "/modules/app/skills/deploy", filepath.Join(want, "claude", "skills", "deploy"))
	symlink(t, "/modules/old/skills/deploy", filepath.Join(have, "claude", "skills", "deploy"))
	symlink(t, "/modules/app/hooks/guard.sh", filepath.Join(want, "hooks"))
	write(t, filepath.Join(have, "hooks"), "not a link\n")

	got, err := render.Diff(want, have)
	if err != nil {
		t.Fatal(err)
	}
	expected := []render.Difference{
		{Path: "AGENTS.md", What: "changed"},
		{Path: "claude/settings.local.json", What: "extra"},
		{Path: "claude/skills/deploy", What: "target"},
		{Path: "claude/skills/release", What: "missing"},
		{Path: "hooks", What: "kind"},
	}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("differences = %+v\nwant %+v", got, expected)
	}
	same, err := render.Diff(want, want)
	if err != nil || len(same) != 0 {
		t.Errorf("a tree against itself: %v, %v", same, err)
	}
	// A home that is not there is an empty tree, so every path of the render is missing.
	none, err := render.Diff(want, filepath.Join(have, "nowhere"))
	if err != nil || len(none) == 0 || none[0].What != "missing" {
		t.Errorf("against a missing tree: %v, %v", none, err)
	}
}

// symlink writes a link at path to target.
func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}
