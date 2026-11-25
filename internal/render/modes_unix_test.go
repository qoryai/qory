//go:build unix

package render_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/stack"
)

// umask077 sets the process's umask to 077, under which a new directory is 0700 and a new
// file 0600, and sets it back when the test ends.
func umask077(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })
}

// chmod sets the mode of path, and fails the test when that fails.
func chmod(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// modesHarness is a harness of the copied module app, whose skill deploy has a 0600
// SKILL.md, a 0700 run.sh, a 0644 grouprun.sh that only group and others may run and a
// 0700 directory private, and of the linked module core, a 0700 directory with a 0600
// notes.md. It returns the result and core's directory.
func modesHarness(t *testing.T) (*compose.Result, string) {
	t.Helper()
	app := appModule(t)
	skill := filepath.Join(app.Dir, "skills", "deploy")
	write(t, filepath.Join(skill, "run.sh"), "#!/bin/sh\n")
	write(t, filepath.Join(skill, "grouprun.sh"), "#!/bin/sh\n")
	write(t, filepath.Join(skill, "private", "notes.md"), "notes\n")
	chmod(t, filepath.Join(skill, "SKILL.md"), 0o600)
	chmod(t, filepath.Join(skill, "run.sh"), 0o700)
	chmod(t, filepath.Join(skill, "grouprun.sh"), 0o654)
	chmod(t, filepath.Join(skill, "private", "notes.md"), 0o600)
	for _, dir := range []string{filepath.Join(skill, "private"), skill, filepath.Join(app.Dir, "skills"), app.Dir} {
		chmod(t, dir, 0o700)
	}
	core := tempDirReal(t)
	write(t, filepath.Join(core, "notes.md"), "core\n")
	chmod(t, filepath.Join(core, "notes.md"), 0o600)
	chmod(t, core, 0o700)
	return &compose.Result{
		Stack:        &stack.Stack{},
		Modules:      []compose.Module{app, {Name: "core", Dir: core}},
		Entries:      []compose.Entry{{Kind: "skills", Name: "deploy", Module: "app", Path: skill}},
		Instructions: "Read the plan first.\n",
	}, core
}

// tempDirReal is a new temporary directory with its links resolved.
func tempDirReal(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBuildSetsTheModesOfTheHomeWhateverTheUmask is a home built under umask 077 from a
// copied module whose files are 0600 and 0700 and whose directories are 0700: every
// directory of the home is 0755, a file its owner may run is 0755 and every other file
// 0644. modules/core, the link to the linked module, stays a link, and the directory and
// the file it leads to keep 0700 and 0600.
func TestBuildSetsTheModesOfTheHomeWhateverTheUmask(t *testing.T) {
	hermetic(t)
	umask077(t)
	res, core := modesHarness(t)
	home := filepath.Join(tempDirReal(t), "home")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	want := map[string]fs.FileMode{
		".":                                          0o755 | fs.ModeDir,
		"AGENTS.md":                                  0o644,
		"modules/app":                                0o755 | fs.ModeDir,
		"modules/app/skills/deploy/SKILL.md":         0o644,
		"modules/app/skills/deploy/run.sh":           0o755,
		"modules/app/skills/deploy/grouprun.sh":      0o644,
		"modules/app/skills/deploy/private":          0o755 | fs.ModeDir,
		"modules/app/skills/deploy/private/notes.md": 0o644,
	}
	for name, mode := range want {
		info, err := os.Lstat(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != mode {
			t.Errorf("%s has mode %v, want %v", name, info.Mode(), mode)
		}
	}
	var seen int
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		seen++
		switch m := info.Mode(); {
		case m&fs.ModeSymlink != 0:
		case m.IsDir() && m.Perm() != 0o755:
			t.Errorf("directory %s has mode %v, want 0755", path, m)
		case m.IsRegular() && m.Perm() != 0o644 && m.Perm() != 0o755:
			t.Errorf("file %s has mode %v, want 0644 or 0755", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < len(want) {
		t.Errorf("the walk saw %d paths, fewer than the %d checked", seen, len(want))
	}
	link, err := os.Lstat(filepath.Join(home, "modules", "core"))
	if err != nil {
		t.Fatal(err)
	}
	if link.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("modules/core has mode %v, want a link", link.Mode())
	}
	for path, mode := range map[string]fs.FileMode{core: 0o700 | fs.ModeDir, filepath.Join(core, "notes.md"): 0o600} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != mode {
			t.Errorf("the link's target %s has mode %v, want %v", path, info.Mode(), mode)
		}
	}
}

// TestACheckOfAHomeWithSetModesFindsNoDifference is the check of a home just built: the
// check renders into a stage with BuildAt and compares it with the home. The stage has the
// home's modes, so the copy of grouprun.sh, 0654 in the module, is no executable in either,
// and the check finds the home up to date.
func TestACheckOfAHomeWithSetModesFindsNoDifference(t *testing.T) {
	hermetic(t)
	umask077(t)
	res, _ := modesHarness(t)
	home := filepath.Join(tempDirReal(t), "home")
	claude := lookup(t, "claude")
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	stage := tempDirReal(t)
	if err := render.BuildAt(res, stage, home, claude); err != nil {
		t.Fatal(err)
	}
	differences, err := render.Diff(stage, home, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(differences) != 0 {
		t.Errorf("the check of a home just built finds differences: %v", differences)
	}
}

// TestSetModeFollowsNoLink is SetMode on a link to a 0600 file, with a want that would set
// any file to 0644: the open with O_NOFOLLOW fails, and the file keeps 0600.
func TestSetModeFollowsNoLink(t *testing.T) {
	dir := tempDirReal(t)
	file := filepath.Join(dir, "secret.txt")
	write(t, file, "x\n")
	chmod(t, file, 0o600)
	link := filepath.Join(dir, "link")
	symlink(t, file, link)
	err := render.SetMode(link, func(fs.FileMode) (fs.FileMode, bool) { return 0o644, true })
	if err == nil {
		t.Error("SetMode set the mode through a link")
	}
	if info, err := os.Lstat(file); err != nil || info.Mode() != 0o600 {
		t.Errorf("the link's target: %v %v, want mode 0600", info, err)
	}
}
