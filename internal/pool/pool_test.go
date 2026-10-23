package pool_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// The lines and reasons the tests look for.
const (
	lineStarted     = "started qory run for the dispatch"
	lineEnded       = "qory run for the dispatch ended"
	lineEndedThere  = "the dispatch ended at Qory Apiary, so its qory run was stopped"
	lineLifetime    = "the dispatch reached its max_lifetime_minutes, so its qory run was stopped"
	lineBeatFailed  = "dispatched failed, so it is sent again after a wait"
	lineReleased    = "released the run"
	lineNotReleased = "the run could not be released"
	lineOverAlready = "the dispatch was over already, so nothing was released"
	lineNotStarted  = "the run could not be started"
	reasonMachine   = "this pool creates no machine of the type in the claim"
	reasonNotStart  = "the run could not be started on this machine"
	reasonRestarted = "the pool program restarted before the run ended"
	reasonStopped   = "the pool program stopped before the run started"
	reasonLease     = "the lease ended before the run could be started"
	withheld        = "(a line that contains a secret, left out)"
)

// allowed are the variables a qory run may get, as child.Env gives them.
var allowed = []string{"PATH", "HOME", "TMPDIR", "LANG", "DOCKER_HOST", "DOCKER_CONTEXT", "XDG_CONFIG_HOME", "XDG_STATE_HOME"}

// TestADispatchRunsQoryRun is one dispatch, from its claim to the end of its qory run:
// claim, dispatched, then qory run --run-code-file /dev/fd/3 --gateway <gateway_url> in
// an empty work/ of the dispatch's folder, with the run code in a file, mode 0600, open
// for reading alone, that no name links to, and the allow-listed environment alone. The
// pool releases nothing and posts no outcome; the dispatch's folder and the code's file
// are gone, and the journal is empty. qory run's line with the run code is left out.
func TestADispatchRunsQoryRun(t *testing.T) {
	t.Setenv("QORY_ACCESS_KEY_SECRET", "x")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	h := newHarness(t, 1)
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.settled(1)
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	d := h.only()
	if routes := h.srv.Routes(); len(routes) < 2 || routes[0] != provisionertest.RouteClaim || routes[1] != provisionertest.RouteDispatched || slices.Contains(routes, provisionertest.RouteOutcome) {
		t.Errorf("the fake was sent %v", routes)
	}
	h.notReleased()
	rep, ok := h.reportOf(d.ID)
	if !ok {
		t.Fatal("qory run did not run")
	}
	if want := []string{"run", "--run-code-file", "/dev/fd/3", "--gateway", provisionertest.GatewayURL}; !slices.Equal(rep.Args, want) {
		t.Errorf("qory run's arguments are %q, want %q", rep.Args, want)
	}
	if !rep.FD3File || rep.CodeSum != sum([]byte(d.Run.RunCode)) {
		t.Errorf("descriptor 3 is a file that no name links to, open for reading alone, %v; it contains the run code %v", rep.FD3File, rep.CodeSum == sum([]byte(d.Run.RunCode)))
	}
	dir := filepath.Join(h.state, "runs", d.ID)
	if rep.Dir != filepath.Join(dir, "work") || rep.DirEntries != 0 {
		t.Errorf("qory run ran in %s, with %d entries, want the empty %s", rep.Dir, rep.DirEntries, filepath.Join(dir, "work"))
	}
	for _, kv := range rep.Env {
		if name, _, _ := strings.Cut(kv, "="); !slices.Contains(allowed, name) {
			t.Errorf("qory run got %s", name)
		}
	}
	for _, want := range []string{"XDG_CONFIG_HOME=" + h.runConfig, "XDG_STATE_HOME=" + filepath.Join(dir, "state")} {
		if !slices.Contains(rep.Env, want) {
			t.Errorf("qory run did not get %s: %q", want, rep.Env)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dispatch's folder is still there: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(h.state, "runcode-*")); len(left) != 0 {
		t.Errorf("a run code's file is still there: %q", left)
	}
	if lines := journalOf(t, h.state); len(lines) != 0 {
		t.Errorf("the journal is %q", lines)
	}
	for _, msg := range []string{"claimed a run", lineStarted, d.ID + ": hello", d.ID + ": " + withheld} {
		if m := h.log.with(msg); len(m) != 1 {
			t.Errorf("%q was logged %d times:\n%s", msg, len(m), h.log.text())
		}
	}
	if m := h.log.with(lineEnded); len(m) != 1 || m[0]["dispatch_id"] != d.ID || m[0]["exit_code"] != float64(0) {
		t.Errorf("the end's lines are %v", m)
	}
	if m := h.log.with(lineStarted); len(m) == 1 && m[0]["dispatch_id"] != d.ID {
		t.Errorf("the start's line is %v", m[0])
	}
}

// TestTheHeartbeat sends dispatched again at each beat while qory run runs, logs a
// dispatched that failed and goes on, and stops qory run, with SIGTERM, once dispatched
// returns 410 gone or 409 lease_expired. Nothing is released.
func TestTheHeartbeat(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func(h *harness, id string)
	}{
		{"gone", func(h *harness, id string) { h.srv.End(id) }},
		{"lease_expired", func(h *harness, _ string) {
			h.srv.Script(provisionertest.RouteDispatched, provisionertest.Reply{Status: 409, Code: "lease_expired"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1)
			h.script(script{Block: true})
			h.srv.Offer(provisionertest.Run{})
			h.start()
			h.waitFor("the dispatch", func() bool { return len(h.srv.Dispatches()) == 1 })
			d := h.only()
			h.ready(d.ID)
			h.beat()
			h.beat()
			h.waitFor("two heartbeats", func() bool { return h.count(provisionertest.RouteDispatched) == 3 })
			h.srv.Script(provisionertest.RouteDispatched, provisionertest.Reply{Status: 403, Code: "forbidden"})
			h.beat()
			h.waitFor("the failed heartbeat's line", func() bool { return len(h.log.with(lineBeatFailed)) == 1 })
			if m := h.log.with(lineBeatFailed)[0]; m["dispatch_id"] != d.ID || m["wait"] == nil || m["error"] == nil {
				t.Errorf("the failed heartbeat's line is %v", m)
			}
			if h.over(d.ID) {
				t.Fatal("a failed heartbeat stopped qory run")
			}
			c.end(h, d.ID)
			h.beat()
			h.settled(1)
			h.stop()
			if m := h.log.with(lineEndedThere); len(m) != 1 || m[0]["dispatch_id"] != d.ID {
				t.Errorf("the dispatch's end is logged %v", m)
			}
			if m := h.log.with(lineEnded); len(m) != 1 || m[0]["exit_code"] != float64(1) {
				t.Errorf("qory run was not stopped with SIGTERM: %v", m)
			}
			if n := h.count(provisionertest.RouteDispatched); n != 5 {
				t.Errorf("dispatched was sent %d times, want 5", n)
			}
			if logs, _, statuses := h.machineLogs(d.ID); len(logs) != 1 || statuses[0] != 204 || !strings.HasPrefix(logs[0].Log, "hello\n") {
				t.Errorf("the machine logs after the dispatch's end are %+v, answered %v", logs, statuses)
			}
			for _, w := range h.waitsOf() {
				if w == 2*time.Second {
					t.Errorf("the pool waited %v", h.waitsOf())
				}
			}
			h.notReleased()
		})
	}
}

// TestTheLifetime stops qory run once the claim's max_lifetime_minutes passed, and logs
// it with the minutes.
func TestTheLifetime(t *testing.T) {
	h := newHarness(t, 1)
	h.script(script{Block: true})
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.waitFor("the dispatch", func() bool { return len(h.srv.Dispatches()) == 1 })
	d := h.only()
	h.ready(d.ID)
	select {
	case h.lifetimes <- struct{}{}:
	case <-time.After(20 * time.Second):
		t.Fatal("the pool did not wait for the lifetime")
	}
	h.settled(1)
	h.stop()
	if m := h.log.with(lineLifetime); len(m) != 1 || m[0]["max_lifetime_minutes"] != float64(provisionertest.MaxLifetimeMinutes) {
		t.Errorf("the lifetime's lines are %v", m)
	}
	if m := h.log.with(lineEnded); len(m) != 1 || m[0]["exit_code"] != float64(1) {
		t.Errorf("qory run was not stopped with SIGTERM: %v", m)
	}
	h.notReleased()
}

// TestAClaimWithAMachineIsReleased releases a claim whose machine member is set, before
// dispatched, writes no run code's file and starts no qory run.
func TestAClaimWithAMachineIsReleased(t *testing.T) {
	h := newHarness(t, 1)
	h.srv.Offer(provisionertest.Run{Machine: "large"})
	h.start()
	h.settled(1)
	h.stop()
	d := h.only()
	h.released(d, reasonMachine)
	if n := h.count(provisionertest.RouteDispatched); n != 0 {
		t.Errorf("dispatched was sent %d times", n)
	}
	if _, ok := h.reportOf(d.ID); ok || len(h.log.with(lineStarted)) != 0 {
		t.Error("qory run ran")
	}
	if m := h.log.with(lineReleased); len(m) != 1 || m[0]["reason"] != reasonMachine {
		t.Errorf("the release's lines are %v", m)
	}
}

// TestTheRunsCap sends no claim while pool.runs dispatches are live, and claims again
// once one ended.
func TestTheRunsCap(t *testing.T) {
	h := newHarness(t, 1)
	h.script(script{Block: true})
	h.srv.Offer(provisionertest.Run{}, provisionertest.Run{})
	h.start()
	h.waitFor("the dispatch", func() bool { return len(h.srv.Dispatches()) == 1 })
	first := h.only()
	h.ready(first.ID)
	time.Sleep(1500 * time.Millisecond)
	if n := h.count(provisionertest.RouteClaim); n != 1 {
		t.Errorf("%d claims went out while one dispatch of one was live", n)
	}
	h.letGo(first.ID)
	h.waitFor("the second dispatch", func() bool { return len(h.srv.Dispatches()) == 2 })
	second := h.srv.Dispatches()[1]
	h.ready(second.ID)
	h.letGo(second.ID)
	h.settled(2)
	h.stop()
}

// TestDispatchedFailures: a signed 429 is waited out up to three times; 409
// lease_expired and 410 drop the dispatch with no release; anything else releases it.
// qory run starts after dispatched alone.
func TestDispatchedFailures(t *testing.T) {
	limited := provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: "2"}
	for _, c := range []struct {
		name    string
		replies []provisionertest.Reply
		reason  string
		ran     bool
		waits   int
	}{
		{name: "429 three times", replies: []provisionertest.Reply{limited, limited, limited}, ran: true, waits: 3},
		{name: "429 four times", replies: []provisionertest.Reply{limited, limited, limited, limited}, reason: reasonNotStart, waits: 3},
		{name: "lease_expired", replies: []provisionertest.Reply{{Status: 409, Code: "lease_expired"}}},
		{name: "gone", replies: []provisionertest.Reply{{Status: 410, Code: "gone"}}},
		{name: "forbidden", replies: []provisionertest.Reply{{Status: 403, Code: "forbidden"}}, reason: reasonNotStart},
		{name: "unsigned", replies: slices.Repeat([]provisionertest.Reply{{Status: 503, Unsigned: true}}, 5), reason: reasonNotStart},
		{name: "429 past the lease", replies: []provisionertest.Reply{{Status: 429, Code: "rate_limited", RetryAfter: "3600"}}, reason: reasonLease},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1)
			h.srv.Script(provisionertest.RouteDispatched, c.replies...)
			h.srv.Offer(provisionertest.Run{})
			h.start()
			h.settled(1)
			h.stop()
			d := h.only()
			if c.reason != "" {
				h.released(d, c.reason)
			} else {
				h.notReleased()
			}
			if _, ran := h.reportOf(d.ID); ran != c.ran {
				t.Errorf("ran %v, want %v; the fake was sent %v", ran, c.ran, h.srv.Routes())
			}
			n := 0
			for _, w := range h.waitsOf() {
				if w == 2*time.Second {
					n++
				}
			}
			if n != c.waits {
				t.Errorf("the pool waited %v, want %d waits of 2s", h.waitsOf(), c.waits)
			}
		})
	}
}

// TestTheRelease waits out a signed 429 to a release up to three times, takes 410 and
// lease_expired as the dispatch over already, and logs a release that failed, 409
// machine_exists among them.
func TestTheRelease(t *testing.T) {
	limited := provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: "3"}
	for _, c := range []struct {
		name    string
		replies []provisionertest.Reply
		line    string
		waits   int
	}{
		{name: "429 twice", replies: []provisionertest.Reply{limited, limited}, line: lineReleased, waits: 2},
		{name: "429 four times", replies: []provisionertest.Reply{limited, limited, limited, limited}, line: lineNotReleased, waits: 3},
		{name: "gone", replies: []provisionertest.Reply{{Status: 410, Code: "gone"}}, line: lineOverAlready},
		{name: "lease_expired", replies: []provisionertest.Reply{{Status: 409, Code: "lease_expired"}}, line: lineOverAlready},
		{name: "machine_exists", replies: []provisionertest.Reply{{Status: 409, Code: "machine_exists"}}, line: lineNotReleased},
		{name: "unsigned", replies: slices.Repeat([]provisionertest.Reply{{Status: 500, Unsigned: true}}, 5), line: lineNotReleased},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1)
			h.srv.Script(provisionertest.RouteRelease, c.replies...)
			h.srv.Offer(provisionertest.Run{Machine: "large"})
			h.start()
			h.waitFor("the release's line", func() bool { return len(h.log.with(c.line)) == 1 })
			h.settled(1)
			h.stop()
			n := 0
			for _, w := range h.waitsOf() {
				if w == 3*time.Second {
					n++
				}
			}
			if n != c.waits {
				t.Errorf("the pool waited %v, want %d waits of 3s", h.waitsOf(), c.waits)
			}
		})
	}
}

// TestTheClaimBacksOff sends a claim that failed again after a wait that doubles from
// a second to a minute, for an unsigned answer, a 401, no answer, a server error and
// 409 no_gateway; a signed 429 waits its Retry-After; and a claim answered starts the
// waits again from a second. Each wait is logged.
func TestTheClaimBacksOff(t *testing.T) {
	five := func(r provisionertest.Reply) []provisionertest.Reply {
		return slices.Repeat([]provisionertest.Reply{r}, 5)
	}
	noGateway := provisionertest.Reply{Status: 409, Code: "no_gateway"}
	var replies []provisionertest.Reply
	replies = append(replies, five(provisionertest.Reply{Status: 503, Unsigned: true})...)
	replies = append(replies, five(provisionertest.Reply{Status: 401, Unsigned: true})...)
	replies = append(replies, five(provisionertest.Reply{Status: 503, Drop: true})...)
	replies = append(replies, five(provisionertest.Reply{Status: 500, Code: "internal"})...)
	replies = append(replies, noGateway, provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: "7"}, provisionertest.Reply{Status: 204}, noGateway)
	replies = append(replies, slices.Repeat([]provisionertest.Reply{noGateway}, 7)...)
	h := newHarness(t, 1)
	h.freeBeats = true
	h.srv.Script(provisionertest.RouteClaim, replies...)
	h.start()
	want := []time.Duration{1, 2, 4, 8, 16, 7, 1, 2, 4, 8, 16, 32, 60, 60}
	h.waitFor("the waits", func() bool { return len(h.waitsOf()) >= len(want) })
	h.stop()
	var got []time.Duration
	for _, w := range h.waitsOf() {
		got = append(got, w/time.Second)
	}
	if !slices.Equal(got[:len(want)], want) {
		t.Errorf("the claim's waits were %v seconds, want %v", got, want)
	}
	m := h.log.with("the claim failed, so it is sent again after a wait")
	if len(m) < len(want) || m[0]["wait"] == nil || m[0]["error"] == nil {
		t.Errorf("the claim's lines are %v", m)
	}
}

// TestAStopDuringAClaimReleasesNothing gives up the claim being held and returns.
func TestAStopDuringAClaimReleasesNothing(t *testing.T) {
	h := newHarness(t, 1)
	h.srv.Hold = time.Minute
	h.start()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the pool took %s to stop", time.Since(start))
	}
	h.notReleased()
}

// TestAStopAtEachPhase stops the pool at each phase of a dispatch: during the wait for
// dispatched the dispatch is released, the pool program stopped before the run started;
// with dispatched sent, qory run does not start and nothing is released; a qory run
// running gets SIGTERM, and nothing is released. Run returns nil each time.
func TestAStopAtEachPhase(t *testing.T) {
	t.Run("waiting for dispatched", func(t *testing.T) {
		h := newHarness(t, 1)
		h.srv.Script(provisionertest.RouteDispatched, provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: "5"})
		waiting := make(chan struct{}, 1)
		h.setSleep(func(ctx context.Context, d time.Duration) error {
			if d == 5*time.Second {
				waiting <- struct{}{}
				<-ctx.Done()
			}
			return ctx.Err()
		})
		h.srv.Offer(provisionertest.Run{})
		h.start()
		<-waiting
		if err := h.stop(); err != nil {
			t.Fatal(err)
		}
		h.released(h.only(), reasonStopped)
	})
	t.Run("dispatched in flight", func(t *testing.T) {
		h := newHarness(t, 1)
		let := h.gate.holdNext("/dispatched")
		h.srv.Offer(provisionertest.Run{})
		h.start()
		<-h.gate.reached
		stopped := make(chan error, 1)
		go func() { stopped <- h.stop() }()
		time.Sleep(100 * time.Millisecond)
		let()
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		d := h.only()
		h.notReleased()
		if _, ran := h.reportOf(d.ID); ran {
			t.Error("qory run ran after the stop")
		}
		if m := h.log.with(lineNotStarted); len(m) != 1 {
			t.Errorf("the start's lines are %v", m)
		}
		if logs, _, statuses := h.machineLogs(d.ID); len(logs) != 1 || statuses[0] != 204 || logs[0].Log != "" {
			t.Errorf("the machine logs of a qory run not started are %+v, answered %v", logs, statuses)
		}
	})
	t.Run("running", func(t *testing.T) {
		h := newHarness(t, 1)
		h.script(script{Block: true})
		h.srv.Offer(provisionertest.Run{})
		h.start()
		h.waitFor("the dispatch", func() bool { return len(h.srv.Dispatches()) == 1 })
		d := h.only()
		h.ready(d.ID)
		if err := h.stop(); err != nil {
			t.Fatal(err)
		}
		if m := h.log.with(lineEnded); len(m) != 1 || m[0]["exit_code"] != float64(1) {
			t.Errorf("qory run was not stopped with SIGTERM: %v", m)
		}
		if len(h.log.with(lineLifetime)) != 0 || len(h.log.with(lineEndedThere)) != 0 {
			t.Errorf("the stop is logged as another end:\n%s", h.log.text())
		}
		if logs, _, statuses := h.machineLogs(d.ID); len(logs) != 1 || statuses[0] != 204 || !strings.HasPrefix(logs[0].Log, "hello\n") {
			t.Errorf("the machine logs after the stop are %+v, answered %v", logs, statuses)
		}
		h.notReleased()
		if lines := journalOf(t, h.state); len(lines) != 0 {
			t.Errorf("the journal is %q", lines)
		}
	})
}

// TestTheJournalAfterAKill kills the pool at each phase, by taking the journal as it is
// on disk then, and starts a second pool with it: before its first claim, the second
// releases a claimed dispatch, for the pool program restarted, and drops a dispatched
// one with no release. The journal is 0600 in a 0700 folder and contains the dispatch
// id and the phase alone.
func TestTheJournalAfterAKill(t *testing.T) {
	for _, phase := range []string{pool.PhaseClaimed, pool.PhaseDispatched} {
		t.Run(phase, func(t *testing.T) {
			first := newHarness(t, 1)
			first.script(script{Block: true})
			if phase == pool.PhaseClaimed {
				defer first.gate.holdNext("/dispatched")()
			}
			first.srv.Offer(provisionertest.Run{})
			first.start()
			if phase == pool.PhaseClaimed {
				<-first.gate.reached
			} else {
				first.waitFor("the dispatch", func() bool { return len(first.srv.Dispatches()) == 1 })
				first.ready(first.only().ID)
			}
			d := first.only()
			if lines, want := journalOf(t, first.state), d.ID+" "+phase; len(lines) != 1 || lines[0] != want {
				t.Fatalf("the journal is %q, want %q", lines, want)
			}
			info, err := os.Stat(filepath.Join(first.state, pool.JournalFile))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("the journal: %v, %v", info, err)
			}
			if info, err := os.Stat(first.state); err != nil || info.Mode().Perm() != 0o700 {
				t.Errorf("the pool's folder: %v, %v", info, err)
			}
			b, _ := os.ReadFile(filepath.Join(first.state, pool.JournalFile))
			first.gate.close()

			second := newHarness(t, 1)
			second.srv = first.srv
			second.pool.Client = first.srv.Client()
			second.pool.Client.Now = first.clk.Now
			if err := os.MkdirAll(second.state, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(second.state, pool.JournalFile), b, 0o600); err != nil {
				t.Fatal(err)
			}
			before := len(first.srv.Calls())
			second.start()
			second.waitFor("a claim", func() bool {
				return slices.ContainsFunc(first.srv.Calls()[before:], func(c provisionertest.Call) bool { return c.Route == provisionertest.RouteClaim })
			})
			second.stop()
			first.stop()
			calls := first.srv.Calls()[before:]
			if phase == pool.PhaseClaimed {
				second.released(d, reasonRestarted)
				if calls[0].Route != provisionertest.RouteRelease || calls[0].DispatchID != d.ID {
					t.Errorf("the second pool's first request was %s %s", calls[0].Route, calls[0].DispatchID)
				}
			} else if calls[0].Route != provisionertest.RouteClaim {
				t.Errorf("the second pool's first request was %s %s, want its claim", calls[0].Route, calls[0].DispatchID)
			}
			if lines := journalOf(t, second.state); len(lines) != 0 {
				t.Errorf("the second pool's journal is %q", lines)
			}
		})
	}
}

// claimTo brings a dispatch of the fake to phase, as a pool killed then would have left
// it, and returns its journal entry.
func claimTo(t *testing.T, srv *provisionertest.Server, phase string) pool.Entry {
	t.Helper()
	ctx := context.Background()
	c := srv.Client()
	claimed, ok, err := c.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("%v, %v", ok, err)
	}
	claimed.RunCode.Clear()
	if phase == pool.PhaseDispatched {
		if err := c.Dispatched(ctx, claimed.DispatchID); err != nil {
			t.Fatal(err)
		}
	}
	return pool.Entry{DispatchID: claimed.DispatchID, Phase: phase}
}

// TestTheJournalIsReplayedBeforeTheFirstClaim releases each claimed entry of the
// journal it starts with, for the pool program restarted, and drops each dispatched one
// with no release. One over already, 410, leaves the journal as one released does, and
// so does its folder, and one refused 403 too; one whose release gets no response stays
// for the next start. A run code's file left in the pool's folder is removed.
func TestTheJournalIsReplayedBeforeTheFirstClaim(t *testing.T) {
	h := newHarness(t, 1)
	h.srv.Offer(provisionertest.Run{}, provisionertest.Run{}, provisionertest.Run{}, provisionertest.Run{})
	var entries []pool.Entry
	for _, phase := range []string{pool.PhaseClaimed, pool.PhaseDispatched, pool.PhaseClaimed, pool.PhaseClaimed} {
		entries = append(entries, claimTo(t, h.srv, phase))
	}
	h.srv.End(entries[2].DispatchID)
	// A dispatch the fake does not know is a signed 403, which no later start would
	// change.
	entries = append(entries[:3], pool.Entry{DispatchID: "dp_0000000000000000", Phase: pool.PhaseClaimed}, entries[3])
	h.srv.Script(provisionertest.RouteRelease, slices.Repeat([]provisionertest.Reply{{}}, 3)...)
	h.srv.Script(provisionertest.RouteRelease, slices.Repeat([]provisionertest.Reply{{Status: 503, Unsigned: true}}, 5)...)
	var journal string
	for _, e := range entries {
		journal += e.DispatchID + " " + e.Phase + "\n"
		if err := os.MkdirAll(filepath.Join(h.state, "runs", e.DispatchID, "work"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.state, pool.JournalFile), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(h.state, "runcode-"+entries[0].DispatchID)
	if err := os.WriteFile(left, []byte("left"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(h.srv.Calls())
	h.start()
	h.waitFor("a claim", func() bool {
		return slices.ContainsFunc(h.srv.Calls()[before:], func(c provisionertest.Call) bool { return c.Route == provisionertest.RouteClaim })
	})
	h.stop()
	d, _ := h.srv.Dispatch(entries[0].DispatchID)
	h.released(d, reasonRestarted)
	calls := h.srv.Calls()[before:]
	for i, e := range []pool.Entry{entries[0], entries[2], entries[3], entries[4]} {
		if c := calls[i]; c.Route != provisionertest.RouteRelease || c.DispatchID != e.DispatchID {
			t.Errorf("request %d was %s %s, want the release of %s", i, c.Route, c.DispatchID, e.DispatchID)
		}
	}
	if slices.ContainsFunc(calls, func(c provisionertest.Call) bool { return c.DispatchID == entries[1].DispatchID }) {
		t.Error("a request was sent for the dispatched entry")
	}
	if len(h.log.with(lineOverAlready)) != 1 {
		t.Errorf("the ended dispatch's release was not logged as over already:\n%s", h.log.text())
	}
	if lines, want := journalOf(t, h.state), entries[4].DispatchID+" claimed"; len(lines) != 1 || lines[0] != want {
		t.Errorf("the journal is %q, want %q", lines, want)
	}
	for i, e := range entries {
		_, err := os.Stat(filepath.Join(h.state, "runs", e.DispatchID))
		if gone := errors.Is(err, os.ErrNotExist); gone != (i < 4) {
			t.Errorf("the folder of entry %d: %v", i, err)
		}
	}
	if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the run code's file is still there: %v", err)
	}
}

// TestAMalformedJournalIsRefused refuses a journal it cannot read, with a clear error,
// and claims nothing. A journal of an earlier qory, with the run id and the step, is
// one.
func TestAMalformedJournalIsRefused(t *testing.T) {
	const entry = "dp_0000000000000000 claimed"
	for _, c := range []struct {
		name, content string
		link          bool
		line          int
	}{
		{name: "a line of one", content: entry + "\ndp_0000000000000001\n", line: 2},
		{name: "an earlier qory's", content: "dp_0000000000000000 0192f0c1-7d4e-7a2b-8c3d-00000000000a explore claimed\n", line: 1},
		{name: "no line end", content: entry, line: 1},
		{name: "an unknown phase", content: "dp_0000000000000000 started\n", line: 1},
		{name: "a dispatch twice", content: entry + "\n" + entry + "\n", line: 2},
		{name: "a dispatch id out of shape", content: "dp_x claimed\n", line: 1},
		{name: "two spaces", content: "dp_0000000000000000  claimed\n", line: 1},
		{name: "a link", link: true},
		{name: "too large", content: strings.Repeat(entry+"\n", 1<<20/len(entry)+1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, 1)
			if err := os.MkdirAll(h.state, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.state, pool.JournalFile)
			if c.link {
				target := filepath.Join(h.root, "elsewhere")
				if err := os.WriteFile(target, []byte(entry+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(c.content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := h.pool.Run(context.Background())
			want := path + " is not a journal of the pool's dispatches: it is not a regular file of at most 1 MiB, so no run is claimed"
			if c.line > 0 {
				want = fmt.Sprintf("fix line %d of %s, the journal of the pool's dispatches: each line must be one dispatch id and a phase, each dispatch once; the pool claims no run until then", c.line, path)
			}
			if err == nil || err.Error() != want {
				t.Errorf("%v, want %q", err, want)
			}
			if n := len(h.srv.Calls()); n != 0 {
				t.Errorf("the fake was sent %d requests", n)
			}
		})
	}
}

// TestTheSettingsAreChecked refuses to run by settings it cannot.
func TestTheSettingsAreChecked(t *testing.T) {
	for _, c := range []struct {
		edit func(*pool.Pool)
		want string
	}{
		{func(p *pool.Pool) { p.Settings.Runs = 0 }, "set pool.runs in forager.yaml to 1 or more: the pool runs at least 1 run at once"},
		{func(p *pool.Pool) { p.Settings.State = "pool" }, `the pool's folder "pool" is not an absolute path`},
		{func(p *pool.Pool) { p.Settings.RunConfig = "runs" }, `set pool.run_config in forager.yaml to an absolute path: "runs" is not one`},
		{func(p *pool.Pool) { p.Client = nil }, "the pool has no client of Qory Apiary"},
	} {
		h := newHarness(t, 1)
		c.edit(h.pool)
		if err := h.pool.Run(context.Background()); err == nil || err.Error() != c.want {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
}

// TestARunCodeFileIsRemovedWhereThePathHasPatternCharacters starts a pool whose folder's
// path contains [1], or a [ with no ], with a run code's file left in the folder: the
// start removes the file and keeps the folder's other files, as it does for any path.
func TestARunCodeFileIsRemovedWhereThePathHasPatternCharacters(t *testing.T) {
	for _, dir := range []string{"a[1]", "a[b"} {
		t.Run(dir, func(t *testing.T) {
			h := newHarness(t, 1)
			h.state = filepath.Join(h.root, dir, "pool")
			h.pool.Settings.State = h.state
			if err := os.MkdirAll(h.state, 0o700); err != nil {
				t.Fatal(err)
			}
			left := filepath.Join(h.state, "runcode-dp_0000000000000001")
			other := filepath.Join(h.state, "other")
			for _, f := range []string{left, other} {
				if err := os.WriteFile(f, []byte("left"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h.start()
			h.waitFor("a claim", func() bool {
				return slices.ContainsFunc(h.srv.Calls(), func(c provisionertest.Call) bool { return c.Route == provisionertest.RouteClaim })
			})
			if err := h.stop(); err != nil {
				t.Fatalf("the pool stopped with %v", err)
			}
			if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the run code's file is still there: %v", err)
			}
			if _, err := os.Stat(other); err != nil {
				t.Errorf("another file of the folder was removed: %v", err)
			}
		})
	}
}
