package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// TestSwarmFrameIsTwoCellsOfBraille is the swarm frame by frame, colour off: indented as
// a row is, two cells wide, each cell a braille pattern or a space, with a dot for every
// bee's head at least and no more than a head and a trail for each.
func TestSwarmFrameIsTwoCellsOfBraille(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	u := New(&bytes.Buffer{})
	for step := range 500 {
		frame := u.swarmFrame(step)
		if !strings.HasPrefix(frame, "  ") || lipgloss.Width(frame) != 2+swarmCells {
			t.Fatalf("step %d: frame %q is not two cells after the indent", step, frame)
		}
		dots := 0
		for _, r := range []rune(frame)[2:] {
			switch {
			case r == ' ':
			case r >= 0x2800 && r <= 0x28FF:
				for bits := r - 0x2800; bits > 0; bits >>= 1 {
					dots += int(bits & 1)
				}
			default:
				t.Fatalf("step %d: frame %q holds %q, neither braille nor a space", step, frame, r)
			}
		}
		if dots < 1 || dots > 2*len(bees) {
			t.Fatalf("step %d: frame %q has %d dots, want 1 to %d", step, frame, dots, 2*len(bees))
		}
	}
}

// TestSwarmBeesStayInTheirSpaceAndMove is every bee over many steps: always inside the
// space, and moving, so the swarm never stands still.
func TestSwarmBeesStayInTheirSpaceAndMove(t *testing.T) {
	for i, b := range bees {
		seen := map[dot]bool{}
		for step := range 200 {
			d := b.at(float64(step))
			if d.x < 0 || d.x >= swarmDots || d.y < 0 || d.y >= swarmDots {
				t.Fatalf("bee %d at step %d is at %v, outside the space", i, step, d)
			}
			seen[d] = true
		}
		if len(seen) < swarmDots {
			t.Errorf("bee %d visits %d dots in 200 steps, want at least %d", i, len(seen), swarmDots)
		}
	}
}

// TestBuzzIsNilOffATerminal is a swarm asked for on a writer that is no terminal: there
// is none, and stopping it is harmless.
func TestBuzzIsNilOffATerminal(t *testing.T) {
	s := Buzz(&bytes.Buffer{})
	if s != nil {
		t.Fatalf("Buzz on a buffer = %v, want nil", s)
	}
	s.Stop()
}

// testSwarm starts a swarm drawing into a buffer, with the output long quiet, and
// returns it and what reads the buffer.
func testSwarm(t *testing.T) (*Swarm, func() string) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	sw := &swarm{
		term:     &buf,
		u:        New(&buf),
		fresh:    true,
		quiet:    time.Now().Add(-time.Hour),
		stop:     make(chan struct{}),
		finished: make(chan struct{}),
	}
	go sw.buzz()
	s := &Swarm{w: &buf, s: sw}
	t.Cleanup(s.Stop)
	return s, func() string {
		sw.mu.Lock()
		defer sw.mu.Unlock()
		return buf.String()
	}
}

// waitFor polls read until it holds want, for up to a second.
func waitFor(t *testing.T, read func() string, want string) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(swarmTick / 2) {
		if strings.Contains(read(), want) {
			return
		}
	}
	t.Fatalf("never wrote %q; wrote %q", want, read())
}

// TestSwarmClearsBeforeEveryWrite is a swarm drawn on quiet output, then written through:
// the frame is cleared and the cursor shown before the text, and the swarm keeps away
// until the output is quiet again. Stop clears nothing that is not drawn.
func TestSwarmClearsBeforeEveryWrite(t *testing.T) {
	s, read := testSwarm(t)
	waitFor(t, read, hideCursor+"\r  ")
	s.Write([]byte("removed\n"))
	out := read()
	if !strings.HasSuffix(out, clearLine+showCursor+"removed\n") {
		t.Fatalf("wrote %q, want the frame cleared before the text", out)
	}
	time.Sleep(swarmDelay / 2)
	s.Stop()
	if after := strings.TrimPrefix(read(), out); after != "" {
		t.Errorf("wrote %q after the text, before the output was quiet", after)
	}
}

// TestSwarmKeepsOffAQuestionsLine is a write that leaves the cursor after a question:
// the swarm stays away however long the answer takes, and comes back once the terminal
// has the cursor on a line of its own again.
func TestSwarmKeepsOffAQuestionsLine(t *testing.T) {
	s, read := testSwarm(t)
	s.Write([]byte("  delete the branch? [y/N] "))
	s.s.mu.Lock()
	s.s.quiet = time.Now().Add(-time.Hour)
	s.s.mu.Unlock()
	time.Sleep(4 * swarmTick)
	if out := read(); strings.Contains(out, hideCursor) {
		t.Fatalf("drew on the question's line: %q", out)
	}
	s.Returned()
	s.s.mu.Lock()
	s.s.quiet = time.Now().Add(-time.Hour)
	s.s.mu.Unlock()
	waitFor(t, read, hideCursor+"\r  ")
	s.Stop()
	if out := read(); !strings.HasSuffix(out, clearLine+showCursor) {
		t.Errorf("stopped with %q, want the frame cleared and the cursor shown", out)
	}
}
