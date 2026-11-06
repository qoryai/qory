package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/internal/exports"
)

// machineStepsFileFlag is the hidden flag of qory run that sets the path of the file
// that the machine's start script writes: the steps of the start of the machine the run
// runs on.
const machineStepsFileFlag = "machine-steps-file"

// machineStepsFileUsage is the help of --machine-steps-file, which --help does not show.
const machineStepsFileUsage = "with --run-code-file, write the steps of the machine's start, one JSON object per line in <file>, as the run's machine step"

// The limits of the file of --machine-steps-file: its size, the entries the machine step
// shows, and the characters of an entry's name.
const (
	machineStepsMaxBytes   = 64 << 10
	machineStepsMaxEntries = 64
	machineStepNameMax     = 100
)

// The texts of the machine step: its name, its line for each entry, and qory run's lines
// when it writes no step or an incomplete one.
const (
	textMachineStep         = "start machine"
	textMachineStepLine     = "%s  %.1f s\n"
	textMachineOnlyWithCode = "remove --" + machineStepsFileFlag + ": it applies only with --" + runCodeFileFlag
	textNoMachineStep       = "the run's record has no machine step: %s"
	textMachineIncomplete   = "the machine step in the run's record is incomplete: writing it failed: %s"
	textMachineUnreadable   = "%s cannot be read: %s"
	textMachineNotRegular   = "%s is not a regular file"
	textMachineTooLarge     = "%s is larger than 64 KiB"
	textMachineBadLine      = "line %d of %s is not a JSON object with the strings name, started and ended"
	textMachineBadTime      = "line %d of %s: %s is not an RFC 3339 time"
	textMachineEndedFirst   = "line %d of %s: ended is before started"
	textMachineWriteFailed  = "writing the step failed: %s"
)

// writeMachineStep writes the run's machine step from the file at path: one step of kind
// machine, named [textMachineStep], with one line per entry, which succeeds with -1. A
// file that [machineStepLines] does not read, or a step Forager does not start, writes no
// step; an error after the step started leaves it incomplete. Each case prints one line
// through said, and the run goes on.
func writeMachineStep(run openedRun, path string, said func(string)) {
	lines, err := machineStepLines(path)
	if err != nil {
		said(fmt.Sprintf(textNoMachineStep, err))
		return
	}
	step, err := run.StartStep(event.StepMachine, textMachineStep)
	if err != nil {
		said(fmt.Sprintf(textNoMachineStep, fmt.Sprintf(textMachineWriteFailed, err)))
		return
	}
	var logErr error
	for _, l := range lines {
		if logErr = step.Log([]byte(l)); logErr != nil {
			break
		}
	}
	if err := errors.Join(logErr, step.Exit(event.StateSucceeded, -1)); err != nil {
		said(fmt.Sprintf(textMachineIncomplete, err))
	}
}

// machineStepLines reads the file at path, as the machine's start script writes it: one
// JSON object per line, {"name": …, "started": …, "ended": …}, in the order the steps
// ran, each time in RFC 3339. It returns the step's line for each of the first
// [machineStepsMaxEntries] entries, "<name>  <seconds> s" with the seconds from started
// to ended to one decimal, and the name without control characters and cut to
// [machineStepNameMax] characters. An empty file has no lines. The error, whose text
// contains the path, is that of a file qory reads none of: one that is missing, cannot be
// read, is not a regular file or is larger than [machineStepsMaxBytes], or that has a
// line that is not such an object, with a time that does not parse, or that ended before
// it started.
func machineStepLines(path string) ([]string, error) {
	shown := exports.Value(path, false)
	// A file that is not a regular one, such as a FIFO, could block the open.
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf(textMachineUnreadable, shown, pathCause(err))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf(textMachineNotRegular, shown)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf(textMachineUnreadable, shown, pathCause(err))
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, machineStepsMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf(textMachineUnreadable, shown, pathCause(err))
	}
	if len(b) > machineStepsMaxBytes {
		return nil, fmt.Errorf(textMachineTooLarge, shown)
	}
	if len(b) == 0 {
		return nil, nil
	}
	var lines []string
	for i, raw := range bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n")) {
		n := i + 1
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
			return nil, fmt.Errorf(textMachineBadLine, n, shown)
		}
		name, okName := jsonString(obj["name"])
		started, okStarted := jsonString(obj["started"])
		ended, okEnded := jsonString(obj["ended"])
		if !okName || !okStarted || !okEnded {
			return nil, fmt.Errorf(textMachineBadLine, n, shown)
		}
		from, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, fmt.Errorf(textMachineBadTime, n, shown, "started")
		}
		to, err := time.Parse(time.RFC3339Nano, ended)
		if err != nil {
			return nil, fmt.Errorf(textMachineBadTime, n, shown, "ended")
		}
		if to.Before(from) {
			return nil, fmt.Errorf(textMachineEndedFirst, n, shown)
		}
		// Every line is checked; the step shows the first entries alone.
		if len(lines) < machineStepsMaxEntries {
			lines = append(lines, fmt.Sprintf(textMachineStepLine, machineStepName(name), to.Sub(from).Seconds()))
		}
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

// machineStepName is name as the machine step's line shows it: without the characters
// of Unicode category Cc, U+2028 and U+2029, then cut to [machineStepNameMax]
// characters.
func machineStepName(name string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, name)
	if utf8.RuneCountInString(clean) > machineStepNameMax {
		clean = string([]rune(clean)[:machineStepNameMax])
	}
	return clean
}

// pathCause is the cause of err without the operation and path that an [fs.PathError]
// puts in front of it, since the line gives the path already.
func pathCause(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
