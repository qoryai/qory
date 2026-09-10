package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
name: app
target:
  runtime: claude
  model: opus
layers:
  - name: core
    source:
      path: layers/core
  - name: team
    source:
      path: layers/team
    variant: codex
    exclude:
      skills: [greet]
`

// write puts a profile document in a fresh temporary directory and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadReadsTargetAndLayers loads a valid profile and records the file it came from.
func TestLoadReadsTargetAndLayers(t *testing.T) {
	path := write(t, valid)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != APIVersion || p.Kind != Kind || p.Name != "app" {
		t.Fatalf("got %+v", p)
	}
	if p.Target.Runtimes.String() != "claude" || p.Target.Model != "opus" {
		t.Fatalf("target %+v", p.Target)
	}
	if len(p.Layers) != 2 {
		t.Fatalf("%d layers, want 2", len(p.Layers))
	}
	core, team := p.Layers[0], p.Layers[1]
	if core.Name != "core" || core.Source.Path != "layers/core" || core.Variant != "" || core.Exclude != nil {
		t.Fatalf("layers[0] %+v", core)
	}
	if team.Variant != "codex" || len(team.Exclude["skills"]) != 1 || team.Exclude["skills"][0] != "greet" {
		t.Fatalf("layers[1] %+v", team)
	}
	if team.Source.String() != "layers/team" {
		t.Fatalf("source %q", team.Source.String())
	}
	if p.File != path {
		t.Fatalf("file %q, want %q", p.File, path)
	}
	if p.Dir() != filepath.Dir(path) {
		t.Fatalf("dir %q, want %q", p.Dir(), filepath.Dir(path))
	}
}

// TestSourceStringNamesAGitSourceTheWayDockerDoes is the text the report and the collision
// message show: the URL, the ref after #, and the path after : when there is one.
func TestSourceStringNamesAGitSourceTheWayDockerDoes(t *testing.T) {
	for _, c := range []struct {
		src  Source
		want string
	}{
		{Source{Path: "layers/core"}, "layers/core"},
		{Source{Git: "https://git.example.com/acme/harness", Ref: "v2.4.0"}, "https://git.example.com/acme/harness#v2.4.0"},
		{Source{Git: "git@git.example.com:acme/harness.git", Ref: "main", Path: "layers/nextjs"}, "git@git.example.com:acme/harness.git#main:layers/nextjs"},
	} {
		if got := c.src.String(); got != c.want {
			t.Errorf("%+v prints %q, want %q", c.src, got, c.want)
		}
	}
}

// TestLoadResolvesTheFileToAnAbsolutePath keeps [Profile.File] absolute for a profile named
// by a relative path, so a layer's relative source resolves against the right directory.
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
			"apiVersion: qory.ai/v2\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source:\n      path: layers/core\n",
			`apiVersion "qory.ai/v2" is not one this qory reads; versions: qory.ai/v1alpha1`,
			false,
		},
		{
			"a missing apiVersion",
			"kind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source:\n      path: layers/core\n",
			`apiVersion "" is not one this qory reads; versions: qory.ai/v1alpha1`,
			false,
		},
		{
			"a kind that is not HarnessProfile",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessLayer\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source:\n      path: layers/core\n",
			`kind "HarnessLayer" is not HarnessProfile`,
			false,
		},
		{
			"an unknown field",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayrs: []\nlayers:\n  - name: core\n    source:\n      path: layers/core\n",
			"field layrs not found in type profile.Profile",
			true,
		},
		{
			"an unknown field inside a layer",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    sources:\n      path: layers/core\n",
			"field sources not found in type profile.Layer",
			true,
		},
		{
			"a target without a runtime",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  model: opus\nlayers:\n  - name: core\n    source:\n      path: layers/core\n",
			"target.runtime is required",
			false,
		},
		{
			"no layers",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers: []\n",
			"layers is empty; a profile names at least one layer",
			false,
		},
		{
			"a layer without a name",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - source:\n      path: layers/core\n",
			"layers[0]: name is required",
			false,
		},
		{
			"the second layer without a name",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source:\n      path: layers/core\n  - source:\n      path: layers/team\n",
			"layers[1]: name is required",
			false,
		},
		{
			"two layers of one name",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source:\n      path: layers/core\n  - name: core\n    source:\n      path: layers/team\n",
			"layer core is named twice",
			false,
		},
		{
			"a layer without a source path",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source: {}\n",
			"layer core: source.path is required",
			false,
		},
		{
			"an exclude over an unknown kind",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source:\n      path: layers/core\n    exclude:\n      prompts: [greet]\n",
			`layer core: exclude names kind "prompts"; kinds: skills, agents, commands, output-styles, hooks, mcp`,
			false,
		},
		{
			"a layer named with a path",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: ../escaped\n    source: {path: layers/core}\n",
			`layers[0]: name "../escaped" is not one path segment; a layer name holds no slash, backslash, @ or leading dot`,
			false,
		},
		{
			"a layer named with an at sign",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: b@a\n    source: {path: layers/core}\n",
			`layers[0]: name "b@a" is not one path segment; a layer name holds no slash, backslash, @ or leading dot`,
			false,
		},
		{
			"two documents in one file",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source: {path: layers/core}\n---\nkind: Other\n",
			"holds more than one document; a profile is one",
			false,
		},
		{
			"a git source without a ref",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source: {git: https://git.example.com/acme/harness}\n",
			"layer core: source.ref is required with source.git",
			false,
		},
		{
			"a ref without a git source",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source: {path: layers/core, ref: v1}\n",
			"layer core: source.ref needs source.git",
			false,
		},
		{
			"a git source whose path leaves the repository",
			"apiVersion: qory.ai/v1alpha1\nkind: HarnessProfile\ntarget:\n  runtime: claude\nlayers:\n  - name: core\n    source: {git: https://git.example.com/acme/harness, ref: v1, path: ../other}\n",
			`layer core: source.path "../other" is not a directory inside the repository`,
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
				t.Fatalf("error %q does not start with the profile path", err)
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

// TestDiscoverPrefersTheCheckoutRoot returns the checkout's own profile even when an
// ancestor directory holds one.
func TestDiscoverPrefersTheCheckoutRoot(t *testing.T) {
	base := t.TempDir()
	checkout := filepath.Join(base, "app")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{base, checkout} {
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(valid), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Discover(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(checkout, FileName); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// TestDiscoverTakesTheNearestAncestor walks up one directory at a time.
func TestDiscoverTakesTheNearestAncestor(t *testing.T) {
	base := t.TempDir()
	checkout := filepath.Join(base, "code", "team", "app")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{base, filepath.Join(base, "code")} {
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(valid), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Discover(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "code", FileName); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// TestDiscoverResolvesARelativeCheckout names the checkout in the error when no ancestor
// holds a profile.
func TestDiscoverResolvesARelativeCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	_, err := Discover(".")
	if err == nil {
		t.Fatal("found a profile where there is none")
	}
	if want := "no " + FileName + " in "; err.Error()[:len(want)] != want {
		t.Fatalf("error %q", err)
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
