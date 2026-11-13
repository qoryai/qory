package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
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

// TestCopyTreeKeepsToItsLimit copies a folder of three files of 4 bytes each with a
// limit of two entries, of 10 bytes, and of 3 bytes a file: each copy fails with the
// limit's refusal for that limit. Within every limit the copy succeeds.
func TestCopyTreeKeepsToItsLimit(t *testing.T) {
	src := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("text"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tooMany, tooManyBytes, tooLarge := errors.New("too many"), errors.New("too many bytes"), errors.New("too large")
	limit := func(entries int, bytes, file int64) *render.CopyLimit {
		return &render.CopyLimit{Entries: entries, Bytes: bytes, FileBytes: file, TooMany: tooMany, TooManyBytes: tooManyBytes, TooLarge: func(string) error { return tooLarge }}
	}
	for _, c := range []struct {
		name  string
		limit *render.CopyLimit
		want  error
	}{
		{"entries", limit(2, 100, 100), tooMany},
		{"bytes", limit(10, 10, 100), tooManyBytes},
		{"file", limit(10, 100, 3), tooLarge},
		{"within", limit(3, 12, 4), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := copyTree(src, t.TempDir(), "skills", []string{src}, c.limit)
			if !errors.Is(err, c.want) {
				t.Errorf("copyTree: %v, want %v", err, c.want)
			}
		})
	}
}

// TestCopyTreeShowsNoAbsolutePath copies a folder with a link that leads nowhere: the
// error starts with the copy's prefix and the link's path in the copy, and contains no
// path of this machine.
func TestCopyTreeShowsNoAbsolutePath(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink(filepath.Join(src, "gone"), filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	err := copyTree(src, t.TempDir(), "skills", []string{src}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "copy skills/link for the Codex home: ") || strings.Contains(err.Error(), src) {
		t.Errorf("copyTree: %v, want the prefix and no path of this machine", err)
	}
}
