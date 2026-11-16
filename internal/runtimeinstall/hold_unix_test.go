//go:build unix

package runtimeinstall_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestAFolderTheInstallMakesIsReadableUnderUmask077 installs under a umask of 077
// into a runtimes folder that does not exist: the install sets both folders to 0755, so
// the wall's user enters them.
func TestAFolderTheInstallMakesIsReadableUnderUmask077(t *testing.T) {
	before := syscall.Umask(0o077)
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
