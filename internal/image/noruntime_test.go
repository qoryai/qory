package image_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoImageContainsARuntime reads the Dockerfile of each agent image qory builds: none
// installs an agent runtime, sets a runtime's variables, or has /qory/agent. The machine
// installs the runtime at run time, and Forager mounts it read-only at /qory/runtime and
// sets the runtime's variables.
func TestNoImageContainsARuntime(t *testing.T) {
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	for _, image := range []string{"agent", "agent-docker", "prebuild"} {
		path := filepath.Join("..", "..", "images", image, "Dockerfile")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, bad := range []string{"claude", "codex", "CLAUDE_CODE_", "DISABLE_AUTOUPDATER", "/qory/agent"} {
			if strings.Contains(text, bad) {
				t.Errorf("Remove %s from %s: no image contains a runtime, and Forager mounts it at run time.", bad, path)
			}
		}
	}
}
