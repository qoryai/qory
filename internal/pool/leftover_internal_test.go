package pool

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/qoryai/qory/internal/pool/child"
)

// TestReadGroup reads the id that run.pgid contains, and returns an error for an id
// that would signal more than one run or the pool itself: 0, 1, a negative id, the pool's
// process id, the pool's process group, text that is no number, and an empty file. A
// folder without run.pgid is an error too.
func TestReadGroup(t *testing.T) {
	dir := t.TempDir()
	if _, err := readGroup(dir); err == nil {
		t.Error("a folder without run.pgid was read")
	}
	for _, bad := range []string{"0", "1", "-5", strconv.Itoa(os.Getpid()), strconv.Itoa(child.ProcessGroup()), "abc", "12 13", ""} {
		if err := os.WriteFile(filepath.Join(dir, runGroupFile), []byte(bad+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if n, err := readGroup(dir); err == nil {
			t.Errorf("run.pgid %q was read as %d", bad, n)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, runGroupFile), []byte("54321\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := readGroup(dir); err != nil || n != 54321 {
		t.Errorf("run.pgid 54321 was read as %d, %v", n, err)
	}
}
