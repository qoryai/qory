package preparation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
)

// baseFetch is [fetchBranchAt] of acme/shop at version with the branch at entries, which
// returns the branch and what the fetch printed.
func baseFetch(t *testing.T, version string, entries []Repository) (*WorkBranch, string, string) {
	t.Helper()
	dir := emptyFolder(t)
	b := &WorkBranch{Ref: testRef, Version: entries}
	line, err := fetchListAt(dir, "acme/shop", Target{System: "git.example.com", Path: "acme/shop", Version: version, Branch: b})
	if err != nil {
		t.Fatal(err)
	}
	return b, dir, line
}

// TestBranchBaseIsTheCutWhenTheTargetMoved is a branch cut from the first commit of the
// target, whose head has moved on: the base is the first commit, and the fetch prints no line
// about it.
func TestBranchBaseIsTheCutWhenTheTargetMoved(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	commit := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, "branch.txt"), "branch\n")
	})

	b, _, line := baseFetch(t, head, versionOf("acme/shop", commit))
	if b.Base == nil || b.Base.Commit != first {
		t.Fatalf("base = %+v, want the commit %s", b.Base, first)
	}
	if strings.Contains(line, "branch_base") {
		t.Errorf("the fetch printed %q", line)
	}
}

// TestBranchBaseIsAnAncestorOfTheTarget is the cut of a branch: the target has the base in its
// history, and a branch commit that the target lacks is not the base.
func TestBranchBaseIsAnAncestorOfTheTarget(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	commit := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, "branch.txt"), "branch\n")
	})

	b, _, _ := baseFetch(t, head, versionOf("acme/shop", commit))
	if b.Base == nil || b.Base.Commit == commit {
		t.Fatalf("base = %+v, want a commit of the target's history, not the branch's", b.Base)
	}
	bare := filepath.Join(base, "acme", "shop.git")
	run(t, bare, "merge-base", "--is-ancestor", b.Base.Commit, head)
}

// TestBranchBaseIsTheTargetWhenTheRunStartsAtIt is a branch with no version, and one whose
// version is the target version: the base is the target version.
func TestBranchBaseIsTheTargetWhenTheRunStartsAtIt(t *testing.T) {
	needGit(t)
	first, head := movedRemote(t, t.TempDir(), "acme/shop")
	for name, entries := range map[string][]Repository{
		"no version":         nil,
		"the target":         versionOf("acme/shop", head),
		"another repository": versionOf("acme/other", first),
	} {
		t.Run(name, func(t *testing.T) {
			b, _, _ := baseFetch(t, head, entries)
			if b.Base == nil || b.Base.Commit != head {
				t.Errorf("base = %+v, want the target version %s", b.Base, head)
			}
		})
	}
}

// TestBranchBaseIsAbsentWithNoCommonCommit is a branch whose history has no commit that the
// target has: the base is absent, and the fetch prints no line.
func TestBranchBaseIsAbsentWithNoCommonCommit(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	_, head := movedRemote(t, base, "acme/shop")
	work := t.TempDir()
	run(t, work, "clone", "--quiet", filepath.Join(base, "acme", "shop.git"), ".")
	run(t, work, "checkout", "--quiet", "--orphan", "other")
	run(t, work, "rm", "-rf", "--quiet", ".")
	write(t, filepath.Join(work, "other.txt"), "other\n")
	run(t, work, "add", "-A")
	run(t, work, "commit", "--quiet", "-m", "other")
	run(t, work, "push", "--quiet", "origin", "other")
	orphan := run(t, work, "rev-parse", "HEAD")

	b, _, line := baseFetch(t, head, versionOf("acme/shop", orphan))
	if b.Base != nil {
		t.Errorf("base = %+v, want none", b.Base)
	}
	if strings.Contains(line, "branch_base") {
		t.Errorf("the fetch printed %q", line)
	}
}

// TestBranchBaseIsAbsentWithTwoMergeBases is a target and a branch that merged each other's
// side commit: the two commits are two merge bases, so the base is absent.
func TestBranchBaseIsAbsentWithTwoMergeBases(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, _ := movedRemote(t, base, "acme/shop")
	work := t.TempDir()
	run(t, work, "clone", "--quiet", filepath.Join(base, "acme", "shop.git"), ".")
	side := func(name string) string {
		run(t, work, "checkout", "--quiet", "-b", name, first)
		write(t, filepath.Join(work, name+".txt"), name+"\n")
		run(t, work, "add", "-A")
		run(t, work, "commit", "--quiet", "-m", name)
		return run(t, work, "rev-parse", "HEAD")
	}
	x, y := side("x"), side("y")
	merge := func(name, into, other string) string {
		run(t, work, "checkout", "--quiet", "-b", name, into)
		run(t, work, "merge", "--quiet", "--no-ff", "-m", name, other)
		run(t, work, "push", "--quiet", "origin", name)
		return run(t, work, "rev-parse", "HEAD")
	}
	target, start := merge("target", x, y), merge("start", y, x)
	if got := strings.Fields(run(t, work, "merge-base", "--all", target, start)); len(got) != 2 {
		t.Fatalf("the test's merge bases are %v, want two", got)
	}

	b, _, line := baseFetch(t, target, versionOf("acme/shop", start))
	if b.Base != nil {
		t.Errorf("base = %+v, want none", b.Base)
	}
	if strings.Contains(line, "branch_base") {
		t.Errorf("the fetch printed %q", line)
	}
}

// TestBranchBaseIsTheMergedTargetCommitWhenTheTargetHasIt is a branch that merged the
// target's head into itself: the head is the one merge base, and the base is the head.
func TestBranchBaseIsTheMergedTargetCommitWhenTheTargetHasIt(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	work := t.TempDir()
	run(t, work, "clone", "--quiet", filepath.Join(base, "acme", "shop.git"), ".")
	run(t, work, "checkout", "--quiet", "-b", "work", first)
	write(t, filepath.Join(work, "branch.txt"), "branch\n")
	run(t, work, "add", "-A")
	run(t, work, "commit", "--quiet", "-m", "branch")
	run(t, work, "merge", "--quiet", "--no-ff", "-m", "merge", head)
	run(t, work, "push", "--quiet", "origin", "work")
	merged := run(t, work, "rev-parse", "HEAD")

	b, _, _ := baseFetch(t, head, versionOf("acme/shop", merged))
	if b.Base == nil || b.Base.Commit != head {
		t.Errorf("base = %+v, want the target's head %s", b.Base, head)
	}
}

// TestBranchBaseTreeIsTheTreeOfTheBaseCommit is the tree id of the base commit in the cases
// of a base at the target and a base below it.
func TestBranchBaseTreeIsTheTreeOfTheBaseCommit(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	bare := filepath.Join(base, "acme", "shop.git")
	commit := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, "branch.txt"), "branch\n")
	})

	cut, _, _ := baseFetch(t, head, versionOf("acme/shop", commit))
	if want := run(t, bare, "rev-parse", first+"^{tree}"); cut.Base == nil || cut.Base.Tree != want || len(want) != 40 {
		t.Errorf("the tree of the cut is %+v, want %s", cut.Base, want)
	}
	atTarget, _, _ := baseFetch(t, head, nil)
	if want := run(t, bare, "rev-parse", head+"^{tree}"); atTarget.Base == nil || atTarget.Base.Tree != want || cut.Base.Tree == want {
		t.Errorf("the tree at the target is %+v, want %s", atTarget.Base, want)
	}
}

// TestAFailedBaseFetchLeavesBranchBaseOutAndPrintsTheLine is a fetch of the base commit that
// git refuses: the base stays nil, and the line says which member inputs.json lacks, with the
// cause.
func TestAFailedBaseFetchLeavesBranchBaseOutAndPrintsTheLine(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	commit := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, "branch.txt"), "branch\n")
	})
	b, dir, _ := baseFetch(t, head, versionOf("acme/shop", commit))
	b.Base = nil

	host := hostRunner(&testProxy)
	failing := func(ctx context.Context, dir string, args ...string) (string, error) {
		if slices.Contains(args, "--depth") {
			return "", errors.New("the remote refused the fetch")
		}
		return host(ctx, dir, args...)
	}
	var report strings.Builder
	ctx := hostgit.WithBase(context.Background(), os.Environ())
	b.setBase(ctx, failing, &report, dir, "git.example.com", "acme/shop", head)
	want := "qory run: inputs.json has no qory.branch_base; the run goes on.\n  Cause: qory could not read the commit where the work branch was cut: the remote refused the fetch\n"
	if b.Base != nil || report.String() != want {
		t.Errorf("base = %+v, report %q, want none and %q", b.Base, report.String(), want)
	}
}

// TestTheBaseIsFoundFromAPushedCommitAboveTheVersion is a branch whose version is the first commit
// and whose newest pushed commit, which a run that a person did not cancel pushed, is above it:
// the start moves to the pushed commit, and the base is still the first commit, where the
// branch was cut.
func TestTheBaseIsFoundFromAPushedCommitAboveTheVersion(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	pushed := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, "pushed.txt"), "pushed\n")
	})

	dir := emptyFolder(t)
	b := &WorkBranch{Ref: testRef, Version: versionOf("acme/shop", first), Pushed: []Landed{{System: "git.example.com", Path: "acme/shop", Commit: pushed}}}
	if _, err := fetchListAt(dir, "acme/shop", Target{System: "git.example.com", Path: "acme/shop", Version: head, Branch: b}); err != nil {
		t.Fatal(err)
	}
	if got := b.CommitOf("git.example.com", "acme/shop"); got != pushed {
		t.Fatalf("the start is %s, want the pushed commit %s", got, pushed)
	}
	if b.Base == nil || b.Base.Commit != first {
		t.Errorf("base = %+v, want the first commit %s", b.Base, first)
	}
}

// TestFindBaseReturnsTheErrorOfTheHistory is a start commit that the remote does not have: the
// history cannot be fetched, findBase returns the error, and setBase prints the line.
func TestFindBaseReturnsTheErrorOfTheHistory(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	b, dir, _ := baseFetch(t, head, versionOf("acme/shop", first))
	b.Base = nil
	b.Version = versionOf("acme/shop", strings.Repeat("a", 40))

	ctx := hostgit.WithBase(context.Background(), os.Environ())
	if got, err := b.findBase(ctx, hostRunner(&testProxy), dir, "git.example.com", "acme/shop", head); err == nil || got != nil {
		t.Errorf("findBase = %+v, %v, want an error", got, err)
	}
	var report strings.Builder
	b.setBase(ctx, hostRunner(&testProxy), &report, dir, "git.example.com", "acme/shop", head)
	if b.Base != nil || !strings.HasPrefix(report.String(), "qory run: inputs.json has no qory.branch_base; the run goes on.\n  Cause: qory could not read the commit where the work branch was cut: ") {
		t.Errorf("base = %+v, report %q", b.Base, report.String())
	}
}

// TestASlowHistoryReadLeavesBranchBaseOutAndPrintsTheLine is a start commit whose history
// read does not end: setBase stops it at [projectBaseLimit], the base stays nil, the line
// shows that the read took longer than the limit, and the setup context is not cut.
func TestASlowHistoryReadLeavesBranchBaseOutAndPrintsTheLine(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	commit := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, "branch.txt"), "branch\n")
	})
	b, dir, _ := baseFetch(t, head, versionOf("acme/shop", commit))
	b.Base = nil

	was := projectBaseLimit
	t.Cleanup(func() { projectBaseLimit = was })
	projectBaseLimit = 200 * time.Millisecond
	host := hostRunner(&testProxy)
	hangs := func(ctx context.Context, dir string, args ...string) (string, error) {
		if slices.Contains(args, "--filter=tree:0") || slices.Contains(args, "merge-base") {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(5 * time.Second):
				return "", errors.New("the read was not stopped")
			}
		}
		return host(ctx, dir, args...)
	}
	var report strings.Builder
	ctx := hostgit.WithBase(context.Background(), os.Environ())
	began := time.Now()
	b.setBase(ctx, hangs, &report, dir, "git.example.com", "acme/shop", head)
	want := "qory run: inputs.json has no qory.branch_base; the run goes on.\n  Cause: the read of the history of the work branch took longer than 0.2 seconds\n"
	if b.Base != nil || report.String() != want {
		t.Errorf("base = %+v, report %q, want none and %q", b.Base, report.String(), want)
	}
	if took := time.Since(began); took > 10*time.Second || ctx.Err() != nil {
		t.Errorf("the read took %v, and the setup context ended: %v", took, ctx.Err())
	}
}

// TestAFailedReadOfTheBasesTreeLeavesBranchBaseOut is a base commit whose tree id git
// cannot read: findBase returns the error, the base stays nil, and the line has the cause.
func TestAFailedReadOfTheBasesTreeLeavesBranchBaseOut(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	b, dir, _ := baseFetch(t, head, versionOf("acme/shop", first))
	b.Base = nil

	host := hostRunner(&testProxy)
	failing := func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "rev-parse" && slices.Contains(args, "--verify") {
			return "", errors.New("git cannot read the tree")
		}
		return host(ctx, dir, args...)
	}
	ctx := hostgit.WithBase(context.Background(), os.Environ())
	if got, err := b.findBase(ctx, failing, dir, "git.example.com", "acme/shop", head); err == nil || got != nil {
		t.Errorf("findBase = %+v, %v, want an error", got, err)
	}
	var report strings.Builder
	b.setBase(ctx, failing, &report, dir, "git.example.com", "acme/shop", head)
	if b.Base != nil || !strings.HasSuffix(report.String(), "qory could not read the commit where the work branch was cut: git cannot read the tree\n") {
		t.Errorf("base = %+v, report %q", b.Base, report.String())
	}
}

// TestAFailedReadOfTheMergeBasesLeavesBranchBaseOut is a branch whose start commit is not
// the target, when git fails to read their merge bases: findBase returns the error, with
// no base.
func TestAFailedReadOfTheMergeBasesLeavesBranchBaseOut(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	commit := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, "branch.txt"), "branch\n")
	})
	b, dir, _ := baseFetch(t, head, versionOf("acme/shop", commit))
	b.Base = nil

	host := hostRunner(&testProxy)
	failing := func(ctx context.Context, dir string, args ...string) (string, error) {
		if slices.Contains(args, "merge-base") && slices.Contains(args, "--all") {
			return "", &gitError{line: "git cannot read the merge bases", exit: 128}
		}
		return host(ctx, dir, args...)
	}
	ctx := hostgit.WithBase(context.Background(), os.Environ())
	got, err := b.findBase(ctx, failing, dir, "git.example.com", "acme/shop", head)
	if got != nil || err == nil || err.Error() != "git cannot read the merge bases" {
		t.Errorf("findBase = %+v, %v, want the error of the merge-base read", got, err)
	}
}

// TestAFetchIntoTheCheckoutIsNotCutByTheHistoryLimit is a branch whose history is read
// within [projectBaseLimit]: the fetch of the base into dest, the checkout, runs under the
// context of setBase, with no deadline, since a git that the limit killed could leave a
// lock in dest.
func TestAFetchIntoTheCheckoutIsNotCutByTheHistoryLimit(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	commit := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, "branch.txt"), "branch\n")
	})
	b, dir, _ := baseFetch(t, head, versionOf("acme/shop", commit))
	b.Base = nil

	was := projectBaseLimit
	t.Cleanup(func() { projectBaseLimit = was })
	projectBaseLimit = time.Minute
	host := hostRunner(&testProxy)
	var checked, limitedHistory bool
	watch := func(ctx context.Context, where string, args ...string) (string, error) {
		_, hasDeadline := ctx.Deadline()
		switch {
		case where == dir && len(args) > 0 && args[0] == "fetch":
			checked = true
			if hasDeadline {
				t.Errorf("the fetch into the checkout has a deadline")
			}
		case where != dir && slices.Contains(args, "--filter=tree:0"):
			limitedHistory = hasDeadline
		}
		return host(ctx, where, args...)
	}
	var report strings.Builder
	ctx := hostgit.WithBase(context.Background(), os.Environ())
	b.setBase(ctx, watch, &report, dir, "git.example.com", "acme/shop", head)
	if b.Base == nil || b.Base.Commit != first || report.Len() != 0 {
		t.Errorf("base = %+v, report %q, want the first commit %s", b.Base, report.String(), first)
	}
	if !checked || !limitedHistory {
		t.Errorf("the fetch into the checkout ran: %v, the history read had the limit: %v", checked, limitedHistory)
	}
}

// TestBranchBaseIsTheStartCommitWhenTheTargetDescendsFromIt is a branch whose start commit
// is the first commit of a target that has moved on, so the merge base is the start commit
// itself: the base has that commit and its tree, and findBase fetches nothing into the
// checkout, which has the start commit already.
func TestBranchBaseIsTheStartCommitWhenTheTargetDescendsFromIt(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	first, head := movedRemote(t, base, "acme/shop")
	b, dir, _ := baseFetch(t, head, versionOf("acme/shop", first))
	b.Base = nil

	host := hostRunner(&testProxy)
	fetches := 0
	counting := func(ctx context.Context, where string, args ...string) (string, error) {
		if where == dir && len(args) > 0 && args[0] == "fetch" {
			fetches++
		}
		return host(ctx, where, args...)
	}
	ctx := hostgit.WithBase(context.Background(), os.Environ())
	got, err := b.findBase(ctx, counting, dir, "git.example.com", "acme/shop", head)
	want := run(t, filepath.Join(base, "acme", "shop.git"), "rev-parse", first+"^{tree}")
	if err != nil || got == nil || got.Commit != first || got.Tree != want {
		t.Errorf("findBase = %+v, %v, want the start commit %s with the tree %s", got, err, first, want)
	}
	if fetches != 0 {
		t.Errorf("findBase fetched %d times into the checkout, want none", fetches)
	}
}
