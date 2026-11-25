package cmd_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/hookrun"
)

// pushHook is a required check hook at the end of the workflow hello, which runs
// scripts/check.sh of the repository of [newPushRepoWith].
var pushHook = compose.Hook{
	Workflow: "hello", At: "after-hello", PointAt: hookrun.PointEnd, Kind: "check", Name: "check.sh",
	Run: []string{"scripts/check.sh"}, Required: true, TimeoutSeconds: 60, Index: 0,
}

// hookRepo is a write run's repository with scripts/check.sh, whose sessions have the hook
// [pushHook], which runs f in place of its container.
func hookRepo(t *testing.T, f func(dir string) int) *pushRepo {
	t.Helper()
	p := newPushRepoWith(t, "write", map[string]string{"scripts/check.sh": "#!/bin/sh\n"})
	t.Cleanup(cmd.SetReadHooks(func(string) ([]compose.Hook, error) { return []compose.Hook{pushHook}, nil }))
	t.Cleanup(cmd.SetHookContainer(f))
	return p
}

// stepIndex is the place among events of the first step that started with kind, -1 for none.
func stepIndex(events []cmd.StepEvent, kind string) int {
	return slices.IndexFunc(events, func(e cmd.StepEvent) bool { return e.Type == "step_started" && e.Kind == kind })
}

// TestARedHookRunsBeforeThePushAndTheNextSession is a write run of two sessions, the second a
// stand-in, whose hook exits 1: after session 1 the hook runs in a build step, its verdict
// changes-requested is posted, and then the push step and session 2 follow.
func TestARedHookRunsBeforeThePushAndTheNextSession(t *testing.T) {
	ran := 0
	p := hookRepo(t, func(string) int { ran++; return 1 })
	verdicts := verdictAPI(p)
	verdictsAtPush, ranBeforeSecond := -1, -1
	p.pushStarted = func() {
		if verdictsAtPush < 0 {
			p.api.mu.Lock()
			verdictsAtPush = len(*verdicts)
			p.api.mu.Unlock()
		}
	}
	t.Cleanup(cmd.SetLaterSessions(func() { ranBeforeSecond = ran }))
	r := p.run(t, func(d string) {
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	hook, push := stepIndex(r.steps, event.StepBuild), stepIndex(r.steps, event.StepPush)
	if hook < 0 || push < 0 || hook > push {
		t.Errorf("the hook's step is event %d and the push step's %d, want the hook's first", hook, push)
	}
	if ranBeforeSecond != 1 {
		t.Errorf("the hook ran %d times before session 2, want 1", ranBeforeSecond)
	}
	want := `{"verdict":"changes-requested","points":[{"text":"The hook check.sh failed: it exited with status 1.","action":"run_again"}]}`
	if verdictsAtPush != 1 || len(*verdicts) != 1 || (*verdicts)[0] != want {
		t.Errorf("%d verdicts posted before the push step, of %q, want %s", verdictsAtPush, *verdicts, want)
	}
}

// TestAHookBindsNoReadTarget is a write run of acme/site and the read target acme/lib whose hook
// runs after session 1: the hook's container binds .qory, where acme/lib is, read-only, and
// nothing below it.
func TestAHookBindsNoReadTarget(t *testing.T) {
	p := hookRepo(t, func(string) int { return 0 })
	var binds []wall.Mount
	t.Cleanup(cmd.SetHookMounts(func(m []wall.Mount) int { binds = m; return 0 }))
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	p.repositories = "acme/site,acme/lib"
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	qory := filepath.Join(p.h.dir, ".qory")
	if !slices.Contains(binds, wall.Mount{Path: qory, ReadOnly: true}) {
		t.Errorf("the hook's binds %+v, want %s read-only", binds, qory)
	}
	if slices.ContainsFunc(binds, func(m wall.Mount) bool { return strings.HasPrefix(m.Path, qory+string(filepath.Separator)) }) {
		t.Errorf("the hook's binds %+v have one below %s", binds, qory)
	}
}

// TestSession1sStartHookRunsBeforeTheSetupCommands is a write run whose hook is at before-hello,
// the start of hello, and whose run configuration lists a repository: the hook's container runs
// in a build step before the setup commands start theirs, and its red verdict is posted after
// the session, before the push step.
func TestSession1sStartHookRunsBeforeTheSetupCommands(t *testing.T) {
	var order []string
	p := hookRepo(t, func(string) int { order = append(order, "hook"); return 1 })
	start := pushHook
	start.At, start.PointAt = "before-hello", hookrun.PointStart
	t.Cleanup(cmd.SetReadHooks(func(string) ([]compose.Hook, error) { return []compose.Hook{start}, nil }))
	p.onSetup = func(session.SetupSpec) { order = append(order, "setup") }
	verdicts := verdictAPI(p)
	r := p.run(t, nil, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if want := []string{"hook", "setup"}; !slices.Equal(order, want) {
		t.Errorf("the order %q, want %q", order, want)
	}
	hook, push := stepIndex(r.steps, event.StepBuild), stepIndex(r.steps, event.StepPush)
	if hook < 0 || push < 0 || hook > push || r.steps[hook].Name != "hook check.sh at before-hello" {
		t.Errorf("the hook's step is event %d and the push step's %d, want the hook's first", hook, push)
	}
	want := `{"verdict":"changes-requested","points":[{"text":"The hook check.sh failed: it exited with status 1.","action":"run_again"}]}`
	if len(*verdicts) != 1 || (*verdicts)[0] != want {
		t.Errorf("posted %q, want %s", *verdicts, want)
	}
}

// TestAStartHookThatLeavesARepositoryStartsNoSetupAndNoSession is a write run whose hook at
// before-hello, the start of hello, makes a repository in the checkout: the restore fails, the
// setup commands do not start, the session does not join, and the run closes failed.
func TestAStartHookThatLeavesARepositoryStartsNoSetupAndNoSession(t *testing.T) {
	p := hookRepo(t, func(dir string) int {
		if out, err := exec.Command("git", "init", "-q", filepath.Join(dir, "nested")).CombinedOutput(); err != nil {
			t.Errorf("git init: %v %s", err, out)
		}
		return 0
	})
	start := pushHook
	start.At, start.PointAt = "before-hello", hookrun.PointStart
	t.Cleanup(cmd.SetReadHooks(func(string) ([]compose.Hook, error) { return []compose.Hook{start}, nil }))
	setup, joined := false, false
	p.onSetup = func(session.SetupSpec) { setup = true }
	r := p.run(t, func(string) { joined = true }, func() (*session.Result, error) { return succeeded() })
	r.wantsClose(t, event.StateFailed, 1)
	if setup || joined {
		t.Errorf("the setup started %v, the session joined %v, want neither", setup, joined)
	}
	wants(t, r.out, "qory run: The session did not start: qory could not undo what a hook at its start changed.")
}

// TestStartHooksPastTheSetupsLimitKeepTheirRecords is the run of
// [TestSession1sStartHookRunsBeforeTheSetupCommands] whose hook runs past the setup's limit:
// the hook is red, ended by the limit, the run ends at the limit with the hooks' step in its
// line, and the hook's record is written.
func TestStartHooksPastTheSetupsLimitKeepTheirRecords(t *testing.T) {
	limit := 3 * time.Second
	defer cmd.SetSetupLimit(limit)()
	p := hookRepo(t, func(string) int { time.Sleep(limit); return 0 })
	start := pushHook
	start.At, start.PointAt = "before-hello", hookrun.PointStart
	t.Cleanup(cmd.SetReadHooks(func(string) ([]compose.Hook, error) { return []compose.Hook{start}, nil }))
	r := p.run(t, nil, nil)
	r.wantsClose(t, event.StateCancelled, 124)
	wants(t, r.out, "the hooks at session 1's start")
	data, err := os.ReadFile(filepath.Join(p.h.runDir, hookrun.Folder, "session-1.json"))
	if err != nil || !strings.Contains(string(data), `"verdict": "red"`) || !strings.Contains(string(data), `"cause": "it ran past the setup's limit"`) {
		t.Errorf("the records %s, %v", data, err)
	}
}

// verdictAPI makes the run API of p take POST /verdict, 201, and returns the bodies posted so
// far; the caller holds p.api.mu, or the run has ended.
func verdictAPI(p *pushRepo) *[]string {
	var verdicts []string
	p.api.other = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/verdict") {
			return false
		}
		body, _ := io.ReadAll(r.Body)
		verdicts = append(verdicts, string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"version":1,"artifact":{"type":"verdict","name":"main","version":{"id":"v1","number":1}}}`)
		return true
	}
	return &verdicts
}

// TestARedHooksVerdictIsPostedBeforeTheNextSessionRequest is a shared run of two sessions
// whose hook is red after each: the verdict of session n is posted before the n-th
// next-session request, the last of which ends the run.
func TestARedHooksVerdictIsPostedBeforeTheNextSessionRequest(t *testing.T) {
	p, requests := sharedPushRepo(t, laterAnswer(2, "claude", "example-provider-2", 30))
	t.Cleanup(cmd.SetReadHooks(func(string) ([]compose.Hook, error) { return []compose.Hook{pushHook}, nil }))
	t.Cleanup(cmd.SetHookContainer(func(string) int { return 1 }))
	verdicts := verdictAPI(p)
	var atRequest []int
	next := p.nextSession
	p.nextSession = func(ctx context.Context, body []byte) ([]byte, error) {
		p.api.mu.Lock()
		atRequest = append(atRequest, len(*verdicts))
		p.api.mu.Unlock()
		return next(ctx, body)
	}
	r := p.run(t, nil, numbered())
	r.wantsClose(t, event.StateSucceeded, 0)
	if requests() != 2 || !slices.Equal(atRequest, []int{1, 2}) {
		t.Errorf("%d next-session requests, with %v verdicts posted before each, want 2 with [1 2]\n%s", requests(), atRequest, r.out)
	}
	// The repository has no scripts/check.sh, so each hook is red as one that did not run.
	for _, v := range *verdicts {
		if !strings.HasPrefix(v, `{"verdict":"changes-requested","points":[{"text":"The hook check.sh `) {
			t.Errorf("posted %s, want changes-requested for the hook", v)
		}
	}
}

// TestAHookThatLeavesARepositoryEndsTheRunWithNoPush is a write run whose hook leaves a
// repository in the checkout, which the restore does not remove: the run closes failed and
// no push step runs.
func TestAHookThatLeavesARepositoryEndsTheRunWithNoPush(t *testing.T) {
	p := hookRepo(t, func(dir string) int { nestedRepository(t, dir); return 0 })
	r := p.run(t, func(d string) {
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	if got := r.pushSteps(); len(got) != 0 {
		t.Errorf("the push steps exited %v, want none", got)
	}
	if head := p.branchHead(t); head != "" && head != p.first {
		t.Errorf("the branch is at %s, want %s", head, p.first)
	}
	r.wantsReason(t, "")
	wants(t, r.out, "qory run: qory could not undo what hook ")
	wants(t, r.out, " changed; no push follows.\n  ")
}

// TestHooksThatCannotBeReadEndTheRunBeforeTheJoin is a write run from a run code whose hooks
// fail to read: the session does not join, and the run closes failed with harness_invalid and
// the line of the cause alone.
func TestHooksThatCannotBeReadEndTheRunBeforeTheJoin(t *testing.T) {
	p := newPushRepo(t, "write")
	t.Cleanup(cmd.SetReadHooks(func(string) ([]compose.Hook, error) { return nil, errors.New("hooks.json is not a hooks file") }))
	joined := false
	r := p.run(t, func(string) { joined = true }, func() (*session.Result, error) { return succeeded() })
	r.wantsClose(t, event.StateFailed, 1)
	if joined {
		t.Error("the session joined")
	}
	r.wantsReason(t, string(contracts.EndCodeHarnessInvalid))
	wants(t, r.out, "qory run: qory could not read the run's hooks.\n  hooks.json is not a hooks file")
}

// nestedRepository makes a repository with a file in the checkout at dir, which the restore
// after a hook does not remove.
func nestedRepository(t *testing.T, dir string) {
	t.Helper()
	nested := filepath.Join(dir, "nested")
	if out, err := exec.Command("git", "init", "-q", nested).CombinedOutput(); err != nil {
		t.Errorf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(nested, "f"), []byte("x\n"), 0o644); err != nil {
		t.Error(err)
	}
}

// TestEndHooksRunOnlyAfterKeptWork is the run of [TestARunWithNoRepositoryStoresItsReply],
// which has no work branch, with the hook [pushHook], whose program the checkout's HEAD has.
// The stand-in hook writes the reply file in the outbox. After a failed session, which stores
// no version, no hook runs: the step that stores the outputs runs and stores nothing, and the
// outbox file has its bytes. After a session that succeeded the hook runs: a hook that exits 1
// posts its verdict and the step stores the session's reply, as a push step pushes after a red
// hook; a hook that leaves a repository in the checkout fails the restore, so no step runs,
// nothing is stored and the outbox file has its bytes, as no push follows.
func TestEndHooksRunOnlyAfterKeptWork(t *testing.T) {
	failed := func() (*session.Result, error) { return &session.Result{State: event.StateFailed, ExitCode: 7}, nil }
	red := func(*testing.T, string) int { return 1 }
	breaks := func(t *testing.T, dir string) int { nestedRepository(t, dir); return 0 }
	for name, c := range map[string]struct {
		result          func() (*session.Result, error)
		hook            func(t *testing.T, dir string) int
		state           string
		exit, ran       int
		steps           []string
		verdict, stores bool
	}{
		"failed, red hook":         {failed, red, event.StateFailed, 7, 0, []string{event.StateSucceeded}, false, false},
		"failed, restore fails":    {failed, breaks, event.StateFailed, 7, 0, []string{event.StateSucceeded}, false, false},
		"succeeded, red hook":      {succeeded, red, event.StateSucceeded, 0, 1, []string{event.StateSucceeded}, true, true},
		"succeeded, restore fails": {succeeded, breaks, event.StateFailed, 1, 1, nil, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			p, outbox := replyRepo(t)
			writeFile(t, filepath.Join(p.h.dir, "scripts", "check.sh"), "#!/bin/sh\n")
			commitAll(t, p.h.dir, "Add the check")
			t.Cleanup(cmd.SetReadHooks(func(string) ([]compose.Hook, error) { return []compose.Hook{pushHook}, nil }))
			verdicts := verdictAPI(p)
			ran := 0
			t.Cleanup(cmd.SetHookContainer(func(dir string) int {
				ran++
				writeFile(t, filepath.Join(dir, ".qory", "outbox", "reply", "reply.json"), `{"text":"Written by the hook."}`)
				return c.hook(t, dir)
			}))
			r := p.run(t, nil, c.result)
			r.wantsClose(t, c.state, c.exit)
			if ran != c.ran {
				t.Fatalf("the hook ran %d times, want %d\n%s", ran, c.ran, r.out)
			}
			if got := r.pushSteps(); !slices.Equal(got, c.steps) {
				t.Errorf("the steps that store the outputs ended %q, want %q\n%s", got, c.steps, r.out)
			}
			if c.verdict != (len(*verdicts) == 1) || len(*verdicts) > 1 {
				t.Errorf("posted %q, want a verdict: %v", *verdicts, c.verdict)
			}
			const reply = `{"text":"The order ships on Monday."}`
			posts, _, _ := p.api.counts()
			switch {
			case c.stores && posts == 1:
				if text := p.api.document(t, 1)["text"]; text != "The order ships on Monday." {
					t.Errorf("the stored text is %v, want the session's reply", text)
				}
			case c.stores || posts != 0:
				t.Errorf("%d versions stored, want one: %v\n%s", posts, c.stores, r.out)
			default:
				got, err := os.ReadFile(filepath.Join(*outbox, "reply", "reply.json"))
				if err != nil || string(got) != reply {
					t.Errorf("the outbox file is %q (%v), want %s", got, err, reply)
				}
			}
		})
	}
}

// TestARedHookPushesAndStoresTheOutput is the write run of
// [TestARedHookRunsBeforeThePushAndTheNextSession], with a work branch, whose session writes
// its pull-request output: after the red hook the run succeeds, the push moves the branch and
// the output is stored. A hook that leaves a repository in the checkout fails the run and
// stops both.
func TestARedHookPushesAndStoresTheOutput(t *testing.T) {
	for name, c := range map[string]struct {
		hook   func(t *testing.T, dir string) int
		pushes bool
		state  string
		exit   int
	}{
		"red":           {func(*testing.T, string) int { return 1 }, true, event.StateSucceeded, 0},
		"restore fails": {func(t *testing.T, dir string) int { nestedRepository(t, dir); return 0 }, false, event.StateFailed, 1},
	} {
		t.Run(name, func(t *testing.T) {
			ran := 0
			p := hookRepo(t, func(dir string) int { ran++; return c.hook(t, dir) })
			verdictAPI(p)
			r := p.run(t, func(d string) {
				writeFile(t, filepath.Join(d, "first.txt"), "first\n")
				commitAll(t, d, "Add a first file")
			}, nil)
			r.wantsClose(t, c.state, c.exit)
			if ran != 1 {
				t.Errorf("the hook ran %d times, want 1\n%s", ran, r.out)
			}
			head := p.branchHead(t)
			if pushed := head != "" && head != p.first; pushed != c.pushes {
				t.Errorf("the branch is at %q, want a push: %v\n%s", head, c.pushes, r.out)
			}
			if _, _, stored := p.api.counts(); (stored == 1) != c.pushes || stored > 1 {
				t.Errorf("%d versions stored, want one: %v\n%s", stored, c.pushes, r.out)
			}
		})
	}
}
