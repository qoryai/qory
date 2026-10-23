package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/render/claude"
)

// claudeLaunch is Claude Code's launch template resolved against a home that holds the
// files it names, mcp.json only when withMCP says so.
func claudeLaunch(t *testing.T, withMCP bool) (render.Launch, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, claude.Runtime)
	files := []string{"settings.json", "launch/CLAUDE.md", "plugin/.claude-plugin/plugin.json"}
	if withMCP {
		files = append(files, "mcp.json")
	}
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rt, err := render.Lookup(claude.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	l, err := render.LaunchFor(rt, home, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return l, dir
}

// TestADispatchedLaunchLoadsTheHarnessAlone is Claude Code's launch for a dispatched
// run: the template's arguments, --mcp-config with the harness's list, and
// --strict-mcp-config with --setting-sources user last, so the MCP list's flag, which
// takes several words, takes none of the arguments after --. A harness with no MCP list
// gets an empty one, so the run still has no server outside the harness.
func TestADispatchedLaunchLoadsTheHarnessAlone(t *testing.T) {
	l, dir := claudeLaunch(t, true)
	got := dispatchedArgs(claude.Runtime, l.Args)
	want := []string{
		"--plugin-dir", dir + "/plugin",
		"--settings", dir + "/settings.json",
		"--mcp-config", dir + "/mcp.json",
		"--append-system-prompt-file", dir + "/launch/CLAUDE.md",
		"--strict-mcp-config", "--setting-sources", "user",
	}
	if !slices.Equal(got, want) {
		t.Errorf("with the harness's MCP list:\n got %q\nwant %q", got, want)
	}

	l, dir = claudeLaunch(t, false)
	if slices.Contains(l.Args, "--mcp-config") {
		t.Fatalf("the template names mcp.json, which the home does not hold: %q", l.Args)
	}
	got = dispatchedArgs(claude.Runtime, l.Args)
	want = []string{
		"--plugin-dir", dir + "/plugin",
		"--settings", dir + "/settings.json",
		"--append-system-prompt-file", dir + "/launch/CLAUDE.md",
		"--mcp-config", `{"mcpServers":{}}`,
		"--strict-mcp-config", "--setting-sources", "user",
	}
	if !slices.Equal(got, want) {
		t.Errorf("with no MCP list:\n got %q\nwant %q", got, want)
	}
}

// TestADispatchedLaunchKeepsNoOtherSettingSources is a harness.launch whose arguments
// read the checkout's settings, or name the flags already: a dispatched run reads the
// user's alone, with each flag once, and keeps the harness's MCP list in either form.
func TestADispatchedLaunchKeepsNoOtherSettingSources(t *testing.T) {
	tail := []string{"--strict-mcp-config", "--setting-sources", "user"}
	for _, c := range []struct{ args, want []string }{
		{
			[]string{"--settings", "s.json", "--setting-sources", "user,project,local"},
			slices.Concat([]string{"--settings", "s.json", "--mcp-config", emptyMCP}, tail),
		},
		{
			[]string{"--setting-sources=project", "--strict-mcp-config", "--mcp-config=m.json"},
			slices.Concat([]string{"--mcp-config=m.json"}, tail),
		},
		{
			[]string{"--mcp-config", "m.json", "--setting-sources", "user"},
			slices.Concat([]string{"--mcp-config", "m.json"}, tail),
		},
		{
			nil,
			slices.Concat([]string{"--mcp-config", emptyMCP}, tail),
		},
	} {
		got := dispatchedArgs(claude.Runtime, c.args)
		if !slices.Equal(got, c.want) {
			t.Errorf("%q:\n got %q\nwant %q", c.args, got, c.want)
		}
	}
}

// TestADispatchedLaunchOfAnotherRuntimeIsAsGiven is a runtime other than Claude Code:
// its arguments are not Claude Code's, and stay as the launch gives them.
func TestADispatchedLaunchOfAnotherRuntimeIsAsGiven(t *testing.T) {
	args := []string{"--config", "c.toml"}
	if got := dispatchedArgs("codex", args); !slices.Equal(got, args) {
		t.Errorf("got %q, want %q", got, args)
	}
}

// TestTheAgentHomeIsAFixedVariable is CLAUDE_CONFIG_DIR among a dispatched walled run's
// fixed variables, once, at the agent's home, beside the template's, sorted by name, and
// the launch's list of fixed variables unchanged.
func TestTheAgentHomeIsAFixedVariable(t *testing.T) {
	fixed := []string{"CODEX_HOME=/h/codex", "CLAUDE_CONFIG_DIR=/elsewhere"}
	got := withAgentHome(fixed)
	want := []string{"CLAUDE_CONFIG_DIR=/tmp/qory-agent-home", "CODEX_HOME=/h/codex"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if fixed[1] != "CLAUDE_CONFIG_DIR=/elsewhere" {
		t.Error("the launch's list changed")
	}
	if got := withAgentHome(nil); len(got) != 1 || !strings.HasPrefix(got[0], "CLAUDE_CONFIG_DIR=") {
		t.Errorf("with no fixed variable: %q", got)
	}
}

// TestARunKeyReadsNoMachineKeyOfTheCheckout is the checkout's qory.yaml setting
// harness.launch.claude, with --dangerously-skip-permissions, and env: qory run and qory
// run resend given a run key read neither, and the file's keys are listed as ignored;
// without a run key both apply.
func TestARunKeyReadsNoMachineKeyOfTheCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "qory.yaml"), []byte("apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    claude:\n      args: [[--dangerously-skip-permissions]]\nenv:\n  FROM_REPOSITORY: \"1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	_, person, err := locateRun(homeOptions{}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := person.Launch[claude.Runtime]; !ok || !slices.Equal(slices.Concat(l.Args...), []string{"--dangerously-skip-permissions"}) || person.Env["FROM_REPOSITORY"] != "1" {
		t.Errorf("without a run key the checkout's keys are not read: %+v, env %v", person.Launch, person.Env)
	}

	_, keyed, err := locateRun(homeOptions{}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keyed.Launch[claude.Runtime]; ok || keyed.Env["FROM_REPOSITORY"] != "" {
		t.Errorf("with a run key the checkout's keys are read: %+v, env %v", keyed.Launch, keyed.Env)
	}
	if want := []string{"harness.launch", "env"}; !slices.Equal(keyed.Ignored, want) {
		t.Errorf("ignored %q, want %q", keyed.Ignored, want)
	}
}
