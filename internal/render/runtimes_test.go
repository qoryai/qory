package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// composeFixture composes the two-modules fixture for the given runtimes and returns the
// result together with a fresh git checkout and the home path inside it.
func composeFixture(t *testing.T, runtimes ...string) (*compose.Result, string, string) {
	t.Helper()
	hermetic(t)
	file, err := filepath.Abs("../../contracts/harness/v1/fixtures/two-modules/qory-stack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := stack.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Runtimes = runtimes
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return res, root, filepath.Join(root, ".qory", "harness")
}

// hermetic points git, and everything under test that runs git, at an empty home and
// configuration, so no test reads the developer's global config or ignore file.
func hermetic(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// lookup is the runtime of that name, which every test here knows exists.
func lookup(t *testing.T, name string) render.Runtime {
	t.Helper()
	p, err := render.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// linkTargets checks that every link of a runtime resolves to something in the home: the
// link itself for a file, every link inside it for a directory.
func linkTargets(t *testing.T, p render.Runtime, res *compose.Result, root, home string) {
	t.Helper()
	for _, l := range p.Links(res) {
		for _, path := range linkPaths(t, home, l) {
			full := filepath.Join(root, path[0])
			if _, err := os.Readlink(full); err != nil {
				t.Errorf("%s: %v", path[0], err)
				continue
			}
			if _, err := os.Stat(full); err != nil {
				t.Errorf("%s is a dangling link: %v", path[0], err)
			}
		}
	}
}

// TestBuildHoldsEveryRuntimeItIsGiven is the switch a person makes mid-branch: one checkout
// composed for two runtimes, both agents reading it.
func TestBuildHoldsEveryRuntimeItIsGiven(t *testing.T) {
	res, root, home := composeFixture(t, "claude", "codex")
	claude, codex := lookup(t, "claude"), lookup(t, "codex")
	if err := render.Build(res, home, claude, codex); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"claude", "codex"} {
		if _, err := os.Stat(filepath.Join(home, dir)); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
	for _, p := range []render.Runtime{claude, codex} {
		if _, err := render.LinkInto(p, res, root, home, false); err != nil {
			t.Fatal(err)
		}
		linkTargets(t, p, res, root, home)
	}
}

// TestBuildForOneRuntimeDropsTheOthers is why the compose command passes the runtimes
// already in the home alongside its target: a build replaces the whole tree.
func TestBuildForOneRuntimeDropsTheOthers(t *testing.T) {
	res, root, home := composeFixture(t, "claude", "codex")
	claude, codex := lookup(t, "claude"), lookup(t, "codex")
	if err := render.Build(res, home, claude, codex); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(claude, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	if err := render.Build(res, home, codex); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "claude")); err == nil {
		t.Error("the claude directory survived a build that did not name it")
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); err == nil {
		t.Error("the .claude/settings.json link still resolves; the test no longer proves anything")
	}
}

// TestComposedReportsTheRuntimesInTheHome is what the compose command reads to know which
// runtimes it must render besides its target.
func TestComposedReportsTheRuntimesInTheHome(t *testing.T) {
	res, _, home := composeFixture(t, "claude", "codex")
	if names := render.Composed(home); len(names) != 0 {
		t.Errorf("a home that does not exist yet reports %v", names)
	}
	if err := render.Build(res, home, lookup(t, "codex"), lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "somethingelse"), 0o755); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range render.Composed(home) {
		got = append(got, p.Name())
	}
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Errorf("Composed = %v, want claude and codex sorted", got)
	}
}

// TestBuildNeedsARuntime keeps a caller from replacing the home with an empty tree.
func TestBuildNeedsARuntime(t *testing.T) {
	res, _, home := composeFixture(t, "claude")
	if err := render.Build(res, home); err == nil {
		t.Fatal("Build with no runtime returned no error")
	}
}

// TestClaudePluginCopiesItsAgents is the roster a launch from outside the checkout
// delivers: Claude Code passes over a link in a plugin's agents directory, so the plugin
// holds the agents as files, byte for byte the module's, while its skills and the
// checkout's own agents stay links, and the launch line still names the plugin.
func TestClaudePluginCopiesItsAgents(t *testing.T) {
	res, _, home := composeFixture(t, "claude")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(home, "claude", "plugin")
	agent := filepath.Join(plugin, "agents", "reviewer.md")
	fi, err := os.Lstat(agent)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("%s is %v, want a regular file", agent, fi.Mode())
	}
	var src string
	for _, e := range res.Entries {
		if e.Kind == "agents" && e.Name == "reviewer" {
			src = e.Path
		}
	}
	want, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(agent); string(got) != string(want) {
		t.Errorf("the plugin's reviewer.md differs from %s", src)
	}
	for _, name := range []string{filepath.Join(plugin, "skills", "review"), filepath.Join(home, "claude", "agents", "reviewer.md")} {
		if fi, err := os.Lstat(name); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: %v, want a link", name, err)
		}
	}
	// The manifest leaves agents to discovery: a manifest naming the directory, even
	// as ./agents, keeps Claude Code from reading it.
	manifest, err := os.ReadFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), `"agents"`) {
		t.Errorf("the manifest names agents:\n%s", manifest)
	}
	l, err := render.LaunchFor(lookup(t, "claude"), home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Env) != 0 || len(l.Args) < 2 || l.Args[0] != "--plugin-dir" || l.Args[1] != plugin {
		t.Errorf("launch %q env %q, want --plugin-dir %s and no variable", l.Args, l.Env, plugin)
	}
}
