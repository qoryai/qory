package worktree_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/worktree"
)

// TestMainReturnsARecordThatCannotBeRead puts a file in the directory of the records of
// the places that is no record: the settings check cannot read the records, so no git
// runs, and Main returns that error rather than saying the folder is outside a git
// working tree.
func TestMainReturnsARecordThatCannotBeRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hostgit.Forget()
	t.Cleanup(hostgit.Forget)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, err := worktree.Main(root); err != nil || got != root {
		t.Fatalf("with no record: %q, %v", got, err)
	}
	dir, err := places.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not a record"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostgit.Forget()
	_, want := places.Load()
	if want == nil {
		t.Fatal("the broken record reads")
	}
	if _, err := worktree.Main(root); err == nil || err.Error() != want.Error() {
		t.Errorf("got %v, want %v", err, want)
	}
}

// TestMainAndListReadAPathWithALineEndWhole lists the working trees of a checkout and of
// a worktree whose folder names have a line end in them: git lists them with -z, and
// Main and List return the whole paths.
func TestMainAndListReadAPathWithALineEndWhole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(home))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hostgit.Forget()
	t.Cleanup(hostgit.Forget)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "a\nb")
	wt := filepath.Join(base, "w\nx")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "--initial-branch=main"},
		{"-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "-q", "--allow-empty", "-m", "first"},
		{"worktree", "add", "-q", "-b", "feature", wt},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if got, err := worktree.Main(wt); err != nil || got != root {
		t.Errorf("main %q, %v, want %q", got, err, root)
	}
	entries, err := worktree.List(root)
	if err != nil || len(entries) != 2 || entries[0].Path != root || entries[1].Path != wt || entries[1].Branch != "feature" {
		t.Errorf("entries %+v, %v, want %q and %q on feature", entries, err, root, wt)
	}
}
