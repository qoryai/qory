// Package statusfd writes qory run's lines to a second place, the file descriptor that
// QORY_STATUS_FD is set to, beside stderr.
//
// A starter that shows a person qory's lines, and not the agent's output, starts qory
// run with QORY_STATUS_FD=3 and descriptor 3 open, and reads that descriptor. qory run
// writes its lines there: the lines of the ui package that start with ✓, ✗ or ⚠ and
// their continuations, its "qory run:" lines, Forager's lines among them, and the last
// error line, an error of qory run's flags included, each only when stderr shows it too,
// see [Shown]. Output that qory forwards from a program it starts, such as the agent,
// never goes there, and neither do the lines of a prebuild, which contain the output of
// its build.
//
// [Claim] reads the variable once, first in the main package, and [Setup] starts the
// writes: the main package calls it for qory run before cobra parses the flags. [Line]
// writes one of qory's lines, and [Tee] returns a writer that also writes each line it
// gets there. Each line is masked with [secretmask.Default], the run's mask list, and
// written whole, in one write. A descriptor that fails a write gets no more lines, and
// the run goes on.
package statusfd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/qoryai/qory/internal/secretmask"
)

// Env is the variable that sets the descriptor.
const Env = "QORY_STATUS_FD"

var (
	// mu guards the variables below.
	mu sync.Mutex
	// file is the descriptor that qory's lines go to; nil when there is none, or when a
	// write to it failed.
	file *os.File
	// claimed is set once Claim has read the variable, until Setup or Close.
	claimed bool
	// held is the descriptor that Claim kept, which Setup makes file.
	held *os.File
	// problem is the line on a value that Claim could not use, for Setup to print.
	problem string
	// stderr is the writer Setup got: where a failed write prints its line, and where
	// [Shown] checks that a line goes.
	stderr io.Writer
)

// Claim reads [Env], removes it from the environment and, when it is set to an open
// descriptor above 2, marks the descriptor close-on-exec and keeps it, without writing to
// it. No program that qory starts then receives either: such a program could write output
// that bypasses the masking. The main package calls Claim before it opens any file, so a
// file that qory opens cannot take the number of a descriptor that was not passed and
// look like it. A value that is not a number, 0, 1 or 2, or a descriptor that is not
// open is kept for [Setup] to report. An empty value is the variable unset.
func Claim() {
	mu.Lock()
	defer mu.Unlock()
	claim()
}

// claim is [Claim]; the caller holds mu.
func claim() {
	claimed = true
	problem = ""
	v, ok := os.LookupEnv(Env)
	if !ok {
		return
	}
	os.Unsetenv(Env)
	if v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	switch {
	case err != nil || n < 0:
		problem = fmt.Sprintf("qory run: set %s to the number of an open file descriptor above 2, or unset it: %q is not one; qory run writes its lines to stderr only\n", Env, v)
		return
	case n <= 2:
		problem = fmt.Sprintf("qory run: set %s to a file descriptor above 2, or unset it: %d is stdin, stdout or stderr; qory run writes its lines to stderr only\n", Env, n)
		return
	}
	f, err := open(n)
	if err != nil {
		problem = fmt.Sprintf("qory run: open descriptor %d for qory run, or unset %s: %v; qory run writes its lines to stderr only\n", n, Env, err)
		return
	}
	if held != nil {
		held.Close()
	}
	held = f
}

// Setup makes [Line] write to the descriptor that [Claim] kept, from now on, or prints
// on errOut the one line on a value that Claim could not use. It calls Claim first when
// nothing has since the last Setup; a second Setup leaves the descriptor as it is. The
// main package calls it for qory run before the flags are parsed, and qory run as it
// starts; every other command leaves a kept descriptor unused.
func Setup(errOut io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	if !claimed {
		claim()
	}
	claimed = false
	if problem != "" {
		fmt.Fprint(errOut, problem)
		problem = ""
		return
	}
	if held == nil {
		return
	}
	if file != nil {
		file.Close()
	}
	file, held, stderr = held, nil, errOut
}

// Active reports whether qory's lines go to a descriptor.
func Active() bool {
	mu.Lock()
	defer mu.Unlock()
	return file != nil
}

// Line writes text to the descriptor, masked and without the terminal's escape
// sequences, such as a colour, one write for each of its lines, each with a newline at
// its end. Without a descriptor it writes nothing. After a write fails, it
// prints one line on stderr and writes nothing more there.
func Line(text string) {
	mu.Lock()
	defer mu.Unlock()
	if file == nil {
		return
	}
	text = strings.TrimSuffix(secretmask.Default().Replace(escapes.ReplaceAllString(text, "")), "\n")
	for _, line := range strings.Split(text, "\n") {
		if _, err := io.WriteString(file, line+"\n"); err != nil {
			fd := file.Fd()
			file.Close()
			file = nil
			if stderr != nil {
				fmt.Fprintf(stderr, "qory run: check what reads %s: a write to descriptor %d failed, so qory run writes its lines to stderr only from now on: %v\n", Env, fd, err)
			}
			return
		}
	}
}

// escapes matches a terminal's escape sequence, such as the colour a ui line on a
// terminal starts with.
var escapes = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// Shown reports whether a line written to w goes to the stderr that [Setup] got and not
// to the descriptor: w is that stderr, or leads to it through the Terminal method of
// each writer on the way, and no writer on the way is a [TeeWriter], which writes the
// line to the descriptor itself. A writer of one of qory's lines writes it with [Line]
// only when Shown reports true, so the descriptor gets only lines that stderr shows,
// each once.
func Shown(w io.Writer) bool {
	mu.Lock()
	to := stderr
	mu.Unlock()
	if to == nil {
		return false
	}
	for range maxTerminals {
		if w == nil {
			return false
		}
		if _, tee := w.(*TeeWriter); tee {
			return false
		}
		if reflect.TypeOf(w).Comparable() && w == to {
			return true
		}
		t, ok := w.(interface{ Terminal() io.Writer })
		if !ok {
			return false
		}
		w = t.Terminal()
	}
	return false
}

// maxTerminals is the most Terminal methods [Shown] follows from a writer to stderr.
const maxTerminals = 8

// Close stops the writes to the descriptor and closes it, and a descriptor that Claim
// kept.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	for _, f := range []*os.File{file, held} {
		if f != nil {
			f.Close()
		}
	}
	file, held, claimed, problem = nil, nil, false, ""
}

// MaxLine is the most bytes of a line that a [Tee] keeps while it waits for the line's
// end. A longer line goes to the descriptor in parts of this size or more.
const MaxLine = 64 << 10

// Tee returns a writer that writes to w what it gets, as it is, and each whole line of
// it to the descriptor with [Line], or a part of a line once [MaxLine] bytes of it wait.
// A ui line written to a Tee goes there through the Tee alone, see [Shown]. The copy for the descriptor goes through a [secretmask.Writer] of [secretmask.Default]
// first, so a value split across two writes, or across the cut of a long line, is
// masked whole. The writer is safe for concurrent writes. A last line without a newline
// goes to the descriptor on [TeeWriter.Flush].
func Tee(w io.Writer) *TeeWriter {
	t := &TeeWriter{w: w}
	t.masked = secretmask.Default().Writer(&t.lines)
	return t
}

// TeeWriter is the writer [Tee] returns.
type TeeWriter struct {
	w      io.Writer
	mu     sync.Mutex
	masked *secretmask.Writer
	lines  lineSplitter
}

// Write writes b to w, and b, masked, to the descriptor, a line at a time.
func (t *TeeWriter) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n, err := t.w.Write(b)
	t.masked.Write(b)
	return n, err
}

// Flush writes to the descriptor what the Tee kept back: the end of the last write, in
// which a value could start, and a last line without a newline. Call it when the stream
// ends.
func (t *TeeWriter) Flush() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	err := t.masked.Flush()
	t.lines.flush()
	return err
}

// Terminal is w's terminal, so a ui on a Tee colours its text as it would on w.
func (t *TeeWriter) Terminal() io.Writer {
	if term, ok := t.w.(interface{ Terminal() io.Writer }); ok {
		return term.Terminal()
	}
	return t.w
}

// lineSplitter writes the masked copy of a [TeeWriter] to the descriptor with [Line], one
// line at a time, and a part of a line once [MaxLine] bytes of it wait: the bytes are
// masked already, so a cut splits no value. Its TeeWriter orders the calls.
type lineSplitter struct{ buf []byte }

// Write keeps b, and writes each line that b ends.
func (l *lineSplitter) Write(b []byte) (int, error) {
	l.buf = append(l.buf, b...)
	if i := bytes.LastIndexByte(l.buf, '\n'); i >= 0 {
		Line(string(l.buf[:i]))
		l.buf = append(l.buf[:0], l.buf[i+1:]...)
	}
	if len(l.buf) > MaxLine {
		Line(string(l.buf))
		l.buf = l.buf[:0]
	}
	return len(b), nil
}

// flush writes a last line without a newline.
func (l *lineSplitter) flush() {
	if len(l.buf) > 0 {
		Line(string(l.buf))
		l.buf = l.buf[:0]
	}
}
