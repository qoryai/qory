package cmd_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// recordBranch is the ref of the work item's branch, as the failure line names it.
const recordBranch = "qory/" + workItem

// failsRecord makes the reports of pushed commits from call from on get the status and
// code, and Retry-After retry when it is not empty.
func failsRecord(p *pushRepo, from, status int, code, retry string) {
	p.api.record = func(n int, w http.ResponseWriter) bool {
		if n < from {
			return false
		}
		failing(w, status, code, retry)
		return true
	}
}

// TestEachSessionThatPushesIsReported is a write run of two sessions that each commit: after
// session 1's push Qory Apiary has its head, before session 2 starts, and after session 2's push
// it has both, in push order, with the body as the contract has it.
func TestEachSessionThatPushesIsReported(t *testing.T) {
	p := newPushRepo(t, "write")
	var dir, afterFirst string
	var reportsBeforeSecond int
	t.Cleanup(cmd.SetLaterSessions(func() {
		afterFirst = p.branchHead(t)
		reportsBeforeSecond, _ = p.api.reports()
		writeFile(t, filepath.Join(dir, "second.txt"), "second\n")
		commitAll(t, dir, "Add a second file")
	}))
	r := p.run(t, func(d string) {
		dir = d
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	head := p.branchHead(t)
	if afterFirst == "" || afterFirst == head {
		t.Fatalf("the branch is at %q after session 1 and %q after session 2", afterFirst, head)
	}
	if reportsBeforeSecond != 1 {
		t.Errorf("%d reports before session 2, want 1", reportsBeforeSecond)
	}
	calls, rows := p.api.reports()
	want := []recordedRow{
		{proxygittest.Host, "acme/site", afterFirst},
		{proxygittest.Host, "acme/site", head},
	}
	if calls != 2 || !slices.Equal(rows, want) {
		t.Errorf("%d reports with the rows %+v, want 2 with %+v", calls, rows, want)
	}
	wantBody := fmt.Sprintf(`{"repositories":[{"system":%q,"path":"acme/site","commit":%q}]}`, proxygittest.Host, afterFirst)
	if got := p.api.recorded[0]; got != wantBody {
		t.Errorf("the first report is %s, want %s", got, wantBody)
	}
}

// TestOnePushOfTwoRepositoriesIsOneReport is a run whose agent commits in the target and in a
// further repository: one report has both, the target first, with the heads on the branches.
func TestOnePushOfTwoRepositoriesIsOneReport(t *testing.T) {
	p := newPushRepo(t, "write")
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	libBare := filepath.Join(p.h.server.Root, "acme", "lib.git")
	proxygittest.Git(t, libBare, "config", "http.receivepack", "true")
	p.repositories = "acme/site,acme/lib"
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "site.txt"), "site\n")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "lib.txt"), "changed\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	calls, rows := p.api.reports()
	want := []recordedRow{
		{proxygittest.Host, "acme/site", p.branchHead(t)},
		{proxygittest.Host, "acme/lib", proxygittest.Git(t, libBare, "rev-parse", "refs/heads/"+recordBranch)},
	}
	if calls != 1 || !slices.Equal(rows, want) {
		t.Errorf("%d reports with the rows %+v, want 1 with %+v", calls, rows, want)
	}
}

// TestASessionThatPushedNothingMakesNoReport is a run with no commit in session 1 and one in
// a stand-in session 2, and a run whose session 2 commits nothing: a push that moved no
// repository sends no call.
func TestASessionThatPushedNothingMakesNoReport(t *testing.T) {
	t.Run("no commit at all", func(t *testing.T) {
		p := newPushRepo(t, "write")
		r := p.run(t, func(string) {}, nil)
		r.wantsClose(t, event.StateSucceeded, 0)
		if calls, _ := p.api.reports(); calls != 0 {
			t.Errorf("%d reports, want none", calls)
		}
	})
	t.Run("a second session with no commit", func(t *testing.T) {
		p := newPushRepo(t, "write")
		t.Cleanup(cmd.SetLaterSessions(func() {}))
		r := p.run(t, func(d string) {
			writeFile(t, filepath.Join(d, "first.txt"), "first\n")
			commitAll(t, d, "Add a first file")
		}, nil)
		r.wantsClose(t, event.StateSucceeded, 0)
		if calls, rows := p.api.reports(); calls != 1 || len(rows) != 1 {
			t.Errorf("%d reports and %d rows, want 1 and 1", calls, len(rows))
		}
	})
}

func commitFirst(t *testing.T) func(string) {
	return func(d string) {
		writeFile(t, filepath.Join(d, "first.txt"), "first\n")
		commitAll(t, d, "Add a first file")
	}
}

// TestAReportThatMayNotHaveArrivedIsSentAgain is a report that gets 503, 429, or no response
// before it is taken: each send has the same body, and the waits are 1 s, 2 s and 4 s, or
// Retry-After, at most 30 s.
func TestAReportThatMayNotHaveArrivedIsSentAgain(t *testing.T) {
	for name, c := range map[string]struct {
		answer func(n int, w http.ResponseWriter) bool
		waits  []time.Duration
	}{
		"503 twice": {func(n int, w http.ResponseWriter) bool {
			if n <= 2 {
				failing(w, http.StatusServiceUnavailable, "unavailable", "")
				return true
			}
			return false
		}, []time.Duration{time.Second, 2 * time.Second}},
		"three failures": {func(n int, w http.ResponseWriter) bool {
			if n <= 3 {
				failing(w, http.StatusBadGateway, "", "")
				return true
			}
			return false
		}, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}},
		"429 with Retry-After": {func(n int, w http.ResponseWriter) bool {
			if n == 1 {
				failing(w, http.StatusTooManyRequests, "rate_limited", "3")
				return true
			}
			return false
		}, []time.Duration{3 * time.Second}},
		"Retry-After over 30 s": {func(n int, w http.ResponseWriter) bool {
			if n == 1 {
				failing(w, http.StatusServiceUnavailable, "unavailable", "90")
				return true
			}
			return false
		}, []time.Duration{30 * time.Second}},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			p.api.record = c.answer
			r := p.run(t, commitFirst(t), nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if !slices.Equal(p.waits, c.waits) {
				t.Errorf("the waits are %v, want %v", p.waits, c.waits)
			}
			calls, rows := p.api.reports()
			if calls != len(c.waits)+1 || len(rows) != 1 {
				t.Errorf("%d reports and %d rows, want %d and 1", calls, len(rows), len(c.waits)+1)
			}
			for _, body := range p.api.recorded {
				if body != p.api.recorded[0] {
					t.Errorf("a report differs from the first: %s", body)
				}
			}
		})
	}
}

// TestAReportWithNoResponseIsSentAgain is a report whose first send gets no response within
// the limit of a send: the second send goes after 1 s and is taken.
func TestAReportWithNoResponseIsSentAgain(t *testing.T) {
	t.Cleanup(cmd.SetRecordAttemptLimit(100 * time.Millisecond))
	p := newPushRepo(t, "write")
	p.api.record = func(n int, w http.ResponseWriter) bool {
		if n > 1 {
			return false
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return true
		}
		t.Cleanup(func() { conn.Close() })
		return true
	}
	r := p.run(t, commitFirst(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if calls, rows := p.api.reports(); calls != 2 || len(rows) != 1 {
		t.Errorf("%d reports and %d rows, want 2 and 1", calls, len(rows))
	}
	if !slices.Equal(p.waits, []time.Duration{time.Second}) {
		t.Errorf("the waits are %v, want [1s]", p.waits)
	}
}

// TestAReportThatIsNeverTakenEndsTheRun is a report that fails 4 times: the run closes
// failed before session 2 with the contract's first line, then the status and Qory Apiary's
// text, and the commit stays on the forge.
func TestAReportThatIsNeverTakenEndsTheRun(t *testing.T) {
	p := newPushRepo(t, "write")
	failsRecord(p, 1, http.StatusServiceUnavailable, "unavailable", "")
	secondRan := false
	t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
	r := p.run(t, commitFirst(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	head := p.branchHead(t)
	if head == "" || head == p.first {
		t.Fatalf("the branch is at %q: the push did not land", head)
	}
	wants(t, r.out, "Contact support: qory pushed "+head+" to "+recordBranch+", and Qory Apiary did not record it.\nQory Apiary returned 503: unavailable\n")
	for _, retry := range []string{"Try again", "Run the command again"} {
		if strings.Contains(r.out, retry) {
			t.Errorf("the output has %q, but qory has sent the report again 3 times\n%s", retry, r.out)
		}
	}
	if calls, _ := p.api.reports(); calls != 4 {
		t.Errorf("%d reports, want 4", calls)
	}
	if !slices.Equal(p.waits, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Errorf("the waits are %v", p.waits)
	}
	if secondRan {
		t.Error("session 2 ran after the report failed")
	}
	if posts, _, _ := p.api.counts(); posts != 0 {
		t.Errorf("%d POST of a version went out before the report was taken", posts)
	}
	if got := r.pushSteps(); len(got) != 1 || got[0] != event.StateFailed {
		t.Errorf("the push steps exited %v, want one failed", got)
	}
}

// TestAReportThatGetsAnother4xxEndsTheRunAtOnce is each other 4xx: no second send, and the run closes
// failed before session 2.
func TestAReportThatGetsAnother4xxEndsTheRunAtOnce(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		code   string
		text   string
	}{
		"400":          {http.StatusBadRequest, "invalid_request", "invalid_request"},
		"401":          {http.StatusUnauthorized, "unauthorized", "unauthorized"},
		"403":          {http.StatusForbidden, "", "Forbidden"},
		"404":          {http.StatusNotFound, "not_found", "not_found"},
		"413":          {http.StatusRequestEntityTooLarge, "payload_too_large", "payload_too_large"},
		"not_writable": {http.StatusConflict, "not_writable", "not_writable"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			failsRecord(p, 1, c.status, c.code, "")
			secondRan := false
			t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
			r := p.run(t, commitFirst(t), nil)
			r.wantsClose(t, event.StateFailed, 1)
			wants(t, r.out, "Contact support: qory pushed "+p.branchHead(t)+" to "+recordBranch+", and Qory Apiary did not record it.\n"+fmt.Sprintf("Qory Apiary returned %d: %s\n", c.status, c.text))
			if calls, _ := p.api.reports(); calls != 1 || len(p.waits) != 0 {
				t.Errorf("%d reports and the waits %v, want 1 and none", calls, p.waits)
			}
			if secondRan {
				t.Error("session 2 ran after the report got a 4xx")
			}
		})
	}
}

// TestARunEndedInApiaryStartsNoLaterSession is a report that gets run_ended after session 1:
// qory sends no second report, stores no version, starts no session 2, and says so in one line.
// The run closes with the session's outcome, as for a run that Qory Apiary closed, with exit
// status 0 and no new status.
func TestARunEndedInApiaryStartsNoLaterSession(t *testing.T) {
	p := newPushRepo(t, "write")
	failsRecord(p, 1, http.StatusConflict, "run_ended", "")
	secondRan := false
	t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
	r := p.run(t, commitFirst(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if secondRan {
		t.Error("session 2 started after run_ended")
	}
	if calls, _ := p.api.reports(); calls != 1 || len(p.waits) != 0 {
		t.Errorf("%d reports and the waits %v, want 1 and none", calls, p.waits)
	}
	if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
		t.Errorf("%d POST and %d versions, want none", posts, stored)
	}
	if got := strings.Count(r.out, "qory run: Qory Apiary ended this run, so qory pushes nothing more and no later session starts.\n"); got != 1 {
		t.Errorf("the line of the ended run is in the output %d times, want 1\n%s", got, r.out)
	}
	if got := r.pushSteps(); len(got) != 1 || got[0] != event.StateSucceeded {
		t.Errorf("the push steps exited %v, want one succeeded", got)
	}
}

// TestAPushThatLandedBeforeASecondRepositoryFailedIsReported is a run of two repositories whose
// first push landed and whose second failed: the first repository's head is reported, and the
// run's line is the second push's failure.
func TestAPushThatLandedBeforeASecondRepositoryFailedIsReported(t *testing.T) {
	p := newPushRepo(t, "write")
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	bare := filepath.Join(p.h.server.Root, "acme", "lib.git")
	proxygittest.Git(t, bare, "config", "http.receivepack", "true")
	writeFile(t, filepath.Join(bare, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(bare, "hooks", "pre-receive"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.repositories = "acme/site,acme/lib"
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "the push of the next repository failed: change the rule on "+proxygittest.Host)
	_, rows := p.api.reports()
	if want := []recordedRow{{proxygittest.Host, "acme/site", p.branchHead(t)}}; !slices.Equal(rows, want) {
		t.Errorf("the rows are %+v, want %+v", rows, want)
	}
}

// TestAPartialPushWhoseReportFailsGivesBothLines is the run above with a report that gets
// 403: the line of the second push's failure comes first, then the Contact support line.
func TestAPartialPushWhoseReportFailsGivesBothLines(t *testing.T) {
	p := newPushRepo(t, "write")
	failsRecord(p, 1, http.StatusForbidden, "", "")
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	bare := filepath.Join(p.h.server.Root, "acme", "lib.git")
	proxygittest.Git(t, bare, "config", "http.receivepack", "true")
	writeFile(t, filepath.Join(bare, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(bare, "hooks", "pre-receive"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.repositories = "acme/site,acme/lib"
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	first := strings.Index(r.out, "the push of the next repository failed")
	second := strings.Index(r.out, "Contact support: qory pushed "+p.branchHead(t))
	if first < 0 || second < first {
		t.Errorf("the lines are out of order or missing\n%s", r.out)
	}
}

// TestAPartialPushWhoseReportGetsRunEndedEndsTheRun is a run of two repositories whose first
// push landed and whose second failed, with a report that gets run_ended: the push's failure
// is the run's line, the line of the ended run shows once, no version is stored and no session 2
// starts.
func TestAPartialPushWhoseReportGetsRunEndedEndsTheRun(t *testing.T) {
	p := newPushRepo(t, "write")
	failsRecord(p, 1, http.StatusConflict, "run_ended", "")
	p.h.server.Repository(t, "acme/lib", "main", map[string]string{"lib.txt": "lib\n"})
	bare := filepath.Join(p.h.server.Root, "acme", "lib.git")
	proxygittest.Git(t, bare, "config", "http.receivepack", "true")
	writeFile(t, filepath.Join(bare, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(bare, "hooks", "pre-receive"), 0o755); err != nil {
		t.Fatal(err)
	}
	p.repositories = "acme/site,acme/lib"
	secondRan := false
	t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(dir, ".qory", "repositories", "acme", "lib", "left.txt"), "left\n")
	}, nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "the push of the next repository failed: change the rule on "+proxygittest.Host)
	const ended = "qory run: Qory Apiary ended this run, so qory pushes nothing more and no later session starts.\n"
	if got := strings.Count(r.out, ended); got != 1 {
		t.Errorf("the line of the ended run is in the output %d times, want 1\n%s", got, r.out)
	}
	if strings.Contains(r.out, "Contact support: qory pushed") {
		t.Errorf("the output has the Contact support line, but Qory Apiary ended the run\n%s", r.out)
	}
	if secondRan {
		t.Error("session 2 started after run_ended")
	}
	if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
		t.Errorf("%d POST and %d versions, want none", posts, stored)
	}
}
