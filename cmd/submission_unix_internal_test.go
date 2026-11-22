//go:build unix

package cmd

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// TestAPipeAsTheHandoffFileIsNotWaitedFor is a named pipe at the path of the pull-request
// output: the read ends with an error and no output, with no wait for a writer of the pipe.
func TestAPipeAsTheHandoffFileIsNotWaitedFor(t *testing.T) {
	root := handoffIn(t, "{}")
	if err := os.Remove(handoffPath(root)); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(handoffPath(root), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		h, err := readHandoff(root)
		if h != nil {
			t.Errorf("the hand-off is %+v", h)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || err.Error() != "the path is not a regular file" {
			t.Errorf("the error is %v, want that the path is not a regular file", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the read waits on the pipe")
	}
}
