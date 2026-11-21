package codex_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/render/codex"
)

// endCheck is a Stop hook command with a quote, a variable, HTML characters and a letter
// outside ASCII, which the hash takes as written.
const endCheck = `/srv/harness/modules/core/hooks/end-check.sh --run "$QORY_RUN_ID" <a&b> é`

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
// checkout: the compose's keys as they were, without the hooks, which the run binds in
// /etc/codex, the login kept in memory only, the login shell, the shell snapshot, goals,
// memories and a shell with a terminal off, each project path untrusted, the skills Codex
// ships and a cloud's skills off, and a disabling entry for each skill name, then for
// each skill path, after the compose's entry.
func TestWalledConfigIsGolden(t *testing.T) {
	got, err := codex.WalledConfig([]byte(composed), []string{"/cache/qory/runs/r1/work", "/workspace"}, []string{"deploying", "plugin:review", "review"}, []string{"/workspace/.agents/skills/review/SKILL.md"})
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
  unified_exec_tty = false

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

// TestWalledConfigTrustsNoProjectAndAddsNoState is a config.toml with a hooks.state
// entry, no project path and no skill to disable: neither hooks nor projects is written,
// and no skills.config entry, but the skills Codex ships and a cloud's skills are off.
func TestWalledConfigTrustsNoProjectAndAddsNoState(t *testing.T) {
	config := "model = \"m\"\n[hooks.state.\"/x/config.toml:stop:0:0\"]\nenabled = false\n"
	got, err := codex.WalledConfig([]byte(config), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "allow_login_shell = false\ncli_auth_credentials_store = \"ephemeral\"\nmodel = \"m\"\n\n[cloud]\n  [cloud.skills]\n    enabled = false\n\n[features]\n  goals = false\n  memories = false\n  shell_snapshot = false\n  unified_exec_tty = false\n\n[skills]\n  [skills.bundled]\n    enabled = false\n"; string(got) != want {
		t.Errorf("config.toml %q", got)
	}
}

// TestWalledConfigTurnsOffTheSystemSkills is a composed config.toml that turns bundled
// skills on, and one with no skills table: the walled config.toml sets
// skills.bundled.enabled = false in both, the setting that keeps Codex from installing its
// system skills into the run's writable home.
func TestWalledConfigTurnsOffTheSystemSkills(t *testing.T) {
	for _, config := range []string{"[skills.bundled]\nenabled = true\n", "model = \"m\"\n"} {
		got, err := codex.WalledConfig([]byte(config), nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Skills struct {
				Bundled struct {
					Enabled *bool `toml:"enabled"`
				} `toml:"bundled"`
			} `toml:"skills"`
		}
		if _, err := toml.Decode(string(got), &m); err != nil {
			t.Fatal(err)
		}
		if e := m.Skills.Bundled.Enabled; e == nil || *e {
			t.Errorf("from %q, skills.bundled.enabled is %v, want false:\n%s", config, e, got)
		}
	}
}

// TestWalledHooksStopsAHookCodexCannotRead is a composed hooks.json or config.toml with one
// group or hook that Codex 0.170.0 cannot decode, after a good group: WalledHooks fails with
// the line that names the event, the group's index and the hook's index, the fix first, and
// writes nothing.
func TestWalledHooksStopsAHookCodexCannotRead(t *testing.T) {
	good := `{"hooks": [{"type": "command", "command": "/bin/guard"}]}`
	for _, c := range []struct{ name, file, hooks, place, cause string }{
		{"no command", "hooks.json", `{"type": "command"}`, "PreToolUse[1].hooks[0]", "It has no `command` text"},
		{"null command", "hooks.json", `{"type": "command", "command": null}`, "PreToolUse[1].hooks[0]", "It has no `command` text"},
		{"command no text", "hooks.json", `{"type": "command", "command": ["/bin/x"]}`, "PreToolUse[1].hooks[0]", "Its `command` is no text"},
		{"no type", "hooks.json", `{"command": "/bin/x"}`, "PreToolUse[1].hooks[0]", "Its `type` is none of command, mcp_tool, prompt and agent"},
		{"other type", "hooks.json", `{"type": "script", "command": "/bin/x"}`, "PreToolUse[1].hooks[0]", "Its `type` is none of command, mcp_tool, prompt and agent"},
		{"hook no table", "hooks.json", `"/bin/x"`, "PreToolUse[1].hooks[0]", "It is no table of keys"},
		{"text timeout", "hooks.json", `{"type": "command", "command": "/bin/x", "timeout": "30"}`, "PreToolUse[1].hooks[0]", "Its `timeout` is no whole number of 0 or more"},
		{"negative timeout", "hooks.json", `{"type": "command", "command": "/bin/x", "timeout": -1}`, "PreToolUse[1].hooks[0]", "Its `timeout` is no whole number of 0 or more"},
		{"async text", "hooks.json", `{"type": "command", "command": "/bin/x", "async": "yes"}`, "PreToolUse[1].hooks[0]", "Its `async` is neither true nor false"},
		{"async null", "hooks.json", `{"type": "command", "command": "/bin/x", "async": null}`, "PreToolUse[1].hooks[0]", "Its `async` is neither true nor false"},
		{"status no text", "hooks.json", `{"type": "command", "command": "/bin/x", "statusMessage": 1}`, "PreToolUse[1].hooks[0]", "Its `statusMessage` is no text"},
		{"both windows", "hooks.json", `{"type": "command", "command": "/bin/x", "commandWindows": "a", "command_windows": "b"}`, "PreToolUse[1].hooks[0]", "It sets both `commandWindows` and `command_windows`"},
		{"no server", "hooks.json", `{"type": "mcp_tool", "tool": "t"}`, "PreToolUse[1].hooks[0]", "It has no `server` text"},
		{"no tool", "hooks.json", `{"type": "mcp_tool", "server": "s"}`, "PreToolUse[1].hooks[0]", "It has no `tool` text"},
		{"input list", "hooks.json", `{"type": "mcp_tool", "server": "s", "tool": "t", "input": [1]}`, "PreToolUse[1].hooks[0]", "Its `input` is no table of keys"},
		{"input null", "hooks.json", `{"type": "mcp_tool", "server": "s", "tool": "t", "input": {"a": {"b": null}}}`, "PreToolUse[1].hooks[0]", "Its `input` contains a null or a whole number above 2^63-1"},
		{"input large", "hooks.json", `{"type": "mcp_tool", "server": "s", "tool": "t", "input": {"a": 9223372036854775808}}`, "PreToolUse[1].hooks[0]", "Its `input` contains a null or a whole number above 2^63-1"},
		{"second hook", "hooks.json", `{"type": "prompt"}, {"type": "command", "command": 7}`, "PreToolUse[1].hooks[1]", "Its `command` is no text"},
	} {
		t.Run(c.name, func(t *testing.T) {
			hooksJSON := `{"hooks": {"Stop": [` + good + `], "PreToolUse": [` + good + `, {"matcher": "*", "hooks": [` + c.hooks + `]}]}}`
			got, err := codex.WalledHooks([]byte(hooksJSON), nil)
			want := "Fix `hooks." + c.place + "` in `settings/codex/hooks.json`.\n  " + c.cause + ", and Codex reads no hook of a file with such a hook."
			if err == nil || err.Error() != want || got != nil {
				t.Errorf("%q, %v, want %q", got, err, want)
			}
		})
	}
	for _, c := range []struct{ name, hooksJSON, config, want string }{
		{"group no table", `{"hooks": {"Stop": ["/bin/x"]}}`, "",
			"Fix `hooks.Stop[0]` in `settings/codex/hooks.json`.\n  It is no table of keys, and Codex reads no hook of a file with such a group."},
		{"matcher no text", `{"hooks": {"Stop": [{"matcher": 1, "hooks": []}]}}`, "",
			"Fix `hooks.Stop[0]` in `settings/codex/hooks.json`.\n  Its `matcher` is no text, and Codex reads no hook of a file with such a group."},
		{"hooks no list", `{"hooks": {"Stop": [{"hooks": null}]}}`, "",
			"Fix `hooks.Stop[0]` in `settings/codex/hooks.json`.\n  Its `hooks` is no list, and Codex reads no hook of a file with such a group."},
		{"config.toml", "", "[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\n[[hooks.SessionStart.hooks]]\ntype = \"command\"\n",
			"Fix `hooks.SessionStart[0].hooks[1]` in `settings/codex/config.toml`.\n  It has no `command` text, and Codex reads no hook of a file with such a hook."},
		{"config.toml timeout", "", "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\ntimeout = \"5\"\n",
			"Fix `hooks.Stop[0].hooks[0]` in `settings/codex/config.toml`.\n  Its `timeout` is no whole number of 0 or more, and Codex reads no hook of a file with such a hook."},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := codex.WalledHooks([]byte(c.hooksJSON), []byte(c.config))
			if err == nil || err.Error() != c.want || got != nil {
				t.Errorf("%q, %v, want %q", got, err, c.want)
			}
		})
	}
}

// TestWalledHooksTakesWhatCodexReads is a composed hooks.json with each type of hook, keys
// Codex does not know, optional keys set to null, a matcher of null, a group with no hooks
// and an event Codex does not know, whose value is a list of anything: WalledHooks passes
// it, as Codex 0.170.0 decodes it.
func TestWalledHooksTakesWhatCodexReads(t *testing.T) {
	hooksJSON := `{"hooks": {"PreToolUse": [{"matcher": null, "other": 1, "hooks": [
		{"type": "command", "command": "/bin/x", "timeout": 0, "async": true, "statusMessage": null, "commandWindows": "x.cmd", "additionalContextLimit": 2500, "extra": [null]},
		{"type": "mcp_tool", "server": "s", "tool": "t", "input": {"a": [1, 2.5, "x", {"b": true}], "c": -9223372036854775808}, "timeout": null},
		{"type": "prompt", "prompt": "anything"},
		{"type": "agent"}]}, {"matcher": "Bash"}],
		"SomeLaterEvent": [1, null]}}`
	if _, err := codex.WalledHooks([]byte(hooksJSON), nil); err != nil {
		t.Fatal(err)
	}
}

// TestTheLineForABadHookFits is the line for a hook of the longest event name Codex reads,
// with indexes of three digits, in the longer settings file: at most 100 characters after
// "qory run: ".
func TestTheLineForABadHookFits(t *testing.T) {
	groups := strings.Repeat(`{"hooks": []}, `, 100) + `{"hooks": [` + strings.Repeat(`{"type": "agent"}, `, 100) + `{"type": "command"}]}`
	_, err := codex.WalledHooks(nil, []byte("[hooks]\nPermissionRequest = ["+strings.NewReplacer(`"hooks": `, "hooks = ", `"type": `, "type = ").Replace(groups)+"]\n"))
	if err == nil {
		t.Fatal("no error")
	}
	line, _, _ := strings.Cut(err.Error(), "\n")
	if want := "Fix `hooks.PermissionRequest[100].hooks[100]` in `settings/codex/config.toml`."; line != want {
		t.Errorf("line 1 is %q, want %q", line, want)
	}
	if n := utf8.RuneCountInString("qory run: " + line); n > 100 {
		t.Errorf("line 1 has %d characters after qory run:, want at most 100", n)
	}
}

// TestWalledHooksIsGolden is the hooks.json a walled run binds in /etc/codex, from a
// composed hooks.json with a description, a PreToolUse group and a Stop group, and the
// config.toml [composed] with two Stop hooks and a hooks.state entry: the key hooks
// alone, each event's groups of hooks.json first, then those of config.toml, numbers and
// text as written, and no state.
func TestWalledHooksIsGolden(t *testing.T) {
	hooksJSON := `{"description": "d", "hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "/bin/guard", "timeout": 120}]}], "Stop": [{"hooks": [{"type": "command", "command": "/bin/first"}]}]}}`
	config := composed + "[hooks.state.\"/x/config.toml:stop:0:0\"]\nenabled = false\n"
	got, err := codex.WalledHooks([]byte(hooksJSON), []byte(config))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "hooks": {
    "PreToolUse": [
      {
        "hooks": [
          {
            "command": "/bin/guard",
            "timeout": 120,
            "type": "command"
          }
        ],
        "matcher": "*"
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "command": "/bin/first",
            "type": "command"
          }
        ]
      },
      {
        "hooks": [
          {
            "command": "` + strings.ReplaceAll(endCheck, `"`, `\"`) + `",
            "timeout": 30,
            "type": "command"
          },
          {
            "command": "/bin/true",
            "type": "command"
          }
        ]
      }
    ]
  }
}
`
	if string(got) != want {
		t.Errorf("hooks.json:\n%s\nwant:\n%s", got, want)
	}
}

// TestWalledHooksWithNoneIsEmpty is a composed home with neither a hooks.json nor hooks
// in config.toml: the hooks.json has an empty hooks table, which Codex reads as no hook.
func TestWalledHooksWithNoneIsEmpty(t *testing.T) {
	got, err := codex.WalledHooks(nil, []byte("model = \"m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"hooks\": {}\n}\n"; string(got) != want {
		t.Errorf("hooks.json %q, want %q", got, want)
	}
}

// TestWalledHooksRefusesAnEventThatIsNoList is an event of hooks.json, and one of
// config.toml, whose value is no list of groups: Codex would read no hook of the file,
// so WalledHooks fails with the fix first.
func TestWalledHooksRefusesAnEventThatIsNoList(t *testing.T) {
	for _, c := range []struct{ hooksJSON, config, file string }{
		{`{"hooks": {"Stop": {"hooks": []}}}`, "", "hooks.json"},
		{"", "[hooks]\nStop = 1\n", "config.toml"},
	} {
		_, err := codex.WalledHooks([]byte(c.hooksJSON), []byte(c.config))
		if want := "Write each event of the hooks in `settings/codex/" + c.file + "` as a list of hook groups.\n  The event Stop is no list, and Codex reads no hook of such a file."; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", c.file, err, want)
		}
	}
}

// TestWriteWalledHome is the read-only part of a walled run's home: config.toml with the
// run's trust and no hooks, a hooks.json with no hook, an empty .env, environments.toml and
// AGENTS.override.md, which Codex passes over for AGENTS.md, the compose's AGENTS.md,
// and a copy of the compose's skills beside an empty rules and agents, in a fixed order,
// each at its path under the home in the wall, and no auth.json. Every source is in the
// folder WriteWalledHome writes, none in the composed home. The copy keeps a script's
// executable bit, and a relative link of the skills is a regular file with the text of
// the file it led to. Last is the folder etc-codex, at /etc/codex, mode 0755, with the
// compose's hooks in hooks.json, mode 0644, and no other file.
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
	want = append(want, codex.Bound{Path: "etc-codex", Source: filepath.Join(files, "etc-codex"), Target: "/etc/codex", Dir: true})
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
	if got := read("config.toml"); !strings.Contains(got, `[projects."/workspace"]`) || strings.Contains(got, "hooks") {
		t.Errorf("config.toml lacks the trust or has hooks:\n%s", got)
	}
	if got := read("etc-codex/hooks.json"); !strings.Contains(got, `"command": "`+strings.ReplaceAll(endCheck, `"`, `\"`)+`"`) || strings.Contains(got, "trusted_hash") {
		t.Errorf("etc-codex/hooks.json lacks the Stop hook:\n%s", got)
	}
	for name, mode := range map[string]os.FileMode{"etc-codex": 0o755 | os.ModeDir, "etc-codex/hooks.json": 0o644} {
		if info, err := os.Lstat(filepath.Join(files, name)); err != nil || info.Mode() != mode {
			t.Errorf("%s: %v, %v, want mode %v", name, info.Mode(), err, mode)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(files, "etc-codex")); err != nil || len(entries) != 1 {
		t.Errorf("etc-codex: %v, %v, want hooks.json alone", entries, err)
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
