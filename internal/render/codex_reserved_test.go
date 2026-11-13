package render_test

import (
	"os"
	"path/filepath"
	"testing"

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
			want := "Remove `" + c.key + "` from the harness's `settings/codex/config.toml`: a Codex run takes this key from qory and Forager, not from the harness."
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

// TestCodexRefusesANegativeHookTimeout is a Codex config.toml or hooks.json fragment with
// a hook whose timeout is negative: the compose's build fails with the line that names
// the event and the file, and writes no config.toml. A timeout of 0, which Codex reads as
// 1 second, stays allowed.
func TestCodexRefusesANegativeHookTimeout(t *testing.T) {
	for _, c := range []struct{ file, fragment string }{
		{"config.toml", "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\n\n[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\ntimeout = 5\n[[hooks.SessionStart.hooks]]\ntype = \"mcp_tool\"\nserver = \"s\"\ntool = \"t\"\ntimeout = -1\n"},
		{"hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "/bin/true", "timeout": -1.5}]}]}}`},
	} {
		t.Run(c.file, func(t *testing.T) {
			res, home := composeEnv(t, withFile("modules/core/settings/codex/"+c.file, c.fragment), nil, "codex")
			err := render.Build(res, home, lookup(t, "codex"))
			want := "Set `timeout` of the `SessionStart` hook in the harness's `settings/codex/" + c.file + "` to 1 or more: Codex ignores the whole file when one is negative."
			if err == nil || err.Error() != want {
				t.Fatalf("Build: %v, want %q", err, want)
			}
			if _, err := os.Stat(filepath.Join(home, "codex", "config.toml")); err == nil {
				t.Error("the refused compose wrote config.toml")
			}
		})
	}
	res, home := composeEnv(t, withFile("modules/core/settings/codex/config.toml", "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\ntimeout = 0\n"), nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatalf("Build with a timeout of 0: %v", err)
	}
}
