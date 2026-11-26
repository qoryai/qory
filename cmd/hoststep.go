package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/statusfd"
)

// hostStep is a step of the run's host work as qory run writes it: a [*session.Step],
// whose lines go into the run's record with the step's number.
type hostStep interface {
	Log(b []byte) error
	Exit(state string, exitCode int) error
}

// The names of the steps of qory run's host work, as the run's record and the run page
// show them. Each is one format string.
const (
	textFetchStep         = "fetch %s"
	textFetchStepOne      = "fetch %s and 1 further repository"
	textFetchStepMany     = "fetch %s and %d further repositories"
	textFetchStepUnnamed  = "fetch the run's repositories"
	textFetchStepReadOne  = "fetch 1 read-only repository"
	textFetchStepReads    = "fetch %d read-only repositories"
	textFetchStepAndRead  = "fetch %s and 1 read-only repository"
	textFetchStepAndReads = "fetch %s and %d read-only repositories"
	textFetchStepBranch   = "%s at %s %s"
	textComposeStep       = "compose the harness for %s"
	textComposeStepAny    = "compose the harness"
	textCopyStep          = "copy work out"
	textReviewCopyStep    = "copy the pushed commit for the reviewers"
)

// fetchStepName is the name of the fetch step of a run whose list is l: the run's
// repository as <system>/<path>, and how many further repositories the run lists; for a
// list of read targets alone, how many. A list that does not pass its check has the name
// without a repository; the fetch then fails on that list's line.
func fetchStepName(l runList) string {
	if l.err != nil || len(l.paths) == 0 {
		return textFetchStepUnnamed
	}
	if !l.target {
		if len(l.paths) == 1 {
			return textFetchStepReadOne
		}
		return fmt.Sprintf(textFetchStepReads, len(l.paths))
	}
	first := integration.Printable(exports.Value(l.system+"/"+l.paths[0], false))
	switch n := len(l.paths) - 1; {
	case n == 0:
		return fmt.Sprintf(textFetchStep, first)
	case n == 1 && l.code:
		return fmt.Sprintf(textFetchStepAndRead, first)
	case l.code:
		return fmt.Sprintf(textFetchStepAndReads, first, n)
	case n == 1:
		return fmt.Sprintf(textFetchStepOne, first)
	default:
		return fmt.Sprintf(textFetchStepMany, first, n)
	}
}

// composeStepName is the name of the compose step of a run of runtime, "" when qory run
// was given none and the compose finds the one the harness is for.
func composeStepName(runtime string) string {
	if runtime == "" {
		return textComposeStepAny
	}
	return fmt.Sprintf(textComposeStep, integration.Printable(exports.Value(runtime, false)))
}

// inStep runs work as one step of run's host work, of kind and named name. work writes
// its lines to the writer it gets, which passes every byte to stderr as it comes and
// writes each whole line into the step's log as well. The step ends succeeded with 0
// when work returns nil; cancelled with -1 when work fails once stopped reports true,
// which is when qory run was stopped or ran out of time; and otherwise failed with the
// status qory run exits with, after the error's line, as [stepFailure] has it, goes into
// the step's log. The error is work's, or the step's when work succeeded.
func inStep(run openedRun, stderr io.Writer, kind, name string, stopped func() bool, work func(io.Writer) error) error {
	step, err := run.StartStep(kind, name)
	if err != nil {
		return err
	}
	w := &stepLines{stderr: stderr, step: step}
	workErr := work(w)
	w.flush()
	state, code := event.StateSucceeded, 0
	switch {
	case workErr == nil:
	case stopped():
		state, code = event.StateCancelled, -1
	default:
		state, code = event.StateFailed, ExitCode(workErr)
		if line := stepFailure(workErr); line != nil {
			w.mu.Lock()
			w.log(line)
			w.mu.Unlock()
		}
	}
	exitErr := step.Exit(state, code)
	w.mu.Lock()
	logErr := w.err
	w.mu.Unlock()
	switch {
	case workErr != nil:
		return workErr
	case logErr != nil:
		return logErr
	}
	return exitErr
}

// stepFailure is the line of err, the error that ended a step of the host work, as the
// step's log has it: the text qory run prints on stderr after "qory run: " or after its
// cross, with "qory run: " in front, a text that contains a run key or an access key
// replaced as [accesskey.ShownError] replaces it. An error qory run printed already,
// whose lines went into the step's log as they were printed, has none.
func stepFailure(err error) []byte {
	var line *runLineError
	if !errors.As(err, &line) && errors.Is(err, ErrReported) {
		return nil
	}
	text := strings.TrimPrefix(accesskey.ShownError(err).Error(), "qory run: ")
	return []byte("qory run: " + strings.TrimSuffix(text, "\n") + "\n")
}

// escapes matches the terminal's escape sequences, such as a colour, which go to stderr
// and not into a step's log: a control sequence of ECMA-48, with parameter bytes 0x30-0x3F,
// intermediate bytes 0x20-0x2F and a final byte 0x40-0x7E.
var escapes = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")

// stepLines is the writer of a step's lines: it writes each byte to stderr at once, and
// each whole line into the step's log, one line per Log, so the run's masker masks every
// password whole. qory run's stderr masks what it gets. Each line that starts with
// "qory run: ", one of qory's lines, also goes to the status descriptor, masked, unless
// program is set: the writer then has the output of a program that qory does not trust, such
// as a workflow hook's, and a line of it keeps in the log what its first [maxStepLine] bytes
// show, masked with the [lookahead] bytes after them, as [stepLines.masked] has it, and ends
// with [textLineCut] when it lost bytes. err is the first error of the step's Log.
type stepLines struct {
	stderr  io.Writer
	step    hostStep
	program bool
	// mu guards buf, cut and err, and orders the writes: os/exec copies a program's stdout and
	// stderr to the writer from two goroutines.
	mu  sync.Mutex
	buf []byte
	// cut is whether the line in buf had more bytes than it keeps.
	cut bool
	err error
}

// maxStepLine is the most bytes of a line of a program's output that a step's log keeps.
const maxStepLine = 64 << 10

// textLineCut ends a line of a step's log that had more than [maxStepLine] bytes, or whose
// masked text had: a mask can make a line longer.
const textLineCut = " [qory cut the rest of this line]"

// Write writes p to stderr, and each line p ends into the step's log.
func (w *stepLines) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.stderr.Write(p)
	data := p[:n]
	for len(data) > 0 {
		end, ends := len(data), false
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			end, ends = i, true
		}
		room := end
		if w.program {
			room = max(0, maxStepLine+lookahead-len(w.buf))
		}
		if end > room {
			w.cut = true
		}
		w.buf = append(w.buf, data[:min(end, room)]...)
		if !ends {
			break
		}
		data = data[end+1:]
		w.line(true)
	}
	return n, err
}

// line writes the line in buf, with a line break when ends, into the step's log, and to the
// status descriptor, and empties buf. The caller holds mu.
func (w *stepLines) line(ends bool) {
	if w.program {
		w.buf = append(w.buf[:0], w.masked()...)
	}
	if w.cut {
		w.buf = append(w.buf, textLineCut...)
	}
	w.status(w.buf)
	if ends {
		w.buf = append(w.buf, '\n')
	}
	w.log(w.buf)
	w.buf, w.cut = w.buf[:0], false
}

// masked is the line of a program's output in buf as a step's log keeps it, [keptLine] of buf
// with [maxStepLine] and [lookahead]; it sets cut when the line loses bytes.
func (w *stepLines) masked() []byte {
	out, lost := keptLine(w.buf, w.cut, maxStepLine, lookahead)
	w.cut = w.cut || lost
	return out
}

// lookahead is how many bytes past the cut a line of a program's output keeps: the longest
// form of a value of the mask list, JSON-escaped with 6 bytes for each byte.
const lookahead = 6 * secretmask.MaxValueLength

// keptLine is what the log keeps of buf, a line of a program's output that went on when more:
// [logText] of buf, up to the cut at keep bytes of buf, with *** for a mask across the cut, then
// cut at keep bytes. lost reports that the line lost bytes.
func keptLine(buf []byte, more bool, keep, ahead int) (out []byte, lost bool) {
	if len(buf) <= keep {
		out = logText(buf)
	} else {
		at := keep
		if more {
			// A mask that starts before the cut ends inside buf only when ahead bytes of text
			// follow the cut, in buf masked once, as logLine strips it: *** is 3 intermediate
			// bytes. No byte counts from the start of a sequence that may end past buf.
			m, src := firstMask(buf, true)
			_, kept, open, _ := unescaped(m, true)
			// The masks of the last ahead bytes of buf can change with the bytes after it, and
			// with them the end of a sequence open where they start, or of one that starts in them
			// and that a byte with no match stops.
			b := 0
			for b < len(m) && src[b].lo < len(buf)-ahead {
				b++
			}
			if _, _, o, _ := unescaped(m[:b], false); o < b {
				open = min(open, o)
			} else if _, _, _, d := unescaped(m[b:], false); b+d < open {
				open = b + d
			}
			// left is the offsets in buf of the bytes that the kept text stands for, a put's once.
			// shortcut: a secret with a run of * can match across puts longer in buf than ahead and
			// show its start; count puts by text length when that costs no 64 KiB lines of tokens.
			var left []int
			next := 0
			for _, k := range kept {
				if k < open && m[k] != 0x1b {
					for at := max(src[k].lo, next); at < src[k].hi; at++ {
						left = append(left, at)
					}
				}
				next = max(next, src[k].hi)
			}
			if i, _ := slices.BinarySearch(left, at); len(left)-i < ahead {
				at = 0
				if len(left) >= ahead {
					at = left[len(left)-ahead]
				}
			}
		}
		text, src := logLine(buf, true)
		n := 0
		for n < len(text) && src[n].hi <= at {
			n++
		}
		out, lost = text[:n], true
		if n < len(text) && src[n].lo < at {
			// A * before the *** of the mask would make the line end with more than 3 of them.
			out = append(bytes.TrimRight(out, "*"), secretmask.Placeholder...)
		}
	}
	if len(out) > keep {
		out, lost = out[:keep], true
	}
	return out, lost
}

// The states of an escape sequence that [unescaped] has seen the start of.
const (
	seqESC    = iota // the ESC byte
	seqCSI           // ESC [ and parameter bytes
	seqInter         // ESC [, parameter bytes and intermediate bytes
	seqString        // ESC and one of ] P X ^ _, and the bytes of the string
)

// unescaped is b without [escapes] and the string sequences ESC ] P X ^ _ to BEL or ESC \, in one
// pass, as stripping them until none is left. With track, kept is the offset in b of each text
// byte. open is the start of the first open sequence, dead of the first a byte stops; else len(b).
func unescaped(b []byte, track bool) (text []byte, kept []int, open, dead int) {
	dead = len(b)
	type seq struct{ at, from, state int }
	// stack has the sequences that started and did not end, inner last; at is where each
	// one's ESC is in text, and from in b.
	var stack []seq
	text = make([]byte, 0, len(b))
	for i, c := range b {
		end := false
		if c == 0x1b {
			stack = append(stack, seq{len(text), i, seqESC})
		} else if n := len(stack); n > 0 {
			top := &stack[n-1]
			switch {
			case top.state == seqString:
				end = c == 0x07
			case top.state == seqESC && c == '\\' && n > 1 && stack[n-2].state == seqString:
				// ESC \ ends the string sequence around it.
				stack, end = stack[:n-1], true
			case top.state == seqESC && c == '[':
				top.state = seqCSI
			case top.state == seqESC && strings.IndexByte("]PX^_", c) >= 0:
				top.state = seqString
			case top.state == seqCSI && 0x30 <= c && c <= 0x3f:
			case top.state != seqESC && 0x20 <= c && c <= 0x2f:
				top.state = seqInter
			case top.state != seqESC && 0x40 <= c && c <= 0x7e:
				end = true
			default:
				// A byte no sequence has there: this sequence and those around it stay.
				if dead == len(b) {
					dead = stack[0].from
				}
				stack = stack[:0]
			}
		}
		if !end {
			text = append(text, c)
			if track {
				kept = append(kept, i)
			}
			continue
		}
		at := stack[len(stack)-1].at
		stack = stack[:len(stack)-1]
		text = text[:at]
		if track {
			kept = kept[:at]
		}
	}
	if len(stack) > 0 {
		return text, kept, stack[0].from, dead
	}
	return text, kept, len(b), dead
}

// span is the bytes of a line, from lo to hi, that a byte of the line's log text stands for:
// the byte itself, or the bytes that a mask replaced with the put the byte is part of.
type span struct{ lo, hi int }

// logText is b as a step's log has it: masked with the run's mask list, [secretmask.Default],
// then without escape sequences and masked again, then without the ESC bytes of sequences that
// did not end and masked again.
func logText(b []byte) []byte {
	text, _ := logLine(b, false)
	return text
}

// logLine is [logText] of b and, with track, the span of b each byte of the text stands for.
func logLine(b []byte, track bool) ([]byte, []span) {
	set := secretmask.Default()
	text, src := firstMask(b, track)
	if next, kept, _, _ := unescaped(text, track); len(next) < len(text) {
		text, src = masks(set, next, picked(src, kept), track)
	}
	if bytes.IndexByte(text, 0x1b) >= 0 {
		var next []byte
		var kept []int
		for i, c := range text {
			if c != 0x1b {
				next, kept = append(next, c), append(kept, i)
			}
		}
		text, src = masks(set, next, picked(src, kept), track)
	}
	return text, src
}

// firstMask is b masked with [secretmask.Default], the first step of [logLine], and with track
// the spans of the masked text.
func firstMask(b []byte, track bool) ([]byte, []span) {
	var src []span
	if track {
		src = make([]span, len(b))
		for i := range src {
			src[i] = span{i, i + 1}
		}
	}
	return masks(secretmask.Default(), b, src, track)
}

// picked is the spans of src at the offsets kept, nil when src is.
func picked(src []span, kept []int) []span {
	if src == nil {
		return nil
	}
	out := make([]span, len(kept))
	for i, at := range kept {
		out[i] = src[at]
	}
	return out
}

// masks is text masked with set, as [secretmask.Set.Replace] masks it, and with track, the
// spans of the masked text, where src has the spans of text: each byte of a put stands for
// what the bytes it replaced stood for.
func masks(set *secretmask.Set, text []byte, src []span, track bool) ([]byte, []span) {
	if !track {
		return []byte(set.Replace(string(text))), nil
	}
	known, query := set.Ranges(text)
	text, src = replaced(text, src, known)
	return replaced(text, src, query)
}

// replaced is text with each of rs replaced by its put, and the spans of the result.
func replaced(text []byte, src []span, rs []secretmask.Range) ([]byte, []span) {
	if len(rs) == 0 {
		return text, src
	}
	var out []byte
	var to []span
	from := 0
	for _, r := range rs {
		out, to = append(out, text[from:r.Start]...), append(to, src[from:r.Start]...)
		// An empty range, such as a query value with no byte, stands for none of the line.
		sp := span{0, 0}
		switch {
		case r.End > r.Start:
			sp = span{src[r.Start].lo, src[r.End-1].hi}
		case r.Start < len(src):
			sp = span{src[r.Start].lo, src[r.Start].lo}
		case len(src) > 0:
			sp = span{src[len(src)-1].hi, src[len(src)-1].hi}
		}
		for range r.Put {
			to = append(to, sp)
		}
		out, from = append(out, r.Put...), r.End
	}
	return append(out, text[from:]...), append(to, src[from:]...)
}

// Terminal is stderr, so a [ui.UI] on w colours its text as it would on stderr.
func (w *stepLines) Terminal() io.Writer { return w.stderr }

// flush writes a last line without a line break into the step's log.
func (w *stepLines) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 || w.cut {
		w.line(false)
	}
}

// status writes line to the status descriptor when it is one of qory's lines, which
// start with "qory run: ", and stderr shows it, see [statusfd.Shown]. The output of a
// program, such as git's, does not.
func (w *stepLines) status(line []byte) {
	if w.program {
		return
	}
	text := string(escapes.ReplaceAll(bytes.TrimSuffix(line, []byte("\r")), nil))
	if strings.HasPrefix(text, "qory run: ") && statusfd.Shown(w.stderr) {
		statusfd.Line(text)
	}
}

// log writes line into the step's log as [logText] has it. The caller holds mu.
func (w *stepLines) log(line []byte) {
	if err := w.step.Log(logText(line)); err != nil && w.err == nil {
		w.err = err
	}
}
