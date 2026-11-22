package cmd_test

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// recordBranch is the ref of the work item's branch, as the failure line names it.
const recordBranch = "qory/" + workItem

// lastPushUnrecorded is the line after the line of an ended run: the push that the report was
// about is on the forge and Qory Apiary has no record of it.
const lastPushUnrecorded = "qory run: The last push is on the branch, and Qory Apiary did not record it.\n"

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

// TestAPushWithAReadTargetReportsTheTargetAlone is a run whose agent changes the target and
// the read target acme/lib: one report has the target alone, at the head on its branch, and
// acme/lib has no branch.
func TestAPushWithAReadTargetReportsTheTargetAlone(t *testing.T) {
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
	want := []recordedRow{{proxygittest.Host, "acme/site", p.branchHead(t)}}
	if calls != 1 || !slices.Equal(rows, want) {
		t.Errorf("%d reports with the rows %+v, want 1 with %+v", calls, rows, want)
	}
	if refs := proxygittest.Git(t, libBare, "for-each-ref", "refs/heads/"+recordBranch); refs != "" {
		t.Errorf("the read target has the branch: %s", refs)
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

// TestAReportThatMayNotHaveArrivedIsSentAgain is a report that gets 503, 429, or a gateway 502 or
// 503 before it is taken: each send has the same body, and the waits are 1 s, 2 s and 4 s. The
// link has no Retry-After, so a Retry-After of Qory Apiary's does not change the waits.
func TestAReportThatMayNotHaveArrivedIsSentAgain(t *testing.T) {
	for name, c := range map[string]struct {
		respond func(n int, w http.ResponseWriter) bool
		gateway func(n int) error
		waits   []time.Duration
		// atApiary is the reports that reach Qory Apiary, one more than the sends that fail
		// before it.
		atApiary int
	}{
		"503 twice": {respond: func(n int, w http.ResponseWriter) bool {
			if n <= 2 {
				failing(w, http.StatusServiceUnavailable, "unavailable", "")
				return true
			}
			return false
		}, waits: []time.Duration{time.Second, 2 * time.Second}, atApiary: 3},
		"three failures": {respond: func(n int, w http.ResponseWriter) bool {
			if n <= 3 {
				failing(w, http.StatusBadGateway, "", "")
				return true
			}
			return false
		}, waits: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, atApiary: 4},
		"429 after 1 s, whatever Retry-After says": {respond: func(n int, w http.ResponseWriter) bool {
			if n == 1 {
				failing(w, http.StatusTooManyRequests, "rate_limited", "3")
				return true
			}
			return false
		}, waits: []time.Duration{time.Second}, atApiary: 2},
		"503 with Retry-After over 30 s waits 1 s": {respond: func(n int, w http.ResponseWriter) bool {
			if n == 1 {
				failing(w, http.StatusServiceUnavailable, "unavailable", "90")
				return true
			}
			return false
		}, waits: []time.Duration{time.Second}, atApiary: 2},
		"gateway 502 control_plane_unavailable twice": {gateway: func(n int) error {
			if n <= 2 {
				return cmd.GatewayRefusal(http.StatusBadGateway, "control_plane_unavailable")
			}
			return nil
		}, waits: []time.Duration{time.Second, 2 * time.Second}, atApiary: 1},
		"gateway 503 with no code three times": {gateway: func(n int) error {
			if n <= 3 {
				return cmd.GatewayBusy()
			}
			return nil
		}, waits: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}, atApiary: 1},
		"an error with no status": {gateway: func(n int) error {
			if n == 1 {
				return errors.New("the pushed-commits request: the link closed")
			}
			return nil
		}, waits: []time.Duration{time.Second}, atApiary: 1},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			p.api.record = c.respond
			p.gateway = c.gateway
			r := p.run(t, commitFirst(t), nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if !slices.Equal(p.waits, c.waits) {
				t.Errorf("the waits are %v, want %v", p.waits, c.waits)
			}
			calls, rows := p.api.reports()
			if calls != c.atApiary || len(rows) != 1 {
				t.Errorf("%d reports and %d rows at Qory Apiary, want %d and 1", calls, len(rows), c.atApiary)
			}
			if p.linkCalls != len(c.waits)+1 {
				t.Errorf("%d sends over the link, want %d", p.linkCalls, len(c.waits)+1)
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
	r.wantsReason(t, "push_not_recorded")
	head := p.branchHead(t)
	if head == "" || head == p.first {
		t.Fatalf("the branch is at %q: the push did not land", head)
	}
	wants(t, r.out, "qory run: Qory Apiary did not record the last push.\n  qory pushed "+head+" to "+recordBranch+".\n  Qory Apiary returned 503: unavailable\n")
	for _, retry := range []string{"Try again", "Run the command again"} {
		if strings.Contains(r.out, retry) {
			t.Errorf("the output has %q, but qory has sent the report again 3 times\n%s", retry, r.out)
		}
	}
	const line1 = "qory run: Qory Apiary did not record the last push."
	found := false
	for _, line := range strings.Split(r.out, "\n") {
		if strings.HasPrefix(line, "qory run: Qory Apiary did not record") {
			found = true
			if line != line1 || len(line) > 100 {
				t.Errorf("line 1 is %q with %d characters, want %q and at most 100", line, len(line), line1)
			}
		}
	}
	if !found {
		t.Errorf("the output has no line 1 of the failed report\n%s", r.out)
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

// TestAnInternalFailureOfTheRunAPIIsTheGatewaysLine is a 500 with the code internal, which the
// link returns as a status with no refusal: sent 4 times, then the second line names the gateway.
func TestAnInternalFailureOfTheRunAPIIsTheGatewaysLine(t *testing.T) {
	p := newPushRepo(t, "write")
	failsRecord(p, 1, http.StatusInternalServerError, "internal", "")
	r := p.run(t, commitFirst(t), nil)
	r.wantsClose(t, event.StateFailed, 1)
	wants(t, r.out, "qory run: Qory Apiary did not record the last push.\n  qory pushed "+p.branchHead(t)+" to "+recordBranch+".\n  The gateway returned 500: internal\n")
	if calls, _ := p.api.reports(); calls != 4 {
		t.Errorf("%d reports, want 4", calls)
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
		"403":          {http.StatusForbidden, "forbidden", "forbidden"},
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
			wants(t, r.out, "qory run: Qory Apiary did not record the last push.\n  qory pushed "+p.branchHead(t)+" to "+recordBranch+".\n  "+fmt.Sprintf("Qory Apiary returned %d: %s\n", c.status, c.text))
			if calls, _ := p.api.reports(); calls != 1 || len(p.waits) != 0 {
				t.Errorf("%d reports and the waits %v, want 1 and none", calls, p.waits)
			}
			if secondRan {
				t.Error("session 2 ran after the report got a 4xx")
			}
		})
	}
}

// TestWhatTheGatewayDoesNotPassOnIsA502AndIsSentAgain is Qory Apiary's 401 and 410: the gateway
// returns each as 502 control_plane_unavailable, so the report is sent 4 times, after 1 s, 2 s and
// 4 s, and the run closes failed with the second line "The gateway returned 502".
func TestWhatTheGatewayDoesNotPassOnIsA502AndIsSentAgain(t *testing.T) {
	for name, status := range map[string]int{"401": http.StatusUnauthorized, "410": http.StatusGone} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			failsRecord(p, 1, status, "run_ended", "")
			secondRan := false
			t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
			r := p.run(t, commitFirst(t), nil)
			r.wantsClose(t, event.StateFailed, 1)
			wants(t, r.out, "qory run: Qory Apiary did not record the last push.\n  qory pushed "+p.branchHead(t)+" to "+recordBranch+".\n  The gateway returned 502: ")
			if calls, _ := p.api.reports(); calls != 4 {
				t.Errorf("%d reports, want 4", calls)
			}
			if !slices.Equal(p.waits, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
				t.Errorf("the waits are %v", p.waits)
			}
			if secondRan {
				t.Error("session 2 ran after the report failed")
			}
		})
	}
}

// TestAGatewayRefusalOfA4xxEndsTheRunAtOnce is a report that the gateway returns a 4xx for before it
// reaches Qory Apiary, 413 payload_too_large or another: no second send, nothing at
// Qory Apiary, and the run closes failed before session 2 with "The gateway returned", the status
// and the gateway's code.
func TestAGatewayRefusalOfA4xxEndsTheRunAtOnce(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		code   string
	}{
		"413 payload_too_large": {http.StatusRequestEntityTooLarge, "payload_too_large"},
		"400 invalid_request":   {http.StatusBadRequest, "invalid_request"},
		"404 not_found":         {http.StatusNotFound, "not_found"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			p.gateway = func(int) error { return cmd.GatewayRefusal(c.status, c.code) }
			secondRan := false
			t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
			r := p.run(t, commitFirst(t), nil)
			r.wantsClose(t, event.StateFailed, 1)
			wants(t, r.out, "qory run: Qory Apiary did not record the last push.\n  qory pushed "+p.branchHead(t)+" to "+recordBranch+".\n  "+fmt.Sprintf("The gateway returned %d: %s\n", c.status, c.code))
			if strings.Contains(r.out, "Send the") {
				t.Errorf("the output has a step to send the call again\n%s", r.out)
			}
			if calls, _ := p.api.reports(); calls != 0 || p.linkCalls != 1 || len(p.waits) != 0 {
				t.Errorf("%d reports at Qory Apiary, %d sends and the waits %v, want 0, 1 and none", calls, p.linkCalls, p.waits)
			}
			if secondRan {
				t.Error("session 2 ran after the report got a 4xx")
			}
		})
	}
}

// TestAGatewayThatStaysDownEndsTheRunAfterFourSends is a gateway 502 on every send: the run
// closes failed with the not-recorded line and 502, after 1 s, 2 s and 4 s.
func TestAGatewayThatStaysDownEndsTheRunAfterFourSends(t *testing.T) {
	for name, down := range map[string]error{
		"502": cmd.GatewayRefusal(http.StatusBadGateway, "control_plane_unavailable"),
		"503": cmd.GatewayBusy(),
	} {
		t.Run(name, func(t *testing.T) {
			p := newPushRepo(t, "write")
			p.gateway = func(int) error { return down }
			secondRan := false
			t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
			r := p.run(t, commitFirst(t), nil)
			r.wantsClose(t, event.StateFailed, 1)
			status := map[string]string{"502": "502: control_plane_unavailable", "503": "503: Service Unavailable"}[name]
			wants(t, r.out, "qory run: Qory Apiary did not record the last push.\n  qory pushed "+p.branchHead(t)+" to "+recordBranch+".\n  The gateway returned "+status+"\n")
			if strings.Contains(r.out, "Send the") {
				t.Errorf("the output has a step to send the call again\n%s", r.out)
			}
			if p.linkCalls != 4 || !slices.Equal(p.waits, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
				t.Errorf("%d sends and the waits %v, want 4 and [1s 2s 4s]", p.linkCalls, p.waits)
			}
			if secondRan {
				t.Error("session 2 ran after the report failed")
			}
		})
	}
}

// TestEachSendOfAReportHas65Seconds is every send over the link: it has a limit of 65 s, 5 s more
// than the 60 s that the gateway waits for Qory Apiary ([server.HostCallTime]), so the gateway's
// response arrives first.
func TestEachSendOfAReportHas65Seconds(t *testing.T) {
	p := newPushRepo(t, "write")
	p.gateway = func(n int) error {
		if n == 1 {
			return cmd.GatewayBusy()
		}
		return nil
	}
	r := p.run(t, commitFirst(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if len(p.linkLimits) != 2 {
		t.Fatalf("%d sends, want 2", len(p.linkLimits))
	}
	for i, limit := range p.linkLimits {
		if limit > 65*time.Second || limit <= server.HostCallTime {
			t.Errorf("send %d has %s left at its start, want just under 65s", i+1, limit)
		}
	}
}

// TestAGatewayThatEndedTheRunStopsTheReport is a report that gets 410 from the gateway, because the
// run ended while the call was open, with each code of [server.EndCodes]: qory sends no second
// report, stores no version, starts no session 2, and prints one line that has the code and does
// not say that Qory Apiary ended the run. The step succeeds.
func TestAGatewayThatEndedTheRunStopsTheReport(t *testing.T) {
	for _, code := range server.EndCodes {
		t.Run(code, func(t *testing.T) {
			p := newPushRepo(t, "write")
			p.gateway = func(int) error { return cmd.GatewayRefusal(http.StatusGone, code) }
			secondRan := false
			t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
			r := p.run(t, commitFirst(t), nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			if secondRan {
				t.Error("session 2 started after the gateway's 410")
			}
			if p.linkCalls != 1 || len(p.waits) != 0 {
				t.Errorf("%d sends and the waits %v, want 1 and none", p.linkCalls, p.waits)
			}
			if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
				t.Errorf("%d POST and %d versions, want none", posts, stored)
			}
			line := "qory run: The run ended (" + code + "), so qory pushes nothing more and no later session starts.\n" + lastPushUnrecorded
			if got := strings.Count(r.out, line); got != 1 {
				t.Errorf("the line of the ended run is in the output %d times, want 1\n%s", got, r.out)
			}
			if strings.Contains(r.out, "Qory Apiary ended this run") {
				t.Errorf("the output says that Qory Apiary ended the run, but the gateway did\n%s", r.out)
			}
			if got := r.pushSteps(); len(got) != 1 || got[0] != event.StateSucceeded {
				t.Errorf("the push steps exited %v, want one succeeded", got)
			}
		})
	}
}

// TestAClosedRunEndsTheReportQuietly is a report whose call returns an error that wraps
// [session.ErrRunClosed], as the run does when it sent nothing because it is closed: qory sends
// the call once, with no wait, stores no version, starts no session 2, and prints the two lines
// of an ended run with the code run_closed and no line that sends the reader to support. The step
// succeeds.
func TestAClosedRunEndsTheReportQuietly(t *testing.T) {
	p := newPushRepo(t, "write")
	p.gateway = func(int) error {
		return fmt.Errorf("Open another run to send the pushed-commits request: %w.", session.ErrRunClosed)
	}
	secondRan := false
	t.Cleanup(cmd.SetLaterSessions(func() { secondRan = true }))
	r := p.run(t, commitFirst(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if secondRan {
		t.Error("session 2 started after the run was closed")
	}
	if p.linkCalls != 1 || len(p.waits) != 0 {
		t.Errorf("%d sends and the waits %v, want 1 and none", p.linkCalls, p.waits)
	}
	if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
		t.Errorf("%d POST and %d versions, want none", posts, stored)
	}
	line := "qory run: The run ended (run_closed), so qory pushes nothing more and no later session starts.\n" + lastPushUnrecorded
	if got := strings.Count(r.out, line); got != 1 {
		t.Errorf("the lines of the ended run are in the output %d times, want 1\n%s", got, r.out)
	}
	if strings.Contains(r.out, "Contact "+"support") {
		t.Errorf("the output sends the reader to support\n%s", r.out)
	}
	if got := r.pushSteps(); len(got) != 1 || got[0] != event.StateSucceeded {
		t.Errorf("the push steps exited %v, want one succeeded", got)
	}
}

// TestARunEndedInApiaryStartsNoLaterSession is a report that gets run_ended after session 1:
// qory sends no second report, stores no version, starts no session 2, and prints one line.
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
	if got := strings.Count(r.out, "qory run: Qory Apiary ended this run, so qory pushes nothing more and no later session starts.\n"+lastPushUnrecorded); got != 1 {
		t.Errorf("the line of the ended run is in the output %d times, want 1\n%s", got, r.out)
	}
	if got := r.pushSteps(); len(got) != 1 || got[0] != event.StateSucceeded {
		t.Errorf("the push steps exited %v, want one succeeded", got)
	}
}

// TestAStepAfterTheRunEndedPushesAndReportsNothing is the push step called again after a
// report found the run over, with a new commit in the working folder: it returns the session's
// outcome with no error, and sends no push, no report and no version.
func TestAStepAfterTheRunEndedPushesAndReportsNothing(t *testing.T) {
	p := newPushRepo(t, "write")
	failsRecord(p, 1, http.StatusConflict, "run_ended", "")
	var dir, afterFirst string
	var againErr error
	called := false
	t.Cleanup(cmd.SetStepAfterEnd(func() {
		afterFirst = p.branchHead(t)
		writeFile(t, filepath.Join(dir, "second.txt"), "second\n")
		commitAll(t, dir, "Add a second file")
	}, func(_ session.Outcome, err error) { called, againErr = true, err }))
	r := p.run(t, func(d string) {
		dir = d
		commitFirst(t)(d)
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if !called || againErr != nil {
		t.Errorf("the second call ran: %v, with the error %v, want true and none", called, againErr)
	}
	if head := p.branchHead(t); afterFirst == "" || head != afterFirst {
		t.Errorf("the branch is at %q, want the first push's %q", head, afterFirst)
	}
	if calls, _ := p.api.reports(); calls != 1 {
		t.Errorf("%d reports, want 1", calls)
	}
	if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
		t.Errorf("%d POST and %d versions, want none", posts, stored)
	}
	if got := strings.Count(r.out, "no later session starts.\n"+lastPushUnrecorded); got != 1 {
		t.Errorf("the line of the ended run is in the output %d times, want 1\n%s", got, r.out)
	}
	if got := r.pushSteps(); len(got) != 1 {
		t.Errorf("the push steps exited %v, want one", got)
	}
}
