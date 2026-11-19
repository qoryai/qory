package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/render"
)

// TestCodexRefusesReservedKeys is a Codex fragment that sets each key Forager's launch
// or qory sets for a run: the compose's build fails with the line that names the key, and
// writes no config.toml. hooks.Stop, the other keys of feedback and another
// experimental_realtime_ key stay allowed.
func TestCodexRefusesReservedKeys(t *testing.T) {
	for _, c := range []struct{ fragment, key string }{
		{"model_provider = \"other\"\n", "model_provider"},
		{"[model_providers.other]\nbase_url = \"https://example.com/v1\"\n", "model_providers"},
		{"[projects.\"/work\"]\ntrust_level = \"trusted\"\n", "projects"},
		{"[hooks.state.\"/x/config.toml:stop:0:0\"]\ntrusted_hash = \"sha256:00\"\n", "hooks.state"},
		{"[features]\nplugins = true\n", "features"},
		{"[analytics]\nenabled = true\n", "analytics"},
		{"[otel]\nexporter = \"none\"\n", "otel"},
		{"notify = [\"/bin/true\"]\n", "notify"},
		{"web_search = \"live\"\n", "web_search"},
		{"[feedback]\nenabled = true\n", "feedback.enabled"},
		{"check_for_update_on_startup = true\n", "check_for_update_on_startup"},
		{"openai_base_url = \"https://example.com/v1\"\n", "openai_base_url"},
		{"chatgpt_base_url = \"https://example.com/\"\n", "chatgpt_base_url"},
		{"experimental_realtime_ws_base_url = \"wss://example.com\"\n", "experimental_realtime_ws_base_url"},
		{"experimental_realtime_webrtc_call_base_url = \"https://example.com\"\n", "experimental_realtime_webrtc_call_base_url"},
		{"responses_api_metadata = { team = \"x\" }\n", "responses_api_metadata"},
		{"profile = \"fast\"\n", "profile"},
		{"[profiles.fast]\nmodel = \"m\"\n", "profiles"},
		{"cli_auth_credentials_store = \"file\"\n", "cli_auth_credentials_store"},
		{"allow_login_shell = true\n", "allow_login_shell"},
		{"model_instructions_file = \"/x.md\"\n", "model_instructions_file"},
		{"experimental_compact_prompt_file = \"/x.md\"\n", "experimental_compact_prompt_file"},
		{"model_catalog_json = \"/x.json\"\n", "model_catalog_json"},
		{"js_repl_node_path = \"/usr/bin/node\"\n", "js_repl_node_path"},
		{"sqlite_home = \"/x\"\n", "sqlite_home"},
		{"log_dir = \"/x\"\n", "log_dir"},
	} {
		t.Run(c.key, func(t *testing.T) {
			res, home := composeEnv(t, withFile("modules/core/settings/codex/config.toml", c.fragment), nil, "codex")
			err := render.Build(res, home, lookup(t, "codex"))
			want := "Remove `" + c.key + "` from `settings/codex/config.toml`.\n  A Codex run takes this key from qory and Forager, not from the harness."
			if err == nil || err.Error() != want {
				t.Fatalf("Build: %v, want %q", err, want)
			}
			if _, err := os.Stat(filepath.Join(home, "codex", "config.toml")); err == nil {
				t.Error("the refused compose wrote config.toml")
			}
		})
	}
	allowed := "experimental_realtime_ws_model = \"m\"\n[feedback]\nother = true\n[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\n"
	res, home := composeEnv(t, withFile("modules/core/settings/codex/config.toml", allowed), nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatalf("Build with feedback.other and hooks.Stop: %v", err)
	}
	config := read(t, filepath.Join(home, "codex", "config.toml"))
	has(t, "codex config.toml", config, "other = true", `command = "/bin/true"`)
}

// TestCodexRefusesABadHookNumber is a Codex config.toml or hooks.json fragment with a
// hook whose timeout or additionalContextLimit Codex cannot read: the compose's build
// fails with the line that names the key, the event and the file, and writes no
// config.toml. A float such as 2.0 counts as fractional in both files. A timeout and an
// additionalContextLimit of 0, and a bad number on a hook type that has no such key, stay
// allowed.
func TestCodexRefusesABadHookNumber(t *testing.T) {
	const command = "[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\n"
	for _, c := range []struct{ name, file, fragment, key string }{
		{"negative timeout", "config.toml", "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\n\n" + command + "timeout = 5\n[[hooks.SessionStart.hooks]]\ntype = \"mcp_tool\"\nserver = \"s\"\ntool = \"t\"\ntimeout = -1\n", "timeout"},
		{"fractional timeout", "config.toml", command + "timeout = 1.5\n", "timeout"},
		{"float timeout", "config.toml", command + "timeout = 2.0\n", "timeout"},
		{"negative limit", "config.toml", command + "additionalContextLimit = -1\n", "additionalContextLimit"},
		{"fractional limit", "config.toml", command + "additionalContextLimit = 0.5\n", "additionalContextLimit"},
		{"json timeout", "hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "/bin/true", "timeout": -1.5}]}]}}`, "timeout"},
		{"json negative", "hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "/bin/true", "additionalContextLimit": -1}]}]}}`, "additionalContextLimit"},
		{"json whole float", "hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "/bin/true", "timeout": 2.0}]}]}}`, "timeout"},
		{"json exponent", "hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "mcp_tool", "server": "s", "tool": "t", "timeout": 1e1}]}]}}`, "timeout"},
		{"json limit", "hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "/bin/true", "additionalContextLimit": 2.5}]}]}}`, "additionalContextLimit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, home := composeEnv(t, withFile("modules/core/settings/codex/"+c.file, c.fragment), nil, "codex")
			err := render.Build(res, home, lookup(t, "codex"))
			want := "Set `" + c.key + "` of the `SessionStart` hook to a whole number of 0 or more.\n  The hook is in the harness's `settings/codex/" + c.file + "`, and Codex ignores the whole file otherwise."
			if err == nil || err.Error() != want {
				t.Fatalf("Build: %v, want %q", err, want)
			}
			if _, err := os.Stat(filepath.Join(home, "codex", "config.toml")); err == nil {
				t.Error("the refused compose wrote config.toml")
			}
		})
	}
	allowed := command + "timeout = 0\nadditionalContextLimit = 0\n[[hooks.SessionStart.hooks]]\ntype = \"mcp_tool\"\nserver = \"s\"\ntool = \"t\"\nadditionalContextLimit = -1\n"
	res, home := composeEnv(t, withFile("modules/core/settings/codex/config.toml", allowed), nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatalf("Build with a timeout and a limit of 0: %v", err)
	}
	res, home = composeEnv(t, withFile("modules/core/settings/codex/hooks.json", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "/bin/true", "timeout": 0, "additionalContextLimit": 0}]}]}}`), nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatalf("Build with whole numbers in hooks.json: %v", err)
	}
}

// TestCodexStopsOnAHookCodexCannotRead is a Codex config.toml or hooks.json fragment with a
// hook that Codex 0.169.0 cannot decode, after a good one: the compose's build fails with the
// line that names the event and the hook's place, the fix first, and writes no config.toml.
func TestCodexStopsOnAHookCodexCannotRead(t *testing.T) {
	for _, c := range []struct{ name, file, fragment, want string }{
		{"config.toml", "config.toml", "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\n[[hooks.Stop.hooks]]\ntype = \"mcp_tool\"\nserver = \"s\"\n",
			"Fix `hooks.Stop[0].hooks[1]` in `settings/codex/config.toml` of module core.\n  It has no `tool` text, and Codex reads no hook of a file with such a hook."},
		{"hooks.json", "hooks.json", `{"hooks": {"PreToolUse": [{"hooks": [{"type": "command", "command": "/bin/true"}]}, {"matcher": "*", "hooks": [{"type": "shell", "command": "/bin/true"}]}]}}`,
			"Fix `hooks.PreToolUse[1].hooks[0]` in `settings/codex/hooks.json` of module core.\n  Its `type` is none of command, mcp_tool, prompt and agent, and Codex reads no hook of a file with such a hook."},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, home := composeEnv(t, withFile("modules/core/settings/codex/"+c.file, c.fragment), nil, "codex")
			err := render.Build(res, home, lookup(t, "codex"))
			if err == nil || err.Error() != c.want {
				t.Fatalf("Build: %v, want %q", err, c.want)
			}
			if _, err := os.Stat(filepath.Join(home, "codex", "config.toml")); err == nil {
				t.Error("the failed compose wrote config.toml")
			}
		})
	}
}

// twoHookModules is envStack with a second module, named name, after core: core's
// hooks.json has two Stop groups that Codex reads, and the second module's file has
// fragment.
func twoHookModules(name, file, fragment string) map[string]string {
	files := withFile("modules/core/settings/codex/hooks.json", `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "/bin/a"}]}, {"hooks": [{"type": "command", "command": "/bin/b"}]}]}}`)
	files["qory.yaml"] = strings.Replace(files["qory.yaml"], "      source: {path: modules/core}\n", "      source: {path: modules/core}\n    - name: "+name+"\n      source: {path: modules/second}\n", 1)
	files["modules/second/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: " + name + "\n"
	files["modules/second/settings/codex/"+file] = fragment
	return files
}

// TestCodexNamesTheModuleOfABadHook is a harness of two modules whose second module has a
// Stop hook that Codex cannot read, after core's two Stop groups: the compose stops with the
// hook's place in the second module's file, group 0 and not group 2 of the merged hooks, and
// the module's name, in the fix line, or, for a name too long for it, on the second line.
func TestCodexNamesTheModuleOfABadHook(t *testing.T) {
	long := strings.Repeat("m", 40)
	for _, c := range []struct{ name, module, file, fragment, want string }{
		{"hooks.json", "second", "hooks.json", `{"hooks": {"Stop": [{"hooks": [{"type": "command"}]}]}}`,
			"Fix `hooks.Stop[0].hooks[0]` in `settings/codex/hooks.json` of module second.\n  It has no `command` text, and Codex reads no hook of a file with such a hook."},
		{"config.toml", "second", "config.toml", "[[hooks.Stop]]\nmatcher = 1\n",
			"Fix `hooks.Stop[0]` in `settings/codex/config.toml` of module second.\n  Its `matcher` is no text, and Codex reads no hook of a file with such a group."},
		{"a long name", long, "hooks.json", `{"hooks": {"Stop": [{"hooks": [{"type": "command"}]}]}}`,
			"Fix `hooks.Stop[0].hooks[0]` in `settings/codex/hooks.json`.\n  The file is in module " + long + ". It has no `command` text, and Codex reads no hook of a file with such a hook."},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, home := composeEnv(t, twoHookModules(c.module, c.file, c.fragment), nil, "codex")
			err := render.Build(res, home, lookup(t, "codex"))
			if err == nil || err.Error() != c.want {
				t.Fatalf("Build: %v, want %q", err, c.want)
			}
			if line, _, _ := strings.Cut(err.Error(), "\n"); utf8.RuneCountInString("qory run: "+line) > 100 {
				t.Errorf("line 1 has more than 100 characters after qory run: %q", line)
			}
		})
	}
}
