package stack

import (
	"errors"
	"io/fs"
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
	isolate(t)
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
			"Set apiVersion to qory.dev/v1alpha1.\n  This qory reads qory.dev/v1alpha1, and apiVersion is \"qory.dev/v2\".\n  apiVersion is in {path}.",
			false,
		},
		{
			"a missing apiVersion",
			"modules:\n  - name: core\n    source:\n      path: modules/core\n",
			"Set apiVersion to qory.dev/v1alpha1.\n  This qory reads qory.dev/v1alpha1, and apiVersion is \"\".\n  apiVersion is in {path}.",
			false,
		},
		{
			"an unknown field",
			"apiVersion: qory.dev/v1alpha1\nlayrs: []\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			"Remove the key, or rename it to one qory-stack.yaml reads.\n  Key \"layrs\" is not one qory-stack.yaml reads.\n  It is at line 2 of {path}.",
			false,
		},
		{
			"an unknown field inside a module",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    sources:\n      path: modules/core\n",
			"Remove the key, or rename it to one qory-stack.yaml reads.\n  Key \"sources\" is not one qory-stack.yaml reads.\n  It is at line 4 of {path}.",
			false,
		},
		{
			"a target",
			"apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			"Remove target from the stack file.\n  A stack is delivered to be extended, and the checkout extending it sets target under harness, beside extends.\n  target is in {path}.",
			false,
		},
		{
			"a tier map",
			"apiVersion: qory.dev/v1alpha1\ntiers:\n  light: claude-haiku-5-5\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			"Remove tiers from the stack file.\n  A repository maps tiers under harness.tiers in its qory.yaml, and a machine maps them in the user's qory.yaml.\n  tiers is in {path}.",
			false,
		},
		{
			"a target with a model alone",
			"apiVersion: qory.dev/v1alpha1\ntarget:\n  model: opus\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			"Remove target from the stack file.\n  A stack is delivered to be extended, and the checkout extending it sets target under harness, beside extends.\n  target is in {path}.",
			false,
		},
		{
			"an extends",
			"apiVersion: qory.dev/v1alpha1\nextends: {path: ../base}\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			"Remove extends from the stack file.\n  Only the harness section of a checkout's qory.yaml extends a stack.\n  extends is in {path}.",
			false,
		},
		{
			"no modules",
			"apiVersion: qory.dev/v1alpha1\nmodules: []\n",
			"List at least one module under modules.\n  modules is empty, and a stack lists one or more modules.\n  modules is in {path}.",
			false,
		},
		{
			"a module with neither a name nor a source",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - link: harness\n",
			"Add a name or a source to modules[0].\n  A module has a name, a source, or both.\n  modules is in {path}.",
			false,
		},
		{
			"the second module with neither a name nor a source",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source:\n      path: modules/core\n  - link: harness\n",
			"Add a name or a source to modules[1].\n  A module has a name, a source, or both.\n  modules is in {path}.",
			false,
		},
		{
			"an empty source without a name",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - source: {}\n",
			"Add a name or a source to modules[0].\n  A module has a name, a source, or both.\n  modules is in {path}.",
			false,
		},
		{
			"two modules of one name",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source:\n      path: modules/core\n  - name: core\n    source:\n      path: modules/team\n",
			"List module core once in modules.\n  modules lists it twice.\n  modules is in {path}.",
			false,
		},
		{
			"a ref without a git source on a module without a name",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - source: {ref: v1}\n",
			"Set source.git of modules[0], or remove source.ref.\n  source.ref needs source.git.\n  source is in {path}.",
			false,
		},
		{
			"an exclude over an unknown kind",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source:\n      path: modules/core\n    exclude:\n      prompts: [greet]\n",
			"Rename \"prompts\" in the exclude of module core to a kind or a part.\n  Kinds: skills, agents, commands, output-styles, hooks, mcp, files.\n  Parts: instructions, settings, env.\n  The exclude is in {path}.",
			false,
		},
		{
			"a module named with a path",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: ../escaped\n    source: {path: modules/core}\n",
			"Rename the name of modules[0] to one path segment.\n  A module name contains no slash, backslash, @, or leading dot, and the name is \"../escaped\".\n  modules is in {path}.",
			false,
		},
		{
			"a module named with an at sign",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: b@a\n    source: {path: modules/core}\n",
			"Rename the name of modules[0] to one path segment.\n  A module name contains no slash, backslash, @, or leading dot, and the name is \"b@a\".\n  modules is in {path}.",
			false,
		},
		{
			"two documents in one file",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\n---\nkind: Other\n",
			"Keep one YAML document in the stack file.\n  A stack is one document, and the file contains more.\n  The file is {path}.",
			false,
		},
		{
			"a git source without a ref",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {git: https://git.example.com/acme/harness}\n",
			"Set source.ref of module core to a branch, tag, or commit.\n  source.ref is required with source.git.\n  source is in {path}.",
			false,
		},
		{
			"a ref without a git source",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core, ref: v1}\n",
			"Set source.git of module core, or remove source.ref.\n  source.ref needs source.git.\n  source is in {path}.",
			false,
		},
		{
			"a git source whose path leaves the repository",
			"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {git: https://git.example.com/acme/harness, ref: v1, path: ../other}\n",
			"Set source.path of module core to a folder inside its repository.\n  source.path is \"../other\", which is not a directory inside the repository: it is absolute, the repository's root, or leads out of it.\n  source is in {path}.",
			false,
		},
		{
			"broken yaml",
			"apiVersion: [\n",
			"Correct the YAML syntax of the file.\n  Line 1: did not find expected node content.",
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := write(t, c.yaml)
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(filepath.Dir(path)))
			_, err := Load(path)
			if err == nil {
				t.Fatalf("loaded %q without an error", c.yaml)
			}
			// A refusal written fix first has the path in its fix line, at {path} in want.
			fixFirst := strings.Contains(c.want, "{path}") || c.loose
			if !fixFirst && !strings.HasPrefix(err.Error(), path+": ") {
				t.Fatalf("error %q does not start with the stack path", err)
			}
			if c.loose {
				if !strings.Contains(err.Error(), c.want) {
					t.Fatalf("error %q does not name %q", err, c.want)
				}
				return
			}
			want := path + ": " + c.want
			if fixFirst {
				want = strings.ReplaceAll(c.want, "{path}", path)
			}
			if err.Error() != want {
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
	if err == nil || !strings.HasPrefix(err.Error(), "List at least one module under modules.\n  modules is empty") {
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

// TestASourcePathErrorHasItsModule is a source.path that leads out of its repository, in a
// module with a name, in one without, and in extends: the error has the module by its name,
// as modules[i] with no name, and none for extends. Its text starts with the fix.
func TestASourcePathErrorHasItsModule(t *testing.T) {
	const source = "{git: https://git.example/h.git, ref: main, path: ../other}"
	head := "apiVersion: qory.dev/v1alpha1\n"
	for _, c := range []struct{ name, text, module string }{
		{"a name", head + "modules:\n  - name: core\n    source: " + source + "\n", "core"},
		{"no name", head + "modules:\n  - name: one\n  - source: " + source + "\n", "modules[1]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "qory-stack.yaml")
			if err := os.WriteFile(path, []byte(c.text), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			var sp *SourcePathError
			if !errors.As(err, &sp) || sp.Module != c.module {
				t.Fatalf("%v, want the module %q", err, c.module)
			}
			want := "Set source.path of " + map[string]string{"core": "module core", "modules[1]": "modules[1]"}[c.module] + " to a folder inside its repository.\n  source.path is \"../other\", which is not a directory inside the repository"
			if !strings.HasPrefix(err.Error(), want) {
				t.Errorf("the text is %q, want it to start with %q", err, want)
			}
			if hidden := sp.Hidden(); strings.Contains(hidden, "../other") || strings.Contains(hidden, filepath.Dir(path)) || !strings.HasPrefix(hidden, "Set source.path of ") {
				t.Errorf("the hidden text is %q", hidden)
			}
		})
	}
}

// TestAnUnreadableStackFileStartsWithTheFixAndMatchesTheOSError is a qory-stack.yaml of
// mode 000: line 1 of the refusal is the fix, the path is in the text once, and the
// error still matches fs.ErrPermission.
func TestAnUnreadableStackFileStartsWithTheFixAndMatchesTheOSError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	isolate(t)
	path := write(t, "apiVersion: qory.dev/v1alpha1\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	_, err := Load(path)
	if err == nil {
		t.Fatal("no refusal")
	}
	if first := strings.SplitN(err.Error(), "\n", 2)[0]; first != "Check that you can read "+filepath.Base(path)+"." {
		t.Errorf("line 1 is %q", first)
	}
	if n := strings.Count(err.Error(), path); n != 1 {
		t.Errorf("the path is in the text %d times: %v", n, err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("%v does not match fs.ErrPermission", err)
	}
}
