package places_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/places"
)

// TestARelativeStateHomeIsRefused is an XDG_STATE_HOME that is no absolute path: the
// state directory, the directory of the records and the records themselves are the
// refusal, never a folder under the working folder or the default one.
func TestARelativeStateHomeIsRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "state")
	places.Forget()
	t.Cleanup(places.Forget)
	if dir, err := places.StateDir(); dir != "" || !errors.Is(err, places.ErrRelativeStateHome) {
		t.Errorf("StateDir: %q, %v", dir, err)
	}
	if dir, err := places.Dir(); dir != "" || !errors.Is(err, places.ErrRelativeStateHome) {
		t.Errorf("Dir: %q, %v", dir, err)
	}
	if recs, err := places.Cached(); recs != nil || !errors.Is(err, places.ErrRelativeStateHome) {
		t.Errorf("Cached: %v, %v", recs, err)
	}
}

// TestNoStateDirectoryIsNoRecordOfNothing is HOME and XDG_STATE_HOME both unset: the
// records cannot be read, and Cached says so in place of reading as no record.
func TestNoStateDirectoryIsNoRecordOfNothing(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	places.Forget()
	t.Cleanup(places.Forget)
	if _, err := places.StateDir(); !errors.Is(err, places.ErrNoStateDir) {
		t.Errorf("StateDir: %v", err)
	}
	if recs, err := places.Cached(); recs != nil || !errors.Is(err, places.ErrNoStateDir) {
		t.Errorf("Cached: %v, %v", recs, err)
	}
	if _, err := places.Load(); !errors.Is(err, places.ErrNoStateDir) {
		t.Errorf("Load: %v", err)
	}
}

// caseFolded makes the folder name in a temporary directory, with the state directory
// and home there, and returns it as written, and as written with its last name in lower
// case. A file system that tells the two apart, such as Linux's, has them as two places,
// so the test skips there.
func caseFolded(t *testing.T, name string) (written, lower string) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	places.Forget()
	t.Cleanup(places.Forget)
	written = filepath.Join(tmp, name)
	if err := os.Mkdir(written, 0o755); err != nil {
		t.Fatal(err)
	}
	lower = filepath.Join(tmp, strings.ToLower(name))
	a, errA := os.Stat(written)
	b, errB := os.Stat(lower)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Skip("the file system tells names apart by case")
	}
	return written, lower
}

// TestAPlaceIsOnePlaceInAnyCase records a folder named Shared through one case of its
// name, on a file system that ignores case, such as macOS's: the record and the lock of
// the place are found through the other case, and Real returns the name as the folder
// that contains it lists it.
func TestAPlaceIsOnePlaceInAnyCase(t *testing.T) {
	written, lower := caseFolded(t, "Shared")
	if got := places.Real(lower); got != written {
		t.Errorf("Real(%s) = %s, want %s", lower, got, written)
	}
	if places.Name(lower) != places.Name(written) {
		t.Errorf("Name: %s and %s", places.Name(lower), places.Name(written))
	}
	if err := places.Update(lower, places.KindMount, func(r *places.Record) error { r.Run = "run-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	r, err := places.Get(written)
	if err != nil || r == nil || r.Path != written || r.Run != "run-1" {
		t.Fatalf("the record through %s: %+v, %v", written, r, err)
	}
	live, err := places.Starting([]string{written})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Release()
	h := &places.Hold{}
	defer h.Release()
	if busy, err := places.InUse(context.Background(), h, lower); err != nil || !busy {
		t.Errorf("the place through %s while a run starts in %s: %v, %v", lower, written, busy, err)
	}
}

// TestARecordWrittenInAnotherCaseIsFound writes the record of a folder named Shared as
// a qory that kept the case as typed wrote it, with the path in lower case and its file
// named by that path. On a file system that ignores case, the record is found through
// the name as the folder lists it: by Get, by Containing, and by Update, which changes
// that record and adds none.
func TestARecordWrittenInAnotherCaseIsFound(t *testing.T) {
	written, lower := caseFolded(t, "Shared")
	dir, err := places.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(lower))
	old := filepath.Join(dir, filepath.Base(lower)+"-"+hex.EncodeToString(sum[:])[:12]+".json")
	data, err := json.Marshal(places.Record{Version: places.Version, Path: lower, Kind: places.KindMount, Run: "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := places.Get(written); err != nil || r == nil || r.Run != "run-1" {
		t.Errorf("Get(%s): %+v, %v", written, r, err)
	}
	recs, err := places.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := places.Containing(recs, filepath.Join(written, "data")); len(got) != 1 {
		t.Errorf("Containing: %+v", got)
	}
	if err := places.Update(written, places.KindMount, func(r *places.Record) error { r.Run = "run-2"; return nil }); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e.Name())
		}
	}
	if len(files) != 1 || files[0] != filepath.Base(old) {
		t.Errorf("the records: %v, want [%s]", files, filepath.Base(old))
	}
	if r, err := places.Get(lower); err != nil || r == nil || r.Run != "run-2" {
		t.Errorf("Get(%s) after the update: %+v, %v", lower, r, err)
	}
}
