package cmd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// suiteFilesOf is the names of the suite result files in the review folder of the run
// folder runDir, in order.
func suiteFilesOf(t *testing.T, runDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(runDir, "review"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "suite-") {
			names = append(names, e.Name())
		}
	}
	return names
}

// TestAFirstSessionWithNoCommitRunsTheSuiteOnTheUnchangedCommit is a write run whose
// session 1 makes no commit and has the full suite, and whose session 2 reviews. The suite
// runs once, on the commit that the run started at, since the run has no suite result for
// it. The review join finds its suite result file, and the review session has no after
// step although its next-session answer sets full_suite.
func TestAFirstSessionWithNoCommitRunsTheSuiteOnTheUnchangedCommit(t *testing.T) {
	withSuite := strings.Replace(answerFor(2, "review-changes"), `"full_suite":false`, `"full_suite":true`, 1)
	r, joins, p := reviewRunWith(t, reviewCfg{writes: []int{}}, withSuite)
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 2 {
		t.Fatalf("%d joins, want 2\n%s", len(joins), r.out)
	}
	after := joins[0].spec.After
	if len(after) != 9 || after[6] != p.first || after[7] != "" {
		t.Errorf("session 1 has the after step %q, want the start commit %s and no tested commit", after, p.first)
	}
	if len(joins[1].spec.After) != 0 {
		t.Errorf("the review session has the after step %q, want none", joins[1].spec.After)
	}
	seen := reviewSessionOf(t, joins, 1)
	if seen.commit != p.first {
		t.Errorf("the review reads %s, want the start commit %s", seen.commit, p.first)
	}
	if !seen.hasSuite || !strings.Contains(seen.suite, `"failed":1`) || !strings.Contains(seen.suite, "qory-suite-head "+p.first) {
		t.Errorf("review_session.suite_result is %q, want the suite result on %s", seen.suite, p.first)
	}
	if got := suiteFilesOf(t, p.h.runDir); !slices.Equal(got, []string{"suite-1.json"}) {
		t.Errorf("the suite result files are %v, want suite-1.json alone", got)
	}
	wants(t, r.out, "the full suite failed on "+p.first[:12], "posted the verdict changes-requested for the full suite")
}

// TestASecondSessionWithNoCommitRunsNoSuiteAndKeepsTheFirstsRecord is a write run whose
// sessions 1 and 2 make no commit and have the full suite, and whose session 3 reviews.
// Session 1's suite is the run's suite result for the commit, so session 2 runs none: it
// writes no suite result file, posts nothing, and the review reads the file of session 1.
func TestASecondSessionWithNoCommitRunsNoSuiteAndKeepsTheFirstsRecord(t *testing.T) {
	r, joins, p := reviewRunWith(t, reviewCfg{writes: []int{}}, fullSuiteAnswer(2, 30), answerFor(3, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 3 {
		t.Fatalf("%d joins, want 3\n%s", len(joins), r.out)
	}
	if after := joins[1].spec.After; len(after) != 9 || after[7] != p.first {
		t.Errorf("session 2 has the after step %q, want the tested commit %s", after, p.first)
	}
	if got := suiteFilesOf(t, p.h.runDir); !slices.Equal(got, []string{"suite-1.json"}) {
		t.Errorf("the suite result files are %v, want suite-1.json alone", got)
	}
	first, err := os.ReadFile(filepath.Join(p.h.runDir, "review", "suite-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if seen := reviewSessionOf(t, joins, 2); seen.suite != string(first) || seen.commit != p.first {
		t.Errorf("the review reads %s with the suite result %q, want session 1's %q", seen.commit, seen.suite, first)
	}
	if got := strings.Count(r.out, "the full suite failed on"); got != 1 {
		t.Errorf("%d suites ended, want 1\n%s", got, r.out)
	}
	if got := strings.Count(r.out, "posted the verdict"); got != 1 {
		t.Errorf("%d verdicts posted, want 1\n%s", got, r.out)
	}
	wants(t, r.out, "the full suite did not run: the run has a suite result for the commit of HEAD")
}

// TestARedSuiteOfAFirstRoundWithNoCommitDoesNotHoldBackTheNextRound is a write run whose
// session 1 makes no commit and gets a red suite, and whose session 2 makes a commit: the
// suite runs again, on the new commit, posts again, and the review reads the new record,
// not the first.
func TestARedSuiteOfAFirstRoundWithNoCommitDoesNotHoldBackTheNextRound(t *testing.T) {
	var pushed string
	r, joins, p := reviewRunWith(t, reviewCfg{writes: []int{2}, next: func(p *pushRepo, n int) {
		if n == 3 {
			pushed = p.branchHead(t)
		}
	}}, fullSuiteAnswer(2, 30), answerFor(3, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 3 {
		t.Fatalf("%d joins, want 3\n%s", len(joins), r.out)
	}
	if after := joins[1].spec.After; len(after) != 9 || after[6] != p.first || after[7] != p.first {
		t.Errorf("session 2 has the after step %q, want the start and the tested commit %s", after, p.first)
	}
	if got := suiteFilesOf(t, p.h.runDir); !slices.Equal(got, []string{"suite-1.json", "suite-2.json"}) {
		t.Errorf("the suite result files are %v, want one for each session", got)
	}
	seen := reviewSessionOf(t, joins, 2)
	if pushed == "" || pushed == p.first || seen.commit != pushed || !strings.Contains(seen.suite, "qory-suite-head "+pushed) {
		t.Errorf("the review reads %s with the suite result %q, want the suite on the pushed commit %s", seen.commit, seen.suite, pushed)
	}
	if got := strings.Count(r.out, "posted the verdict changes-requested for the full suite"); got != 2 {
		t.Errorf("%d verdicts posted, want 2\n%s", got, r.out)
	}
}

// TestARunThatStartsAtAnEarlierRunsCommitRunsTheSuiteAlthoughHeadDoesNotMove is a run
// whose branch has the commit of an earlier run, as after a lost machine: the run starts
// there, its session makes no commit, and the suite runs on that commit, since this run
// has no suite result for it.
func TestARunThatStartsAtAnEarlierRunsCommitRunsTheSuiteAlthoughHeadDoesNotMove(t *testing.T) {
	p, _ := sharedPushRepo(t)
	change := p.change
	p.change = func(d map[string]any) {
		change(d)
		d["type_settings"] = map[string]any{"full_suite": true}
	}
	suiteServed(t, p, nil)
	old := p.earlierPush(t)
	p.api.pushedCommits = fmt.Sprintf(`[{"system":%q,"path":"acme/site","commit":%q,"run_id":"run_old","cancelled":false,"at":"2026-11-16T09:00:00Z"}]`, proxygittest.Host, old)
	var spec session.SessionSpec
	p.mountsOf = func(s session.SessionSpec) { spec = s }
	p.noOutput = true
	r := p.run(t, nil, suiteResultsIn(t, &spec, func() string { return p.h.runDir }))
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(spec.After) != 9 || spec.After[6] != old || spec.After[7] != "" {
		t.Fatalf("the after step is %q, want the start commit %s and no tested commit\n%s", spec.After, old, r.out)
	}
	file, err := os.ReadFile(filepath.Join(p.h.runDir, "review", "suite-1.json"))
	if err != nil || !strings.Contains(string(file), "qory-suite-head "+old) {
		t.Errorf("the suite result file is %q (%v), want a suite on %s", file, err, old)
	}
}

// TestASuiteThatDidNotStartStillStopsTheReviewOfAnUnchangedCommit is a write run whose
// session 1 makes no commit and whose after step does not start: the review join stops
// with the text of a suite that did not start.
func TestASuiteThatDidNotStartStillStopsTheReviewOfAnUnchangedCommit(t *testing.T) {
	r, joins, _ := reviewRunWith(t, reviewCfg{writes: []int{}, result: func(func() string) func() (*session.Result, error) {
		n := numbered()
		return func() (*session.Result, error) {
			res, err := n()
			if res.Session == 1 {
				res.After = &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: -1}
			}
			return res, err
		}
	}}, answerFor(2, "review-changes"))
	if len(joins) != 1 {
		t.Fatalf("%d joins, want 1\n%s", len(joins), r.out)
	}
	r.wantsClose(t, event.StateFailed, 1)
	if r.err == nil || !strings.Contains(strings.ReplaceAll(r.err.Error(), "\n  ", " "), "The full suite of the pushed commit did not start, so the review did not start") {
		t.Errorf("the error is %v, want the text of a suite that did not start\n%s", r.err, r.out)
	}
}

// TestAFailedSessionWithNoCommitRunsTheSuiteButGetsNoVerdictForIt is a write run whose
// session 1 fails and makes no commit, with the full suite set and no suite result in the
// run: the suite runs on the unchanged commit and its result file is kept, but qory posts
// no verdict for the suite, since the session did not succeed. Session 2 reviews and sends
// the verdict of its round.
func TestAFailedSessionWithNoCommitRunsTheSuiteButGetsNoVerdictForIt(t *testing.T) {
	withSuite := strings.Replace(answerFor(2, "review-changes"), `"full_suite":false`, `"full_suite":true`, 1)
	r, joins, p := reviewRunWith(t, reviewCfg{writes: []int{}, fail1: true}, withSuite)
	if len(joins) == 0 {
		t.Fatalf("no session joined\n%s", r.out)
	}
	if after := joins[0].spec.After; len(after) != 9 || after[6] != p.first || after[7] != "" {
		t.Errorf("session 1 has the after step %q, want the start commit %s and no tested commit", after, p.first)
	}
	if got := suiteFilesOf(t, p.h.runDir); !slices.Equal(got, []string{"suite-1.json"}) {
		t.Errorf("the suite result files are %v, want suite-1.json", got)
	}
	// The review session after it sends the verdict of its round, which has no point of the
	// suite.
	for _, v := range p.api.verdicts {
		if strings.Contains(v, "full suite") {
			t.Errorf("qory posted the verdict %s for the suite of a session that failed", v)
		}
	}
	if strings.Contains(r.out, "for the full suite") {
		t.Errorf("qory run shows a verdict for the full suite of a session that failed\n%s", r.out)
	}
}
