package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/foragerdir"
)

// TestForagersDirectoryRefusesARelativeConfigHome is the directory of forager.yaml, which
// holds the access key's secret and the kept run keys, with XDG_CONFIG_HOME relative:
// machineDir, the kept run key's reader and its remover, the access key's reader and
// the user's harness.home each refuse it, and no file under it is read or removed.
func TestForagersDirectoryRefusesARelativeConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	kept := filepath.Join(dir, "cfg", "qory")
	if err := os.MkdirAll(kept, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := foragerdir.Dir(kept).WriteRunKey("run-1", "not a run key"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", "cfg")

	if d, err := machineDir(); d != "" || !errors.Is(err, config.ErrRelativeConfigHome) {
		t.Errorf("machineDir: %q, %v", d, err)
	}
	if k, err := keptRunKey("run-1"); k != nil || !errors.Is(err, config.ErrRelativeConfigHome) {
		t.Errorf("keptRunKey: %v, %v", k, err)
	}
	var said []string
	dropKeptRunKey("run-1", func(line string) { said = append(said, line) })
	if len(said) != 1 || said[0] != config.ErrRelativeConfigHome.Error() {
		t.Errorf("dropKeptRunKey said %q", said)
	}
	if _, err := os.Stat(foragerdir.Dir(kept).RunKeyPath("run-1")); err != nil {
		t.Errorf("the kept run key: %v", err)
	}
	if _, err := signingKey(&config.Forager{}, nil); !errors.Is(err, config.ErrRelativeConfigHome) {
		t.Errorf("signingKey: %v", err)
	}
	if err := userHome(places{}, config.Defaults()); !errors.Is(err, config.ErrRelativeConfigHome) {
		t.Errorf("userHome: %v", err)
	}
}
