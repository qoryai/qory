//go:build unix

package machinestep

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestLinesOfAFIFO is a FIFO at the path, which no process writes: Lines returns at
// once, with the error of a file that is not a regular one.
func TestLinesOfAFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "steps.jsonl")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Lines(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || err.Error() != path+" is not a regular file" {
			t.Errorf("a FIFO: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Lines waited on the FIFO")
	}
}
