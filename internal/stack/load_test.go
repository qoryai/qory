package stack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `apiVersion: qory.ai/v1alpha1
name: app
target:
  runtime: claude
  model: opus
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

// TestLoadReadsTargetAndModules loads a valid stack and records the file it came from.
func TestLoadReadsTargetAndModules(t *testing.T) {
	path := write(t, valid)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != APIVersion || p.Name != "app" {
		t.Fatalf("got %+v", p)
	}
	if p.Target.Runtimes.String() != "claude" || p.Target.Model != "opus" {
		t.Fatalf("target %+v", p.Target)
	}
	if len(p.Modules) != 2 {
		t.Fatalf("%d modules, want 2", len(p.Modules))
	}
	core, team := p.Modules[0], p.Modules[1]
	if core.Name != "core" || core.Source.Path != "modules/core" || core.Variant != "" || core.Exclude != nil {
		t.Fatalf("modules[0] %+v", core)
	}
	if team.Variant != "codex" || len(team.Exclude["skills"]) != 1 || team.Exclude["skills"][0] != "greet" {
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
	path := write(t, "apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n  - source: {path: ../shared/team}\n  - name: tools\n    source: {}\n")
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
			"apiVersion: qory.ai/v2\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			`apiVersion "qory.ai/v2" is not one this qory reads; versions: qory.ai/v1alpha1`,
			false,
		},
		{
			"a missing apiVersion",
			"target:\n  runtime: claude\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			`apiVersion "" is not one this qory reads; versions: qory.ai/v1alpha1`,
			false,
		},
		{
			"an unknown field",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nlayrs: []\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			`line 4: key "layrs" is not one qory-stack.yaml reads`,
			false,
		},
		{
			"an unknown field inside a module",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    sources:\n      path: modules/core\n",
			`line 6: key "sources" is not one qory-stack.yaml reads`,
			false,
		},
		{
			"a target without a runtime",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  model: opus\nmodules:\n  - name: core\n    source:\n      path: modules/core\n",
			"target.runtime is required",
			false,
		},
		{
			"no modules",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules: []\n",
			"modules is empty; a stack names at least one module",
			false,
		},
		{
			"a module with neither a name nor a source",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - link: harness\n",
			"modules[0]: a module gives a name, a source, or both",
			false,
		},
		{
			"the second module with neither a name nor a source",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source:\n      path: modules/core\n  - link: harness\n",
			"modules[1]: a module gives a name, a source, or both",
			false,
		},
		{
			"an empty source without a name",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - source: {}\n",
			"modules[0]: a module gives a name, a source, or both",
			false,
		},
		{
			"two modules of one name",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source:\n      path: modules/core\n  - name: core\n    source:\n      path: modules/team\n",
			"module core is named twice",
			false,
		},
		{
			"a ref without a git source on a module without a name",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - source: {ref: v1}\n",
			"modules[0]: source.ref needs source.git",
			false,
		},
		{
			"an exclude over an unknown kind",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source:\n      path: modules/core\n    exclude:\n      prompts: [greet]\n",
			`module core: exclude names kind "prompts"; kinds: skills, agents, commands, output-styles, hooks, mcp, files`,
			false,
		},
		{
			"a module named with a path",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: ../escaped\n    source: {path: modules/core}\n",
			`modules[0]: name "../escaped" is not one path segment; a module name holds no slash, backslash, @ or leading dot`,
			false,
		},
		{
			"a module named with an at sign",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: b@a\n    source: {path: modules/core}\n",
			`modules[0]: name "b@a" is not one path segment; a module name holds no slash, backslash, @ or leading dot`,
			false,
		},
		{
			"two documents in one file",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {path: modules/core}\n---\nkind: Other\n",
			"holds more than one document; a stack is one",
			false,
		},
		{
			"a git source without a ref",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: https://git.example.com/acme/harness}\n",
			"module core: source.ref is required with source.git",
			false,
		},
		{
			"a ref without a git source",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {path: modules/core, ref: v1}\n",
			"module core: source.ref needs source.git",
			false,
		},
		{
			"a git source whose path leaves the repository",
			"apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: core\n    source: {git: https://git.example.com/acme/harness, ref: v1, path: ../other}\n",
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
