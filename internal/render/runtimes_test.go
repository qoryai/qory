package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/render"
)

// composeFixture composes the two-layers fixture for the given runtimes and returns the
// result together with a fresh git checkout and the home path inside it.
func composeFixture(t *testing.T, runtimes ...string) (*compose.Result, string, string) {
	t.Helper()
	file, err := filepath.Abs("../../contracts/harness/v1/fixtures/two-layers/harness-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load(file)
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
