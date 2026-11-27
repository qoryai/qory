package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/suite"
)

// outputLine is a log event of after step 2 whose bytes are text.
func outputLine(seq int, text string) string {
	return fmt.Sprintf(`{"type":"dev.qory.run.log","sequence":"%010d","data":{"stream":"stdout","bytes":%q,"step":2}}`,
		seq, base64.StdEncoding.EncodeToString([]byte(text)))
}

// headLine is the log event of the marker that the script of the after step prints before
// the suite's command, for the commit sha.
func headLine(seq int, sha string) string { return outputLine(seq, suite.HeadMarker+sha+"\n") }

// readSuiteResult reads the suite result file of the record kept by h's suite.
func readSuiteResult(t *testing.T, h helperRun) suite.Result {
	t.Helper()
	if h.record == nil || h.record.path == "" {
		t.Fatalf("record %+v, want a suite result file", h.record)
	}
	data, err := os.ReadFile(h.record.path)
	if err != nil {
		t.Fatal(err)
	}
	var r suite.Result
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestAGreenSuiteWritesAResultWithCountsAndAShortTail: the file is below the run's folder
// and has the counts of the result file and the lines of the step's output.
func TestAGreenSuiteWritesAResultWithCountsAndAShortTail(t *testing.T) {
	h := newHelper(t)
	h.session = 2
	h.state(t, "ran "+suiteSHA, greenResult)
	writeEvents(t, h.runDir, afterStarted(3, 2), headLine(4, suiteSHA), outputLine(5, "ok example\n"), afterExited(6, 2))
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(h.runDir, "review", "suite-2.json"); h.record == nil || h.record.path != want {
		t.Fatalf("record %+v, want the path %s", h.record, want)
	}
	if got, want := readSuiteResult(t, h), (suite.Result{Counts: suite.Counts{Tests: 2}, OutputTail: suite.HeadMarker + suiteSHA + "\nok example"}); got != want {
		t.Errorf("result %+v, want %+v", got, want)
	}
	if rec := h.record; rec.outcome.State != suite.Passed || rec.outcome.Commit != suiteSHA || rec.head != suiteSHA {
		t.Errorf("record %+v, want a passed suite on %s", rec, suiteSHA)
	}
}

// TestARedSuiteWritesTheLast200LinesOfItsOutput: the output of other steps stays out.
func TestARedSuiteWritesTheLast200LinesOfItsOutput(t *testing.T) {
	h := newHelper(t)
	var out strings.Builder
	for i := 1; i <= 500; i++ {
		out.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	h.state(t, "ran "+suiteSHA, redResult)
	writeEvents(t, h.runDir, afterStarted(3, 2), outputLine(4, out.String()),
		strings.Replace(outputLine(5, "other step\n"), `"step":2`, `"step":3`, 1), afterExited(6, 2))
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, succeeded); err != nil {
		t.Fatal(err)
	}
	got := readSuiteResult(t, h)
	lines := strings.Split(got.OutputTail, "\n")
	if len(lines) != suite.MaxTailLines || lines[0] != "line 301" || lines[len(lines)-1] != "line 500" || got.Failed != 1 {
		t.Errorf("result has %d lines from %q to %q and %d failed, want 200 lines from line 301 to line 500", len(lines), lines[0], lines[len(lines)-1], got.Failed)
	}
	if rec := h.record; rec.outcome.State != suite.Failed || !rec.outcome.Started {
		t.Errorf("record %+v, want a red suite that started", rec.outcome)
	}
}

// TestOutputOverTheByteCapKeepsOnlyTheEnd: one line of more than the cap, then a last
// line.
func TestOutputOverTheByteCapKeepsOnlyTheEnd(t *testing.T) {
	h := newHelper(t)
	h.state(t, "ran "+suiteSHA, redResult)
	writeEvents(t, h.runDir, afterStarted(3, 2), outputLine(4, "first\n"+strings.Repeat("x", 3*suite.MaxOutputBytes)), outputLine(5, "\nlast\n"), afterExited(6, 2))
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, succeeded); err != nil {
		t.Fatal(err)
	}
	got := readSuiteResult(t, h).OutputTail
	// The cap cuts inside the line of x, which is dropped whole.
	if got != "last" {
		t.Errorf("tail has %d bytes and ends %q, want the line after the cut only", len(got), got[max(0, len(got)-8):])
	}
}

// noTextLine is the tail of output in whose last 256 KiB no line with text starts.
const noTextLine = "No line with text starts in the last 256 KiB of the suite's output, so qory shows none of those bytes."

// TestOutputWithNoTextLineInTheCapHasTheLineInTheFileAndTheFinding: the after step prints
// one line of more than the cap, so the tail of the result file and the tail of the
// finding's ending are the line that says qory shows none of it.
func TestOutputWithNoTextLineInTheCapHasTheLineInTheFileAndTheFinding(t *testing.T) {
	h := newHelper(t)
	h.state(t, "ran "+suiteSHA, redResult)
	writeEvents(t, h.runDir, afterStarted(3, 2), outputLine(4, strings.Repeat("x", 3*suite.MaxOutputBytes)), afterExited(5, 2))
	res := &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}
	if _, err := h.after(res, succeeded); err != nil {
		t.Fatal(err)
	}
	if got := readSuiteResult(t, h).OutputTail; got != noTextLine {
		t.Errorf("the result file has the tail %q, want %q", got, noTextLine)
	}
	var snapshot runapi.Snapshot
	if err := json.Unmarshal([]byte(`{"stage":{"id":"st_aaa","name":"Build","round":1,"rounds":2,"state":null}}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	h.suiteRun.findings = &findingsRun{code: &codeRun{snapshot: &snapshot}}
	end := h.suiteRun.endingFor(nil, suite.Outcome{State: suite.Failed}, nil, &session.Result{After: res})
	if end.Tail != noTextLine {
		t.Errorf("the ending has the tail %q, want %q", end.Tail, noTextLine)
	}
}

// TestASuiteThatDidNotStartHasNoHead: the script ended before the command, so the record
// has the outcome and no head, and the file has the step's output with no counts.
func TestASuiteThatDidNotStartHasNoHead(t *testing.T) {
	h := newHelper(t)
	writeEvents(t, h.runDir, afterStarted(3, 2), outputLine(4, "qory: could not clone the commit.\n"), afterExited(5, 2))
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 71}, succeeded); err != nil {
		t.Fatal(err)
	}
	if got, want := readSuiteResult(t, h), (suite.Result{OutputTail: "qory: could not clone the commit."}); got != want {
		t.Errorf("result %+v, want %+v", got, want)
	}
	if h.record.head != "" || h.record.outcome.Started {
		t.Errorf("record %+v, want no head and no start", h.record)
	}
}

// TestTheHeadIsTheMarkerOfTheScriptAndNotTheStatusFile: the suite's code wrote another
// commit to the status file, so the outcome has that commit, and the record has the
// commit of the marker that the script printed before the code ran.
func TestTheHeadIsTheMarkerOfTheScriptAndNotTheStatusFile(t *testing.T) {
	const forged = "89abcdef0123456789abcdef0123456789abcdef"
	h := newHelper(t)
	h.state(t, "ran "+forged, greenResult)
	writeEvents(t, h.runDir, afterStarted(3, 2), headLine(4, suiteSHA), outputLine(5, suite.HeadMarker+forged+"\n"), afterExited(6, 2))
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded); err != nil {
		t.Fatal(err)
	}
	if h.record.outcome.Commit != forged || h.record.head != suiteSHA {
		t.Errorf("record %+v, want the status file's commit %s and the marker's %s", h.record, forged, suiteSHA)
	}
	// With no marker in the events, the record has no head, whatever the status file says.
	h = newHelper(t)
	h.state(t, "ran "+suiteSHA, greenResult)
	writeEvents(t, h.runDir, afterStarted(3, 2), outputLine(4, suite.HeadMarker+suiteSHA[:39]+"\n"), afterExited(5, 2))
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded); err != nil {
		t.Fatal(err)
	}
	if h.record.head != "" {
		t.Errorf("record %+v, want no head", h.record)
	}
}

// TestACauseInThePlanLeavesARecordWithNoFile: a fault has a record with no file and no
// head.
func TestACauseInThePlanLeavesARecordWithNoFile(t *testing.T) {
	for name, fault := range map[string]*suiteFault{
		"a fixable cause":   faultOf(suite.ErrNoFull, "build-plan", true),
		"an unfixable one":  noGitFault(),
		"a missing run API": noRunAPIFault(),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHelper(t)
			h.fault = fault
			if _, err := h.after(nil, succeeded); err != nil {
				t.Fatal(err)
			}
			if h.record == nil || h.record.path != "" || h.record.head != "" || h.record.outcome.State != suite.Failed {
				t.Errorf("record %+v, want a failed suite with no path and no head", h.record)
			}
			if _, err := os.Stat(filepath.Join(h.runDir, "review")); err == nil {
				t.Error("a suite with no after step made the folder of suite result files")
			}
		})
	}
}

// TestAFaultWhereHeadDidNotMoveKeepsTheSuiteBefore: a fault leaves no after step to show
// whether HEAD moved, so kept compares HEAD of the session's folder with the commit of the
// marker of the suite before: the same commit keeps that suite, and another HEAD gets the
// fault's record.
func TestAFaultWhereHeadDidNotMoveKeepsTheSuiteBefore(t *testing.T) {
	h := newHelper(t)
	h.workspace = t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
		c.Dir, c.Env = h.workspace, append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--quiet")
	run("commit", "--quiet", "--allow-empty", "-m", "first")
	first := run("rev-parse", "HEAD")
	h.suiteRun.dir = h.workspace
	h.fault = noGitFault()
	if _, err := h.after(nil, succeeded); err != nil {
		t.Fatal(err)
	}
	valid := &suiteRecord{path: "/x/suite-1.json", head: first, outcome: suite.Outcome{State: suite.Passed, Commit: first, Started: true}}
	if got := h.kept(context.Background(), valid); got != valid {
		t.Errorf("kept %+v, want the suite before, whose commit is HEAD", got)
	}
	run("commit", "--quiet", "--allow-empty", "-m", "second")
	if got := h.kept(context.Background(), valid); got != h.record || got == valid {
		t.Errorf("kept %+v, want the record of the fault: HEAD moved", got)
	}
	if got := h.kept(context.Background(), &suiteRecord{path: "/x/suite-1.json"}); got != h.record {
		t.Errorf("kept %+v, want the record of the fault: the suite before has no head", got)
	}
	if got := h.kept(context.Background(), nil); got != h.record {
		t.Errorf("kept %+v, want the record of the fault: no suite before", got)
	}
}

// TestASkippedSuiteKeepsTheRecordBefore: HEAD is the commit of the suite before, so that
// suite still describes it, and kept returns it; a session that ran no suite returns it
// too. A skipped suite prints a line about the suite result of the run and, for a workflow
// with no plan type, a line about the start commit.
func TestASkippedSuiteKeepsTheRecordBefore(t *testing.T) {
	before := &suiteRecord{path: "/x/suite-1.json"}
	h := newHelper(t)
	h.state(t, "skipped", "")
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded); err != nil {
		t.Fatal(err)
	}
	if h.record != nil || h.kept(context.Background(), before) != before {
		t.Errorf("record %+v, kept %+v, want no record and the one before", h.record, h.kept(context.Background(), before))
	}
	if !slices.Contains(*h.said, textSuiteHasLast) {
		t.Errorf("lines %q, want %q", *h.said, textSuiteHasLast)
	}
	h.noPlan = noPlanFault("fix")
	h.state(t, "skipped", "")
	*h.said = nil
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded); err != nil || !slices.Contains(*h.said, textSuiteSkipped) {
		t.Errorf("error %v, lines %q, want %q", err, *h.said, textSuiteSkipped)
	}
	var none *suiteRun
	if none.kept(context.Background(), before) != before {
		t.Error("a session with no suite did not keep the record before")
	}
	h = newHelper(t)
	h.state(t, "ran "+suiteSHA, greenResult)
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded); err != nil {
		t.Fatal(err)
	}
	if got := h.kept(context.Background(), before); got == before || got != h.record {
		t.Errorf("kept %+v, want the record of the suite that ran", got)
	}
}

// TestASuiteResultFileIsNewForEachSessionAndNeverReplaced: sessions 2 and 3 of a run write
// two files, and a second write for session 3 does not replace the first, so qory prints
// that and the record has no path.
func TestASuiteResultFileIsNewForEachSessionAndNeverReplaced(t *testing.T) {
	h := newHelper(t)
	var paths []string
	for _, n := range []int{2, 3} {
		h.session, h.record = n, nil
		h.state(t, "ran "+suiteSHA, greenResult)
		if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, h.record.path)
	}
	if paths[0] == paths[1] || filepath.Base(paths[0]) != "suite-2.json" || filepath.Base(paths[1]) != "suite-3.json" {
		t.Errorf("the paths are %q, want a new one for each session", paths)
	}
	before, err := os.ReadFile(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	h.record = nil
	*h.said = nil
	h.state(t, "ran "+suiteSHA, redResult)
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, succeeded); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(paths[1]); string(after) != string(before) || h.record == nil || h.record.path != "" {
		t.Errorf("the file changed or the record is %+v, want the first file and no path", h.record)
	}
	if lines := *h.said; len(lines) == 0 || !strings.HasPrefix(lines[0], "qory could not write the suite result file of the review.\n  The suite result file ") {
		t.Errorf("lines %q, want the line that starts with the cause", lines)
	}
}
