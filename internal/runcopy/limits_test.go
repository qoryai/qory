package runcopy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTheSessionLimitScalesWithTheCopy is the time limit of the git session: 5 minutes on a small
// copy, a minute more per whole 20,000 entries and per whole 2 GiB, the two added, and at most 60
// minutes.
func TestTheSessionLimitScalesWithTheCopy(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		entries, bytes int64
		want           time.Duration
	}{
		{0, 0, 5 * time.Minute},
		{19_999, 2*gib - 1, 5 * time.Minute},
		{20_000, 0, 6 * time.Minute},
		{0, 2 * gib, 6 * time.Minute},
		{20_000, 2 * gib, 7 * time.Minute},
		{100_000, 10 * gib, 15 * time.Minute},
		{1_100_000, 0, 60 * time.Minute},
		{1_080_000, 2 * gib, 60 * time.Minute},
		{0, 1 << 62, 60 * time.Minute},
		{-1, -1, 5 * time.Minute},
	}
	for _, c := range cases {
		if got := SessionLimitFor(c.entries, c.bytes); got != c.want {
			t.Errorf("SessionLimitFor(%d, %d) = %s, want %s", c.entries, c.bytes, got, c.want)
		}
	}
	if SessionLimitFor(0, 0) != SessionBase || SessionLimitFor(1<<40, 1<<62) != SessionMax {
		t.Errorf("the base is %s and the cap %s", SessionBase, SessionMax)
	}
}

// TestTheWorkSizeFollowsNoLink is the count of a copy with a folder, two files and a link to a
// large folder outside it: the link counts as one entry, and nothing outside the copy counts.
func TestTheWorkSizeFollowsNoLink(t *testing.T) {
	isolate(t)
	d := newDir(t)
	work := d.Work()
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, work, "a.txt", "hello\n")
	write(t, work, "src/b.txt", "1234567890")
	outside := t.TempDir()
	write(t, outside, "big.txt", string(make([]byte, 1<<20)))
	if err := os.Symlink(outside, filepath.Join(work, "out")); err != nil {
		t.Fatal(err)
	}
	entries, bytes, err := d.WorkSize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if entries != 4 || bytes != 16 {
		t.Errorf("%d entries and %d bytes, want 4 and 16", entries, bytes)
	}
}

// TestTheWorkSizeGoesPastAFolderItCannotRead is a folder of mode 000 in the copy: it counts as
// one entry, and the count goes on.
func TestTheWorkSizeGoesPastAFolderItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder of mode 000")
	}
	isolate(t)
	d := newDir(t)
	work := d.Work()
	closed := filepath.Join(work, "closed")
	if err := os.MkdirAll(closed, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, closed, "in.txt", "hidden")
	write(t, work, "a.txt", "abc")
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	entries, bytes, err := d.WorkSize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if entries != 2 || bytes != 3 {
		t.Errorf("%d entries and %d bytes, want 2 and 3", entries, bytes)
	}
}

// TestTheWorkSizeEndsWithItsContext is a count whose context ended: it returns the context's
// error.
func TestTheWorkSizeEndsWithItsContext(t *testing.T) {
	isolate(t)
	d := newDir(t)
	write(t, d.Work(), "a.txt", "abc")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := d.WorkSize(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("the count returned %v, want the end of its context", err)
	}
}
