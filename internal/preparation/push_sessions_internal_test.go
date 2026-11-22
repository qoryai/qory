package preparation

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// onRemote is the commit of the branch ref in the stand-in forge's repository path, "" when
// it has no such branch.
func onRemote(t *testing.T, path, ref string) string {
	t.Helper()
	bare := filepath.Join(strings.TrimPrefix(baseURL, "file://"), filepath.FromSlash(path)+".git")
	return run(t, bare, "for-each-ref", "--format=%(objectname)", "refs/heads/"+ref)
}

// sessionCommits stands in for a session's agent: it writes file and commits it in dir.
func sessionCommits(t *testing.T, dir, file string) string {
	t.Helper()
	write(t, filepath.Join(dir, file), file+"\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "--quiet", "-m", "Add "+file)
	return run(t, dir, "rev-parse", "HEAD")
}

// TestEachSessionsCommitsAreOnTheBranchBeforeTheNext is two sessions of one run, with a
// Push after each: the commit of session 1 is on the forge's branch before session 2
// starts, and the second Push adds only session 2's commit on top of it, and returns a
// document at that commit.
func TestEachSessionsCommitsAreOnTheBranchBeforeTheNext(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	start := b.Repositories[0].Commit
	first := sessionCommits(t, dir, "first.md")
	if _, lines, err := pushOf(dir, b); err != nil {
		t.Fatalf("the push after session 1: %v\n%s", err, lines)
	}
	if got := onRemote(t, "acme/shop", b.Ref); got != first {
		t.Fatalf("before session 2 the branch is at %q, want session 1's commit %s", got, first)
	}
	second := sessionCommits(t, dir, "second.md")
	doc, lines, err := pushOf(dir, b)
	if err != nil {
		t.Fatalf("the push after session 2: %v\n%s", err, lines)
	}
	if got := onRemote(t, "acme/shop", b.Ref); got != second {
		t.Errorf("after session 2 the branch is at %q, want %s", got, second)
	}
	bare := filepath.Join(strings.TrimPrefix(baseURL, "file://"), "acme", "shop.git")
	if got := run(t, bare, "log", "--format=%s", start+".."+second); got != "Add second.md\nAdd first.md" {
		t.Errorf("the branch's commits of the run are:\n%s", got)
	}
	if want := fmt.Sprintf(textPushed, b.Ref, "acme/shop", second[:12]); strings.TrimSpace(lines) != want {
		t.Errorf("the lines of the push after session 2 are %q, want %q", lines, want)
	}
	if doc == nil || len(doc.Repositories) != 1 || doc.Repositories[0].Commit != second {
		t.Errorf("the document after session 2 is %+v", doc)
	}
	if b.PushedPaths() != "acme/shop" {
		t.Errorf("the pushed paths are %q", b.PushedPaths())
	}
	if got, want := b.Landed(), []Landed{{System: b.system, Path: "acme/shop", Commit: second}}; !slices.Equal(got, want) {
		t.Errorf("the landed repositories are %+v, want %+v", got, want)
	}
}

// TestASessionWithNoCommitsPushesNothing is a session after one whose commit was pushed,
// that changed nothing: its Push pushes nothing, returns no document and prints the line
// of no new commits, and the branch stays at the commit the first Push left.
func TestASessionWithNoCommitsPushesNothing(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	first := sessionCommits(t, dir, "first.md")
	if _, lines, err := pushOf(dir, b); err != nil {
		t.Fatalf("the push after session 1: %v\n%s", err, lines)
	}
	doc, lines, err := pushOf(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	if doc != nil {
		t.Errorf("the push after a session with no commits returned %+v", doc)
	}
	if strings.TrimSpace(lines) != textNoNewCommits {
		t.Errorf("the lines are %q, want %q", lines, textNoNewCommits)
	}
	if got := onRemote(t, "acme/shop", b.Ref); got != first {
		t.Errorf("the branch is at %q, want %s", got, first)
	}
	if b.PushedPaths() != "" {
		t.Errorf("the pushed paths are %q, want none", b.PushedPaths())
	}
	if got := b.Landed(); len(got) != 0 {
		t.Errorf("the landed repositories are %+v, want none", got)
	}
}

// TestALaterSessionThatLeavesThePushedCommitIsRefused is a session that moves HEAD off the
// commit that the push after the session before it left on the branch: its Push stops as
// one that would not be a fast-forward from that commit, and pushes nothing.
func TestALaterSessionThatLeavesThePushedCommitIsRefused(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	start := b.Repositories[0].Commit
	first := sessionCommits(t, dir, "first.md")
	if _, lines, err := pushOf(dir, b); err != nil {
		t.Fatalf("the push after session 1: %v\n%s", err, lines)
	}
	run(t, dir, "reset", "--quiet", "--hard", start)
	other := sessionCommits(t, dir, "other.md")
	_, _, err := pushOf(dir, b)
	want := fmt.Sprintf(textNotFastForward, "acme/shop", other[:12], first[:12])
	if err == nil || err.Error() != want {
		t.Fatalf("the push after session 2: %v, want %q", err, want)
	}
	if got := onRemote(t, "acme/shop", b.Ref); got != first {
		t.Errorf("the branch is at %q, want %s", got, first)
	}
}
