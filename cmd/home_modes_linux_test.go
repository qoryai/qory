//go:build linux

package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestAnotherUserReadsAHomeComposedUnderUmask077 is a home composed under umask 077, read
// by uid and gid 65534, which shows that the modes do not depend on the umask: cat of
// AGENTS.md and of push-checks.json succeeds. The test opens the folders above the home to
// 0755 itself, so only the modes that the compose sets in the home count. It runs only as
// root, which may start a process as another user; CI runs the tests as root.
func TestAnotherUserReadsAHomeComposedUnderUmask077(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("starts a process as uid 65534, which only root may do")
	}
	top, err := os.MkdirTemp("", "qory-modes-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(top) })
	userHome := filepath.Join(top, "home")
	if err := os.Mkdir(userHome, 0o755); err != nil {
		t.Fatal(err)
	}
	home, _ := composeUnderTheCache(t, userHome)
	for dir := filepath.Dir(home); ; dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if dir == top {
			break
		}
	}
	for _, name := range []string{"AGENTS.md", render.PushChecksFile} {
		path := filepath.Join(home, name)
		c := exec.Command("cat", path)
		c.Dir = "/"
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
		if out, err := c.CombinedOutput(); err != nil {
			t.Errorf("uid 65534 cannot read %s: %v\n%s", path, err, out)
		}
	}
}
