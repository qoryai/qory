package suite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/sourcetext"
)

// MaxTailLines is the most lines of the full suite's output that [Result] keeps.
const MaxTailLines = 200

// MaxOutputBytes is the most bytes of the full suite's output that qory reads and that
// [NewResult] looks at: the last 256 KiB, so one very long line cannot make a large file.
const MaxOutputBytes = 256 << 10

// Result is the suite result file that the reviewers of a round read as
// qory.review_session.suite_result: the counts of the full suite and the last lines of its
// output. A reviewer cites OutputTail as the proof of a failing check.
type Result struct {
	Counts
	// OutputTail is the last [MaxTailLines] lines of the suite's output, "" for none.
	OutputTail string `json:"output_tail"`
}

// NewResult is the [Result] of counts and output, the full suite's output as it ended:
// OutputTail has at most the last [MaxTailLines] lines of the last [MaxOutputBytes] bytes
// of output, with each hidden character written as <U+XXXX> (see sourcetext.Escape),
// since a reviewer reads the text. When the cap cuts the output, the first line of the
// tail may start in the middle of a line.
func NewResult(counts Counts, output []byte) Result {
	return Result{Counts: counts, OutputTail: sourcetext.Escape(Tail(lastBytes(output, MaxOutputBytes), MaxTailLines))}
}

// lastBytes is the last n bytes of b, all of b when it is shorter. A cut that falls inside
// a multi-byte character moves forward to the next character's first byte, so the result
// has up to 3 bytes fewer than n and starts at a rune start, as far as b is UTF-8.
func lastBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	b = b[len(b)-n:]
	for skip := 0; skip < utf8.UTFMax-1 && len(b) > 0 && !utf8.RuneStart(b[0]); skip++ {
		b = b[1:]
	}
	return b
}

// Tail is the last n lines of output as text, with the line break at the end of the
// last line left out. A line break at the end of output does not start a line.
func Tail(output []byte, n int) string {
	output = bytes.TrimSuffix(output, []byte("\n"))
	if n < 1 || len(output) == 0 {
		return ""
	}
	lines := bytes.Split(output, []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}

// WriteResult writes r as JSON to path, a new file of mode 0644, so the run's user can
// read it as a reviewer does. It returns an error that starts with the fix when the file
// exists or cannot be written.
func WriteResult(path string, r Result) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("The suite result could not be written as JSON: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("The suite result file %s exists or could not be made: %w", strconv.Quote(path), err)
	}
	err = f.Chmod(0o644)
	if err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err != nil {
		f.Close()
		return fmt.Errorf("Writing the suite result file %s failed: %w", strconv.Quote(path), err)
	}
	return f.Close()
}
