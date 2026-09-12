package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// TestExportsOfTheRootFileAreReadAndChecked is a repository whose qory.yaml exports a
// stack and a module: an export with no document behind it fails the load naming it,
// and once both are there the exports are read, printed as rows with the file as their
// origin, and read under extends as well.
func TestExportsOfTheRootFileAreReadAndChecked(t *testing.T) {
	hermetic(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "qory.yaml"), "exports:\n  dir: harness\n  stacks: [nextjs]\n  modules: [core]\n")
	_, err := config.Load(root, true)
	if want := filepath.Join(root, "qory.yaml") + ": exports.stacks names nextjs, and harness/stacks/nextjs holds no qory-stack.yaml"; err == nil || err.Error() != want {
		t.Fatalf("a missing stack: %v\nwant %s", err, want)
	}
	write(t, filepath.Join(root, "harness", "stacks", "nextjs", "qory-stack.yaml"), "")
	_, err = config.Load(root, true)
	if want := "exports.modules names core, and harness/modules/core holds no qory-module.yaml"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a missing module: %v\nwant %s", err, want)
	}
	write(t, filepath.Join(root, "harness", "modules", "core", "qory-module.yaml"), "name: core\n")
	for _, own := range []bool{true, false} {
		c, err := config.Load(root, own)
		if err != nil {
			t.Fatal(err)
		}
		if c.Exports == nil || c.Exports.Root != root || strings.Join(c.Exports.Stacks, ",") != "nextjs" || strings.Join(c.Exports.Modules, ",") != "core" {
			t.Fatalf("own %v: exports %+v", own, c.Exports)
		}
		rows := map[string][2]string{}
		for _, r := range c.Rows() {
			rows[r.Key] = [2]string{r.Value, r.Origin}
		}
		file := filepath.Join(root, "qory.yaml")
		for key, want := range map[string][2]string{
			"exports.dir.stacks":  {filepath.Join("harness", "stacks"), file},
			"exports.dir.modules": {filepath.Join("harness", "modules"), file},
			"exports.stacks":      {"nextjs", file},
			"exports.modules":     {"core", file},
		} {
			if rows[key] != want {
				t.Errorf("own %v: %s = %v, want %v", own, key, rows[key], want)
			}
		}
	}
}

// TestExportsElsewhereAreNotARepositorys is an exports section in the user's file: it
// is read, so a mistake in it is refused, and it is not the checkout's exports.
func TestExportsElsewhereAreNotARepositorys(t *testing.T) {
	home := hermetic(t)
	user := filepath.Join(home, ".config", "qory", "qory.yaml")
	write(t, user, "exports: {modules: [core]}\n")
	root := t.TempDir()
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Exports != nil {
		t.Errorf("the user's exports were taken as the checkout's: %+v", c.Exports)
	}
	for _, r := range c.Rows() {
		if strings.HasPrefix(r.Key, "exports.") {
			t.Errorf("row %s printed with no exports at the root", r.Key)
		}
	}
	write(t, user, "exports: {}\n")
	if _, err := config.Load(root, true); err == nil || !strings.Contains(err.Error(), user+": exports names no stacks and no modules") {
		t.Errorf("a mistake in the user's section: %v", err)
	}
}

// TestLoadRefusesAnExportsMistake is the section's own checks, each naming the file.
func TestLoadRefusesAnExportsMistake(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"exports: {}\n", "exports names no stacks and no modules"},
		{"exports: {stacks: [nextjs, nextjs]}\n", "exports.stacks names nextjs twice"},
		{"exports: {dir: ../shared, modules: [core]}\n", `exports.dir stacks "../shared/stacks" is not a directory inside the repository`},
		{"exports: {dir: {stacks: s}, modules: [core]}\n", "exports.dir names no modules directory; the map form names both"},
		{"exports: {module: [core]}\n", `line 2: key "module" is not one qory.yaml reads`},
	} {
		root := t.TempDir()
		write(t, filepath.Join(root, "qory.yaml"), c.body)
		_, err := config.Load(root, true)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), root) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}
