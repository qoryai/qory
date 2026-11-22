package preparation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitIn writes name with content in the repository at dir and commits it as one
// commit, and returns the commit.
func commitIn(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", name)
	run(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", name)
	return run(t, dir, "rev-parse", "HEAD")
}

// TestScopeCloneHoldsTheCommitsAlone is a working folder whose agent wrote a remote branch
// and a tag: the clone has head checked out detached, and no ref, branch or tag of the
// folder; a head that does not descend from the session's start stops the clone.
func TestScopeCloneHoldsTheCommitsAlone(t *testing.T) {
	folder := emptyFolder(t)
	start := commitIn(t, folder, "a.txt", "a\n")
	head := commitIn(t, folder, "b.txt", "b\n")
	run(t, folder, "update-ref", "refs/remotes/origin/main", start)
	run(t, folder, "tag", "v1", start)
	clone := filepath.Join(t.TempDir(), "clone")
	r := WorkRepository{Path: "acme/shop", Dir: ".", Commit: start, Start: start}
	if err := ScopeClone(t.Context(), os.Environ(), folder, clone, r, head); err != nil {
		t.Fatal(err)
	}
	if got := run(t, clone, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD of the clone is %s, want %s", got, head)
	}
	if refs := run(t, clone, "for-each-ref"); refs != "" {
		t.Errorf("the clone has refs:\n%s", refs)
	}
	if _, err := os.Stat(filepath.Join(clone, ".git", "hooks")); !os.IsNotExist(err) {
		t.Errorf("the clone has a hooks folder: %v", err)
	}

	other := emptyFolder(t)
	elsewhere := commitIn(t, other, "c.txt", "c\n")
	run(t, folder, "fetch", "--quiet", other, "master")
	err := ScopeClone(t.Context(), os.Environ(), folder, filepath.Join(t.TempDir(), "clone"), r, elsewhere)
	if err == nil || !strings.Contains(err.Error(), "does not descend from commit "+start[:12]) {
		t.Errorf("a head off the start: %v", err)
	}
}

// TestScopeCloneReadsAShallowCheckout is a checkout fetched at depth one, as a run's is:
// the clone of a commit past its start passes.
func TestScopeCloneReadsAShallowCheckout(t *testing.T) {
	origin := emptyFolder(t)
	commitIn(t, origin, "a.txt", "a\n")
	commitIn(t, origin, "b.txt", "b\n")
	folder := filepath.Join(t.TempDir(), "work")
	run(t, filepath.Dir(folder), "clone", "--quiet", "--depth", "1", "file://"+origin, folder)
	start := run(t, folder, "rev-parse", "HEAD")
	head := commitIn(t, folder, "c.txt", "c\n")
	clone := filepath.Join(t.TempDir(), "clone")
	if err := ScopeClone(t.Context(), os.Environ(), folder, clone, WorkRepository{Commit: start, Start: start}, head); err != nil {
		t.Fatal(err)
	}
	if got := run(t, clone, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD of the clone is %s, want %s", got, head)
	}
}

// TestChangedPathFindsTheFirstChange lists the paths of the commits past a start: none
// for an empty commit, and the first changed path, oldest commit first, otherwise.
func TestChangedPathFindsTheFirstChange(t *testing.T) {
	dir := emptyFolder(t)
	start := commitIn(t, dir, "a.txt", "a\n")
	run(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "empty")
	empty := run(t, dir, "rev-parse", "HEAD")
	if c, p, err := ChangedPath(t.Context(), os.Environ(), dir, start, empty); c != "" || p != "" || err != nil {
		t.Errorf("an empty commit: %q, %q, %v", c, p, err)
	}
	first := commitIn(t, dir, "src.go", "package src\n")
	commitIn(t, dir, "z.txt", "z\n")
	if c, p, err := ChangedPath(t.Context(), os.Environ(), dir, start, run(t, dir, "rev-parse", "HEAD")); c != first || p != "src.go" || err != nil {
		t.Errorf("got %q, %q, %v, want %s, src.go", c, p, err, first)
	}
}
