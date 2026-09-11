package exports_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/exports"
)

// write puts a qory.yaml with the given body under its apiVersion into a fresh
// repository root and returns the root.
func write(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, exports.FileName), []byte("apiVersion: qory.ai/v1alpha1\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// touch creates an empty file and the directories above it.
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadDefaultsTheDirectoriesToTheRoot is a section with names alone: the stacks are
// under stacks/ and the modules under modules/, and each export resolves to its directory.
func TestReadDefaultsTheDirectoriesToTheRoot(t *testing.T) {
	root := write(t, "exports:\n  stacks: [nextjs]\n  modules: [core, nextjs]\n")
	e, err := exports.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if e.Root != root || e.File != filepath.Join(root, "qory.yaml") || e.Dir.Stacks != "stacks" || e.Dir.Modules != "modules" {
		t.Fatalf("got %+v", e)
	}
	if dir, err := e.Stack("nextjs"); err != nil || dir != filepath.Join("stacks", "nextjs") {
		t.Errorf("stack nextjs = %q, %v", dir, err)
	}
	if dir, err := e.Module("core"); err != nil || dir != filepath.Join("modules", "core") {
		t.Errorf("module core = %q, %v", dir, err)
	}
}

// TestReadTakesDirAsOneDirectoryOrTwo is the two forms of dir: a string is the directory
// holding stacks/ and modules/, a map names each directory as it is.
func TestReadTakesDirAsOneDirectoryOrTwo(t *testing.T) {
	root := write(t, "exports:\n  dir: ./harness\n  stacks: [nextjs]\n")
	e, err := exports.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if e.Dir.Stacks != filepath.Join("harness", "stacks") || e.Dir.Modules != filepath.Join("harness", "modules") {
		t.Errorf("string dir: %+v", e.Dir)
	}
	root = write(t, "exports:\n  dir: {stacks: ./stacks, modules: lib/modules}\n  modules: [core]\n")
	if e, err = exports.Read(root); err != nil {
		t.Fatal(err)
	}
	if e.Dir.Stacks != "stacks" || e.Dir.Modules != filepath.Join("lib", "modules") {
		t.Errorf("map dir: %+v", e.Dir)
	}
	if dir, err := e.Module("core"); err != nil || dir != filepath.Join("lib", "modules", "core") {
		t.Errorf("module core = %q, %v", dir, err)
	}
	// dir alone sets where a module named without a source is read from.
	root = write(t, "exports:\n  dir: harness\n")
	if dir, err := exports.ModulesDir(root); err != nil || dir != filepath.Join("harness", "modules") {
		t.Errorf("modules dir = %q, %v", dir, err)
	}
}

// TestReadIsNilWithoutASection is a root with no qory.yaml, and one whose file has no
// exports section: neither is an error, and the modules directory is the default.
func TestReadIsNilWithoutASection(t *testing.T) {
	for _, root := range []string{t.TempDir(), write(t, "harness: {runtime: claude}\nworktree: {base: main}\n")} {
		if e, err := exports.Read(root); e != nil || err != nil {
			t.Errorf("%s: got %+v, %v", root, e, err)
		}
		if dir, err := exports.ModulesDir(root); err != nil || dir != "modules" {
			t.Errorf("%s: modules dir = %q, %v", root, dir, err)
		}
	}
}

// TestReadRefuses is every mistake in the section: a section naming nothing, a name
// that is not a segment or is listed twice, a directory outside the repository or
// missing from the map form, a key inside the section the reader does not know, and
// another apiVersion. A key elsewhere in the file is not the section's to refuse.
func TestReadRefuses(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"exports: {}\n", "exports names no stacks and no modules"},
		{"exports: {stacks: [a/b]}\n", `exports.stacks names "a/b", which is not one path segment`},
		{"exports: {modules: [core, core]}\n", "exports.modules names core twice"},
		{"exports: {dir: ../shared, modules: [core]}\n", `exports.dir stacks "../shared/stacks" is not a directory inside the repository`},
		{"exports: {dir: /srv/harness, modules: [core]}\n", `exports.dir stacks "/srv/harness/stacks" is not a directory inside the repository`},
		{"exports: {dir: {stacks: ./s}, modules: [core]}\n", "exports.dir names no modules directory; the map form names both"},
		{"exports: {dir: {stacks: ./s, module: ./m}}\n", `dir names "module"; a dir map names stacks and modules`},
		{"exports: {dir: \"\", modules: [core]}\n", "dir is empty; it is a directory relative to the repository root"},
		{"exports: {dir: [a, b], modules: [core]}\n", "dir is one directory holding stacks/ and modules/, or a map"},
		{"exports: {stack: [nextjs]}\n", `key "stack" is not one qory.yaml reads`},
	} {
		root := write(t, c.body)
		_, err := exports.Read(root)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), root) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "qory.yaml"), []byte("apiVersion: qory.ai/v2\nexports: {modules: [core]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Read(root); err == nil || !strings.Contains(err.Error(), `apiVersion "qory.ai/v2" is not one this qory reads`) {
		t.Errorf("another apiVersion: %v", err)
	}
	root = write(t, "exports: {modules: [core]}\nharnes: {runtime: claude}\n")
	if _, err := exports.Read(root); err != nil {
		t.Errorf("a key outside the section was refused: %v", err)
	}
}

// TestAnExportNotListedNamesTheOthers asks for a stack and a module the section does not
// list: the error says what is exported, or that nothing of that kind is.
func TestAnExportNotListedNamesTheOthers(t *testing.T) {
	root := write(t, "exports:\n  modules: [core, tools]\n")
	e, err := exports.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Module("nextjs"); err == nil || err.Error() != "exports no module named nextjs; modules: core, tools" {
		t.Errorf("module: %v", err)
	}
	if _, err := e.Stack("nextjs"); err == nil || err.Error() != "exports no stack named nextjs; it exports no stacks" {
		t.Errorf("stack: %v", err)
	}
}

// TestVerifyWantsEveryExportOnDisk is the publisher's check: an exported stack without
// its qory-stack.yaml, or a module without its qory-module.yaml, is named with the
// directory that lacks the file, and a section whose exports are all there passes.
func TestVerifyWantsEveryExportOnDisk(t *testing.T) {
	root := write(t, "exports:\n  dir: harness\n  stacks: [nextjs]\n  modules: [core]\n")
	e, err := exports.Read(root)
	if err != nil {
		t.Fatal(err)
	}
	want := root + "/qory.yaml: exports.stacks names nextjs, and harness/stacks/nextjs holds no qory-stack.yaml"
	if err := e.Verify(); err == nil || err.Error() != want {
		t.Errorf("no stack: %v\nwant %s", err, want)
	}
	touch(t, filepath.Join(root, "harness", "stacks", "nextjs", "qory-stack.yaml"))
	want = root + "/qory.yaml: exports.modules names core, and harness/modules/core holds no qory-module.yaml"
	if err := e.Verify(); err == nil || err.Error() != want {
		t.Errorf("no module: %v\nwant %s", err, want)
	}
	touch(t, filepath.Join(root, "harness", "modules", "core", "qory-module.yaml"))
	if err := e.Verify(); err != nil {
		t.Errorf("all there: %v", err)
	}
}
