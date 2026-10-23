package explore_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/qoryai/qory/internal/pool/explore"
)

// testRunID is a run id.
const testRunID = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"

// sum is the SHA-256 of b, in hex.
func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestRecordDir(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "elsewhere"))
	work := filepath.Join(root, "work")
	os.Mkdir(work, 0o700)
	link := filepath.Join(root, "link")
	os.Symlink(work, link)
	state := filepath.Join(root, "run", "state")
	want := filepath.Join(state, "qory", "runs", "work-"+sum([]byte(work))[:12], testRunID)
	for _, w := range []string{work, link} {
		if got, err := explore.RecordDir(state, w, testRunID); err != nil || got != want {
			t.Errorf("RecordDir(%s) %s, %v; want %s", w, got, err, want)
		}
	}
	if _, err := explore.RecordDir("state", work, testRunID); err == nil {
		t.Error("a record folder under a relative state directory")
	}
}

func TestRemove(t *testing.T) {
	var dropped []string
	defer explore.SetDropPlace(func(dir string) { dropped = append(dropped, dir) })()
	state := t.TempDir()
	dir := explore.Dir(state, testRunID)
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := explore.Remove(state, testRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the run directory is still there: %v", err)
	}
	if !slices.Equal(dropped, []string{dir}) {
		t.Errorf("places dropped %q, want %q", dropped, dir)
	}
	if err := explore.Remove(state, "../pool"); err == nil {
		t.Error("removed a directory by a path that is no run id")
	}
	if _, err := os.Stat(filepath.Join(state, "pool")); err != nil {
		t.Errorf("the pool's directory went: %v", err)
	}
}
