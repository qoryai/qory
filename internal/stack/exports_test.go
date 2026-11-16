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
	head := "apiVersion: qory.dev/v1alpha1\nmodules:\n"
	for _, c := range []struct{ module, want string }{
		{"  - name: core\n    source: {git: https://h, ref: v1, stack: nextjs}\n", "Select a module in the source of module core, and move the stack to extends.\n  The source selects stack nextjs, and a module's source selects a module."},
		{"  - name: core\n    source: {git: https://h, ref: v1, module: core, stack: nextjs}\n", "Remove source.module or source.stack from module core.\n  A source selects one export."},
		{"  - name: core\n    source: {git: https://h, ref: v1, path: modules/core, module: core}\n", "Remove source.path or the export from module core.\n  Both select the directory, and an export's directory is what the repository's qory.yaml sets."},
		{"  - source: {module: core}\n", "Set source.path of modules[0] to the repository that exports it.\n  With an export selected, source.path is required."},
		{"  - name: core\n    source: {git: https://h, ref: v1, module: a/b}\n", "Set source.module of module core to one path segment.\n  The value is \"a/b\"."},
		{"  - name: core\n    source: {git: https://h, module: core}\n", "Set source.ref of module core to a branch, tag, or commit.\n  source.ref is required with source.git."},
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
	if want := "Move the module from extends to modules, and select a stack under extends.\n  extends selects module core, and a checkout extends a stack.\n  extends is in " + path + "."; err == nil || err.Error() != want {
		t.Errorf("extends a module: err = %v, want %q", err, want)
	}
	// extends by an export alone is a compose document, and needs its repository.
	_, err = NewCompose(path, &Stack{APIVersion: APIVersion, Extends: Source{Stack: "nextjs"}, Modules: []Module{{Name: "app"}}})
	if want := "Set source.path of extends to the repository that exports it.\n  With an export selected, source.path is required.\n  source is in " + path + "."; err == nil || err.Error() != want {
		t.Errorf("extends without a repository: err = %v, want %q", err, want)
	}
}

// TestAModuleByNameReadsTheRepositoryModulesDir is a stack in a repository whose
// qory.yaml sets exports.dir: a module named without a source is read under that
// directory, and the same stack in a repository without the key reads modules/.
func TestAModuleByNameReadsTheRepositoryModulesDir(t *testing.T) {
	doc := "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n"
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
	if _, err = Load(path); err == nil || !strings.Contains(err.Error(), "exports lists no stacks and no modules") {
		t.Errorf("a broken exports section: %v", err)
	}
}
