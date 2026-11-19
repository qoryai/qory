package cmd_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// sharedPushRepo is a [pushRepo] whose documents set shared_run, with a harness document
// that defines a stack of the module hello of acme/site and its workflow hello, write. The
// next-session request after session n gets answers[n-1], or the end once answers run out;
// requests counts the requests.
func sharedPushRepo(t *testing.T, answers ...string) (p *pushRepo, requests func() int) {
	t.Helper()
	p = newPushRepo(t, "write")
	url := p.h.server.URL("acme/site")
	p.stackProxy = true
	p.change = func(d map[string]any) {
		d["shared_run"] = true
		d["harness"] = map[string]any{"harness": map[string]any{
			"modules":   []map[string]any{{"name": "hello", "source": map[string]any{"git": url, "ref": "main", "path": "modules/hello"}}},
			"workflows": map[string]any{"hello": map[string]any{"access": "write"}},
		}}
	}
	var mu sync.Mutex
	n := 0
	p.nextSession = func(context.Context, []byte) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n <= len(answers) {
			return []byte(answers[n-1]), nil
		}
		return []byte(`{"next":"end"}`), nil
	}
	return p, func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
}

// numbered is the result of each join of a run: a session that succeeded, numbered by the
// gateway from 1.
func numbered() func() (*session.Result, error) {
	var mu sync.Mutex
	n := 0
	return func() (*session.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		return &session.Result{State: event.StateSucceeded, Session: n}, nil
	}
}

// TestTheNextSessionStartsAfterThePushOfTheSessionBefore is a write run of two sessions on
// claude, each of which commits: session 1's commit is on the forge's branch, with its
// version stored, before session 2 joins; session 2's commit follows it, with a second
// version; and the run closes succeeded after two push steps and two next-session requests.
func TestTheNextSessionStartsAfterThePushOfTheSessionBefore(t *testing.T) {
	p, requests := sharedPushRepo(t, laterAnswer(2, "claude", "example-provider-2", 30))
	joins := 0
	var beforeSecond string
	var postsBeforeSecond int
	r := p.run(t, func(d string) {
		joins++
		if joins == 1 {
			writeFile(t, filepath.Join(d, "first.txt"), "first\n")
			commitAll(t, d, "Add a first file")
			return
		}
		beforeSecond = p.branchHead(t)
		postsBeforeSecond, _, _ = p.api.counts()
		writeFile(t, filepath.Join(d, "second.txt"), "second\n")
		commitAll(t, d, "Add a second file")
	}, numbered())
	r.wantsClose(t, event.StateSucceeded, 0)
	if joins != 2 || requests() != 2 {
		t.Fatalf("%d joins and %d next-session requests, want 2 and 2\n%s", joins, requests(), r.out)
	}
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
	if got := r.pushSteps(); len(got) != 2 || got[0] != event.StateSucceeded || got[1] != event.StateSucceeded {
		t.Errorf("the push steps exited %v, want two succeeded", got)
	}
}

// TestAFailedPushStartsNoNextSession is a write run whose POST of session 1's version fails
// with run_ended: the run closes failed, sends no next-session request, and session 2 does
// not join.
func TestAFailedPushStartsNoNextSession(t *testing.T) {
	p, requests := sharedPushRepo(t, laterAnswer(2, "claude", "example-provider-2", 30))
	p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
		failing(w, http.StatusConflict, "run_ended", "")
		return true, false
	}
	joins := 0
	r := p.run(t, func(d string) {
		joins++
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}, numbered())
	r.wantsClose(t, event.StateFailed, 1)
	if joins != 1 || requests() != 0 {
		t.Errorf("%d joins and %d next-session requests, want 1 and 0", joins, requests())
	}
}

// TestAFailureBeforeSession2KeepsThePushOfSession1 is a write run whose answer starts session
// 2 on a runtime qory does not run: the run closes failed, exit 1, before session 2 joins,
// and the branch keeps session 1's commit with its version stored.
func TestAFailureBeforeSession2KeepsThePushOfSession1(t *testing.T) {
	p, requests := sharedPushRepo(t, laterAnswer(2, "example-runtime", "example-provider-2", 30))
	joins := 0
	r := p.run(t, func(d string) {
		joins++
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}, numbered())
	r.wantsClose(t, event.StateFailed, 1)
	if joins != 1 || requests() != 1 {
		t.Errorf("%d joins and %d next-session requests, want 1 and 1", joins, requests())
	}
	head := p.branchHead(t)
	if got := proxygittest.Git(t, p.bare, "log", "--format=%s", p.first+".."+head); got != "Add a first file" {
		t.Errorf("the branch's new commits are:\n%s", got)
	}
	if posts, _, stored := p.api.counts(); posts != 1 || stored != 1 {
		t.Errorf("%d POST and %d versions, want 1 and 1", posts, stored)
	}
	if !strings.Contains(r.out, "example-runtime") {
		t.Errorf("the output does not show the runtime:\n%s", r.out)
	}
}
