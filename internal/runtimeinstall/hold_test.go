package runtimeinstall_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// exists reports whether path is there, without following a link.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// installVersion publishes version as newest and installs it, failing the test on an
// error.
func (in *installer) installVersion(t *testing.T, version string) runtimeinstall.Installed {
	t.Helper()
	in.src.Publish(version, rt.PlainTarball(t, version))
	got, err := in.install()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestAHeldOlderVersionSurvivesTheCleanup installs 1.0.0 and keeps its hold, as a run
// that has not ended: the install of 2.0.0 keeps 1.0.0's folder. Once the hold ends, the
// next install removes 1.0.0's folder and its hold file, and keeps 2.0.0's.
func TestAHeldOlderVersionSurvivesTheCleanup(t *testing.T) {
	in := newInstaller(t)
	old := in.installVersion(t, "1.0.0")
	newer := in.installVersion(t, "2.0.0")
	defer newer.Release()
	if !exists(old.Program()) {
		t.Fatal("the install of 2.0.0 removed 1.0.0, which a run holds")
	}
	old.Release()
	again := in.installVersion(t, "2.0.0")
	defer again.Release()
	base := filepath.Join(in.folder, "claude")
	if exists(old.Dir) || exists(filepath.Join(base, ".hold-1.0.0")) {
		t.Fatalf("1.0.0 is still there once no run holds it: %q", files(t, in.folder))
	}
	if again.Downloaded || !exists(again.Program()) {
		t.Fatalf("2.0.0 = %+v, want its folder kept and reused", again)
	}
}

// TestAnUnheldOlderVersionIsRemoved installs 1.0.0 and ends its hold: the install of
// 2.0.0 removes 1.0.0, and keeps a newer version's folder, 3.0.0, and a folder whose
// name is no semantic version, nightly.
func TestAnUnheldOlderVersionIsRemoved(t *testing.T) {
	in := newInstaller(t)
	old := in.installVersion(t, "1.0.0")
	old.Release()
	base := filepath.Join(in.folder, "claude")
	for _, d := range []string{"3.0.0", "nightly"} {
		if err := os.Mkdir(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := in.installVersion(t, "2.0.0")
	defer got.Release()
	if exists(old.Dir) {
		t.Fatal("the install of 2.0.0 kept 1.0.0, which no run holds")
	}
	for _, d := range []string{"3.0.0", "nightly"} {
		if !exists(filepath.Join(base, d)) {
			t.Errorf("the install of 2.0.0 removed %s", d)
		}
	}
}

// TestStrayTemporaryEntriesAreRemoved leaves temporary entries of a run that stopped, of
// 1.0.0 and with no version, and one of 2.0.0 while a run holds 2.0.0, as a run that is
// reading its tarball: the next install removes the stray ones, keeps the held one, and
// keeps an entry with another name.
func TestStrayTemporaryEntriesAreRemoved(t *testing.T) {
	in := newInstaller(t)
	holder := in.installVersion(t, "2.0.0")
	defer holder.Release()
	base := filepath.Join(in.folder, "claude")
	stray := []string{".download-1.0.0-123", ".download-456", ".install-1.0.0-789"}
	kept := []string{".download-2.0.0-321", "notes.txt"}
	for _, name := range append(append([]string{}, stray...), kept...) {
		path := filepath.Join(base, name)
		if name == ".install-1.0.0-789" {
			writeIn(t, filepath.Join(path, "bin", "claude"))
			continue
		}
		writeIn(t, path)
	}
	got := in.installVersion(t, "2.0.0")
	defer got.Release()
	for _, name := range stray {
		if exists(filepath.Join(base, name)) {
			t.Errorf("%s is still there", name)
		}
	}
	for _, name := range kept {
		if !exists(filepath.Join(base, name)) {
			t.Errorf("%s was removed", name)
		}
	}
}

// TestTheCleanupFollowsNoLink puts links in the runtime's folder, named as an older
// version and as a temporary folder, to a folder outside it: the install of 2.0.0
// removes nothing in that folder, and leaves the links.
func TestTheCleanupFollowsNoLink(t *testing.T) {
	in := newInstaller(t)
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	writeIn(t, keep)
	base := filepath.Join(in.folder, "claude")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	links := []string{"1.0.0", ".install-1.0.0-1", ".download-1.0.0-2"}
	for _, name := range links {
		if err := os.Symlink(outside, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	got := in.installVersion(t, "2.0.0")
	defer got.Release()
	if !exists(keep) {
		t.Fatal("the cleanup removed a file through a link")
	}
	for _, name := range links {
		if !exists(filepath.Join(base, name)) {
			t.Errorf("the link %s was removed", name)
		}
	}
}

// TestTheCleanupKeepsAFolderThatAnInstallDidNotMake plants, in the runtime's folder,
// folders named as older versions that lack the shape an install makes, bin/claude and
// integrity as regular files in a real bin folder: one with a file only, one with the
// two files and a link at bin, one with no integrity, one whose program is a link, and
// one whose integrity is a folder. The install of 2.0.0 keeps every one, and removes the
// older version that an install made.
func TestTheCleanupKeepsAFolderThatAnInstallDidNotMake(t *testing.T) {
	in := newInstaller(t)
	made := in.installVersion(t, "0.9.0")
	made.Release()
	base := filepath.Join(in.folder, "claude")
	outside := t.TempDir()
	writeIn(t, filepath.Join(outside, "bin", "claude"))
	writeIn(t, filepath.Join(outside, "integrity"))
	foreign := map[string]func(dir string){
		"1.0.0": func(dir string) { writeIn(t, filepath.Join(dir, "notes.txt")) },
		"1.0.1": func(dir string) {
			if err := os.Symlink(filepath.Join(outside, "bin"), filepath.Join(dir, "bin")); err != nil {
				t.Fatal(err)
			}
			writeIn(t, filepath.Join(dir, "integrity"))
		},
		"1.0.2": func(dir string) { writeIn(t, filepath.Join(dir, "bin", "claude")) },
		"1.0.3": func(dir string) {
			writeIn(t, filepath.Join(dir, "integrity"))
			if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "bin", "claude"), filepath.Join(dir, "bin", "claude")); err != nil {
				t.Fatal(err)
			}
		},
		"1.0.4": func(dir string) {
			writeIn(t, filepath.Join(dir, "bin", "claude"))
			if err := os.MkdirAll(filepath.Join(dir, "integrity"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, plant := range foreign {
		dir := filepath.Join(base, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		plant(dir)
	}
	got := in.installVersion(t, "2.0.0")
	defer got.Release()
	if exists(made.Dir) {
		t.Error("the install of 2.0.0 kept 0.9.0, which an install made and no run holds")
	}
	for name := range foreign {
		if !exists(filepath.Join(base, name)) {
			t.Errorf("the install of 2.0.0 removed %s, which no install made", name)
		}
	}
	if !exists(filepath.Join(outside, "bin", "claude")) || !exists(filepath.Join(outside, "integrity")) {
		t.Error("the cleanup removed a file through a link")
	}
}

// TestAHoldFileThatIsALinkIsNotFollowed names links .hold-<version> after an older
// version whose folder an install made, and after a version with no folder, each to a
// file outside the folder. The install of 2.0.0 does not follow them: it removes
// neither the files nor the links, and keeps the older version's folder, since a hold
// that it cannot check keeps its folder.
func TestAHoldFileThatIsALinkIsNotFollowed(t *testing.T) {
	in := newInstaller(t)
	old := in.installVersion(t, "1.0.0")
	old.Release()
	base := filepath.Join(in.folder, "claude")
	outside := t.TempDir()
	targets := map[string]string{".hold-1.0.0": "a", ".hold-0.5.0": "b"}
	for name, file := range targets {
		path := filepath.Join(outside, file)
		writeIn(t, path)
		if err := os.Remove(filepath.Join(base, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	got := in.installVersion(t, "2.0.0")
	defer got.Release()
	for name, file := range targets {
		if !exists(filepath.Join(outside, file)) {
			t.Errorf("the cleanup removed the file that %s leads to", name)
		}
		if info, err := os.Lstat(filepath.Join(base, name)); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link %s is gone or changed: %v", name, err)
		}
	}
	if !exists(old.Program()) {
		t.Error("the install of 2.0.0 removed 1.0.0 although its hold file could not be checked")
	}
}

// TestVersionsCompareAsSemanticVersions orders versions as the cleanup reads them, and
// leaves a name that is no semantic version uncompared.
func TestVersionsCompareAsSemanticVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{
		{"2.1.320", "2.1.321", -1, true},
		{"2.1.99", "2.1.100", -1, true},
		{"2.10.0", "2.9.9", 1, true},
		{"2.1.320", "2.1.320+build.5", 0, true},
		{"2.1.320-beta.1", "2.1.320", -1, true},
		{"2.1.320-beta.2", "2.1.320-beta.10", -1, true},
		{"2.1.320-alpha", "2.1.320-1", 1, true},
		{"1.0", "1.0.0", 0, true},
		{"nightly", "2.1.320", 0, false},
		{"2.1.x", "2.1.320", 0, false},
	} {
		got, ok := runtimeinstall.CompareVersions(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("compare %s %s = %d %t, want %d %t", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

// writeIn writes a small file at path, and its folder.
func writeIn(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAWritableRuntimesFolderStopsTheInstall has a runtimes folder, and a runtime's
// folder in it, that existed before the install and that their group can write: each
// stops the install with chmod go-w first, and no version's folder is made. A folder
// the install makes itself is 0755 under a umask of 002 and stops nothing.
func TestAWritableRuntimesFolderStopsTheInstall(t *testing.T) {
	for _, c := range []struct {
		name string
		path func(folder string) string
	}{
		{"the runtimes folder", func(folder string) string { return folder }},
		{"the runtime's folder", func(folder string) string { return filepath.Join(folder, "claude") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := newInstaller(t)
			if err := os.MkdirAll(filepath.Join(in.folder, "claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(c.path(in.folder), 0o775); err != nil {
				t.Fatal(err)
			}
			in.src.Publish("2.0.0", rt.PlainTarball(t, "2.0.0"))
			_, err := in.install()
			want := "Run chmod go-w " + c.path(in.folder) + ", then run again\n"
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("the install returned %v, want it to start with %q", err, want)
			}
			if exists(filepath.Join(in.folder, "claude", "2.0.0")) {
				t.Fatal("the install made the version's folder")
			}
		})
	}
}
