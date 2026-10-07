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

	got, err := render.Diff(want, have, "")
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
	same, err := render.Diff(want, want, "")
	if err != nil || len(same) != 0 {
		t.Errorf("a tree against itself: %v, %v", same, err)
	}
	// A home that is not there is an empty tree, so every path of the render is missing.
	none, err := render.Diff(want, filepath.Join(have, "nowhere"), "")
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

// TestDiffComparesALinkIntoTheCacheByWhatItHolds links the render and the home into two
// clones of one module under the cache: identical trees at different paths are the same,
// whatever the clones' .git holds, and a changed byte, an added file, a removed file, a
// changed mode or a target that is not there differ. A link outside the cache still
// compares by its target.
func TestDiffComparesALinkIntoTheCacheByWhatItHolds(t *testing.T) {
	clone := func(t *testing.T, dir string) string {
		t.Helper()
		write(t, filepath.Join(dir, ".git", "HEAD"), filepath.Base(dir)+"\n")
		write(t, filepath.Join(dir, "modules", "core", "AGENTS.md"), "# Core\n")
		write(t, filepath.Join(dir, "modules", "core", "hooks", "guard.sh"), "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(dir, "modules", "core", "hooks", "guard.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		symlink(t, "AGENTS.md", filepath.Join(dir, "modules", "core", "CLAUDE.md"))
		return dir
	}
	cases := []struct {
		name   string
		change func(t *testing.T, mod string)
		want   []render.Difference
	}{
		{"identical", func(*testing.T, string) {}, nil},
		{"a changed byte", func(t *testing.T, mod string) { write(t, filepath.Join(mod, "AGENTS.md"), "# Core!\n") }, []render.Difference{{Path: "modules/core", What: "target"}}},
		{"an added file", func(t *testing.T, mod string) { write(t, filepath.Join(mod, "README.md"), "") }, []render.Difference{{Path: "modules/core", What: "target"}}},
		{"a removed file", func(t *testing.T, mod string) {
			if err := os.Remove(filepath.Join(mod, "AGENTS.md")); err != nil {
				t.Fatal(err)
			}
		}, []render.Difference{{Path: "modules/core", What: "target"}}},
		{"a changed mode", func(t *testing.T, mod string) {
			if err := os.Chmod(filepath.Join(mod, "hooks", "guard.sh"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, []render.Difference{{Path: "modules/core", What: "target"}}},
		{"a target that is not there", func(t *testing.T, mod string) {
			if err := os.RemoveAll(mod); err != nil {
				t.Fatal(err)
			}
		}, []render.Difference{{Path: "modules/core", What: "target"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cache := t.TempDir()
			a := clone(t, filepath.Join(cache, "harness", "aaaa"))
			b := clone(t, filepath.Join(cache, "harness", "bbbb"))
			c.change(t, filepath.Join(b, "modules", "core"))
			want, have := t.TempDir(), t.TempDir()
			write(t, filepath.Join(want, "keep"), "")
			write(t, filepath.Join(have, "keep"), "")
			if err := os.MkdirAll(filepath.Join(want, "modules"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(have, "modules"), 0o755); err != nil {
				t.Fatal(err)
			}
			symlink(t, filepath.Join(a, "modules", "core"), filepath.Join(want, "modules", "core"))
			symlink(t, filepath.Join(b, "modules", "core"), filepath.Join(have, "modules", "core"))
			got, err := render.Diff(want, have, cache)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("differences = %+v, want %+v", got, c.want)
			}
		})
	}
	// The same two identical clones outside the cache compare by their targets.
	elsewhere := t.TempDir()
	a := clone(t, filepath.Join(elsewhere, "aaaa"))
	b := clone(t, filepath.Join(elsewhere, "bbbb"))
	want, have := t.TempDir(), t.TempDir()
	symlink(t, a, filepath.Join(want, "core"))
	symlink(t, b, filepath.Join(have, "core"))
	got, err := render.Diff(want, have, t.TempDir())
	if err != nil || !reflect.DeepEqual(got, []render.Difference{{Path: "core", What: "target"}}) {
		t.Errorf("links outside the cache: %+v (%v), want a target difference", got, err)
	}
}

// TestDiffComparesALinkThatLeavesTheCacheByItsTarget is a clone holding a link out of the
// cache: a target reached through that link, and the link itself inside a module, are
// compared by their targets, and nothing outside the cache is read, though it is the same
// on both sides and cannot be listed.
func TestDiffComparesALinkThatLeavesTheCacheByItsTarget(t *testing.T) {
	cache, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(outside, "mod", "AGENTS.md"), "# Elsewhere\n")
	if err := os.Chmod(filepath.Join(outside, "mod"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(outside, "mod"), 0o755) })
	for _, c := range []string{"aaaa", "bbbb"} {
		write(t, filepath.Join(cache, "harness", c, "core", "AGENTS.md"), "# Core\n")
		symlink(t, filepath.Join(outside, "mod"), filepath.Join(cache, "harness", c, "core", "out"))
		symlink(t, outside, filepath.Join(cache, "harness", c, "escape"))
	}
	want, have := t.TempDir(), t.TempDir()
	symlink(t, filepath.Join(cache, "harness", "aaaa", "escape", "mod"), filepath.Join(want, "through"))
	symlink(t, filepath.Join(cache, "harness", "bbbb", "escape", "mod"), filepath.Join(have, "through"))
	symlink(t, filepath.Join(cache, "harness", "aaaa", "core"), filepath.Join(want, "core"))
	symlink(t, filepath.Join(cache, "harness", "bbbb", "core"), filepath.Join(have, "core"))
	got, err := render.Diff(want, have, cache)
	if err != nil {
		t.Fatalf("Diff read outside the cache: %v", err)
	}
	if expected := []render.Difference{{Path: "through", What: "target"}}; !reflect.DeepEqual(got, expected) {
		t.Errorf("differences = %+v, want %+v", got, expected)
	}
}
