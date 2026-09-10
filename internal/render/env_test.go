package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestExportedVariablesReachTheRuntimes is a compose exporting HARNESS_HOME: Claude Code
// gets it under env in settings.json beside QORY_HARNESS_HOME, rendered for the home, and
// Codex under shell_environment_policy.set in config.toml.
func TestExportedVariablesReachTheRuntimes(t *testing.T) {
	res, _, home := composeFixture(t, "claude", "codex")
	res.Env = map[string]string{"HARNESS_HOME": "$QORY_HARNESS_HOME/layers/core"}
	if err := render.Build(res, home, lookup(t, "claude"), lookup(t, "codex")); err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(filepath.Join(home, "claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"HARNESS_HOME": "` + home + `/layers/core"`, `"QORY_HARNESS_HOME": "` + home + `"`} {
		if !strings.Contains(string(settings), want) {
			t.Errorf("claude settings.json lacks %s:\n%s", want, settings)
		}
	}
	config, err := os.ReadFile(filepath.Join(home, "codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[shell_environment_policy.set]", `HARNESS_HOME = "` + home + `/layers/core"`, `QORY_HARNESS_HOME = "` + home + `"`} {
		if !strings.Contains(string(config), want) {
			t.Errorf("codex config.toml lacks %s:\n%s", want, config)
		}
	}
}
