package codex

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolvedPathTakesTheCaseOnDisk resolves a path whose folder is written in another
// case than on disk, and whose last name does not exist: the folder gets the case on
// disk, and the rest of the path stays as written. It skips on a volume that tells the
// cases apart.
func TestResolvedPathTakesTheCaseOnDisk(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "Home"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "home")); err != nil {
		t.Skip("the volume tells the cases apart")
	}
	if got, want := resolvedPath(filepath.Join(base, "home", "Codex")), filepath.Join(base, "Home", "Codex"); got != want {
		t.Errorf("resolvedPath %q, want %q", got, want)
	}
}
