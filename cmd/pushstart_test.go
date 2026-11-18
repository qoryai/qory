package cmd_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/integrations/contracts"
	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// pushedRow is a row of the work item's list of pushed commits for acme/site.
func pushedRow(commit string, cancelled bool) string {
	return fmt.Sprintf(`{"system":%q,"path":"acme/site","commit":%q,"run_id":"run_old","cancelled":%v,"at":"2026-11-16T09:00:00Z"}`, proxygittest.Host, commit, cancelled)
}

// timedOut is the result of a session that the time limit stopped.
func timedOut() (*session.Result, error) {
	return &session.Result{State: event.StateCancelled, TimedOut: true, ExitCode: 124}, nil
}

// TestARunAfterATimeLimitStartsAtTheNewestPushedCommit is a run that the time limit stopped
// after its agent made one commit and left edits, so the step pushed 2 commits: the stored
// version is at the branch's head, and the next run, whose list of pushed commits has both
// as not cancelled, starts at the newest one, replaces nothing and pushes on top of it.
func TestARunAfterATimeLimitStartsAtTheNewestPushedCommit(t *testing.T) {
	p := newPushRepo(t, "write")
	first := p.run(t, writesAndCommits(t), timedOut)
	first.wantsClose(t, event.StateCancelled, 124)
	head := p.branchHead(t)
	pushed := strings.Fields(proxygittest.Git(t, p.bare, "rev-list", "--reverse", p.first+".."+head))
	if len(pushed) != 2 {
		t.Fatalf("the time-limited run pushed %d commits, want 2\n%s", len(pushed), first.out)
	}
	doc := p.api.document(t, 1)
	if got := doc.Repositories[0].Commit; got != head {
		t.Errorf("the stored version is at %s, want the branch's head %s", got, head)
	}
	if got := doc.Repositories[0].Commits; len(got) != 2 || got[0].Commit != pushed[0] || got[1].Commit != pushed[1] {
		t.Errorf("the version lists %+v, want the 2 pushed commits in order", got)
	}

	p.again(t)
	p.api.number = 58
	p.api.pushedCommits = "[" + pushedRow(pushed[0], false) + "," + pushedRow(pushed[1], false) + "]"
	var startedAt string
	second := p.run(t, func(dir string) {
		startedAt = gitOut(t, dir, "rev-parse", "HEAD")
		writeFile(t, filepath.Join(dir, "second.txt"), "second\n")
	}, nil)
	second.wantsClose(t, event.StateSucceeded, 0)
	if startedAt != pushed[1] {
		t.Errorf("the second run started at %s, want the newest pushed commit %s", startedAt, pushed[1])
	}
	next := p.branchHead(t)
	if got := proxygittest.Git(t, p.bare, "merge-base", pushed[1], next); got != pushed[1] || next == pushed[1] {
		t.Errorf("the branch went from %s to %s, not as a fast-forward", pushed[1], next)
	}
	for _, commit := range pushed {
		if got := proxygittest.Git(t, p.bare, "merge-base", commit, next); got != commit {
			t.Errorf("commit %s of the first run is not on the branch", commit)
		}
	}
	if base := p.api.document(t, 2).Repositories[0].Base; base == nil || base.Commit != pushed[1] {
		t.Errorf("the second version's base is %+v, want %s", base, pushed[1])
	}
}

// TestALaterSessionWhoseHeadMovedAboveTheLastPushIsRefused is a run of two sessions where
// another push put a commit above session 1's push before session 2's push: the second push
// stops, the branch stays at the other push's commit, no second version is stored, and the run
// closes failed with push_head_changed and the refusal's "this run's last push".
func TestALaterSessionWhoseHeadMovedAboveTheLastPushIsRefused(t *testing.T) {
	p := newPushRepo(t, "write")
	var dir, moved string
	t.Cleanup(cmd.SetLaterSessions(func() {
		other := tempDir(t)
		proxygittest.Git(t, other, "clone", "--quiet", p.bare, ".")
		proxygittest.Git(t, other, "checkout", "--quiet", "qory/"+workItem)
		writeFile(t, filepath.Join(other, "other.txt"), "other\n")
		commitAll(t, other, "Another push")
		proxygittest.Git(t, other, "push", "--quiet", "origin", "HEAD:refs/heads/qory/"+workItem)
		moved = p.branchHead(t)
		writeFile(t, filepath.Join(dir, "second.txt"), "second\n")
		commitAll(t, dir, "Add a second file")
	}))
	r := p.run(t, func(d string) {
		dir = d
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	if got := r.closes[0].Reason; got != string(contracts.EndCodePushHeadChanged) {
		t.Errorf("the run closed with the reason %q, want %q", got, contracts.EndCodePushHeadChanged)
	}
	wants(t, r.out, "Head: "+moved+". Expected: ", ", this run's last push.")
	if got := p.branchHead(t); moved == "" || got != moved {
		t.Errorf("the branch is at %s, want the other push's %q", got, moved)
	}
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 1 {
		t.Errorf("%d POST and %d versions, want only session 1's", posts, stored)
	}
}

// TestARunReplacesTwoCancelledCommits is a branch with 2 commits above the start that a run
// which a person cancelled pushed: the next run's first push replaces both, the branch
// descends from the start commit and holds neither, and the version's base is the start.
func TestARunReplacesTwoCancelledCommits(t *testing.T) {
	p := newPushRepo(t, "write")
	other := tempDir(t)
	proxygittest.Git(t, other, "clone", "--quiet", p.bare, ".")
	proxygittest.Git(t, other, "checkout", "--quiet", p.first)
	for _, name := range []string{"one.txt", "two.txt"} {
		writeFile(t, filepath.Join(other, name), name+"\n")
		commitAll(t, other, "Cancelled "+name)
	}
	proxygittest.Git(t, other, "push", "--quiet", "origin", "HEAD:refs/heads/qory/"+workItem)
	old := strings.Fields(proxygittest.Git(t, p.bare, "rev-list", "--reverse", p.first+".."+p.branchHead(t)))
	if len(old) != 2 {
		t.Fatalf("the cancelled run pushed %d commits, want 2", len(old))
	}
	p.api.pushedCommits = "[" + pushedRow(old[0], true) + "," + pushedRow(old[1], true) + "]"

	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	head := p.branchHead(t)
	if got := proxygittest.Git(t, p.bare, "merge-base", p.first, head); got != p.first {
		t.Errorf("the merge base is %s, want the start commit %s", got, p.first)
	}
	for _, commit := range old {
		if got := proxygittest.Git(t, p.bare, "rev-list", "--count", commit+"..."+head); got == "0" || proxygittest.Git(t, p.bare, "merge-base", commit, head) == commit {
			t.Errorf("the cancelled commit %s is on the branch", commit)
		}
	}
	if base := p.api.document(t, 1).Repositories[0].Base; base == nil || base.Commit != p.first {
		t.Errorf("the base is %+v, want %s", base, p.first)
	}
}
