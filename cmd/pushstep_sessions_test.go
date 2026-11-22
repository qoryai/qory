package cmd_test

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// pushSteps is the state each push step of r exited with, in order.
func (r pushed) pushSteps() []string {
	var states []string
	push := map[int]bool{}
	for _, e := range r.steps {
		switch {
		case e.Type == "step_started" && e.Kind == event.StepPush:
			push[e.Step] = true
		case e.Type == "step_exited" && push[e.Step]:
			states = append(states, e.State)
		}
	}
	return states
}

// TestTheCommitsOfASessionAreOnTheBranchBeforeTheNextSession is a write run of two
// sessions, the second a stand-in, with the push step after each: the commit of session 1
// is on the forge's branch, with its version stored, before session 2 starts, and session
// 2's commit follows it, with a second version.
func TestTheCommitsOfASessionAreOnTheBranchBeforeTheNextSession(t *testing.T) {
	p := newPushRepo(t, "write")
	var dir, beforeSecond string
	var postsBeforeSecond int
	t.Cleanup(cmd.SetLaterSessions(func() {
		beforeSecond = p.branchHead(t)
		postsBeforeSecond, _, _ = p.api.counts()
		writeFile(t, filepath.Join(dir, "second.txt"), "second\n")
		commitAll(t, dir, "Add a second file")
		// Session 2 writes its output too: the push step of session 1 removed that of session 1.
		writeFile(t, filepath.Join(dir, ".qory", "outbox", "pull-request", "pull-request.json"), `{"title":"Add a second page"}`)
	}))
	r := p.run(t, func(d string) {
		dir = d
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := proxygittest.Git(t, p.bare, "log", "--format=%s", p.first+".."+beforeSecond); got != "Add a first file" {
		t.Errorf("before session 2 the branch has the commits:\n%s", got)
	}
	if postsBeforeSecond != 1 {
		t.Errorf("%d POST before session 2, want 1", postsBeforeSecond)
	}
	head := p.branchHead(t)
	if got := proxygittest.Git(t, p.bare, "log", "--format=%s", p.first+".."+head); got != "Add a second file\nAdd a first file" {
		t.Errorf("the branch's new commits are:\n%s", got)
	}
	if got := headOf(p.api.document(t, 1)); got != beforeSecond {
		t.Errorf("version 1 is at %s, want session 1's %s", got, beforeSecond)
	}
	if got := headOf(p.api.document(t, 2)); got != head {
		t.Errorf("version 2 is at %s, want session 2's %s", got, head)
	}
	if got := r.pushSteps(); len(got) != 2 || got[0] != event.StateSucceeded || got[1] != event.StateSucceeded {
		t.Errorf("the push steps exited %v, want two succeeded", got)
	}
	wants(t, r.log(), "qory run: pushed qory/"+workItem+" to acme/site at "+beforeSecond[:min(len(beforeSecond), 12)], "qory run: pushed qory/"+workItem+" to acme/site at "+head[:12])
}

// TestASessionWithNoCommitsAfterAPushPushesNothing is a write run whose second session, a
// stand-in, commits nothing: its push step pushes nothing and stores no version, and the
// branch stays at session 1's commit.
func TestASessionWithNoCommitsAfterAPushPushesNothing(t *testing.T) {
	p := newPushRepo(t, "write")
	var first string
	t.Cleanup(cmd.SetLaterSessions(func() { first = p.branchHead(t) }))
	r := p.run(t, func(d string) {
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if head := p.branchHead(t); first == "" || head != first {
		t.Errorf("the branch is at %q, want session 1's %q", head, first)
	}
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 1 {
		t.Errorf("%d POST and %d versions, want 1 and 1", posts, stored)
	}
	if got := r.pushSteps(); len(got) != 2 || got[1] != event.StateSucceeded {
		t.Errorf("the push steps exited %v, want two succeeded", got)
	}
	if got, want := r.log(), "qory run: pushed qory/"+workItem+" to acme/site at "+first[:min(len(first), 12)]+"\nqory run: no new commits to push\n"; got != want {
		t.Errorf("the push steps' lines are %q, want %q", got, want)
	}
}

// TestAFailedStoreAfterASessionEndsTheRunBeforeTheNextSession is a write run of two
// sessions, the second a stand-in, whose first push lands but whose POST of the version
// fails with run_ended: the run closes failed with that failure, session 2 does not run,
// and the branch stays at session 1's commit with no version stored.
func TestAFailedStoreAfterASessionEndsTheRunBeforeTheNextSession(t *testing.T) {
	p := newPushRepo(t, "write")
	p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
		failing(w, http.StatusConflict, "run_ended", "")
		return true, false
	}
	secondRan := false
	t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
	r := p.run(t, func(d string) {
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	if secondRan {
		t.Error("session 2 ran after the push step of session 1 failed")
	}
	if got := r.pushSteps(); len(got) != 1 || got[0] != event.StateFailed {
		t.Errorf("the push steps exited %v, want one failed", got)
	}
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 0 {
		t.Errorf("%d POST and %d versions, want 1 and 0", posts, stored)
	}
	if head := p.branchHead(t); head == "" || head == p.first {
		t.Errorf("the branch is at %q: session 1's push did not land past %s", head, p.first)
	}
}
