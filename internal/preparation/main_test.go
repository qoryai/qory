package preparation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain runs the tests with HOME and the XDG directories in a folder that is made
// for the run and removed after it. Each preparation holds its working folder as a
// place, which writes a lock under $XDG_STATE_HOME/qory/places and reads Forager's
// registry of walled runs: with the person's HOME, every test wrote and read them.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

// runIsolated runs m with HOME, the XDG directories and git's settings in a new folder,
// and no QORY_ or FORAGER_ variable, and removes the folder after the run.
func runIsolated(m *testing.M) int {
	home, err := os.MkdirTemp("", "preparation-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(home)
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"XDG_STATE_HOME":      filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME":      filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":       filepath.Join(home, ".local", "share"),
		"GIT_CONFIG_GLOBAL":   filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM": "1",
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "QORY_") || strings.HasPrefix(k, "FORAGER_") {
			os.Unsetenv(k)
		}
	}
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "DOCKER_CONFIG", "GH_CONFIG_DIR"} {
		os.Unsetenv(k)
	}
	return m.Run()
}
