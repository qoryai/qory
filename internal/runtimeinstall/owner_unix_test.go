//go:build unix

package runtimeinstall_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runtimeinstall"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// TestAFolderOfAnotherUserStopsTheInstall has the install run as a user who is not the
// owner of the folders, which are not root's either: for a runtimes folder that exists
// and for one that the install makes, the install stops with the fix first, and makes no
// version's folder.
func TestAFolderOfAnotherUserStopsTheInstall(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root owns the folders, so no other user's folder can be made")
	}
	for _, c := range []struct {
		name   string
		exists bool
	}{{"an existing folder", true}, {"a folder the install makes", false}} {
		t.Run(c.name, func(t *testing.T) {
			defer runtimeinstall.SetCurrentUID(os.Getuid() + 1000)()
			in := newInstaller(t)
			if !c.exists {
				in.folder = filepath.Join(in.folder, "runtimes")
			}
			in.src.Publish("2.0.0", rt.PlainTarball(t, "2.0.0"))
			_, err := in.install()
			want := "Give " + in.folder + " to root or to the user that runs qory, then run again\n"
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("the install returned %v, want it to start with %q", err, want)
			}
			if exists(filepath.Join(in.folder, "claude", "2.0.0")) {
				t.Fatal("the install made the version's folder")
			}
		})
	}
}
