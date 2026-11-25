//go:build unix

package cmd

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// wantModes fails the test for each path whose permission, setuid, setgid and sticky
// bits are not its mode in want.
func wantModes(t *testing.T, want map[string]fs.FileMode) {
	t.Helper()
	for path, mode := range want {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky); got != mode {
			t.Errorf("%s has mode %v, want %v", path, got, mode)
		}
	}
}

// hermeticHome sets HOME and the XDG directories to a new temporary directory.
func hermeticHome(t *testing.T) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for _, name := range []string{"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, filepath.Join(home, name))
	}
}

// TestACheckStagesUnderA0700Folder is the stage of a check, built: the stage is in a
// temporary folder that keeps mode 0700, so other users cannot list or read the stage,
// which the build gives mode 0755.
func TestACheckStagesUnderA0700Folder(t *testing.T) {
	hermeticHome(t)
	tmp, stage, err := checkStage()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	if !within(tmp, stage) || stage == tmp {
		t.Fatalf("the stage %s is not inside %s", stage, tmp)
	}
	claude, err := render.Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	res := &compose.Result{Stack: &stack.Stack{}, Instructions: "Read the plan first.\n"}
	if err := render.BuildAt(res, stage, filepath.Join(tmp, "unused-home"), claude); err != nil {
		t.Fatal(err)
	}
	wantModes(t, map[string]fs.FileMode{tmp: 0o700, stage: 0o755, filepath.Join(stage, "AGENTS.md"): 0o644})
}
