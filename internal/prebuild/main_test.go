package prebuild_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/testenv"
)

// TestMain runs every test with HOME and the XDG folders in a new temporary folder, so
// that no test reads the state or the configuration of the machine it runs on. It first
// removes every variable of the developer's shell that [testenv.FixedEnvironment] does not
// list.
func TestMain(m *testing.M) {
	testenv.StartFromFixedEnvironment()
	home, err := os.MkdirTemp("", "prebuild-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for name, sub := range map[string]string{
		"HOME":            "",
		"XDG_STATE_HOME":  ".local/state",
		"XDG_CONFIG_HOME": ".config",
		"XDG_CACHE_HOME":  ".cache",
		"XDG_DATA_HOME":   ".local/share",
	} {
		if err := os.Setenv(name, filepath.Join(home, sub)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
