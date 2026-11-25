//go:build unix

package cmd_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// umask077 sets the process's umask to 077, under which a new directory is 0700 and a new
// file 0600, and sets it back when the test ends.
func umask077(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })
}

// modeOf is the mode of path, links not followed.
func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

// composeUnderTheCache composes, under umask 077, the checkout of pushChecksCheckout with
// AGENTS.md in its module core, into a home under qory/homes in the cache directory, with
// HOME set to userHome. The test makes the cache directory at mode 0700 first. It returns
// the home and the cache directory.
func composeUnderTheCache(t *testing.T, userHome string) (home, base string) {
	t.Helper()
	root, _, _ := pushChecksCheckout(t, true)
	writeFile(t, filepath.Join(root, "modules", "core", "AGENTS.md"), "Read the plan first.\n")
	t.Setenv("HOME", userHome)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(userHome, ".cache"))
	umask077(t)
	base = cacheDir(t)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	homes := filepath.Join(base, "qory", "homes")
	if out, err := run(t, "harness", "compose", "--home", homes); err != nil {
		t.Fatalf("compose: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(homes)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			home = filepath.Join(homes, e.Name())
		}
	}
	if home == "" {
		t.Fatalf("%s has no home", homes)
	}
	return home, base
}

// cacheDir is the user's cache directory, as qory looks it up.
func cacheDir(t *testing.T) string {
	t.Helper()
	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAComposeSetsModesInsideTheHomeAlone is a compose under umask 077 into qory/homes in
// the cache directory: the home is 0755 with AGENTS.md and push-checks.json at 0644, and the
// folders above the home keep the 0700 the umask gave them.
func TestAComposeSetsModesInsideTheHomeAlone(t *testing.T) {
	home, base := composeUnderTheCache(t, tempDir(t))
	want := map[string]fs.FileMode{
		home:                                    0o755 | fs.ModeDir,
		filepath.Join(home, "AGENTS.md"):        0o644,
		filepath.Join(home, "push-checks.json"): 0o644,
		filepath.Join(base, "qory", "homes"):    0o700 | fs.ModeDir,
		filepath.Join(base, "qory"):             0o700 | fs.ModeDir,
		base:                                    0o700 | fs.ModeDir,
	}
	for path, mode := range want {
		if got := modeOf(t, path); got != mode {
			t.Errorf("%s has mode %v, want %v", path, got, mode)
		}
	}
}

// TestACheckBuildsItsStageUnderA0700Folder is qory harness compose --check of a composed
// home: the check takes its temporary folder and stage from checkStage once, and builds the
// stage inside a folder that stays 0700. The stand-in keeps that folder, held, and hands the
// check another to remove, so the stage is there to see after the check.
func TestACheckBuildsItsStageUnderA0700Folder(t *testing.T) {
	pushChecksCheckout(t, true)
	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("compose: %v\n%s", err, out)
	}
	umask077(t)
	held := filepath.Join(tempDir(t), "held")
	if err := os.Mkdir(held, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	defer cmd.SetCheckStage(func() (string, string, error) {
		calls++
		removed, err := os.MkdirTemp(tempDir(t), "removed-")
		return removed, filepath.Join(held, "home"), err
	})()
	if out, err := run(t, "harness", "compose", "--check"); err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	if calls != 1 {
		t.Errorf("the check took a stage %d times, want 1", calls)
	}
	if got := modeOf(t, held); got != 0o700|fs.ModeDir {
		t.Errorf("the stage's folder has mode %v, want 0700", got)
	}
	if got := modeOf(t, filepath.Join(held, "home")); got != 0o755|fs.ModeDir {
		t.Errorf("the stage has mode %v, want 0755", got)
	}
}
