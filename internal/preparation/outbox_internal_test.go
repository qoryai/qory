package preparation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestACommitThatHadTheOutboxIsFoundAfterALaterCommitRemovedIt is a history where one
// commit adds .qory/outbox/pull-request/pull-request.json and the next removes it, and a merge of a side
// branch that adds it: each range that has such a commit is refused with the commit and the
// path, and a range with none passes.
func TestACommitThatHadTheOutboxIsFoundAfterALaterCommitRemovedIt(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "-b", "main")
	run(t, dir, "config", "user.name", "qory")
	run(t, dir, "config", "user.email", "qory@localhost")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	start := commitHere(t, dir)
	write(t, filepath.Join(dir, "b.txt"), "b\n")
	plain := commitHere(t, dir)
	write(t, filepath.Join(dir, ".qory", "outbox", "pull-request", "pull-request.json"), "{}\n")
	run(t, dir, "add", "-f", ".qory/outbox/pull-request/pull-request.json")
	added := commitHere(t, dir)
	run(t, dir, "rm", "--quiet", "-f", ".qory/outbox/pull-request/pull-request.json")
	removed := commitHere(t, dir)

	git := stepGit(noRemote, "")
	r := WorkRepository{Path: "acme/site"}
	err := outboxCommitted(context.Background(), git, dir, "git.test", r, start, removed, true)
	want := "qory run: Keep .qory/outbox/ out of the commits of this run; qory pushed nothing.\n  A commit of this run has a path under .qory/outbox/.\n  Repository git.test/acme/site.\n  Commit " + added + ".\n  Path .qory/outbox/pull-request/pull-request.json."
	if err == nil || err.Error() != want {
		t.Errorf("the refusal is %v, want\n%s", err, want)
	}
	if err := outboxCommitted(context.Background(), git, dir, "git.test", r, start, plain, true); err != nil {
		t.Errorf("a range with no such commit is refused: %v", err)
	}
	if err := outboxCommitted(context.Background(), git, dir, "git.test", r, removed, removed, true); err != nil {
		t.Errorf("an empty range is refused: %v", err)
	}

	// A merge that brings the file from a side branch is refused too.
	run(t, dir, "checkout", "--quiet", "-b", "side", plain)
	write(t, filepath.Join(dir, ".qory", "outbox", "pull-request", "pull-request.json"), "{}\n")
	run(t, dir, "add", "-f", ".qory/outbox/pull-request/pull-request.json")
	run(t, dir, "-c", "user.name=qory", "-c", "user.email=qory@localhost", "commit", "--quiet", "-m", "side")
	run(t, dir, "checkout", "--quiet", "main")
	run(t, dir, "-c", "user.name=qory", "-c", "user.email=qory@localhost", "merge", "--quiet", "--no-ff", "-m", "merge", "side")
	merged := run(t, dir, "rev-parse", "HEAD")
	if err := outboxCommitted(context.Background(), git, dir, "git.test", r, removed, merged, true); err == nil || !strings.Contains(err.Error(), ".qory/outbox/pull-request/pull-request.json") {
		t.Errorf("a merge range with the file is not refused: %v", err)
	}
}

// TestACommitWithTheOutboxInAnotherCaseIsRefused is a commit that adds .QORY/Outbox/x: a
// forge on a disk that ignores case keeps the folder under both names, so the check ignores case.
func TestACommitWithTheOutboxInAnotherCaseIsRefused(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "-b", "main")
	run(t, dir, "config", "user.name", "qory")
	run(t, dir, "config", "user.email", "qory@localhost")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	start := commitHere(t, dir)
	write(t, filepath.Join(dir, ".QORY", "Outbox", "x"), "x\n")
	run(t, dir, "add", "-f", ".QORY/Outbox/x")
	added := commitHere(t, dir)
	err := outboxCommitted(context.Background(), stepGit(noRemote, ""), dir, "git.test", WorkRepository{Path: "acme/site"}, start, added, true)
	if err == nil || !strings.Contains(err.Error(), "Commit "+added+".") || !strings.Contains(err.Error(), "Path .QORY/Outbox/x.") {
		t.Errorf("the refusal is %v, want the commit %s and the path .QORY/Outbox/x", err, added)
	}
}

// TestAListOfCommitsThatFailsSaysTheListFailed is a start commit that the repository does not
// have, so git rev-list fails: the error is the list's, with the fix on line 1, and not the
// text of the check that HEAD descends from a commit.
func TestAListOfCommitsThatFailsSaysTheListFailed(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "-b", "main")
	run(t, dir, "config", "user.name", "qory")
	run(t, dir, "config", "user.email", "qory@localhost")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	head := commitHere(t, dir)
	err := outboxCommitted(context.Background(), stepGit(noRemote, ""), dir, "git.test", WorkRepository{Path: "acme/site"}, strings.Repeat("1", 40), head, true)
	want := "qory run: Start the run again: the list of the run's commits failed.\n  Listing the commits of acme/site from commit 111111111111 to " + head[:12] + " failed: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), ". qory pushed nothing. The run code is used up.") {
		t.Fatalf("the error is %v, want the start %q", err, want)
	}
	if strings.Contains(err.Error(), "descends from") {
		t.Errorf("the error has the text of the history check: %v", err)
	}
}

// TestOutboxCommittedOfALocalRunNamesNoRunCode is [OutboxCommitted], the check of a local run,
// whose list of commits fails: the text ends with "qory pushed nothing.", with no words about a
// run code, which a local run does not have.
func TestOutboxCommittedOfALocalRunNamesNoRunCode(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "-b", "main")
	run(t, dir, "config", "user.name", "qory")
	run(t, dir, "config", "user.email", "qory@localhost")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	head := commitHere(t, dir)
	err := OutboxCommitted(context.Background(), dir, "git.test", "acme/site", strings.Repeat("1", 40), head)
	if err == nil || !strings.HasPrefix(err.Error(), "qory run: Start the run again: ") || !strings.HasSuffix(err.Error(), ". qory pushed nothing.") {
		t.Fatalf("the error is %v", err)
	}
	if strings.Contains(err.Error(), "run code") {
		t.Errorf("the text of a local run names a run code: %v", err)
	}
}
