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

[[skills.config]]
  name = "visuals"
  enabled = true
`

// TestWalledConfigIsGolden is the config.toml a walled run binds, for a copy of the
// checkout: the compose's keys as they were, the login kept in memory only, the login
// shell, the shell snapshot, goals and memories off, each project path untrusted, a
// trusted_hash for each Stop hook, keyed by config.toml in the home in the wall, the
// skills Codex ships and a cloud's skills off, and a disabling entry for each skill name,
// then for each skill path, after the compose's entry.
func TestWalledConfigIsGolden(t *testing.T) {
	got, err := codex.WalledConfig([]byte(composed), codex.HomeInWall, []string{"/cache/qory/runs/r1/work", "/workspace"}, []string{"deploying", "plugin:review", "review"}, []string{"/workspace/.agents/skills/review/SKILL.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := `allow_login_shell = false
cli_auth_credentials_store = "ephemeral"
model = "gpt-6.1-sol"

[cloud]
  [cloud.skills]
    enabled = false

[features]
  goals = false
  memories = false
  shell_snapshot = false

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
    [hooks.state."/var/lib/qory-codex-home/config.toml:stop:0:0"]
      trusted_hash = "` + endCheckHash + `"
    [hooks.state."/var/lib/qory-codex-home/config.toml:stop:0:1"]
      trusted_hash = "` + trueHash + `"

[mcp_servers]
  [mcp_servers.db]
    command = "/srv/harness/modules/core/scripts/db.py"

[projects]
  [projects."/cache/qory/runs/r1/work"]
    trust_level = "untrusted"
  [projects."/workspace"]
    trust_level = "untrusted"

[skills]
  [skills.bundled]
    enabled = false

  [[skills.config]]
    enabled = true
    name = "visuals"

  [[skills.config]]
    enabled = false
    name = "deploying"

  [[skills.config]]
    enabled = false
    name = "plugin:review"

  [[skills.config]]
    enabled = false
    name = "review"

  [[skills.config]]
    enabled = false
    path = "/workspace/.agents/skills/review/SKILL.md"
`
	if string(got) != want {
		t.Errorf("config.toml:\n%s\nwant:\n%s", got, want)
	}
}

// TestWalledConfigHashesAsTrustHook is a hook in each place of the key: the hash is the
// one trust-hook.py computes for the same command, whatever the group, the handler, the
// matcher of an event that takes none, or an async false written out. The hashes of
// /bin/false and of the MCP tool hook come from the same JSON in Python, with no
// matcher, since Stop takes none:
//
//	json.dumps(identity, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
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
		"/var/lib/qory-codex-home/config.toml:stop:0:0": "sha256:5cde819ee8866303693164854d02d8bfc2d76a0b837bb47a63777a72243e20db",
		"/var/lib/qory-codex-home/config.toml:stop:1:0": "sha256:a32fadf67dd9ba6b779757c3db73589e436c56976457f459c05f7e453a1f589f",
		"/var/lib/qory-codex-home/config.toml:stop:1:1": endCheckHash,
	}
	if len(state) != len(want) {
		t.Fatalf("hooks.state %v, want the keys of %v", state, want)
	}
	for key, hash := range want {
		if got, ok := state[key]; !ok || got != hash {
			t.Errorf("%s: %q, want %s", key, got, hash)
		}
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

// TestWalledConfigTrustsNoProjectAndAddsNoState is a config.toml with no hooks, no
// project path and no skill to disable: neither hooks.state nor projects is written, and
// no skills.config entry, but the skills Codex ships and a cloud's skills are off.
func TestWalledConfigTrustsNoProjectAndAddsNoState(t *testing.T) {
	got, err := codex.WalledConfig([]byte("model = \"m\"\n"), codex.HomeInWall, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "allow_login_shell = false\ncli_auth_credentials_store = \"ephemeral\"\nmodel = \"m\"\n\n[cloud]\n  [cloud.skills]\n    enabled = false\n\n[features]\n  goals = false\n  memories = false\n  shell_snapshot = false\n\n[skills]\n  [skills.bundled]\n    enabled = false\n"; string(got) != want {
		t.Errorf("config.toml %q", got)
	}
}

// TestWalledConfigTrustsNoLineSeparator is a hook with U+2028 or U+2029 in its command,
// statusMessage or matcher: encoding/json escapes them and serde_json does not, so the
// hook gets no trust. A command with the text \u2028, a backslash and five letters, gets
// its hash, from the same JSON in Python as TestWalledConfigNormalizesAsCodex.
func TestWalledConfigTrustsNoLineSeparator(t *testing.T) {
	config := `[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "echo \u2028"
[[hooks.Stop.hooks]]
type = "command"
command = "/bin/true"
statusMessage = "a\u2029b"
[[hooks.Stop.hooks]]
type = "command"
command = 'echo \u2028'
[[hooks.PreToolUse]]
matcher = "Bash\u2028"
[[hooks.PreToolUse.hooks]]
type = "command"
command = "/bin/true"
`
	want := map[string]string{
		codex.HomeInWall + "/config.toml:stop:0:2": "sha256:62952c1af1eb1d3c580409562f1776a3da6cc48eefdc6cb6a3b04528df5d043b",
	}
	if got := walledState(t, config, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("hooks.state:\n%v\nwant:\n%v", got, want)
	}
}

// TestWalledConfigHashesMCPToolHooks is an MCP tool hook with a matcher, a nested input,
// a timeout and a statusMessage, and an Interrupt one with Codex's default timeout of 1:
// each gets the hash that Codex 0.167.2's app-server listed for it with hooks/list, with
// status trusted. The same JSON in Python gives both, with each integer of the input
// written as {"$serde_json::private::Number": "<n>"}. A SessionEnd hook and one with an
// empty server, which Codex skips, and one whose input contains a float, whose JSON qory
// cannot write as Codex does, get none.
func TestWalledConfigHashesMCPToolHooks(t *testing.T) {
	config := `[[hooks.PostToolUse]]
matcher = "Bash"
[[hooks.PostToolUse.hooks]]
type = "mcp_tool"
server = "db"
tool = "log"
timeout = 30
statusMessage = "checking"
input = { path = "/x", depth = 2, flags = [true, "a"], nested = { b = 1, a = "z" } }
[[hooks.Interrupt]]
[[hooks.Interrupt.hooks]]
type = "mcp_tool"
server = "db"
tool = "stop"
[[hooks.SessionEnd]]
[[hooks.SessionEnd.hooks]]
type = "mcp_tool"
server = "db"
tool = "end"
[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "mcp_tool"
server = " "
tool = "t"
[[hooks.Stop.hooks]]
type = "mcp_tool"
server = "db"
tool = "t"
input = { ratio = 0.5 }
`
	key := codex.HomeInWall + "/config.toml:"
	want := map[string]string{
		key + "post_tool_use:0:0": "sha256:93c4a017141461f73ceaab7222ee5dcf8ae0918f3ab5a0bbf99e1c4cf192d00a",
		key + "interrupt:0:0":     "sha256:88ed5e40feedce2304ca60f03d0d19e063250ed610c100d054e81510b9e2633c",
	}
	if got := walledState(t, config, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("hooks.state:\n%v\nwant:\n%v", got, want)
	}
}

// walledState is the hooks.state table of [codex.WalledConfig] for config, key to hash.
func walledState(t *testing.T, config string, projects []string) map[string]string {
	t.Helper()
	got, err := codex.WalledConfig([]byte(config), codex.HomeInWall, projects, nil, nil)
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
// and a copy of the compose's skills beside an empty rules and agents, in a fixed order,
// each at its path under the home in the wall, and no auth.json. Every source is in the
// folder WriteWalledHome writes, none in the composed home. The copy keeps a script's
// executable bit, and a relative link of the skills is a regular file with the text of
// the file it led to.
func TestWriteWalledHome(t *testing.T) {
	dir, files := t.TempDir(), filepath.Join(t.TempDir(), "fixed")
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
	write("skills/review/run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "skills/review/run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("review/SKILL.md", filepath.Join(dir, "skills", "linked.md")); err != nil {
		t.Fatal(err)
	}
	binds, err := codex.WriteWalledHome(dir, files, codex.WalledRun{Projects: []string{"/workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	var want []codex.Bound
	for _, name := range []string{"config.toml", "hooks.json", ".env", "environments.toml", "AGENTS.override.md", "AGENTS.md", "rules", "skills", "agents"} {
		want = append(want, codex.Bound{Path: name, Source: filepath.Join(files, name), Target: "/var/lib/qory-codex-home/" + name, Dir: !strings.Contains(name, ".")})
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
	if got := read("skills/review/SKILL.md"); got != "skill\n" {
		t.Errorf("skills/review/SKILL.md %q", got)
	}
	if info, err := os.Stat(filepath.Join(files, "skills/review/run.sh")); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("skills/review/run.sh: %v, want an executable file", err)
	}
	if info, err := os.Lstat(filepath.Join(files, "skills", "linked.md")); err != nil || !info.Mode().IsRegular() || read("skills/linked.md") != "skill\n" {
		t.Errorf("skills/linked.md: %v, want a regular file with the text of the SKILL.md it linked to", err)
	}
	for _, name := range []string{"rules", "agents"} {
		if entries, err := os.ReadDir(filepath.Join(files, name)); err != nil || len(entries) > 0 {
			t.Errorf("%s: %v %v, want an empty folder", name, entries, err)
		}
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

// TestWriteWalledHomeReadsNoPlantedLogin is a composed home with an auth.json and a
// cloud-config-bundle-cache.json, as an agent could write them in a writable Codex home:
// the walled home has neither, and its config.toml keeps the login in memory only, so
// Codex reads no auth.json and, without a login, no cache.
func TestWriteWalledHomeReadsNoPlantedLogin(t *testing.T) {
	dir, files := t.TempDir(), filepath.Join(t.TempDir(), "fixed")
	for name, content := range map[string]string{
		"auth.json":                      `{"OPENAI_API_KEY": "sk-planted"}`,
		"cloud-config-bundle-cache.json": `{"signature": "x"}`,
		"config.toml":                    "model = \"m\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := codex.WriteWalledHome(dir, files, codex.WalledRun{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "cloud-config-bundle-cache.json"} {
		if _, err := os.Lstat(filepath.Join(files, name)); err == nil {
			t.Errorf("the walled home has %s", name)
		}
	}
	var config struct {
		Store string `toml:"cli_auth_credentials_store"`
	}
	if _, err := toml.DecodeFile(filepath.Join(files, "config.toml"), &config); err != nil {
		t.Fatal(err)
	}
	if config.Store != "ephemeral" {
		t.Errorf("cli_auth_credentials_store %q, want ephemeral", config.Store)
	}
}

// TestWriteWalledHomeWritesThroughNoLink is a files folder that exists, with a link at
// hooks.json to a file elsewhere: WriteWalledHome fails when it makes the folder, before
// it writes any file, and the file the link leads to is unchanged. writeNew and copyFile
// refuse a link at the path they write, see their tests in walled_internal_test.go.
func TestWriteWalledHomeWritesThroughNoLink(t *testing.T) {
	dir, files, outside := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(files, "hooks.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := codex.WriteWalledHome(dir, files, codex.WalledRun{}); err == nil {
		t.Fatal("WriteWalledHome wrote through a link")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "theirs\n" {
		t.Errorf("the link's target: %q %v, want it unchanged", b, err)
	}
}

// TestWriteWalledHomeLeavesNoCwdTrusted is a working folder under each path where the
// wall shows the checkout, for a checkout that is a repository's root, one that is a
// folder inside a repository, one that is a worktree, one that is a submodule, and one
// whose .git is a link to a git folder, which Codex follows. Codex looks up the trust of
// the working folder, then of its git root, and trusts the working folder for the
// process when neither has an entry; every lookup here finds untrusted. The run's copy
// is at the same path in the wall as on this machine, so its git root is too;
// /workspace shows the checkout alone, so a cwd under it has /workspace as its git root
// only when the checkout is the repository's root. For a worktree and a submodule,
// Codex's lookup of the git root finds none, so the working folder's entry, from
// WorkDirs, decides.
func TestWriteWalledHomeLeavesNoCwdTrusted(t *testing.T) {
	mkdir := func(p string) {
		t.Helper()
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile := func(p, content string) {
		t.Helper()
		mkdir(filepath.Dir(p))
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	writeFile(filepath.Join(repo, ".git/HEAD"), "ref: refs/heads/main\n")
	mkdir(filepath.Join(repo, "pkg/sub"))
	worktree := filepath.Join(base, "work")
	writeFile(filepath.Join(repo, ".git/worktrees/work/HEAD"), "ref: refs/heads/w\n")
	writeFile(filepath.Join(worktree, ".git"), "gitdir: "+filepath.Join(repo, ".git/worktrees/work")+"\n")
	mkdir(filepath.Join(worktree, "sub"))
	linked := filepath.Join(base, "linked")
	writeFile(filepath.Join(base, "gitdata/HEAD"), "ref: refs/heads/main\n")
	mkdir(filepath.Join(linked, "sub"))
	if err := os.Symlink(filepath.Join(base, "gitdata"), filepath.Join(linked, ".git")); err != nil {
		t.Fatal(err)
	}
	submodule := filepath.Join(repo, "lib")
	writeFile(filepath.Join(repo, ".git/modules/lib/HEAD"), "ref: refs/heads/main\n")
	writeFile(filepath.Join(submodule, ".git"), "gitdir: ../.git/modules/lib\n")
	mkdir(filepath.Join(submodule, "sub"))
	for _, c := range []struct {
		name, checkout string
		// workDirs are the run's working folder in the wall, at each path.
		workDirs []string
		// lookups are cwd and git root pairs as Codex sees them in the wall.
		lookups [][2]string
	}{
		{"the repository's root", repo, nil, [][2]string{
			{filepath.Join(repo, "sub"), repo},
			{"/workspace/sub", "/workspace"},
			{"/workspace", "/workspace"},
		}},
		{"a folder of a repository", filepath.Join(repo, "pkg"), nil, [][2]string{
			{filepath.Join(repo, "pkg/sub"), repo},
			{"/workspace", ""},
		}},
		{"a worktree", worktree, []string{filepath.Join(worktree, "sub"), "/workspace/sub"}, [][2]string{
			{filepath.Join(worktree, "sub"), ""},
			{"/workspace/sub", ""},
		}},
		{"a submodule", submodule, []string{filepath.Join(submodule, "sub"), "/workspace/sub"}, [][2]string{
			{filepath.Join(submodule, "sub"), ""},
			{"/workspace/sub", ""},
		}},
		{"a .git link to a folder", linked, nil, [][2]string{
			{filepath.Join(linked, "sub"), linked},
			{"/workspace/sub", "/workspace"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := filepath.Join(t.TempDir(), "fixed")
			run := codex.WalledRun{Projects: []string{c.checkout, "/workspace"}, Checkouts: []string{c.checkout}, WorkDirs: c.workDirs}
			if _, err := codex.WriteWalledHome(t.TempDir(), files, run); err != nil {
				t.Fatal(err)
			}
			var config struct {
				Projects map[string]struct {
					TrustLevel string `toml:"trust_level"`
				}
			}
			if _, err := toml.DecodeFile(filepath.Join(files, "config.toml"), &config); err != nil {
				t.Fatal(err)
			}
			for _, l := range c.lookups {
				level := config.Projects[l[0]].TrustLevel
				if level == "" && l[1] != "" {
					level = config.Projects[l[1]].TrustLevel
				}
				if level != "untrusted" {
					t.Errorf("cwd %s, git root %q: trust %q, want untrusted; projects %v", l[0], l[1], level, config.Projects)
				}
			}
		})
	}
}

// TestWriteWalledHomeCopiesALinkedSkillFile is a composed home whose skills/review is a
// folder with SKILL.md a link to a module's file, as qory places a skill: the walled
// home has a regular SKILL.md with the module's text when the module's folder is a link
// root, and WriteWalledHome fails, naming the link, when it is not.
func TestWriteWalledHomeCopiesALinkedSkillFile(t *testing.T) {
	dir, module := filepath.Join(t.TempDir(), "home", "codex"), t.TempDir()
	src := filepath.Join(module, "skills", "review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("module text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "skills", "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, filepath.Join(dir, "skills", "review", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	files := filepath.Join(t.TempDir(), "fixed")
	if _, err := codex.WriteWalledHome(dir, files, codex.WalledRun{LinkRoots: []string{module}}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(files, "skills", "review", "SKILL.md")
	if info, err := os.Lstat(p); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("%s: %v, want a regular file", p, err)
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "module text\n" {
		t.Errorf("SKILL.md %q %v, want the module's text", b, err)
	}
	_, err := codex.WriteWalledHome(dir, filepath.Join(t.TempDir(), "fixed"), codex.WalledRun{})
	if err == nil || !strings.HasPrefix(err.Error(), "Remove the link `skills/review/SKILL.md` from the harness.\n  It leads to `") {
		t.Errorf("WriteWalledHome with no link root: %v, want the refusal of the link", err)
	}
}
