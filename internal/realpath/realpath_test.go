package realpath_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/realpath"
)

// TestAFolderIsOneFolderInAnyCase reads a folder named Shared through one case of its
// name, on a file system that ignores case, such as macOS's: Real returns the name as
// the folder that contains it lists it, and Inside finds a file in it through either
// case. A file system that tells the two apart, such as Linux's, has them as two
// folders, so the test skips there.
func TestAFolderIsOneFolderInAnyCase(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	written := filepath.Join(tmp, "Shared")
	if err := os.Mkdir(written, 0o755); err != nil {
		t.Fatal(err)
	}
	lower := filepath.Join(tmp, strings.ToLower("Shared"))
	a, errA := os.Stat(written)
	b, errB := os.Stat(lower)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Skip("the file system tells names apart by case")
	}
	if got := realpath.Real(lower); got != written {
		t.Errorf("Real(%s) = %s, want %s", lower, got, written)
	}
	if !realpath.Inside(written, filepath.Join(lower, "qory.yaml")) {
		t.Errorf("%s is not inside %s", filepath.Join(lower, "qory.yaml"), written)
	}
}

// TestALinkOutOfTheFolderIsNotInside is a folder inside a folder as written that leads
// out of it through a link: Inside is false for it, and true for a path that has no link.
func TestALinkOutOfTheFolderIsNotInside(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	place, outside := filepath.Join(tmp, "place"), filepath.Join(tmp, "outside")
	for _, dir := range []string{place, outside, filepath.Join(outside, "stack")} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(place, "out")); err != nil {
		t.Fatal(err)
	}
	if realpath.Inside(place, filepath.Join(place, "out", "stack")) {
		t.Error("a path through the link out is inside")
	}
	if !realpath.Inside(place, filepath.Join(place, "stack")) {
		t.Error("a plain path is not inside")
	}
	if got := realpath.Real(filepath.Join(place, "out")); got != outside {
		t.Errorf("Real of the link: %s, want %s", got, outside)
	}
}
