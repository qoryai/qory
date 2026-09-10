package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/render"

	_ "github.com/qoryai/qory/internal/render/amp"
	_ "github.com/qoryai/qory/internal/render/any"
	_ "github.com/qoryai/qory/internal/render/claude"
	_ "github.com/qoryai/qory/internal/render/codex"
	_ "github.com/qoryai/qory/internal/render/copilot"
	_ "github.com/qoryai/qory/internal/render/cursor"
	_ "github.com/qoryai/qory/internal/render/gemini"
	_ "github.com/qoryai/qory/internal/render/goose"
	_ "github.com/qoryai/qory/internal/render/opencode"
)

// TestRuntimes renders the two-layers fixture for every runtime, links it into a fresh
// git checkout, checks the files each CLI reads, and unlinks again.
func TestRuntimes(t *testing.T) {
	file, err := filepath.Abs("../../contracts/harness/v1/fixtures/two-layers/harness-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	expect := map[string][]string{
		"claude":   {"claude/skills/review/SKILL.md", "claude/agents/reviewer.md", "claude/commands/ship.md", "claude/output-styles/terse.md", "claude/hooks/guard.sh", "claude/settings.json", "claude/CLAUDE.md"},
		"codex":    {"codex/config.toml", "codex/agents/reviewer.toml"},
		"gemini":   {"gemini/settings.json", "gemini/skills/review/SKILL.md", "gemini/agents/reviewer.md", "gemini/commands/ship.toml"},
		"opencode": {"opencode/opencode.json", "opencode/agents/reviewer.md", "opencode/commands/ship.md"},
		"cursor":   {"cursor/agents/reviewer.md", "cursor/hooks/guard.sh"},
		"copilot":  {"copilot/agents/reviewer.agent.md"},
		"amp":      {},
		"goose":    {"goose/agents/reviewer.md"},
		"any":      {},
	}
	contains := map[string][2]string{
		"claude":   {"claude/settings.json", `"model": "opus"`},
		"codex":    {"codex/config.toml", `model = "opus"`},
		"gemini":   {"gemini/settings.json", `"name": "opus"`},
		"opencode": {"opencode/opencode.json", `"model": "opus"`},
		"cursor":   {"cursor/agents/reviewer.md", "name: reviewer"},
		"copilot":  {"copilot/agents/reviewer.agent.md", "name: reviewer"},
		"amp":      {"AGENTS.md", "# Core"},
		"goose":    {"goose/agents/reviewer.md", "name: reviewer"},
		"any":      {"AGENTS.md", "# Core"},
	}
	for _, name := range render.Names() {
		t.Run(name, func(t *testing.T) {
			p, err := profile.Load(file)
			if err != nil {
				t.Fatal(err)
			}
			p.Target.Runtimes = profile.Runtimes{name}
			res, err := compose.Compose(p)
			if err != nil {
				t.Fatal(err)
			}
			prov, err := render.Lookup(name)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			home := filepath.Join(root, ".qory", "harness")
			if err := render.Build(res, home, prov); err != nil {
				t.Fatal(err)
			}
			for _, path := range append([]string{"AGENTS.md", "skills/review/SKILL.md", "hooks/guard.sh"}, expect[name]...) {
				if _, err := os.Stat(filepath.Join(home, path)); err != nil {
					t.Errorf("%s: %v", path, err)
				}
			}
			data, _ := os.ReadFile(filepath.Join(home, contains[name][0]))
			if !strings.Contains(string(data), contains[name][1]) {
				t.Errorf("%s lacks %s:\n%s", contains[name][0], contains[name][1], data)
			}
			if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, out)
			}
			if skipped, err := render.LinkInto(prov, res, root, home); err != nil || len(skipped) != 0 {
				t.Fatalf("link: skipped=%v err=%v", skipped, err)
			}
			exclude, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
			if !strings.Contains(string(exclude), "/.qory\n") {
				t.Errorf("exclude file lacks /.qory")
			}
			for _, l := range prov.Links(res) {
				path := filepath.Join(root, l.Checkout)
				target, err := os.Readlink(path)
				if err != nil || filepath.IsAbs(target) {
					t.Errorf("%s links to %q (%v), want a relative link", l.Checkout, target, err)
				}
				resolved, err := filepath.EvalSymlinks(path)
				if err != nil {
					t.Errorf("%s links to a path that does not exist: %v", l.Checkout, err)
				} else if want, _ := filepath.EvalSymlinks(filepath.Join(home, l.Home)); resolved != want {
					t.Errorf("%s resolves to %s, want %s", l.Checkout, resolved, want)
				}
				if !strings.Contains(string(exclude), "/"+l.Checkout+"\n") {
					t.Errorf("exclude file lacks /%s", l.Checkout)
				}
			}
			if _, err := render.LinkInto(prov, res, root, home); err != nil {
				t.Fatalf("second link: %v", err)
			}
			removed, err := render.Unlink(prov, root)
			if err != nil || len(removed) != len(prov.Links(res)) {
				t.Fatalf("unlink: removed=%v err=%v", removed, err)
			}
		})
	}
}

// TestForeignDirectory keeps a real .claude directory: qory does not replace it.
func TestForeignDirectory(t *testing.T) {
	prov, err := render.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(prov, nil, root, filepath.Join(root, ".qory", "harness")); err == nil {
		t.Error("replaced a real .claude directory")
	}
}

// TestSoftLink keeps a repository's own AGENTS.md and reports it instead of failing.
func TestSoftLink(t *testing.T) {
	file, _ := filepath.Abs("../../contracts/harness/v1/fixtures/two-layers/harness-compose.yaml")
	p, err := profile.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Runtimes = profile.Runtimes{"opencode"}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	prov, _ := render.Lookup("opencode")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, ".qory", "harness")
	if err := render.Build(res, home, prov); err != nil {
		t.Fatal(err)
	}
	skipped, err := render.LinkInto(prov, res, root, home)
	if err != nil || len(skipped) != 1 || skipped[0] != "AGENTS.md" {
		t.Fatalf("skipped=%v err=%v", skipped, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if string(data) != "ours\n" {
		t.Errorf("AGENTS.md was replaced: %q", data)
	}
	removed, err := render.Unlink(prov, root)
	if err != nil || len(removed) != 3 {
		t.Fatalf("unlink: removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents")); err == nil {
		t.Error(".agents left behind empty")
	}
	data, _ = os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if string(data) != "ours\n" {
		t.Errorf("AGENTS.md was removed: %q", data)
	}
}
