package codex_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/render/codex"
)

// endCheck is a Stop hook command with a quote, a variable, HTML characters and a letter
// outside ASCII, which the hash takes as written.
const endCheck = `/srv/harness/modules/core/hooks/end-check.sh --run "$QORY_RUN_ID" <a&b> é`

// Both hashes come from the probe's trust-hook.py, which a Codex 0.167.2 run accepted
// (probe 521, item 10g), for the same command and timeout:
//
//	python3 -I trust-hook.py config.toml "<command>" <timeout>
const (
	endCheckHash = "sha256:63c3f3b6340d10c653f0da3f6fb82f539ed270e6a4e24cfc4bab96de20d4ec3a" // endCheck, 30
	trueHash     = "sha256:fef68b1a8a8090019e0421bcd64cfac1f7b7c21980e2f35d49b46427395a6dda" // /bin/true, 600
)

// composed is a config.toml as the compose renders it: the model, a server and the
// harness's Stop hook, one with a timeout and one without.
const composed = `model = "gpt-6.1-sol"

[[hooks.Stop]]

  [[hooks.Stop.hooks]]
    command = '/srv/harness/modules/core/hooks/end-check.sh --run "$QORY_RUN_ID" <a&b> é'
    timeout = 30
    type = "command"

  [[hooks.Stop.hooks]]
    command = "/bin/true"
    type = "command"

[mcp_servers.db]
  command = "/srv/harness/modules/core/scripts/db.py"
`

// TestWalledConfigIsGolden is the config.toml a walled run binds, for a copy of the
// checkout: the compose's keys as they were, each project path untrusted, and a
// trusted_hash for each Stop hook, keyed by config.toml in the home in the wall.
func TestWalledConfigIsGolden(t *testing.T) {
	got, err := codex.WalledConfig([]byte(composed), codex.HomeInWall, []string{"/cache/qory/runs/r1/work", "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	want := `model = "gpt-6.1-sol"

[hooks]

  [[hooks.Stop]]

    [[hooks.Stop.hooks]]
      command = "/srv/harness/modules/core/hooks/end-check.sh --run \"$QORY_RUN_ID\" <a&b> é"
      timeout = 30
      type = "command"

    [[hooks.Stop.hooks]]
      command = "/bin/true"
      type = "command"
  [hooks.state]
    [hooks.state."/home/agent/.codex/config.toml:stop:0:0"]
      trusted_hash = "` + endCheckHash + `"
    [hooks.state."/home/agent/.codex/config.toml:stop:0:1"]
      trusted_hash = "` + trueHash + `"

[mcp_servers]
  [mcp_servers.db]
    command = "/srv/harness/modules/core/scripts/db.py"

[projects]
  [projects."/cache/qory/runs/r1/work"]
    trust_level = "untrusted"
  [projects."/workspace"]
    trust_level = "untrusted"
`
	if string(got) != want {
		t.Errorf("config.toml:\n%s\nwant:\n%s", got, want)
	}
}

// TestWalledConfigHashesAsTrustHook is a hook in each place of the key: the hash is the
// one trust-hook.py computes for the same command, whatever the group, the handler, the
// matcher of an event that takes none, or an async false written out.
func TestWalledConfigHashesAsTrustHook(t *testing.T) {
	config := `[[hooks.Stop]]
matcher = "ignored"
[[hooks.Stop.hooks]]
type = "command"
command = "/bin/false"
[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "mcp_tool"
server = "s"
tool = "t"
[[hooks.Stop.hooks]]
type = "command"
command = '` + endCheck + `'
timeout = 30
async = false
`
	state := walledState(t, config, nil)
	want := map[string]string{
		"/home/agent/.codex/config.toml:stop:0:0": "",
		"/home/agent/.codex/config.toml:stop:1:1": endCheckHash,
	}
	if len(state) != len(want) {
		t.Fatalf("hooks.state %v, want the keys of %v", state, want)
	}
	for key, hash := range want {
		if _, ok := state[key]; !ok {
			t.Errorf("hooks.state has no %s: %v", key, state)
		}
		if hash != "" && state[key] != hash {
			t.Errorf("%s: %s, want %s", key, state[key], hash)
		}
	}
	if _, ok := state["/home/agent/.codex/config.toml:stop:1:0"]; ok {
		t.Error("the MCP tool hook got a trusted_hash")
	}
}

// TestWalledConfigNormalizesAsCodex is a hook of each rule Codex 0.167.2 normalizes before
// it hashes (codex-rs/hooks/src/engine/discovery.rs, append_matcher_groups): a matcher
// where the event takes one, statusMessage, additionalContextLimit only on the events
// that keep it and not at its default 2500, a timeout raised to 1, a SessionEnd timeout
// held at 3, an Interrupt timeout of 1 by default, and a command with a tab, a control
// character and a backslash. A command of only white space gets no key. The control
// characters' hash comes from trust-hook.py; the others from the same JSON in Python:
//
//	json.dumps(identity, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
func TestWalledConfigNormalizesAsCodex(t *testing.T) {
	config := `[[hooks.PreToolUse]]
matcher = "Bash"
[[hooks.PreToolUse.hooks]]
type = "command"
command = "/bin/check"
timeout = 0
statusMessage = "checking"
additionalContextLimit = 100
[[hooks.SessionEnd]]
[[hooks.SessionEnd.hooks]]
type = "command"
command = "/bin/end"
timeout = 9
async = true
[[hooks.Interrupt]]
[[hooks.Interrupt.hooks]]
type = "command"
command = "/bin/int"
additionalContextLimit = 50
[[hooks.PostToolUse]]
[[hooks.PostToolUse.hooks]]
type = "command"
command = "/bin/post"
additionalContextLimit = 2500
[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "  "
[[hooks.Stop.hooks]]
type = "command"
command = "a\tb\u0001c\\d"
timeout = 30
`
	key := codex.HomeInWall + "/config.toml:"
	want := map[string]string{
		key + "pre_tool_use:0:0":  "sha256:500e7100a547b86e63104adc7a4ed0ea1662017fc62f326050956607867b7f21",
		key + "session_end:0:0":   "sha256:40705d49d9cd1a41fb51fba7346276ec5196e12b6a4c0204513c68e7adac7d30",
		key + "interrupt:0:0":     "sha256:a7d100ff9d5d584786b571d0b65be0dc8b9b8b95c2a2ac755cd581ea196a4e39",
		key + "post_tool_use:0:0": "sha256:7bd471240b678be053e62ae1ed64763490b210f38aeafd6d216b94728b72a9c9",
		key + "stop:0:1":          "sha256:9db311ec503d58207150226ca9891ab7c8971e4a8b7f60713b2dba3303d816f2",
	}
	if got := walledState(t, config, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("hooks.state:\n%v\nwant:\n%v", got, want)
	}
}

// TestWalledConfigTrustsNoProjectAndAddsNoState is a config.toml with no hooks and no
// project path: neither table is written.
func TestWalledConfigTrustsNoProjectAndAddsNoState(t *testing.T) {
	got, err := codex.WalledConfig([]byte("model = \"m\"\n"), codex.HomeInWall, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "model = \"m\"\n" {
		t.Errorf("config.toml %q", got)
	}
}

// walledState is the hooks.state table of [codex.WalledConfig] for config, key to hash.
func walledState(t *testing.T, config string, projects []string) map[string]string {
	t.Helper()
	got, err := codex.WalledConfig([]byte(config), codex.HomeInWall, projects)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Hooks struct {
			State map[string]struct {
				TrustedHash string `toml:"trusted_hash"`
			} `toml:"state"`
		} `toml:"hooks"`
	}
	if _, err := toml.Decode(string(got), &m); err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	out := map[string]string{}
	for k, v := range m.Hooks.State {
		out[k] = v.TrustedHash
	}
	return out
}

// TestWriteWalledHome is the read-only part of a walled run's home: config.toml with the
// run's trust, a hooks.json with no hook, an empty .env, environments.toml and
// AGENTS.override.md, which Codex passes over for AGENTS.md, the compose's AGENTS.md,
// and the compose's skills beside an empty rules and agents, in a fixed order, and no
// auth.json.
func TestWriteWalledHome(t *testing.T) {
	dir, files := t.TempDir(), t.TempDir()
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("config.toml", composed)
	write("AGENTS.md", "instructions\n")
	write("skills/review/SKILL.md", "skill\n")
	binds, err := codex.WriteWalledHome(dir, files, []string{"/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	want := []codex.Bound{
		{Path: "config.toml", Source: filepath.Join(files, "config.toml")},
		{Path: "hooks.json", Source: filepath.Join(files, "hooks.json")},
		{Path: ".env", Source: filepath.Join(files, ".env")},
		{Path: "environments.toml", Source: filepath.Join(files, "environments.toml")},
		{Path: "AGENTS.override.md", Source: filepath.Join(files, "AGENTS.override.md")},
		{Path: "AGENTS.md", Source: filepath.Join(files, "AGENTS.md")},
		{Path: "rules", Source: filepath.Join(files, "rules"), Dir: true},
		{Path: "skills", Source: filepath.Join(dir, "skills"), Dir: true},
		{Path: "agents", Source: filepath.Join(files, "agents"), Dir: true},
	}
	if !reflect.DeepEqual(binds, want) {
		t.Errorf("binds:\n%+v\nwant:\n%+v", binds, want)
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(files, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read("hooks.json"); got != "{\"hooks\": {}}\n" {
		t.Errorf("hooks.json %q", got)
	}
	for _, name := range []string{".env", "environments.toml", "AGENTS.override.md"} {
		if got := read(name); got != "" {
			t.Errorf("%s %q, want empty", name, got)
		}
	}
	if got := read("AGENTS.md"); got != "instructions\n" {
		t.Errorf("AGENTS.md %q", got)
	}
	if got := read("config.toml"); !strings.Contains(got, `[projects."/workspace"]`) || !strings.Contains(got, endCheckHash) {
		t.Errorf("config.toml lacks the trust or the hook's hash:\n%s", got)
	}
	for _, d := range []string{dir, files} {
		if _, err := os.Lstat(filepath.Join(d, "auth.json")); err == nil {
			t.Errorf("%s has an auth.json", d)
		}
	}
}
