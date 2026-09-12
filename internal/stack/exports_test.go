package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSourceStringNamesAnExport writes a source naming an export the way the report
// and the messages show it: the repository, then the export.
func TestSourceStringNamesAnExport(t *testing.T) {
	for _, c := range []struct {
		source Source
		want   string
	}{
		{Source{Git: "https://git.example.com/acme/harness", Ref: "v1", Module: "core"}, "https://git.example.com/acme/harness#v1 module core"},
		{Source{Git: "https://git.example.com/acme/harness", Ref: "v1", Stack: "nextjs"}, "https://git.example.com/acme/harness#v1 stack nextjs"},
		{Source{Path: "../harness", Module: "core"}, "../harness module core"},
		{Source{Path: "../harness", Stack: "nextjs"}, "../harness stack nextjs"},
	} {
		if got := c.source.String(); got != c.want {
			t.Errorf("%+v = %q, want %q", c.source, got, c.want)
		}
	}
}

// TestLoadRefusesAnExportWrittenWrong is every mistake in a source naming an export: a
// module's source naming a stack, a source naming both, a git source naming a path and
// an export, an export without the repository, and a name that is not one path segment.
func TestLoadRefusesAnExportWrittenWrong(t *testing.T) {
	head := "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n"
	for _, c := range []struct{ module, want string }{
		{"  - name: core\n    source: {git: https://h, ref: v1, stack: nextjs}\n", "module core: source names stack nextjs; a module's source names a module, and a stack goes under extends"},
		{"  - name: core\n    source: {git: https://h, ref: v1, module: core, stack: nextjs}\n", "module core: source names both a module and a stack; it names one export"},
		{"  - name: core\n    source: {git: https://h, ref: v1, path: modules/core, module: core}\n", "module core: source.path and an export both name the directory; an export's directory is what the repository's qory.yaml says"},
		{"  - source: {module: core}\n", "modules[0]: source.path is required; with an export named, it is the repository exporting it"},
		{"  - name: core\n    source: {git: https://h, ref: v1, module: a/b}\n", `module core: source.module "a/b" is not one path segment`},
		{"  - name: core\n    source: {git: https://h, module: core}\n", "module core: source.ref is required with source.git"},
	} {
		path := write(t, head+c.module)
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.module, err, c.want)
		}
	}
	// extends names a stack; a module there is refused.
	path := filepath.Join(t.TempDir(), "qory.yaml")
	_, err := NewCompose(path, &Stack{APIVersion: APIVersion, Extends: Source{Git: "https://h", Ref: "v1", Module: "core"}, Modules: []Module{{Name: "app"}}})
	if want := "extends names module core; a checkout extends a stack, and a module goes under modules"; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("extends a module: err = %v, want %q", err, want)
	}
	// extends by an export alone is a compose document, and needs its repository.
	_, err = NewCompose(path, &Stack{APIVersion: APIVersion, Extends: Source{Stack: "nextjs"}, Modules: []Module{{Name: "app"}}})
	if want := "extends: source.path is required; with an export named, it is the repository exporting it"; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("extends without a repository: err = %v, want %q", err, want)
	}
}

// TestAModuleByNameReadsTheRepositoryModulesDir is a stack in a repository whose
// qory.yaml sets exports.dir: a module named without a source is read under that
// directory, and the same stack in a repository without the key reads modules/.
func TestAModuleByNameReadsTheRepositoryModulesDir(t *testing.T) {
	doc := "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n"
	path := write(t, doc)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.SourceOf(p.Modules[0]); got.Path != filepath.Join("modules", "core") || p.ModulesDir != "modules" {
		t.Errorf("without exports.dir: %q, modules dir %q", got.Path, p.ModulesDir)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "qory.yaml"), []byte("apiVersion: qory.dev/v1alpha1\nexports: {dir: harness}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err = Load(path); err != nil {
		t.Fatal(err)
	}
	if got := p.SourceOf(p.Modules[0]); got.Path != filepath.Join("harness", "modules", "core") || p.DirOf(p.Modules[0]) != p.Root {
		t.Errorf("with exports.dir: %q against %s", got.Path, p.DirOf(p.Modules[0]))
	}
	// A qory.yaml at the root that cannot be read is that error.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "qory.yaml"), []byte("apiVersion: qory.dev/v1alpha1\nexports: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(path); err == nil || !strings.Contains(err.Error(), "exports names no stacks and no modules") {
		t.Errorf("a broken exports section: %v", err)
	}
}
