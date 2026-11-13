package render_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestCodexRefusesReservedKeys is a Codex fragment that sets each key Forager's launch
// or qory sets for a run: the compose's build fails with the line that names the key, and
// writes no config.toml. hooks.Stop and the other keys of feedback stay allowed.
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
	allowed := "[feedback]\nother = true\n[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\n"
	res, home := composeEnv(t, withFile("modules/core/settings/codex/config.toml", allowed), nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatalf("Build with feedback.other and hooks.Stop: %v", err)
	}
	config := read(t, filepath.Join(home, "codex", "config.toml"))
	has(t, "codex config.toml", config, "other = true", `command = "/bin/true"`)
}
