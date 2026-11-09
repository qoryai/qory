package pool

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/qoryai/qory/internal/pool/child"
)

// TestReadSession reads the id and the leader's start stamp that run.session contains,
// and returns an error for an id that would signal more than one run or the pool
// itself: 0, 1, a negative id, the pool's process id, the pool's session. Text that is
// no number, a stamp that is no number, an empty file, and a file with the id alone are
// errors too, as is a folder without run.session.
func TestReadSession(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := readSession(dir); err == nil {
		t.Error("a folder without run.session was read")
	}
	for _, bad := range []string{"0 1.5", "1 1.5", "-5 1.5", strconv.Itoa(os.Getpid()) + " 1.5", strconv.Itoa(child.Session()) + " 1.5",
		"abc 1.5", "54321 abc", "54321 1.5 7", "54321", ""} {
		if err := os.WriteFile(filepath.Join(dir, runSessionFile), []byte(bad+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if n, stamp, err := readSession(dir); err == nil {
			t.Errorf("run.session %q was read as %d %q", bad, n, stamp)
		}
	}
	for _, good := range []string{"54321 8811", "54321 1762600000.000123"} {
		if err := os.WriteFile(filepath.Join(dir, runSessionFile), []byte(good+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if n, stamp, err := readSession(dir); err != nil || strconv.Itoa(n)+" "+stamp != good {
			t.Errorf("run.session %q was read as %d %q, %v", good, n, stamp, err)
		}
	}
}
