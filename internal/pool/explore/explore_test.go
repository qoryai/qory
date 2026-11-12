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
	if err := explore.Remove(state, "../pool"); err == nil {
		t.Error("removed a directory by a path that is no run id")
	}
	if _, err := os.Stat(filepath.Join(state, "pool")); err != nil {
		t.Errorf("the pool's directory went: %v", err)
	}
}

// TestRecordedRuns lists the run id of each record folder of RecordDir's layout, and
// leaves out a file, a folder whose name is no run id, and a run id at another depth.
// A state folder with no records lists nothing.
func TestRecordedRuns(t *testing.T) {
	state := t.TempDir()
	if ids := explore.RecordedRuns(state); len(ids) != 0 {
		t.Errorf("an empty state folder lists %q", ids)
	}
	work := t.TempDir()
	record, err := explore.RecordDir(state, work, testRunID)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(record), "not-a-run")
	deeper := filepath.Join(record, "0191f2a4-3c5e-7b8d-9e0f-000000000000")
	for _, d := range []string{record, other, deeper} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(record), "0191f2a4-3c5e-7b8d-9e0f-111111111111"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ids := explore.RecordedRuns(state); !slices.Equal(ids, []string{testRunID}) {
		t.Errorf("the runs are %q, want %q", ids, testRunID)
	}
}
