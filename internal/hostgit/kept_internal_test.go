package hostgit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/places"
)

// TestAKeptCheckHoldsTheOtherPlaceBeforeItReadsIt records a checkout and its linked
// worktree as two places, the worktree's git directory and common directory in the
// checkout's, and checks the worktree until the check keeps what it read. A walled run
// then starts in the checkout and writes nothing, so the files hash as they did: the
// kept check in the worktree refuses it as in use without reading the files.
func TestAKeptCheckHoldsTheOtherPlaceBeforeItReadsIt(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(home))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	Forget()
	t.Cleanup(Forget)
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	main := filepath.Join(home, "main")
	worktree := filepath.Join(home, "feature")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(main, "init", "-q", "--initial-branch=main")
	git(main, "-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "-q", "--allow-empty", "-m", "first")
	git(main, "worktree", "add", "-q", "-b", "feature", worktree)
	ctx := context.Background()
	for _, p := range []string{main, worktree} {
		if err := Record(ctx, p, places.KindCheckout, "run-1"); err != nil {
			t.Fatal(err)
		}
	}
	read := false
	before := same
	same = func(sums map[string]string) bool { read = true; return before(sums) }
	t.Cleanup(func() { same = before })
	// The first check makes the lock files of the places, and the second reads the
	// records again since; the third keeps what the second read.
	for i := 0; i < 3; i++ {
		read = false
		h, err := check(ctx, worktree)
		if err != nil {
			t.Fatalf("check %d with no run: %v", i+1, err)
		}
		h.Release()
	}
	if !read {
		t.Fatal("the third check did not keep what the second read")
	}
	live, err := places.Starting([]string{main})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Release()
	read = false
	var r *RefusedError
	if h, err := check(ctx, worktree); !errors.As(err, &r) || r.Reason != RefusedInUse {
		h.Release()
		t.Errorf("got %v, want the in-use refusal", err)
	}
	if read {
		t.Error("the kept check read the checkout's files before it held the checkout")
	}
	if _, ok := state.verified[worktree]; !ok {
		t.Error("the check did not keep what it read: the records were read again")
	}
}
