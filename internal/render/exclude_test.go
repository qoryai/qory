package render_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestTheExcludeFileIsReadOnlyAsARegularFile puts a folder where the exclude file goes:
// adding a line and taking one out are each refused, and nothing is written there.
func TestTheExcludeFileIsReadOnlyAsARegularFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	file := filepath.Join(root, ".git", "info", "exclude")
	if err := os.RemoveAll(file); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(file, 0o755); err != nil {
		t.Fatal(err)
	}
	want := file + " is not a regular file; qory reads the exclude file only when it is one"
	if err := render.Exclude(root, "/.qory"); err == nil || err.Error() != want {
		t.Errorf("Exclude: %v, want %q", err, want)
	}
	if err := render.RemoveExclude(root, "/.qory"); err == nil || err.Error() != want {
		t.Errorf("RemoveExclude: %v, want %q", err, want)
	}
	if entries, _ := os.ReadDir(file); len(entries) != 0 {
		t.Errorf("written into the folder: %v", entries)
	}
}

// TestASocketAtTheExcludeFileIsNotOpened puts a socket where the exclude file goes: it
// is refused as no regular file before it is opened, as a device there would be. The
// checkout lies in a folder of a short path, which a socket's needs.
func TestASocketAtTheExcludeFileIsNotOpened(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	short, err := os.MkdirTemp("", "x")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	root, err := filepath.EvalSymlinks(short)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	file := filepath.Join(root, ".git", "info", "exclude")
	if err := os.RemoveAll(file); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", file)
	if err != nil {
		t.Skipf("no socket at %s: %v", file, err)
	}
	defer l.Close()
	want := file + " is not a regular file; qory reads the exclude file only when it is one"
	if err := render.Exclude(root, "/.qory"); err == nil || err.Error() != want {
		t.Errorf("Exclude: %v, want %q", err, want)
	}
	if err := render.RemoveExclude(root, "/.qory"); err == nil || err.Error() != want {
		t.Errorf("RemoveExclude: %v, want %q", err, want)
	}
}

// TestALinkAtTheExcludeFileIsQuoted leads the exclude file, and then its folder, through
// a link whose target holds a line break and an escape: a walled run could have written
// either, so the refusal quotes the target rather than print it to the terminal as it is.
func TestALinkAtTheExcludeFileIsQuoted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	info := filepath.Join(root, ".git", "info")
	file := filepath.Join(info, "exclude")
	target := filepath.Join(t.TempDir(), "x\n\x1b[31mred")
	if err := os.RemoveAll(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, file); err != nil {
		t.Fatal(err)
	}
	want := file + " is a link to " + strconv.Quote(target) + "; qory reads the exclude file only when it is no link"
	if err := render.Exclude(root, "/.qory"); err == nil || err.Error() != want {
		t.Errorf("a link at the file: %v, want %q", err, want)
	}
	if err := os.RemoveAll(info); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, info); err != nil {
		t.Fatal(err)
	}
	want = info + " is a link to " + strconv.Quote(target) + "; qory writes the exclude file only in a folder that is no link"
	if err := render.Exclude(root, "/.qory"); err == nil || err.Error() != want {
		t.Errorf("a link at the folder: %v, want %q", err, want)
	}
}
