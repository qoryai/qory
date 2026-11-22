package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
)

// launchFaultRun opens the suite of a walled write run with fullSuite true, whose build plan is
// plan ("" for none) and whose folder has a .git folder unless noGit, and returns the run.
func launchFaultRun(t *testing.T, h helperRun, plan string, noGit bool) {
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
		workflow: "fix", plan: "build-plan",
		run: apiRun{api: h.api, dir: h.runDir}, spec: spec, runID: "r1", walled: true, fullSuite: true,
		said: func(string) {},
	})
	if r == nil || r.fault == nil || len(spec.After) != 0 {
		t.Fatalf("suite %v, After %q, want a fault and no After", r != nil, spec.After)
	}
	r.said = func(string) {}
	*h.suiteRun = *r
}

// noFullText and noFullFix are the point of a plan of the type build-plan with no full command.
var (
	noFullText = fmt.Sprintf(textFaultNoFull, "build-plan")
	noFullFix  = fmt.Sprintf(textFixNoFull, "build-plan")
)

const planNoFull = "# Plan\n\n## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n| fast | `a` | none | s |\n"

// TestACauseInThePlanPostsChangesRequestedWithTheFix: a plan with no full row, no table, no
// command, or no plan at all stops the suite at the launch. After the session, qory posts
// changes-requested with the point "The full suite did not start: <cause>." and the fix in `fix`.
func TestACauseInThePlanPostsChangesRequestedWithTheFix(t *testing.T) {
	for _, c := range []struct {
		name, plan, text, fix string
	}{
		{"no full row", planNoFull, noFullText, noFullFix},
		{"no table", "# Plan\n", noFullText, textFixNoTable},
		{"no command", "## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n| full | none | none | s |\n", fmt.Sprintf(textFaultNoCmd, "build-plan"), textFixNoCmd},
		{"no plan", "", fmt.Sprintf(textFaultNoPlan, "build-plan"), fmt.Sprintf(textFixNoPlan, "build-plan")},
		{"a plan over 1 MiB", "## Commands\n" + strings.Repeat("x", maxPlan), fmt.Sprintf(textFaultTooBig, "build-plan"), fmt.Sprintf(textFixTooBig, "build-plan")},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			launchFaultRun(t, h, c.plan, false)
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
// unreadable plan and a start commit that cannot be read post escalate with the point.
func TestACauseOutsideTheAgentsReachPostsEscalate(t *testing.T) {
	h := newHelper(t)
	launchFaultRun(t, h, buildPlanText, true)
	if _, err := h.after(nil, succeeded); err != nil {
		t.Fatal(err)
	}
	want := `{"verdict":"escalate","points":[{"text":"` + textFaultNoGit + `","action":"run_again"}]}`
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("no .git: posted %q, want %s", posted, want)
	}
	for name, err := range map[string]error{
		"an unreadable plan": errors.Join(errNoPlan, errors.New("the index failed")),
		"a bad start commit": errors.New("x"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHelper(t)
			h.fault = faultOf(err, "build-plan")
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
	launchFaultRun(t, h, planNoFull, false)
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
	fixable := `{"text":"` + noFullText + `","action":"run_again","fix":"` + noFullFix + `"}`
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
			h.fault = faultOf(errNoPlan, "build-plan")
			h.fault.point.Text, h.fault.point.Fix = noFullText, noFullFix
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

// TestAPlanThatCannotBeReadIsOutsideTheAgentsReach: a plan that the run API cannot send is
// wrapped with errNoPlan, not fixable, and posts escalate.
func TestAPlanThatCannotBeReadIsOutsideTheAgentsReach(t *testing.T) {
	h := newHelper(t)
	h.api.failFiles = true
	launchFaultRun(t, h, buildPlanText, false)
	if h.fault.fixable {
		t.Fatalf("the fault %+v is fixable", h.fault)
	}
	if _, err := h.after(nil, succeeded); err != nil {
		t.Fatal(err)
	}
	want := `{"verdict":"escalate","points":[{"text":"` + fmt.Sprintf(textFaultPlanRead, "build-plan") + `","action":"run_again"}]}`
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestAPassedVerdictKeepsItsTrackUnderACauseInThePlan.
func TestAPassedVerdictKeepsItsTrackUnderACauseInThePlan(t *testing.T) {
	const track = `"track":{"id":"small","reason":"One file."}`
	h := newHelper(t)
	h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: `{"verdict":"passed",` + track + `}`})
	launchFaultRun(t, h, planNoFull, false)
	if _, err := h.after(nil, succeeded); err != nil {
		t.Fatal(err)
	}
	want := `{"verdict":"changes-requested","points":[{"text":"` + noFullText + `","action":"run_again","fix":"` + noFullFix + `"}],` + track + `}`
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// flakyRun is an apiRun whose RunAPI fails while down is true.
type flakyRun struct {
	apiRun
	down *bool
}

func (r flakyRun) RunAPI() (*http.Client, string, error) {
	if *r.down {
		return nil, "", errors.New("the run API is closed")
	}
	return r.apiRun.RunAPI()
}

// TestARunAPIThatFailsAtTheLaunchIsACauseOutsideTheAgentsReach: openSuites keeps the cause and prints
// its line. When the run API answers after the session, qory posts escalate with the point; when it
// still fails, the run closes failed with exit status 1, as a failed post does.
func TestARunAPIThatFailsAtTheLaunchIsACauseOutsideTheAgentsReach(t *testing.T) {
	for _, c := range []struct {
		name      string
		stillDown bool
	}{{"the post goes through", false}, {"the post fails", true}} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			down := true
			var said []string
			spec := &runSpec{}
			spec.Dir = workspaceRepo(t)
			r := openSuites(context.Background(), suiteSetup{
				workflow: "fix", plan: "build-plan",
				run: flakyRun{apiRun{api: h.api, dir: h.runDir}, &down}, spec: spec, runID: "r1", walled: true, fullSuite: true,
				said: func(l string) { said = append(said, l) },
			})
			if r == nil || r.fault == nil || r.fault.fixable || len(spec.After) != 0 || len(said) != 1 || said[0] != textFaultNoRunAPI {
				t.Fatalf("suite %v, After %q, lines %q", r != nil, spec.After, said)
			}
			down = c.stillDown
			r.said = func(l string) { said = append(said, l) }
			got, err := r.after(context.Background(), context.Background(), time.Time{}, nil, succeeded)
			if c.stillDown {
				if got.State != event.StateFailed || got.ExitCode != 1 || ExitCode(err) != 1 || len(h.api.verdicts()) != 0 {
					t.Errorf("outcome %+v, error %v, posted %q", got, err, h.api.verdicts())
				}
				if len(said) < 2 || !strings.HasPrefix(said[len(said)-1], "Take the work item in again") {
					t.Errorf("lines %q", said)
				}
				return
			}
			want := `{"verdict":"escalate","points":[{"text":"` + textFaultNoRunAPI + `","action":"run_again"}]}`
			if err != nil || got != succeeded || len(h.api.verdicts()) != 1 || h.api.verdicts()[0] != want {
				t.Errorf("outcome %+v, error %v, posted %q", got, err, h.api.verdicts())
			}
		})
	}
}

// TestTheCapBeforeTheCommandIsNotStartedAndTheCapAfterItIsRed: a step cut at the cap with no `ran` status
// file never reached the command: qory posts escalate with "The full suite did not start: ..." and, over
// a changes-requested of the agent, escalate with the agent's points. A step cut at the cap after the
// command started posts changes-requested with "did not finish".
func TestTheCapBeforeTheCommandIsNotStartedAndTheCapAfterItIsRed(t *testing.T) {
	cut := session.AfterResult{Step: 2, State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: 143}
	const notStarted = `{"text":"The full suite did not start: the after step used its 7 minutes before the command.","action":"run_again"}`
	const red = `{"text":"The full suite did not finish: the after step reached its limit of 7 minutes.","action":"run_again"}`
	for _, c := range []struct {
		name, agent, want string
		ran               bool
	}{
		{"before the command", "", `{"verdict":"escalate","points":[` + notStarted + `]}`, false},
		{"before the command, over a changes-requested", `{"verdict":"changes-requested","points":[{"text":"x","action":"run_again"}]}`,
			`{"verdict":"escalate","points":[{"text":"x","action":"run_again"},` + notStarted + `]}`, false},
		{"after the command started", "", `{"verdict":"changes-requested","points":[` + red + `]}`, true},
		{"after the command started, over a changes-requested", `{"verdict":"changes-requested","points":[{"text":"x","action":"run_again"}]}`, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			if c.agent != "" {
				h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: c.agent})
			}
			if c.ran {
				h.state(t, "ran "+suiteSHA, "")
			}
			writeEvents(t, h.runDir, afterStarted(3, 2), afterExited(4, 2))
			end := cut
			if got, err := h.after(&end, succeeded); err != nil || got != succeeded {
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

// TestAnUnreadableStatusFileUnderTheCapIsNotStarted: a status file that qory cannot read, here a
// folder in its place, is no `ran` record, so a step cut at the cap fails closed as a suite that
// did not start, with escalate.
func TestAnUnreadableStatusFileUnderTheCapIsNotStarted(t *testing.T) {
	h := newHelper(t)
	if err := os.MkdirAll(filepath.Join(h.workspace, ".git/qory-suite/status"), 0o755); err != nil {
		t.Fatal(err)
	}
	cut := session.AfterResult{Step: 2, State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: 143}
	if got, err := h.after(&cut, succeeded); err != nil || got != succeeded {
		t.Fatalf("outcome %+v, error %v", got, err)
	}
	want := `{"verdict":"escalate","points":[{"text":"The full suite did not start: the after step used its 7 minutes before the command.","action":"run_again"}]}`
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestTheFullSuiteRefusalIsCauseFirstAsPrinted: the refusal of full_suite on a write-no-push
// workflow, as [ui.UI.Fail] prints it, has the cause as line 1 and, as line 2, the line that has
// the workflow's name and ends with the code used. Line 1 has no name, so with its mark it has 71
// characters for session 1 ("The run documents ask") and 78 for a later session ("The
// next-session answer asks"), for a workflow of 1, 24 and 64 characters. Line 2 says "The run did
// not start" for session 1 and "The session did not start" for a later session.
func TestTheFullSuiteRefusalIsCauseFirstAsPrinted(t *testing.T) {
	for _, name := range []string{"a", strings.Repeat("w", 24), strings.Repeat("w", 64)} {
		for _, c := range []struct {
			later bool
			line1 string
			runes int
			start string
		}{
			{false, "✗ The run documents ask for the full suite in a write-no-push workflow.", 71, "The run did not start: "},
			{true, "✗ The next-session answer asks for the full suite in a write-no-push workflow.", 78, "The session did not start: "},
		} {
			workflows := map[string]stack.Workflow{name: {Access: stack.AccessWriteNoPush}}
			err := fullSuiteNoPlan(&codeRun{workflow: name, fullSuite: true}, workflows, c.later)
			if err == nil {
				t.Fatalf("the workflow %q is not refused", name)
			}
			var buf bytes.Buffer
			ui.New(&buf).Fail(err)
			line1, line2, _ := strings.Cut(strings.TrimSuffix(buf.String(), "\n"), "\n")
			if line1 != c.line1 || utf8.RuneCountInString(line1) != c.runes {
				t.Errorf("line 1 as printed is %q with %d characters, want %q with %d", line1, utf8.RuneCountInString(line1), c.line1, c.runes)
			}
			if want := "    " + c.start + name + " has write-no-push access, so it has no plan to read the full suite from; the code is used"; line2 != want {
				t.Errorf("line 2 as printed is %q, want %q", line2, want)
			}
		}
	}
}
