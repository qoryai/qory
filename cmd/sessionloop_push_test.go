package cmd_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// sharedPushRepo is a [pushRepo] whose documents set shared_run, with a harness document
// that defines a stack of the module hello of acme/site and its workflow hello, write; the
// module has the skill workflow-hello, by which Codex starts the workflow. The
// next-session request after session n gets answers[n-1], or the end once answers run out;
// requests counts the requests.
func sharedPushRepo(t *testing.T, answers ...string) (p *pushRepo, requests func() int) {
	t.Helper()
	p = newPushRepoWith(t, "write", map[string]string{
		"modules/hello/skills/workflow-hello/SKILL.md": "---\nname: workflow-hello\ndescription: Say hello.\n---\n\nSay hello.\n",
	})
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
// The checkout's setup, the image's fetch and the stack proxy are once for the run. Each
// session writes its pull-request output: session 2's version has session 2's title and the
// commits of both sessions.
func TestTheNextSessionStartsAfterThePushOfTheSessionBefore(t *testing.T) {
	p, requests := sharedPushRepo(t, laterAnswer(2, "claude", "example-provider-2", 30))
	p.noOutput = true
	joins := 0
	var beforeSecond, first, second string
	var postsBeforeSecond int
	r := p.run(t, func(d string) {
		joins++
		if joins == 1 {
			writeFile(t, filepath.Join(d, "first.txt"), "first\n")
			commitAll(t, d, "Add a first file")
			first = gitHead(t, d)
			writeFile(t, filepath.Join(d, ".qory", "outbox", "pull-request", "pull-request.json"), `{"title":"Add a first file"}`)
			return
		}
		beforeSecond = p.branchHead(t)
		postsBeforeSecond, _, _ = p.api.counts()
		writeFile(t, filepath.Join(d, "second.txt"), "second\n")
		commitAll(t, d, "Add a second file")
		second = gitHead(t, d)
		writeFile(t, filepath.Join(d, ".qory", "outbox", "pull-request", "pull-request.json"), `{"title":"Add a second file"}`)
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
	if doc := p.api.document(t, 1); doc["title"] != "Add a first file" || headOf(doc) != first {
		t.Errorf("session 1's version is %+v, want its title at %s", doc, first)
	}
	doc := p.api.document(t, 2)
	if doc["title"] != "Add a second file" || headOf(doc) != second {
		t.Errorf("session 2's version is %+v, want its title at %s", doc, second)
	}
	if got := commitsOf(doc); !slices.Equal(got, []string{first, second}) {
		t.Errorf("session 2's version has the commits %v, want %s then %s", got, first, second)
	}
	for call, want := range map[string]int{"setup": 1, "fetch image": 1, "stack proxy": 1} {
		n := 0
		for _, c := range r.calls {
			if c == call {
				n++
			}
		}
		if n != want {
			t.Errorf("the run made the call %q %d times, want %d: %q", call, n, want, r.calls)
		}
	}
}

// TestALaterSessionTurnsOnPushChecksThatSession1DidNotHave is a walled write run of two
// sessions whose home has no push check for session 1 and a check of pull-request for session
// 2. As Forager does, the run takes the check only before a join: the run sets it once,
// before session 1, session 1's push runs no check, and session 2's push runs its check and
// stores the version.
func TestALaterSessionTurnsOnPushChecksThatSession1DidNotHave(t *testing.T) {
	docker, log := checkDocker(t, 0, "")
	p, requests := sharedPushRepo(t, laterAnswer(2, "claude", "example-provider-2", 30))
	walledBy(t, docker)
	var mu sync.Mutex
	loads, joins, sets := 0, 0, 0
	t.Cleanup(cmd.SetLoadPushChecks(func(string) ([]compose.PushCheck, error) {
		mu.Lock()
		defer mu.Unlock()
		loads++
		if loads == 1 {
			return nil, nil
		}
		return []compose.PushCheck{{Type: "pull-request", Module: "hello", Command: []string{"bin/check"}}}, nil
	}))
	p.onSetCheck = func(func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
		mu.Lock()
		defer mu.Unlock()
		sets++
		if joins > 0 {
			return errors.New("Set the check before the run's first join: a join passed its checks.")
		}
		return nil
	}
	var checkedBeforeSecond string
	r := p.run(t, func(d string) {
		mu.Lock()
		joins++
		n := joins
		mu.Unlock()
		if n == 2 {
			checkedBeforeSecond = checkLog(t, log)
		}
		writeFile(t, filepath.Join(d, "s"+strconv.Itoa(n)+".txt"), "s\n")
		commitAll(t, d, "Add file "+strconv.Itoa(n))
	}, numbered())
	r.wantsClose(t, event.StateSucceeded, 0)
	if joins != 2 || requests() != 2 || sets != 1 {
		t.Fatalf("%d joins, %d next-session requests and %d sets of the check, want 2, 2 and 1\n%s", joins, requests(), sets, r.out)
	}
	if checkedBeforeSecond != "" {
		t.Errorf("session 1's push ran a check:\n%s", checkedBeforeSecond)
	}
	if !strings.Contains(checkLog(t, log), "/qory-push-check/files/pull-request.json") {
		t.Errorf("session 2's push ran no check of pull-request:\n%s", checkLog(t, log))
	}
	if posts, _, stored := p.api.counts(); posts != 2 || stored != 2 {
		t.Errorf("%d POST and %d versions, want 2 and 2\n%s", posts, stored, r.out)
	}
}

// TestASharedRunWithNoRunAPISetsNoCheck is a walled read run of two sessions from a run
// code whose gateway sends no run API address, and whose home has no push check. The fake
// fails SetRunAPICheck as Forager does for a run with no run API: the run sets no check,
// both sessions join, and the run closes succeeded.
func TestASharedRunWithNoRunAPISetsNoCheck(t *testing.T) {
	var mu sync.Mutex
	sets := 0
	r := runLoop(t, loopSetup{
		answers: []string{laterAnswer(2, "claude", "example-provider-2", 30)},
		nowhere: func(n *cmd.Nowhere) {
			n.RunAPIErr = func() error {
				return errors.New("Open the run through a gateway with a control plane to reach the run API: this run has none, so it has no QORY_RUN_API.")
			}
			n.SetRunAPICheck = func(func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
				mu.Lock()
				defer mu.Unlock()
				sets++
				return errors.New("Open the run at a gateway with a control plane to check its run API: the run has no run API.")
			}
		},
	})
	if r.err != nil || len(r.joins) != 2 || sets != 0 {
		t.Fatalf("%d joins and %d sets of the check, %v (exit %d), want 2 joins and no set\n%s", len(r.joins), sets, r.err, cmd.ExitCode(r.err), r.out)
	}
	if len(r.closes) != 1 || r.closes[0].State != event.StateSucceeded {
		t.Errorf("the run closed %+v, want once, succeeded\n%s", r.closes, r.out)
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

// TestAFailedSessionsOutputIsNotStoredByTheNextSession is a write run of two sessions whose
// session 1 writes its pull-request output and fails, and whose session 2 commits, succeeds
// and writes no output: session 2 starts with an empty outbox, and no version is stored.
func TestAFailedSessionsOutputIsNotStoredByTheNextSession(t *testing.T) {
	p, requests := sharedPushRepo(t, laterAnswer(2, "claude", "example-provider-2", 30))
	p.noOutput = true
	joins := 0
	left := true
	r := p.run(t, func(d string) {
		joins++
		outbox := filepath.Join(d, ".qory", "outbox")
		if joins == 1 {
			writeFile(t, filepath.Join(outbox, "pull-request", "pull-request.json"), `{"title":"Left by a failed session"}`)
			return
		}
		_, err := os.Lstat(outbox)
		left = err == nil
		writeFile(t, filepath.Join(d, "second.txt"), "second\n")
		commitAll(t, d, "Add a second file")
	}, func() func() (*session.Result, error) {
		n := 0
		return func() (*session.Result, error) {
			n++
			if n == 1 {
				return &session.Result{State: event.StateFailed, Session: 1}, nil
			}
			return &session.Result{State: event.StateSucceeded, Session: n}, nil
		}
	}())
	r.wantsClose(t, event.StateSucceeded, 0)
	if joins != 2 || requests() != 2 {
		t.Fatalf("%d joins and %d next-session requests, want 2 and 2\n%s", joins, requests(), r.out)
	}
	if left {
		t.Error("session 2 starts with the outbox of session 1")
	}
	if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
		t.Errorf("%d POST and %d versions, want none", posts, stored)
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

// suiteServed adds to the run API of p the build plan suitePlan beside the versions of the
// branch, and POST verdict, which returns 201, or leaves the request with no response when
// hang returns true for the verdict's number, from 1. order is each verdict that arrived,
// "verdict", and each next-session request, "next", in the order they arrived.
func suiteServed(t *testing.T, p *pushRepo, hang func(n int) bool) (order func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	note := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, s)
	}
	sum := sha256.Sum256([]byte(suitePlan))
	plan := map[string]any{"type": "build-plan", "name": "main", "newest_version": 1, "versions": []any{map[string]any{
		"id": "bp1", "number": 1, "writer": map[string]any{"kind": "run", "run_id": "earlier"},
		"files": []any{map[string]any{"filename": "build-plan.md", "size": len(suitePlan), "sha256": hex.EncodeToString(sum[:])}},
	}}}
	p.api.index = func(_ int, w http.ResponseWriter) bool {
		rec := httptest.NewRecorder()
		p.api.writeIndex(rec)
		var index map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &index); err != nil {
			t.Error(err)
			return false
		}
		index["artifacts"] = append(index["artifacts"].([]any), plan)
		json.NewEncoder(w).Encode(index)
		return true
	}
	verdicts := 0
	p.api.other = func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files/build-plan.md"):
			io.WriteString(w, suitePlan)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/verdict"):
			note("verdict")
			verdicts++
			if hang != nil && hang(verdicts) {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return true
				}
				t.Cleanup(func() { conn.Close() })
				return true
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"version":1,"artifact":{"type":"verdict","name":"main","version":{"id":"v1","number":1}}}`)
		default:
			return false
		}
		return true
	}
	next := p.nextSession
	p.nextSession = func(ctx context.Context, body []byte) ([]byte, error) {
		note("next")
		return next(ctx, body)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// suiteResults is the result of each join of a run, numbered by the gateway from 1, whose
// session that has an after step runs it with a red suite, as Forager's after step does.
// spec is the spec of the session that joins, which [pushRepo.mountsOf] sets.
func suiteResults(t *testing.T, spec *session.SessionSpec) func() (*session.Result, error) {
	n := numbered()
	return func() (*session.Result, error) {
		res, err := n()
		if len(spec.After) > 0 {
			res.After = runAfter(t, *spec, suiteEnd{result: suiteRed, exit: 1})
		}
		return res, err
	}
}

// fullSuiteAnswer is laterAnswer with full_suite true in type_settings.
func fullSuiteAnswer(n int, minutes int) string {
	return strings.Replace(laterAnswer(n, "claude", "example-provider-2", minutes), `"full_suite":false`, `"full_suite":true`, 1)
}

// TestASessionsSuiteIsPostedBeforeTheNextSessionRequest is a write run whose answer starts
// session 2 with full_suite: session 1 runs no suite, session 2 runs the full suite in its
// after step, and qory posts the suite's verdict before the next-session request after
// session 2. The run closes succeeded.
func TestASessionsSuiteIsPostedBeforeTheNextSessionRequest(t *testing.T) {
	p, requests := sharedPushRepo(t, fullSuiteAnswer(2, 30))
	order := suiteServed(t, p, nil)
	var spec session.SessionSpec
	var afters []bool
	p.mountsOf = func(s session.SessionSpec) {
		spec = s
		afters = append(afters, len(s.After) > 0)
	}
	joins := 0
	r := p.run(t, func(d string) {
		joins++
		writeFile(t, filepath.Join(d, "file-"+strconv.Itoa(joins)+".txt"), "text\n")
		commitAll(t, d, "Add file "+strconv.Itoa(joins))
	}, suiteResults(t, &spec))
	r.wantsClose(t, event.StateSucceeded, 0)
	if joins != 2 || requests() != 2 {
		t.Fatalf("%d joins and %d next-session requests, want 2 and 2\n%s", joins, requests(), r.out)
	}
	if !slices.Equal(afters, []bool{false, true}) {
		t.Errorf("the sessions have an after step: %v, want only session 2", afters)
	}
	if got, want := order(), []string{"next", "verdict", "next"}; !slices.Equal(got, want) {
		t.Errorf("the requests arrived in the order %q, want %q", got, want)
	}
}

// TestEachSessionEndsAtItsTimeLimit is a write run whose documents set 30 minutes and whose
// answer starts session 2 with 60, where a minute lasts 100ms: session 1 joins with 3s and
// session 2 with 6s. The verdict of session 2's suite, or the version of its push, then
// gets no response, and the step that waits for it ends at session 2's limit, 6s after
// session 2's start, and not at once, as it would at session 1's limit.
func TestEachSessionEndsAtItsTimeLimit(t *testing.T) {
	defer cmd.SetSessionMinute(100 * time.Millisecond)()
	defer cmd.SetPushTimes(200*time.Millisecond, 400*time.Millisecond)()
	for name, hangPush := range map[string]bool{"suite": false, "push": true} {
		t.Run(name, func(t *testing.T) {
			p, _ := sharedPushRepo(t, fullSuiteAnswer(2, 60))
			p.change = func(change func(map[string]any)) func(map[string]any) {
				return func(d map[string]any) {
					change(d)
					d["time_limit_minutes"] = 30
				}
			}(p.change)
			suiteServed(t, p, func(int) bool { return !hangPush })
			if hangPush {
				hangingPost(t, p, nil)
				p.api.post = func(post func(int, http.ResponseWriter) (bool, bool)) func(int, http.ResponseWriter) (bool, bool) {
					return func(n int, w http.ResponseWriter) (bool, bool) {
						if n == 1 {
							return false, true
						}
						return post(n, w)
					}
				}(p.api.post)
			}
			var spec session.SessionSpec
			var limits []time.Duration
			var second time.Time
			p.mountsOf = func(s session.SessionSpec) {
				spec = s
				limits = append(limits, s.Timeout)
				second = time.Now()
			}
			joins := 0
			r := p.run(t, func(d string) {
				joins++
				writeFile(t, filepath.Join(d, "file-"+strconv.Itoa(joins)+".txt"), "text\n")
				commitAll(t, d, "Add file "+strconv.Itoa(joins))
			}, suiteResults(t, &spec))
			took := time.Since(second)
			if joins != 2 || len(limits) != 2 {
				t.Fatalf("%d joins, want 2\n%s", joins, r.out)
			}
			if limits[0] <= 2500*time.Millisecond || limits[0] > 3*time.Second || limits[1] <= 5500*time.Millisecond || limits[1] > 6*time.Second {
				t.Errorf("the sessions join with %v, want 3s and 6s", limits)
			}
			if took < 5*time.Second {
				t.Errorf("the run ended %v after session 2's start, want at its limit of 6s\n%s", took, r.out)
			}
			if len(r.closes) != 1 || r.closes[0].State == event.StateSucceeded {
				t.Errorf("the run closed with %+v, want an end at the limit", r.closes)
			}
		})
	}
}

// TestACodexInstallThatFailsInAWriteRunKeepsThePushOfSession1 is a write run of claude whose
// answer starts session 2 on codex, whose install fails: the run's host proxy returns 403 to
// the request to npm. Session 1's commit stays on the branch with its version stored, the run
// closes failed, exit 1, and session 2 does not join.
func TestACodexInstallThatFailsInAWriteRunKeepsThePushOfSession1(t *testing.T) {
	defer cmd.SetNpmRegistry("http://127.0.0.1:9")()
	p, requests := sharedPushRepo(t, laterAnswer(2, "codex", "example-provider-2", 30))
	joins := 0
	r := p.run(t, func(d string) {
		joins++
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
		// Session 1 installs nothing, so that only the install of codex reaches npm.
		t.Cleanup(cmd.SetInstallRuntimes(true))
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
	wants(t, r.out, "qory run: session 2 starts the workflow hello on codex\n", "qory run: composed 2 entries from 1 module for codex\n",
		"qory run: the dist-tags of @openai/codex returned 403; the code is used\n")
	lacks(t, r.out, "installed codex")
}
