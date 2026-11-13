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

// TestCopyFileWritesOverNothing is a file, and a link to a file elsewhere, at the path
// copyFile writes: the copy fails, and the file there and the file the link leads to
// are unchanged.
func TestCopyFileWritesOverNothing(t *testing.T) {
	src, outside := filepath.Join(t.TempDir(), "SKILL.md"), filepath.Join(t.TempDir(), "target")
	for p, text := range map[string]string{src: "skill\n", outside: "theirs\n"} {
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	file, link := filepath.Join(dir, "file.md"), filepath.Join(dir, "link.md")
	if err := os.WriteFile(file, []byte("there\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dst, read, want string }{
		{file, file, "there\n"},
		{link, outside, "theirs\n"},
	} {
		if err := copyFile(src, c.dst, 0o644); err == nil {
			t.Errorf("copyFile to %s: no error, want one", filepath.Base(c.dst))
		}
		if b, err := os.ReadFile(c.read); err != nil || string(b) != c.want {
			t.Errorf("%s: %q %v, want it unchanged", filepath.Base(c.read), b, err)
		}
	}
}
