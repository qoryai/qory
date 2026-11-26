package cmd_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
)

// runAPICheck is the check that a run set for the POSTs from behind the wall.
type runAPICheck = func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)

// verdictProbe keeps the check of a run and what it returned for the POSTs that
// [verdictProbe.try] sent, in the order of the calls.
type verdictProbe struct {
	mu    sync.Mutex
	check runAPICheck
	tries []verdictTry
}

// verdictTry is the refusals of one call of [verdictProbe.try], by the name of the POST.
type verdictTry map[string]*session.RunAPIRefusal

// setCheck is the onSetCheck of a run: it keeps the check, and sends the POSTs of a setup
// command through it.
func (v *verdictProbe) setCheck(c runAPICheck) error {
	v.mu.Lock()
	v.check = c
	v.mu.Unlock()
	v.try()
	return nil
}

// try sends one POST of each kind that the verdict rule reads, and the POSTs that it does
// not, through the run's check, and keeps the refusals.
func (v *verdictProbe) try() verdictTry {
	v.mu.Lock()
	defer v.mu.Unlock()
	got := verdictTry{}
	if v.check == nil {
		v.tries = append(v.tries, got)
		return got
	}
	push := func(artifactType, verdict string) []byte {
		return []byte(`{"type":"` + artifactType + `","name":"main","files":[{"filename":"v.json","content_base64":"` +
			base64.StdEncoding.EncodeToString([]byte(`{"verdict":"`+verdict+`"}`)) + `"}]}`)
	}
	posts := map[string]session.RunAPIRequest{
		"post passed":            {Path: "/verdict", Body: []byte(`{"verdict":"passed"}`)},
		"post changes-requested": {Path: "/verdict", Body: []byte(`{"verdict":"changes-requested"}`)},
		"post escalate":          {Path: "/verdict", Body: []byte(`{"verdict":"escalate"}`)},
		"push passed":            {Path: "/artifacts", Body: push("verdict", "passed")},
		"push changes-requested": {Path: "/artifacts", Body: push("verdict", "changes-requested")},
		"push escalate":          {Path: "/artifacts", Body: push("verdict", "escalate")},
		"other push":             {Path: "/artifacts", Body: push("note", "passed")},
		"pushed-commits":         {Path: "/pushed-commits", Body: []byte(`{}`)},
		"next-session":           {Path: "/next-session", Body: []byte(`{}`)},
	}
	for name, req := range posts {
		refusal, err := v.check(context.Background(), req)
		if err != nil {
			refusal = &session.RunAPIRefusal{Message: "error: " + err.Error()}
		}
		got[name] = refusal
	}
	v.tries = append(v.tries, got)
	return got
}

// The three states of the verdict rule that a try shows.
const (
	// allRefused is no session running: each verdict is stopped, escalate too.
	allRefused = "every verdict is refused"
	// limited is a session that does not judge: escalate passes, the other verdicts do not.
	limited = "a session that does not judge"
	// free is a session that judges: every POST passes.
	free = "no POST is refused"
)

// wantTry checks that a try has POSTs, since a run that set no check has none, then the
// refusals of the try against the state, and that every refusal is the 422
// push_check_failed of the push checks with the fix of the verdict rule.
func wantTry(t *testing.T, when string, got verdictTry, state string) {
	t.Helper()
	if len(got) == 0 {
		t.Errorf("%s: no try, the run set no check", when)
		return
	}
	for name, refusal := range got {
		verdict := strings.HasPrefix(name, "post ") || strings.HasPrefix(name, "push ")
		refused := false
		switch state {
		case allRefused:
			refused = verdict
		case limited:
			refused = verdict && !strings.HasSuffix(name, "escalate")
		}
		if refused != (refusal != nil) {
			t.Errorf("%s: %s: refusal %+v, refused want %t (%s)", when, name, refusal, refused, state)
			continue
		}
		if refusal == nil {
			continue
		}
		first, _, _ := strings.Cut(refusal.Message, "\n")
		fix := "Record escalate or no verdict: the next workflow in the loop judges this work."
		if state == allRefused {
			fix = "Post the verdict from a session of the run."
		}
		if refusal.Status != http.StatusUnprocessableEntity || refusal.Code != "push_check_failed" || first != fix {
			t.Errorf("%s: %s: refusal %d %s %q", when, name, refusal.Status, refusal.Code, refusal.Message)
		}
	}
}

// loopWorkflows is the workflows table of the stack of [loopRepo], with hello that
// writes and review-changes that reads and has a review group. judges is the line that
// sets judges in hello's entry, "" for none: a run's harness document cannot set it, so
// the stack that the document extends does.
func loopWorkflows(judges string) string {
	return "workflows:\n  hello:\n    access: write\n    plan: build-plan\n" + judges +
		"  review-changes:\n    access: read\n    review_group: reviewers\n"
}

// doesNotJudge is the line of hello's entry that sets judges false.
const doesNotJudge = "    judges: false\n"

// loopRepo is a [sharedPushRepoWith] whose harness document extends the stack loop/ of the
// repository: the module hello of [reviewFiles] and the workflows table.
func loopRepo(t *testing.T, workflows string, answers ...string) *pushRepo {
	t.Helper()
	files := map[string]string{
		"loop/qory-stack.yaml": "apiVersion: qory.dev/v1alpha1\nname: loop\nextending:\n  instructions: true\n" +
			"modules:\n  - name: hello\n    source: {path: modules/hello}\n" + workflows,
		"loop/modules/hello/process.yaml":         pullRequestProcess,
		"loop/modules/hello/" + helloScopeProgram: "#!/bin/sh\n",
		"loop/modules/hello/commands/hello.md":    helloFiles(t)["examples/hello/modules/hello/commands/hello.md"],
	}
	for name, content := range reviewFiles {
		files["loop/"+name] = content
	}
	p, _ := sharedPushRepoWith(t, files, nil, answers...)
	change := p.change
	p.change = func(d map[string]any) {
		change(d)
		d["harness"] = map[string]any{"harness": map[string]any{
			"extends": map[string]any{"git": p.h.server.URL("acme/site"), "ref": p.first, "path": "loop"}}}
	}
	return p
}

// TestTheVerdictRuleFollowsTheSessionThatRuns is a run of hello with judges false, a
// review session, and hello again: the verdicts of each state, from the setup to the end
// of the run, with the check that the run set.
func TestTheVerdictRuleFollowsTheSessionThatRuns(t *testing.T) {
	var v verdictProbe
	var mu sync.Mutex
	var between, inside []verdictTry
	r, _, _ := reviewRunWith(t, reviewCfg{
		stack:      loopWorkflows(doesNotJudge),
		onSetCheck: v.setCheck,
		between: func(int) {
			got := v.try()
			mu.Lock()
			between = append(between, got)
			mu.Unlock()
		},
		next: func(*pushRepo, int) {
			got := v.try()
			mu.Lock()
			inside = append(inside, got)
			mu.Unlock()
		},
	}, answerFor(2, "review-changes"), answerFor(3, "hello"))
	r.wantsClose(t, event.StateSucceeded, 0)
	mu.Lock()
	defer mu.Unlock()
	if len(v.tries) < 1 || len(inside) != 3 || len(between) != 3 {
		t.Fatalf("tries: %d at the setup, %d in sessions, %d between", len(v.tries), len(inside), len(between))
	}
	wantTry(t, "setup", v.tries[0], allRefused)
	wantTry(t, "session 1, hello, judges false", inside[0], limited)
	wantTry(t, "between sessions 1 and 2", between[0], allRefused)
	wantTry(t, "session 2, review-changes", inside[1], free)
	wantTry(t, "between sessions 2 and 3", between[1], allRefused)
	wantTry(t, "session 3, hello, judges false again", inside[2], limited)
	wantTry(t, "after the last session", between[2], allRefused)
	// After the run, the join of the last session returned: nothing is left over.
	wantTry(t, "after the run", v.try(), allRefused)
}

// TestTheVerdictStateIsClearedWhenAJoinReturnsAnError is a join that returns an error
// after the agent ran: the next POST finds no running session.
func TestTheVerdictStateIsClearedWhenAJoinReturnsAnError(t *testing.T) {
	var v verdictProbe
	var inside verdictTry
	p := loopRepo(t, loopWorkflows(doesNotJudge))
	p.onSetCheck = v.setCheck
	r := p.run(t, func(string) { inside = v.try() }, func() (*session.Result, error) {
		return nil, errors.New("the join failed")
	})
	_ = r
	wantTry(t, "the session that failed", inside, limited)
	wantTry(t, "after the join", v.try(), allRefused)
}

// TestARunOfOneSessionThatDoesNotJudgeIsLimited is a run that is no shared run, whose
// workflow has judges false: the run sets the check, with no push check.
func TestARunOfOneSessionThatDoesNotJudgeIsLimited(t *testing.T) {
	var v verdictProbe
	var inside verdictTry
	p := loopRepo(t, loopWorkflows(doesNotJudge))
	change := p.change
	p.change = func(d map[string]any) {
		change(d)
		d["shared_run"] = false
	}
	p.onSetCheck = v.setCheck
	r := p.run(t, func(string) { inside = v.try() }, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	wantTry(t, "setup", v.tries[0], allRefused)
	wantTry(t, "the session", inside, limited)
}

// TestARunWhoseWorkflowsAllJudgeIsNotLimited is a run of hello with no judges member,
// which judges: a run of one session sets no check, and a shared run sets one that passes
// every POST in a session.
func TestARunWhoseWorkflowsAllJudgeIsNotLimited(t *testing.T) {
	t.Run("one session", func(t *testing.T) {
		var v verdictProbe
		p, _ := sharedPushRepoWith(t, nil, nil)
		change := p.change
		p.change = func(d map[string]any) {
			change(d)
			d["shared_run"] = false
		}
		p.onSetCheck = v.setCheck
		r := p.run(t, nil, nil)
		r.wantsClose(t, event.StateSucceeded, 0)
		if v.check != nil {
			t.Error("the run set a check, want none")
		}
	})
	t.Run("a shared run", func(t *testing.T) {
		var v verdictProbe
		var inside verdictTry
		r, _, _ := reviewRunWith(t, reviewCfg{
			onSetCheck: v.setCheck,
			next: func(_ *pushRepo, n int) {
				if n == 1 {
					inside = v.try()
				}
			},
		})
		r.wantsClose(t, event.StateSucceeded, 0)
		wantTry(t, "a session that judges", inside, free)
	})
}
