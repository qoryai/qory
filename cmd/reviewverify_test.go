package cmd_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// pullRequestOf is the file of a pull-request version whose head is head and whose base is
// base, both commits of the target of the run, as references.
func pullRequestOf(head, base string) []byte {
	ref := func(commit string) string {
		return fmt.Sprintf(`{"collection":"R_1001","address":{"system":%q,"path":%q}}`, proxygittest.Host, commit)
	}
	return []byte(`{"target":` + ref("") + `,"title":"A change","head":` + ref(head) + `,"base":` + ref(base) + `}`)
}

// verifyWork is the setup of a Verify run: the work item's branch on the forge has a
// commit above the start, the stage's input is a pull-request version whose head is that
// commit, and ledger l keeps the findings of the work item. It returns the commit that the
// checkout is at when the session starts. based is whether the version's base is the start
// commit; otherwise the version has another head than the commit.
func verifyWork(t *testing.T, p *pushRepo, based bool) (head string) {
	t.Helper()
	head = p.earlierPush(t)
	p.api.pushedCommits = fmt.Sprintf(`[{"system":%q,"path":"acme/site","commit":%q,"run_id":"run_old","cancelled":false,"at":"2026-11-16T09:00:00Z"}]`, proxygittest.Host, head)
	shown := head
	if !based {
		shown = p.first
	}
	p.api.addVersionBy("run_old", "pull-request", "pull-request.json", pullRequestOf(shown, p.first))
	p.api.inputs = `[{"type":"pull-request","required":false,"version":{"id":"av_1","number":1}}]`
	return head
}

// verifyRun is a run of one Verify session: the workflow hello at write-no-push with the
// review group reviewers, which writes nothing. cfg is changed before the run starts.
func verifyRun(t *testing.T, cfg reviewCfg) (r pushed, joins []reviewJoined, p *pushRepo) {
	t.Helper()
	cfg.verify, cfg.noSuite, cfg.writes = true, true, []int{}
	return reviewRunWith(t, cfg)
}

// TestAVerifySessionGetsTheReviewSessionAndAWritableCheckout is a run of one session at
// write-no-push whose workflow starts reviewers, on a branch with a pull-request version
// of the stage's input: the session gets qory.review_session with the commit that its
// checkout is at, a read-only copy of it, the diff from the version's base and a reports
// folder, beside its writable checkout. It gets no suite result, no previous findings and
// no build report.
func TestAVerifySessionGetsTheReviewSessionAndAWritableCheckout(t *testing.T) {
	l := &ledger{}
	var head string
	r, joins, p := verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		head = verifyWork(t, p, true)
	}})
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 1 || joins[0].seen == nil {
		t.Fatalf("%d joins, the first has no review_session\n%s", len(joins), r.out)
	}
	seen := joins[0].seen
	if seen.commit != head {
		t.Errorf("review_session.commit is %q, want the checkout's commit %s", seen.commit, head)
	}
	if !seen.hasDiff || !strings.Contains(seen.diff, "+++ b/earlier.txt") {
		t.Errorf("review_session.diff is %q, want the change from the base %s", seen.diff, p.first)
	}
	if seen.hasSuite || seen.hasBuild || seen.hasPrevious {
		t.Errorf("review_session has suite_result %v, build_report %v and previous_findings %v, want none", seen.hasSuite, seen.hasBuild, seen.hasPrevious)
	}
	if !slices.Contains(seen.copyFiles, "earlier.txt") || seen.copyHasGit {
		t.Errorf("the copy has the files %v, with a git folder %v, want the commit's files only", seen.copyFiles, seen.copyHasGit)
	}
	var writable []string
	copyReadOnly := false
	for _, m := range joins[0].spec.Mounts {
		if !m.ReadOnly {
			writable = append(writable, m.Path)
		}
		if m.Path == seen.commitFolder && m.ReadOnly && m.Target == "" {
			copyReadOnly = true
		}
	}
	if !copyReadOnly || slices.Contains(writable, seen.commitFolder) {
		t.Errorf("the mounts %+v have no read-only mount of the copy %s", joins[0].spec.Mounts, seen.commitFolder)
	}
	if !slices.Contains(writable, seen.reports) || !slices.Contains(writable, joins[0].spec.Dir) {
		t.Errorf("the writable mounts are %v, want the reports folder %s and the checkout %s", writable, seen.reports, joins[0].spec.Dir)
	}
}

// TestAVerifySessionGetsThePreviousFindingsOfItsStageOnly is a Verify run whose work item
// has findings of the reviewer code-reviewer in the stage of the session and in another
// stage, and a finding of another source: previous_findings has the open findings of the
// reviewer in the session's stage, in the order of the GET, and no other. With 499
// findings listed, the file has all 499 and the run goes on.
func TestAVerifySessionGetsThePreviousFindingsOfItsStageOnly(t *testing.T) {
	read := func(t *testing.T, got contracts.Findings) []string {
		t.Helper()
		l := &ledger{got: got}
		r, joins, _ := verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
			verifyWork(t, p, true)
		}})
		r.wantsClose(t, event.StateSucceeded, 0)
		if len(joins) != 1 || joins[0].seen == nil || !joins[0].seen.hasPrevious {
			t.Fatalf("the Verify session has no previous_findings\n%s", r.out)
		}
		var file struct {
			Round    int `json:"round"`
			Findings []struct{ ID string }
		}
		if err := json.Unmarshal([]byte(joins[0].seen.previous), &file); err != nil || file.Round != 1 {
			t.Fatalf("previous_findings is %s (%v), want the file of round 1", joins[0].seen.previous, err)
		}
		var ids []string
		for _, f := range file.Findings {
			ids = append(ids, f.ID)
		}
		return ids
	}
	t.Run("the stage of the session only", func(t *testing.T) {
		elsewhere := reviewerFinding("f2")
		elsewhere.Stage = "b"
		check := reviewerFinding("f3")
		check.Source = contracts.FindingSource{Kind: "check", Name: "suite"}
		got := read(t, contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1"), elsewhere, check, reviewerFinding("f4")}})
		if !slices.Equal(got, []string{"f1", "f4"}) {
			t.Errorf("previous_findings has %v, want f1 and f4", got)
		}
	})
	t.Run("499 findings listed", func(t *testing.T) {
		var all []contracts.Finding
		var want []string
		for i := 1; i < 500; i++ {
			id := fmt.Sprintf("f%d", i)
			all, want = append(all, reviewerFinding(id)), append(want, id)
		}
		if got := read(t, contracts.Findings{Findings: all}); !slices.Equal(got, want) {
			t.Errorf("previous_findings has %d findings, want the 499 that the GET lists, in order", len(got))
		}
	})
}

// TestAVerifyRunStopsAtAGetOfFiveHundredFindings is a GET that lists 500 findings, the
// most it lists, at the two places where a Verify session reads the findings to decide
// something: before the session starts, and where its round is posted. Each stops the run
// with the cause on line 1, and the round is never posted.
func TestAVerifyRunStopsAtAGetOfFiveHundredFindings(t *testing.T) {
	const line = "The GET of the findings lists 500, its limit, so "
	t.Run("before the session starts", func(t *testing.T) {
		l := &ledger{}
		r, joins, _ := verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
			verifyWork(t, p, true)
			l.got = fullList()
		}})
		r.wantsClose(t, event.StateFailed, 1)
		wants(t, r.out, line+"the review did not start")
		if len(joins) != 0 || len(l.posts) != 0 {
			t.Errorf("%d sessions joined and %d POSTs were sent, want none", len(joins), len(l.posts))
		}
	})
	t.Run("where the round is posted", func(t *testing.T) {
		l := &ledger{}
		r, joins, _ := verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
			verifyWork(t, p, true)
		}, review: func(p *pushRepo, seen *reviewSeen) {
			writeReport(t, seen, "code-reviewer", closesF1)
			l.mu.Lock()
			l.got = fullList()
			l.mu.Unlock()
		}})
		r.wantsClose(t, event.StateFailed, 1)
		wants(t, r.out, line+"the review did not end")
		if len(joins) != 1 || len(l.posts) != 0 {
			t.Errorf("%d sessions joined and %d POSTs were sent, want 1 and none", len(joins), len(l.posts))
		}
	})
}

// TestAVerifySessionsSettingsGetTheReportsRulesAndNothingElse is a Verify run and a write
// run on the same harness: the Verify session's Claude Code settings are the write
// session's with a Read and an Edit rule for the reports folder in allow and the folder in
// additionalDirectories, and nothing else, and the write session's have no rule for a
// reports folder.
func TestAVerifySessionsSettingsGetTheReportsRulesAndNothingElse(t *testing.T) {
	var writes, joins []reviewJoined
	// Each run changes the test's working folder, which a subtest restores.
	t.Run("write run", func(t *testing.T) {
		_, writes, _ = reviewRunWith(t, reviewCfg{})
	})
	t.Run("Verify run", func(t *testing.T) {
		l := &ledger{}
		_, joins, _ = verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
			verifyWork(t, p, true)
		}})
	})
	if len(writes) == 0 || len(joins) != 1 || joins[0].seen == nil {
		t.Fatalf("%d write sessions and %d Verify sessions, want some and 1 with review_session", len(writes), len(joins))
	}
	plain, verified := claudeSettings(t, writes[0].spec), claudeSettings(t, joins[0].spec)
	if plain == nil {
		t.Fatal("the write session has no Claude Code settings")
	}
	for _, rule := range allowOf(plain) {
		if strings.Contains(rule, "review-reports") {
			t.Errorf("the write session allows %s", rule)
		}
	}
	reports := joins[0].seen.reports
	rules := []string{"Read(/" + reports + "/**)", "Edit(/" + reports + "/**)"}
	// want is the write session's settings with the three entries added.
	want := plain
	permissions, _ := want["permissions"].(map[string]any)
	if permissions == nil {
		permissions = map[string]any{}
		want["permissions"] = permissions
	}
	for _, add := range []struct {
		key  string
		list []string
	}{{"allow", rules}, {"additionalDirectories", []string{reports}}} {
		have, _ := permissions[add.key].([]any)
		for _, v := range add.list {
			have = append(have, v)
		}
		permissions[add.key] = have
	}
	if !reflect.DeepEqual(verified, want) {
		t.Errorf("the Verify session's settings are %v, want the write session's with %v in allow and %s in additionalDirectories", verified, rules, reports)
	}
}

// hasObject reports whether the git folder of dir has the commit id.
func hasObject(dir, id string) bool {
	c := exec.Command("git", "-C", dir, "cat-file", "-e", id+"^{commit}")
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	return c.Run() == nil
}

// TestAVerifyRunFetchesTheBaseBeforeTheSetupAndTheSession is a Verify run whose work item
// stores a base that no branch of the run reaches, so only a fetch by its id brings it
// into the checkout: the checkout has the base when the setup starts and when the session
// joins, and not before the run fetched it. A fetch that comes after the setup fails the
// first check.
func TestAVerifyRunFetchesTheBaseBeforeTheSetupAndTheSession(t *testing.T) {
	l := &ledger{}
	var side string
	var setups, sessions int
	var atSetup, atSession bool
	cfg := reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		verifyWork(t, p, true)
		tree := proxygittest.Git(t, p.bare, "rev-parse", p.first+"^{tree}")
		side = proxygittest.Git(t, p.bare, "-c", "user.name=qory", "-c", "user.email=qory@localhost", "commit-tree", tree, "-p", p.first, "-m", "Side")
		proxygittest.Git(t, p.bare, "update-ref", "refs/heads/side", side)
		l.got = contracts.Findings{Bases: []contracts.FindingBase{{Target: ledgerTarget(), Base: side}}}
		p.setup = func(dir string) {
			setups++
			atSetup = hasObject(dir, side)
		}
	}, review: func(p *pushRepo, _ *reviewSeen) {
		sessions++
		atSession = hasObject(p.h.dir, side)
	}}
	r, _, _ := verifyRun(t, cfg)
	r.wantsClose(t, event.StateSucceeded, 0)
	if setups != 1 || sessions != 1 {
		t.Fatalf("%d setups and %d sessions, want 1 of each\n%s", setups, sessions, r.out)
	}
	if !atSetup {
		t.Errorf("the checkout lacks the base %s when the setup starts", side)
	}
	if !atSession {
		t.Errorf("the checkout lacks the base %s when the session joins", side)
	}
}

// TestAVerifyRunWithNoBaseStopsBeforeTheSession is a run whose stage's input is a
// pull-request version with another head than the checkout's commit, and whose work item
// has no stored base: the run closes failed with the cause, and no session joins.
func TestAVerifyRunWithNoBaseStopsBeforeTheSession(t *testing.T) {
	l := &ledger{}
	r, joins, _ := verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		verifyWork(t, p, false)
	}})
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "The run has no base commit for the diff, so the review did not start")
	if len(joins) != 0 {
		t.Errorf("%d sessions joined, want none", len(joins))
	}
}

// TestAVerifyRunWithNoRunAPIStopsAtTheReviewAndSetsNoCheck is a Verify run whose run API
// is gone once the compose ends, as for a run through a gateway with no control plane:
// the run stops before the session with the cause of the review, and sets no check of
// the run API, whose set Forager fails for such a run.
func TestAVerifyRunWithNoRunAPIStopsAtTheReviewAndSetsNoCheck(t *testing.T) {
	l := &ledger{}
	var mu sync.Mutex
	gone, sets := false, 0
	r, joins, _ := verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		verifyWork(t, p, true)
		p.onCall = func(call string) {
			mu.Lock()
			defer mu.Unlock()
			gone = gone || call == "stack proxy closed"
		}
		p.runAPIErr = func() error {
			mu.Lock()
			defer mu.Unlock()
			if gone {
				return errors.New("Open the run through a gateway with a control plane to reach the run API: this run has none, so it has no QORY_RUN_API.")
			}
			return nil
		}
		p.onSetCheck = func(runAPICheck) error {
			mu.Lock()
			defer mu.Unlock()
			sets++
			return errors.New("Open the run at a gateway with a control plane to check its run API: the run has no run API.")
		}
	}})
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "qory run: The run has no run API, so the review did not start\n")
	lacks(t, r.out, "push checks", "check its run API")
	if sets != 0 || len(joins) != 0 {
		t.Errorf("%d sets of the check and %d sessions, want none", sets, len(joins))
	}
}

// TestAVerifyRunTakesTheBaseThatQoryApiaryStoresBeforeTheBaseOfTheVersion is a run whose
// work item has a stored base for the target, the checkout's commit, and an input whose
// version has the start commit as its base: the diff starts at the stored base, so it is
// empty where the version's base would give the change of the commit, and no post sends a
// base.
func TestAVerifyRunTakesTheBaseThatQoryApiaryStoresBeforeTheBaseOfTheVersion(t *testing.T) {
	l := &ledger{}
	r, joins, _ := verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		head := verifyWork(t, p, true)
		l.got = contracts.Findings{Bases: []contracts.FindingBase{{Target: ledgerTarget(), Base: head}}}
	}})
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(joins) != 1 || joins[0].seen == nil {
		t.Fatalf("%d joins\n%s", len(joins), r.out)
	}
	if got := joins[0].seen.diff; strings.Contains(got, "earlier.txt") {
		t.Errorf("the diff is %q, want the change from the stored base, which has none", got)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, post := range l.posts {
		if len(post.Bases) != 0 {
			t.Errorf("a post sent the bases %+v, want none", post.Bases)
		}
	}
}

// passedBody is the body of a POST of the verdict passed.
const passedBody = `{"verdict":"passed"}`

// verifyRound is a run of one Verify session whose work item has the open blocking finding
// f1 of the stage, which the report of the reviewer code-reviewer closes. The session's
// reviewers write the report that makeReport makes from what the session sees, and the
// session then records the verdict passed: the check of the POST runs, and then the POST
// reaches a run API that returns the status 409 for passed while the ledger lists an open
// blocking finding. status is the status that the POST got. The documents of the run set
// shared_run when shared is true.
func verifyRound(t *testing.T, makeReport func(*reviewSeen) string, shared bool) (l *ledger, status int, r pushed) {
	t.Helper()
	l = &ledger{open: 1}
	// closing is the ledger's post, which lists no open blocking finding once it stored the
	// close of f1.
	closing := func(ctx context.Context, body []byte) ([]byte, error) {
		var parsed contracts.FindingsPost
		if json.Unmarshal(body, &parsed) == nil {
			for _, e := range parsed.Events {
				if e.Kind == "close_fixed" && e.ID == "f1" {
					l.mu.Lock()
					l.open = 0
					l.mu.Unlock()
				}
			}
		}
		return l.post(ctx, body)
	}
	var mu sync.Mutex
	var check func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)
	var refusal *session.RunAPIRefusal
	var checkErr error
	cfg := reviewCfg{findings: l.get, postFindings: closing,
		onSetCheck: func(c func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
			mu.Lock()
			defer mu.Unlock()
			check = c
			return nil
		},
		setup: func(p *pushRepo) {
			verifyWork(t, p, true)
			change := p.change
			p.change = func(d map[string]any) {
				change(d)
				d["shared_run"] = shared
			}
			l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
			p.api.refuse = func(body string) string {
				l.mu.Lock()
				defer l.mu.Unlock()
				if verdictOf(t, body) == "passed" && l.open > 0 {
					return "open_blocking_findings"
				}
				return ""
			}
		},
		review: func(p *pushRepo, seen *reviewSeen) {
			writeReport(t, seen, "code-reviewer", makeReport(seen))
			mu.Lock()
			c := check
			mu.Unlock()
			if c == nil {
				t.Error("the run set no check of the run API")
				return
			}
			refusal, checkErr = c(context.Background(), session.RunAPIRequest{Path: "/verdict", Body: []byte(passedBody)})
			if refusal != nil || checkErr != nil {
				return
			}
			resp, err := http.Post(p.api.base()+"/verdict", "application/json", strings.NewReader(passedBody))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			status = resp.StatusCode
		}}
	r, _, _ = verifyRun(t, cfg)
	if refusal != nil || checkErr != nil {
		t.Fatalf("the check of the verdict returned the refusal %+v and the error %v", refusal, checkErr)
	}
	return l, status, r
}

// closesF1 is the report of the reviewer code-reviewer that closes the finding f1.
const closesF1 = `{"reviewer":"code-reviewer","findings":[],"closes":[{"id":"f1","state":"fixed","reason":"Deleted x."}]}`

// closesPrevious is the report of the reviewer code-reviewer that closes the first finding
// of the session's previous_findings, with the id that the file has.
func closesPrevious(t *testing.T) func(*reviewSeen) string {
	return func(seen *reviewSeen) string {
		var file struct{ Findings []struct{ ID string } }
		if err := json.Unmarshal([]byte(seen.previous), &file); err != nil || len(file.Findings) == 0 {
			t.Errorf("previous_findings is %q (%v), want a file with a finding", seen.previous, err)
			return `{"reviewer":"code-reviewer","findings":[]}`
		}
		return fmt.Sprintf(`{"reviewer":"code-reviewer","findings":[],"closes":[{"id":%q,"state":"fixed","reason":"Deleted x."}]}`, file.Findings[0].ID)
	}
}

// TestAVerifyRoundIsPostedBeforeTheVerdictAndOnce is a Verify session whose reviewers
// close the open blocking finding f1, with the id that previous_findings has, and whose
// session then records passed: qory posts the round when it checks the POST of the
// verdict, so Qory Apiary stores the close before it reads the verdict, and the POST gets
// 201 where it gets 409 with the finding open. The end of the session posts nothing more,
// and qory sends no verdict itself. A run whose documents do not set shared_run checks
// the verdict the same way.
func TestAVerifyRoundIsPostedBeforeTheVerdictAndOnce(t *testing.T) {
	for _, shared := range []bool{true, false} {
		t.Run(fmt.Sprintf("shared_run %v", shared), func(t *testing.T) {
			l, status, r := verifyRound(t, closesPrevious(t), shared)
			r.wantsClose(t, event.StateSucceeded, 0)
			if status != http.StatusCreated {
				t.Errorf("the verdict passed got the status %d, want 201\n%s", status, r.out)
			}
			if got := kindsOf(l.events()); !slices.Equal(got, []string{"close_fixed code-reviewer"}) {
				t.Errorf("the events are %v, want one close of f1", got)
			}
			l.mu.Lock()
			defer l.mu.Unlock()
			if len(l.posts) != 1 {
				t.Errorf("%d posts, want 1", len(l.posts))
			}
			for _, post := range l.posts {
				if len(post.Bases) != 0 {
					t.Errorf("the post has the bases %+v, want none", post.Bases)
				}
			}
		})
	}
}

// TestAVerifyRoundIsPostedAtTheEndOfASessionThatSendsNoVerdict is a Verify session that
// sends no verdict: its round is posted once, after it, with the events of its reports; a
// session with no report raises the finding of the missing report; a session that
// recorded escalate or changes-requested by a push and left no report posts nothing; and
// one that recorded passed, or a verdict that qory cannot read, raises the finding.
func TestAVerifyRoundIsPostedAtTheEndOfASessionThatSendsNoVerdict(t *testing.T) {
	for _, c := range []struct {
		name, report, recorded string
		want                   []string
	}{
		{"a report that closes f1", closesF1, "", []string{"close_fixed code-reviewer"}},
		{"no report", "", "", []string{"raise report-code-reviewer"}},
		{"escalate recorded and no report", "", `{"verdict":"escalate"}`, nil},
		{"changes-requested recorded and no report", "", `{"verdict":"changes-requested"}`, nil},
		{"passed recorded and no report", "", passedBody, []string{"raise report-code-reviewer"}},
		{"a verdict that is no JSON recorded and no report", "", `nope`, []string{"raise report-code-reviewer"}},
		{"a verdict with no value recorded and no report", "", `{"points":[]}`, []string{"raise report-code-reviewer"}},
		{"passed recorded and a report", closesF1, passedBody, []string{"close_fixed code-reviewer"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{open: 1}
			cfg := reviewCfg{findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
				verifyWork(t, p, true)
				l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
			}, review: func(p *pushRepo, seen *reviewSeen) {
				if c.report != "" {
					writeReport(t, seen, "code-reviewer", c.report)
				}
				if c.recorded != "" {
					p.api.addVersion("verdict", "verdict.json", []byte(c.recorded))
				}
			}}
			r, _, p := verifyRun(t, cfg)
			r.wantsClose(t, event.StateSucceeded, 0)
			if got := kindsOf(l.events()); !slices.Equal(got, c.want) {
				t.Errorf("the events are %v, want %v", got, c.want)
			}
			if got := p.api.verdicts; len(got) != 0 {
				t.Errorf("qory sent the verdicts %v, want none", got)
			}
		})
	}
}

// TestAVerifyRunReadsTheBaseOfTheInputsThatReferenceItsCommit is a run with no stored
// base whose stage has pull-request versions as inputs: two with the checkout's commit as
// head and the same base give that base; two with other bases, a version that
// references a commit of another collection, and a base that no run started at stop the
// run with the cause.
func TestAVerifyRunReadsTheBaseOfTheInputsThatReferenceItsCommit(t *testing.T) {
	other := strings.Repeat("a", 40)
	// The pull-request type of the software factory requires the members that qory fills.
	process := strings.Replace(pullRequestProcess, "required: [target, title]", "required: [target, title, commits, head, base]", 1)
	for _, c := range []struct {
		name string
		// files are the pull-request files, from the head and the start commit. earlier is
		// a commit that a run before the versions' run pushed, "" for none.
		files   func(head, start string) [][]byte
		earlier string
		want    string
	}{
		{"two versions with the same base", func(head, start string) [][]byte {
			return [][]byte{pullRequestOf(head, start), pullRequestOf(head, start)}
		}, "", ""},
		{"two versions with other bases", func(head, start string) [][]byte {
			return [][]byte{pullRequestOf(head, start), pullRequestOf(head, other)}
		}, other, "the inputs of the stage have 2 versions with the head"},
		{"a base that no run started at", func(head, start string) [][]byte {
			return [][]byte{pullRequestOf(head, other)}
		}, "", "The base of the pull request is not the start of its run, so the review did not start"},
		{"a base of another collection", func(head, start string) [][]byte {
			file := string(pullRequestOf(head, start))
			return [][]byte{[]byte(strings.Replace(file, `"base":{"collection":"R_1001"`, `"base":{"collection":"R_1002"`, 1))}
		}, "", "no input of the stage has a version with a base and the head"},
		{"a head in capitals", func(head, start string) [][]byte {
			return [][]byte{pullRequestOf(strings.ToUpper(head), start)}
		}, "", "no input of the stage has a version with a base and the head"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := &ledger{}
			r, joins, _ := verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, files: map[string]string{"modules/hello/process.yaml": process}, setup: func(p *pushRepo) {
				head := verifyWork(t, p, true)
				p.api.files, p.api.kinds = nil, nil
				if c.earlier != "" {
					// The run before pushed the commit, and the versions' run pushed the head.
					p.api.pushedCommits = fmt.Sprintf(`[{"system":%q,"path":"acme/site","commit":%q,"run_id":"run_before","cancelled":false,"at":"2026-11-16T08:00:00Z"},`, proxygittest.Host, c.earlier) + strings.TrimPrefix(p.api.pushedCommits, "[")
				}
				inputs := []string{}
				for i, file := range c.files(head, p.first) {
					p.api.addVersionBy("run_old", "pull-request", "pull-request.json", file)
					inputs = append(inputs, fmt.Sprintf(`{"type":"pull-request","required":false,"version":{"id":"av_%d","number":%d}}`, i+1, i+1))
				}
				p.api.inputs = "[" + strings.Join(inputs, ",") + "]"
				if c.earlier != "" {
					// The versions are of the stage st_b, as a Build run writes them, and the
					// run is in st_a. A version of the run before confirms the commit that it
					// pushed.
					p.api.versionStage = "st_b"
					p.api.addDeliveredBy("run_before", "pull-request", "pull-request.json", pullRequestOf(c.earlier, p.first))
				}
			}})
			if c.want == "" {
				r.wantsClose(t, event.StateSucceeded, 0)
				if len(joins) != 1 || !strings.Contains(joins[0].seen.diff, "+++ b/earlier.txt") {
					t.Errorf("%d joins, the diff is not the change from the start commit\n%s", len(joins), r.out)
				}
				return
			}
			r.wantsClose(t, event.StateFailed, 1)
			wants(t, r.out, c.want)
			if len(joins) != 0 {
				t.Errorf("%d sessions joined, want none", len(joins))
			}
		})
	}
}

// TestAVerifyRoundThatCannotBePostedStopsTheVerdictAndTheRun is a Verify session whose
// round fails to post: the check of its POST of the verdict returns the error, which
// stops the verdict, and prints the status line; the end of the session does not post
// again, and the run closes failed with the cause of the post.
func TestAVerifyRoundThatCannotBePostedStopsTheVerdictAndTheRun(t *testing.T) {
	l := &ledger{}
	posts := 0
	var mu sync.Mutex
	var check func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)
	var refusal *session.RunAPIRefusal
	var checkErr error
	cfg := reviewCfg{findings: l.get, postFindings: func(context.Context, []byte) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		posts++
		return nil, fmt.Errorf("run closed: %w", session.ErrRunClosed)
	}, onSetCheck: func(c func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
		mu.Lock()
		defer mu.Unlock()
		check = c
		return nil
	}, setup: func(p *pushRepo) {
		verifyWork(t, p, true)
	}, review: func(p *pushRepo, seen *reviewSeen) {
		writeReport(t, seen, "code-reviewer", closesF1)
		mu.Lock()
		c := check
		mu.Unlock()
		if c == nil {
			t.Error("the run set no check of the run API")
			return
		}
		refusal, checkErr = c(context.Background(), session.RunAPIRequest{Path: "/verdict", Body: []byte(passedBody)})
	}}
	r, _, _ := verifyRun(t, cfg)
	r.wantsClose(t, event.StateFailed, 1)
	if refusal != nil || checkErr == nil {
		t.Errorf("the check returned the refusal %+v and the error %v, want the error alone", refusal, checkErr)
	}
	wants(t, r.out, "qory run: the round was not posted, so the verdict was not stored (503 push_check_unavailable)\n")
	wants(t, r.out, "Findings batch 1 of 1 is not stored")
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Errorf("%d posts, want 1", posts)
	}
}

// TestASecondSessionOfAVerifyRunStopsTheRunBeforeItStarts is a Verify run whose next
// session starts a workflow that starts reviewers at write-no-push, here with a name of
// 15 characters: qory.review_session is for session 1 only, so the run closes failed with
// the cause before session 2 starts, and no verdict is recorded. Line 1 has the session
// and at most 100 characters, and line 2 has the workflow.
func TestASecondSessionOfAVerifyRunStopsTheRunBeforeItStarts(t *testing.T) {
	l := &ledger{}
	cfg := reviewCfg{findings: l.get, postFindings: l.post, verify: true, noSuite: true, writes: []int{},
		files:     map[string]string{"modules/hello/skills/workflow-verify-a-ticket/SKILL.md": "---\nname: workflow-verify-a-ticket\ndescription: Verify.\n---\n\nVerify.\n"},
		workflows: map[string]any{"verify-a-ticket": map[string]any{"access": "write-no-push", "review_group": "reviewers"}},
		setup: func(p *pushRepo) {
			verifyWork(t, p, true)
		}, next: func(p *pushRepo, n int) {
			// A session that reached the run API would record its verdict.
			if n == 2 {
				if resp, err := http.Post(p.api.base()+"/verdict", "application/json", strings.NewReader(passedBody)); err == nil {
					resp.Body.Close()
				}
			}
		}}
	r, joins, p := reviewRunWith(t, cfg, answerFor(2, "verify-a-ticket"))
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "qory run: Session 2 starts reviewers, and only session 1 of a run does, so the review did not start\n"+
		"  The workflow verify-a-ticket has access write-no-push and a review_group, and only session 1 of a run has qory.review_session")
	if len(joins) != 1 {
		t.Errorf("%d sessions joined, want 1 (session 2 stops before it starts)", len(joins))
	}
	if len(p.api.verdicts) != 0 {
		t.Errorf("the run recorded the verdicts %v, want none", p.api.verdicts)
	}
}

// verifyCheck is a Verify run whose session runs agent. The agent gets the ledger, what
// the session sees, with the folder of its checkout, and send, which sends one POST to the
// run's check of the run API, as the gateway does, and returns the error of the check; a
// refusal fails the test. The ledger lists the open blocking finding f1 of the reviewer
// code-reviewer.
func verifyCheck(t *testing.T, agent func(l *ledger, seen *reviewSeen, send func(path string, body []byte) error)) (*ledger, pushed, *pushRepo) {
	t.Helper()
	return verifyCheckRefusing(t, func(l *ledger, seen *reviewSeen, send func(path string, body []byte) (*session.RunAPIRefusal, error)) {
		agent(l, seen, func(path string, body []byte) error {
			refusal, err := send(path, body)
			if refusal != nil {
				t.Errorf("the check of %s refused the POST: %+v", path, refusal)
			}
			return err
		})
	})
}

// verifyCheckRefusing is [verifyCheck] whose send returns the refusal of the check too.
func verifyCheckRefusing(t *testing.T, agent func(l *ledger, seen *reviewSeen, send func(path string, body []byte) (*session.RunAPIRefusal, error))) (*ledger, pushed, *pushRepo) {
	t.Helper()
	l := &ledger{open: 1}
	var mu sync.Mutex
	var check func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)
	cfg := reviewCfg{findings: l.get, postFindings: l.post,
		onSetCheck: func(c func(context.Context, session.RunAPIRequest) (*session.RunAPIRefusal, error)) error {
			mu.Lock()
			defer mu.Unlock()
			check = c
			return nil
		},
		setup: func(p *pushRepo) {
			verifyWork(t, p, true)
			l.got = contracts.Findings{Findings: []contracts.Finding{reviewerFinding("f1")}}
		},
		review: func(p *pushRepo, seen *reviewSeen) {
			mu.Lock()
			c := check
			mu.Unlock()
			if c == nil {
				t.Error("the run set no check of the run API")
				return
			}
			agent(l, seen, func(path string, body []byte) (*session.RunAPIRefusal, error) {
				return c(context.Background(), session.RunAPIRequest{Path: path, Body: body})
			})
		}}
	r, _, p := verifyRun(t, cfg)
	return l, r, p
}

// verdictPush is the body of a POST of /artifacts that pushes the file of a verdict in the
// artifact type typ.
func verdictPush(t *testing.T, typ, verdict string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"type": typ, "name": "main", "files": []map[string]any{
		{"filename": "verdict.json", "content_type": "application/json", "content_base64": base64.StdEncoding.EncodeToString([]byte(verdict))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// moveHead makes a commit in the checkout dir, so its HEAD is another commit.
func moveHead(t *testing.T, dir string) {
	t.Helper()
	proxygittest.Git(t, dir, "-c", "user.name=Agent", "-c", "user.email=agent@example.com", "commit", "--allow-empty", "--quiet", "-m", "Move HEAD")
}

// TestAVerifyVerdictIsNotCheckedThroughWhileTheCheckoutIsAtAnotherCommit is a Verify
// session whose agent moves the HEAD of the checkout after the review read its commit: the
// check of its POST of the verdict returns an error, so the POST goes no further, posts no
// round and stores no verdict, and prints the status line. The check reads HEAD at each
// POST: once HEAD is at the commit again, the round is posted and the POST passes.
func TestAVerifyVerdictIsNotCheckedThroughWhileTheCheckoutIsAtAnotherCommit(t *testing.T) {
	var moved, restored error
	var postsWhileMoved int
	l, r, p := verifyCheck(t, func(l *ledger, seen *reviewSeen, send func(string, []byte) error) {
		writeReport(t, seen, "code-reviewer", closesF1)
		was := proxygittest.Git(t, seen.checkout, "rev-parse", "HEAD")
		moveHead(t, seen.checkout)
		moved = send("/verdict", []byte(passedBody))
		l.mu.Lock()
		postsWhileMoved = len(l.posts)
		l.mu.Unlock()
		proxygittest.Git(t, seen.checkout, "update-ref", "HEAD", was)
		restored = send("/verdict", []byte(passedBody))
	})
	r.wantsClose(t, event.StateSucceeded, 0)
	if moved == nil || !strings.Contains(moved.Error(), "The checkout's HEAD is not the commit the review read, so the review did not end\n  The checkout's HEAD is ") {
		t.Errorf("the check with HEAD moved returned %v, want the stop with both commits", moved)
	}
	if postsWhileMoved != 0 {
		t.Errorf("%d POSTs of findings were sent while HEAD was moved, want none", postsWhileMoved)
	}
	wants(t, r.out, "qory run: the checkout's HEAD moved, so the verdict was not stored (503 push_check_unavailable)\n")
	if restored != nil {
		t.Errorf("the check with HEAD at the commit again returned %v, want none", restored)
	}
	if got := kindsOf(l.events()); !slices.Equal(got, []string{"close_fixed code-reviewer"}) {
		t.Errorf("the events are %v, want one close of f1", got)
	}
	if len(p.api.verdicts) != 0 {
		t.Errorf("the run recorded the verdicts %v, want none", p.api.verdicts)
	}
}

// TestAVerifyRoundIsNotPostedAtTheEndWhileTheCheckoutIsAtAnotherCommit is a Verify session
// that leaves a report, sends no verdict and moves HEAD before it ends, and one that also
// tries its verdict while HEAD is moved and never moves it back: the end of the session
// reads HEAD, stops the run with the cause on line 1, and posts nothing and stores no
// verdict.
func TestAVerifyRoundIsNotPostedAtTheEndWhileTheCheckoutIsAtAnotherCommit(t *testing.T) {
	for _, c := range []struct {
		name     string
		verdicts int
	}{
		{"a session that sends no verdict", 0},
		{"an agent that moves HEAD, tries its verdict twice and does not restore it", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			var errs []error
			l, r, p := verifyCheck(t, func(l *ledger, seen *reviewSeen, send func(string, []byte) error) {
				writeReport(t, seen, "code-reviewer", closesF1)
				moveHead(t, seen.checkout)
				for range c.verdicts {
					errs = append(errs, send("/verdict", []byte(passedBody)))
				}
			})
			r.wantsClose(t, event.StateFailed, 1)
			wants(t, r.out, "qory run: The checkout's HEAD is not the commit the review read, so the review did not end\n  The checkout's HEAD is ")
			for _, err := range errs {
				if err == nil {
					t.Error("a check with HEAD moved returned no error")
				}
			}
			if len(l.posts) != 0 || len(p.api.verdicts) != 0 {
				t.Errorf("%d POSTs of findings and the verdicts %v, want none", len(l.posts), p.api.verdicts)
			}
		})
	}
}

// TestAVerifyRoundIsPostedBeforeAVerdictOfAnyKindOfPost is a Verify session with a report
// that closes f1: a POST of /verdict, and a push of the artifact type verdict, post the
// round first, once. A push of another type, and a POST of changes-requested with no
// report, post nothing.
func TestAVerifyRoundIsPostedBeforeAVerdictOfAnyKindOfPost(t *testing.T) {
	for _, c := range []struct {
		name, path string
		body       func(t *testing.T) []byte
		report     bool
		want       []string
	}{
		{"a POST of the verdict", "/verdict", func(*testing.T) []byte { return []byte(passedBody) }, true, []string{"close_fixed code-reviewer"}},
		{"a push of the type verdict", "/artifacts", func(t *testing.T) []byte { return verdictPush(t, "verdict", passedBody) }, true, []string{"close_fixed code-reviewer"}},
		{"a push of another type", "/artifacts", func(t *testing.T) []byte { return verdictPush(t, "screenshot", passedBody) }, true, nil},
		{"a POST of changes-requested with no report", "/verdict", func(*testing.T) []byte { return []byte(`{"verdict":"changes-requested"}`) }, false, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var after []string
			l, r, _ := verifyCheck(t, func(l *ledger, seen *reviewSeen, send func(string, []byte) error) {
				if c.report {
					writeReport(t, seen, "code-reviewer", closesF1)
				}
				if err := send(c.path, c.body(t)); err != nil {
					t.Errorf("the check returned %v", err)
				}
				after = kindsOf(l.events())
			})
			if !slices.Equal(after, c.want) {
				t.Errorf("the events after the check are %v, want %v", after, c.want)
			}
			if c.want != nil {
				// The end of the session posts nothing more.
				r.wantsClose(t, event.StateSucceeded, 0)
				if len(l.posts) != 1 {
					t.Errorf("%d POSTs, want 1", len(l.posts))
				}
			}
		})
	}
}

// TestAVerifyRoundIsNotPostedForAVerdictThatQoryCannotReadInOneWay is a Verify session
// with a report that closes f1 whose POST of a verdict has a member twice, a member in
// another case of letters, or a push with its type twice: Qory Apiary keeps the last of
// a member that a body has twice, so the POST gets the 422 of the push check, and qory
// posts no round and stores no verdict. A verdict that reads in one way then posts the
// round.
func TestAVerifyRoundIsNotPostedForAVerdictThatQoryCannotReadInOneWay(t *testing.T) {
	file := base64.StdEncoding.EncodeToString([]byte(passedBody))
	for _, c := range []struct{ name, path, body string }{
		{"a verdict twice", "/verdict", `{"verdict":"passed","verdict":"passed"}`},
		{"a verdict twice with another last", "/verdict", `{"verdict":"changes-requested","verdict":"passed"}`},
		{"a member in another case", "/verdict", `{"Verdict":"passed"}`},
		{"a push with its type twice", "/artifacts", `{"type":"note","type":"verdict","name":"main","files":[{"filename":"verdict.json","content_base64":"` + file + `"}]}`},
		{"a push with its type twice, the other order", "/artifacts", `{"type":"verdict","type":"note","name":"main","files":[{"filename":"verdict.json","content_base64":"` + file + `"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var refusal *session.RunAPIRefusal
			var err, after error
			var postsBefore int
			l, r, p := verifyCheckRefusing(t, func(l *ledger, seen *reviewSeen, send func(string, []byte) (*session.RunAPIRefusal, error)) {
				writeReport(t, seen, "code-reviewer", closesF1)
				refusal, err = send(c.path, []byte(c.body))
				l.mu.Lock()
				postsBefore = len(l.posts)
				l.mu.Unlock()
				var again *session.RunAPIRefusal
				if again, after = send("/verdict", []byte(passedBody)); again != nil {
					t.Errorf("the verdict that reads in one way got the refusal %+v", again)
				}
			})
			r.wantsClose(t, event.StateSucceeded, 0)
			if refusal == nil || err != nil {
				t.Errorf("the check returned the refusal %+v and the error %v, want the refusal alone", refusal, err)
			}
			if postsBefore != 0 {
				t.Errorf("%d POSTs of findings were sent for the POST, want none", postsBefore)
			}
			if after != nil {
				t.Errorf("the verdict that reads in one way returned %v", after)
			}
			if len(l.posts) != 1 || len(p.api.verdicts) != 0 {
				t.Errorf("%d POSTs of findings and the verdicts %v, want 1 and none", len(l.posts), p.api.verdicts)
			}
		})
	}
}

// TestAVerifyRunStopsEveryVerdictFromItsSetupCommand is a Verify run of one session whose
// workflow judges, and whose documents do not set shared_run: a verdict POSTed while the
// setup runs, before the session joins, gets 422, escalate too, since no session runs yet
// and the check has no round to post. The session then records any verdict.
func TestAVerifyRunStopsEveryVerdictFromItsSetupCommand(t *testing.T) {
	var v verdictProbe
	l := &ledger{}
	var inSession verdictTry
	r, _, _ := verifyRun(t, reviewCfg{findings: l.get, postFindings: l.post, onSetCheck: v.setCheck,
		setup: func(p *pushRepo) {
			verifyWork(t, p, true)
			change := p.change
			p.change = func(d map[string]any) {
				change(d)
				d["shared_run"] = false
			}
		},
		review: func(*pushRepo, *reviewSeen) {
			inSession = v.try()
		}})
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(v.tries) < 2 {
		t.Fatalf("%d tries, want one from the setup and one from the session\n%s", len(v.tries), r.out)
	}
	wantTry(t, "setup", v.tries[0], allRefused)
	wantTry(t, "session", inSession, free)
}

// TestAVerifyVerdictAfterTheRoundIsStoppedWhileTheCheckoutIsAtAnotherCommit is a Verify
// session whose verdict at the commit posts the round, and whose agent then moves HEAD: a
// second POST of /verdict, and a push of the type verdict, return the error of the HEAD
// and post nothing more, and the round stays the one that was posted.
func TestAVerifyVerdictAfterTheRoundIsStoppedWhileTheCheckoutIsAtAnotherCommit(t *testing.T) {
	var first, second, third error
	var postsAfterFirst int
	l, r, p := verifyCheck(t, func(l *ledger, seen *reviewSeen, send func(string, []byte) error) {
		writeReport(t, seen, "code-reviewer", closesF1)
		first = send("/verdict", []byte(passedBody))
		l.mu.Lock()
		postsAfterFirst = len(l.posts)
		l.mu.Unlock()
		moveHead(t, seen.checkout)
		second = send("/verdict", []byte(passedBody))
		third = send("/artifacts", verdictPush(t, "verdict", passedBody))
	})
	r.wantsClose(t, event.StateSucceeded, 0)
	if first != nil || postsAfterFirst != 1 {
		t.Errorf("the verdict at the commit returned %v after %d POSTs of findings, want none after 1", first, postsAfterFirst)
	}
	for name, err := range map[string]error{"the second POST of /verdict": second, "the push of the type verdict": third} {
		if err == nil || !strings.Contains(err.Error(), "The checkout's HEAD is not the commit the review read, so the review did not end\n  The checkout's HEAD is ") {
			t.Errorf("%s with HEAD moved returned %v, want the stop with both commits", name, err)
		}
	}
	if len(l.posts) != 1 || len(p.api.verdicts) != 0 {
		t.Errorf("%d POSTs of findings and the verdicts %v, want 1 and none", len(l.posts), p.api.verdicts)
	}
}

// TestAVerifyRunWhoseStackComesFromTheCheckoutGetsNoSecondWritableMount is a run whose
// workflow has access write-no-push and a review group in the checkout's qory.yaml, with
// no harness in the run documents: the change may have written that file, so the session
// gets no qory.review_session and no reports folder, and its checkout is the only
// writable mount.
func TestAVerifyRunWhoseStackComesFromTheCheckoutGetsNoSecondWritableMount(t *testing.T) {
	l := &ledger{}
	r, joins, _ := verifyRun(t, reviewCfg{checkoutStack: true, findings: l.get, postFindings: l.post, setup: func(p *pushRepo) {
		verifyWork(t, p, true)
	}})
	if len(joins) != 1 {
		t.Fatalf("%d joins, want 1\n%s", len(joins), r.out)
	}
	if joins[0].seen != nil || joins[0].qory["review_session"] != nil {
		t.Errorf("the session has qory.review_session %v, want none", joins[0].qory["review_session"])
	}
	var writable []string
	for _, m := range joins[0].spec.Mounts {
		if !m.ReadOnly {
			writable = append(writable, m.Path)
		}
	}
	if !slices.Equal(writable, []string{joins[0].spec.Dir}) {
		t.Errorf("the writable mounts are %v, want the checkout %s alone", writable, joins[0].spec.Dir)
	}
}
