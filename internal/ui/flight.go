package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// A Flight is the line a command shows while it waits: the bee flying to the jar, its
// stripes trailing behind it, then what is being waited for.
//
//	🫙·········🐝━━━━━━━━━━━━━━━  downloading qory 0.6.0  62% of 7.4 MB
//
// The bee flies right to left, the way the glyph faces. With a total known, from
// [Flight.Progress], where it is along the track is how much is done, and it reaches the
// jar when all of it is. Without one it makes the trip over and over, and the line shows
// how long the wait has been.
//
// The line is redrawn in place, so it is drawn on a terminal only, and not when TERM is
// dumb or CI is set. Anywhere else a Flight draws nothing, and the command's output is
// the same text without it. A flight ends one of two ways. [Flight.Stop] clears the
// line, so nothing of it is left above what the command prints next, which is for a wait
// whose result the command reports itself, and for one that failed. [Flight.Land]
// leaves the line where it is, the bee home, the jar full and the stripes all the way
// along the track, and shows what was done in place of what was waited for:
//
//	🍯━━━━━━━━━━━━━━━━━━━━━━━━━━  downloaded qory 0.6.0  7.4 MB
//
// and prints that line off a terminal too, so a command of several steps lists each one
// it finished wherever it runs.
//
// The flight draws from its own goroutine. Between [UI.Fly] and [Flight.Stop] nothing
// else may print through the UI or to its writer.
type Flight struct {
	u     *UI
	label string
	start time.Time

	mu          sync.Mutex
	done, total int64

	stop     chan struct{}
	finished chan struct{}
}

const (
	// flightTick is how often the line is redrawn, and how long the bee takes over one
	// cell of a trip with no total.
	flightTick = 80 * time.Millisecond
	// flightTrack is how many cells lie between the jar and the right end of the track,
	// on a terminal wide enough for them, and flightTrackMin the fewest worth drawing.
	flightTrack    = 24
	flightTrackMin = 8
)

// Fly starts a flight labelled with what is being waited for, and returns it. The
// caller ends it, on every path, with [Flight.Stop] or [Flight.Land], before it prints
// anything else.
func (u *UI) Fly(label string) *Flight {
	f := &Flight{u: u, label: label, start: time.Now()}
	if u.columns == 0 || os.Getenv("TERM") == "dumb" || os.Getenv("CI") != "" {
		return f
	}
	f.stop = make(chan struct{})
	f.finished = make(chan struct{})
	go f.fly()
	return f
}

// Progress records that done of total is complete, bytes of a download. A total of zero
// or less leaves the flight without one. It is safe to call from any goroutine.
func (f *Flight) Progress(done, total int64) {
	f.mu.Lock()
	f.done, f.total = done, total
	f.mu.Unlock()
}

// Stop ends the flight and clears its line. It returns once the last frame is gone, so
// what the caller prints next starts on a clean line.
func (f *Flight) Stop() {
	if f.stop == nil {
		return
	}
	close(f.stop)
	<-f.finished
	f.stop = nil
}

// Land ends the flight and leaves its line in place, finished: the pot where the jar
// was, the stripes along the whole track, then text, which states what was done, then
// faint what it took, the size when a total was set and else the
// seconds waited, when that is a second or more. The line is printed wherever the UI
// writes, a terminal or not.
func (f *Flight) Land(text string) {
	f.Stop()
	f.mu.Lock()
	total := f.total
	f.mu.Unlock()
	fmt.Fprintln(f.u.w, f.u.landed(text, total, time.Since(f.start)))
}

// landed is the line [Flight.Land] leaves.
func (u *UI) landed(text string, total int64, elapsed time.Duration) string {
	took := ""
	switch {
	case total > 0:
		took = "  " + megabytes(total)
	case elapsed >= time.Second:
		took = fmt.Sprintf("  %ds", int(elapsed.Seconds()))
	}
	track := u.track(text + took)
	if track < flightTrackMin {
		return "  " + Pot + " " + text + u.key.Render(took)
	}
	// The stripes take the bee's two columns too, so the text stays where it was.
	track += 2
	cells := make([]string, track)
	steps := make([]int, track)
	for i := range cells {
		cells[i] = "━"
		steps[i] = track - 1 - i
	}
	return "  " + Pot + u.edge(cells, steps) + "  " + text + u.key.Render(took)
}

// track is how many cells the track of a flight gets beside text: [flightTrack], or as
// many as keep the line inside the terminal. That is the width less text, two columns of
// margin, two each for the jar and the bee, two before the text, and one spare so the
// cursor never sits in the last column.
func (u *UI) track(text string) int {
	if u.columns == 0 {
		return flightTrack
	}
	return min(flightTrack, u.columns-lipgloss.Width(text)-9)
}

// clearLine returns the cursor to the start of the line and erases the line.
const clearLine = "\r\x1b[2K"

// fly redraws the line every tick until the flight is stopped, then clears it. The first
// frame is drawn after one tick, so a wait shorter than that shows nothing.
func (f *Flight) fly() {
	defer close(f.finished)
	ticker := time.NewTicker(flightTick)
	defer ticker.Stop()
	drawn := false
	for step := 0; ; step++ {
		select {
		case <-f.stop:
			if drawn {
				fmt.Fprint(f.u.w, clearLine)
			}
			return
		case <-ticker.C:
			f.mu.Lock()
			done, total := f.done, f.total
			f.mu.Unlock()
			fmt.Fprint(f.u.w, clearLine+f.u.flightLine(f.label, step, done, total, time.Since(f.start)))
			drawn = true
		}
	}
}

// flightLine is one frame of a flight: the jar, the cells still ahead of the bee, the bee,
// its stripes back to the right end of the track, the label, and how far along the wait
// is. The track is shortened to keep the line inside the terminal, since a line that
// wraps cannot be redrawn in place, and left out when there is no room for one.
func (u *UI) flightLine(label string, step int, done, total int64, elapsed time.Duration) string {
	text := label + "  "
	if total > 0 {
		done = max(0, min(done, total))
		text += fmt.Sprintf("%d%% of %s", done*100/total, megabytes(total))
	} else {
		text += fmt.Sprintf("%ds", int(elapsed.Seconds()))
	}
	track := u.track(text)
	if track < flightTrackMin {
		return "  " + Mark + " " + text
	}
	flown := step % (track + 1)
	if total > 0 {
		flown = int(done * int64(track) / total)
	}
	// The stripes are counted from the right end, where the bee set off, so a stripe
	// stays where it is as the bee flies on.
	cells := make([]string, flown)
	steps := make([]int, flown)
	for i := range cells {
		cells[i] = "━"
		steps[i] = flown - 1 - i
	}
	return "  " + Jar + u.key.Render(strings.Repeat("·", track-flown)) + Mark + u.edge(cells, steps) + "  " + text
}

// megabytes writes a count of bytes as megabytes to one decimal place.
func megabytes(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}
