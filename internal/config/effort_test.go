package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// TestEffortIsAMachineKey reads harness.effort like harness.model: the nearer file wins,
// the value is kept exactly as written, and its row contains the file that set it.
func TestEffortIsAMachineKey(t *testing.T) {
	home := isolated(t)
	write(t, filepath.Join(home, ".config", "qory", "qory.yaml"), "harness: {effort: low}\n")
	root := t.TempDir()
	own := filepath.Join(root, "qory.yaml")
	write(t, own, "harness: {effort: xhigh}\n")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Effort != "xhigh" || c.Origin("harness.effort") != own {
		t.Errorf("effort %q from %s", c.Effort, c.Origin("harness.effort"))
	}
	for _, r := range c.Rows() {
		if r.Key == "harness.effort" && (r.Value != "xhigh" || r.Origin != own) {
			t.Errorf("harness.effort row %+v", r)
		}
	}
	if c, err = config.Load(t.TempDir(), true); err != nil || c.Effort != "low" {
		t.Errorf("the user's file: effort %q, %v", c.Effort, err)
	}
}

// TestEffortIsNotSetByAFileUnderExtends leaves harness.effort of the checkout's file out
// of a compose under extends and lists it as ignored, as it does harness.model.
func TestEffortIsNotSetByAFileUnderExtends(t *testing.T) {
	isolated(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "qory.yaml"), "harness: {model: m, effort: high}\n")
	c, err := config.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Effort != "" {
		t.Errorf("effort %q was read", c.Effort)
	}
	if got := strings.Join(c.Ignored, ", "); got != "harness.model, harness.effort" {
		t.Errorf("ignored = %q", got)
	}
}

// TestAnEmptyEffortIsNone reads harness.effort: "" as no effort, as harness.model: ""
// keeps the document's model: the compose gets none, and the row shows (none) and the
// file that set the key.
func TestAnEmptyEffortIsNone(t *testing.T) {
	home := isolated(t)
	user := filepath.Join(home, ".config", "qory", "qory.yaml")
	write(t, user, "harness: {effort: high}\n")
	root := t.TempDir()
	own := filepath.Join(root, "qory.yaml")
	write(t, own, "harness: {effort: \"\"}\n")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Effort != "" {
		t.Errorf("effort %q, want none", c.Effort)
	}
	for _, r := range c.Rows() {
		if r.Key == "harness.effort" && (r.Value != "(none)" || r.Origin != own) {
			t.Errorf("harness.effort row %+v", r)
		}
	}
}
