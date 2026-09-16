package render_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// read returns the file's text, failing the test when it is not there.
func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// regular fails the test unless path is a regular file, not a link.
func regular(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("%s is %v, want a regular file", path, fi.Mode())
	}
}

// linked fails the test unless path is a symlink.
func linked(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s: %v, want a link", path, err)
	}
}

// has fails the test for every want the text lacks.
func has(t *testing.T, what, text string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, text)
		}
	}
}

// lacks fails the test for every unwanted the text holds.
func lacks(t *testing.T, what, text string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			t.Errorf("%s holds %q:\n%s", what, u, text)
		}
	}
}

// TestReferencesResolveToThePathsNames is one compose read three ways. The plugin the
// claude launch loads registers every kind as harness:<name>, and the documents placed
// in it say so: a skill with a reference is a written copy carrying the plugin's names,
// a role resolved to the entry the stack binds, while a skill without one stays a link.
// The checkout's .claude and the shared root register the bare names, and their copies
// of the same skill carry those. The instructions follow the same split: CLAUDE.md,
// linked into the checkout, resolves bare and ends with nothing more; launch/CLAUDE.md,
// read at launch alone, resolves to the plugin's names and ends with the names the
// session registers, under the prefix the plugin's own manifest declares. A Codex home
// registers bare names and its AGENTS.md says that.
func TestReferencesResolveToThePathsNames(t *testing.T) {
	res, root, home := composeFixtureNamed(t, "references-resolve", "claude", "codex")
	if err := render.Build(res, home, lookup(t, "claude"), lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(home, "claude", "plugin")
	var manifest struct{ Name string }
	if err := json.Unmarshal([]byte(read(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"))), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name == "" {
		t.Fatal("the plugin manifest names nothing")
	}
	prefix := manifest.Name + ":"

	// The plugin: written copies where a reference stood, links elsewhere, and the
	// prefix from the manifest on every resolved name.
	deploy := filepath.Join(plugin, "skills", "deploy", "SKILL.md")
	regular(t, deploy)
	has(t, "plugin SKILL.md", read(t, deploy), "Dispatch "+prefix+"rails-coder for the code.", "Run "+prefix+"test, then dispatch "+prefix+"reviewer.")
	lacks(t, "plugin SKILL.md", read(t, deploy), "${qory:")
	checklist := filepath.Join(plugin, "skills", "deploy", "checklist.md")
	regular(t, checklist)
	has(t, "plugin checklist.md", read(t, checklist), prefix+"ship is run once")
	linked(t, filepath.Join(plugin, "skills", "test"))
	regular(t, filepath.Join(plugin, "agents", "rails-coder.md"))

	// The checkout's directory and the shared root: the same skill, bare names.
	for _, path := range []string{filepath.Join(home, "claude", "skills", "deploy", "SKILL.md"), filepath.Join(home, "skills", "deploy", "SKILL.md")} {
		regular(t, path)
		has(t, path, read(t, path), "Dispatch rails-coder for the code.", "Run test, then dispatch reviewer.")
		lacks(t, path, read(t, path), prefix, "${qory:")
	}
	linked(t, filepath.Join(home, "claude", "skills", "test"))
	linked(t, filepath.Join(home, "skills", "test"))

	// The instructions, twice.
	checkout := read(t, filepath.Join(home, "claude", "CLAUDE.md"))
	if want := "Product code goes to rails-coder; a change is checked by test.\n"; checkout != want {
		t.Errorf("CLAUDE.md:\n%s\nwant:\n%s", checkout, want)
	}
	launch := read(t, filepath.Join(home, "claude", "launch", "CLAUDE.md"))
	has(t, "launch/CLAUDE.md", launch,
		"Product code goes to "+prefix+"rails-coder; a change is checked by "+prefix+"test.\n\n# Names in this session\n",
		"- agents: "+prefix+"rails-coder, "+prefix+"reviewer\n",
		"- commands: "+prefix+"ship\n",
		"- skills: "+prefix+"deploy, "+prefix+"test\n",
		"- roles: agents/coder is "+prefix+"rails-coder\n")
	lacks(t, "launch/CLAUDE.md", launch, "${qory:")
	shared := read(t, filepath.Join(home, "AGENTS.md"))
	if shared != checkout {
		t.Errorf("the root AGENTS.md differs from the checkout's CLAUDE.md:\n%s", shared)
	}

	// Codex: bare names, commands skipped, and the file says the names are the modules'.
	codex := read(t, filepath.Join(home, "codex", "AGENTS.md"))
	has(t, "codex AGENTS.md", codex,
		"Product code goes to rails-coder; a change is checked by test.\n\n# Names in this session\n",
		"under the names the modules wrote",
		"- agents: rails-coder, reviewer\n",
		"- skills: deploy, test\n",
		"- roles: agents/coder is rails-coder\n")
	lacks(t, "codex AGENTS.md", codex, "- commands:", prefix)

	// The checkout gets the plugin's names nowhere: .claude links neither directory.
	if _, err := render.LinkInto(lookup(t, "claude"), res, root, home, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin", "launch"} {
		if _, err := os.Lstat(filepath.Join(root, ".claude", name)); err == nil {
			t.Errorf(".claude/%s is linked into the checkout", name)
		}
	}
	linked(t, filepath.Join(root, ".claude", "CLAUDE.md"))
}

// TestAddressingSaysNothingWithoutARoster is a harness of instructions alone: no agent,
// skill or command to name, so the launch instructions are the composed instructions
// and nothing more, on both paths.
func TestAddressingSaysNothingWithoutARoster(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("qory-stack.yaml", "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n    source: {path: modules/core}\n")
	write("modules/core/qory-module.yaml", "apiVersion: qory.dev/v1alpha1\nname: core\n")
	write("modules/core/AGENTS.md", "Rules.\n")
	p, err := stack.Load(filepath.Join(dir, "qory-stack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p.Target = stack.Target{Runtimes: stack.Runtimes{"claude", "codex"}}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := render.Build(res, home, lookup(t, "claude"), lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude/launch/CLAUDE.md", "codex/AGENTS.md", "claude/CLAUDE.md"} {
		if got := read(t, filepath.Join(home, name)); got != "Rules.\n" {
			t.Errorf("%s:\n%s\nwant the instructions alone", name, got)
		}
	}
	if s := render.Addressing(res, render.PluginAddress, nil); s != "" {
		t.Errorf("Addressing on an empty roster: %q", s)
	}
}

// TestAddressOfIsBareUnlessTheLauncherSaysOtherwise pins which launch paths rename: the
// claude plugin does, and every other runtime's launch path registers the name as
// written, since none has measured otherwise.
func TestAddressOfIsBareUnlessTheLauncherSaysOtherwise(t *testing.T) {
	for _, name := range render.Names() {
		got := render.AddressOf(lookup(t, name))("agents", "reviewer")
		want := "reviewer"
		if name == "claude" {
			want = render.PluginName + ":reviewer"
		}
		if got != want {
			t.Errorf("%s addresses agents/reviewer as %q, want %q", name, got, want)
		}
	}
}
