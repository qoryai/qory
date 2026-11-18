package preparation

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestACommitThatHadTheHandoffFolderIsFoundAfterALaterCommitRemovedIt is a history where one
// commit adds .qory/handoff/submission.json and the next removes it, and a merge of a side
// branch that adds it: each range that has such a commit is refused with the commit and the
// path, and a range with none passes.
func TestACommitThatHadTheHandoffFolderIsFoundAfterALaterCommitRemovedIt(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "-b", "main")
	run(t, dir, "config", "user.name", "qory")
	run(t, dir, "config", "user.email", "qory@localhost")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	start := commitHere(t, dir)
	write(t, filepath.Join(dir, "b.txt"), "b\n")
	plain := commitHere(t, dir)
	write(t, filepath.Join(dir, ".qory", "handoff", "submission.json"), "{}\n")
	run(t, dir, "add", "-f", ".qory/handoff/submission.json")
	added := commitHere(t, dir)
	run(t, dir, "rm", "--quiet", "-f", ".qory/handoff/submission.json")
	removed := commitHere(t, dir)

	git := stepGit(noRemote, "")
	r := WorkRepository{Path: "acme/site"}
	err := handoffCommitted(context.Background(), git, dir, "git.test", r, start, removed)
	want := "qory run: A commit of this run has a path under .qory/handoff/, so qory did not push.\n  Repository git.test/acme/site.\n  Commit " + added + ".\n  Path .qory/handoff/submission.json."
	if err == nil || err.Error() != want {
		t.Errorf("the refusal is %v, want\n%s", err, want)
	}
	if err := handoffCommitted(context.Background(), git, dir, "git.test", r, start, plain); err != nil {
		t.Errorf("a range with no such commit is refused: %v", err)
	}
	if err := handoffCommitted(context.Background(), git, dir, "git.test", r, removed, removed); err != nil {
		t.Errorf("an empty range is refused: %v", err)
	}

	// A merge that brings the file from a side branch is refused too.
	run(t, dir, "checkout", "--quiet", "-b", "side", plain)
	write(t, filepath.Join(dir, ".qory", "handoff", "submission.json"), "{}\n")
	run(t, dir, "add", "-f", ".qory/handoff/submission.json")
	run(t, dir, "-c", "user.name=qory", "-c", "user.email=qory@localhost", "commit", "--quiet", "-m", "side")
	run(t, dir, "checkout", "--quiet", "main")
	run(t, dir, "-c", "user.name=qory", "-c", "user.email=qory@localhost", "merge", "--quiet", "--no-ff", "-m", "merge", "side")
	merged := run(t, dir, "rev-parse", "HEAD")
	if err := handoffCommitted(context.Background(), git, dir, "git.test", r, removed, merged); err == nil || !strings.Contains(err.Error(), ".qory/handoff/submission.json") {
		t.Errorf("a merge range with the file is not refused: %v", err)
	}
}

// TestACommitWithTheHandoffFolderInAnotherCaseIsRefused is a commit that adds .QORY/Handoff/x: a
// forge on a disk that ignores case keeps the folder under both names, so the check ignores case.
func TestACommitWithTheHandoffFolderInAnotherCaseIsRefused(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "-b", "main")
	run(t, dir, "config", "user.name", "qory")
	run(t, dir, "config", "user.email", "qory@localhost")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	start := commitHere(t, dir)
	write(t, filepath.Join(dir, ".QORY", "Handoff", "x"), "x\n")
	run(t, dir, "add", "-f", ".QORY/Handoff/x")
	added := commitHere(t, dir)
	err := handoffCommitted(context.Background(), stepGit(noRemote, ""), dir, "git.test", WorkRepository{Path: "acme/site"}, start, added)
	if err == nil || !strings.Contains(err.Error(), "Commit "+added+".") || !strings.Contains(err.Error(), "Path .QORY/Handoff/x.") {
		t.Errorf("the refusal is %v, want the commit %s and the path .QORY/Handoff/x", err, added)
	}
}
