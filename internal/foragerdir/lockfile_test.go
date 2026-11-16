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

// TestLockFileTextIsReadWhileHeld is a lock file with text: a process that finds the lock
// held reads the text of the holder, a later holder replaces it, and a link or a missing
// file reads as no text.
func TestLockFileTextIsReadWhileHeld(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "home.lock")
	l, err := foragerdir.LockFileText(path, "run one, a long first text\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foragerdir.LockFileText(path, "run two\n"); !errors.Is(err, foragerdir.ErrLocked) {
		t.Errorf("a second take: %v, want ErrLocked", err)
	}
	if got := foragerdir.LockText(path); got != "run one, a long first text\n" {
		t.Errorf("the text while held is %q", got)
	}
	l.Release()
	l, err = foragerdir.LockFileText(path, "run two\n")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if got := foragerdir.LockText(path); got != "run two\n" {
		t.Errorf("the text of the later holder is %q, want it to replace the first", got)
	}
	if err := os.Symlink(path, filepath.Join(dir, "link.lock")); err != nil {
		t.Fatal(err)
	}
	if got := foragerdir.LockText(filepath.Join(dir, "link.lock")); got != "" {
		t.Errorf("the text through a link is %q, want none", got)
	}
	if got := foragerdir.LockText(filepath.Join(dir, "missing.lock")); got != "" {
		t.Errorf("the text of a missing file is %q, want none", got)
	}
}

// TestSetTextReplacesTheHoldersText is a holder that writes its run id once it is known: a
// reader finds the new text, and a nil lock sets nothing.
func TestSetTextReplacesTheHoldersText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home.lock")
	l, err := foragerdir.LockFileText(path, "pid 1\nstarted later than the run id\n")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := l.SetText("run r1\npid 1\n"); err != nil {
		t.Fatal(err)
	}
	if got := foragerdir.LockText(path); got != "run r1\npid 1\n" {
		t.Errorf("the text after SetText is %q", got)
	}
	var none *foragerdir.Lock
	if err := none.SetText("x"); err != nil {
		t.Errorf("SetText of a nil lock: %v", err)
	}
}

// TestSetTextWithAShorterTextLeavesNoTail is a holder that sets a text shorter than the
// file holds: the file has exactly the new text, with no bytes of the old one after it.
func TestSetTextWithAShorterTextLeavesNoTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home.lock")
	l, err := foragerdir.LockFileText(path, "run a-long-run-id\npid 12345\nstarted 2026-11-16T10:00:00Z\n")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := l.SetText("pid 1\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "pid 1\n" {
		t.Errorf("the file holds %q (%v), want exactly %q", data, err, "pid 1\n")
	}
}
