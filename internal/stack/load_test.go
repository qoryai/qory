package stack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `apiVersion: qory.dev/v1alpha1
name: app
modules:
  - name: core
    source:
      path: modules/core
  - name: team
    source:
      path: modules/team
    variant: codex
    exclude:
      skills: [greet]
`

// write puts a stack document in a fresh temporary directory and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadReadsNameAndModules loads a valid stack and records the file it came from. A
// stack carries no target, so the loaded one is empty.
func TestLoadReadsNameAndModules(t *testing.T) {
	path := write(t, valid)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != APIVersion || p.Name != "app" {
		t.Fatalf("got %+v", p)
	}
	if len(p.Target.Runtimes) != 0 || p.Target.Model != "" {
		t.Fatalf("target %+v", p.Target)
	}
	if len(p.Modules) != 2 {
		t.Fatalf("%d modules, want 2", len(p.Modules))
	}
	core, team := p.Modules[0], p.Modules[1]
	if core.Name != "core" || core.Source.Path != "modules/core" || core.Variant != "" || !core.Exclude.Empty() {
		t.Fatalf("modules[0] %+v", core)
	}
	if team.Variant != "codex" || len(team.Exclude.Kinds["skills"]) != 1 || team.Exclude.Kinds["skills"][0] != "greet" {
		t.Fatalf("modules[1] %+v", team)
	}
	if team.Source.String() != "modules/team" {
		t.Fatalf("source %q", team.Source.String())
	}
	if p.File != path {
		t.Fatalf("file %q, want %q", p.File, path)
	}
	if p.Dir() != filepath.Dir(path) {
		t.Fatalf("dir %q, want %q", p.Dir(), filepath.Dir(path))
	}
}

// TestLoadReadsAModuleByNameAlone loads an entry that gives a name and no source, and one
// that gives a source and no name: the first reads modules/<name>, resolved under the root,
// the second keeps its source, resolved under the stack's directory, as SourceOf and
// DirOf tell.
func TestLoadReadsAModuleByNameAlone(t *testing.T) {
	path := write(t, "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n  - source: {path: ../shared/team}\n  - name: tools\n    source: {}\n")
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != filepath.Dir(path) {
		t.Fatalf("root %q, want %q", p.Root, filepath.Dir(path))
	}
	if got, want := p.SourceOf(p.Modules[0]), (Source{Path: filepath.Join("modules", "core")}); got != want || p.DirOf(p.Modules[0]) != p.Root {
		t.Fatalf("modules[0] source %+v under %s, want %+v under the root", got, p.DirOf(p.Modules[0]), want)
	}
	if got, want := p.SourceOf(p.Modules[1]), (Source{Path: "../shared/team"}); got != want || p.DirOf(p.Modules[1]) != p.Dir() {
		t.Fatalf("modules[1] source %+v under %s, want %+v under the stack's directory", got, p.DirOf(p.Modules[1]), want)
	}
	if got, want := p.SourceOf(p.Modules[2]), (Source{Path: filepath.Join("modules", "tools")}); got != want {
		t.Fatalf("modules[2] source %+v, want %+v", got, want)
	}
}

// TestSourceStringNamesAGitSourceTheWayDockerDoes is the text the report and the collision
// message show: the URL, the ref after #, and the path after : when there is one.
func TestSourceStringNamesAGitSourceTheWayDockerDoes(t *testing.T) {
	for _, c := range []struct {
		src  Source
		want string
	}{
		{Source{Path: "modules/core"}, "modules/core"},
		{Source{Git: "https://git.example.com/acme/harness", Ref: "v2.4.0"}, "https://git.example.com/acme/harness#v2.4.0"},
		{Source{Git: "git@git.example.com:acme/harness.git", Ref: "main", Path: "modules/nextjs"}, "git@git.example.com:acme/harness.git#main:modules/nextjs"},
	} {
		if got := c.src.String(); got != c.want {
			t.Errorf("%+v prints %q, want %q", c.src, got, c.want)
		}
	}
}

// TestLoadResolvesTheFileToAnAbsolutePath keeps [Stack.File] absolute for a stack named
// by a relative path, so a module's relative source resolves against the right directory.
func TestLoadResolvesTheFileToAnAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	p, err := Load(FileName)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p.File) {
		t.Fatalf("file %q is relative", p.File)
	}
	if filepath.Base(p.File) != FileName {
		t.Fatalf("file %q", p.File)
	}
}

// TestLoadReportsAMissingFile hands back the operating system's error, which a caller
// matches with os.ErrNotExist.
func TestLoadReportsAMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), FileName))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error %v, want one matching os.ErrNotExist", err)
	}
}

// TestLoadRefuses covers every document validation turns down. The message names the
// offending field, because the command prints it as it comes.
func TestLoadRefuses(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
		// loose marks a message the yaml decoder writes, which carries a line number.
		loose bool
	}{
		{
			"an unknown apiVersion",
			"apiVersion: qory.dev/v2\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			`apiVersion "qory.dev/v2" is not one this qory reads; versions: qory.dev/v1alpha1`,
			false,
		},
		{
			"a missing apiVersion",
			"modules:\n  - name: core\n    source:\n      path: modules/core\n",
			`apiVersion "" is not one this qory reads; versions: qory.dev/v1alpha1`,
			false,
		},
		{
			"an unknown field",
			"apiVersion: qory.dev/v1alpha1\nlayrs: []\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			`line 2: key "layrs" is not one qory-stack.yaml reads`,
			false,
		},
		{
			"an unknown field inside a module",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    sources:\n      path: modules/core\n",
			`line 4: key "sources" is not one qory-stack.yaml reads`,
			false,
		},
		{
			"a target",
			"apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			"target is not a stack's; a stack is delivered to be extended, and the checkout extending it sets target under harness, beside extends",
			false,
		},
		{
			"a target with a model alone",
			"apiVersion: qory.dev/v1alpha1\ntarget:\n  model: opus\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			"target is not a stack's; a stack is delivered to be extended, and the checkout extending it sets target under harness, beside extends",
			false,
		},
		{
			"an extends",
			"apiVersion: qory.dev/v1alpha1\nextends: {path: ../base}\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			"extends is not a stack's; the harness section of a checkout's qory.yaml extends a stack",
			false,
		},
		{
			"no modules",
			"apiVersion: qory.dev/v1alpha1\nmodules: []\n",
			"modules is empty; a stack lists at least one module",
			false,
		},
		{
			"a module with neither a name nor a source",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - link: harness\n",
			"modules[0]: a module has a name, a source, or both",
			false,
		},
		{
			"the second module with neither a name nor a source",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source:\n      path: modules/core\n  - link: harness\n",
			"modules[1]: a module has a name, a source, or both",
			false,
		},
		{
			"an empty source without a name",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - source: {}\n",
			"modules[0]: a module has a name, a source, or both",
			false,
		},
		{
			"two modules of one name",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source:\n      path: modules/core\n  - name: core\n    source:\n      path: modules/team\n",
			"module core is listed twice",
			false,
		},
		{
			"a ref without a git source on a module without a name",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - source: {ref: v1}\n",
			"modules[0]: source.ref needs source.git",
			false,
		},
		{
			"an exclude over an unknown kind",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source:\n      path: modules/core\n    exclude:\n      prompts: [greet]\n",
			`module core: exclude selects kind "prompts"; kinds: skills, agents, commands, output-styles, hooks, mcp, files; parts: instructions, settings, env`,
			false,
		},
		{
			"a module named with a path",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: ../escaped\n    source: {path: modules/core}\n",
			`modules[0]: name "../escaped" is not one path segment; a module name contains no slash, backslash, @ or leading dot`,
			false,
		},
		{
			"a module named with an at sign",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: b@a\n    source: {path: modules/core}\n",
			`modules[0]: name "b@a" is not one path segment; a module name contains no slash, backslash, @ or leading dot`,
			false,
		},
		{
			"two documents in one file",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\n---\nkind: Other\n",
			"contains more than one document; a stack is one",
			false,
		},
		{
			"a git source without a ref",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {git: https://git.example.com/acme/harness}\n",
			"module core: source.ref is required with source.git",
			false,
		},
		{
			"a ref without a git source",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core, ref: v1}\n",
			"module core: source.ref needs source.git",
			false,
		},
		{
			"a git source whose path leaves the repository",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {git: https://git.example.com/acme/harness, ref: v1, path: ../other}\n",
			`module core: source.path "../other" is not a directory inside the repository`,
			false,
		},
		{
			"broken yaml",
			"apiVersion: [\n",
			"did not find expected node content",
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := write(t, c.yaml)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("loaded %q without an error", c.yaml)
			}
			if !strings.HasPrefix(err.Error(), path+": ") {
				t.Fatalf("error %q does not start with the stack path", err)
			}
			if c.loose {
				if !strings.Contains(err.Error(), c.want) {
					t.Fatalf("error %q does not name %q", err, c.want)
				}
				return
			}
			if want := path + ": " + c.want; err.Error() != want {
				t.Fatalf("error:\n%s\nwant:\n%s", err, want)
			}
		})
	}
}

// TestOwnedByCurrentUser accepts a file this process just created.
func TestOwnedByCurrentUser(t *testing.T) {
	path := write(t, valid)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !OwnedByCurrentUser(info) {
		t.Fatal("a file this process wrote counts as another user's")
	}
}

// TestNewComposeAcceptsADocumentWithoutModules is a checkout's document that extends a
// stack and appends nothing: the compose takes it, with the base kept and no module of
// its own, while a stack with an empty modules list is refused as before, since a stack
// names a module.
func TestNewComposeAcceptsADocumentWithoutModules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qory.yaml")
	p, err := NewCompose(path, &Stack{APIVersion: APIVersion, Extends: Source{Path: "../base"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Extends.Path != "../base" || len(p.Modules) != 0 || p.File != path {
		t.Fatalf("got %+v", p)
	}
	_, err = Load(write(t, "apiVersion: qory.dev/v1alpha1\nmodules: []\n"))
	if err == nil || !strings.HasSuffix(err.Error(), "modules is empty; a stack lists at least one module") {
		t.Fatalf("a stack without modules: %v", err)
	}
}

// TestLoadReadsARetiredAPIVersion is a stack naming a retired apiVersion: it
// loads as the current format, and the spelling it declared is kept beside it so the
// compose can say the line wants rewriting. A stack naming the current version keeps
// nothing there.
func TestLoadReadsARetiredAPIVersion(t *testing.T) {
	p, err := Load(write(t, strings.Replace(valid, "qory.dev/v1alpha1", "qory.ai/v1alpha1", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != APIVersion || p.RetiredAPIVersion != "qory.ai/v1alpha1" || p.Name != "app" {
		t.Fatalf("got apiVersion %q, retired %q, name %q", p.APIVersion, p.RetiredAPIVersion, p.Name)
	}
	if p, err = Load(write(t, valid)); err != nil || p.RetiredAPIVersion != "" {
		t.Fatalf("the current version: retired %q, %v", p.RetiredAPIVersion, err)
	}
	path := write(t, "apiVersion: qory.ai/v1alpha1\nextends: {path: ../base}\nmodules:\n  - name: app\n")
	p, err = NewCompose(path, &Stack{APIVersion: "qory.ai/v1alpha1", Extends: Source{Path: "../base"}})
	if err != nil || p.APIVersion != APIVersion || p.RetiredAPIVersion != "qory.ai/v1alpha1" {
		t.Fatalf("a document: apiVersion %q, retired %q, %v", p.APIVersion, p.RetiredAPIVersion, err)
	}
}
