//go:build unix

package suite_test

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/qoryai/qory/internal/suite"
)

// TestReadResultDoesNotBlockOnAPipe is a result file that is a named pipe: the read returns at
// once with no file, where opening it would wait for a writer that never comes.
func TestReadResultDoesNotBlockOnAPipe(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.xml"), 0o600); err != nil {
		t.Skipf("no fifo: %v", err)
	}
	if st, data := suite.ReadResult(dir, "pipe.xml"); st.Exists || data != nil {
		t.Errorf("a pipe was read: exists %v, %d bytes", st.Exists, len(data))
	}
}
