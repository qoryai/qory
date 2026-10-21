//go:build unix

package foragerdir_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/foragerdir"
)

// TestLockFileIsHeldOnceAtATime takes a file's lock, made mode 0600: a second take while
// it is held is ErrLocked, without waiting, and one after the release holds it again. A
// link in the file's place is refused.
func TestLockFileIsHeldOnceAtATime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.lock")
	l, err := foragerdir.LockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the lock file: %v, %v", info, err)
	}
	if again, err := foragerdir.LockFile(path); !errors.Is(err, foragerdir.ErrLocked) {
		again.Release()
		t.Errorf("a second take: %v, want ErrLocked", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	l, err = foragerdir.LockFile(path)
	if err != nil {
		t.Fatalf("a take after the release: %v", err)
	}
	l.Release()
	link := filepath.Join(t.TempDir(), "link.lock")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if l, err := foragerdir.LockFile(link); err == nil {
		l.Release()
		t.Error("a link was taken as the lock file")
	}
}
