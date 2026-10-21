package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSetupTemplatesShowTheTierKey reads both qory.yaml templates setup writes: each parses
// as written, has one commented tiers line, and with that line uncommented has the key
// at harness.tiers with the example entry.
func TestSetupTemplatesShowTheTierKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	for name, doc := range map[string]string{
		"checkoutConfig": fmt.Sprintf(checkoutConfig, "app", column("app", 23), column("app", 19)),
		"userConfig":     userConfig,
	} {
		var plain map[string]any
		if err := yaml.Unmarshal([]byte(doc), &plain); err != nil {
			t.Fatalf("%s does not parse: %v", name, err)
		}
		if n := strings.Count(doc, "\n  #tiers: "); n != 1 {
			t.Fatalf("%s has %d commented tiers lines, want 1", name, n)
		}
		var uncommented struct {
			Harness struct {
				Tiers map[string]string `yaml:"tiers"`
			} `yaml:"harness"`
		}
		if err := yaml.Unmarshal([]byte(strings.Replace(doc, "\n  #tiers: ", "\n  tiers: ", 1)), &uncommented); err != nil {
			t.Fatalf("%s with tiers uncommented does not parse: %v", name, err)
		}
		if got := uncommented.Harness.Tiers["light"]; got != "claude-haiku-5-5" {
			t.Errorf("%s: harness.tiers.light = %q, want claude-haiku-5-5", name, got)
		}
	}
}
