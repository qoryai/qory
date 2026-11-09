package pool

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/qoryai/qory/internal/pool/child"
)

// TestReadGroup reads the id and the leader's start stamp that run.pgid contains, and
// returns an error for an id that would signal more than one run or the pool itself: 0,
// 1, a negative id, the pool's process id, the pool's process group. Text that is no
// number, a stamp that is no number, an empty file, and a file with the id alone, as
// run.pgid was written before it had the stamp, are errors too, as is a folder without
// run.pgid.
func TestReadGroup(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := readGroup(dir); err == nil {
		t.Error("a folder without run.pgid was read")
	}
	for _, bad := range []string{"0 1.5", "1 1.5", "-5 1.5", strconv.Itoa(os.Getpid()) + " 1.5", strconv.Itoa(child.ProcessGroup()) + " 1.5",
		"abc 1.5", "54321 abc", "54321 1.5 7", "54321", ""} {
		if err := os.WriteFile(filepath.Join(dir, runGroupFile), []byte(bad+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if n, stamp, err := readGroup(dir); err == nil {
			t.Errorf("run.pgid %q was read as %d %q", bad, n, stamp)
		}
	}
	for _, good := range []string{"54321 8811", "54321 1762600000.000123"} {
		if err := os.WriteFile(filepath.Join(dir, runGroupFile), []byte(good+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if n, stamp, err := readGroup(dir); err != nil || strconv.Itoa(n)+" "+stamp != good {
			t.Errorf("run.pgid %q was read as %d %q, %v", good, n, stamp, err)
		}
	}
}
