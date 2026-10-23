package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
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
	textFetchStep        = "fetch %s"
	textFetchStepOne     = "fetch %s and 1 further repository"
	textFetchStepMany    = "fetch %s and %d further repositories"
	textFetchStepUnnamed = "fetch the run's repositories"
	textComposeStep      = "compose the harness for %s"
	textComposeStepAny   = "compose the harness"
)

// fetchStepName is the name of the fetch step of a run whose server's values getenv
// returns: the run's repository as <system>/<path>, and how many further repositories
// the run lists. A list that does not pass [preparation.Check] has the name without a
// repository; the fetch then fails on that list's line.
func fetchStepName(getenv func(string) string) string {
	system := getenv(preparation.EnvSystem)
	paths, err := preparation.Check(system, getenv(preparation.EnvRepositories))
	if err != nil || len(paths) == 0 {
		return textFetchStepUnnamed
	}
	first := integration.Printable(exports.Value(system+"/"+paths[0], false))
	switch n := len(paths) - 1; n {
	case 0:
		return fmt.Sprintf(textFetchStep, first)
	case 1:
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
			w.log(line)
		}
	}
	exitErr := step.Exit(state, code)
	switch {
	case workErr != nil:
		return workErr
	case w.err != nil:
		return w.err
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
// and not into a step's log.
var escapes = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// stepLines is the writer of a step's lines: it writes each byte to stderr at once, and
// each whole line into the step's log, one line per Log, so the run's masker masks every
// password whole. err is the first error of the step's Log.
type stepLines struct {
	stderr io.Writer
	step   hostStep
	buf    []byte
	err    error
}

// Write writes p to stderr, and each line p ends into the step's log.
func (w *stepLines) Write(p []byte) (int, error) {
	n, err := w.stderr.Write(p)
	w.buf = append(w.buf, p[:n]...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.log(w.buf[:i+1])
		w.buf = w.buf[i+1:]
	}
	return n, err
}

// Terminal is stderr, so a [ui.UI] on w colours its text as it would on stderr.
func (w *stepLines) Terminal() io.Writer { return w.stderr }

// flush writes a last line without a line break into the step's log.
func (w *stepLines) flush() {
	if len(w.buf) > 0 {
		w.log(w.buf)
		w.buf = nil
	}
}

// log writes line into the step's log, without the terminal's escape sequences.
func (w *stepLines) log(line []byte) {
	if err := w.step.Log(escapes.ReplaceAll(bytes.Clone(line), nil)); err != nil && w.err == nil {
		w.err = err
	}
}
