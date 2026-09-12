package source_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// publisherConfig is the qory.yaml of a repository exporting one stack and one module
// under harness/, with a second module it keeps to itself.
const publisherConfig = "apiVersion: qory.ai/v1alpha1\nexports:\n  dir: harness\n  stacks: [nextjs]\n  modules: [core, gone]\n"

// publisher makes a repository with an exports section: the stack under
// harness/stacks/nextjs, the modules under harness/modules, and a listed module, gone,
// with no directory. It returns the working tree.
func publisher(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "qory.yaml"), publisherConfig)
	write(t, filepath.Join(dir, "harness", "stacks", "nextjs", "qory-stack.yaml"), "apiVersion: qory.ai/v1alpha1\n")
	write(t, filepath.Join(dir, "harness", "modules", "core", "qory-module.yaml"), "apiVersion: qory.ai/v1alpha1\nname: core\n")
	write(t, filepath.Join(dir, "harness", "modules", "tools", "qory-module.yaml"), "apiVersion: qory.ai/v1alpha1\nname: tools\n")
	return dir
}

// publishedRemote is [publisher] committed and tagged v1, as a file:// URL, with the
// cache under a temporary HOME.
func publishedRemote(t *testing.T) string {
	t.Helper()
	hermetic(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", filepath.Join(os.Getenv("HOME"), ".cache"))
	dir := publisher(t)
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.name", "Test User")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "publish")
	run(t, dir, "tag", "v1")
	run(t, dir, "config", "uploadpack.allowReachableSHA1InWant", "true")
	return "file://" + dir
}

// TestResolveReadsAnExportFromAGitSource names a module and a stack the repository
// exports: each resolves to the directory the publisher's qory.yaml says, inside the
// clone, pinned by the commit.
func TestResolveReadsAnExportFromAGitSource(t *testing.T) {
	url := publishedRemote(t)
	got, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "v1", Module: "core"}, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got.Dir, filepath.Join("harness", "modules", "core")) || len(got.Pin) != 12 || got.Dirty {
		t.Errorf("module core: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(got.Dir, "qory-module.yaml")); err != nil {
		t.Errorf("the module's manifest: %v", err)
	}
	got, err = source.Resolve("/nowhere", stack.Source{Git: url, Ref: "v1", Stack: "nextjs"}, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got.Dir, filepath.Join("harness", "stacks", "nextjs")) {
		t.Errorf("stack nextjs: %+v", got)
	}
}

// TestResolveReadsAnExportFromAPath names an export of a repository on disk, by an
// absolute path and by one relative to the base directory: the working tree serves.
func TestResolveReadsAnExportFromAPath(t *testing.T) {
	hermetic(t)
	dir := publisher(t)
	got, err := source.Resolve("/nowhere", stack.Source{Path: dir, Module: "core"}, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != filepath.Join(dir, "harness", "modules", "core") || got.Pin != source.WorkingTree {
		t.Errorf("absolute: %+v", got)
	}
	got, err = source.Resolve(filepath.Dir(dir), stack.Source{Path: filepath.Base(dir), Stack: "nextjs"}, source.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != filepath.Join(dir, "harness", "stacks", "nextjs") {
		t.Errorf("relative: %+v", got)
	}
}

// TestResolveRefusesAnExportTheRepositoryDoesNotList asks a git source and a path
// source for a module the repository keeps to itself, and for a stack it does not have:
// the error names the repository and what it exports.
func TestResolveRefusesAnExportTheRepositoryDoesNotList(t *testing.T) {
	url := publishedRemote(t)
	_, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "v1", Module: "tools"}, source.Options{})
	if want := url + " at v1 exports no module named tools; modules: core, gone"; err == nil || err.Error() != want {
		t.Errorf("git: %v\nwant %s", err, want)
	}
	dir := strings.TrimPrefix(url, "file://")
	_, err = source.Resolve("/nowhere", stack.Source{Path: dir, Stack: "other"}, source.Options{})
	if want := dir + " exports no stack named other; stacks: nextjs"; err == nil || err.Error() != want {
		t.Errorf("path: %v\nwant %s", err, want)
	}
}

// TestResolveRefusesAnExportThatIsNotThere is a module the section lists with no
// directory behind it: the error names the directory the section points at.
func TestResolveRefusesAnExportThatIsNotThere(t *testing.T) {
	url := publishedRemote(t)
	_, err := source.Resolve("/nowhere", stack.Source{Git: url, Ref: "v1", Module: "gone"}, source.Options{})
	if want := url + " at v1 exports module gone at harness/modules/gone, which is not there"; err == nil || err.Error() != want {
		t.Errorf("git: %v\nwant %s", err, want)
	}
	dir := strings.TrimPrefix(url, "file://")
	_, err = source.Resolve("/nowhere", stack.Source{Path: dir, Module: "gone"}, source.Options{})
	if want := dir + " exports module gone at harness/modules/gone, which is not a directory there"; err == nil || err.Error() != want {
		t.Errorf("path: %v\nwant %s", err, want)
	}
}

// TestResolveNamesARepositoryExportingNothing names an export of a repository with no
// exports section: the error says so and points at path.
func TestResolveNamesARepositoryExportingNothing(t *testing.T) {
	hermetic(t)
	root := committedRepo(t)
	_, err := source.Resolve("/nowhere", stack.Source{Path: root, Module: "core"}, source.Options{})
	if want := root + " exports nothing: it has no qory.yaml with an exports section, so name its directories with path"; err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
}
