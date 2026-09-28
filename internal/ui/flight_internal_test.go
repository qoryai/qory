package ui

import (
	"bytes"
	"testing"
	"time"
)

// TestFlightLineDrawsTheSwarmBesideTheWait is one frame at a time, colour off: the swarm
// at that step, then the label, then the share of the total done, capped at all of it,
// or without a total the seconds waited.
func TestFlightLineDrawsTheSwarmBesideTheWait(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	u := New(&bytes.Buffer{})
	cases := []struct {
		name        string
		step        int
		done, total int64
		want        string
	}{
		{"nothing downloaded", 7, 0, 7_400_000, "  fetching  0% of 7.4 MB"},
		{"half downloaded", 7, 3_700_000, 7_400_000, "  fetching  50% of 7.4 MB"},
		{"all downloaded", 7, 7_400_000, 7_400_000, "  fetching  100% of 7.4 MB"},
		{"more than the total", 7, 9_000_000, 7_400_000, "  fetching  100% of 7.4 MB"},
		{"no total", 7, 0, 0, "  fetching  3s"},
		{"no total, later", 27, 0, 0, "  fetching  3s"},
	}
	for _, c := range cases {
		want := u.swarmFrame(c.step) + c.want
		if got := u.flightLine("fetching", c.step, c.done, c.total, 3*time.Second); got != want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, want)
		}
	}
}

// TestFlightLandsAsALineOffATerminalToo is a flight on a writer that is no terminal,
// landed: nothing is drawn while it flies, and the step it finished is left as a line,
// the pot in the swarm's two columns, what was done and the size of it or the seconds
// it took.
func TestFlightLandsAsALineOffATerminalToo(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	f := New(&buf).Fly("fetching")
	f.Progress(7_400_000, 7_400_000)
	f.Land("fetched")
	if want := "  " + Pot + "  fetched  7.4 MB\n"; buf.String() != want {
		t.Errorf("wrote %q, want %q", buf.String(), want)
	}
	u := New(&buf)
	for _, c := range []struct {
		elapsed time.Duration
		want    string
	}{
		{41 * time.Second, "  " + Pot + "  built  41s"},
		{300 * time.Millisecond, "  " + Pot + "  built"},
	} {
		if got := u.landed("built", 0, c.elapsed); got != c.want {
			t.Errorf("landed after %s = %q, want %q", c.elapsed, got, c.want)
		}
	}
}

// TestFlightDrawsNothingOffATerminal is a flight on a writer that is no terminal: it
// writes nothing, takes progress, and stops, more than once.
func TestFlightDrawsNothingOffATerminal(t *testing.T) {
	var buf bytes.Buffer
	f := New(&buf).Fly("fetching")
	f.Progress(1, 2)
	time.Sleep(2 * swarmTick)
	f.Stop()
	f.Stop()
	if buf.Len() != 0 {
		t.Errorf("wrote %q", buf.String())
	}
}
