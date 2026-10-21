package child

import (
	"bytes"
	"sync"

	"github.com/qoryai/forager/accesskey"
)

// maxLine is the longest line a [LineWriter] holds: a longer one is passed on in parts
// of this size.
const maxLine = 16 << 10

// lookahead is how many bytes after a part of a long line are read with it: one fewer
// than qak_ or qrk_, so what starts a secret across the split is seen in the part
// before it.
const lookahead = len("qrk_") - 1

// withheld takes the place of a line that holds what starts a secret.
const withheld = "(a line that holds a secret, left out)"

// LineWriter passes what a command writes on, one line at a time, each after a prefix.
// A line that holds what starts an access key secret or a run key, qak_ or qrk_ in any
// case, is never passed on: a fixed line takes its place. A line longer than [maxLine]
// is passed on in parts, each read with the [lookahead] bytes after it; once a part
// holds what starts a secret, it and every later part of the line are left out, so no
// piece of the secret is passed on on either side of a split.
type LineWriter struct {
	mu     sync.Mutex
	prefix string
	out    func(line string)
	buf    []byte
	// secret reports whether a part of the line being passed on held what starts a
	// secret, so the rest of the line is left out too.
	secret bool
}

// NewLineWriter is a LineWriter that hands each line, after prefix and without its
// newline, to out.
func NewLineWriter(prefix string, out func(line string)) *LineWriter {
	return &LineWriter{prefix: prefix, out: out}
}

// Write passes on each line p completes, and keeps the rest for the next Write.
func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		switch {
		case i >= 0 && i <= maxLine:
			w.emit(w.buf[:i], w.buf[:i])
			w.secret = false
			w.buf = w.buf[i+1:]
		case i >= 0 || len(w.buf) >= maxLine+lookahead:
			// A part of a long line, read with what follows it on the line.
			end := min(maxLine+lookahead, len(w.buf))
			if i >= 0 {
				end = min(end, i)
			}
			w.emit(w.buf[:maxLine], w.buf[:end])
			w.buf = w.buf[maxLine:]
		default:
			if len(w.buf) == 0 {
				w.buf = nil
			}
			return len(p), nil
		}
	}
}

// Flush passes on the last line, one with no newline after it.
func (w *LineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(w.buf, w.buf)
	}
	w.buf, w.secret = nil, false
}

// emit passes one line or part of a line, b, on, a carriage return at its end left
// out; window is b and what follows it on the line, which is checked for what starts a
// secret with b.
func (w *LineWriter) emit(b, window []byte) {
	if accesskey.ContainsSecret(string(window)) {
		w.secret = true
	}
	line := string(bytes.TrimSuffix(b, []byte("\r")))
	if w.secret {
		line = withheld
	}
	w.out(w.prefix + line)
}
