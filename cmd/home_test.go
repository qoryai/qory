package cmd_test

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/report"
)

// trackedHarnessCheckout is a checkout on the linked stack, the core module linked as
// harness, whose repository tracks a harness directory of its own at that very path,
// everything committed. A compose into the checkout would refuse the link with exit 4;
// a compose outside it has no link to write.
func trackedHarnessCheckout(t *testing.T) string {
	t.Helper()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeOwnStack(t, root, linkedStack)
	writeFile(t, filepath.Join(root, "harness", "README.md"), "# The repository's own harness directory\n")
	// The hook is run through the launch settings below, so it has to be executable,
	// which a fixture copied by a test is not.
	if err := os.Chmod(filepath.Join(root, "modules", "core", "hooks", "guard.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "app with its own harness directory")
	return root
}

// clean fails the test when git sees anything in the checkout: a changed, untracked or
// ignored file, which a compose outside the checkout never leaves.
func clean(t *testing.T, root string) {
	t.Helper()
	if status := gitOut(t, root, "status", "--porcelain", "--ignored", "--untracked-files=all"); status != "" {
		t.Errorf("git status is not clean:\n%s", status)
	}
}

// TestComposeOutsideTheCheckoutLeavesItUntouched is the acceptance case: a checkout with a
// tracked harness directory, a stack that links the core module as harness, and a compose
// with --home naming a directory outside the checkout. The compose exits 0 and writes
// nothing under the checkout, byte for byte; the home under the directory holds the tree
// and the report beside it; launch prints the arguments that give Claude Code the
// plugin, the settings, the servers and the instructions; the hook the settings name
// runs from the home; check, inspect and remove find the pair from either side.
func TestComposeOutsideTheCheckoutLeavesItUntouched(t *testing.T) {
	root := trackedHarnessCheckout(t)
	before := snapshot(t, root)
	homes := tempDir(t)
	home := filepath.Join(homes, checkout.Key(root))

	out, err := run(t, "harness", "compose", "--home", homes)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "composed 8 entries from 2 modules")
	wantsRow(t, out, "home", home)
	wantsRow(t, out, "claude", "no links  (qory harness launch --runtime claude prints how to start it)")
	wantsRow(t, out, "link", "harness  (module core; not written, the checkout gets no links)")
	if !maps.Equal(snapshot(t, root), before) {
		t.Error("the compose changed the checkout")
	}
	clean(t, root)
	gone(t, root, ".qory", ".claude", ".mcp.json")
	if exclude, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude")); err == nil && strings.Contains(string(exclude), "\n/") {
		t.Errorf("the compose wrote exclude lines:\n%s", exclude)
	}

	rep, err := report.Read(home + "-report.json")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Checkout != root || rep.Home != home || rep.Links != report.NoLinks {
		t.Errorf("report checkout %q, home %q, links %q", rep.Checkout, rep.Home, rep.Links)
	}
	if rep.Modules[0].Link != "harness" {
		t.Errorf("the report lost the stack's link: %+v", rep.Modules[0])
	}
	for _, name := range []string{"AGENTS.md", "hooks/guard.sh", "modules/core/scripts/db.py", "claude/settings.json", "claude/mcp.json", "claude/CLAUDE.md", "claude/skills/review/SKILL.md", "claude/plugin/.claude-plugin/plugin.json", "claude/plugin/skills/review/SKILL.md", "claude/plugin/skills/e2e/SKILL.md", "claude/plugin/agents/reviewer.md", "claude/plugin/commands/ship.md", "claude/plugin/output-styles/terse.md"} {
		if _, err := os.Stat(filepath.Join(home, name)); err != nil {
			t.Errorf("home lacks %s: %v", name, err)
		}
	}
	// The hooks and the servers reach the session through the settings and mcp.json,
	// so the plugin carries neither, and nothing runs twice.
	gone(t, home, "claude/plugin/hooks", "claude/plugin/.mcp.json", "claude/plugin/settings.json")
	manifest, err := os.ReadFile(filepath.Join(home, "claude", "plugin", ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(manifest), `"name": "harness"`, `"outputStyles": "./output-styles"`)

	// The launch line names the home's files by absolute path and cuts the setting
	// sources to the user's.
	out, err = run(t, "harness", "launch", "--runtime", "claude", "--home", homes)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	claude := filepath.Join(home, "claude")
	want := strings.Join([]string{"claude", "--plugin-dir", filepath.Join(claude, "plugin"), "--settings", filepath.Join(claude, "settings.json"), "--mcp-config", filepath.Join(claude, "mcp.json"), "--append-system-prompt-file", filepath.Join(claude, "CLAUDE.md"), "--setting-sources", "user"}, " ")
	if strings.TrimSpace(out) != want {
		t.Errorf("launch printed\n%s\nwant\n%s", out, want)
	}
	// The settings carry the hook by its path in the home, and it runs from there,
	// with the module environment rendered for the home.
	var settings struct {
		Env   map[string]string `json:"env"`
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	data, err := os.ReadFile(filepath.Join(claude, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Env["QORY_HARNESS_HOME"] != home {
		t.Errorf("QORY_HARNESS_HOME = %q, want %q", settings.Env["QORY_HARNESS_HOME"], home)
	}
	command := settings.Hooks["PreToolUse"][0].Hooks[0].Command
	if command != filepath.Join(home, "hooks", "guard.sh") {
		t.Errorf("hook command %q", command)
	}
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
		t.Errorf("the hook does not run from the home: %v\n%s", err, out)
	}
	if strings.Contains(string(data), "QORY_HARNESS_HOME/") {
		t.Errorf("settings.json still names $QORY_HARNESS_HOME:\n%s", data)
	}

	// A check compares the home wherever it is, and touches the checkout as little as
	// a compose does.
	out, err = run(t, "harness", "compose", "--check", "--home", homes)
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	wants(t, out, "up to date")
	if !maps.Equal(snapshot(t, root), before) {
		t.Error("the check changed the checkout")
	}

	// From the other side: standing outside the checkout, --home naming the home itself
	// finds the checkout through the report.
	t.Chdir(tempDir(t))
	out, err = run(t, "harness", "inspect", "--home", home)
	if err != nil {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	wantsRow(t, out, "checkout", root)
	wantsRow(t, out, "links", "none")
	wants(t, out, "linked as harness")
	out, err = run(t, "harness", "remove", "--home", home)
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	wants(t, out, "removed "+home+"\n", "removed "+home+"-report.json\n")
	gone(t, homes, filepath.Base(home), filepath.Base(home)+"-report.json")
	if !maps.Equal(snapshot(t, root), before) {
		t.Error("the remove changed the checkout")
	}
	clean(t, root)
	// With the report gone the home is read as a root again, and the process stands
	// in no checkout, so there is nothing to find.
	if _, err = run(t, "harness", "remove", "--home", home); err == nil || !strings.Contains(err.Error(), "is not inside a git working tree") {
		t.Errorf("second remove: %v", err)
	}
}

// TestHomeFromTheConfiguration is harness.home in the user's file: every compose of the
// checkout goes under the directory it names, keyed by the checkout, and launch needs no
// flag. harness.links: checkout is refused with such a home, and a home inside the
// checkout other than .qory/harness is refused too.
func TestHomeFromTheConfiguration(t *testing.T) {
	root := trackedHarnessCheckout(t)
	before := snapshot(t, root)
	homes := tempDir(t)
	home := filepath.Join(homes, checkout.Key(root))
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	writeFile(t, user, "apiVersion: qory.dev/v1alpha1\nharness: {home: "+homes+"}\n")

	out, err := run(t, "config")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) >= 3 {
			rows[f[0]] = strings.Join(f[1:], " ")
		}
	}
	if rows["harness.home"] != homes+" ~/.config/qory/qory.yaml" || rows["harness.links"] != "(by home) default" {
		t.Errorf("config rows: %v\n%s", rows, out)
	}
	out, err = run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "home", home)
	if !maps.Equal(snapshot(t, root), before) {
		t.Error("the compose changed the checkout")
	}
	if _, err := os.Stat(filepath.Join(home, "claude", "plugin", ".claude-plugin", "plugin.json")); err != nil {
		t.Error(err)
	}
	out, err = run(t, "harness", "launch")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "claude --plugin-dir "+filepath.Join(home, "claude", "plugin")+" ")
	out, err = run(t, "harness", "remove")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed "+home+"\n")
	gone(t, homes, filepath.Base(home))

	writeFile(t, user, "apiVersion: qory.dev/v1alpha1\nharness: {home: "+homes+", links: checkout}\n")
	out, err = run(t, "harness", "compose")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("links: checkout with a home outside: %v, exit %d\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, err.Error(), "harness.links: checkout needs the home inside the checkout")
	writeFile(t, user, "apiVersion: qory.dev/v1alpha1\nharness: {home: build/harness}\n")
	out, err = run(t, "harness", "compose")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("a home inside the checkout elsewhere: %v, exit %d\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, err.Error(), "a home inside the checkout is .qory/harness; build/harness is not it")
	writeFile(t, user, "apiVersion: qory.dev/v1alpha1\nharness: {links: sometimes}\n")
	if _, err = run(t, "harness", "compose"); err == nil || !strings.Contains(err.Error(), `harness.links "sometimes" is not checkout or none`) {
		t.Errorf("links: sometimes: %v", err)
	}
	if !maps.Equal(snapshot(t, root), before) {
		t.Error("a refused compose changed the checkout")
	}
}

// TestNoLinksKeepsTheHomeInTheCheckout is --no-links, and harness.links: none, with the
// default home: the tree and the report go under .qory, excluded from git, and nothing
// else is written; launch works on it, and remove takes .qory.
func TestNoLinksKeepsTheHomeInTheCheckout(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeOwnStack(t, root, linkedStack)
	out, err := run(t, "harness", "compose", "--no-links")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "home", ".qory/harness")
	wantsRow(t, out, "claude", "no links  (qory harness launch --runtime claude prints how to start it)")
	gone(t, root, ".claude", ".mcp.json", "harness")
	if _, err := os.Stat(filepath.Join(root, ".qory", "harness", "claude", "plugin")); err != nil {
		t.Error(err)
	}
	exclude, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	wants(t, string(exclude), "/.qory\n")
	lacks(t, string(exclude), "/.claude", "/harness")
	if rep := readReport(t, root); rep.Links != report.NoLinks {
		t.Errorf("report links %q", rep.Links)
	}
	out, err = run(t, "harness", "launch")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "--plugin-dir "+filepath.Join(root, ".qory", "harness", "claude", "plugin")+" ", "--setting-sources user")

	// A compose with links after one without writes them, and one without after one
	// with takes them back, the way any compose takes back what it no longer asks for.
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dirLinks(t, root, ".claude", "skills", "settings.json")
	gone(t, root, ".claude/plugin")
	if _, err := os.Lstat(filepath.Join(root, "harness")); err != nil {
		t.Error("the module link is not written with links on")
	}
	configure(t, root, []string{"links: none"}, nil)
	out, err = run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(root, ".claude", "settings.json")); err == nil {
		t.Error("harness.links: none left the runtime's links in the checkout")
	}
	for _, path := range []string{".claude", ".mcp.json", "harness"} {
		wants(t, out, "  removed  "+path+"  (linked by an earlier compose; the checkout gets no links)\n")
	}
	if _, err := os.Lstat(filepath.Join(root, "harness")); err == nil {
		t.Error("harness.links: none left the module link in the checkout")
	}
	out, err = run(t, "harness", "remove")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gone(t, root, ".qory", ".claude")
}

// TestLaunchNeedsARuntimeWithASpec is launch on a home composed for two runtimes: the
// runtime has to be named, one the harness is not composed for is refused, and one whose
// program reads the harness from the checkout alone has no launch template.
func TestLaunchNeedsARuntimeWithASpec(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "harness", "compose", "--runtime", "claude,goose", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "the harness is composed for claude, goose; --runtime says which to start"},
		{[]string{"--runtime", "gemini"}, "the harness is not composed for gemini; composed: claude, goose"},
		{[]string{"--runtime", "goose"}, "goose reads its harness from the checkout alone"},
	} {
		_, err := run(t, append([]string{"harness", "launch"}, c.args...)...)
		if err == nil || cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), c.want) {
			t.Errorf("launch %v: %v, exit %d, want %q", c.args, err, cmd.ExitCode(err), c.want)
		}
	}
	if out, err := run(t, "harness", "launch", "--runtime", "claude"); err != nil || !strings.Contains(out, "--plugin-dir") {
		t.Errorf("launch claude: %v\n%s", err, out)
	}
	if out, err := run(t, "harness", "remove"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := run(t, "harness", "launch", "--runtime", "claude"); err == nil || !strings.Contains(err.Error(), "no composed harness for "+root) {
		t.Errorf("launch after remove: %v", err)
	}
}

// TestWorktreeHomeOutsideTheCheckout is harness.home in the user's file with worktrees:
// add composes the worktree into its own home under the directory, keyed by the worktree,
// list says it is composed and names the report there, and remove takes the home with
// the worktree. The worktree itself gets nothing.
func TestWorktreeHomeOutsideTheCheckout(t *testing.T) {
	root := worktreeRepo(t)
	homes := tempDir(t)
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	writeFile(t, user, "apiVersion: qory.dev/v1alpha1\nharness: {home: "+homes+"}\n")
	out, err := run(t, "wa", "feature")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	home := filepath.Join(homes, checkout.Key(wt))
	wants(t, out, "composed 8 entries from 2 modules")
	wantsRow(t, out, "home", home)
	gone(t, wt, ".qory", ".claude")
	if _, err := os.Stat(home + "-report.json"); err != nil {
		t.Errorf("the worktree's report is not beside its home: %v", err)
	}
	if home == filepath.Join(homes, checkout.Key(root)) {
		t.Error("the worktree and the main checkout share one home")
	}
	out, err = run(t, "wl", "--verbose")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "../wt-feature", "composed", filepath.Base(home)+"-report.json")
	out, err = run(t, "wr", "feature")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed  "+home+"  (its home)")
	gone(t, homes, filepath.Base(home), filepath.Base(home)+"-report.json")
}

// TestLaunchPerRuntime composes one home for every runtime with a launch template and
// checks each line: the program, the flags or the variables that hand it the home, and
// the files under the runtime's directory those name. The line is the runtime's own
// template; --json prints the same as one object.
func TestLaunchPerRuntime(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	out, err := run(t, "harness", "compose", "--runtime", "claude,codex,opencode,amp,gemini,cursor,copilot", "--no-links")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	dir := func(rt string) string { return filepath.Join(home, rt) }
	lines := map[string]string{
		"claude":   "claude --plugin-dir " + dir("claude") + "/plugin --settings " + dir("claude") + "/settings.json --mcp-config " + dir("claude") + "/mcp.json --append-system-prompt-file " + dir("claude") + "/CLAUDE.md --setting-sources user",
		"codex":    "env CODEX_HOME=" + dir("codex") + " codex",
		"opencode": "env OPENCODE_CONFIG_DIR=" + dir("opencode") + " opencode",
		"amp":      "amp --settings-file " + dir("amp") + "/settings.json",
		"gemini":   "env GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + dir("gemini") + "/settings.json gemini",
		"cursor":   "cursor-agent --plugin-dir " + dir("cursor") + "/plugin",
		"copilot":  "copilot --add-dir " + dir("copilot") + "/workspace --additional-mcp-config @" + dir("copilot") + "/mcp.json",
	}
	for rt, want := range lines {
		out, err := run(t, "harness", "launch", "--runtime", rt)
		if err != nil {
			t.Errorf("launch %s: %v\n%s", rt, err, out)
			continue
		}
		if got := strings.TrimSpace(out); got != want {
			t.Errorf("launch %s printed\n%s\nwant\n%s", rt, got, want)
		}
	}
	// What each template names is there: the directories a program reads as its own,
	// with the skills linked and the instructions written where it looks for them.
	for _, name := range []string{
		"codex/skills/review/SKILL.md", "codex/AGENTS.md", "codex/config.toml",
		"opencode/skills/review/SKILL.md", "opencode/agents/reviewer.md",
		"cursor/plugin/.cursor-plugin/plugin.json", "cursor/plugin/skills/review/SKILL.md", "cursor/plugin/agents/reviewer.md", "cursor/plugin/.mcp.json",
		"copilot/workspace/.github/skills/review/SKILL.md", "copilot/workspace/.github/agents/reviewer.agent.md", "copilot/mcp.json",
		"gemini/settings.json", "amp/settings.json",
	} {
		if _, err := os.Stat(filepath.Join(home, name)); err != nil {
			t.Errorf("home lacks %s: %v", name, err)
		}
	}
	// No module ships a cursor hooks fragment, so the plugin carries no hooks file.
	gone(t, home, "cursor/plugin/hooks")
	data, _ := os.ReadFile(filepath.Join(home, "copilot", "mcp.json"))
	wants(t, string(data), `"mcpServers"`, `"db"`)
	lacks(t, out, "mcp/db  (no place in copilot)")

	out, err = run(t, "harness", "launch", "--runtime", "codex", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if got.Command != "codex" || got.Args == nil || len(got.Args) != 0 || got.Env["CODEX_HOME"] != dir("codex") {
		t.Errorf("--json printed %+v", got)
	}
}

// TestLaunchFromTheConfiguration is harness.launch in the user's file: a field set there
// stands in for the runtime's own, a group naming a file the compose did not write is
// left out, ${home} and ${dir} are the home and the runtime's directory, qory config
// shows the entry, and a malformed entry is refused by name.
func TestLaunchFromTheConfiguration(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	writeFile(t, user, `apiVersion: qory.dev/v1alpha1
harness:
  launch:
    claude:
      command: /opt/claude/bin/claude
      args:
        - [--plugin-dir, "${dir}/plugin"]
        - [--append-system-prompt-file, "${dir}/nothing.md"]
        - --verbose
    codex:
      env: {CODEX_HOME: "${home}", OPENAI_API_KEY: "it's secret"}
`)
	if out, err := run(t, "harness", "compose", "--runtime", "claude,codex", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	out, err := run(t, "harness", "launch", "--runtime", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), "/opt/claude/bin/claude --plugin-dir "+home+"/claude/plugin --verbose"; got != want {
		t.Errorf("launch printed\n%s\nwant\n%s", got, want)
	}
	out, err = run(t, "harness", "launch", "--runtime", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), "env CODEX_HOME="+home+" 'OPENAI_API_KEY=it'\\''s secret' codex"; got != want {
		t.Errorf("launch printed\n%s\nwant\n%s", got, want)
	}
	out, err = run(t, "config")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "harness.launch.claude", "/opt/claude/bin/claude --plugin-dir ${dir}/plugin --append-system-prompt-file ${dir}/nothing.md --verbose", "harness.launch.codex", "CODEX_HOME=${home} OPENAI_API_KEY=it's secret")
	for _, c := range []struct{ file, want string }{
		{"harness: {launch: {claude: {command: \"\"}}}\n", "harness.launch.claude.command is empty"},
		{"harness: {launch: {claude: {args: [[]]}}}\n", "harness.launch.claude.args[0] is empty"},
		{"harness: {launch: {codex: {env: {1BAD: x}}}}\n", "harness.launch.codex.env: 1BAD is not an environment variable name"},
		{"harness: {launch: {codex: {env: {QORY_HARNESS_HOME: x}}}}\n", "harness.launch.codex.env.QORY_HARNESS_HOME is qory's own"},
	} {
		writeFile(t, user, "apiVersion: qory.dev/v1alpha1\n"+c.file)
		if _, err := run(t, "config"); err == nil || !strings.Contains(err.Error(), c.want) || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%s: %v, want %q", c.file, err, c.want)
		}
	}
}

// TestLaunchDirectoriesAreNotLinked is a compose with links for the runtimes that render
// a launch directory beside what the checkout links: the plugin, the skills and the
// instructions a program takes from outside stay in the home, and the checkout's
// directories hold what they held before.
func TestLaunchDirectoriesAreNotLinked(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "harness", "compose", "--runtime", "claude,codex,opencode,cursor"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gone(t, root, ".claude/plugin", ".cursor/plugin", ".codex/skills", ".codex/AGENTS.md", ".opencode/skills")
	dirLinks(t, root, ".codex", "config.toml", "agents")
	dirLinks(t, root, ".opencode", "agents", "commands")
	dirLinks(t, root, ".cursor", "agents", "mcp.json")
}
