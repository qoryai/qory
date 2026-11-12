package worktree_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/worktree"
)

// TestMainReturnsAGitTooOld puts first on the PATH a git that prints version 2.34.1:
// no git runs, and Main returns the line for it rather than saying the folder is outside
// a git working tree.
func TestMainReturnsAGitTooOld(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, err := worktree.Main(root); err != nil || got != root {
		t.Fatalf("with git: %q, %v", got, err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'git version 2.34.1'; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var old *hostgit.GitVersionError
	if _, err := worktree.Main(root); !errors.As(err, &old) {
		t.Errorf("got %v, want the line for git 2.34.1", err)
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
