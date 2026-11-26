package cmd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// ledger is Qory Apiary's findings of a work item behind the run's two findings calls:
// what its GET lists is got, each POST is kept, and its answer numbers the raises f1, f2
// and so on and counts open as the open blocking findings of the stage a.
type ledger struct {
	mu     sync.Mutex
	got    contracts.Findings
	posts  []contracts.FindingsPost
	open   int
	gets   int
	getErr error
	// keepBases has Qory Apiary store the first base of each target that a POST sends, as
	// it does, so that the next GET lists it.
	keepBases bool
	// onGet, when set, is called once by the next GET, before it lists: it models what the
	// session does while qory reads the findings.
	onGet func()
}

func (l *ledger) get(context.Context) ([]byte, error) {
	l.mu.Lock()
	hook := l.onGet
	l.onGet = nil
	l.mu.Unlock()
	if hook != nil {
		hook()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gets++
	if l.getErr != nil {
		return nil, l.getErr
	}
	return json.Marshal(l.got)
}

func (l *ledger) post(_ context.Context, body []byte) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var post contracts.FindingsPost
	if err := json.Unmarshal(body, &post); err != nil {
		return nil, err
	}
	l.posts = append(l.posts, post)
	for _, b := range post.Bases {
		if l.keepBases && !slices.ContainsFunc(l.got.Bases, func(have contracts.FindingBase) bool { return have.Target.Collection == b.Target.Collection }) {
			l.got.Bases = append(l.got.Bases, b)
		}
	}
	var answer contracts.FindingsPostAnswer
	for _, e := range post.Events {
		if e.Kind == "raise" {
			answer.Created = append(answer.Created, contracts.FindingCreated{Ref: e.Ref, ID: fmt.Sprintf("f%d", len(answer.Created)+1)})
		}
	}
	if l.open > 0 {
		answer.OpenBlocking = []contracts.FindingsOpenBlocking{{Stage: "a", Count: l.open}}
	}
	return json.Marshal(answer)
}

// events are the events of every POST, in order, except those of the check finding suite:
// the full suite of session 1 is red in these runs and raises it.
func (l *ledger) events() []contracts.FindingsPostEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []contracts.FindingsPostEvent
	for _, p := range l.posts {
		for _, e := range p.Events {
			if e.By != "suite" {
				all = append(all, e)
			}
		}
	}
	return all
}

// roundVerdicts are the verdicts that qory sent, except the first, which is the verdict
// for the red full suite of session 1.
func roundVerdicts(t *testing.T, p *pushRepo) []string {
	t.Helper()
	if len(p.api.verdicts) == 0 || !strings.Contains(p.api.verdicts[0], "The full suite failed") {
		t.Fatalf("the first verdict is not the verdict for the full suite: %v", p.api.verdicts)
	}
	return p.api.verdicts[1:]
}

// target is the target of the run of [reviewRunWith]: the collection R_1001, which the
// documents of [sharedPushRepo] set, at the repository acme/site.
func ledgerTarget() contracts.Target {
	return contracts.Target{Collection: "R_1001", Address: &contracts.TargetAddress{System: proxygittest.Host, Path: "acme/site"}}
}

// reviewerFinding is an open blocking finding of the stage a that the reviewer
// code-reviewer raised in round 1.
func reviewerFinding(id string) contracts.Finding {
	by := contracts.FindingActor{Kind: "qory", Name: "code-reviewer"}
	return contracts.Finding{ID: id, Target: ledgerTarget(), Stage: "a",
		Source: contracts.FindingSource{Kind: "reviewer", Name: "code-reviewer"}, Severity: "blocking",
		Place: contracts.FindingPlace{Quote: "quote", File: "a.go", Line: 1}, Point: "Point of " + id,
		Problem: "Problem", Fix: "Fix", Proof: "Proof", State: "open",
		Events: []contracts.FindingEvent{{Kind: "raised", Round: 1, Stage: "a", Actor: by}}}
}

// TestASecondRunReviewsFromTheBaseThatQoryApiaryStoresAndGetsThePreviousFindings is a run
// whose work item has the findings of an earlier run: the base of the target, and a
// finding of the reviewer code-reviewer. The run starts at the earlier run's commit, so
// its checkout lacks the stored base; the review join fetches it, the diff starts there
// and so has the earlier run's change too, and qory.review_session.previous_findings has
// the finding with the round that starts.
func TestASecondRunReviewsFromTheBaseThatQoryApiaryStoresAndGetsThePreviousFindings(t *testing.T) {
	l := &ledger{}
	var side string
	cfg := reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		// The stored base is a commit that no branch of the run reaches, which only a fetch
		// by its id brings into the checkout.
		tree := proxygittest.Git(t, p.bare, "rev-parse", p.first+"^{tree}")
		side = proxygittest.Git(t, p.bare, "-c", "user.name=qory", "-c", "user.email=qory@localhost", "commit-tree", tree, "-p", p.first, "-m", "Side")
		proxygittest.Git(t, p.bare, "update-ref", "refs/heads/side", side)
		old := p.earlierPush(t)
		p.api.pushedCommits = fmt.Sprintf(`[{"system":%q,"path":"acme/site","commit":%q,"run_id":"run_old","cancelled":false,"at":"2026-11-16T09:00:00Z"}]`, proxygittest.Host, old)
		l.got = contracts.Findings{Bases: []contracts.FindingBase{{Target: ledgerTarget(), Base: side}}, Findings: []contracts.Finding{reviewerFinding("f1")}}
	}}
	r, joins, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	seen := reviewSessionOf(t, joins, 1)
	if !seen.hasDiff || !strings.Contains(seen.diff, "+++ b/earlier.txt") || !strings.Contains(seen.diff, "+++ b/s1.txt") {
		t.Errorf("review_session.diff is %q, want the change from the stored base %s, the earlier run's file included", seen.diff, side)
	}
	if !seen.hasPrevious {
		t.Fatalf("the review session has no previous_findings\n%s", r.out)
	}
	var file struct {
		Round    int `json:"round"`
		Findings []struct {
			ID, Point string
		}
	}
	data := []byte(seen.previous)
	if err := json.Unmarshal(data, &file); err != nil || file.Round != 1 || len(file.Findings) != 1 || file.Findings[0].ID != "f1" {
		t.Errorf("previous_findings is %s (%v), want the finding f1 of round 1", data, err)
	}
}

// TestARunWithNoStoredBaseReviewsFromItsStartAndGetsNoPreviousFindings is a run whose work
// item has no base and no finding of the group: the diff starts at the run's start, and
// the review session has no previous_findings.
func TestARunWithNoStoredBaseReviewsFromItsStartAndGetsNoPreviousFindings(t *testing.T) {
	l := &ledger{}
	r, joins, _ := reviewRunWith(t, reviewCfg{findings: l.get, postFindings: l.post}, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	seen := reviewSessionOf(t, joins, 1)
	if strings.Contains(seen.diff, "earlier.txt") || !strings.Contains(seen.diff, "+++ b/s1.txt") || seen.hasPrevious {
		t.Errorf("review_session has the diff %q and previous_findings %v, want the change of session 1 and none", seen.diff, seen.hasPrevious)
	}
	if l.gets == 0 {
		t.Error("the run read no findings")
	}
}

// reportOf is the report file of the reviewer code-reviewer with one blocking finding.
const reportOf = `{"reviewer":"code-reviewer","findings":[{"severity":"blocking","quote":"x := 1","file":"a.go","line":3,"point":"The value is unused.","problem":"x is never read.","fix":"Delete x.","proof":"failing check: go vet reports x as unused."}]}`

// writeReport writes the report file of role into the reports folder of a review session.
func writeReport(t *testing.T, seen *reviewSeen, role, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(seen.reports, role+".json"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// verdictOf is the verdict of the body of a POST verdict.
func verdictOf(t *testing.T, body string) string {
	t.Helper()
	var v struct{ Verdict string }
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	return v.Verdict
}

// kindsOf is the kind and the by of each event, "raise code-reviewer".
func kindsOf(events []contracts.FindingsPostEvent) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind+" "+e.By)
	}
	return out
}

// TestAReviewRoundPostsTheReportAndSendsTheVerdictOfTheOpenFindings is a run whose review
// session leaves the report of its reviewer and records no verdict: qory posts the
// report's finding, once, and then sends one verdict, changes-requested while the answer
// lists an open blocking finding of the stage and passed when it lists none.
func TestAReviewRoundPostsTheReportAndSendsTheVerdictOfTheOpenFindings(t *testing.T) {
	for _, c := range []struct {
		name    string
		open    int
		verdict string
	}{{"an open blocking finding", 1, "changes-requested"}, {"none left", 0, "passed"}} {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{open: c.open}
			cfg := reviewCfg{findings: l.get, postFindings: l.post, review: func(p *pushRepo, seen *reviewSeen) {
				writeReport(t, seen, "code-reviewer", reportOf)
			}}
			r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
			r.wantsClose(t, event.StateSucceeded, 0)
			if got := kindsOf(l.events()); !slices.Equal(got, []string{"raise code-reviewer"}) {
				t.Errorf("the events are %v, want one raise by code-reviewer", got)
			}
			if got := roundVerdicts(t, p); len(got) != 1 || verdictOf(t, got[0]) != c.verdict {
				t.Errorf("the verdicts sent are %v, want one %s", got, c.verdict)
			}
		})
	}
}

// TestAReviewRoundPostsTheEventsAndSendsNoVerdictWhenTheSessionRecordedOne is a review
// session that records a verdict and leaves a report: qory posts the report's events
// and sends no verdict.
func TestAReviewRoundPostsTheEventsAndSendsNoVerdictWhenTheSessionRecordedOne(t *testing.T) {
	l := &ledger{open: 1}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, review: func(p *pushRepo, seen *reviewSeen) {
		writeReport(t, seen, "code-reviewer", reportOf)
		p.api.addVersion("verdict", "verdict.json", []byte(`{"verdict":"changes-requested"}`))
	}}
	r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := kindsOf(l.events()); !slices.Equal(got, []string{"raise code-reviewer"}) {
		t.Errorf("the events are %v, want one raise by code-reviewer", got)
	}
	if got := roundVerdicts(t, p); len(got) != 0 {
		t.Errorf("qory sent the verdicts %v, want none", got)
	}
}

// TestAReviewRoundThatRecordedAVerdictAndLeftNoReportPostsNothing is a review session that
// records its verdict and starts no reviewer on purpose, so the reports folder is empty:
// qory posts no event, so no report-code-reviewer finding is raised or repeated and none
// is closed, and it sends no second verdict.
func TestAReviewRoundThatRecordedAVerdictAndLeftNoReportPostsNothing(t *testing.T) {
	l := &ledger{}
	open := reviewerFinding("f1")
	check := reviewerFinding("f2")
	check.Source = contracts.FindingSource{Kind: "check", Name: "report-code-reviewer"}
	l.got = contracts.Findings{Findings: []contracts.Finding{open, check}}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, review: func(p *pushRepo, seen *reviewSeen) {
		p.api.addVersion("verdict", "verdict.json", []byte(`{"verdict":"changes-requested"}`))
	}}
	r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := kindsOf(l.events()); len(got) != 0 {
		t.Errorf("the events are %v, want none", got)
	}
	if got := roundVerdicts(t, p); len(got) != 0 {
		t.Errorf("qory sent the verdicts %v, want none", got)
	}
}

// TestAReviewRoundWithNoReportRaisesTheReportCheckAndSendsChangesRequested is a review
// session that records no verdict and leaves no report: qory raises the blocking check
// finding report-code-reviewer and sends changes-requested.
func TestAReviewRoundWithNoReportRaisesTheReportCheckAndSendsChangesRequested(t *testing.T) {
	l := &ledger{open: 1}
	r, _, p := reviewRunWith(t, reviewCfg{findings: l.get, postFindings: l.post}, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	events := l.events()
	if len(events) != 1 || events[0].Kind != "raise" || events[0].By != "report-code-reviewer" {
		t.Errorf("the events are %v, want one raise by report-code-reviewer", kindsOf(events))
	}
	if got := roundVerdicts(t, p); len(got) != 1 || verdictOf(t, got[0]) != "changes-requested" {
		t.Errorf("the verdicts sent are %v, want one changes-requested", got)
	}
}

// replyOfF1 is a build ledger with the reply of the fix step of the finding f1.
const replyOfF1 = `{"steps":[{"id":"fix:f1","reply":{"kind":"fixed","reason":"Deleted x."}}]}`

// writesLedger is the next hook of a run whose session 1 writes the build ledger text, as
// the harness does while a session runs: the ledger is not there when the session starts.
func writesLedger(text string) func(p *pushRepo, n int) {
	return func(p *pushRepo, n int) {
		if n == 1 {
			p.api.addRunVersion("build-ledger", "build-ledger.json", []byte(text))
		}
	}
}

// TestAWriteSessionGetsTheOpenFindingsAndItsRepliesArePostedAfterThePush is a run whose
// work item has an open finding f1: session 1 gets open-findings.json with it, and once
// the session has pushed, qory posts the reply of the newest build ledger to f1 before the
// next-session request.
func TestAWriteSessionGetsTheOpenFindingsAndItsRepliesArePostedAfterThePush(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, next: writesLedger(replyOfF1), setup: func(p *pushRepo) {
		l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
	}}
	r, joins, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	lacks(t, r.out, "not posted")
	if !joins[0].hasOpen || !strings.Contains(joins[0].open, `"id": "f1"`) && !strings.Contains(joins[0].open, `"id":"f1"`) {
		t.Errorf("session 1 has the open findings %q, want f1", joins[0].open)
	}
	if joins[1].hasOpen {
		t.Errorf("the review session has the open findings %q, want none: only a session that writes gets them", joins[1].open)
	}
	events := l.events()
	if len(events) < 1 || events[0].Kind != "reply_fixed" || events[0].ID != "f1" || events[0].Reason != "Deleted x." {
		t.Errorf("the first event is %v, want reply_fixed of f1", events)
	}
}

// TestAReviewRoundClosesTheReportCheckOfARoleThatTheChangeNoLongerNeeds is a second run
// whose work item has a base for the target and an open check finding
// report-code-reviewer, and whose reviewer reads only files that no file of the change
// matches: qory closes the check finding as fixed with the reason, and sends no raise for
// a missing report.
func TestAReviewRoundClosesTheReportCheckOfARoleThatTheChangeNoLongerNeeds(t *testing.T) {
	for _, c := range []struct {
		name  string
		based bool
		want  []string
	}{{"a base of the work item", true, []string{"close_fixed report-code-reviewer"}}, {"no base", false, nil}} {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{}
			check := reviewerFinding("f1")
			check.Source = contracts.FindingSource{Kind: "check", Name: "report-code-reviewer"}
			cfg := reviewCfg{findings: l.get, postFindings: l.post, agent: "---\nname: code-reviewer\ndescription: Review code.\npaths: [\"*.go\"]\n---\n\nReview.\n",
				setup: func(p *pushRepo) {
					l.got = contracts.Findings{Findings: []contracts.Finding{check}}
					if c.based {
						l.got.Bases = []contracts.FindingBase{{Target: ledgerTarget(), Base: p.first}}
					}
				}}
			r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
			r.wantsClose(t, event.StateSucceeded, 0)
			got := kindsOf(l.events())
			if !slices.Equal(got, c.want) {
				t.Errorf("the events are %v, want %v", got, c.want)
			}
			if c.based && (len(l.events()) != 1 || l.events()[0].Reason != "No file of the change needs a report of code-reviewer.") {
				t.Errorf("the close is %+v, want the reason of a role that the change does not need", l.events())
			}
		})
	}
}

// TestAPassedVerdictWithStatus409IsSentAgainAsChangesRequested is a review round
// whose post lists no open blocking finding but whose verdict gets the status 409 with the
// code open_blocking_findings: qory sends changes-requested once.
func TestAPassedVerdictWithStatus409IsSentAgainAsChangesRequested(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, review: func(p *pushRepo, seen *reviewSeen) {
		writeReport(t, seen, "code-reviewer", `{"reviewer":"code-reviewer","findings":[]}`)
		p.api.refuse = func(body string) string {
			if verdictOf(t, body) == "passed" {
				return "open_blocking_findings"
			}
			return ""
		}
	}}
	r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	got := roundVerdicts(t, p)
	if len(got) != 2 || verdictOf(t, got[0]) != "passed" || verdictOf(t, got[1]) != "changes-requested" {
		t.Errorf("the verdicts sent are %v, want passed, then changes-requested", got)
	}
}

// TestAReviewRoundWhoseFindingsAreNotPostedStopsTheRunAndSendsNoVerdict is a review round
// whose post of the findings fails with a closed run: the run closes failed, exit 1, with
// the cause of the poster, and qory sends no verdict for the round.
func TestAReviewRoundWhoseFindingsAreNotPostedStopsTheRunAndSendsNoVerdict(t *testing.T) {
	l := &ledger{}
	posts := 0
	cfg := reviewCfg{findings: l.get, postFindings: func(ctx context.Context, body []byte) ([]byte, error) {
		posts++
		if posts > 1 {
			return nil, fmt.Errorf("run closed: %w", session.ErrRunClosed)
		}
		return l.post(ctx, body)
	}}
	r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateFailed, 1)
	if got := roundVerdicts(t, p); len(got) != 0 {
		t.Errorf("qory sent the verdicts %v, want none", got)
	}
	wants(t, r.out, "Findings batch 1 of 1 is not stored")
}

// TestARedSuiteRaisesTheFindingSuiteAndKeepsItsVerdict is a run whose session 1 has a red
// full suite: qory posts a raise of the check finding suite, with the point and the output
// of the suite as its proof, and the verdict for the suite is sent as before.
func TestARedSuiteRaisesTheFindingSuiteAndKeepsItsVerdict(t *testing.T) {
	l := &ledger{}
	r, _, p := reviewRunWith(t, reviewCfg{findings: l.get, postFindings: l.post}, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	l.mu.Lock()
	first := l.posts[0].Events
	l.mu.Unlock()
	if len(first) != 1 || first[0].Kind != "raise" || first[0].By != "suite" || first[0].Severity != "blocking" ||
		first[0].Point != "The full suite failed: 1 of 2 tests failed." || first[0].Proof == "" {
		t.Errorf("the first post is %+v, want a blocking raise of suite with the point and a proof", first)
	}
	if len(p.api.verdicts) == 0 || !strings.Contains(p.api.verdicts[0], "The full suite failed: 1 of 2 tests failed.") {
		t.Errorf("the verdicts are %v, want the verdict for the red suite first", p.api.verdicts)
	}
}

// TestAReviewRoundSendsItsVerdictAlthoughTheRunRecordedOneBefore is a review session that
// records no verdict, in a run that wrote a verdict before the session joined: that
// verdict is not the session's, so qory sends the verdict of the round.
func TestAReviewRoundSendsItsVerdictAlthoughTheRunRecordedOneBefore(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, next: func(p *pushRepo, n int) {
		if n == 1 {
			p.api.addVersion("verdict", "verdict.json", []byte(`{"verdict":"changes-requested"}`))
		}
	}, review: func(p *pushRepo, seen *reviewSeen) {
		writeReport(t, seen, "code-reviewer", `{"reviewer":"code-reviewer","findings":[]}`)
	}}
	r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	// The verdict that the run recorded stands over the red suite, so qory sent only the
	// verdict of the round.
	if got := p.api.verdicts; len(got) != 1 || verdictOf(t, got[0]) != "passed" {
		t.Errorf("the verdicts sent are %v, want one passed", got)
	}
}

// basesOf is the base that each POST of the ledger sent, in order: "" for none.
func basesOf(l *ledger) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, p := range l.posts {
		switch len(p.Bases) {
		case 0:
			out = append(out, "")
		case 1:
			out = append(out, p.Bases[0].Base)
		default:
			out = append(out, fmt.Sprintf("%d bases", len(p.Bases)))
		}
	}
	return out
}

// TestTheBaseIsSentOnceByTheFindingOfTheSuiteOfAWriteSession is a first run whose session
// 1 has a red suite: the first POST is the finding of the suite, and its base is the
// commit that the session started at. The POSTs after it, the review round's included,
// send none, since Qory Apiary stores the first.
func TestTheBaseIsSentOnceByTheFindingOfTheSuiteOfAWriteSession(t *testing.T) {
	l := &ledger{keepBases: true}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, review: func(p *pushRepo, seen *reviewSeen) {
		writeReport(t, seen, "code-reviewer", reportOf)
	}}
	r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	got := basesOf(l)
	if len(got) < 2 || got[0] != p.first || slices.Contains(got[1:], p.first) || slices.ContainsFunc(got[1:], func(b string) bool { return b != "" }) {
		t.Errorf("the POSTs sent the bases %q, want the start %s in the first and none in the rest", got, p.first)
	}
	if first := l.posts[0].Events; len(first) != 1 || first[0].By != "suite" {
		t.Errorf("the first POST has %v, want the finding of the suite", kindsOf(first))
	}
}

// TestTheBaseIsSentByTheReviewRoundWhenItPostsFirst is a first run whose suite is green,
// so session 1 posts nothing: the review round's POST is the first, and its base is the
// one that the plan of the review built, the commit that the run started at. The next
// POSTs send none.
func TestTheBaseIsSentByTheReviewRoundWhenItPostsFirst(t *testing.T) {
	l := &ledger{keepBases: true}
	cfg := reviewCfg{green: true, findings: l.get, postFindings: l.post, review: func(p *pushRepo, seen *reviewSeen) {
		writeReport(t, seen, "code-reviewer", reportOf)
	}}
	r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := basesOf(l); !slices.Equal(got, []string{p.first}) {
		t.Errorf("the POSTs sent the bases %q, want one POST with the start %s", got, p.first)
	}
	if got := kindsOf(l.events()); !slices.Equal(got, []string{"raise code-reviewer"}) {
		t.Errorf("the events are %v, want the raise of the review round", got)
	}
}

// TestTheBaseOfAWriteSessionThatPushesBeforeItsPostIsTheStartOfTheRun is a first run whose
// session 1 has a green suite and a reply in the build ledger: the reply is posted after
// the push step, which put the target in the version of the branch, and still sends the
// commit that the run started at, since the run read the branch before the push.
func TestTheBaseOfAWriteSessionThatPushesBeforeItsPostIsTheStartOfTheRun(t *testing.T) {
	l := &ledger{keepBases: true}
	cfg := reviewCfg{green: true, findings: l.get, postFindings: l.post, next: writesLedger(replyOfF1), setup: func(p *pushRepo) {
		l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
	}}
	r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	got := basesOf(l)
	if len(got) == 0 || got[0] != p.first || slices.ContainsFunc(got[1:], func(b string) bool { return b != "" }) {
		t.Fatalf("the POSTs sent the bases %q, want the start %s in the first only", got, p.first)
	}
	if first := l.posts[0].Events; len(first) != 1 || first[0].Kind != "reply_fixed" {
		t.Errorf("the first POST has %v, want the reply", kindsOf(first))
	}
}

// TestARunThatStartsAtAPushedCommitOfTheTargetSendsNoBase is a run whose work item lists a
// commit that an earlier run pushed to the target, so the run starts there: no POST of the
// run has a base, whether the finding of the suite, the replies or the review round posts,
// and session 1 pushes before the later POSTs.
func TestARunThatStartsAtAPushedCommitOfTheTargetSendsNoBase(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, next: writesLedger(replyOfF1), review: func(p *pushRepo, seen *reviewSeen) {
		writeReport(t, seen, "code-reviewer", reportOf)
	}, setup: func(p *pushRepo) {
		old := p.earlierPush(t)
		p.api.pushedCommits = fmt.Sprintf(`[{"system":%q,"path":"acme/site","commit":%q,"run_id":"run_old","cancelled":false,"at":"2026-11-16T09:00:00Z"}]`, proxygittest.Host, old)
		l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
	}}
	r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	got := basesOf(l)
	if len(got) < 3 || slices.ContainsFunc(got, func(b string) bool { return b != "" }) {
		t.Errorf("the POSTs sent the bases %q, want at least 3 POSTs and no base", got)
	}
}

// TestASecondRunWithAStoredBaseSendsNone is a run whose work item has the base of the
// target already: no POST of the run has a base.
func TestASecondRunWithAStoredBaseSendsNone(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, review: func(p *pushRepo, seen *reviewSeen) {
		writeReport(t, seen, "code-reviewer", reportOf)
	}, setup: func(p *pushRepo) {
		l.got = contracts.Findings{Bases: []contracts.FindingBase{{Target: ledgerTarget(), Base: p.first}}}
	}}
	r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	got := basesOf(l)
	if len(got) < 2 || slices.ContainsFunc(got, func(b string) bool { return b != "" }) {
		t.Errorf("the POSTs sent the bases %q, want at least 2 POSTs and no base", got)
	}
}

// TestAReplyOfALedgerThatAnotherRunWroteIsNotPosted is a work item whose newest build
// ledger, with a reply to the open finding f1, was written by another run: this run posts
// no reply, and its suite is green, so it posts no finding of the suite.
func TestAReplyOfALedgerThatAnotherRunWroteIsNotPosted(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{green: true, findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
		p.api.addVersionBy("run_other", "build-ledger", "build-ledger.json", []byte(`{"steps":[{"id":"fix:f1","reply":{"kind":"fixed","reason":"Deleted x."}}]}`))
	}}
	r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	for _, e := range l.events() {
		if e.Kind == "reply_fixed" || e.Kind == "reply_disputed" {
			t.Errorf("the run posted the reply %+v of a ledger that another run wrote", e)
		}
	}
}

// TestARepeatedFindingGetsNoReplyOfAPassThatAnEarlierSessionRan is a work item whose
// finding f3 was fixed in pass 1 of fix:f3, replied to, and repeated by the review round.
// The build ledger is cumulative: the ledger that session 1 writes has pass 1 in it as
// well. Session 1 that fails before fix:f3 posts no reply, and one that runs fix:f3 again
// as pass 2 posts the reply of pass 2.
func TestARepeatedFindingGetsNoReplyOfAPassThatAnEarlierSessionRan(t *testing.T) {
	const pass1 = `{"id":"fix:f3","pass":1,"reply":{"kind":"fixed","reason":"Fixed in pass 1."}}`
	const pass2 = `{"id":"fix:f3","pass":2,"reply":{"kind":"disputed","reason":"The repeat is wrong."}}`
	for _, c := range []struct {
		name, ledger string
		want         []string
	}{
		{"session 1 fails before fix:f3", `{"steps":[{"id":"plan","state":"failed"},` + pass1 + `]}`, nil},
		{"session 1 runs fix:f3 again", `{"steps":[` + pass1 + `,` + pass2 + `]}`, []string{"reply_disputed f3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{}
			f3 := reviewerFinding("f3")
			f3.Events = append(f3.Events,
				contracts.FindingEvent{Kind: "replied_fixed", Round: 1, Stage: "a", Actor: contracts.FindingActor{Kind: "qory", Name: "implement-changes"}, Reason: "Fixed in pass 1."},
				contracts.FindingEvent{Kind: "repeated", Round: 2, Stage: "a", Actor: contracts.FindingActor{Kind: "qory", Name: "code-reviewer"}})
			cfg := reviewCfg{green: true, findings: l.get, postFindings: l.post, next: writesLedger(c.ledger),
				setup: func(p *pushRepo) {
					l.got = contracts.Findings{Findings: []contracts.Finding{f3}}
					// The ledger of session 1 of the run before, which another run wrote.
					p.api.addVersionBy("run_old", "build-ledger", "build-ledger.json", []byte(`{"steps":[`+pass1+`]}`))
				}}
			r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
			r.wantsClose(t, event.StateSucceeded, 0)
			lacks(t, r.out, "not posted")
			var got []string
			for _, e := range l.events() {
				if e.Kind == "reply_fixed" || e.Kind == "reply_disputed" {
					got = append(got, e.Kind+" "+e.ID)
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("the replies are %v, want %v", got, c.want)
			}
		})
	}
}

// TestABuildLedgerOfAnotherRunThatQoryCannotReadStartsASessionThatPostsNoReplies is a work
// item whose newest build ledger, written by another run, is not JSON, has a name that
// qory cannot write to disk, is over 4 MiB, or has a row with the pass 999999999 that
// would hide each reply: session 1 starts, qory prints the two lines for it, the version
// number and the first check that the ledger fails, and the run posts no reply of the
// ledger that session 1 wrote, since qory cannot tell which passes ran before it.
func TestABuildLedgerOfAnotherRunThatQoryCannotReadStartsASessionThatPostsNoReplies(t *testing.T) {
	const line = "qory run: this session posts no replies: build ledger version 1 fails qory's checks\nqory run: "
	if first, _, _ := strings.Cut(line, "\n"); len(first) > 100 {
		t.Errorf("line 1 has %d characters, want at most 100", len(first))
	}
	for name, c := range map[string]struct{ file, content, check string }{
		"not JSON":    {"build-ledger.json", `{"steps":`, "The build ledger is not the JSON that the harness writes: "},
		"bad name":    {"../ledger.json", `{"steps":[]}`, `The newest build ledger has the file name "../ledger.json", which is not allowed` + "\n"},
		"too large":   {"build-ledger.json", `{"steps":[],"pad":"` + strings.Repeat("x", 4<<20) + `"}`, "The newest build ledger file has 4194325 bytes, more than 4 MiB\n"},
		"forged pass": {"build-ledger.json", `{"steps":[{"id":"fix:f1","pass":999999999}]}`, "Row 1 of the build ledger has the pass 999999999, and the harness writes 1 to 100\n"},
	} {
		t.Run(name, func(t *testing.T) {
			l := &ledger{}
			cfg := reviewCfg{green: true, findings: l.get, postFindings: l.post, next: writesLedger(replyOfF1), setup: func(p *pushRepo) {
				l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
				p.api.addVersionBy("run_old", "build-ledger", c.file, []byte(c.content))
			}}
			r, joins, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
			r.wantsClose(t, event.StateSucceeded, 0)
			wants(t, r.out, line+c.check)
			if len(joins) == 0 || !joins[0].hasOpen {
				t.Errorf("session 1 did not start with open-findings.json: %d sessions", len(joins))
			}
			for _, e := range l.events() {
				if e.Kind == "reply_fixed" || e.Kind == "reply_disputed" {
					t.Errorf("the run posted the reply %+v after a ledger that qory cannot read", e)
				}
			}
		})
	}
}

// TestAnEarlierLedgerWithAHigherPassHidesTheRepliesAndPrintsALine is a work item whose
// newest build ledger of another run has the pass 100 for the open finding f1, and session
// 1 writes a ledger whose reply of f1 has no pass: the run posts no reply, and prints one
// line with the number of replies that it did not post.
func TestAnEarlierLedgerWithAHigherPassHidesTheRepliesAndPrintsALine(t *testing.T) {
	const line = "qory run: 1 reply of the build ledger is not posted: an earlier ledger has a higher pass"
	l := &ledger{}
	cfg := reviewCfg{green: true, findings: l.get, postFindings: l.post, next: writesLedger(replyOfF1), setup: func(p *pushRepo) {
		l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
		p.api.addVersionBy("run_old", "build-ledger", "build-ledger.json", []byte(`{"steps":[{"id":"fix:f1","pass":100}]}`))
	}}
	r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	wants(t, r.out, line+"\n")
	if len(line) > 100 {
		t.Errorf("the line has %d characters, want at most 100", len(line))
	}
	for _, e := range l.events() {
		if e.Kind == "reply_fixed" || e.Kind == "reply_disputed" {
			t.Errorf("the run posted the reply %+v that the pass 100 hides", e)
		}
	}
}

// TestTwoRepliesThatAnEarlierLedgerHidesPrintTheLineInThePlural is the work item of the
// test above with the open findings f1 and f2, which the pass 100 hides in both: the line
// has the count 2 and the word "replies".
func TestTwoRepliesThatAnEarlierLedgerHidesPrintTheLineInThePlural(t *testing.T) {
	l := &ledger{}
	both := `{"steps":[{"id":"fix:f1","reply":{"kind":"fixed","reason":"Deleted x."}},{"id":"fix:f2","reply":{"kind":"fixed","reason":"Deleted y."}}]}`
	cfg := reviewCfg{green: true, findings: l.get, postFindings: l.post, next: writesLedger(both), setup: func(p *pushRepo) {
		l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1"), reviewerFinding("f2")}}
		p.api.addVersionBy("run_old", "build-ledger", "build-ledger.json", []byte(`{"steps":[{"id":"fix:f1","pass":100},{"id":"fix:f2","pass":100}]}`))
	}}
	r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	wants(t, r.out, "qory run: 2 replies of the build ledger are not posted: an earlier ledger has a higher pass\n")
}

// TestABuildLedgerThatQoryReadsLeavesNoWarning is the same work item with a ledger of
// another run that is as the harness writes it, with the pass 99, the highest below the
// harness's limit of 100: the run prints no line about the ledger, and posts the reply of
// pass 100 that session 1 wrote.
func TestABuildLedgerThatQoryReadsLeavesNoWarning(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{green: true, findings: l.get, postFindings: l.post, next: writesLedger(`{"steps":[{"id":"fix:f1","pass":100,"reply":{"kind":"fixed","reason":"Deleted x."}}]}`), setup: func(p *pushRepo) {
		l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
		p.api.addVersionBy("run_old", "build-ledger", "build-ledger.json", []byte(`{"steps":[{"id":"fix:f1","pass":99}]}`))
	}}
	r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	lacks(t, r.out, "fails qory's checks")
	lacks(t, r.out, "not posted")
	if !slices.ContainsFunc(l.events(), func(e contracts.FindingsPostEvent) bool { return e.Kind == "reply_fixed" && e.ID == "f1" }) {
		t.Errorf("the events are %v, want the reply_fixed of f1", l.events())
	}
}

// TestADownloadOfTheBuildLedgerThatFailsStopsASessionThatWritesBeforeItStarts is a work
// item whose newest build ledger cannot be fetched whole, such as a file with another
// sum than the index lists: that is a failed read, not a ledger that qory cannot read, so
// session 1 does not start and the run ends with the cause on line 1.
func TestADownloadOfTheBuildLedgerThatFailsStopsASessionThatWritesBeforeItStarts(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		p.api.addVersionBy("run_old", "build-ledger", "build-ledger.json", []byte(`{"steps":[]}`))
		p.api.shownFile = []byte("other content")
	}}
	r, joins, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "Fetching the build ledger failed, so the session did not start")
	if len(joins) != 0 || len(l.posts) != 0 {
		t.Errorf("%d sessions joined and %d POSTs were sent, want none", len(joins), len(l.posts))
	}
}

// TestABuildLedgerOfThisRunThatQoryCannotReadStopsASessionThatWritesBeforeItStarts is a
// work item whose newest build ledger was written by this run and is not JSON: the run
// wrote it, so the stop stays, and session 1 does not start.
func TestABuildLedgerOfThisRunThatQoryCannotReadStopsASessionThatWritesBeforeItStarts(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		p.api.addRunVersion("build-ledger", "build-ledger.json", []byte(`{"steps":`))
	}}
	r, joins, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "The newest build ledger does not pass qory's checks, so the session did not start")
	if len(joins) != 0 || len(l.posts) != 0 {
		t.Errorf("%d sessions joined and %d POSTs were sent, want none", len(joins), len(l.posts))
	}
}

// fullList is a GET that lists 500 findings, the most it lists: the work item may have
// more, so the list may hide an open finding.
func fullList() contracts.Findings {
	var all []contracts.Finding
	for i := 1; i <= 500; i++ {
		all = append(all, reviewerFinding(fmt.Sprintf("f%d", i)))
	}
	return contracts.Findings{Findings: all}
}

// TestAGetOfFiveHundredFindingsStopsTheRunBeforeAnythingIsDecidedFromIt is a GET that
// lists 500 findings at each place where qory reads the findings to decide something: the
// start of a session that writes, the finding of the suite, the replies, the start of a
// review join and the end of a review round. Each stops the run with the cause on the
// first line, writes no file for a session and posts nothing after it.
func TestAGetOfFiveHundredFindingsStopsTheRunBeforeAnythingIsDecidedFromIt(t *testing.T) {
	const line = "The GET of the findings lists 500, its limit, so "
	setFull := func(l *ledger) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.got = fullList()
	}
	t.Run("the start of a session that writes", func(t *testing.T) {
		l := &ledger{}
		cfg := reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) { l.got = fullList() }}
		r, joins, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
		r.wantsClose(t, event.StateFailed, 1)
		wants(t, r.out, line+"the session did not start")
		if len(joins) != 0 || len(l.posts) != 0 {
			t.Errorf("%d sessions joined and %d POSTs were sent, want none", len(joins), len(l.posts))
		}
	})
	t.Run("the finding of the suite", func(t *testing.T) {
		l := &ledger{}
		cfg := reviewCfg{findings: l.get, postFindings: l.post, next: func(p *pushRepo, n int) { setFull(l) }}
		r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
		r.wantsClose(t, event.StateFailed, 1)
		wants(t, r.out, line+"the session did not end")
		if len(l.posts) != 0 {
			t.Errorf("%d POSTs were sent, want none", len(l.posts))
		}
	})
	t.Run("the replies", func(t *testing.T) {
		l := &ledger{}
		third := func(ctx context.Context) ([]byte, error) {
			l.mu.Lock()
			if l.gets >= 2 {
				l.got = fullList()
			}
			l.mu.Unlock()
			return l.get(ctx)
		}
		cfg := reviewCfg{green: true, findings: third, postFindings: l.post, next: writesLedger(replyOfF1)}
		r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
		r.wantsClose(t, event.StateFailed, 1)
		wants(t, r.out, line+"the stage did not end")
		if len(l.posts) != 0 {
			t.Errorf("%d POSTs were sent, want none", len(l.posts))
		}
	})
	t.Run("the start of a review join", func(t *testing.T) {
		l := &ledger{}
		cfg := reviewCfg{findings: l.get, postFindings: l.post, between: func(n int) {
			if n == 1 {
				setFull(l)
			}
		}}
		r, joins, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
		r.wantsClose(t, event.StateFailed, 1)
		wants(t, r.out, line+"the review did not start")
		if len(joins) != 1 {
			t.Errorf("%d sessions joined, want only session 1: no review session and no previous_findings", len(joins))
		}
	})
	t.Run("the end of a review round", func(t *testing.T) {
		l := &ledger{}
		cfg := reviewCfg{findings: l.get, postFindings: l.post, next: func(p *pushRepo, n int) {
			if n == 2 {
				setFull(l)
			}
		}, review: func(p *pushRepo, seen *reviewSeen) { writeReport(t, seen, "code-reviewer", reportOf) }}
		r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
		r.wantsClose(t, event.StateFailed, 1)
		wants(t, r.out, line+"the review did not end")
		if got := kindsOf(l.events()); len(got) != 0 {
			t.Errorf("the events are %v, want none after the suite's", got)
		}
		if got := roundVerdicts(t, p); len(got) != 0 {
			t.Errorf("qory sent the verdicts %v, want none", got)
		}
	})
}

// TestAReviewRoundPostsNoKeyOfAReport is a report whose proof has a key of a known shape:
// the finding that qory posts has *** in its place.
func TestAReviewRoundPostsNoKeyOfAReport(t *testing.T) {
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	l := &ledger{}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, review: func(p *pushRepo, seen *reviewSeen) {
		writeReport(t, seen, "code-reviewer", strings.Replace(reportOf, "go vet reports x as unused.", "the key "+key+" is in a.go.", 1))
	}}
	r, _, _ := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	r.wantsClose(t, event.StateSucceeded, 0)
	events := l.events()
	if len(events) != 1 || events[0].Proof != "failing check: the key *** is in a.go." {
		t.Errorf("the events are %+v, want one raise with the key masked in the proof", events)
	}
}
