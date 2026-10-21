//go:build unix

package hostgit_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
)

// TestAPipeTheCheckWouldReadIsRefused puts a named pipe, with nothing writing to it,
// where the check reads a file of a recorded repository: info/grafts, which the check
// hashes, and the configuration, which git reads. The check refuses each through the
// line it says for that file, at once, rather than waiting on the pipe.
func TestAPipeTheCheckWouldReadIsRefused(t *testing.T) {
	for _, c := range []struct {
		file   string
		reason int
		want   func(dir, path string) string
	}{
		{"info/grafts", hostgit.RefusedObjectsFile, func(dir, path string) string {
			return "the git directory of " + dir + " holds " + path + ", which it did not hold when a walled run began here; qory runs no git there, since a run could have written it to change which objects or history git reads. Remove it to go on"
		}},
		{"config", hostgit.RefusedUnreadable, func(dir, _ string) string {
			return "the git configuration of " + dir + " cannot be read; qory runs no git there, since a run could have set it to make git on this machine run a program"
		}},
	} {
		t.Run(c.file, func(t *testing.T) {
			hermetic(t)
			dir := newRepo(t)
			record(t, dir)
			path := filepath.Join(dir, ".git", filepath.FromSlash(c.file))
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := hostgit.Command(context.Background(), dir, "status")
				done <- err
			}()
			select {
			case err := <-done:
				var r *hostgit.RefusedError
				if want := c.want(dir, path); !errors.As(err, &r) || r.Reason != c.reason || err.Error() != want {
					t.Errorf("got %v, want %q", err, want)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the check waits on the pipe")
			}
		})
	}
}
