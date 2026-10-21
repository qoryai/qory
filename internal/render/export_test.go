package render

import (
	"os"
	"path/filepath"

	"github.com/qoryai/qory/internal/compose"
)

// CopyModule is [copyModule] for the tests: the directory of m copied to dst, the way a
// compose writes modules/<name>, through a root at the directory dst is in.
func CopyModule(m compose.Module, dst string) error {
	out, err := os.OpenRoot(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer out.Close()
	return copyModule(m, out, filepath.Base(dst))
}

// CopyEntry is [copyEntry] for the tests: the file or directory at path in m copied to
// dst, with no reference to resolve, through a root at the directory dst is in.
func CopyEntry(m compose.Module, path, dst string) error {
	out, err := os.OpenRoot(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer out.Close()
	return copyEntry(m, path, out, filepath.Base(dst), nil)
}

// ReadEntry is [readEntry] for the tests, outside a build.
func ReadEntry(m compose.Module, path string) ([]byte, error) {
	return readEntry(m, path)
}

// SetMaxModuleBytes sets the most bytes one copy writes to n until the test ends.
func SetMaxModuleBytes(t interface{ Cleanup(func()) }, n int64) {
	old := maxModuleBytes
	maxModuleBytes = n
	t.Cleanup(func() { maxModuleBytes = old })
}
