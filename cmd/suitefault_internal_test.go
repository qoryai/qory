package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
)

// launchFaultRun opens the suite of a walled write run with full_suite true, whose build plan is
// plan ("" for none) and whose folder has a .git folder unless noGit, and returns the run.
func launchFaultRun(t *testing.T, h helperRun, plan string, noGit bool, inputs string) {
	t.Helper()
	dir := workspaceRepo(t)
	if noGit {
		if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
	}
	if plan != "" {
		h.api.withPlan(plan)
	}
	spec := &runSpec{}
	spec.Dir = dir
	r := openSuites(context.Background(), suiteSetup{
		run: apiRun{api: h.api, dir: h.runDir}, spec: spec, runID: "r1", walled: true, fullSuite: true,
		inputs: []byte(inputs), said: func(string) {},
	})
	if r == nil || r.fault == nil || len(spec.After) != 0 {
		t.Fatalf("suite %v, After %q, want a fault and no After", r != nil, spec.After)
	}
	r.said = func(string) {}
	*h.suiteRun = *r
}

const planNoFull = "# Plan\n\n## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n| fast | `a` | none | s |\n"

// TestACauseInTheBuildPlanPostsChangesRequestedWithTheFix: a plan with no full row, no table, no
// command, or no plan at all stops the suite at the launch. After the session, qory posts
// changes-requested with the point "The full suite did not start: <cause>." and the fix in `fix`.
func TestACauseInTheBuildPlanPostsChangesRequestedWithTheFix(t *testing.T) {
	for _, c := range []struct {
		name, plan, text, fix string
	}{
		{"no full row", planNoFull, textFaultNoFull, textFixNoFull},
		{"no table", "# Plan\n", textFaultNoTable, textFixNoTable},
		{"no command", "## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n| full | none | none | s |\n", textFaultNoCmd, textFixNoCmd},
		{"no plan", "", textFaultNoPlan, textFixNoPlan},
		{"a plan over 1 MiB", "## Commands\n" + strings.Repeat("x", maxBuildPlan), textFaultTooBig, textFixTooBig},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			launchFaultRun(t, h, c.plan, false, "")
			got, err := h.after(nil, succeeded)
			if err != nil || got != succeeded {
				t.Errorf("outcome %+v, error %v, want the session's outcome", got, err)
			}
			want := `{"verdict":"changes-requested","points":[{"text":"` + c.text + `","action":"run_again","fix":"` + c.fix + `"}]}`
			if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
				t.Errorf("posted %q, want %s", posted, want)
			}
		})
	}
}

// TestACauseOutsideTheAgentsReachPostsEscalate: a session folder with no .git folder, an
// unreadable build plan and a start commit that cannot be read post escalate with the point.
func TestACauseOutsideTheAgentsReachPostsEscalate(t *testing.T) {
	h := newHelper(t)
	launchFaultRun(t, h, buildPlanText, true, "")
	if _, err := h.after(nil, succeeded); err != nil {
		t.Fatal(err)
	}
	want := `{"verdict":"escalate","points":[{"text":"` + textFaultNoGit + `","action":"run_again"}]}`
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("no .git: posted %q, want %s", posted, want)
	}
	for name, err := range map[string]error{
		"an unreadable plan": errors.Join(errNoBuildPlan, errors.New("the index failed")),
		"a bad start commit": errors.New("x"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHelper(t)
			h.fault = faultOf(err)
			if h.fault.fixable {
				t.Fatalf("the fault %+v is fixable", h.fault)
			}
			if _, err := h.after(nil, succeeded); err != nil {
				t.Fatal(err)
			}
			if posted := h.api.verdicts(); len(posted) != 1 || !strings.HasPrefix(posted[0], `{"verdict":"escalate","points":[{"text":"The full suite did not start: qory could not read`) {
				t.Errorf("posted %q", posted)
			}
		})
	}
}

// TestASessionThatDidNotSucceedPostsNothingForALaunchCause: nothing is posted, and the run keeps
// the session's outcome.
func TestASessionThatDidNotSucceedPostsNothingForALaunchCause(t *testing.T) {
	h := newHelper(t)
	launchFaultRun(t, h, planNoFull, false, "")
	failed := session.Outcome{State: event.StateFailed, ExitCode: 3}
	if got, err := h.after(nil, failed); err != nil || got != failed || len(h.api.verdicts()) != 0 {
		t.Errorf("outcome %+v, error %v, posted %q", got, err, h.api.verdicts())
	}
}

// TestACauseOverAChangesRequestedOfTheAgent: a cause that the agent can fix keeps changes-requested
// with the agent's points and the point; one it cannot fix posts escalate with the agent's points,
// the point and the agent's track; 20 points post nothing.
func TestACauseOverAChangesRequestedOfTheAgent(t *testing.T) {
	const track = `,"track":{"id":"small","reason":"One file."}`
	point := func(i int) string { return `{"text":"Point ` + string(rune('0'+i)) + `.","action":"run_again"}` }
	agent := `{"verdict":"changes-requested","points":[` + point(1) + `]` + track + `}`
	twenty := make([]string, 20)
	for i := range twenty {
		twenty[i] = `{"text":"P","action":"run_again"}`
	}
	fixable := `{"text":"` + textFaultNoFull + `","action":"run_again","fix":"` + textFixNoFull + `"}`
	unfixable := `{"text":"` + textFaultNoGit + `","action":"run_again"}`
	for _, c := range []struct {
		name, agent, want string
		unfixable         bool
		points            int
	}{
		{"fixable", agent, `{"verdict":"changes-requested","points":[` + point(1) + `,` + fixable + `]` + track + `}`, false, 0},
		{"unfixable", agent, `{"verdict":"escalate","points":[` + point(1) + `,` + unfixable + `]` + track + `}`, true, 0},
		{"fixable with 20 points", `{"verdict":"changes-requested","points":[` + strings.Join(twenty, ",") + `]}`, "", false, 20},
		{"unfixable with 20 points", `{"verdict":"changes-requested","points":[` + strings.Join(twenty, ",") + `]}`, "", true, 20},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: c.agent})
			h.fault = faultOf(errNoBuildPlan)
			h.fault.point.Text, h.fault.point.Fix = textFaultNoFull, textFixNoFull
			if c.unfixable {
				h.fault = noGitFault()
			}
			if got, err := h.after(nil, succeeded); err != nil || got != succeeded {
				t.Errorf("outcome %+v, error %v", got, err)
			}
			posted := h.api.verdicts()
			switch {
			case c.want == "" && len(posted) != 0:
				t.Errorf("posted %q, want nothing", posted)
			case c.want != "" && (len(posted) != 1 || posted[0] != c.want):
				t.Errorf("posted %q, want %s", posted, c.want)
			}
		})
	}
}

// TestASessionWhoseAfterStepDidNotRunPostsEscalate: the session succeeded and Result.After is
// nil: qory posts escalate with "The full suite did not run: ...", also over an escalate of the
// agent, where it adds the point.
func TestASessionWhoseAfterStepDidNotRunPostsEscalate(t *testing.T) {
	for _, c := range []struct{ name, agent, want string }{
		{"no verdict", "", `{"verdict":"escalate","points":[{"text":"The full suite did not run: the run closed before the after step.","action":"run_again"}]}`},
		{"a passed verdict", `{"verdict":"passed"}`, `{"verdict":"escalate","points":[{"text":"The full suite did not run: the run closed before the after step.","action":"run_again"}]}`},
		{"an escalate", `{"verdict":"escalate","points":[{"text":"Pick.","action":"answer","question":"q1"}]}`, `{"verdict":"escalate","points":[{"text":"Pick.","action":"answer","question":"q1"},{"text":"The full suite did not run: the run closed before the after step.","action":"run_again"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			if c.agent != "" {
				h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: c.agent})
			}
			if got, err := h.after(nil, succeeded); err != nil || got != succeeded {
				t.Errorf("outcome %+v, error %v", got, err)
			}
			if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != c.want {
				t.Errorf("posted %q, want %s", posted, c.want)
			}
		})
	}
}

// TestALaunchCauseWithAResultFileIsAFindingAndNoVerdict: with review.suite_result, a cause at the launch
// and an after step that did not run write state did_not_run with the finding, and post nothing.
func TestALaunchCauseWithAResultFileIsAFindingAndNoVerdict(t *testing.T) {
	file := filepath.Join(t.TempDir(), "suite.json")
	inputs := `{"review":{"suite_result":"` + file + `"}}`
	h := newHelper(t)
	launchFaultRun(t, h, planNoFull, false, inputs)
	if got, err := h.after(nil, succeeded); err != nil || got != succeeded {
		t.Fatalf("outcome %+v, error %v", got, err)
	}
	if posted := h.api.verdicts(); len(posted) != 0 {
		t.Errorf("posted %q", posted)
	}
	res := readResult(t, file)
	finding, _ := res["finding"].(map[string]any)
	if res["state"] != "did_not_run" || finding["text"] != textFaultNoFull || finding["fix"] != textFixNoFull {
		t.Errorf("the file has %v", res)
	}
}

// TestAPlanThatCannotBeReadIsOutsideTheAgentsReach: a plan that the run API cannot send is
// wrapped with errNoBuildPlan, not fixable, and posts escalate.
func TestAPlanThatCannotBeReadIsOutsideTheAgentsReach(t *testing.T) {
	h := newHelper(t)
	h.api.failFiles = true
	launchFaultRun(t, h, buildPlanText, false, "")
	if h.fault.fixable {
		t.Fatalf("the fault %+v is fixable", h.fault)
	}
	if _, err := h.after(nil, succeeded); err != nil {
		t.Fatal(err)
	}
	want := `{"verdict":"escalate","points":[{"text":"` + textFaultPlanRead + `","action":"run_again"}]}`
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestAPassedVerdictKeepsItsTrackUnderACauseInTheBuildPlan.
func TestAPassedVerdictKeepsItsTrackUnderACauseInTheBuildPlan(t *testing.T) {
	const track = `"track":{"id":"small","reason":"One file."}`
	h := newHelper(t)
	h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: `{"verdict":"passed",` + track + `}`})
	launchFaultRun(t, h, planNoFull, false, "")
	if _, err := h.after(nil, succeeded); err != nil {
		t.Fatal(err)
	}
	want := `{"verdict":"changes-requested","points":[{"text":"` + textFaultNoFull + `","action":"run_again","fix":"` + textFixNoFull + `"}],` + track + `}`
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}
