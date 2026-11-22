package codex_test

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/render/codex"
	"github.com/qoryai/qory/internal/stack"
)

// roleFiles composes a module whose agents are the given files, by name and whole text,
// builds it for codex, and returns the text of each agents/<name>.toml.
func roleFiles(t *testing.T, agents map[string]string) map[string]string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tmp, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	src := filepath.Join(t.TempDir(), "src")
	mod := filepath.Join(src, "modules", "example")
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: example\n      source: {path: modules/example}\n")
	write(filepath.Join(mod, "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: example\n")
	for name, text := range agents {
		write(filepath.Join(mod, "agents", name+".md"), text)
	}
	p, err := config.Compose(filepath.Join(src, "qory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Runtimes = stack.Runtimes{"codex"}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := render.Build(res, home, rt); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for name := range agents {
		data, err := os.ReadFile(filepath.Join(home, "codex", "agents", name+".toml"))
		if err != nil {
			t.Fatal(err)
		}
		got[name] = string(data)
	}
	return got
}

func agent(tools string) string {
	front := "---\nname: role\ndescription: A role.\n" + tools + "---\n\nDo the work.\n"
	return front
}

// TestARoleWithoutAShellToolGetsNoShell composes agents that list tools without a shell,
// with a shell, and with no tools key. Only the first kind gets [features] shell_tool =
// false; a Bash rule such as Bash(git:*) and PowerShell count as a shell.
func TestARoleWithoutAShellToolGetsNoShell(t *testing.T) {
	cases := []struct {
		name  string
		tools string
		off   bool
	}{
		{"no-shell-list", "tools: [Read, Grep]\n", true},
		{"no-shell-string", "tools: Read, Grep\n", true},
		{"empty-list", "tools: []\n", true},
		{"bash-list", "tools: [Read, Bash]\n", false},
		{"bash-string", "tools: Read, Bash\n", false},
		{"bash-rule", "tools: [Read, \"Bash(git:*)\"]\n", false},
		{"bash-rule-space", "tools: [Read, \"Bash (git *)\"]\n", false},
		{"bash-padded-string", "tools: Read,  Bash  ,Grep\n", false},
		{"bash-padded-list", "tools: [Read, \" Bash \"]\n", false},
		{"powershell", "tools: [PowerShell]\n", false},
		{"no-tools-key", "", false},
		{"null-tools", "tools: ~\n", false},
		{"empty-tools-value", "tools:\n", false},
	}
	agents := map[string]string{}
	for _, c := range cases {
		agents[c.name] = agent(c.tools)
	}
	got := roleFiles(t, agents)
	for _, c := range cases {
		text := got[c.name]
		has := strings.Contains(text, "[features]\n  shell_tool = false\n")
		if has != c.off {
			t.Errorf("%s: shell_tool = false present is %v, want %v:\n%s", c.name, has, c.off, text)
		}
		if !strings.Contains(text, "developer_instructions") {
			t.Errorf("%s: no developer_instructions:\n%s", c.name, text)
		}
	}
}

// hookSettings are a module's Codex hooks in both files: in hooks.json, a PreToolUse hook
// with a matcher, a timeout, a status message and a context limit, and a Stop hook with a
// matcher and a context limit, which Codex leaves out of a Stop hook's hash; in
// config.toml, a PreModelRequest hook, a PreToolUse group with an mcp_tool hook before an
// async command hook, and a SessionEnd hook whose timeout Codex limits to 3. hooks.json
// also has a PostToolUse hook with the default context limit, 2500, which Codex leaves
// out of the hash, and a PreToolUse group with no matcher, whose hash has none;
// config.toml also has a SessionEnd hook and an Interrupt hook with no timeout, which
// default to 1, and a UserPromptSubmit hook with timeout 0, which Codex raises to 1.
var hookSettings = map[string]string{
	"settings/codex/hooks.json": `{"hooks": {
  "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "$QORY_HARNESS_HOME/hooks/guard.sh", "timeout": 10, "statusMessage": "Checking", "additionalContextLimit": 100}]},
    {"hooks": [{"type": "command", "command": "echo any tool"}]}],
  "Stop": [{"matcher": "ignored", "hooks": [{"type": "command", "command": "echo stop", "additionalContextLimit": 100}]}],
  "PostToolUse": [{"matcher": "Edit", "hooks": [{"type": "command", "command": "echo post", "additionalContextLimit": 2500}]}]
}}
`,
	"settings/codex/config.toml": `[[hooks.PreModelRequest]]
[[hooks.PreModelRequest.hooks]]
type = "command"
command = "echo model"
timeout = 30

[[hooks.PreToolUse]]
matcher = "*"
[[hooks.PreToolUse.hooks]]
type = "mcp_tool"
server = "files"
tool = "check"
[[hooks.PreToolUse.hooks]]
type = "command"
command = "echo tool"
async = true

[[hooks.SessionEnd]]
[[hooks.SessionEnd.hooks]]
type = "command"
command = "echo end"
timeout = 10

[[hooks.SessionEnd]]
[[hooks.SessionEnd.hooks]]
type = "command"
command = "echo end again"

[[hooks.Interrupt]]
[[hooks.Interrupt.hooks]]
type = "command"
command = "echo interrupt"

[[hooks.UserPromptSubmit]]
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "echo prompt"
timeout = 0
`,
}

// composeModule composes a module with the files given, by path in the module and whole
// text, for codex, and returns the result.
func composeModule(t *testing.T, files map[string]string) *compose.Result {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tmp, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	src := filepath.Join(t.TempDir(), "src")
	files = maps.Clone(files)
	files["qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: example\n"
	for name, text := range files {
		path := filepath.Join(src, "modules", "example", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stackFile := filepath.Join(src, "qory.yaml")
	if err := os.WriteFile(stackFile, []byte("apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: example\n      source: {path: modules/example}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := config.Compose(stackFile)
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Runtimes = stack.Runtimes{"codex"}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestConfigTrustsEachCommandHook builds a home reached through a link, for a module with
// [hookSettings]. config.toml has features.unified_exec_tty = false, web_search =
// "disabled" at the top level, and in hooks.state a
// trusted_hash for each command hook, keyed by the file with the link resolved, the
// event's snake-case label and the hook's places. Each hash is the SHA-256 of the
// identity JSON written out here, as Codex computes it. The mcp_tool hook gets no key,
// keeps its place, and is the one hook that UntrustedHooks lists. A second build, for a
// home below the link in a folder that does not exist yet, keys each hook by the link's
// target and the rest of the path.
func TestConfigTrustsEachCommandHook(t *testing.T) {
	res := composeModule(t, hookSettings)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "link", ".qory", "harness")
	if err := os.Mkdir(filepath.Join(base, "real", ".qory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := render.Build(res, home, rt); err != nil {
		t.Fatal(err)
	}
	// want are the keys and hashes for a home at h, resolved to codex: each hash is that of
	// the identity written out here, the command as the home's hooks.json has it.
	want := func(h, codex string) map[string]string {
		identities := map[string]string{
			"hooks.json:pre_tool_use:0:0":        `{"event_name":"pre_tool_use","hooks":[{"additionalContextLimit":100,"async":false,"command":"` + h + `/hooks/guard.sh","statusMessage":"Checking","timeout":10,"type":"command"}],"matcher":"Bash"}`,
			"hooks.json:stop:0:0":                `{"event_name":"stop","hooks":[{"async":false,"command":"echo stop","timeout":600,"type":"command"}]}`,
			"config.toml:pre_model_request:0:0":  `{"event_name":"pre_model_request","hooks":[{"async":false,"command":"echo model","timeout":30,"type":"command"}]}`,
			"config.toml:pre_tool_use:0:1":       `{"event_name":"pre_tool_use","hooks":[{"async":true,"command":"echo tool","timeout":600,"type":"command"}],"matcher":"*"}`,
			"config.toml:session_end:0:0":        `{"event_name":"session_end","hooks":[{"async":false,"command":"echo end","timeout":3,"type":"command"}]}`,
			"hooks.json:pre_tool_use:1:0":        `{"event_name":"pre_tool_use","hooks":[{"async":false,"command":"echo any tool","timeout":600,"type":"command"}]}`,
			"hooks.json:post_tool_use:0:0":       `{"event_name":"post_tool_use","hooks":[{"async":false,"command":"echo post","timeout":600,"type":"command"}],"matcher":"Edit"}`,
			"config.toml:session_end:1:0":        `{"event_name":"session_end","hooks":[{"async":false,"command":"echo end again","timeout":1,"type":"command"}]}`,
			"config.toml:interrupt:0:0":          `{"event_name":"interrupt","hooks":[{"async":false,"command":"echo interrupt","timeout":1,"type":"command"}]}`,
			"config.toml:user_prompt_submit:0:0": `{"event_name":"user_prompt_submit","hooks":[{"async":false,"command":"echo prompt","timeout":1,"type":"command"}]}`,
		}
		out := map[string]string{}
		for key, identity := range identities {
			sum := sha256.Sum256([]byte(identity))
			out[filepath.Join(codex, key)] = "sha256:" + hex.EncodeToString(sum[:])
		}
		return out
	}
	// The second home's folder new does not exist: it is resolved to the link's target
	// and the rest of the path.
	newHome := filepath.Join(base, "link", "new", "harness")
	stage := filepath.Join(base, "stage")
	if err := render.BuildAt(res, stage, newHome, rt); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, home, codex string }{
		{home, home, filepath.Join(base, "real", ".qory", "harness", "codex")},
		{stage, newHome, filepath.Join(base, "real", "new", "harness", "codex")},
	} {
		var config struct {
			Features  map[string]any `toml:"features"`
			WebSearch string         `toml:"web_search"`
			Hooks     struct {
				State map[string]struct {
					TrustedHash string `toml:"trusted_hash"`
				} `toml:"state"`
			} `toml:"hooks"`
		}
		if _, err := toml.DecodeFile(filepath.Join(c.dir, "codex", "config.toml"), &config); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(config.Features, map[string]any{"unified_exec_tty": false}) {
			t.Errorf("%s: features %v, want unified_exec_tty = false alone", c.home, config.Features)
		}
		if config.WebSearch != "disabled" {
			t.Errorf("%s: web_search %q, want \"disabled\"", c.home, config.WebSearch)
		}
		got := map[string]string{}
		for key, s := range config.Hooks.State {
			got[key] = s.TrustedHash
		}
		if w := want(c.home, c.codex); !reflect.DeepEqual(got, w) {
			t.Errorf("%s: hooks.state %v\nwant %v", c.home, got, w)
		}
	}
	if got, want := codex.UntrustedHooks(res, home), []codex.UntrustedHook{{Hook: "hooks.PreToolUse[0].hooks[0] in config.toml", Reason: "qory writes no trusted hash for an mcp_tool hook"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("UntrustedHooks %q, want %q", got, want)
	}
}

// TestConfigHasHookStateOnlyForHooks checks the hooks table of config.toml: a module with
// hooks in hooks.json alone gets one with the state of its hook, and a module with no hook
// gets none.
func TestConfigHasHookStateOnlyForHooks(t *testing.T) {
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		want  int
	}{
		{"hooks.json alone", map[string]string{"settings/codex/hooks.json": `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "echo stop"}]}]}}`}, 1},
		{"no hook", map[string]string{"settings/codex/config.toml": "approval_policy = \"never\"\n"}, 0},
	} {
		res := composeModule(t, c.files)
		home := filepath.Join(t.TempDir(), "harness")
		if err := render.Build(res, home, rt); err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if _, err := toml.DecodeFile(filepath.Join(home, "codex", "config.toml"), &config); err != nil {
			t.Fatal(err)
		}
		hooks, has := config["hooks"].(map[string]any)
		state, _ := hooks["state"].(map[string]any)
		if len(state) != c.want || has != (c.want > 0) {
			t.Errorf("%s: hooks %v, want %d keys in hooks.state", c.name, config["hooks"], c.want)
		}
	}
}
