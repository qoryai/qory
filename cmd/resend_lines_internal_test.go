package cmd

import (
	"fmt"
	"slices"
	"testing"

	"github.com/qoryai/forager/gateway"
)

// TestResendLinesLeaveOutForagersRepeats pins what both resends, to the server and
// behind a gateway, pass on of Forager's report lines: when qory says itself that the run
// never opened, Forager's gateway.ResendNotOpened and gateway.ResendNoServer are left
// out, and when qory says itself that events were not accepted, or that the server wants
// no more events of the run, so is Forager's line of them; every other line, the torn lines' and the server's stop among them, is passed
// on in order. What qory does not say is passed on.
func TestResendLinesLeaveOutForagersRepeats(t *testing.T) {
	torn := fmt.Sprintf(gateway.ResendTorn, 2, "/run/events.jsonl")
	const stop = "the server said stop during the run; nothing is sent"
	const undelivered = "2 events were not accepted by the server; see /run/undelivered"
	const serverStop = "the server wants no more events of this run; the run goes on"
	for _, c := range []struct {
		name     string
		reported []string
		said     []int
		want     []string
	}{
		{"never opened", []string{gateway.ResendNotOpened}, []int{heldNotOpened}, nil},
		{"no server", []string{gateway.ResendNoServer}, []int{heldNotOpened}, nil},
		{"torn, never opened", []string{torn, gateway.ResendNotOpened}, []int{heldNotOpened}, []string{torn}},
		{"torn, no server", []string{torn, gateway.ResendNoServer}, []int{heldNotOpened}, []string{torn}},
		{"stop kept", []string{stop, gateway.ResendNotOpened}, []int{heldNotOpened}, []string{stop}},
		{"undelivered said", []string{undelivered, stop}, []int{heldUndelivered}, []string{stop}},
		{"undelivered not said", []string{undelivered}, nil, []string{undelivered}},
		{"the server's stop said", []string{serverStop, torn}, []int{heldServerStop}, []string{torn}},
		{"the server's stop not said", []string{serverStop}, nil, []string{serverStop}},
		{"opened: never opened kept", []string{gateway.ResendNotOpened}, nil, []string{gateway.ResendNotOpened}},
		{"opened: no server kept", []string{gateway.ResendNoServer}, nil, []string{gateway.ResendNoServer}},
		{"opened: all kept", []string{torn, stop}, nil, []string{torn, stop}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			lines := resendLines(func(l string) { got = append(got, l) })
			for _, l := range c.reported {
				lines.line(l)
			}
			lines.done(func(kind int) bool { return slices.Contains(c.said, kind) })
			if !slices.Equal(got, c.want) {
				t.Errorf("passed on %q, want %q", got, c.want)
			}
		})
	}
}

// TestRunLinesLeaveOutForagersRepeats pins what qory run passes on of Forager's report
// lines: the session's at the time limit, the gateway's of events the server did not
// accept, and the gateway's that it ends this run, are left out when qory's own line
// says the same, and passed on, after the others, when it does not. The gateway's line
// of another run, and every other line, is passed on at once.
func TestRunLinesLeaveOutForagersRepeats(t *testing.T) {
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f"
	const (
		limit       = "the runtime was stopped at the limit of 5m0s"
		undelivered = "3 events were not accepted by the server; see /runs/x/undelivered"
		silent      = "run " + id + ": its session sent nothing for 1m30s; the run ends, session_lost"
		refused     = "run " + id + ": the gateway refused a batch of its session's, too large; the run ends: failed"
		other       = "run 0191f2a4-3c5e-7b8d-9e0f-000000000000: its session sent nothing for 1m30s; the run ends, session_lost"
		sinks       = "closing the sinks: context deadline exceeded"
	)
	for _, c := range []struct {
		name     string
		reported []string
		said     []int
		want     []string
	}{
		{"all said", []string{limit, undelivered, silent, refused, sinks}, []int{heldLimit, heldUndelivered, heldRunEnds}, []string{sinks}},
		{"none said", []string{limit, sinks, silent}, nil, []string{sinks, limit, silent}},
		{"another run's", []string{other}, []int{heldRunEnds}, []string{other}},
		{"the end said, the limit not", []string{limit, silent}, []int{heldRunEnds}, []string{limit}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			lines := runLines(func(l string) { got = append(got, l) }, id)
			for _, l := range c.reported {
				lines.line(l)
			}
			lines.done(func(kind int) bool { return slices.Contains(c.said, kind) })
			if !slices.Equal(got, c.want) {
				t.Errorf("passed on %q, want %q", got, c.want)
			}
		})
	}
	var got []string
	lines := runLines(func(l string) { got = append(got, l) }, id)
	lines.line(limit)
	lines.done(nil)
	if !slices.Equal(got, []string{limit}) {
		t.Errorf("done(nil) passed on %q", got)
	}
}
