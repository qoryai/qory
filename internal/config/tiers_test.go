package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// isolated is [hermetic] with the cache and state directories under the temporary home
// too, so a test reads and writes nothing of the machine it runs on.
func isolated(t *testing.T) string {
	t.Helper()
	home := hermetic(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return home
}

// TestTiersOfTheMachineAndOfTheRepository reads harness.tiers in its two forms: in the
// user's file and an ancestor's as the machine's map, the nearer file per tier, and in
// the checkout root's file as the repository's map, with or without extends. Each entry
// records the file it came from.
func TestTiersOfTheMachineAndOfTheRepository(t *testing.T) {
	home := isolated(t)
	user := filepath.Join(home, ".config", "qory", "qory.yaml")
	write(t, user, "harness:\n  tiers:\n    light: claude-haiku-5-5\n    fast: {model: claude-sonnet-5-5, effort: low}\n")
	base := t.TempDir()
	root := filepath.Join(base, "code", "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(base, "code", "qory.yaml")
	write(t, ancestor, "harness:\n  tiers:\n    fast: claude-haiku-5-5\n")
	own := filepath.Join(root, "qory.yaml")
	write(t, own, "harness:\n  tiers:\n    light: claude-opus-5-5\n    thinking: {model: claude-opus-5-5, effort: max}\n")
	machine := map[string]stack.Tier{
		"light": {Model: "claude-haiku-5-5", From: user},
		"fast":  {Model: "claude-haiku-5-5", From: ancestor},
	}
	repo := map[string]stack.Tier{
		"light":    {Model: "claude-opus-5-5", From: own},
		"thinking": {Model: "claude-opus-5-5", Effort: "max", From: own},
	}
	for _, readOwn := range []bool{true, false} {
		c, err := config.Load(root, readOwn)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(c.Tiers, machine) {
			t.Errorf("own %v: machine tiers %+v, want %+v", readOwn, c.Tiers, machine)
		}
		if !reflect.DeepEqual(c.RepoTiers, repo) {
			t.Errorf("own %v: repository tiers %+v, want %+v", readOwn, c.RepoTiers, repo)
		}
		if len(c.Ignored) != 0 {
			t.Errorf("own %v: ignored %v; harness.tiers is read under extends", readOwn, c.Ignored)
		}
		rows := map[string][2]string{}
		for _, r := range c.Rows() {
			rows[r.Key] = [2]string{r.Value, r.Origin}
		}
		want := map[string][2]string{
			"harness.tiers.light":    {"claude-haiku-5-5", user},
			"harness.tiers.fast":     {"claude-haiku-5-5", ancestor},
			"harness.tiers.thinking": {"claude-opus-5-5, effort max", own},
		}
		for key, w := range want {
			if rows[key] != w {
				t.Errorf("own %v: row %s = %v, want %v", readOwn, key, rows[key], w)
			}
		}
	}
}

// TestTiersRefuseAMistake refuses a name that is not a tier, an entry of another shape,
// an empty model, and a model that is a tier placeholder, each with the file and the key.
func TestTiersRefuseAMistake(t *testing.T) {
	isolated(t)
	root := t.TempDir()
	path := filepath.Join(root, "qory.yaml")
	for body, want := range map[string]string{
		"harness:\n  tiers:\n    heavy: claude-haiku-5-5\n":                       "harness.tiers.heavy: use one of the tiers light, fast and thinking; heavy is not a tier",
		"harness:\n  tiers:\n    \"he\\eavy\": a\n":                               `harness.tiers.he\x1bavy: use one of the tiers light, fast and thinking; he\x1bavy is not a tier`,
		"harness:\n  tiers:\n    light: [a, b]\n":                                 "harness.tiers.light: write a model id, or {model: <model id>, effort: <value>}",
		"harness:\n  tiers:\n    light: 5\n":                                      "harness.tiers.light: write a model id, or {model: <model id>, effort: <value>}",
		"harness:\n  tiers:\n    light: \"\"\n":                                   "harness.tiers.light: write a model id, or {model: <model id>, effort: <value>}",
		"harness:\n  tiers:\n    light: {effort: max}\n":                          "harness.tiers.light: write a model id, or {model: <model id>, effort: <value>}",
		"harness:\n  tiers:\n    light: {model: a, effort: max, extra: 1}\n":      "harness.tiers.light: write a model id, or {model: <model id>, effort: <value>}",
		"harness:\n  tiers:\n    light: {model: a, effort: \"\"}\n":               "harness.tiers.light: write a model id, or {model: <model id>, effort: <value>}",
		"harness:\n  tiers:\n    light: ${qory:fast}\n":                           "harness.tiers.light: write a model id; ${qory:fast} is a tier placeholder, which goes only in an agent's model",
		"harness:\n  tiers:\n    light: {model: \"${qory:fast}\", effort: max}\n": "harness.tiers.light: write a model id; ${qory:fast} is a tier placeholder, which goes only in an agent's model",
	} {
		write(t, path, body)
		_, err := config.Load(root, true)
		if err == nil || err.Error() != path+": "+want {
			t.Errorf("%q:\nerr = %v\nwant %s: %s", body, err, path, want)
		}
	}
}
