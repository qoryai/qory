package pool_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/pool"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// The lines of the machine end gone.
const (
	lineGone       = "reported the dispatch's machine as gone"
	lineGoneFailed = "the report of the machine as gone failed, so it is sent again after a wait"
	lineGoneEnded  = "the machine could not be reported as gone, and the same report cannot succeed, so it is not sent again"
	lineGoneKept   = "the machine could not be reported as gone before the pool stopped, so the pool sends the report again at its next start"
)

// machineEnds are the states of the machine ends sent for the dispatch id, in order,
// with the status each got.
func (h *harness) machineEnds(id string) (states []string, statuses []int) {
	h.t.Helper()
	for _, c := range h.srv.Calls() {
		if c.Route != provisionertest.RouteMachineEnd || c.DispatchID != id {
			continue
		}
		var me contracts.MachineEnd
		if err := json.Unmarshal(c.Body, &me); err != nil {
			h.t.Fatal(err)
		}
		states, statuses = append(states, me.State), append(statuses, c.Status)
	}
	return states, statuses
}

// indexOf is the index of the first line logged with the message msg for the dispatch
// id, or -1.
func (h *harness) indexOf(msg, id string) int {
	return slices.IndexFunc(h.log.all(), func(m map[string]any) bool { return m["msg"] == msg && m["dispatch_id"] == id })
}

// TestGoneFollowsTheMachineLog runs a dispatch whose qory run exits 3, and one whose
// qory run cannot start: each gets the machine log, then the machine end gone, once,
// answered 204. The journal entry is at ended while gone is held, before the first gone
// went out; once gone got the 204, the entry and the dispatch's folder are gone. The
// lines are logged in that order: qory run's end, the upload, gone.
func TestGoneFollowsTheMachineLog(t *testing.T) {
	for _, name := range []string{"exits", "cannot start"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 1)
			end := lineEnded
			if name == "cannot start" {
				h.pool.Settings.Qory = filepath.Join(h.root, "no-qory")
				end = lineNotStarted
			} else {
				h.script(script{Exit: 3})
			}
			let := h.gate.holdNext("/machine-end")
			h.srv.Offer(provisionertest.Run{})
			h.start()
			<-h.gate.reached
			d := h.only()
			if lines, want := journalOf(t, h.state), d.ID+" "+pool.PhaseEnded; len(lines) != 1 || lines[0] != want {
				t.Errorf("the journal while gone is held is %q, want %q", lines, want)
			}
			let()
			h.settled(1)
			h.stop()
			calls := h.srv.Calls()
			var routes []provisionertest.Route
			for _, c := range calls {
				if c.Route != provisionertest.RouteClaim {
					routes = append(routes, c.Route)
				}
			}
			if want := []provisionertest.Route{provisionertest.RouteDispatched, provisionertest.RouteMachineLog, provisionertest.RouteMachineEnd}; !slices.Equal(routes, want) {
				t.Errorf("the fake was sent %v besides claims, want %v", routes, want)
			}
			if states, statuses := h.machineEnds(d.ID); !slices.Equal(states, []string{"gone"}) || statuses[0] != 204 {
				t.Errorf("the machine ends are %q, answered %v", states, statuses)
			}
			ended, uploaded, gone := h.indexOf(end, d.ID), h.indexOf(lineLogUploaded, d.ID), h.indexOf(lineGone, d.ID)
			if ended < 0 || uploaded < ended || gone < uploaded {
				t.Errorf("the lines are not end, upload, gone:\n%s", h.log.text())
			}
			if _, err := os.Stat(filepath.Join(h.state, "runs", d.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the dispatch's folder is still there: %v", err)
			}
			h.notReleased()
		})
	}
}

// TestGoneIsSentAgain answers gone with a signed 503 unavailable, an unsigned 401, each
// on every try of the client, and a signed 429 with Retry-After 7: the pool sends gone
// again after 1 s, 2 s, then the 7 s of the 429, each built anew, and logs each wait
// with the dispatch, the wait and the error. The fourth gone gets the 204.
func TestGoneIsSentAgain(t *testing.T) {
	five := func(r provisionertest.Reply) []provisionertest.Reply {
		return slices.Repeat([]provisionertest.Reply{r}, 5)
	}
	h := newHarness(t, 1)
	h.srv.Script(provisionertest.RouteMachineEnd, five(provisionertest.Reply{Status: 503, Code: "unavailable"})...)
	h.srv.Script(provisionertest.RouteMachineEnd, five(provisionertest.Reply{Status: 401, Code: "unauthorized", Unsigned: true})...)
	h.srv.Script(provisionertest.RouteMachineEnd, provisionertest.Reply{Status: 429, Code: "rate_limited", RetryAfter: "7"})
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.settled(1)
	h.stop()
	d := h.only()
	states, statuses := h.machineEnds(d.ID)
	if len(states) != 12 || statuses[11] != 204 || slices.ContainsFunc(states, func(s string) bool { return s != "gone" }) {
		t.Errorf("the machine ends are %q, answered %v", states, statuses)
	}
	var waits []time.Duration
	for _, w := range h.waitsOf() {
		if w != heartbeat && w != lifetime {
			waits = append(waits, w)
		}
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 7 * time.Second}; !slices.Equal(waits, want) {
		t.Errorf("the pool waited %v, want %v", waits, want)
	}
	m := h.log.with(lineGoneFailed)
	wantErrors := []string{"503 unavailable", "401 unauthorized", "429 rate_limited"}
	if len(m) != 3 {
		t.Fatalf("the failed gone's lines are %v", m)
	}
	for i, line := range m {
		if line["dispatch_id"] != d.ID || line["wait"] != float64(waits[i]) || !strings.Contains(line["error"].(string), wantErrors[i]) {
			t.Errorf("failed gone %d is logged %v", i, line)
		}
	}
	if len(h.log.with(lineGone)) != 1 {
		t.Errorf("the gone's lines are %v", h.log.with(lineGone))
	}
	seen := map[string]bool{}
	for _, c := range h.srv.Calls() {
		if c.Route != provisionertest.RouteMachineEnd {
			continue
		}
		if sig := c.Header.Get(accesskey.HeaderSignature); c.Replayed || seen[sig] {
			t.Error("a gone was sent with a signature sent before")
		} else {
			seen[sig] = true
		}
	}
}

// TestGoneEndsAtASignedRefusal answers gone with a signed 403 forbidden and a signed
// 404 not_found: the pool sends it once, logs that the same report cannot succeed, with
// the error, and takes the dispatch out of the journal and its folder away.
func TestGoneEndsAtASignedRefusal(t *testing.T) {
	for _, rep := range []provisionertest.Reply{{Status: 403, Code: "forbidden"}, {Status: 404, Code: "not_found"}} {
		t.Run(rep.Code, func(t *testing.T) {
			h := newHarness(t, 1)
			h.srv.Script(provisionertest.RouteMachineEnd, rep)
			h.srv.Offer(provisionertest.Run{})
			h.start()
			h.settled(1)
			h.stop()
			d := h.only()
			if states, statuses := h.machineEnds(d.ID); len(states) != 1 || statuses[0] != rep.Status {
				t.Errorf("the machine ends are %q, answered %v", states, statuses)
			}
			if m := h.log.with(lineGoneEnded); len(m) != 1 || m[0]["dispatch_id"] != d.ID || !strings.Contains(m[0]["error"].(string), rep.Code) {
				t.Errorf("the gone's lines are %v", m)
			}
			if len(h.log.with(lineGone)) != 0 || len(h.log.with(lineGoneFailed)) != 0 {
				t.Errorf("the log:\n%s", h.log.text())
			}
			if _, err := os.Stat(filepath.Join(h.state, "runs", d.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the dispatch's folder is still there: %v", err)
			}
		})
	}
}

// TestGoneIsKeptAtAStopAndSentAtTheNextStart stops the pool while qory run runs, with
// gone failing on every try: the pool sends gone once after the stop, logs that it
// sends it again at its next start, and Run returns, with the entry at ended and the
// dispatch's folder kept. The pool started again sends gone for the entry beside its
// claims: a claim goes out while that gone is held. Once gone got the 204, the entry
// and the folder are gone.
func TestGoneIsKeptAtAStopAndSentAtTheNextStart(t *testing.T) {
	h := newHarness(t, 1)
	h.script(script{Block: true})
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.waitFor("the dispatch", func() bool { return len(h.srv.Dispatches()) == 1 })
	d := h.only()
	h.ready(d.ID)
	h.srv.Script(provisionertest.RouteMachineEnd, slices.Repeat([]provisionertest.Reply{{Status: 503, Unsigned: true}}, 5)...)
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if states, _ := h.machineEnds(d.ID); len(states) != 5 {
		t.Errorf("%d tries of gone after the stop, want the client's 5", len(states))
	}
	if m := h.log.with(lineGoneKept); len(m) != 1 || m[0]["dispatch_id"] != d.ID || m[0]["error"] == nil {
		t.Errorf("the kept gone's lines are %v", m)
	}
	if lines, want := journalOf(t, h.state), d.ID+" "+pool.PhaseEnded; len(lines) != 1 || lines[0] != want {
		t.Fatalf("the journal after the stop is %q, want %q", lines, want)
	}
	dir := filepath.Join(h.state, "runs", d.ID)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the dispatch's folder is gone at the stop: %v", err)
	}

	let := h.gate.holdNext("/machine-end")
	before := len(h.srv.Calls())
	h.start()
	<-h.gate.reached
	h.waitFor("a claim while gone is held", func() bool {
		return slices.ContainsFunc(h.srv.Calls()[before:], func(c provisionertest.Call) bool { return c.Route == provisionertest.RouteClaim })
	})
	let()
	h.waitFor("the gone's line", func() bool { return len(h.log.with(lineGone)) == 1 })
	h.stop()
	if states, statuses := h.machineEnds(d.ID); len(states) != 6 || statuses[5] != 204 {
		t.Errorf("the machine ends are %q, answered %v", states, statuses)
	}
	if lines := journalOf(t, h.state); len(lines) != 0 {
		t.Errorf("the journal is %q", lines)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dispatch's folder is still there: %v", err)
	}
	h.notReleased()
}

// TestAReplayedMachineExistsGetsGone starts a pool with a journal of a claimed entry
// whose dispatch the fake took dispatched for, and an ended entry: the release of the
// first returns 409 machine_exists, and both get gone, answered 204, so both leave the
// journal with their folders.
func TestAReplayedMachineExistsGetsGone(t *testing.T) {
	h := newHarness(t, 1)
	h.srv.Offer(provisionertest.Run{}, provisionertest.Run{})
	claimed := claimTo(t, h.srv, pool.PhaseDispatched)
	claimed.Phase = pool.PhaseClaimed
	ended := claimTo(t, h.srv, pool.PhaseDispatched)
	ended.Phase = pool.PhaseEnded
	var journal string
	for _, e := range []pool.Entry{claimed, ended} {
		journal += e.DispatchID + " " + e.Phase + "\n"
		if err := os.MkdirAll(filepath.Join(h.state, "runs", e.DispatchID, "work"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.state, pool.JournalFile), []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.waitFor("both gones' lines", func() bool { return len(h.log.with(lineGone)) == 2 })
	h.stop()
	if m := h.log.with(lineNotReleased); len(m) != 1 || m[0]["dispatch_id"] != claimed.DispatchID || !strings.Contains(m[0]["error"].(string), "409 machine_exists") {
		t.Errorf("the release's lines are %v", m)
	}
	for _, e := range []pool.Entry{claimed, ended} {
		if states, statuses := h.machineEnds(e.DispatchID); !slices.Equal(states, []string{"gone"}) || statuses[0] != 204 {
			t.Errorf("%s: the machine ends are %q, answered %v", e.Phase, states, statuses)
		}
		if _, err := os.Stat(filepath.Join(h.state, "runs", e.DispatchID)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: the dispatch's folder is still there: %v", e.Phase, err)
		}
	}
	if lines := journalOf(t, h.state); len(lines) != 0 {
		t.Errorf("the journal is %q", lines)
	}
}
