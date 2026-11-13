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
// run's trust, a hooks.json with no hook, an empty .env and environments.toml, the
// compose's AGENTS.md, and the compose's skills beside an empty rules and agents, in a
// fixed order, and no auth.json.
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
	for _, name := range []string{".env", "environments.toml"} {
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
