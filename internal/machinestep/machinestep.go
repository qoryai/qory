// Package machinestep writes the machine step of a run: the steps of the start of the
// machine the run runs on, which the machine's start script writes to a file, as one
// step of the run's record of kind machine. qory run writes it for a run from a run code
// given --machine-steps-file, and a prebuild for one of the step prebuild, each right
// after the run opens.
package machinestep

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/internal/exports"
)

// The limits of the file: its size, its lines, the time from an entry's start to its
// end, and the characters of an entry's name.
const (
	maxBytes   = 64 << 10
	maxEntries = 64
	maxSpan    = 24 * time.Hour
	maxName    = 100
)

// Name is the name of the machine step.
const Name = "start machine"

// The texts of the machine step: its line for each entry, and the lines of a run that
// writes no step or an incomplete one. Each is one format string.
const (
	textLine         = "%s  %d.%d s\n"
	textNoStep       = "the run's record has no machine step: %s"
	textIncomplete   = "the machine step in the run's record is incomplete: writing it failed: %s"
	textWriteFailed  = "writing the step failed: %s"
	textUnreadable   = "%s cannot be read: %s"
	textNotRegular   = "%s is not a regular file"
	textTooLarge     = "%s is larger than 64 KiB"
	textTooMany      = "%s has more than 64 lines"
	textNoSteps      = "%s has no steps"
	textBadLine      = "line %d of %s is not a JSON object with the strings name, started and ended"
	textBadTime      = "line %d of %s: %s is not an RFC 3339 time"
	textEndedFirst   = "line %d of %s: ended is before started"
	textTooLongAfter = "line %d of %s: ended is more than 24 hours after started"
)

// escapes matches the terminal's escape sequences, such as a colour, as qory run's host
// steps leave them out of their lines.
var escapes = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// Step is a step of a run's host work, a [*session.Step].
type Step interface {
	Log(b []byte) error
	Exit(state string, exitCode int) error
}

// Write writes the run's machine step from the file at path: start starts a step of
// kind machine, named [Name], then the step gets one Log per line that [Lines] returns,
// and exits succeeded with -1. A file that Lines returns an error for, or a step that
// start returns an error for, writes no step; a Log or an Exit that returns an error
// leaves the step incomplete. Each of these prints one line through said, with the
// Log's error when there is one, and the run goes on.
func Write(start func(kind, name string) (Step, error), path string, said func(string)) {
	lines, err := Lines(path)
	if err != nil {
		said(fmt.Sprintf(textNoStep, err))
		return
	}
	step, err := start(event.StepMachine, Name)
	if err != nil {
		said(fmt.Sprintf(textNoStep, fmt.Sprintf(textWriteFailed, err)))
		return
	}
	var logErr error
	for _, l := range lines {
		if logErr = step.Log([]byte(l)); logErr != nil {
			break
		}
	}
	exitErr := step.Exit(event.StateSucceeded, -1)
	switch {
	case logErr != nil:
		said(fmt.Sprintf(textIncomplete, logErr))
	case exitErr != nil:
		said(fmt.Sprintf(textIncomplete, exitErr))
	}
}

// Lines reads the file at path, as the machine's start script writes it, and returns
// the machine step's line for each entry. The file has one JSON object per line, in the
// order the steps ran:
//
//	{"name":"start Docker","started":"2026-11-06T00:31:58.904Z","ended":"2026-11-06T00:32:02.104Z"}
//
// started and ended are times in RFC 3339; further members are allowed. Each line of the
// step is "<name>  <seconds> s", such as "start Docker  3.2 s": the seconds from started
// to ended, rounded to one decimal, half up, and the name without terminal escape
// sequences, without the characters of Unicode categories Cc and Cf, U+2028 and U+2029,
// then cut to 100 characters.
//
// The error, whose text contains the path, is that of a bad file, which gets no step: a
// file that is missing, cannot be read, is not a regular file, is larger than 64 KiB,
// has no line or more than 64, or has a line that is not such an object, with a time
// that does not parse, that ended before it started, or more than 24 hours after.
func Lines(path string) ([]string, error) {
	shown := exports.Value(path, false)
	// The open does not wait on a FIFO at the path; the check after it reads a regular
	// file alone.
	f, err := os.OpenFile(path, os.O_RDONLY|openFlags, 0)
	if err != nil {
		return nil, fmt.Errorf(textUnreadable, shown, pathCause(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf(textUnreadable, shown, pathCause(err))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf(textNotRegular, shown)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf(textUnreadable, shown, pathCause(err))
	}
	if len(b) > maxBytes {
		return nil, fmt.Errorf(textTooLarge, shown)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf(textNoSteps, shown)
	}
	raws := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	if len(raws) > maxEntries {
		return nil, fmt.Errorf(textTooMany, shown)
	}
	lines := make([]string, 0, len(raws))
	for i, raw := range raws {
		n := i + 1
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			return nil, fmt.Errorf(textBadLine, n, shown)
		}
		name, okName := jsonString(obj["name"])
		started, okStarted := jsonString(obj["started"])
		ended, okEnded := jsonString(obj["ended"])
		if !okName || !okStarted || !okEnded {
			return nil, fmt.Errorf(textBadLine, n, shown)
		}
		from, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, fmt.Errorf(textBadTime, n, shown, "started")
		}
		to, err := time.Parse(time.RFC3339Nano, ended)
		if err != nil {
			return nil, fmt.Errorf(textBadTime, n, shown, "ended")
		}
		if to.Before(from) {
			return nil, fmt.Errorf(textEndedFirst, n, shown)
		}
		// Sub returns the largest Duration for times further apart than it holds, which
		// is past the limit too, so the milliseconds are at most 86,400,000.
		span := to.Sub(from)
		if span > maxSpan {
			return nil, fmt.Errorf(textTooLongAfter, n, shown)
		}
		tenths := (span.Milliseconds() + 50) / 100
		lines = append(lines, fmt.Sprintf(textLine, cleanName(name), tenths/10, tenths%10))
	}
	return lines, nil
}

// jsonString is the string raw is, and whether raw is a JSON string: null, a number or
// an absent member is not.
func jsonString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// cleanName is name as the machine step's line shows it: without terminal escape
// sequences, then without the characters of Unicode categories Cc and Cf, U+2028 and
// U+2029, then cut to [maxName] characters.
func cleanName(name string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, escapes.ReplaceAllString(name, ""))
	if utf8.RuneCountInString(clean) > maxName {
		clean = string([]rune(clean)[:maxName])
	}
	return clean
}

// pathCause is the cause of err without the operation and path that an [fs.PathError]
// puts in front of it, since the line contains the path already.
func pathCause(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
