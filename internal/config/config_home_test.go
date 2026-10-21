package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/config"
)

// relativeConfigHome makes a fresh folder the working folder, writes in it a qory.yaml
// and a forager.yaml under cfg/qory that no reader could take, and sets XDG_CONFIG_HOME
// to cfg, a relative path to that folder.
func relativeConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	writeRaw(t, filepath.Join(dir, "cfg", "qory", config.FileName), "harness: [\n")
	writeRaw(t, filepath.Join(dir, "cfg", "qory", config.ForagerFileName), "gateway: [\n")
	t.Setenv("XDG_CONFIG_HOME", "cfg")
	return dir
}

// TestUserDirRefusesARelativeConfigHome is the configuration directory for each form of
// XDG_CONFIG_HOME: an absolute one names it, an unset or empty one leaves it under the
// home, and a relative one is refused with the line that says why, and no directory.
func TestUserDirRefusesARelativeConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	abs := filepath.Join(home, "elsewhere")
	for _, c := range []struct {
		value, want string
		err         error
	}{
		{abs, filepath.Join(abs, "qory"), nil},
		{"", filepath.Join(home, ".config", "qory"), nil},
		{"cfg", "", config.ErrRelativeConfigHome},
		{"./cfg", "", config.ErrRelativeConfigHome},
		{"../cfg", "", config.ErrRelativeConfigHome},
	} {
		t.Setenv("XDG_CONFIG_HOME", c.value)
		dir, err := config.UserDir()
		if dir != c.want || !errors.Is(err, c.err) {
			t.Errorf("XDG_CONFIG_HOME=%q: %q, %v; want %q, %v", c.value, dir, err, c.want, c.err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "cfg")
	_, err := config.UserDir()
	if want := "qory: XDG_CONFIG_HOME is not an absolute path; qory reads its configuration only from an absolute one, so that it does not change with the working folder"; err == nil || err.Error() != want {
		t.Errorf("the refusal: %v, want %q", err, want)
	}
}

// TestNoReaderTakesAFileFromARelativeConfigHome reads the configuration each way qory
// does with a relative XDG_CONFIG_HOME: each one is refused for that, before it reads the
// file it names, which would refuse otherwise.
func TestNoReaderTakesAFileFromARelativeConfigHome(t *testing.T) {
	root := relativeConfigHome(t)
	readers := map[string]func() error{
		"Load":     func() error { _, err := config.Load(root, true); return err },
		"LoadUser": func() error { _, err := config.LoadUser(); return err },
		"Discover": func() error { _, err := config.Discover(root); return err },
		"LoadForager": func() error {
			_, err := config.LoadForager()
			return err
		},
		"LoadForagerInstance": func() error {
			_, err := config.LoadForagerInstance()
			return err
		},
	}
	for name, read := range readers {
		if err := read(); !errors.Is(err, config.ErrRelativeConfigHome) {
			t.Errorf("%s: %v, want the refusal of a relative XDG_CONFIG_HOME", name, err)
		}
	}
}

// TestAnAbsoluteOrUnsetConfigHomeIsRead reads the user's qory.yaml under an absolute
// XDG_CONFIG_HOME, and under ~/.config with none set.
func TestAnAbsoluteOrUnsetConfigHomeIsRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	for _, c := range []struct{ value, dir string }{
		{filepath.Join(home, "elsewhere"), filepath.Join(home, "elsewhere", "qory")},
		{"", filepath.Join(home, ".config", "qory")},
	} {
		t.Setenv("XDG_CONFIG_HOME", c.value)
		file := filepath.Join(c.dir, config.FileName)
		writeRaw(t, file, "apiVersion: qory.dev/v1alpha1\nharness: {force: true}\n")
		conf, err := config.LoadUser()
		if err != nil {
			t.Fatalf("XDG_CONFIG_HOME=%q: %v", c.value, err)
		}
		if got := conf.Origin("harness.force"); got != file {
			t.Errorf("XDG_CONFIG_HOME=%q: harness.force from %q, want %q", c.value, got, file)
		}
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
	}
}
