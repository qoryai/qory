package preparation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// bareCommit makes a commit on top of parent in the bare repository bare, with the tree of
// parent, and points branch at it. It returns the commit.
func bareCommit(t *testing.T, bare, branch, parent, message string) string {
	t.Helper()
	tree := run(t, bare, "rev-parse", parent+"^{tree}")
	commit := run(t, bare, "commit-tree", tree, "-p", parent, "-m", message)
	run(t, bare, "update-ref", "refs/heads/"+branch, commit)
	return commit
}

// leave writes a file that the agent left, so the push has a commit to make.
func leave(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, "left.txt"), "left\n")
}

// headError is the [*HeadChanged] that err is, failing the test for any other error.
func headError(t *testing.T, err error) *HeadChanged {
	t.Helper()
	var changed *HeadChanged
	if !errors.As(err, &changed) {
		t.Fatalf("error %v, want a head that changed", err)
	}
	return changed
}

// TestAMissingBranchTakesAFirstPushOnly is a branch that the forge does not have: a run whose
// start is the checkout pushes it, and a run that started from a version or a pushed
// commit stops with "none" as the head.
func TestAMissingBranchTakesAFirstPushOnly(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	leave(t, dir)
	if doc, _, err := pushOf(dir, b); err != nil || doc == nil {
		t.Fatalf("first push: %v", err)
	}
	dir, b = onBranch(t, "acme/shop")
	start := b.Repositories[0].Commit
	b.Version = versionOf("acme/shop", start)
	leave(t, dir)
	_, _, err := pushOf(dir, b)
	changed := headError(t, err)
	want := "qory run: Create the branch at the expected commit on the forge; it is not there.\n" +
		"  Branch qory/wi_1. Repository git.example.com/acme/shop. Head: none. Expected: " + start + ", the start commit. qory did not push.\n" +
		"  Run git push <remote> " + start + ":refs/heads/qory/wi_1, where <remote> is the remote that points to git.example.com/acme/shop; fetch the commit first if the checkout has no such commit."
	if err.Error() != want {
		t.Errorf("the refusal is\n%s\nwant\n%s", err, want)
	}
	if changed.Head != "" {
		t.Errorf("refusal %+v, want no head", changed)
	}
}

// TestAMissingBranchIsFreshPerRepository is a work item whose first submission lists the
// repository acme/lib alone: a later run that changes acme/shop for the first time pushes its
// branch, which the forge does not have, while a repository the version or a pushed commit
// lists is refused with "none" as the head.
func TestAMissingBranchIsFreshPerRepository(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(b *WorkBranch)
		fresh bool
	}{
		{"the version lists another repository", func(b *WorkBranch) {
			b.Version = versionOf("acme/lib", b.Repositories[1].Commit)
		}, true},
		{"a pushed commit of another repository", func(b *WorkBranch) {
			b.Pushed = []Landed{{System: "git.example.com", Path: "acme/lib", Commit: b.Repositories[1].Commit}}
		}, true},
		{"the version lists the repository", func(b *WorkBranch) {
			b.Version = versionOf("acme/shop", b.Repositories[0].Commit)
		}, false},
		{"a pushed commit of the repository", func(b *WorkBranch) {
			b.Pushed = []Landed{{System: "git.example.com", Path: "acme/shop", Commit: b.Repositories[0].Commit}}
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b := onBranch(t, "acme/shop,acme/lib")
			c.setup(b)
			leave(t, dir)
			doc, _, err := pushOf(dir, b)
			if c.fresh {
				if err != nil || doc == nil {
					t.Fatalf("the first push of acme/shop: %v", err)
				}
				return
			}
			if changed := headError(t, err); changed.Head != "" || changed.Path != "acme/shop" {
				t.Errorf("refusal %+v, want acme/shop with no head", changed)
			}
		})
	}
}

// TestAHeadAtTheExpectedCommitTakesAPlainPush is a second push of a run: the head is the
// run's last push, so the push is a fast-forward with no lease.
func TestAHeadAtTheExpectedCommitTakesAPlainPush(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	leave(t, dir)
	if _, _, err := pushOf(dir, b); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "more.txt"), "more\n")
	if doc, _, err := pushOf(dir, b); err != nil || doc == nil {
		t.Fatalf("second push: %v", err)
	}
}

// TestAHeadThatAnotherPushMovedStopsThePush is a branch with a commit that no cancelled run
// pushed, one or two on top of the start commit, and one that does not descend from it: the
// push stops, the branch stays, and the refusal has the head and the expected commit. The text is the
// same for each.
func TestAHeadThatAnotherPushMovedStopsThePush(t *testing.T) {
	for _, c := range []struct {
		name      string
		cancel    int // how many of the commits above the start a cancelled run pushed, the oldest first
		commits   int
		unrelated bool
	}{
		{"one commit of no cancelled run", 0, 1, false},
		{"the second of two commits is not a cancelled run's", 1, 2, false},
		{"the first of two commits is not a cancelled run's", 0, 2, false},
		{"a head that does not descend", 0, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b, base := onBranchIn(t, "acme/shop")
			bare := filepath.Join(base, "acme", "shop.git")
			start := b.Repositories[0].Commit
			head := start
			var made []string
			for i := 0; i < c.commits; i++ {
				head = bareCommit(t, bare, b.Ref, head, "other")
				made = append(made, head)
			}
			if c.unrelated {
				run(t, bare, "update-ref", "refs/heads/"+b.Ref, run(t, bare, "rev-parse", "main~1"))
			}
			for _, commit := range made[:c.cancel] {
				b.Cancelled = append(b.Cancelled, Landed{System: "git.example.com", Path: "acme/shop", Commit: commit})
			}
			leave(t, dir)
			_, _, err := pushOf(dir, b)
			changed := headError(t, err)
			wantHead := run(t, bare, "rev-parse", b.Ref)
			if changed.Head != wantHead || changed.Expected != start {
				t.Errorf("refusal %+v, want head %s expected %s", changed, wantHead, start)
			}
			if got := run(t, bare, "rev-parse", b.Ref); got != wantHead {
				t.Errorf("the branch moved to %s", got)
			}
			text := err.Error()
			if strings.Contains(text, "does not descend") || !strings.Contains(text, "Head: "+wantHead+". Expected: "+start+", the start commit.") {
				t.Errorf("the refusal is\n%s", text)
			}
		})
	}
}

// TestACancelledRunsCommitsAreReplacedWithALease is a branch with 2 commits of a cancelled
// run on top of the start commit: the first push replaces them, with a lease on the head,
// and the branch is the run's commit afterwards.
func TestACancelledRunsCommitsAreReplacedWithALease(t *testing.T) {
	dir, b, base := onBranchIn(t, "acme/shop")
	bare := filepath.Join(base, "acme", "shop.git")
	head := b.Repositories[0].Commit
	for range 2 {
		head = bareCommit(t, bare, b.Ref, head, "cancelled")
		b.Cancelled = append(b.Cancelled, Landed{System: "git.example.com", Path: "acme/shop", Commit: head})
	}
	leave(t, dir)
	doc, _, err := pushOf(dir, b)
	if err != nil || doc == nil {
		t.Fatalf("push: %v", err)
	}
	mine := run(t, dir, "rev-parse", "HEAD")
	if got := run(t, bare, "rev-parse", b.Ref); got != mine {
		t.Errorf("the branch is at %s, want the run's %s", got, mine)
	}
	if doc.Repositories[0].Base == nil || doc.Repositories[0].Base.Commit != b.Repositories[0].Start || len(doc.Repositories[0].Commits) != 1 {
		t.Errorf("the repository is %+v, want its base and one commit", doc.Repositories[0])
	}
}

// TestAHeadMovedAfterTheLeaseWasReadStopsThePush is a push whose lease is a head that
// another push has moved since: the forge rejects it, and the branch is as the other push left it.
func TestAHeadMovedAfterTheLeaseWasReadStopsThePush(t *testing.T) {
	dir, b, base := onBranchIn(t, "acme/shop")
	bare := filepath.Join(base, "acme", "shop.git")
	start := b.Repositories[0].Commit
	old := bareCommit(t, bare, b.Ref, start, "cancelled")
	moved := bareCommit(t, bare, b.Ref, old, "another push")
	leave(t, dir)
	r := b.Repositories[0]
	head := commitHere(t, dir)
	var report strings.Builder
	err := pushTo(context.Background(), stepGit(&testProxy, ""), hostRunnerOf(&testProxy, noMaintenance, "", nil), dir, b, r, head, old, &report)
	changed := headError(t, err)
	if changed.Head != moved || changed.Expected != start {
		t.Errorf("refusal %+v, want head %s expected %s", changed, moved, start)
	}
	if got := run(t, bare, "rev-parse", b.Ref); got != moved {
		t.Errorf("the branch is at %s, want %s", got, moved)
	}
}

// commitHere commits what the folder has and returns HEAD.
func commitHere(t *testing.T, dir string) string {
	t.Helper()
	run(t, dir, "add", "-A")
	run(t, dir, "-c", "user.name=qory", "-c", "user.email=qory@localhost", "commit", "--quiet", "-m", "left")
	return run(t, dir, "rev-parse", "HEAD")
}

// TestAHeadMovedAfterTheCheckStopsAPlainPush is a plain push that the forge rejects as not a
// fast-forward: the refusal is the head's, with the head read again.
func TestAHeadMovedAfterTheCheckStopsAPlainPush(t *testing.T) {
	dir, b, base := onBranchIn(t, "acme/shop")
	bare := filepath.Join(base, "acme", "shop.git")
	start := b.Repositories[0].Commit
	moved := bareCommit(t, bare, b.Ref, start, "another push")
	leave(t, dir)
	head := commitHere(t, dir)
	var report strings.Builder
	err := pushTo(context.Background(), stepGit(&testProxy, ""), hostRunnerOf(&testProxy, noMaintenance, "", nil), dir, b, b.Repositories[0], head, "", &report)
	if changed := headError(t, err); changed.Head != moved {
		t.Errorf("refusal %+v, want head %s", changed, moved)
	}
}

// TestARunStartsAtTheNewestPushedCommitThatDescends is a work item whose earlier runs pushed
// commits: the run starts at the newest pushed commit of a run that is not cancelled when it
// descends from the version's commit, or from the target version when there is no version,
// and stays where it was for a commit that does not descend.
func TestARunStartsAtTheNewestPushedCommitThatDescends(t *testing.T) {
	needGit(t)
	for _, c := range []struct {
		name      string
		version   bool // the version has the target version as its commit
		unrelated bool // the pushed commit is on the first commit, not on the target version
		want      string
	}{
		{"no version, a commit that descends", false, false, "pushed"},
		{"a version, a commit that descends", true, false, "pushed"},
		{"a commit that does not descend", true, true, "target"},
		{"no version, a commit that does not descend", false, true, "target"},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, dir := t.TempDir(), emptyFolder(t)
			remote(t, base, "acme/shop", "main", nil)
			bare := filepath.Join(base, "acme", "shop.git")
			target := run(t, bare, "rev-parse", "main")
			parent := target
			if c.unrelated {
				parent = run(t, bare, "rev-parse", "main~1")
			}
			pushed := bareCommit(t, bare, "qory/wi_1", parent, "an earlier run")
			b := &WorkBranch{Ref: testRef, Pushed: []Landed{{System: "git.example.com", Path: "acme/shop", Commit: pushed}}}
			if c.version {
				b.Version = []Repository{{System: "git.example.com", Path: "acme/shop", Commit: target}}
			}
			if _, err := fetchListAt(dir, "acme/shop", Target{System: "git.example.com", Path: "acme/shop", Version: target, Branch: b}); err != nil {
				t.Fatal(err)
			}
			if _, err := switchTo(dir, b); err != nil {
				t.Fatal(err)
			}
			want := target
			if c.want == "pushed" {
				want = pushed
			}
			if got := run(t, dir, "rev-parse", "HEAD"); got != want || b.Repositories[0].Start != want {
				t.Errorf("HEAD is %s and the start %s, want %s", got, b.Repositories[0].Start, want)
			}
		})
	}
}

// TestAnApprovedStartMovesToNoPushedCommit is a version that an approval lists, with a newer
// commit that descends from the version's commit, pushed by a later run: the run starts at the
// version's commit and the version stays. With Approved false the same input moves the run
// to the pushed commit and sets it in the version.
func TestAnApprovedStartMovesToNoPushedCommit(t *testing.T) {
	needGit(t)
	for _, c := range []struct {
		name     string
		approved bool
		moves    bool
	}{
		{"an approved version stays", true, false},
		{"the stage's version moves", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, dir := t.TempDir(), emptyFolder(t)
			remote(t, base, "acme/shop", "main", nil)
			bare := filepath.Join(base, "acme", "shop.git")
			target := run(t, bare, "rev-parse", "main")
			pushed := bareCommit(t, bare, "qory/wi_1", target, "a later run")
			b := &WorkBranch{
				Ref:      testRef,
				Approved: c.approved,
				Version:  []Repository{{System: "git.example.com", Path: "acme/shop", Commit: target}},
				Pushed:   []Landed{{System: "git.example.com", Path: "acme/shop", Commit: pushed}},
			}
			if _, err := fetchListAt(dir, "acme/shop", Target{System: "git.example.com", Path: "acme/shop", Version: target, Branch: b}); err != nil {
				t.Fatal(err)
			}
			if _, err := switchTo(dir, b); err != nil {
				t.Fatal(err)
			}
			want := target
			if c.moves {
				want = pushed
			}
			if got := run(t, dir, "rev-parse", "HEAD"); got != want || b.Repositories[0].Start != want || b.CommitOf("git.example.com", "acme/shop") != want {
				t.Errorf("HEAD is %s, the start %s, the version %s, want %s", got, b.Repositories[0].Start, b.CommitOf("git.example.com", "acme/shop"), want)
			}
		})
	}
}

// TestAnApprovedStartThatCommitsAfterALaterPushFailsAtPush is a version that an approval lists,
// with a later run's commit that descends from it on the branch at the forge: the approved run
// starts at the version's commit, commits, and its push stops with the head of the later run's
// commit, [*HeadChanged], and leaves the branch as it is. With Approved false the run starts at
// the later commit and its push lands on top of it.
func TestAnApprovedStartThatCommitsAfterALaterPushFailsAtPush(t *testing.T) {
	needGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, c := range []struct {
		name     string
		approved bool
	}{
		{"an approved version", true},
		{"the stage's version", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, dir := t.TempDir(), emptyFolder(t)
			remote(t, base, "acme/shop", "main", nil)
			bare := filepath.Join(base, "acme", "shop.git")
			run(t, bare, "config", "http.receivepack", "true")
			target := run(t, bare, "rev-parse", "main")
			later := bareCommit(t, bare, testRef, target, "a later run")
			b := &WorkBranch{
				Ref:      testRef,
				Approved: c.approved,
				Version:  versionOf("acme/shop", target),
				Pushed:   []Landed{{System: "git.example.com", Path: "acme/shop", Commit: later}},
			}
			if _, err := fetchListAt(dir, "acme/shop", Target{System: "git.example.com", Path: "acme/shop", Version: target, Branch: b}); err != nil {
				t.Fatal(err)
			}
			if _, err := switchTo(dir, b); err != nil {
				t.Fatal(err)
			}
			leave(t, dir)
			_, _, err := pushOf(dir, b)
			if !c.approved {
				if err != nil {
					t.Fatalf("push: %v", err)
				}
				if got, mine := run(t, bare, "rev-parse", testRef), run(t, dir, "rev-parse", "HEAD"); got != mine || run(t, dir, "rev-parse", "HEAD~1") != later {
					t.Errorf("the branch is at %s, want the run's commit %s on top of %s", got, mine, later)
				}
				return
			}
			changed := headError(t, err)
			if changed.Head != later || changed.Expected != target || changed.LastPush {
				t.Errorf("refusal %+v, want head %s expected %s, the start commit", changed, later, target)
			}
			if got := run(t, bare, "rev-parse", testRef); got != later {
				t.Errorf("the branch is at %s, want %s", got, later)
			}
		})
	}
}

// TestALeaseNeverDropsACommitOfARunNobodyCancelled is a run that starts at S, the commit of its
// version, whose branch at the forge has a cancelled run's head Y. The push replaces Y with a lease
// when only cancelled commits are between S and Y, also when a commit of [WorkBranch.Pushed] is
// below S or is not at the forge. It stops with [*HeadChanged] and leaves the branch when a commit
// X of a run that nobody cancelled is above S and is Y or below it: with one cancelled commit on
// X, with several, and when a later commit Z of another run that does not descend from S follows X
// in Pushed, with or without an approval.
func TestALeaseNeverDropsACommitOfARunNobodyCancelled(t *testing.T) {
	needGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	// build makes the branch at the forge and returns its head, the commits of Pushed and the
	// commits of Cancelled. s is the start, first the commit below it.
	type shape func(t *testing.T, bare, s, first string) (head string, pushed, cancelled []string)
	onTop := func(t *testing.T, bare, s, first string) (string, []string, []string) {
		x := bareCommit(t, bare, testRef, s, "a run that nobody cancelled")
		return bareCommit(t, bare, testRef, x, "cancelled"), []string{x}, nil
	}
	for _, c := range []struct {
		name      string
		approved  []bool
		build     shape
		cancelled func(head string) []string
		changed   bool
	}{
		{"a commit of a run nobody cancelled under a cancelled commit", []bool{true}, onTop, func(head string) []string { return []string{head} }, true},
		{"a commit of a run nobody cancelled under several cancelled commits", []bool{true}, func(t *testing.T, bare, s, first string) (string, []string, []string) {
			x := bareCommit(t, bare, testRef, s, "a run that nobody cancelled")
			one := bareCommit(t, bare, testRef, x, "cancelled one")
			return bareCommit(t, bare, testRef, one, "cancelled two"), []string{x}, []string{one}
		}, func(head string) []string { return []string{head} }, true},
		{"a later commit that does not descend from the start", []bool{true, false}, func(t *testing.T, bare, s, first string) (string, []string, []string) {
			z := bareCommit(t, bare, "other", first, "a later run")
			x := bareCommit(t, bare, testRef, s, "a run that nobody cancelled")
			return bareCommit(t, bare, testRef, x, "cancelled"), []string{x, z}, nil
		}, func(head string) []string { return []string{head} }, true},
		{"cancelled commits on the start", []bool{true, false}, func(t *testing.T, bare, s, first string) (string, []string, []string) {
			return bareCommit(t, bare, testRef, s, "cancelled"), nil, nil
		}, func(head string) []string { return []string{head} }, false},
		{"a commit below the start", []bool{true, false}, func(t *testing.T, bare, s, first string) (string, []string, []string) {
			return bareCommit(t, bare, testRef, s, "cancelled"), []string{first}, nil
		}, func(head string) []string { return []string{head} }, false},
		{"a commit that the forge does not have", []bool{true}, func(t *testing.T, bare, s, first string) (string, []string, []string) {
			return bareCommit(t, bare, testRef, s, "cancelled"), []string{strings.Repeat("a", 40)}, nil
		}, func(head string) []string { return []string{head} }, false},
	} {
		for _, approved := range c.approved {
			t.Run(fmt.Sprintf("%s, approved %v", c.name, approved), func(t *testing.T) {
				base, dir := t.TempDir(), emptyFolder(t)
				remote(t, base, "acme/shop", "main", nil)
				bare := filepath.Join(base, "acme", "shop.git")
				run(t, bare, "config", "http.receivepack", "true")
				s := run(t, bare, "rev-parse", "main")
				first := run(t, bare, "rev-parse", "main~1")
				head, pushed, cancelled := c.build(t, bare, s, first)
				b := &WorkBranch{Ref: testRef, Approved: approved, Version: versionOf("acme/shop", s)}
				for _, p := range pushed {
					b.Pushed = append(b.Pushed, Landed{System: "git.example.com", Path: "acme/shop", Commit: p})
				}
				for _, cancelledCommit := range append(cancelled, c.cancelled(head)...) {
					b.Cancelled = append(b.Cancelled, Landed{System: "git.example.com", Path: "acme/shop", Commit: cancelledCommit})
				}
				if _, err := fetchListAt(dir, "acme/shop", Target{System: "git.example.com", Path: "acme/shop", Version: s, Branch: b}); err != nil {
					t.Fatal(err)
				}
				if _, err := switchTo(dir, b); err != nil {
					t.Fatal(err)
				}
				if got := run(t, dir, "rev-parse", "HEAD"); got != s {
					t.Fatalf("the run starts at %s, want %s", got, s)
				}
				leave(t, dir)
				_, _, err := pushOf(dir, b)
				if c.changed {
					if changed := headError(t, err); changed.Head != head || changed.Expected != s {
						t.Errorf("refusal %+v, want head %s expected %s", changed, head, s)
					}
					if got := run(t, bare, "rev-parse", testRef); got != head {
						t.Errorf("the branch is at %s, want %s", got, head)
					}
					return
				}
				if err != nil {
					t.Fatalf("push: %v", err)
				}
				if got, mine := run(t, bare, "rev-parse", testRef), run(t, dir, "rev-parse", "HEAD"); got != mine || run(t, dir, "rev-parse", "HEAD~1") != s {
					t.Errorf("the branch is at %s, want the run's commit %s on %s", got, mine, s)
				}
			})
		}
	}
}

// TestAPushOfTwoCancelledCommitsHasOneRecordedHead is the shape the recorder has: a cancelled run
// pushed 2 commits in one push, so one head is recorded and its parent is not. The branch is at
// that head, so the next run's first push replaces both with a lease on it.
func TestAPushOfTwoCancelledCommitsHasOneRecordedHead(t *testing.T) {
	dir, b, base := onBranchIn(t, "acme/shop")
	bare := filepath.Join(base, "acme", "shop.git")
	first := bareCommit(t, bare, b.Ref, b.Repositories[0].Commit, "cancelled one")
	second := bareCommit(t, bare, b.Ref, first, "cancelled two")
	b.Cancelled = []Landed{{System: "git.example.com", Path: "acme/shop", Commit: second}}
	leave(t, dir)
	doc, _, err := pushOf(dir, b)
	if err != nil || doc == nil {
		t.Fatalf("push: %v", err)
	}
	if got, mine := run(t, bare, "rev-parse", b.Ref), run(t, dir, "rev-parse", "HEAD"); got != mine {
		t.Errorf("the branch is at %s, want the run's %s", got, mine)
	}
}

// TestOnlyARecordedHeadOfACancelledRunIsReplaced is a branch whose head is, in turn: a commit on
// top of the recorded head, a commit below it that the record does not list, a recorded head that
// is unrelated to the start commit (an orphan that equals the branch head), a recorded head that
// the forge does not serve, and an empty record. Each stops the push and leaves the branch, with the
// head in the refusal. The rule is equality with a recorded head that descends from the start commit.
func TestOnlyARecordedHeadOfACancelledRunIsReplaced(t *testing.T) {
	for _, c := range []struct {
		name string
		// setup makes the branch and returns its head and the record.
		setup func(t *testing.T, bare, ref, start string) (head string, record []string)
	}{
		{"a commit on top of the recorded head", func(t *testing.T, bare, ref, start string) (string, []string) {
			first := bareCommit(t, bare, ref, start, "cancelled")
			return bareCommit(t, bare, ref, first, "another push"), []string{first}
		}},
		{"a commit below the recorded head that the record does not list", func(t *testing.T, bare, ref, start string) (string, []string) {
			first := bareCommit(t, bare, ref, start, "cancelled one")
			second := bareCommit(t, bare, ref, first, "cancelled two")
			bareCommit(t, bare, ref, second, "cancelled three")
			run(t, bare, "update-ref", "refs/heads/"+ref, first)
			return first, []string{second}
		}},
		{"a recorded head that is unrelated to the start commit", func(t *testing.T, bare, ref, start string) (string, []string) {
			orphan := run(t, bare, "commit-tree", run(t, bare, "rev-parse", start+"^{tree}"), "-m", "orphan")
			run(t, bare, "update-ref", "refs/heads/"+ref, orphan)
			return orphan, []string{orphan}
		}},
		{"a recorded head that the forge does not have", func(t *testing.T, bare, ref, start string) (string, []string) {
			return bareCommit(t, bare, ref, start, "cancelled"), []string{strings.Repeat("a", 40)}
		}},
		{"an empty record", func(t *testing.T, bare, ref, start string) (string, []string) {
			return bareCommit(t, bare, ref, start, "cancelled"), nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b, base := onBranchIn(t, "acme/shop")
			bare := filepath.Join(base, "acme", "shop.git")
			start := b.Repositories[0].Commit
			head, record := c.setup(t, bare, b.Ref, start)
			for _, commit := range record {
				b.Cancelled = append(b.Cancelled, Landed{System: "git.example.com", Path: "acme/shop", Commit: commit})
			}
			leave(t, dir)
			_, _, err := pushOf(dir, b)
			if changed := headError(t, err); changed.Head != head || changed.Expected != start {
				t.Errorf("refusal %+v, want head %s expected %s", changed, head, start)
			}
			if got := run(t, bare, "rev-parse", b.Ref); got != head {
				t.Errorf("the branch moved to %s", got)
			}
		})
	}
}

// TestAMergeInACancelledPushIsReplacedAtItsHeadOnly is a cancelled push whose recorded head is a
// merge of the run's commit with a commit X that the run did not write. The branch at the merge is
// replaced, with the ref of the branch alone. The branch moved to X, a commit of the merge's
// history that the record does not list, is refused: no history is read to cover X.
func TestAMergeInACancelledPushIsReplacedAtItsHeadOnly(t *testing.T) {
	for _, atMerge := range []bool{true, false} {
		name := map[bool]string{true: "the branch at the merge", false: "the branch at the foreign commit"}[atMerge]
		t.Run(name, func(t *testing.T) {
			dir, b, base := onBranchIn(t, "acme/shop")
			bare := filepath.Join(base, "acme", "shop.git")
			start := b.Repositories[0].Commit
			mine := bareCommit(t, bare, b.Ref, start, "cancelled work")
			foreign := run(t, bare, "commit-tree", run(t, bare, "rev-parse", start+"^{tree}"), "-p", start, "-m", "a commit of another branch")
			run(t, bare, "update-ref", "refs/heads/feature", foreign)
			merge := run(t, bare, "commit-tree", run(t, bare, "rev-parse", mine+"^{tree}"), "-p", mine, "-p", foreign, "-m", "merge feature")
			head := merge
			if !atMerge {
				head = foreign
			}
			run(t, bare, "update-ref", "refs/heads/"+b.Ref, head)
			b.Cancelled = []Landed{{System: "git.example.com", Path: "acme/shop", Commit: merge}}
			leave(t, dir)
			doc, _, err := pushOf(dir, b)
			if !atMerge {
				if changed := headError(t, err); changed.Head != foreign {
					t.Errorf("refusal %+v, want head %s", changed, foreign)
				}
				if got := run(t, bare, "rev-parse", b.Ref); got != foreign {
					t.Errorf("the branch moved to %s", got)
				}
				return
			}
			if err != nil || doc == nil {
				t.Fatalf("push: %v", err)
			}
			if got, want := run(t, bare, "rev-parse", b.Ref), run(t, dir, "rev-parse", "HEAD"); got != want {
				t.Errorf("the branch is at %s, want the run's %s", got, want)
			}
			if got := run(t, bare, "rev-parse", "refs/heads/feature"); got != foreign {
				t.Errorf("the branch feature is at %s, want %s: the push replaces the ref of the branch alone", got, foreign)
			}
		})
	}
}

// TestSeveralRecordedHeadsOfCancelledRuns is two cancelled runs that each pushed once: the first
// pushed one commit and the second 2 more on top of it, so the work item lists the heads A and C.
// The branch at C is replaced, and so is the branch at A, because each is a recorded head; the
// branch at the middle commit B is refused, because the record does not list it.
func TestSeveralRecordedHeadsOfCancelledRuns(t *testing.T) {
	for _, c := range []struct {
		name  string
		at    int // 0 is A, 1 is B, 2 is C
		stops bool
	}{
		{"the branch at the head of the second run", 2, false},
		{"the branch at the head of the first run", 0, false},
		{"the branch at a commit that no run recorded", 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b, base := onBranchIn(t, "acme/shop")
			bare := filepath.Join(base, "acme", "shop.git")
			a := bareCommit(t, bare, b.Ref, b.Repositories[0].Commit, "first run")
			mid := bareCommit(t, bare, b.Ref, a, "second run one")
			last := bareCommit(t, bare, b.Ref, mid, "second run two")
			at := []string{a, mid, last}[c.at]
			run(t, bare, "update-ref", "refs/heads/"+b.Ref, at)
			b.Cancelled = []Landed{{System: "git.example.com", Path: "acme/shop", Commit: a}, {System: "git.example.com", Path: "acme/shop", Commit: last}}
			leave(t, dir)
			doc, _, err := pushOf(dir, b)
			if c.stops {
				if changed := headError(t, err); changed.Head != at {
					t.Errorf("refusal %+v, want head %s", changed, at)
				}
				return
			}
			if err != nil || doc == nil {
				t.Fatalf("push: %v", err)
			}
		})
	}
}

// TestTheTextsOfTheHeadCheckStartWithTheFix is line 1 of each refusal of the head check: at most
// 100 characters with the prefix "qory run: ", and the fix first, as an instruction. Line 1 has no
// value of the run, so the branch with the longest name that the contract allows (qory/wi_ and 64
// characters) does not change its length.
func TestTheTextsOfTheHeadCheckStartWithTheFix(t *testing.T) {
	branch := "qory/wi_" + strings.Repeat("a", 64)
	for name, c := range map[string]struct{ text, start string }{
		"textHeadChanged":  {(&HeadChanged{Branch: branch, System: "git.example.com", Path: "acme/shop", Head: strings.Repeat("a", 40), Expected: strings.Repeat("b", 40)}).Error(), "qory run: Set "},
		"textHeadMissing":  {(&HeadChanged{Branch: branch, System: "git.example.com", Path: "acme/shop", Expected: strings.Repeat("b", 40)}).Error(), "qory run: Create "},
		"textHeadNoRead":   {fmt.Sprintf(textHeadNoRead, "acme/shop", "git.example.com", "cause"), "qory run: Take "},
		"textOutboxCommit": {fmt.Sprintf(textOutboxCommitted, "git.example.com/acme/shop", strings.Repeat("a", 40), ".qory/outbox/pull-request/pull-request.json"), "qory run: Keep "},
		"textListFailed":   {listFailed(false, WorkRepository{Path: "acme/shop"}, strings.Repeat("a", 40), strings.Repeat("b", 40), errors.New("cause")).Error(), "qory run: Start "},
		"textListFromCode": {listFailed(true, WorkRepository{Path: "acme/shop"}, strings.Repeat("a", 40), strings.Repeat("b", 40), errors.New("cause")).Error(), "qory run: Start "},
	} {
		line, _, _ := strings.Cut(c.text, "\n")
		if n := utf8.RuneCountInString(line); n > 100 {
			t.Errorf("%s: line 1 has %d characters, want at most 100: %q", name, n, line)
		}
		if !strings.HasPrefix(line, c.start) {
			t.Errorf("%s: line 1 is %q, want it to start with %q", name, line, c.start)
		}
	}
}

// TestAnUnreadableHeadStopsThePushWithTheFixFirst is a read of the branch head that fails: the
// check returns the whole text, with the fix on line 1, and the cause on the last line.
func TestAnUnreadableHeadStopsThePushWithTheFixFirst(t *testing.T) {
	_, b := onBranch(t, "acme/shop")
	read := func(context.Context, string, ...string) (string, error) { return "", errors.New("network down") }
	_, err := checkHead(context.Background(), read, t.TempDir(), b, b.Repositories[0])
	want := "qory run: Take the work item in again in Qory Apiary: the run's host proxy could not read the head.\n" +
		"  qory could not read the head of the branch, so it pushed nothing.\n" +
		"  Repository git.example.com/acme/shop.\n" +
		"  Cause: network down; the code is used"
	if err == nil || err.Error() != want {
		t.Errorf("the refusal is\n%v\nwant\n%s", err, want)
	}
}

// TestTheRefusalOfAMovedBranchSaysWhatIsLostAndWhatApiaryRecorded is the whole text of a refusal
// for each case: a head that moved, with the start commit and with the run's last push as the
// expected commit, and a branch that is not on the forge, with the same two. The loss is on the
// terminal for a head that exists, and the caveat of the record for the run's last push.
func TestTheRefusalOfAMovedBranchSaysWhatIsLostAndWhatApiaryRecorded(t *testing.T) {
	head, expected := strings.Repeat("a", 40), strings.Repeat("b", 40)
	const (
		fixMoved   = "qory run: Set the branch to the expected commit or delete it; its head is not that commit.\n"
		fixMissing = "qory run: Create the branch at the expected commit on the forge; it is not there.\n"
		loss       = "\n  Setting the branch to that commit or deleting it loses the commits on it that no version has."
		recorded   = "\n  Expected is this run's last push: moving the branch to it works only if Qory Apiary recorded that push."
		created    = "\n  Expected is this run's last push: creating the branch at it works only if Qory Apiary recorded that push."
		push       = "\n  Run git push <remote> %s:refs/heads/qory/wi_1, where <remote> is the remote that points to git.example.com/acme/shop; fetch the commit first if the checkout has no such commit."
	)
	line2 := func(head, from string) string {
		return "  Branch qory/wi_1. Repository git.example.com/acme/shop. Head: " + head + ". Expected: " + expected + ", " + from + ". qory did not push."
	}
	for _, c := range []struct {
		name string
		err  *HeadChanged
		want string
	}{
		{"moved, start commit", &HeadChanged{Head: head}, fixMoved + line2(head, "the start commit") + loss},
		{"moved, last push", &HeadChanged{Head: head, LastPush: true}, fixMoved + line2(head, "this run's last push") + loss + recorded},
		{"missing, start commit", &HeadChanged{}, fixMissing + line2("none", "the start commit") + fmt.Sprintf(push, expected)},
		{"missing, last push", &HeadChanged{LastPush: true}, fixMissing + line2("none", "this run's last push") + fmt.Sprintf(push, expected) + created},
	} {
		c.err.Branch, c.err.System, c.err.Path, c.err.Expected = "qory/wi_1", "git.example.com", "acme/shop", expected
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s: the refusal is\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}
