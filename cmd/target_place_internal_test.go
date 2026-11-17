package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// TestATargetRefusalShowsWhereTheRuntimeCameFrom is a runtime list that is not valid, set
// by the document, by the configuration and by --runtime: the last line of the refusal is
// the one that set it, prints the document's path with a folder whose name has an access key secret as
// the phrase, and never prints the secret.
func TestATargetRefusalShowsWhereTheRuntimeCameFrom(t *testing.T) {
	file := filepath.Join(t.TempDir(), "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA", "qory.yaml")
	document := func() *stack.Stack {
		return &stack.Stack{File: file, Target: stack.Target{Runtimes: stack.Runtimes{"claude", "claude"}}}
	}
	for _, c := range []struct {
		name    string
		p       *stack.Stack
		conf    config.Config
		flag    string
		want    string
		notWant string
	}{
		{"the document", document(), config.Config{}, "", "target.runtime is in ", "--runtime sets it"},
		{"the configuration", document(), config.Config{Runtime: stack.Runtimes{"codex", "codex"}}, "", "harness.runtime in the configuration sets it.", "target.runtime is in"},
		{"the flag", document(), config.Config{Runtime: stack.Runtimes{"codex", "codex"}}, "claude,claude", "--runtime sets it.", "harness.runtime"},
	} {
		err := applyTarget(c.p, nil, c.conf, c.flag, "", "")
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		text := err.Error()
		if !strings.Contains(text, c.want) || strings.Contains(text, c.notWant) {
			t.Errorf("%s: %q, want it to name %q and not %q", c.name, text, c.want, c.notWant)
		}
		if strings.Contains(strings.ToLower(text), "qak_") {
			t.Errorf("%s: the refusal prints the secret: %q", c.name, text)
		}
	}
	err := applyTarget(document(), nil, config.Config{}, "", "", "")
	if want := "(a name that contains an access key secret)/qory.yaml."; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("the document's path: %v, want it to end with %q", err, want)
	}
}
