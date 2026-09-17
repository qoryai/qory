package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestFlightLineDrawsTheBeeWhereTheWorkIs is one frame at a time, colour off: with a
// total the bee stands as far along the track as the work is done, reaching the jar when
// all of it is; without one it makes the trip by steps and the line carries the seconds
// waited; and a terminal too narrow for a track gets the bee and the text alone.
func TestFlightLineDrawsTheBeeWhereTheWorkIs(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	u := New(&bytes.Buffer{})
	dots := func(n int) string { return strings.Repeat("·", n) }
	trail := func(n int) string { return strings.Repeat("━", n) }
	cases := []struct {
		name        string
		columns     int
		step        int
		done, total int64
		want        string
	}{
		{"nothing downloaded", 0, 7, 0, 7_400_000, "  " + Jar + dots(24) + Mark + "  fetching  0% of 7.4 MB"},
		{"half downloaded", 0, 7, 3_700_000, 7_400_000, "  " + Jar + dots(12) + Mark + trail(12) + "  fetching  50% of 7.4 MB"},
		{"all downloaded", 0, 7, 7_400_000, 7_400_000, "  " + Jar + Mark + trail(24) + "  fetching  100% of 7.4 MB"},
		{"more than the total", 0, 7, 9_000_000, 7_400_000, "  " + Jar + Mark + trail(24) + "  fetching  100% of 7.4 MB"},
		{"no total, seven steps in", 0, 7, 0, 0, "  " + Jar + dots(17) + Mark + trail(7) + "  fetching  3s"},
		{"no total, a second trip", 0, 27, 0, 0, "  " + Jar + dots(22) + Mark + trail(2) + "  fetching  3s"},
		{"a narrow terminal shortens the track", 32, 5, 0, 0, "  " + Jar + dots(6) + Mark + trail(5) + "  fetching  3s"},
		{"a narrower one drops it", 28, 5, 0, 0, "  " + Mark + " fetching  3s"},
	}
	for _, c := range cases {
		u.columns = c.columns
		if got := u.flightLine("fetching", c.step, c.done, c.total, 3*time.Second); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// TestFlightLandsAsALineOffATerminalToo is a flight on a writer that is no terminal,
// landed: nothing is drawn while it flies, and the step it finished is left as a line,
// the pot, the stripes over the track and the bee's place, what was done and the size of
// it or the seconds it took. A terminal too narrow for a track gets the line without one.
func TestFlightLandsAsALineOffATerminalToo(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	f := New(&buf).Fly("fetching")
	f.Progress(7_400_000, 7_400_000)
	f.Land("fetched")
	stripes := strings.Repeat("━", 26)
	if want := "  " + Pot + stripes + "  fetched  7.4 MB\n"; buf.String() != want {
		t.Errorf("wrote %q, want %q", buf.String(), want)
	}
	u := New(&buf)
	for _, c := range []struct {
		columns int
		elapsed time.Duration
		want    string
	}{
		{0, 41 * time.Second, "  " + Pot + stripes + "  built  41s"},
		{0, 300 * time.Millisecond, "  " + Pot + stripes + "  built"},
		{30, 41 * time.Second, "  " + Pot + strings.Repeat("━", 13) + "  built  41s"},
		{24, 41 * time.Second, "  " + Pot + " built  41s"},
	} {
		u.columns = c.columns
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
	time.Sleep(2 * flightTick)
	f.Stop()
	f.Stop()
	if buf.Len() != 0 {
		t.Errorf("wrote %q", buf.String())
	}
}
