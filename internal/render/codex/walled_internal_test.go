package codex

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteNewWritesThroughNoLink is a link to a file elsewhere at the path writeNew
// writes: the write fails, and the file the link leads to is unchanged.
func TestWriteNewWritesThroughNoLink(t *testing.T) {
	files, outside := t.TempDir(), filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(files, "hooks.json")
	if err := os.Symlink(outside, p); err != nil {
		t.Fatal(err)
	}
	if err := writeNew(p, []byte(noHooks), 0o644); err == nil {
		t.Fatal("writeNew wrote through a link")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "theirs\n" {
		t.Errorf("the link's target: %q %v, want it unchanged", b, err)
	}
}
