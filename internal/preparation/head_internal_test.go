package preparation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/submission"
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
	want := "qory run: Check branch qory/wi_1: its head is not the commit this run expected, so qory did not push.\n" +
		"  Repository git.example.com/acme/shop. Head: none. Expected: " + start + ", the start commit."
	if err.Error() != want {
		t.Errorf("the refusal is\n%s\nwant\n%s", err, want)
	}
	if changed.Head != "" || changed.NotAbove {
		t.Errorf("refusal %+v, want no head and no third line", changed)
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
// push stops, the branch stays, and the refusal has the head and the expected commit, with
// the third line for the head that does not descend.
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
			if changed.Head != wantHead || changed.Expected != start || changed.NotAbove != c.unrelated {
				t.Errorf("refusal %+v, want head %s expected %s not above %v", changed, wantHead, start, c.unrelated)
			}
			if got := run(t, bare, "rev-parse", b.Ref); got != wantHead {
				t.Errorf("the branch moved to %s", got)
			}
			text := err.Error()
			if strings.Contains(text, "does not descend") != c.unrelated || !strings.Contains(text, "Head: "+wantHead+". Expected: "+start+", the start commit.") {
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
				b.Version = []submission.Repository{{System: "git.example.com", Path: "acme/shop", Commit: target}}
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

// TestAPushOfTwoCancelledCommitsHasOneRecordedHead is the shape the recorder has: a cancelled run
// pushed 2 commits in one push, so one head is recorded and its parent is not. The next run's
// first push replaces both with a lease on the branch head.
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

// TestACommitThatNoRecordedHeadHoldsStopsThePush is a branch with a cancelled push of one commit
// and a commit of another push on top of it, and a branch where the one recorded head is a commit
// that is unrelated to the start commit (an orphan) or that the forge does not have: each stops the push
// and leaves the branch, because a commit that no recorded head that descends from the start
// commit has in its history is not a cancelled run's.
func TestACommitThatNoRecordedHeadHoldsStopsThePush(t *testing.T) {
	for _, c := range []struct {
		name   string
		record func(t *testing.T, bare, first string) string
	}{
		{"a commit on top of the recorded head", func(_ *testing.T, _, first string) string { return first }},
		{"a recorded head that is unrelated to the start commit", func(t *testing.T, bare, first string) string {
			orphan := run(t, bare, "commit-tree", run(t, bare, "rev-parse", first+"^{tree}"), "-m", "orphan")
			run(t, bare, "update-ref", "refs/heads/orphan", orphan)
			return orphan
		}},
		{"a recorded head that the forge does not have", func(*testing.T, string, string) string { return strings.Repeat("a", 40) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b, base := onBranchIn(t, "acme/shop")
			bare := filepath.Join(base, "acme", "shop.git")
			start := b.Repositories[0].Commit
			first := bareCommit(t, bare, b.Ref, start, "cancelled")
			second := bareCommit(t, bare, b.Ref, first, "another push")
			b.Cancelled = []Landed{{System: "git.example.com", Path: "acme/shop", Commit: c.record(t, bare, first)}}
			leave(t, dir)
			_, _, err := pushOf(dir, b)
			if changed := headError(t, err); changed.Head != second || changed.Expected != start {
				t.Errorf("refusal %+v, want head %s expected %s", changed, second, start)
			}
			if got := run(t, bare, "rev-parse", b.Ref); got != second {
				t.Errorf("the branch moved to %s", got)
			}
		})
	}
}

// TestAMergeInACancelledPushIsCoveredByItsHead is a cancelled push whose head is a merge: the range
// from the start commit to the head follows every parent, so the commit that the merge brought in
// counts as cancelled with the others, and the push replaces the branch ref alone.
func TestAMergeInACancelledPushIsCoveredByItsHead(t *testing.T) {
	dir, b, base := onBranchIn(t, "acme/shop")
	bare := filepath.Join(base, "acme", "shop.git")
	start := b.Repositories[0].Commit
	mine := bareCommit(t, bare, b.Ref, start, "cancelled work")
	brought := run(t, bare, "commit-tree", run(t, bare, "rev-parse", start+"^{tree}"), "-p", start, "-m", "brought in from main")
	run(t, bare, "update-ref", "refs/heads/brought", brought)
	merge := run(t, bare, "commit-tree", run(t, bare, "rev-parse", mine+"^{tree}"), "-p", mine, "-p", brought, "-m", "merge main")
	run(t, bare, "update-ref", "refs/heads/"+b.Ref, merge)
	b.Cancelled = []Landed{{System: "git.example.com", Path: "acme/shop", Commit: merge}}
	leave(t, dir)
	doc, _, err := pushOf(dir, b)
	if err != nil || doc == nil {
		t.Fatalf("push: %v", err)
	}
	if got, want := run(t, bare, "rev-parse", b.Ref), run(t, dir, "rev-parse", "HEAD"); got != want {
		t.Errorf("the branch is at %s, want the run's %s", got, want)
	}
	if got := run(t, bare, "rev-parse", "refs/heads/brought"); got != brought {
		t.Errorf("the branch brought is at %s, want %s: the push replaces the ref of the branch alone", got, brought)
	}
}

// TestSeveralRecordedHeadsOfCancelledRuns is two cancelled runs that each pushed once: the first
// pushed one commit and the second 2 more on top of it, so the work item lists the heads A and C.
// Both heads together cover the three commits and the push replaces them; with the head A and the
// middle commit B listed, the commit C on top is covered by no head and the push stops.
func TestSeveralRecordedHeadsOfCancelledRuns(t *testing.T) {
	for _, c := range []struct {
		name  string
		heads func(a, b, c string) []string
		stops bool
	}{
		{"the heads of both runs", func(a, _, c string) []string { return []string{a, c} }, false},
		{"heads that leave the last commit out", func(a, b, _ string) []string { return []string{a, b} }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b, base := onBranchIn(t, "acme/shop")
			bare := filepath.Join(base, "acme", "shop.git")
			a := bareCommit(t, bare, b.Ref, b.Repositories[0].Commit, "first run")
			mid := bareCommit(t, bare, b.Ref, a, "second run one")
			last := bareCommit(t, bare, b.Ref, mid, "second run two")
			for _, head := range c.heads(a, mid, last) {
				b.Cancelled = append(b.Cancelled, Landed{System: "git.example.com", Path: "acme/shop", Commit: head})
			}
			leave(t, dir)
			doc, _, err := pushOf(dir, b)
			if c.stops {
				if changed := headError(t, err); changed.Head != last {
					t.Errorf("refusal %+v, want head %s", changed, last)
				}
				return
			}
			if err != nil || doc == nil {
				t.Fatalf("push: %v", err)
			}
		})
	}
}
