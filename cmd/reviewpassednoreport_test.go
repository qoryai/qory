package cmd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"
)

// blockingLedger is a ledger that keeps the findings that a post raises for a missing
// report, as Qory Apiary does, so a later GET lists them open, and counts them with open
// as the open blocking findings of the stage.
type blockingLedger struct {
	ledger
	raised int
}

// blocking is the number of open blocking findings of the stage.
func (l *blockingLedger) blocking() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open + l.raised
}

func (l *blockingLedger) post(ctx context.Context, body []byte) ([]byte, error) {
	var post contracts.FindingsPost
	if err := json.Unmarshal(body, &post); err != nil {
		return nil, err
	}
	l.mu.Lock()
	for _, e := range post.Events {
		if e.Kind == "raise" && strings.HasPrefix(e.By, "report-") {
			l.raised++
			f := reviewerFinding(fmt.Sprintf("f%d", 10+l.raised))
			f.Source = contracts.FindingSource{Kind: "check", Name: e.By}
			l.got.Findings = append(l.got.Findings, f)
		}
	}
	l.mu.Unlock()
	out, err := l.ledger.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var answer contracts.FindingsPostAnswer
	if err := json.Unmarshal(out, &answer); err != nil {
		return nil, err
	}
	answer.OpenBlocking = nil
	if n := l.blocking(); n > 0 {
		answer.OpenBlocking = []contracts.FindingsOpenBlocking{{Stage: "a", Count: n}}
	}
	return json.Marshal(answer)
}

// withSecurityReviewer adds the role security-reviewer, which reads *.txt, beside
// code-reviewer, which reads every change, to the group reviewers of the module hello. The
// change of a Verify run has the file earlier.txt, so both roles are expected.
func withSecurityReviewer(t *testing.T) {
	t.Helper()
	was := reviewFiles
	files := maps.Clone(reviewFiles)
	files["modules/hello/qory-module.yaml"] = "apiVersion: qory.dev/v1alpha1\nname: hello\n" + helloScope +
		"roles: {agents: {reviewers: many}}\nprovides: {agents: {reviewers: [code-reviewer, security-reviewer]}}\n"
	files["modules/hello/agents/security-reviewer.md"] = "---\nname: security-reviewer\ndescription: Review security.\npaths: [\"*.txt\"]\n---\n\nReview.\n"
	reviewFiles = files
	t.Cleanup(func() { reviewFiles = was })
}

// sentVerdict is what came of one POST that a session sent: the refusal and the error of
// the run's check, the events that the ledger had when the check returned, and the status
// that Qory Apiary returned for the POST, 0 when the check stopped it.
type sentVerdict struct {
	refusal *session.RunAPIRefusal
	err     error
	events  []string
	status  int
}

// stored is whether Qory Apiary stored the verdict of the POST.
func (s sentVerdict) stored() bool { return s.status == http.StatusCreated }

// verifySending is a Verify run whose session runs agent, with the roles code-reviewer and
// security-reviewer, and a ledger l that has the open reviewer finding f1 when f1 is true.
// send sends one POST as the gateway does: through the check that the run set, and, when
// the check lets it go on, to the fake Qory Apiary, which returns 409 for passed while
// the stage has an open blocking finding. The ledger counts a raise of report-<role>.
func verifySending(t *testing.T, f1 bool, agent func(l *blockingLedger, seen *reviewSeen, send func(path string, body []byte) sentVerdict)) (*blockingLedger, pushed, *pushRepo) {
	t.Helper()
	withSecurityReviewer(t)
	l := &blockingLedger{}
	var mu sync.Mutex
	var check runAPICheck
	// blockPush is whether the fake returns 409 for the push that is on its way.
	var blockPush bool
	cfg := reviewCfg{findings: l.get, postFindings: l.post,
		onSetCheck: func(c runAPICheck) error {
			mu.Lock()
			defer mu.Unlock()
			check = c
			return nil
		},
		setup: func(p *pushRepo) {
			verifyWork(t, p, true)
			if f1 {
				l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
			}
			p.api.refuse = func(body string) string {
				if verdictOf(t, body) == "passed" && l.blocking() > 0 {
					return "open_blocking_findings"
				}
				return ""
			}
			p.api.post = func(_ int, w http.ResponseWriter) (bool, bool) {
				if blockPush {
					failing(w, http.StatusConflict, "open_blocking_findings", "")
				}
				return blockPush, false
			}
		},
		review: func(p *pushRepo, seen *reviewSeen) {
			mu.Lock()
			c := check
			mu.Unlock()
			if c == nil {
				t.Error("the run set no check of the run API")
				return
			}
			agent(l, seen, func(path string, body []byte) sentVerdict {
				var s sentVerdict
				s.refusal, s.err = c(context.Background(), session.RunAPIRequest{Path: path, Body: body})
				s.events = kindsOf(l.events())
				if s.refusal != nil || s.err != nil {
					return s
				}
				blockPush = path == "/artifacts" && l.blocking() > 0 && pushedVerdict(t, body) == "passed"
				resp, err := http.Post(p.api.base()+path, "application/json", strings.NewReader(string(body)))
				if err != nil {
					t.Error(err)
					return s
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				s.status = resp.StatusCode
				if path == "/verdict" && s.stored() {
					// Qory Apiary stores a verdict version that this run wrote.
					p.api.addVersion("verdict", "verdict.json", body)
				}
				return s
			})
		}}
	r, _, p := verifyRun(t, cfg)
	return l, r, p
}

// pushedVerdict is the verdict of the file of a push of the type verdict.
func pushedVerdict(t *testing.T, body []byte) string {
	t.Helper()
	var push struct {
		Files []struct {
			Content []byte `json:"content_base64"`
		}
	}
	if err := json.Unmarshal(body, &push); err != nil || len(push.Files) != 1 {
		t.Fatalf("the push %s has no one file: %v", body, err)
	}
	return verdictOf(t, string(push.Files[0].Content))
}

// bothMissing are the events that qory posts for a session with no report, with the
// security reviewer expected beside the code reviewer.
var bothMissing = []string{"raise report-code-reviewer", "raise report-security-reviewer"}

// raisesOf are the events of the ledger that raise a finding, sorted.
func raisesOf(events []string) []string {
	var raises []string
	for _, e := range events {
		if strings.HasPrefix(e, "raise ") {
			raises = append(raises, e)
		}
	}
	slices.Sort(raises)
	return raises
}

// TestAVerifySessionThatRecordsPassedWithNoReportGetsTheMissingReportFindingsFirst is a
// Verify session that starts no reviewer, leaves no report and records passed, by a POST
// of /verdict or a push of the type verdict: when the check returns, and before the
// POST goes on to Qory Apiary, qory has posted the blocking finding report-<role> of each
// expected role, so Qory Apiary returns 409 for passed and stores no verdict. The end of
// the session raises no finding twice.
func TestAVerifySessionThatRecordsPassedWithNoReportGetsTheMissingReportFindingsFirst(t *testing.T) {
	for _, c := range []struct {
		name, path string
		body       func(t *testing.T) []byte
	}{
		{"a POST of the verdict", "/verdict", func(*testing.T) []byte { return []byte(passedBody) }},
		{"a push of the type verdict", "/artifacts", func(t *testing.T) []byte { return verdictPush(t, "verdict", passedBody) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var sent sentVerdict
			l, r, p := verifySending(t, false, func(l *blockingLedger, seen *reviewSeen, send func(string, []byte) sentVerdict) {
				if len(seen.entries) != 0 {
					t.Errorf("the reports folder has %v, want it empty", seen.entries)
				}
				sent = send(c.path, c.body(t))
			})
			r.wantsClose(t, event.StateSucceeded, 0)
			if sent.refusal != nil || sent.err != nil {
				t.Fatalf("the check returned the refusal %+v and the error %v", sent.refusal, sent.err)
			}
			if got := raisesOf(sent.events); !slices.Equal(got, bothMissing) {
				t.Errorf("the events when the check returned are %v, want %v", sent.events, bothMissing)
			}
			if sent.status != http.StatusConflict || sent.stored() {
				t.Errorf("Qory Apiary returned %d for passed, want 409 and no stored verdict", sent.status)
			}
			if got := raisesOf(kindsOf(l.events())); !slices.Equal(got, bothMissing) {
				t.Errorf("the events at the end are %v, want each raise once", kindsOf(l.events()))
			}
			// The fake stores the verdicts that reach it: the session's POST of /verdict is the
			// only one, and qory sends none.
			if want := map[string]int{"/verdict": 1, "/artifacts": 0}[c.path]; len(p.api.verdicts) != want {
				t.Errorf("Qory Apiary got the verdicts %v, want %d", p.api.verdicts, want)
			}
		})
	}
}

// TestAVerifySessionThatRecordsAnyOtherVerdictWithNoReportGetsNoFindings is a Verify
// session that leaves no report and records changes-requested or escalate, with no
// reviewer on purpose: the check posts nothing, Qory Apiary stores the verdict, and the
// end of the session posts nothing.
func TestAVerifySessionThatRecordsAnyOtherVerdictWithNoReportGetsNoFindings(t *testing.T) {
	for _, c := range []struct {
		name, path, verdict string
	}{
		{"changes-requested", "/verdict", `{"verdict":"changes-requested"}`},
		{"escalate", "/verdict", `{"verdict":"escalate"}`},
		{"a push of changes-requested", "/artifacts", `{"verdict":"changes-requested"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var sent sentVerdict
			l, r, _ := verifySending(t, false, func(l *blockingLedger, seen *reviewSeen, send func(string, []byte) sentVerdict) {
				body := []byte(c.verdict)
				if c.path == "/artifacts" {
					body = verdictPush(t, "verdict", c.verdict)
				}
				sent = send(c.path, body)
			})
			r.wantsClose(t, event.StateSucceeded, 0)
			if sent.refusal != nil || sent.err != nil {
				t.Fatalf("the check returned the refusal %+v and the error %v", sent.refusal, sent.err)
			}
			if len(sent.events) != 0 {
				t.Errorf("the events when the check returned are %v, want none", sent.events)
			}
			if !sent.stored() {
				t.Errorf("Qory Apiary returned %d, want 201", sent.status)
			}
			if got := kindsOf(l.events()); len(got) != 0 {
				t.Errorf("the events at the end are %v, want none", got)
			}
		})
	}
}

// TestAVerifySessionThatWritesItsReportsAfterAPassedWithNoReportGetsTheirRound is a
// Verify session that records passed with no report, and then writes the report of the
// code reviewer, which closes f1, and records passed again: the first check posts the
// raises alone, and the second posts the round of the report, so a close of the finding
// of the missing report that Qory Apiary stores is not left out.
func TestAVerifySessionThatWritesItsReportsAfterAPassedWithNoReportGetsTheirRound(t *testing.T) {
	var first, second sentVerdict
	l, r, _ := verifySending(t, true, func(l *blockingLedger, seen *reviewSeen, send func(string, []byte) sentVerdict) {
		first = send("/verdict", []byte(passedBody))
		writeReport(t, seen, "code-reviewer", closesF1)
		second = send("/verdict", []byte(passedBody))
	})
	r.wantsClose(t, event.StateSucceeded, 0)
	if got := raisesOf(first.events); !slices.Equal(got, bothMissing) || len(first.events) != 2 {
		t.Errorf("the events after the first check are %v, want %v", first.events, bothMissing)
	}
	if got := slices.Clone(second.events[len(first.events):]); !slices.Contains(got, "close_fixed code-reviewer") {
		t.Errorf("the events after the second check are %v, want the close of f1 by code-reviewer", second.events)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.posts) != 2 {
		t.Errorf("%d POSTs of findings, want 2: the raises, then the round of the report", len(l.posts))
	}
}

// TestAReportThatAppearsBetweenTheCheckAndTheRoundIsPostedOnce is a Verify session that
// records passed with no report: the check sees an empty reports folder and picks the
// round of the missing reports, but the code reviewer's report appears while qory reads
// the findings for that round. The round raises the missing reports only, and the raise
// of the report is posted once, in the round of the end of the session.
func TestAReportThatAppearsBetweenTheCheckAndTheRoundIsPostedOnce(t *testing.T) {
	l, r, _ := verifySending(t, false, func(l *blockingLedger, seen *reviewSeen, send func(string, []byte) sentVerdict) {
		l.mu.Lock()
		l.onGet = func() { writeReport(t, seen, "code-reviewer", reportOf) }
		l.mu.Unlock()
		send("/verdict", []byte(passedBody))
	})
	r.wantsClose(t, event.StateSucceeded, 0)
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []string
	for _, post := range l.posts {
		all = append(all, strings.Join(kindsOf(post.Events), ","))
	}
	if n := strings.Count(strings.Join(all, ";"), "raise code-reviewer"); n != 1 {
		t.Errorf("the POSTs have %v, want the raise of the report once", all)
	}
	if len(all) == 0 || strings.Contains(all[0], "raise code-reviewer") {
		t.Errorf("the POSTs have %v, want the first to raise the missing reports only", all)
	}
}

// reviewSending is a run whose session 2 is a review join with the roles code-reviewer
// and security-reviewer, both expected, whose session runs agent: it starts no reviewer
// and leaves no report. send sends one POST through the check that the run set, as the
// gateway does, and returns the refusal and the error of the check. The full suite
// passes, so qory sends no verdict for it. stack is the workflows table of the
// run, "" for the default one, in which every workflow judges.
func reviewSending(t *testing.T, stack string, agent func(p *pushRepo, send func(path string, body []byte) (*session.RunAPIRefusal, error))) (*blockingLedger, pushed, *pushRepo) {
	t.Helper()
	withSecurityReviewer(t)
	l := &blockingLedger{}
	var mu sync.Mutex
	var check runAPICheck
	cfg := reviewCfg{green: true, stack: stack, findings: l.get, postFindings: l.post,
		onSetCheck: func(c runAPICheck) error {
			mu.Lock()
			defer mu.Unlock()
			check = c
			return nil
		},
		review: func(p *pushRepo, seen *reviewSeen) {
			mu.Lock()
			c := check
			mu.Unlock()
			if c == nil {
				t.Error("the run set no check of the run API")
				return
			}
			agent(p, func(path string, body []byte) (*session.RunAPIRefusal, error) {
				return c(context.Background(), session.RunAPIRequest{Path: path, Body: body})
			})
		}}
	r, _, p := reviewRunWith(t, cfg, answerFor(2, "review-changes"))
	return l, r, p
}

// TestAReviewJoinReturns422ForAPassedWithNoReportAndRaisesTheMissingReportFindings is a
// review session that starts no reviewer, leaves no report and records passed by a POST
// of /verdict or a push of the type verdict, in a run where every workflow judges and in
// one whose workflow review-changes sets judges false: the check returns 422 with the
// first line of its rule, qory stores no passed, raises the blocking finding
// report-<role> of each expected role and sends changes-requested.
func TestAReviewJoinReturns422ForAPassedWithNoReportAndRaisesTheMissingReportFindings(t *testing.T) {
	const (
		reviewFix = "Record changes-requested, escalate or no verdict: qory sends passed after the round."
		judgeFix  = "Record escalate or no verdict: the next workflow in the loop judges this work."
	)
	for _, c := range []struct {
		name, path, stack, fix string
		body                   func(t *testing.T) []byte
	}{
		{"a POST of the verdict", "/verdict", "", reviewFix, func(*testing.T) []byte { return []byte(passedBody) }},
		{"a push of the type verdict", "/artifacts", "", reviewFix, func(t *testing.T) []byte { return verdictPush(t, "verdict", passedBody) }},
		{"a POST of the verdict, judges false", "/verdict", loopWorkflows("") + doesNotJudge, judgeFix, func(*testing.T) []byte { return []byte(passedBody) }},
		{"a push of the type verdict, judges false", "/artifacts", loopWorkflows("") + doesNotJudge, judgeFix, func(t *testing.T) []byte { return verdictPush(t, "verdict", passedBody) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var refusal *session.RunAPIRefusal
			var err error
			l, r, p := reviewSending(t, c.stack, func(p *pushRepo, send func(string, []byte) (*session.RunAPIRefusal, error)) {
				refusal, err = send(c.path, c.body(t))
			})
			r.wantsClose(t, event.StateSucceeded, 0)
			if err != nil || refusal == nil {
				t.Fatalf("the check returned the refusal %+v and the error %v, want a refusal", refusal, err)
			}
			first, _, _ := strings.Cut(refusal.Message, "\n")
			if refusal.Status != http.StatusUnprocessableEntity || refusal.Code != "push_check_failed" || first != c.fix {
				t.Errorf("the refusal is %d %s %q, want 422 push_check_failed %q", refusal.Status, refusal.Code, first, c.fix)
			}
			if !strings.Contains(refusal.Message, `The session sent the verdict "passed".`) {
				t.Errorf("the refusal text is %q, want the cause that the session sent passed", refusal.Message)
			}
			if got := kindsOf(l.events()); !slices.Equal(raisesOf(got), bothMissing) || len(got) != 2 {
				t.Errorf("the events are %v, want %v", got, bothMissing)
			}
			if len(p.api.verdicts) != 1 || verdictOf(t, p.api.verdicts[0]) != "changes-requested" {
				t.Errorf("the verdicts that Qory Apiary got are %v, want one changes-requested", p.api.verdicts)
			}
		})
	}
}

// TestAReviewJoinThatRecordsChangesRequestedWithNoReportGetsNoFindingsAndNoVerdict is the
// control of the cases above: a review session that starts no reviewer on purpose and
// records changes-requested gets no 422, and qory posts no finding and sends no verdict.
func TestAReviewJoinThatRecordsChangesRequestedWithNoReportGetsNoFindingsAndNoVerdict(t *testing.T) {
	var refusal *session.RunAPIRefusal
	var err error
	l, r, p := reviewSending(t, "", func(p *pushRepo, send func(string, []byte) (*session.RunAPIRefusal, error)) {
		const body = `{"verdict":"changes-requested"}`
		refusal, err = send("/verdict", []byte(body))
		// Qory Apiary stores the verdict that the check let through.
		p.api.addVersion("verdict", "verdict.json", []byte(body))
	})
	r.wantsClose(t, event.StateSucceeded, 0)
	if err != nil || refusal != nil {
		t.Errorf("the check returned the refusal %+v and the error %v, want neither", refusal, err)
	}
	if got := kindsOf(l.events()); len(got) != 0 {
		t.Errorf("the events are %v, want none", got)
	}
	if len(p.api.verdicts) != 0 {
		t.Errorf("the verdicts that Qory Apiary got are %v, want none", p.api.verdicts)
	}
}
