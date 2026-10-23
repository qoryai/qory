package cmd_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	for _, name := range []string{"AGENTS.md", "hooks/guard.sh", "modules/core/scripts/db.py", "claude/settings.json", "claude/mcp.json", "claude/CLAUDE.md", "claude/launch/CLAUDE.md", "claude/skills/review/SKILL.md", "claude/plugin/.claude-plugin/plugin.json", "claude/plugin/skills/review/SKILL.md", "claude/plugin/skills/e2e/SKILL.md", "claude/plugin/agents/reviewer.md", "claude/plugin/commands/ship.md", "claude/plugin/output-styles/terse.md"} {
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
	// sources to the user's. Its variables are the home and the fragments' A and B,
	// which the settings do not hold.
	out, err = run(t, "harness", "launch", "--runtime", "claude", "--home", homes)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	claude := filepath.Join(home, "claude")
	want := strings.Join([]string{"env", "QORY_HARNESS_HOME=" + home, "A=core", "B=1", "claude", "--plugin-dir", filepath.Join(claude, "plugin"), "--settings", filepath.Join(claude, "settings.json"), "--mcp-config", filepath.Join(claude, "mcp.json"), "--append-system-prompt-file", filepath.Join(claude, "launch", "CLAUDE.md"), "--setting-sources", "user"}, " ")
	if strings.TrimSpace(out) != want {
		t.Errorf("launch printed\n%s\nwant\n%s", out, want)
	}
	// The settings carry the hook by its path in the home, and it runs from there; the
	// environment is the launch's, not theirs.
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
	if settings.Env != nil {
		t.Errorf("settings env = %v, want none", settings.Env)
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
	// With the report gone the home is read as a directory of homes again, outside the
	// folder the process stands in, and it contains no home of that folder.
	if out, err = run(t, "harness", "remove", "--home", home); err != nil {
		t.Errorf("second remove: %v\n%s", err, out)
	}
	wants(t, out, "nothing composed here")
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
		wants(t, out, "  removed  "+path+"  (linked by a previous compose; the checkout gets no links)\n")
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
		{nil, "the harness is composed for claude, goose; --runtime selects which to start"},
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
	// Every line starts with the home; claude's carries its fragments' A and B.
	at := "env QORY_HARNESS_HOME=" + home + " "
	lines := map[string]string{
		"claude":   at + "A=core B=1 claude --plugin-dir " + dir("claude") + "/plugin --settings " + dir("claude") + "/settings.json --mcp-config " + dir("claude") + "/mcp.json --append-system-prompt-file " + dir("claude") + "/launch/CLAUDE.md --setting-sources user",
		"codex":    at + "CODEX_HOME=" + dir("codex") + " codex",
		"opencode": at + "OPENCODE_CONFIG_DIR=" + dir("opencode") + " opencode",
		"amp":      at + "amp --settings-file " + dir("amp") + "/settings.json",
		"gemini":   at + "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + dir("gemini") + "/settings.json gemini",
		"cursor":   at + "cursor-agent --plugin-dir " + dir("cursor") + "/plugin",
		"copilot":  at + "copilot --add-dir " + dir("copilot") + "/workspace --additional-mcp-config @" + dir("copilot") + "/mcp.json",
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
	if got.Command != "codex" || got.Args == nil || len(got.Args) != 0 || got.Env["CODEX_HOME"] != dir("codex") || got.Env["QORY_HARNESS_HOME"] != home || len(got.Env) != 2 {
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
	if got, want := strings.TrimSpace(out), "env QORY_HARNESS_HOME="+home+" A=core B=1 /opt/claude/bin/claude --plugin-dir "+home+"/claude/plugin --verbose"; got != want {
		t.Errorf("launch printed\n%s\nwant\n%s", got, want)
	}
	out, err = run(t, "harness", "launch", "--runtime", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), "env QORY_HARNESS_HOME="+home+" CODEX_HOME="+home+" 'OPENAI_API_KEY=it'\\''s secret' codex"; got != want {
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
		{"harness: {launch: {codex: {env: {QORY_HARNESS_HOME: x}}}}\n", "harness.launch.codex.env.QORY_HARNESS_HOME is qory's"},
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
	gone(t, root, ".claude/plugin", ".claude/launch", ".cursor/plugin", ".codex/skills", ".codex/AGENTS.md", ".opencode/skills")
	dirLinks(t, root, ".codex", "config.toml", "agents")
	dirLinks(t, root, ".opencode", "agents", "commands")
	dirLinks(t, root, ".cursor", "agents", "mcp.json")
}

// TestLaunchPrintsTheRegisteredNames is the names a launcher builds its prompt from:
// --json carries addresses, per kind, the name the session registers each agent, skill
// and command under on that launch, a bound role beside them as the entry it is bound to,
// harness:<name> for claude, whose plugin prefixes every kind, and the bare name for
// codex; --address prints one of them alone, and a name the harness is not composed with
// is refused.
func TestLaunchPrintsTheRegisteredNames(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "references-resolve", root)
	if out, err := run(t, "harness", "compose", "--runtime", "claude,codex", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	type launch struct {
		Addresses map[string]map[string]string `json:"addresses"`
	}
	read := func(rt string) launch {
		t.Helper()
		out, err := run(t, "harness", "launch", "--runtime", rt, "--json")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		var got launch
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v:\n%s", err, out)
		}
		return got
	}
	claude := read("claude")
	want := map[string]map[string]string{
		"agents":   {"coder": "harness:rails-coder", "rails-coder": "harness:rails-coder", "reviewer": "harness:reviewer"},
		"commands": {"ship": "harness:ship"},
		"skills":   {"deploy": "harness:deploy", "test": "harness:test"},
	}
	if !reflect.DeepEqual(claude.Addresses, want) {
		t.Errorf("claude addresses %v, want %v", claude.Addresses, want)
	}
	// Codex skips commands, and registers what it has under the modules' names.
	codex := read("codex")
	want = map[string]map[string]string{
		"agents": {"coder": "rails-coder", "rails-coder": "rails-coder", "reviewer": "reviewer"},
		"skills": {"deploy": "deploy", "test": "test"},
	}
	if !reflect.DeepEqual(codex.Addresses, want) {
		t.Errorf("codex addresses %v, want %v", codex.Addresses, want)
	}
	for _, c := range [][3]string{{"claude", "skills/deploy", "harness:deploy"}, {"claude", "agents/coder", "harness:rails-coder"}, {"codex", "agents/coder", "rails-coder"}} {
		out, err := run(t, "harness", "launch", "--runtime", c[0], "--address", c[1])
		if err != nil {
			t.Errorf("--address %s for %s: %v\n%s", c[1], c[0], err, out)
			continue
		}
		if got := strings.TrimSpace(out); got != c[2] {
			t.Errorf("--address %s for %s printed %q, want %q", c[1], c[0], got, c[2])
		}
	}
	out, err := run(t, "harness", "launch", "--runtime", "codex", "--address", "commands/ship")
	if err == nil {
		t.Fatalf("--address commands/ship for codex printed %q; want a refusal, codex has no commands", out)
	}
	wants(t, err.Error(), "commands/ship is not an agent, skill, command or bound role the harness is composed with for codex")
}

// TestLaunchCarriesTheHarnessEnv is codex composed on a module exporting CORE_SCRIPTS and
// a qory.yaml setting SERVICE_TOKEN over a module export of the same name: the launch line
// and --json carry QORY_HARNESS_HOME, then the template's CODEX_HOME, fixed, then the
// defaults, the export CORE_SCRIPTS and SERVICE_TOKEN, config.toml holds none of them, and
// the report records each with where it comes from, a default.
func TestLaunchCarriesTheHarnessEnv(t *testing.T) {
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "modules", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\nenv:\n  CORE_SCRIPTS: scripts\n  SERVICE_TOKEN: .\n")
	writeFile(t, filepath.Join(root, "modules", "core", "scripts", "run.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: core\n      source: {path: modules/core}\nenv:\n  SERVICE_TOKEN: example-token\n")
	if out, err := run(t, "harness", "compose", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	out, err := run(t, "harness", "launch")
	if err != nil {
		t.Fatal(err)
	}
	want := "env QORY_HARNESS_HOME=" + home + " CODEX_HOME=" + home + "/codex CORE_SCRIPTS=" + home + "/modules/core/scripts SERVICE_TOKEN=example-token codex"
	if got := strings.TrimSpace(out); got != want {
		t.Errorf("launch printed\n%s\nwant\n%s", got, want)
	}
	out, err = run(t, "harness", "launch", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	wantEnv := map[string]string{"QORY_HARNESS_HOME": home, "CODEX_HOME": home + "/codex", "CORE_SCRIPTS": home + "/modules/core/scripts", "SERVICE_TOKEN": "example-token"}
	if !maps.Equal(got.Env, wantEnv) {
		t.Errorf("--json env %v, want %v", got.Env, wantEnv)
	}
	data, err := os.ReadFile(filepath.Join(home, "codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(data), "shell_environment_policy", "QORY_HARNESS_HOME", "CORE_SCRIPTS", "SERVICE_TOKEN")
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantVars := []report.Var{
		{Name: "CORE_SCRIPTS", Value: "$QORY_HARNESS_HOME/modules/core/scripts", From: "module core"},
		{Name: "SERVICE_TOKEN", Value: "example-token", From: "configuration"},
	}
	if !reflect.DeepEqual(rep.LaunchEnv["codex"], wantVars) {
		t.Errorf("report launch_env %+v, want %+v", rep.LaunchEnv, wantVars)
	}
	out, err = run(t, "harness", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			rows[f[0]] = strings.Join(f[1:], " ")
		}
	}
	if rows["CORE_SCRIPTS"] != "$QORY_HARNESS_HOME/modules/core/scripts default, module core" || rows["SERVICE_TOKEN"] != "example-token default, configuration" {
		t.Errorf("inspect's env rows:\n%s", out)
	}
}

// TestLaunchKeepsTheTemplatesOwnVariable is a qory.yaml whose env sets CODEX_HOME, the
// name Codex's own launch template sets: the launch keeps the template's, the report's
// launch_env leaves the configuration's out, and a check is up to date. With
// harness.launch.codex.env replacing the template's, the configuration's value is the
// launch's, the check sees the report lacks it, and a compose records it.
func TestLaunchKeepsTheTemplatesOwnVariable(t *testing.T) {
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "modules", "core", "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: core\n")
	stack := "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: core\n      source: {path: modules/core}\n"
	writeFile(t, filepath.Join(root, "qory.yaml"), stack+"env:\n  CODEX_HOME: /elsewhere\n")
	if out, err := run(t, "harness", "compose", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	out, err := run(t, "harness", "launch")
	if err != nil {
		t.Fatal(err)
	}
	if want := "env QORY_HARNESS_HOME=" + home + " CODEX_HOME=" + home + "/codex codex"; strings.TrimSpace(out) != want {
		t.Errorf("launch printed\n%s\nwant\n%s", strings.TrimSpace(out), want)
	}
	rep, err := report.Read(filepath.Join(root, ".qory", "harness-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if vars := rep.LaunchEnv["codex"]; len(vars) != 0 {
		t.Errorf("report launch_env %+v, want none", vars)
	}
	if out, err := run(t, "harness", "compose", "--check"); err != nil {
		t.Fatalf("check after a compose: %v\n%s", err, out)
	}

	writeFile(t, filepath.Join(root, "qory.yaml"), stack+"  launch:\n    codex: {env: {CODEX_HOME: \"${dir}\"}}\nenv:\n  CODEX_HOME: /elsewhere\n")
	out, err = run(t, "harness", "compose", "--check")
	if cmd.ExitCode(err) != cmd.ExitStale {
		t.Fatalf("check after harness.launch.codex.env: %v\n%s", err, out)
	}
	wantsRow(t, out, "launch_env/codex/CODEX_HOME", "missing")
	if out, err := run(t, "harness", "compose", "--no-links"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = run(t, "harness", "launch")
	if err != nil {
		t.Fatal(err)
	}
	if want := "env QORY_HARNESS_HOME=" + home + " CODEX_HOME=/elsewhere codex"; strings.TrimSpace(out) != want {
		t.Errorf("launch under harness.launch.codex.env printed\n%s\nwant\n%s", strings.TrimSpace(out), want)
	}
	if out, err := run(t, "harness", "compose", "--check"); err != nil {
		t.Fatalf("check after the second compose: %v\n%s", err, out)
	}
}

// harnessStack is a stack of one module, app, read from ./harness in the checkout, for
// claude.
const harnessStack = `apiVersion: qory.dev/v1alpha1
target:
  runtime: claude
modules:
  - name: app
    source: {path: ./harness}
`

// writeAppModule writes the module app into dir: a skill deploy and a command ship,
// neither with a reference.
func writeAppModule(t *testing.T, dir string) {
	t.Helper()
	writeManifest(t, dir, "app")
	writeFile(t, filepath.Join(dir, "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Deploys the app.\n---\n\nSteps.\n")
	writeFile(t, filepath.Join(dir, "commands", "ship.md"), "# ship\n\nShips the app.\n")
}

// symlinkTo writes a link at path to target, failing the test on an error.
func symlinkTo(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// TestCopyDecision is the decision a compose makes once per module: a home outside the
// checkout contains copies of a module that lies in the checkout, as written or with
// every link resolved, and links to every other module; a home inside the checkout
// contains links to every module.
func TestCopyDecision(t *testing.T) {
	t.Run("a module at ./harness is copied", func(t *testing.T) {
		root := newCheckout(t)
		writeOwnStack(t, root, harnessStack)
		writeAppModule(t, filepath.Join(root, "harness"))
		copied, err := cmd.CopiedModules(tempDir(t))
		if err != nil {
			t.Fatal(err)
		}
		if !copied["app"] {
			t.Errorf("copied = %v, want app copied", copied)
		}
	})
	t.Run("a ./harness that links out of the checkout is copied", func(t *testing.T) {
		root := newCheckout(t)
		writeOwnStack(t, root, harnessStack)
		shared := tempDir(t)
		writeAppModule(t, shared)
		symlinkTo(t, shared, filepath.Join(root, "harness"))
		copied, err := cmd.CopiedModules(tempDir(t))
		if err != nil {
			t.Fatal(err)
		}
		if !copied["app"] {
			t.Errorf("copied = %v, want app copied", copied)
		}
	})
	t.Run("a module outside the checkout that links into it is copied", func(t *testing.T) {
		root := newCheckout(t)
		writeAppModule(t, filepath.Join(root, "harness"))
		outside := tempDir(t)
		symlinkTo(t, filepath.Join(root, "harness"), filepath.Join(outside, "app"))
		writeOwnStack(t, root, strings.Replace(harnessStack, "./harness", filepath.Join(outside, "app"), 1))
		copied, err := cmd.CopiedModules(tempDir(t))
		if err != nil {
			t.Fatal(err)
		}
		if !copied["app"] {
			t.Errorf("copied = %v, want app copied", copied)
		}
	})
	t.Run("a module outside the checkout is linked", func(t *testing.T) {
		root := newCheckout(t)
		outside := tempDir(t)
		writeAppModule(t, outside)
		writeOwnStack(t, root, strings.Replace(harnessStack, "./harness", outside, 1))
		copied, err := cmd.CopiedModules(tempDir(t))
		if err != nil {
			t.Fatal(err)
		}
		if copied["app"] {
			t.Errorf("copied = %v, want app linked", copied)
		}
	})
	t.Run("a home inside the checkout copies nothing", func(t *testing.T) {
		root := newCheckout(t)
		writeOwnStack(t, root, harnessStack)
		writeAppModule(t, filepath.Join(root, "harness"))
		copied, err := cmd.CopiedModules("")
		if err != nil {
			t.Fatal(err)
		}
		if len(copied) != 1 || copied["app"] {
			t.Errorf("copied = %v, want app linked", copied)
		}
	})
}

// writeFullAppModule writes the module app into dir with an entry of every kind a home
// links today: the skill deploy and the command ship, a skill release whose SKILL.md
// references the command, beside a file that references nothing, an agent, an output
// style, an executable hook, a files entry for claude, and a script no entry lists.
func writeFullAppModule(t *testing.T, dir string) {
	t.Helper()
	writeAppModule(t, dir)
	writeFile(t, filepath.Join(dir, "skills", "release", "SKILL.md"), "---\nname: release\ndescription: Releases the app.\n---\n\nRun ${qory:commands/ship}.\n")
	writeFile(t, filepath.Join(dir, "skills", "release", "notes.txt"), "No reference here.\n")
	writeFile(t, filepath.Join(dir, "agents", "reviewer.md"), "# reviewer\n")
	writeFile(t, filepath.Join(dir, "output-styles", "terse.md"), "# terse\n")
	writeFile(t, filepath.Join(dir, "hooks", "guard.sh"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "hooks", "guard.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "files", "claude", "rules", "style.md"), "# style\n")
	writeFile(t, filepath.Join(dir, "scripts", "db.py"), "print('db')\n")
}

// linksUnder lists every link in tree whose target, with every link resolved, lies under
// root, as the path relative to tree.
func linksUnder(t *testing.T, tree, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(tree, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(root, real); err == nil && !strings.HasPrefix(rel, "..") {
			found = append(found, strings.TrimPrefix(path, tree+"/"))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// TestHomeOutsideCopiesAModuleInTheCheckout is a compose of a module at ./harness into a
// home outside the checkout: no link in the home leads under the checkout, every entry
// and modules/app are copies, a skill with a reference is a full copy with the
// reference resolved, and an edit to the module makes --check exit 6 until the next
// compose.
func TestHomeOutsideCopiesAModuleInTheCheckout(t *testing.T) {
	root := newCheckout(t)
	writeOwnStack(t, root, harnessStack)
	writeFullAppModule(t, filepath.Join(root, "harness"))
	homes := tempDir(t)
	home := filepath.Join(homes, checkout.Key(root))
	if out, err := run(t, "harness", "compose", "--home", homes); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if links := linksUnder(t, home, root); len(links) > 0 {
		t.Errorf("links in the home lead under the checkout: %v", links)
	}
	for name, want := range map[string]string{
		"skills/deploy/SKILL.md":                 "Steps.",
		"claude/skills/deploy/SKILL.md":          "Steps.",
		"claude/commands/ship.md":                "Ships the app.",
		"claude/plugin/skills/deploy/SKILL.md":   "Steps.",
		"claude/plugin/commands/ship.md":         "Ships the app.",
		"claude/agents/reviewer.md":              "# reviewer",
		"claude/output-styles/terse.md":          "# terse",
		"claude/plugin/output-styles/terse.md":   "# terse",
		"claude/rules/style.md":                  "# style",
		"hooks/guard.sh":                         "exit 0",
		"claude/hooks/guard.sh":                  "exit 0",
		"modules/app/scripts/db.py":              "print('db')",
		"modules/app/qory-module.yaml":           "name: app",
		"skills/release/notes.txt":               "No reference here.",
		"skills/release/SKILL.md":                "Run ship.",
		"claude/skills/release/SKILL.md":         "Run ship.",
		"claude/plugin/skills/release/SKILL.md":  "Run harness:ship.",
		"claude/plugin/skills/release/notes.txt": "No reference here.",
	} {
		path := filepath.Join(home, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("%s is not a file: %v %v", name, info, err)
			continue
		}
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), want) {
			t.Errorf("%s = %q, want %q in it", name, data, want)
		}
	}
	if info, err := os.Stat(filepath.Join(home, "hooks", "guard.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("hooks/guard.sh: %v %v, want mode 0755", info, err)
	}
	if out, err := run(t, "harness", "compose", "--check", "--home", homes); err != nil {
		t.Fatalf("a home just composed is stale: %v\n%s", err, out)
	}
	writeFile(t, filepath.Join(root, "harness", "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Deploys the app.\n---\n\nOther steps.\n")
	out, err := run(t, "harness", "compose", "--check", "--home", homes)
	if got := cmd.ExitCode(err); got != cmd.ExitStale {
		t.Fatalf("exit %d for %v, want %d\n%s", got, err, cmd.ExitStale, out)
	}
	wantsRow(t, out, "claude/plugin/skills/deploy/SKILL.md", "changed")
	if out, err := run(t, "harness", "compose", "--home", homes); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "claude", "plugin", "skills", "deploy", "SKILL.md")); !strings.Contains(string(data), "Other steps.") {
		t.Errorf("the next compose did not take the edit: %q", data)
	}
}

// TestHomeInsideKeepsLinksToAModuleInTheCheckout is the same module composed into the
// home inside the checkout: every entry and modules/app are links into the module, as
// before.
func TestHomeInsideKeepsLinksToAModuleInTheCheckout(t *testing.T) {
	root := newCheckout(t)
	writeOwnStack(t, root, harnessStack)
	writeFullAppModule(t, filepath.Join(root, "harness"))
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	module := filepath.Join(root, "harness")
	for name, want := range map[string]string{
		"skills/deploy":                          "skills/deploy",
		"claude/skills/deploy":                   "skills/deploy",
		"claude/commands/ship.md":                "commands/ship.md",
		"claude/plugin/skills/deploy":            "skills/deploy",
		"claude/plugin/commands/ship.md":         "commands/ship.md",
		"claude/agents/reviewer.md":              "agents/reviewer.md",
		"claude/rules/style.md":                  "files/claude/rules/style.md",
		"hooks/guard.sh":                         "hooks/guard.sh",
		"modules/app":                            "",
		"claude/plugin/skills/release/notes.txt": "skills/release/notes.txt",
	} {
		target, err := os.Readlink(filepath.Join(home, name))
		if err != nil || target != filepath.Join(module, want) {
			t.Errorf("%s links to %q (%v), want %s", name, target, err, filepath.Join(module, want))
		}
	}
}

// TestPluginOfTwoModulesInTheCheckoutStopsAt10000 is two modules in the checkout whose
// skills hold 5,000 files each: each module is copied, and the plugin, with more than
// 10,000 entries, is refused at the compose, before a walled launch would refuse it.
func TestPluginOfTwoModulesInTheCheckoutStopsAt10000(t *testing.T) {
	root := newCheckout(t)
	writeOwnStack(t, root, harnessStack+"  - name: lib\n    source: {path: ./lib}\n")
	writeAppModule(t, filepath.Join(root, "harness"))
	writeManifest(t, filepath.Join(root, "lib"), "lib")
	writeFile(t, filepath.Join(root, "lib", "skills", "build", "SKILL.md"), "---\nname: build\ndescription: Builds the app.\n---\n\nSteps.\n")
	for _, dir := range []string{filepath.Join(root, "harness", "skills", "deploy"), filepath.Join(root, "lib", "skills", "build")} {
		for i := range 5_000 {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	out, err := run(t, "harness", "compose", "--home", tempDir(t))
	if err == nil || !strings.Contains(out+err.Error(), "Move a module out of the checkout, or ship fewer files. claude/plugin contains more than 10,000 files, directories and links, the most a walled Claude Code run takes.") {
		t.Errorf("compose: %v\n%s", err, out)
	}
}

// TestCopiedModuleRows is what a person sees of a module the home contains copies of: a
// row of the compose saying why, the module's row of inspect and of a dry run marked
// copied, and "copied": true in the report. A home inside the checkout shows none of it.
func TestCopiedModuleRows(t *testing.T) {
	root := newCheckout(t)
	writeOwnStack(t, root, harnessStack)
	writeAppModule(t, filepath.Join(root, "harness"))
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "app with its module")
	homes := tempDir(t)
	out, err := run(t, "harness", "compose", "--home", homes)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "copied", "./harness  (module app lies in the checkout, which a walled run can write, so the home contains copies; an edit reaches it on the next compose)")
	out, err = run(t, "harness", "inspect", "--home", filepath.Join(homes, checkout.Key(root)))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "app  ./harness  working-tree  copied\n")
	out, err = run(t, "harness", "compose", "--dry-run", "--home", homes)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "app  ./harness  working-tree  copied\n")
	data, err := os.ReadFile(filepath.Join(homes, checkout.Key(root)) + "-report.json")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, string(data), `"copied": true`)

	out, err = run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "copied") {
		t.Errorf("a compose into the checkout mentions copies:\n%s", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, ".qory", "harness-report.json")); strings.Contains(string(data), "copied") {
		t.Errorf("the report of a home inside the checkout marks a copy:\n%s", data)
	}
}

// TestCopiedModuleSkipsALinkThatLeadsNowhere is a module in the checkout that ships a
// script link to nothing: the compose into a home outside the checkout goes on, and the
// home has no file at that name.
func TestCopiedModuleSkipsALinkThatLeadsNowhere(t *testing.T) {
	root := newCheckout(t)
	writeOwnStack(t, root, harnessStack)
	writeAppModule(t, filepath.Join(root, "harness"))
	if err := os.MkdirAll(filepath.Join(root, "harness", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkTo(t, "missing.sh", filepath.Join(root, "harness", "scripts", "dangle"))
	homes := tempDir(t)
	if out, err := run(t, "harness", "compose", "--home", homes); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(homes, checkout.Key(root), "modules", "app", "scripts", "dangle")); !os.IsNotExist(err) {
		t.Errorf("the home has modules/app/scripts/dangle: %v", err)
	}
}

// TestModuleAtTheCheckoutRootComposesOutside is a module at source: {path: .}, the
// checkout itself, composed first into the home inside the checkout and then into a home
// outside it: the second compose succeeds, and its copy of the module leaves out .qory,
// which holds the first home.
func TestModuleAtTheCheckoutRootComposesOutside(t *testing.T) {
	root := newCheckout(t)
	writeOwnStack(t, root, strings.Replace(harnessStack, "./harness", ".", 1))
	writeAppModule(t, root)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("inside: %v\n%s", err, out)
	}
	homes := tempDir(t)
	if out, err := run(t, "harness", "compose", "--home", homes); err != nil {
		t.Fatalf("outside: %v\n%s", err, out)
	}
	home := filepath.Join(homes, checkout.Key(root))
	if _, err := os.Lstat(filepath.Join(home, "modules", "app", ".qory")); !os.IsNotExist(err) {
		t.Errorf("the home's copy of the module holds .qory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "modules", "app", "skills", "deploy", "SKILL.md")); err != nil {
		t.Error(err)
	}
}

// TestCopiedModuleRowHidesASecret is a module named qak_m at ./qak_m in the checkout,
// composed into a home outside it: the copied row prints the phrase in place of the name
// and of the path, and the output contains no qak_.
func TestCopiedModuleRowHidesASecret(t *testing.T) {
	root := newCheckout(t)
	writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: claude\nmodules:\n  - name: qak_m\n    source: {path: ./qak_m}\n")
	dir := filepath.Join(root, "qak_m")
	writeManifest(t, dir, "qak_m")
	writeFile(t, filepath.Join(dir, "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Deploys the app.\n---\n\nSteps.\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "app with its module")
	out, err := run(t, "harness", "compose", "--home", tempDir(t))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	const name = "(a name that contains an access key secret)"
	wantsRow(t, out, "copied", "./"+name+"  (module "+name+" lies in the checkout, which a walled run can write, so the home contains copies; an edit reaches it on the next compose)")
	if strings.Contains(strings.ToLower(out), "qak_") {
		t.Errorf("the compose prints qak_:\n%s", out)
	}
}
