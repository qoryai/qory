package suite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// MaxTailLines is the most lines of the full suite's output that [Result] keeps.
const MaxTailLines = 200

// Result is the suite result file that the reviewers of a round read as
// qory.review.suite_result: the counts of the full suite and the last lines of its
// output. A reviewer cites OutputTail as the proof of a failing check.
type Result struct {
	Counts
	// OutputTail is the last [MaxTailLines] lines of the suite's output, "" for none.
	OutputTail string `json:"output_tail"`
}

// NewResult is the [Result] of counts and output, the full suite's output as it ended:
// OutputTail has at most the last [MaxTailLines] lines of output.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
// shortcut: no limit on the bytes of a line, so one very long line makes a large
// file; cap the output where the caller reads it, when the caller exists.
func NewResult(counts Counts, output []byte) Result {
	return Result{Counts: counts, OutputTail: Tail(output, MaxTailLines)}
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

// WriteResult writes r as JSON to path, a new file of mode 0600. It returns an error
// that starts with the fix when the file exists or cannot be written.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func WriteResult(path string, r Result) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("Give the suite result counts and text that JSON can write: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("Give the suite result a new file in a folder qory can write.\n%s: %w", strconv.Quote(path), err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("Free space for the suite result file.\n%s: %w", strconv.Quote(path), err)
	}
	return f.Close()
}
