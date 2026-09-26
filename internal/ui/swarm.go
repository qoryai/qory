package ui

import (
	"bytes"
	"io"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
)

// A Swarm is what a command shows while it works and has nothing to print: a few bees
// circling in a space one row high and two cells wide, on the line under the last one
// printed.
//
//	🐝 main · worktree remove ../wt-feature
//	  ⢌⠢
//
// Each bee is two dots, its head in the yellow of the stripes and where it just was in
// their black, and each circles on its own orbit at its own speed, so the swarm never
// quite repeats. The dots are braille, eight to a cell, and a terminal draws a cell in
// one colour, so a cell takes the colour of most of its dots, yellow on a tie.
//
// A [Flight] stands for one wait the command labels and measures. A swarm labels
// nothing: it stands under a whole command, and shows only once the output has been
// quiet for [swarmDelay] with the cursor at the start of a line, so a command that
// answers at once never shows it, and a question waiting on a line of its own keeps its
// line.
//
// A Swarm is a writer. The command prints through it, and every write clears the swarm
// first, then lets the swarm come back once the output is quiet again. What prints to the
// terminal around it, such as a child process that has the terminal, is drawn over while
// the swarm is on, so the swarm stays off while a command passes the terminal to another program. The
// cursor is hidden while the swarm is drawn, and shown again when it is cleared, when it
// stops, and on an interrupt, before the signal is let through.
type Swarm struct {
	w io.Writer
	s *swarm
}

// swarm is the state the writers of one swarm share: [Buzz]'s and those of
// [Swarm.Beside], which print to the same terminal.
type swarm struct {
	term io.Writer // where the frames go, the writer given to Buzz
	u    *UI       // the styles of term

	mu    sync.Mutex
	drawn bool      // a frame is on the terminal and the cursor hidden
	fresh bool      // the cursor is at the start of an empty line
	quiet time.Time // when the last write was

	stop     chan struct{}
	finished chan struct{}
	once     sync.Once
}

const (
	// swarmTick is how often the swarm is redrawn.
	swarmTick = 60 * time.Millisecond
	// swarmDelay is how long the output has to be quiet before the swarm shows.
	swarmDelay = 250 * time.Millisecond

	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
)

// Buzz starts a swarm under what is printed to w, and returns the writer to print
// through in place of w. It returns nil when w is no terminal, or TERM is dumb or CI is
// set, where the caller prints to w as it did. The caller stops the swarm with
// [Swarm.Stop], on every path, before anything prints to w other than through it.
func Buzz(w io.Writer) *Swarm {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) || os.Getenv("TERM") == "dumb" || os.Getenv("CI") != "" {
		return nil
	}
	s := &swarm{
		term:     w,
		u:        New(w),
		fresh:    true,
		quiet:    time.Now(),
		stop:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	go s.buzz()
	return &Swarm{w: w, s: s}
}

// Beside returns w to print through beside the swarm when w is a terminal too, the
// standard error of a command whose output the swarm is under, so a line written there
// also clears the swarm first. Any other w is returned as it is.
func (s *Swarm) Beside(w io.Writer) io.Writer {
	if f, ok := w.(*os.File); !ok || !term.IsTerminal(f.Fd()) {
		return w
	}
	return &Swarm{w: w, s: s.s}
}

// Write clears the swarm, then writes p to the terminal. The swarm comes back once the
// output has been quiet for a while, if p leaves the cursor at the start of a line.
func (s *Swarm) Write(p []byte) (int, error) {
	sw := s.s
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.clear()
	n, err := s.w.Write(p)
	if len(p) > 0 {
		sw.fresh = p[len(p)-1] == '\n' || bytes.HasSuffix(p, []byte(clearLine))
		sw.quiet = time.Now()
	}
	return n, err
}

// Returned records that the cursor is at the start of a line although nothing printed
// through it put it there: the terminal's echo of an answer typed to a question put
// it there. Without it the swarm stays away until the next line is printed.
func (s *Swarm) Returned() {
	s.s.mu.Lock()
	s.s.fresh = true
	s.s.quiet = time.Now()
	s.s.mu.Unlock()
}

// Stop ends the swarm and clears it. It returns once the swarm is gone, so what is
// printed next starts on a clean line. Stop may be called on a nil Swarm, and more than
// once.
func (s *Swarm) Stop() {
	if s == nil {
		return
	}
	s.s.once.Do(func() {
		close(s.s.stop)
		<-s.s.finished
	})
}

// clear erases the frame on the terminal, if one is drawn, and shows the cursor again.
// The caller holds mu.
func (sw *swarm) clear() {
	if sw.drawn {
		io.WriteString(sw.term, clearLine+showCursor)
		sw.drawn = false
	}
}

// buzz draws a frame every tick the output is quiet and the cursor at the start of a
// line, until the swarm is stopped or the process interrupted.
func (sw *swarm) buzz() {
	defer close(sw.finished)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ticker := time.NewTicker(swarmTick)
	defer ticker.Stop()
	for step := 0; ; {
		select {
		case <-sw.stop:
			sw.mu.Lock()
			sw.clear()
			sw.mu.Unlock()
			return
		case sig := <-signals:
			// The cursor is shown again before the signal does what it would have, so
			// an interrupted command does not leave the shell without one.
			sw.mu.Lock()
			sw.clear()
			signal.Stop(signals)
			if p, err := os.FindProcess(os.Getpid()); err == nil && p.Signal(sig) == nil {
				time.Sleep(time.Second)
			}
			os.Exit(130)
		case <-ticker.C:
			sw.mu.Lock()
			if sw.fresh && time.Since(sw.quiet) >= swarmDelay {
				frame := "\r" + sw.u.swarmFrame(step)
				if !sw.drawn {
					frame = hideCursor + frame
				}
				io.WriteString(sw.term, frame)
				sw.drawn = true
				step++
			}
			sw.mu.Unlock()
		}
	}
}

// A bee is one bee of the swarm: how fast it circles, in radians a step, negative
// against the others; where on its orbit it starts; how far out it flies; and how fast
// that distance swells and shrinks.
type bee struct {
	speed, phase, radius, wobble float64
}

// bees are the swarm. Their speeds share no small multiple, so the pattern they make
// takes long to come round again.
var bees = []bee{
	{speed: 0.50, phase: 0, radius: 1.5, wobble: 0.13},
	{speed: 0.37, phase: 2.1, radius: 1.2, wobble: 0.21},
	{speed: -0.44, phase: 4.2, radius: 1.4, wobble: 0.17},
}

// swarmCells is how many cells wide the swarm is, and swarmDots how many dots it is
// across and down: two to a cell across, four down.
const (
	swarmCells = 2
	swarmDots  = 2 * swarmCells
)

// dot is one dot of the swarm's space, counted from its top left.
type dot struct{ x, y int }

// at is where b is at step t, a fraction of a step allowed.
func (b bee) at(t float64) dot {
	a := b.speed*t + b.phase
	r := b.radius + 0.4*math.Sin(b.wobble*t+b.phase)
	c := float64(swarmDots-1) / 2
	place := func(v float64) int {
		return max(0, min(swarmDots-1, int(math.Round(c+r*v))))
	}
	return dot{place(math.Cos(a)), place(math.Sin(a))}
}

// behind is where b was last before it came to where it is at step t, or where it is if
// it has not moved for a while.
func (b bee) behind(t float64) dot {
	now := b.at(t)
	for back := 0.25; back <= 3; back += 0.25 {
		if d := b.at(t - back); d != now {
			return d
		}
	}
	return now
}

// brailleBits is the bit of a braille pattern that raises the dot x across and y down
// within its cell.
var brailleBits = [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

// swarmFrame is the swarm at step, indented as a row is: every bee's head yellow and the
// dot behind it black, one braille cell for each two dots across, a cell with no dot a
// space.
func (u *UI) swarmFrame(step int) string {
	var heads, tails [swarmDots][swarmDots]bool
	for _, b := range bees {
		t := float64(step)
		h := b.at(t)
		heads[h.x][h.y] = true
		if d := b.behind(t); d != h {
			tails[d.x][d.y] = true
		}
	}
	var out strings.Builder
	out.WriteString("  ")
	for c := range swarmCells {
		var bits rune
		yellow, black := 0, 0
		for x := range 2 {
			for y := range swarmDots {
				switch {
				case heads[2*c+x][y]:
					yellow++
				case tails[2*c+x][y]:
					black++
				default:
					continue
				}
				bits |= brailleBits[x][y]
			}
		}
		switch {
		case bits == 0:
			out.WriteString(" ")
		case yellow >= black:
			out.WriteString(u.yellow.Render(string(0x2800 + bits)))
		default:
			out.WriteString(u.black.Render(string(0x2800 + bits)))
		}
	}
	return out.String()
}
