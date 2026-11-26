package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/suite"
)

// stagedRun is a findingsRun of a work item whose stage has a round, over calls.
func stagedRun(t *testing.T, calls findingsCalls) *findingsRun {
	t.Helper()
	var snapshot runapi.Snapshot
	if err := json.Unmarshal([]byte(`{"stage":{"id":"st_aaa","name":"Build","round":2,"rounds":3,"state":null}}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	return &findingsRun{poster: newFindingsPoster(calls), code: &codeRun{snapshot: &snapshot}, target: ledgerTarget}
}

// reviewGroupResult is a compose result whose workflow review-changes has the review group
// reviewers, with the members code-reviewer and test-reviewer.
func reviewGroupResult() *compose.Result {
	return &compose.Result{
		Stack:  &stack.Stack{Workflows: map[string]stack.Workflow{"review-changes": {ReviewGroup: "reviewers"}}},
		Groups: map[string][]string{"agents/reviewers": {"code-reviewer", "test-reviewer"}},
	}
}

// TestAReviewPlanTakesTheBaseThatQoryApiaryStoresForTheTarget: with findings, the diff
// starts at the stored base of the run's target, and at the run's start when the work item
// has no base for the target, a base for another target, or the run has no findings.
func TestAReviewPlanTakesTheBaseThatQoryApiaryStoresForTheTarget(t *testing.T) {
	const stored = "fedcba9876543210fedcba9876543210fedcba98"
	other := contracts.Target{Collection: "R_2002"}
	for _, c := range []struct {
		name      string
		bases     []contracts.FindingBase
		none      bool
		want      string
		ledger    bool
		wantGets  int
		wantRoles []string
	}{
		{"a stored base", []contracts.FindingBase{{Target: ledgerTarget, Base: stored}}, false, stored, true, 1, []string{"code-reviewer", "test-reviewer"}},
		{"no base", nil, false, headA, false, 1, []string{"code-reviewer", "test-reviewer"}},
		{"the base of another target", []contracts.FindingBase{{Target: other, Base: stored}}, false, headA, false, 1, []string{"code-reviewer", "test-reviewer"}},
		{"a run with no findings", []contracts.FindingBase{{Target: ledgerTarget, Base: stored}}, true, headA, false, 0, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeApiary()
			fake.bases = c.bases
			branch := &preparation.WorkBranch{Repositories: []preparation.WorkRepository{{Dir: ".", Path: "acme/shop"}}}
			setup := reviewJoinSetup{workflow: "review-changes", res: reviewGroupResult(), cwd: "/checkout", branch: branch}
			if !c.none {
				setup.findings = stagedRun(t, fake)
			}
			p := &reviewJoinPlan{base: headA}
			if err := p.readLedger(t.Context(), setup, "forge"); err != nil {
				t.Fatal(err)
			}
			if p.base != c.want || p.fromLedger != c.ledger || fake.gets != c.wantGets || !slices.Equal(p.roles, c.wantRoles) {
				t.Errorf("the base is %s (stored %v) after %d GETs with the roles %v, want %s (%v), %d and %v",
					p.base, p.fromLedger, fake.gets, p.roles, c.want, c.ledger, c.wantGets, c.wantRoles)
			}
		})
	}
}

// TestAReviewPlanStopsWhenTheFindingsAreNotRead: a GET that fails stops the plan with the
// text of the poster, and a plan that was not stopped has the findings it read.
func TestAReviewPlanStopsWhenTheFindingsAreNotRead(t *testing.T) {
	fake := newFakeApiary()
	fake.getFate = func(int) error { return errors.New("no route") }
	p := &reviewJoinPlan{base: headA}
	branch := &preparation.WorkBranch{Repositories: []preparation.WorkRepository{{Dir: ".", Path: "acme/shop"}}}
	err := p.readLedger(t.Context(), reviewJoinSetup{findings: stagedRun(t, fake), res: reviewGroupResult(), branch: branch}, "forge")
	if !hasStop(err, "The findings of the work item are not read, so the review did not start") {
		t.Errorf("a failed GET returned %v", err)
	}
	if fake.gets != maxSubmissionTries {
		t.Errorf("%d GETs, want %d tries", fake.gets, maxSubmissionTries)
	}
}

// TestPreviousFindingsFileHoldsTheFindingsOfTheGroupAndNoOtherFile: the file has the
// findings of the group's reviewers of the stage with the round that starts, and is left
// out when the group has none or the run has no findings.
func TestPreviousFindingsFileHoldsTheFindingsOfTheGroupAndNoOtherFile(t *testing.T) {
	mine := apiaryFinding("f1", sourceReviewer, "code-reviewer", stateOpen)
	mine.Stage = "a"
	foreign := apiaryFinding("f2", sourceReviewer, "other-reviewer", stateOpen)
	foreign.Stage = "a"
	elsewhere := apiaryFinding("f3", sourceReviewer, "code-reviewer", stateOpen)
	elsewhere.Stage = "verify"
	for _, c := range []struct {
		name   string
		run    bool
		listed []contracts.Finding
		want   []string
	}{
		{"findings of the group", true, []contracts.Finding{mine, foreign, elsewhere}, []string{"f1"}},
		{"no finding of the group", true, []contracts.Finding{foreign, elsewhere}, nil},
		{"no findings in the run", false, []contracts.Finding{mine}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &reviewJoinPlan{round: 2, stageSlug: "a", roles: []string{"code-reviewer", "test-reviewer"}, found: listing(c.listed...)}
			if c.run {
				p.findings = &findingsRun{}
			}
			dir := t.TempDir()
			path, err := p.previousFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			if c.want == nil {
				if path != "" || exists(filepath.Join(dir, "open-findings.json")) {
					t.Errorf("the file is %q, want none", path)
				}
				return
			}
			data, err := os.ReadFile(path)
			if err != nil || path != filepath.Join(dir, "open-findings.json") {
				t.Fatalf("the file is %q (%v)", path, err)
			}
			round, got := decodedFile(t, data)
			if round != 2 || !slices.Equal(ids(got), c.want) {
				t.Errorf("the file has round %d and the findings %v, want 2 and %v", round, ids(got), c.want)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o444 {
				t.Errorf("the file has mode %v (%v), want 0444", info, err)
			}
		})
	}
}

// stoppedRun is an openedRun whose host proxy does not start.
type stoppedRun struct{ openedRun }

func (stoppedRun) HostProxy() (hostProxy, error) { return hostProxy{}, errors.New("a session runs") }

// TestFetchBaseTouchesTheCheckoutOnlyForAStoredBase: a base that is the run's start needs
// no host proxy, and a stored base whose proxy does not start stops the review.
func TestFetchBaseTouchesTheCheckoutOnlyForAStoredBase(t *testing.T) {
	if err := (&reviewJoinPlan{base: headA}).fetchBase(t.Context(), stoppedRun{}); err != nil {
		t.Errorf("the run's start was fetched: %v", err)
	}
	err := (&reviewJoinPlan{base: headB, fromLedger: true}).fetchBase(t.Context(), stoppedRun{})
	if !hasStop(err, "The host proxy did not start, so the review did not start") || !strings.Contains(err.Error(), "a session runs") {
		t.Errorf("a stored base with no host proxy returned %v", err)
	}
}

// TestAFindingsRunKeepsTheBranchAsTheRunStarted: begin copies the Version and the Pushed
// of the branch and the Start of its target, so a push that changes the branch, by a new
// entry or by a change of an entry in place, does not change the bases that a later post
// sends, and a first run sends the start as the base.
func TestAFindingsRunKeepsTheBranchAsTheRunStarted(t *testing.T) {
	f := &findingsRun{target: ledgerTarget}
	branch := &preparation.WorkBranch{
		Version:      []preparation.Repository{{System: "other", Path: "acme/lib", Commit: headA}},
		Pushed:       []preparation.Landed{{System: "other", Path: "acme/lib", Commit: headA}},
		Repositories: []preparation.WorkRepository{{Dir: ".", Path: "acme/shop", Start: headA}},
	}
	f.begin(branch)
	branch.Version[0] = preparation.Repository{System: "forge", Path: "acme/shop", Commit: headB}
	branch.Pushed[0] = preparation.Landed{System: "forge", Path: "acme/shop", Commit: headB}
	branch.Version = append(branch.Version, preparation.Repository{System: "forge", Path: "acme/shop", Commit: headB})
	branch.Pushed = append(branch.Pushed, preparation.Landed{System: "forge", Path: "acme/shop", Commit: headB})
	if f.start != headA || len(f.version) != 1 || len(f.pushed) != 1 || f.version[0].Path != "acme/lib" || f.pushed[0].Path != "acme/lib" {
		t.Fatalf("the findings have the start %s, the version %v and the pushed %v", f.start, f.version, f.pushed)
	}
	got := f.bases(contracts.Findings{}, f.start)
	if len(got) != 1 || got[0].Base != headA {
		t.Errorf("the bases are %+v, want the start of the run for the target", got)
	}
	(*findingsRun)(nil).begin(branch)
}

// TestABranchWithTheTargetAtTheStartGivesNoBase: when the Version or the Pushed that the
// run read at its start has the target repository, the bases are none. With another
// repository only, the bases are the start of the run.
func TestABranchWithTheTargetAtTheStartGivesNoBase(t *testing.T) {
	repositories := []preparation.WorkRepository{{Dir: ".", Path: "acme/shop", Start: headA}}
	for _, c := range []struct {
		name   string
		branch preparation.WorkBranch
		want   bool
	}{
		{"a version of the target", preparation.WorkBranch{Version: []preparation.Repository{{System: "forge", Path: "acme/shop", Commit: headB}}}, false},
		{"a pushed commit of the target", preparation.WorkBranch{Pushed: []preparation.Landed{{System: "forge", Path: "acme/shop", Commit: headB}}}, false},
		{"another repository", preparation.WorkBranch{Version: []preparation.Repository{{System: "forge", Path: "acme/lib", Commit: headB}}, Pushed: []preparation.Landed{{System: "forge", Path: "acme/lib", Commit: headB}}}, true},
		{"no entry", preparation.WorkBranch{}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &findingsRun{target: ledgerTarget}
			c.branch.Repositories = repositories
			f.begin(&c.branch)
			got := f.bases(contracts.Findings{}, f.start)
			if (len(got) == 1) != c.want || c.want && got[0].Base != headA {
				t.Errorf("the bases are %+v, want the start: %v", got, c.want)
			}
		})
	}
}

// suiteFinding is an open check finding suite of the stage a, as a GET lists it.
func suiteFinding() contracts.Finding {
	f := apiaryFinding("f4", sourceCheck, checkSuite, stateOpen)
	f.Stage = "a"
	return f
}

// TestASuiteThatEndedIsPostedAsTheFindingSuite: a red suite, and one that did not start,
// raises the check finding suite or repeats the open one, a green suite closes it, and a
// skipped suite, or a run with no findings, posts nothing.
func TestASuiteThatEndedIsPostedAsTheFindingSuite(t *testing.T) {
	for _, c := range []struct {
		name  string
		state suite.State
		fault bool
		open  bool
		none  bool
		want  []string
	}{
		{"red", suite.Failed, false, false, false, []string{eventRaise}},
		{"red again", suite.Failed, false, true, false, []string{eventRepeat}},
		{"timed out", suite.Timeout, false, false, false, []string{eventRaise}},
		{"missing", "", true, false, false, []string{eventRaise}},
		{"missing again", "", true, true, false, []string{eventRepeat}},
		{"green", suite.Passed, false, true, false, []string{eventCloseFixed}},
		{"green, nothing open", suite.Passed, false, false, false, nil},
		{"skipped", suite.Skipped, false, true, false, nil},
		{"a run with no findings", suite.Failed, false, false, true, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeApiary()
			if c.open {
				fake.findings = []contracts.Finding{suiteFinding()}
			}
			r := &suiteRun{}
			if !c.none {
				r.findings = stagedRun(t, fake)
			}
			var fault *suiteFault
			var point *runapi.Point
			if c.fault {
				fault = noAfterFault()
				point = &fault.point
			}
			ending := endingOf(fault, suite.Outcome{State: c.state}, point)
			if c.none {
				if err := r.findings.postSuite(t.Context(), ending); err != nil {
					t.Fatal(err)
				}
			} else if err := r.findings.postSuiteOf(t.Context(), "a", ending); err != nil {
				t.Fatal(err)
			}
			if c.fault && (ending.State != suite.Failed || ending.Point != textPointNoAfter) {
				t.Errorf("a suite that did not start ends as %+v, want red with the point of the fault", ending)
			}
			var kinds []string
			for _, b := range fake.bodies {
				for _, e := range b.Events {
					kinds = append(kinds, e.Kind)
					if e.By != checkSuite {
						t.Errorf("the event is by %q, want suite", e.By)
					}
				}
			}
			if !slices.Equal(kinds, c.want) {
				t.Errorf("the events are %v, want %v", kinds, c.want)
			}
		})
	}
}

// TestFinishReviewOfARunWithNoFindingsDoesNothing: a run with no findings, or a review
// plan that has none, reads and posts nothing, and does not stop on the missing run.
func TestFinishReviewOfARunWithNoFindingsDoesNothing(t *testing.T) {
	if err := (*findingsRun)(nil).finishReview(t.Context(), &reviewJoinPlan{}); err != nil {
		t.Errorf("a nil findings run returned %v", err)
	}
	fake := newFakeApiary()
	if err := stagedRun(t, fake).finishReview(t.Context(), &reviewJoinPlan{}); err != nil {
		t.Errorf("a plan with no findings returned %v", err)
	}
	if fake.gets != 0 || fake.posts != 0 {
		t.Errorf("%d GETs and %d POSTs, want none", fake.gets, fake.posts)
	}
}
