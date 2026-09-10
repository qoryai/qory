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

// TestRuntimes renders the two-modules fixture for every runtime, links it into a fresh
// git checkout, checks the files each CLI reads, and unlinks again.
func TestRuntimes(t *testing.T) {
	file, err := filepath.Abs("../../contracts/harness/v1/fixtures/two-modules/qory-stack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	expect := map[string][]string{
		"claude":   {"claude/skills/review/SKILL.md", "claude/agents/reviewer.md", "claude/commands/ship.md", "claude/output-styles/terse.md", "claude/hooks/guard.sh", "claude/settings.json", "claude/CLAUDE.md", "claude/mcp.json"},
		"codex":    {"codex/config.toml", "codex/agents/reviewer.toml"},
		"gemini":   {"gemini/settings.json", "gemini/skills/review/SKILL.md", "gemini/agents/reviewer.md", "gemini/commands/ship.toml"},
		"opencode": {"opencode/opencode.json", "opencode/agents/reviewer.md", "opencode/commands/ship.md"},
		"cursor":   {"cursor/agents/reviewer.md", "cursor/hooks/guard.sh", "cursor/mcp.json"},
		"copilot":  {"copilot/agents/reviewer.agent.md"},
		"amp":      {"amp/settings.json"},
		"goose":    {"goose/agents/reviewer.md"},
		"any":      {},
	}
	// contains is what one file of each runtime carries: the model where the runtime has a
	// place for it, and the MCP server where it has one, with the home substituted.
	contains := map[string][][2]string{
		"claude":   {{"claude/settings.json", `"model": "opus"`}, {"claude/mcp.json", `"mcpServers"`}, {"claude/mcp.json", `/modules/core/scripts/db.py`}},
		"codex":    {{"codex/config.toml", `model = "opus"`}, {"codex/config.toml", `[mcp_servers.db]`}, {"codex/config.toml", `/modules/core/scripts/db.py`}},
		"gemini":   {{"gemini/settings.json", `"name": "opus"`}, {"gemini/settings.json", `"mcpServers"`}},
		"opencode": {{"opencode/opencode.json", `"model": "opus"`}, {"opencode/opencode.json", `"type": "local"`}, {"opencode/opencode.json", `"environment"`}},
		"cursor":   {{"cursor/agents/reviewer.md", "name: reviewer"}, {"cursor/mcp.json", `"mcpServers"`}},
		"copilot":  {{"copilot/agents/reviewer.agent.md", "name: reviewer"}},
		"amp":      {{"AGENTS.md", "# Core"}, {"amp/settings.json", `"amp.mcpServers"`}},
		"goose":    {{"goose/agents/reviewer.md", "name: reviewer"}},
		"any":      {{"AGENTS.md", "# Core"}},
	}
	for _, name := range render.Names() {
		if _, ok := expect[name]; !ok {
			t.Fatalf("runtime %s has no expectations here", name)
		}
		t.Run(name, func(t *testing.T) {
			hermetic(t)
			p, err := stack.Load(file)
			if err != nil {
				t.Fatal(err)
			}
			p.Target.Runtimes = stack.Runtimes{name}
			res, err := compose.Compose(p)
			if err != nil {
				t.Fatal(err)
			}
			rt, err := render.Lookup(name)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			home := filepath.Join(root, ".qory", "harness")
			if err := render.Build(res, home, rt); err != nil {
				t.Fatal(err)
			}
			for _, path := range append([]string{"AGENTS.md", "skills/review/SKILL.md", "hooks/guard.sh", "modules/core/scripts/db.py"}, expect[name]...) {
				if _, err := os.Stat(filepath.Join(home, path)); err != nil {
					t.Errorf("%s: %v", path, err)
				}
			}
			for _, c := range contains[name] {
				data, _ := os.ReadFile(filepath.Join(home, c[0]))
				if !strings.Contains(string(data), c[1]) {
					t.Errorf("%s lacks %s:\n%s", c[0], c[1], data)
				}
				if strings.Contains(string(data), "QORY_HARNESS_HOME/") {
					t.Errorf("%s still names $QORY_HARNESS_HOME:\n%s", c[0], data)
				}
			}
			if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, out)
			}
			if linked, err := render.LinkInto(rt, res, root, home, false); err != nil || len(linked.Skipped) != 0 || len(linked.Replaced) != 0 {
				t.Fatalf("link: %+v err=%v", linked, err)
			}
			exclude, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
			if !strings.Contains(string(exclude), "/.qory\n") {
				t.Errorf("exclude file lacks /.qory")
			}
			for _, l := range rt.Links(res) {
				for _, path := range linkPaths(t, home, l) {
					checkLink(t, root, home, path[0], path[1], string(exclude))
				}
			}
			if _, err := render.LinkInto(rt, res, root, home, false); err != nil {
				t.Fatalf("second link: %v", err)
			}
			// Unlink names every declared link that had a symlink behind it; a directory
			// link the home left empty, such as copilot's .github/hooks, wrote none.
			written := 0
			for _, l := range rt.Links(res) {
				if len(linkPaths(t, home, l)) > 0 {
					written++
				}
			}
			removed, err := render.Unlink(rt, root)
			if err != nil || len(removed) != written {
				t.Fatalf("unlink: removed=%v err=%v, want %d", removed, err, written)
			}
		})
	}
}

// linkPaths lists the symlinks one declared link stands for, as checkout path and home
// path: the link itself for a file, one per child for a directory.
func linkPaths(t *testing.T, home string, l render.Link) [][2]string {
	t.Helper()
	src := filepath.Join(home, l.Home)
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		return [][2]string{{l.Checkout, l.Home}}
	}
	children, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	var out [][2]string
	for _, c := range children {
		out = append(out, [2]string{l.Checkout + "/" + c.Name(), l.Home + "/" + c.Name()})
	}
	return out
}

// checkLink checks one symlink in the checkout: relative, resolving to the home path,
// and listed in the exclude file.
func checkLink(t *testing.T, root, home, checkoutPath, homePath, exclude string) {
	t.Helper()
	path := filepath.Join(root, checkoutPath)
	target, err := os.Readlink(path)
	if err != nil || filepath.IsAbs(target) {
		t.Errorf("%s links to %q (%v), want a relative link", checkoutPath, target, err)
		return
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Errorf("%s links to a path that does not exist: %v", checkoutPath, err)
	} else if want, _ := filepath.EvalSymlinks(filepath.Join(home, homePath)); resolved != want {
		t.Errorf("%s resolves to %s, want %s", checkoutPath, resolved, want)
	}
	if !strings.Contains(exclude, "/"+checkoutPath+"\n") {
		t.Errorf("exclude file lacks /%s", checkoutPath)
	}
}

// TestForeignFile keeps a real .claude/settings.json: qory does not replace it.
func TestForeignFile(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	rt := lookup(t, "claude")
	if err := render.Build(res, home, rt); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := render.LinkInto(rt, res, root, home, false)
	var foreign *render.ForeignPathError
	if !errorsAs(err, &foreign) {
		t.Fatalf("err = %v, want a ForeignPathError", err)
	}
	if foreign.Path != own {
		t.Errorf("foreign path = %s, want %s", foreign.Path, own)
	}
	if data, _ := os.ReadFile(own); string(data) != "{}\n" {
		t.Errorf("the checkout's own settings.json was replaced: %q", data)
	}
}

// TestSoftLink keeps a repository's own AGENTS.md and reports it instead of failing.
func TestSoftLink(t *testing.T) {
	res, root, home := composeFixture(t, "opencode")
	rt := lookup(t, "opencode")
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := render.Build(res, home, rt); err != nil {
		t.Fatal(err)
	}
	linked, err := render.LinkInto(rt, res, root, home, false)
	if err != nil || len(linked.Skipped) != 1 || linked.Skipped[0] != "AGENTS.md" {
		t.Fatalf("linked=%+v err=%v", linked, err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if string(data) != "ours\n" {
		t.Errorf("AGENTS.md was replaced: %q", data)
	}
	removed, err := render.Unlink(rt, root)
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

// TestClaudeMCPFragmentAndEntriesMeet is a module migrating from a settings/claude/mcp.json
// fragment: the file is written and linked with the fragment's servers, and a server
// composed as an entry is written on top of one the fragment names.
func TestClaudeMCPFragmentAndEntriesMeet(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	claude := lookup(t, "claude")
	res.Settings["claude"] = map[string]map[string]any{"mcp.json": {"mcpServers": map[string]any{
		"legacy": map[string]any{"command": "old"},
		"db":     map[string]any{"command": "fragment"},
	}}}
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "claude", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"legacy"`, `"old"`, `"python3"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("mcp.json lacks %s:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), `"fragment"`) {
		t.Errorf("the fragment's db server won over the entry:\n%s", data)
	}
	// A fragment alone still gets the file and the link.
	res.MCP = nil
	res.Entries = nil
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(claude, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".mcp.json")); err != nil {
		t.Errorf(".mcp.json is not linked for a fragment-only compose: %v", err)
	}
}
