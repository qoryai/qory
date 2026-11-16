//go:build unix

package runtimeinstall_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestAFolderTheInstallMakesIsNotWritableUnderUmask002 installs under a umask of 002
// into a runtimes folder that does not exist: the install makes both folders 0755.
func TestAFolderTheInstallMakesIsNotWritableUnderUmask002(t *testing.T) {
	before := syscall.Umask(0o002)
	defer syscall.Umask(before)
	in := newInstaller(t)
	in.folder = filepath.Join(in.folder, "runtimes")
	got := in.installVersion(t, "2.0.0")
	defer got.Release()
	for _, p := range []string{in.folder, filepath.Join(in.folder, "claude")} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("%s: %v, %v, want mode 0755", p, info, err)
		}
	}
}
