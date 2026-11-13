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

// TestWalledLimitIsAModuleEntrysLimit is the limit of the walled copy of rules: the
// numbers of a copy of one entry of a module, 10,000 entries, 1 GiB and 64 MiB a file,
// with the walled refusals.
func TestWalledLimitIsAModuleEntrysLimit(t *testing.T) {
	l := walledLimit("rules")
	if l.Entries != 10_000 || l.Bytes != 1<<30 || l.FileBytes != 64<<20 {
		t.Errorf("limit %d entries, %d bytes, %d a file; want 10000, 1 GiB, 64 MiB", l.Entries, l.Bytes, l.FileBytes)
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{l.TooMany, "Ship fewer files in the modules. codex/rules contains more than 10,000 files, directories and links, the most a walled Codex run takes."},
		{l.TooManyBytes, "Ship fewer files in the modules. The files of codex/rules add up to more than 1 GiB, the most a walled Codex run takes."},
		{l.TooLarge("rules/big.rules"), "Ship codex/rules/big.rules under 64 MiB. The file is larger than 64 MiB, the most a walled Codex run takes for one file."},
	} {
		if c.err == nil || c.err.Error() != c.want {
			t.Errorf("refusal %v, want %q", c.err, c.want)
		}
	}
}

// TestWriteWalledHomeKeepsRulesAndAgentsToTheLimit writes a walled home from a composed
// home whose skills, rules and agents each have three files of 4 bytes and a .git folder.
// With the limit of one folder lowered to two entries, to 10 bytes, or to 3 bytes a file,
// the write fails with the walled refusal for that folder and that limit. With every
// limit as it is, the write succeeds, and .git is left out of every copy. The skills
// folder gets no such limit.
func TestWriteWalledHomeKeepsRulesAndAgentsToTheLimit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home", "codex")
	for _, folder := range []string{"skills/review", "rules", "agents"} {
		for _, name := range []string{"a", "b", "c", ".git/HEAD"} {
			p := filepath.Join(dir, folder, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("text"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	real := walledLimit
	t.Cleanup(func() { walledLimit = real })
	for _, c := range []struct {
		name, lowered string
		entries       int
		bytes, file   int64
		want          string
	}{
		{"rules entries", "rules", 2, 100, 100, "Ship fewer files in the modules. codex/rules contains more than 10,000 files, directories and links, the most a walled Codex run takes."},
		{"agents entries", "agents", 2, 100, 100, "Ship fewer files in the modules. codex/agents contains more than 10,000 files, directories and links, the most a walled Codex run takes."},
		{"agents bytes", "agents", 10, 10, 100, "Ship fewer files in the modules. The files of codex/agents add up to more than 1 GiB, the most a walled Codex run takes."},
		{"agents file", "agents", 10, 100, 3, "Ship codex/agents/a under 64 MiB. The file is larger than 64 MiB, the most a walled Codex run takes for one file."},
		{"skills", "skills", 2, 10, 3, ""},
		{"within", "", 0, 0, 0, ""},
	} {
		t.Run(strings.ReplaceAll(c.name, " ", "-"), func(t *testing.T) {
			walledLimit = func(name string) *render.CopyLimit {
				l := real(name)
				if name == c.lowered {
					l.Entries, l.Bytes, l.FileBytes = c.entries, c.bytes, c.file
				}
				return l
			}
			files := filepath.Join(t.TempDir(), "fixed")
			_, err := WriteWalledHome(dir, files, WalledRun{})
			if c.want != "" {
				if err == nil || err.Error() != c.want {
					t.Errorf("WriteWalledHome: %v, want %q", err, c.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, folder := range []string{"skills/review", "rules", "agents"} {
				if _, err := os.Lstat(filepath.Join(files, folder, ".git")); !os.IsNotExist(err) {
					t.Errorf("%s/.git: %v, want it left out", folder, err)
				}
			}
		})
	}
}
