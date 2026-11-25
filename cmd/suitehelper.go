package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/sink"

	"github.com/qoryai/qory/internal/hookrun"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/suite"
	"github.com/qoryai/qory/internal/verdictformat"
)

// maxPlan is the most bytes of the file of a plan that qory reads.
const maxPlan = 1 << 20

// planFile is the name of the file of a version of the plan type plan: the type and ".md".
func planFile(plan string) string { return plan + ".md" }

// noPlanCommand is the command of the after step of a session whose workflow has no plan
// type. The step still checks whether HEAD moved, which the script of [suite.Command] decides: a
// session that left HEAD where it started is skipped, and the command of any other is not a
// suite, see [suiteRun.noPlan].
const noPlanCommand = "true"

// The lines of qory run about the full suite. A line with an instruction starts with the fix,
// and has at most 100 characters in its first line.
const (
	textSuiteSkipped = "the full suite did not run: the session left HEAD at the commit it started at"
	textSuiteGreen   = "the full suite passed, so qory posts no verdict"
	textSuiteCut     = "qory run was stopped while the full suite ran, so the run closes cancelled"
	textSuiteStands  = "the verdict %s of the agent stands over the red full suite"
	textSuitePosted  = "posted the verdict %s for the full suite"
	textSuiteNoRoom  = "the verdict of the agent has 20 points already, so qory adds none for the full suite"
	textSuiteNoPost  = "Take the work item in again in Qory Apiary: qory could not post the suite's verdict.\n  %s"
	textSuiteNoClear = "Remove .git/qory-suite from the session's folder: qory could not remove it.\n  %s"
)

// The lines of the point of the verdict that a red suite sets. Each is one sentence on one line
// of at most 100 characters in its first line.
const (
	textPointHosts    = "Allow %s in the run's policy, or change the suite: it failed after the refusal%s."
	textPointDenied   = "Refused connections: the run's Connections tab, Denied."
	textPointFailed   = "The full suite failed: %d of %d tests failed."
	textPointExit     = "The full suite failed: its command exited with status %d."
	textPointNoTests  = "The full suite ran 0 tests, so it proves nothing."
	textPointMismatch = "The full suite failed: the totals of its result file do not match its testcases."
	textPointStopped  = "The full suite did not finish: the run was stopped."
	textPointTimeout  = "The full suite did not finish: the after step reached its limit of %d minutes."
	maxPointLine      = 100
)

// The points of a suite that did not start, which a person must see: the cause follows the colon.
const (
	textShownMac           = "The full suite needs a Linux machine; this machine runs Docker on macOS."
	textShownNone          = "The full suite's container did not start."
	textShownExit          = "The full suite's container exited with status "
	textPointNoStartCap    = "The full suite did not start: the after step used its %d minutes before the command."
	textPointNoStartMac    = "The full suite needs a Linux machine, and this run's machine runs Docker on macOS."
	textPointNoStartNone   = "The full suite did not start, because its container did not start."
	textPointNoStartFolder = "The full suite did not start: the script could not make a folder it needs."
	textPointNoStartClone  = "The full suite did not start: git could not clone HEAD of the workspace."
	textPointNoStartHead   = "The full suite did not start: git could not read HEAD of the workspace."
	textPointNoStartShell  = "The full suite did not start: the image may lack sh or git (exit 127)."
	textPointNoStartExit   = "The full suite did not start, because its container exited with status %d."
)

// suiteMinutes is the time the suite's cap counts in: the cap is limit minutes.
const suiteMinutes = time.Minute

// maxVerdictPoints is the most points a verdict has, 20, as the verdict format allows.
const maxVerdictPoints = 20

// errNoVerdictFile is the error of a version of the verdict of the run that has no file.
var errNoVerdictFile = errors.New("the run's verdict has no file")

// errNoPlan is why a run has no suite command: the run started with no version of its plan
// type, or qory could not read it.
var errNoPlan = errors.New("the run started with no version of its plan type")

// errPlanTooBig is the error of a plan whose file is over [maxPlan] bytes.
var errPlanTooBig = errors.New("the plan is too large")

// suiteSetup is what [openSuites] needs from a run that has its session about to join.
type suiteSetup struct {
	run    openedRun
	spec   *runSpec
	runID  string
	walled bool
	// readOnly is whether the run only reads the checkout, so no commit comes of it.
	readOnly bool
	// fullSuite is full_suite of the run documents' type_settings: whether the session runs
	// the full suite.
	fullSuite bool
	// workflow is the workflow of the run, and plan the plan type set in
	// workflows.<name>.plan for it, "" for a workflow that sets none.
	workflow, plan string
	// extensions is the harness extensions of the report that the run composed.
	extensions map[string]any
	said       func(string)
}

// suiteRun is the full suite of one session: the after step that runs it, and the judgement of
// its outcome and the post of its verdict, see [suiteRun.after].
type suiteRun struct {
	run     openedRun
	runID   string
	said    func(string)
	dir     string
	full    suite.Full
	minutes int
	// fault is why no suite runs for this session, nil when the after step is set.
	fault *suiteFault
	// noPlan is set for a session whose workflow has no plan type: the fault that a session
	// whose HEAD moved gets. A session whose HEAD did not move is skipped, as with a plan.
	noPlan *suiteFault
}

// openSuites sets the after step of the full suite on the session's spec and returns the run that
// judges it; nil when the session runs none. A cause that stops the step is kept as the run's fault.
func openSuites(ctx context.Context, s suiteSetup) *suiteRun {
	if !s.walled || s.readOnly || !s.fullSuite {
		return nil
	}
	r := &suiteRun{run: s.run, runID: s.runID, said: s.said, dir: s.spec.Dir, minutes: suite.LimitMinutes(s.extensions)}
	if s.plan == "" {
		// No version of a plan type sets a command, so the after step only checks whether HEAD
		// moved.
		r.full, r.noPlan = suite.Full{Command: noPlanCommand}, noPlanFault(s.workflow)
	} else {
		// A cause that no After step can show is kept, and judged once the session ends.
		if _, _, err := s.run.RunAPI(); err != nil {
			r.fault = noRunAPIFault()
			s.said(r.fault.line())
			return r
		}
		planCtx, cancelPlan := context.WithTimeout(ctx, suitePlanWait)
		var err error
		r.full, err = launchFull(planCtx, s.run, s.plan)
		cancelPlan()
		if err != nil {
			r.fault = faultOf(err, s.plan)
			s.said(r.fault.line())
			return r
		}
	}
	if info, err := os.Lstat(filepath.Join(s.spec.Dir, ".git")); err != nil || !info.IsDir() {
		r.fault = noGitFault()
		s.said(r.fault.line())
		return r
	}
	after, err := suite.Command(headCommit(ctx, s.spec.Dir), r.full)
	if err != nil {
		r.fault = faultOf(err, s.plan)
		s.said(r.fault.line())
		return r
	}
	s.spec.After, s.spec.AfterTimeout = after, afterLimit(r.minutes)
	return r
}

// afterLimit is the time limit of the after step: the suite's cap alone. The session's time
// limit covers the agent's runtime only.
func afterLimit(minutes int) time.Duration { return time.Duration(minutes) * suiteMinutes }

// fullSuiteOf is whether the session of a run from Qory Apiary runs the full suite: full_suite
// of the run documents' type_settings, false for a run with no run code.
var fullSuiteOf = func(c *codeRun) bool { return c != nil && c.fullSuite }

// planOf is the workflow of a run from Qory Apiary and the plan type set in
// workflows.<name>.plan for it, "" for each when the run has no run code and "" for the type of a
// workflow that sets none.
var planOf = func(c *codeRun, workflows map[string]stack.Workflow) (workflow, plan string) {
	if c == nil {
		return "", ""
	}
	return c.workflow, workflows[c.workflow].Plan
}

// textFullSuiteNoPlan is the refusal of a session whose full_suite flag is set and whose workflow
// has write-no-push access. The flag comes from the run documents for session 1 and from the
// next-session answer for a later session. That access allows no plan, see [stack.Workflow], so
// no plan type could give the suite its full command. Line 1 is the cause and has no fix, since
// the reader cannot change full_suite. The first %s is "The run documents ask" for session 1 and
// "The next-session answer asks" for a later session, the second is "run" for session 1 and
// "session" for a later session, the third is the workflow.
const textFullSuiteNoPlan = "%s for the full suite in a write-no-push workflow.\n  The %s did not start: %s has write-no-push access, so it has no plan to read the full suite from" + codeUsed

// fullSuiteNoPlan is the error of a run from a run code whose full_suite flag is set and whose
// workflow, in workflows, has write-no-push access, nil for any other run. The flag comes from the
// run documents for session 1 and from the next-session answer for a later session (later is
// true). A run that only reads the checkout runs no suite and is not refused. The error is exit 1
// and closes the run failed before the join. Line 1 is the cause. For a later session line 1
// starts "The next-session answer asks", not "The run documents ask", and line 2 says "The
// session did not start", not "The run did not start".
func fullSuiteNoPlan(c *codeRun, workflows map[string]stack.Workflow, later bool) error {
	declared := (&stack.Stack{Workflows: workflows}).AccessOf(c.workflow)
	if !fullSuiteOf(c) || declared != stack.AccessWriteNoPush || readsOnly(c, workflows) {
		return nil
	}
	asks, started := "The run documents ask", "run"
	if later {
		asks, started = "The next-session answer asks", "session"
	}
	return fmt.Errorf(textFullSuiteNoPlan, asks, started, c.workflow)
}

// suitePlanWait is the longest the read of the plan at the run's start may take.
const suitePlanWait = 15 * time.Second

// headCommit is the commit that HEAD has in dir, "" when dir has none or [hookrun.Head] cannot
// read it for certain. It runs no git: a run's setup and its agents can write dir, so git here
// could run a program of their configuration.
func headCommit(_ context.Context, dir string) string {
	head, err := hookrun.Head(dir)
	if err != nil {
		return ""
	}
	return head
}

// launchFull is the full row of the plan type plan, in the version that the run's work item has at
// the run's launch, read through the run API, from the work item's artifacts. An error is
// [errNoPlan] or an error of [suite.FullRow].
func launchFull(ctx context.Context, run openedRun, plan string) (suite.Full, error) {
	httpClient, base, err := run.RunAPI()
	if err != nil {
		return suite.Full{}, fmt.Errorf("%w: %s", errNoPlan, shownError(err))
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return suite.Full{}, fmt.Errorf("%w: %s", errNoPlan, shownError(err))
	}
	text, err := readPlan(ctx, client, plan)
	if err != nil {
		return suite.Full{}, err
	}
	return suite.FullRow(text)
}

// readPlan is the text of the newest version of the artifact of type plan, as the index lists it:
// the name main, or the one name; a withdrawn name has none. The file is [planFile].
func readPlan(ctx context.Context, client *runapi.Client, plan string) (string, error) {
	index, err := client.Artifacts(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %s", errNoPlan, shownError(err))
	}
	var plans []runapi.Artifact
	for _, a := range index.Artifacts {
		if a.Type == plan && !withdrawn(a) {
			plans = append(plans, a)
		}
	}
	chosen := -1
	for i, a := range plans {
		if a.Name == defaultName || len(plans) == 1 {
			chosen = i
			break
		}
	}
	if chosen < 0 {
		return "", errNoPlan
	}
	version := newestBeforeWithdrawal(plans[chosen])
	if version == nil {
		return "", errNoPlan
	}
	for _, file := range version.Files {
		if file.Filename != planFile(plan) {
			continue
		}
		if file.Size > maxPlan {
			return "", fmt.Errorf("%w: %s is over %d bytes", errPlanTooBig, planFile(plan), maxPlan)
		}
		var text bytes.Buffer
		if err := client.Download(ctx, version.ID, file, &text); err != nil {
			return "", fmt.Errorf("%w: %s", errNoPlan, shownError(err))
		}
		return text.String(), nil
	}
	return "", errNoPlan
}

// after judges the suite, writes its result, and posts the verdict of a suite that did not pass
// when no result file is named, before the push step. A red suite leaves the run's outcome. A
// post that fails closes the run failed, with the end code of its error, see [endCodeOf], and
// with no end code when a stop or a limit cut it.
func (r *suiteRun) after(ctx, again context.Context, limit time.Time, res *session.Result, out session.Outcome) (session.Outcome, error) {
	if r == nil {
		return out, nil
	}
	defer func() {
		if err := suite.Clear(r.dir); err != nil {
			r.said(fmt.Sprintf(textSuiteNoClear, shownError(err)))
		}
	}()
	fault := r.fault
	if fault == nil && (res == nil || res.After == nil) {
		fault = noAfterFault()
	}
	var got suite.Outcome
	var point *runapi.Point
	kind := kindRed
	if fault == nil {
		a := res.After
		got = suite.Judge(r.dir, r.full, suite.End{
			Succeeded: a.State == event.StateSucceeded,
			Cancelled: a.State == event.StateCancelled,
			TimedOut:  a.Reason == event.ReasonTimeout,
			ExitCode:  a.ExitCode,
		})
		// A workflow with no plan type has no suite: a session that moved HEAD gets the fault, and
		// one that did not is skipped. A step that did not get as far as its command, or a stop
		// that cut it, is judged as it is with a plan.
		if r.noPlan != nil && got.Started && got.State != suite.Stopped {
			fault = r.noPlan
		}
	}
	if fault != nil {
		point, kind = &fault.point, fault.kind()
	} else {
		a := res.After
		if got.State.Red() {
			refused := suite.Refused{Local: true}
			if got.Started && got.State != suite.Stopped {
				refused = suite.ReadRefused(filepath.Join(r.run.Dir(), sink.EventsFile), a.Step)
			}
			p := suitePoint(got, refused, r.minutes)
			point = &p
		}
		if got.NotStarted() {
			kind = kindUnfixable
		}
	}
	if out.State != event.StateSucceeded {
		return out, nil
	}
	if fault == nil && got.State == suite.Stopped {
		return r.cut(ctx, again, limit, *point)
	}
	switch {
	case fault != nil:
		r.said(shownLine(point.Text))
	case got.State == suite.Skipped:
		r.said(textSuiteSkipped)
	case got.State == suite.Passed:
		r.said(endedLine(got))
		r.said(textSuiteGreen)
	case got.NotStarted():
		r.said(shownLine(point.Text))
	default:
		r.said(endedLine(got))
	}
	if point == nil {
		return out, nil
	}
	postCtx, cancelPost := pushContext(ctx, again, limit, pushTimes.limit, pushTimes.signal)
	defer cancelPost()
	posted, err := r.post(postCtx, *point, kind, maxSubmissionTries)
	if err != nil {
		r.said(fmt.Sprintf(textSuiteNoPost, shownError(err)))
		failed := session.Outcome{State: event.StateFailed, ExitCode: 1}
		// A stop or a limit that ended postCtx cut the post: no call failed, so there is no
		// end code.
		if postCtx.Err() == nil {
			failed.Reason = string(endCodeOf(err))
		}
		return failed, &exitError{code: 1}
	}
	if posted != "" {
		r.said(posted)
	}
	return out, nil
}

// cutPostWait is the longest the post of the verdict may take once a stop cut the suite, with one
// try: the push step follows within the time that a signal leaves.
const cutPostWait = 5 * time.Second

// cut ends the run after a stop cut the after step of a session that succeeded: qory posts the
// red verdict with one try and closes the run cancelled, interrupted, exit 1.
func (r *suiteRun) cut(ctx, again context.Context, limit time.Time, point runapi.Point) (session.Outcome, error) {
	r.said(textSuiteCut)
	postCtx, cancelPost := pushContext(ctx, again, limit, pushTimes.limit, pushTimes.signal)
	defer cancelPost()
	postCtx, cancelWait := context.WithTimeout(postCtx, cutPostWait)
	defer cancelWait()
	posted, err := r.post(postCtx, point, kindRed, 1)
	switch {
	case err != nil:
		r.said(fmt.Sprintf(textSuiteNoPost, shownError(err)))
	case posted != "":
		r.said(posted)
	}
	return session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}, &exitError{code: 1}
}

// endedLine is the line of qory run for a suite that ended: "the full suite passed on 0123456789ab:
// 140 tests" or "the full suite failed on 0123456789ab: 2 of 140 tests failed".
func endedLine(got suite.Outcome) string {
	verb := map[suite.State]string{suite.Passed: "passed", suite.Failed: "failed", suite.Timeout: "timed out"}[got.State]
	line := "the full suite " + verb + onCommit(got.Commit)
	switch c := got.Counts; {
	case c == nil:
	case c.Failed > 0:
		line += fmt.Sprintf(": %d of %d tests failed", c.Failed, c.Tests)
	default:
		line += fmt.Sprintf(": %d tests", c.Tests)
	}
	return line
}

// post posts the verdict for a suite that did not pass, with at most tries requests, and returns
// the line for what it posted, "" for nothing. The rules by prior verdict are in docs/run.md.
func (r *suiteRun) post(ctx context.Context, point runapi.Point, kind suiteKind, tries int) (string, error) {
	httpClient, base, err := r.run.RunAPI()
	if err != nil {
		return "", err
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return "", err
	}
	agent, err := agentVerdict(ctx, client, r.runID)
	if err != nil {
		return "", err
	}
	// verdict is what qory posts over a prior verdict of the agent, and points the points it has.
	verdict, points := runapi.VerdictChangesRequested, []runapi.Point{point}
	switch {
	case agent == nil || agent.verdict == runapi.VerdictPassed:
		if kind == kindUnfixable {
			verdict = runapi.VerdictEscalate
		}
	case agent.verdict == runapi.VerdictEscalate || !verdictformat.Known(agent.verdict):
		// A kind that this qory does not know stops at a person, as escalate does.
		verdict, points = runapi.VerdictEscalate, append(agent.points, point)
	case kind == kindRed:
		return fmt.Sprintf(textSuiteStands, agent.verdict), nil
	default:
		verdict, points = runapi.VerdictChangesRequested, append(agent.points, point)
		if kind == kindUnfixable {
			verdict = runapi.VerdictEscalate
		}
	}
	if len(points) > maxVerdictPoints {
		return textSuiteNoRoom, nil
	}
	var track *runapi.Track
	if agent != nil {
		track = agent.track
	}
	if err := postVerdict(ctx, client, verdict, points, track, tries); err != nil {
		return "", err
	}
	return fmt.Sprintf(textSuitePosted, verdict), nil
}

// suitePoint is the one point for a suite that did not pass. A host text that the verdict format
// refuses is left out of its first line.
func suitePoint(got suite.Outcome, refused suite.Refused, minutes int) runapi.Point {
	p := buildPoint(got, refused, minutes)
	if len(refused.Hosts) > 0 && !validPoint(p) {
		refused.Hosts = nil
		p = buildPoint(got, refused, minutes)
	}
	return p
}

func buildPoint(got suite.Outcome, refused suite.Refused, minutes int) runapi.Point {
	p := runapi.Point{Text: pointLine(got, refused, minutes), Action: "run_again"}
	if !got.NotStarted() && (!refused.Local || refused.Denied > 0) {
		p.Fix = textPointDenied
	}
	return p
}

// validPoint reports whether a verdict with p as its one point passes the verdict format.
func validPoint(p runapi.Point) bool {
	body, err := runapi.VerdictBody(runapi.VerdictChangesRequested, []runapi.Point{p}, nil)
	return err == nil && verdictformat.Check(body) == nil
}

// pointLine is the first line of the point of a suite that did not pass: at most [maxPointLine]
// characters.
func pointLine(got suite.Outcome, refused suite.Refused, minutes int) string {
	switch c := got.Counts; {
	case got.State == suite.Stopped:
		return textPointStopped
	case got.NotStarted() && got.State == suite.Timeout:
		return fmt.Sprintf(textPointNoStartCap, minutes)
	case got.NotStarted():
		return notStartedLine(got.ExitCode)
	case len(refused.Hosts) > 0:
		return hostsLine(refused.Hosts)
	case got.State == suite.Timeout:
		return fmt.Sprintf(textPointTimeout, minutes)
	case got.Mismatch:
		return textPointMismatch
	case c != nil && c.Tests == 0:
		return textPointNoTests
	case c != nil && c.Failed > 0:
		return fmt.Sprintf(textPointFailed, c.Failed, c.Tests)
	}
	return fmt.Sprintf(textPointExit, got.ExitCode)
}

// shownLine is the line of qory run for the point text: the text itself, or a shorter one with the
// same cause when "qory run: " and the text would pass 100 characters.
func shownLine(text string) string {
	switch {
	case text == textPointNoStartMac:
		return textShownMac
	case text == textPointNoStartNone:
		return textShownNone
	case text == textFaultBadFile:
		return textShownBadFile
	}
	if status, ok := strings.CutPrefix(text, strings.TrimSuffix(textPointNoStartExit, "%d.")); ok {
		return textShownExit + status
	}
	return text
}

// goos is the operating system that qory runs on; a test sets it.
var goos = runtime.GOOS

// notStartedLine is the first line of the point of a suite whose command never ran, by the exit
// status of the script, or -1 for an after step that ended with none.
func notStartedLine(exit int) string {
	switch {
	case exit == suite.ExitNoState:
		return textPointNoStartFolder
	case exit == suite.ExitNoClone:
		return textPointNoStartClone
	case exit == suite.ExitNoHead:
		return textPointNoStartHead
	case exit == 127:
		return textPointNoStartShell
	case goos == "darwin":
		return textPointNoStartMac
	case exit < 0:
		return textPointNoStartNone
	}
	return fmt.Sprintf(textPointNoStartExit, exit)
}

// hostsLine is the line for the refused hosts, at most [maxPointLine] characters: the first host
// whole when it fits, with "and N more" for the others, else cut, and "..." ends a cut host.
func hostsLine(hosts []string) string {
	host, suffix, end := hosts[0], "", ""
	if more := len(hosts) - 1; more > 0 {
		suffix, end = fmt.Sprintf(" and %d more", more), "s"
	}
	build := func(h string) string { return fmt.Sprintf(textPointHosts, h+suffix, end) }
	room := max(maxPointLine-len(build("")), minHostShown)
	return build(cutHost(host, room))
}

// minHostShown is the fewest characters of a host that a line shows, "..." included.
const minHostShown = 4

// cutHost is host in at most n characters: a host that was cut ends in "...", after no "." or "-".
func cutHost(host string, n int) string {
	if len(host) <= n {
		return host
	}
	return strings.TrimRight(host[:n-3], ".-") + "..."
}

// onCommit is " on <commit>" for a suite that ran on a commit, and "" for one that never got to it.
func onCommit(commit string) string {
	if commit == "" {
		return ""
	}
	return " on " + shortCommit(commit)
}

// shortCommit is the first 12 characters of a commit id, for a line of qory run.
func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// priorVerdict is the verdict that the run wrote: its verdict, points and track.
type priorVerdict struct {
	verdict string
	points  []runapi.Point
	track   *runapi.Track
}

// agentVerdict is the newest verdict that the run wrote, nil when it wrote none. It is read
// from the artifact index and its file, since the work item's newest verdict can be an earlier
// run's. A member whose name differs from a defined one only in letter case is not read.
func agentVerdict(ctx context.Context, client *runapi.Client, runID string) (*priorVerdict, error) {
	ctx, cancel := context.WithTimeout(ctx, runAPITimeout)
	defer cancel()
	index, err := client.Artifacts(ctx)
	if err != nil {
		return nil, err
	}
	var newest *runapi.Version
	for i := range index.Artifacts {
		a := &index.Artifacts[i]
		if a.Type != verdictType || a.Name != defaultName {
			continue
		}
		for j := range a.Versions {
			v := &a.Versions[j]
			if v.Writer.Kind == "run" && v.Writer.RunID == runID && !v.Cancelled && !v.Withdrawn && (newest == nil || v.Number > newest.Number) {
				newest = v
			}
		}
	}
	if newest == nil {
		return nil, nil
	}
	if len(newest.Files) == 0 || newest.Files[0].Size > 64<<10 {
		return nil, errNoVerdictFile
	}
	var file bytes.Buffer
	if err := client.Download(ctx, newest.ID, newest.Files[0], &file); err != nil {
		return nil, err
	}
	var d struct {
		Verdict string         `json:"verdict,case:strict"`
		Points  []runapi.Point `json:"points,case:strict"`
		Track   *runapi.Track  `json:"track,case:strict"`
	}
	if err := json.Unmarshal(file.Bytes(), &d); err != nil {
		return nil, fmt.Errorf("the run's verdict is not a verdict document: %w", err)
	}
	return &priorVerdict{d.Verdict, d.Points, d.Track}, nil
}

// postVerdict posts the verdict as the run's verdict, up to tries times: a verdict sent twice is
// stored twice with the same content, so a request that got no response is sent again.
func postVerdict(ctx context.Context, client *runapi.Client, verdict string, points []runapi.Point, track *runapi.Track, tries int) error {
	body, err := runapi.VerdictBody(verdict, points, track)
	if err != nil {
		return err
	}
	if err := verdictformat.Check(body); err != nil {
		return err
	}
	var last error
	for try := 1; try <= tries; try++ {
		if try > 1 {
			if err := submissionWait(ctx, retryAfter(last)); err != nil {
				return err
			}
		}
		one, cancel := context.WithTimeout(ctx, runAPITimeout)
		_, last = client.PushVerdict(one, verdict, points, track)
		cancel()
		switch {
		case last == nil:
			return nil
		case ctx.Err() != nil:
			return last
		case runapi.HasCode(last, runapi.CodeRateLimited), noResponse(last):
		default:
			return last
		}
	}
	return last
}
