package render_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestCodexRefusesReservedKeys is a Codex fragment that sets each key Forager's launch
// or qory sets for a run: the compose's build fails with the line that names the key, and
// writes no config.toml. hooks.Stop and web_search stay allowed.
func TestCodexRefusesReservedKeys(t *testing.T) {
	const fromKey, everyRun = "qory writes it from the run's model key.", "qory sets it for every Codex run."
	for _, c := range []struct{ fragment, key, why string }{
		{"model_provider = \"other\"\n", "model_provider", fromKey},
		{"[model_providers.other]\nbase_url = \"https://example.com/v1\"\n", "model_providers", fromKey},
		{"[projects.\"/work\"]\ntrust_level = \"trusted\"\n", "projects", everyRun},
		{"[hooks.state.\"/x/config.toml:stop:0:0\"]\ntrusted_hash = \"sha256:00\"\n", "hooks.state", everyRun},
		{"[features]\nplugins = true\n", "features", everyRun},
		{"[analytics]\nenabled = true\n", "analytics", everyRun},
		{"[otel]\nexporter = \"none\"\n", "otel", everyRun},
		{"notify = [\"/bin/true\"]\n", "notify", everyRun},
	} {
		t.Run(c.key, func(t *testing.T) {
			res, home := composeEnv(t, withFile("modules/core/settings/codex/config.toml", c.fragment), nil, "codex")
			err := render.Build(res, home, lookup(t, "codex"))
			want := "Remove `" + c.key + "` from the harness's `settings/codex/config.toml`: " + c.why
			if err == nil || err.Error() != want {
				t.Fatalf("Build: %v, want %q", err, want)
			}
			if _, err := os.Stat(filepath.Join(home, "codex", "config.toml")); err == nil {
				t.Error("the refused compose wrote config.toml")
			}
		})
	}
	allowed := "web_search = \"live\"\n[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = \"/bin/true\"\n"
	res, home := composeEnv(t, withFile("modules/core/settings/codex/config.toml", allowed), nil, "codex")
	if err := render.Build(res, home, lookup(t, "codex")); err != nil {
		t.Fatalf("Build with web_search and hooks.Stop: %v", err)
	}
	config := read(t, filepath.Join(home, "codex", "config.toml"))
	has(t, "codex config.toml", config, `web_search = "live"`, `command = "/bin/true"`)
}
