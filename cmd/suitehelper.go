package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/suite"
)

// The type and the file of the build plan, and the most bytes of its file qory reads.
const (
	buildPlanType = "build-plan"
	buildPlanFile = "build-plan.md"
	maxBuildPlan  = 1 << 20
	suitesDir     = "suites"
)

// The lines of qory run about the full suite. A line with an instruction starts with the fix,
// and has at most 100 characters in its first line.
const (
	textSuiteNotStarted = "Add a full row to the build plan and take the work item in again: no suite started.\n  %s"
	textSuiteNoFolder   = "Start the run again: qory could not make the folder of the suites.\n  %s"
	textSuiteStarted    = "started the full suite %s on %s"
	textSuiteWaiting    = "waiting for the full suite %s on %s, until it ends or the run's time limit"
	textSuiteEnded      = "the full suite %s on %s is %s%s"
	textSuiteGreen      = "the full suite passed, so qory posts no verdict"
	textSuiteStands     = "the verdict %s of the agent stands over the red full suite"
	textSuitePosted     = "posted the verdict %s for the red full suite"
	textSuiteNoRoom     = "the verdict escalate has 20 points already, so qory adds none for the red full suite"
	textSuiteNoPost     = "Take the work item in again in Qory Apiary: qory could not post the verdict of the red full suite.\n  %s"
	textSuiteCutLimit   = "the run reached its time limit of %s while qory waited for the full suite"
	textSuiteCutStop    = "qory run got %s while qory waited for the full suite"
	textSuiteCutSecond  = "qory run got a second signal while qory waited for the full suite"
)

// The points of the verdict that a red suite sets. Each is one sentence on one line.
const (
	textPointFailedTests = "The full suite failed on %s: %d of %d tests failed."
	textPointNoTests     = "The full suite ran 0 tests on %s, so it proves nothing."
	textPointFailed      = "The full suite failed on %s."
	textPointTimeout     = "The full suite did not finish on %s within %d minutes, and qory stopped it."
	textPointStopped     = "The full suite on %s was stopped before it finished."
)

// suiteCutPost is the longest the post of a verdict may take once a stop or the run's time limit
// cut the wait, with one try: the push step follows within the time that a signal leaves.
const suiteCutPost = 5 * time.Second

// suitePlanWait is the longest the read of the build plan at the run's start may take.
const suitePlanWait = 15 * time.Second

// suiteMinute is the length of a minute of the suite's limit; a test sets a shorter one.
var suiteMinute = time.Minute

// maxVerdictPoints is the most points a verdict has, 20, as the verdict format allows.
const maxVerdictPoints = 20

// errNoVerdictFile is the error of a version of the verdict of the run that has no file.
var errNoVerdictFile = errors.New("the run's verdict has no file")

// suiteSetup is what [openSuites] needs from a run that has its session about to join.
type suiteSetup struct {
	run   openedRun
	spec  *runSpec
	route *qoryRoute
	runID string
	// walled is whether the run has a wall; a run with no wall has no suite.
	walled bool
	// source is the session's checkout, which the commits are cloned from.
	source string
	// extensions is the harness extensions of the report that the run composed.
	extensions map[string]any
	// timeout is the run's time limit, 0 for none.
	timeout time.Duration
	said    func(string)
}

// suiteRun is the full suite of a walled run: the registry that runs the suites on the
// route's requests, and the helper that, once the session exited, waits for the newest
// suite and posts its verdict, see [suiteRun.after].
type suiteRun struct {
	reg   *suite.Registry
	run   openedRun
	runID string
	said  func(string)
	// limit is the time one suite may take, minutes of [suiteMinute], and timeout the run's time
	// limit.
	limit, timeout time.Duration
	minutes        int
	// none is why reg is nil: the run has no suite command, see [suiteHost.none].
	none error
}

// openSuites sets up the suites of the run described by s before its session joins and
// sets the suite paths on its route; nil for a run with no wall. The command of the suite is the
// full row of the build plan of the run's launch, read here from the run API, so a plan that the agent
// pushes during the session changes nothing. A run that has no control plane, no plan or no
// full row has the paths too: a request gets the reason.
func openSuites(ctx context.Context, s suiteSetup) *suiteRun {
	if !s.walled {
		return nil
	}
	host := &suiteHost{said: s.said}
	minutes := suite.LimitMinutes(s.extensions)
	r := &suiteRun{run: s.run, runID: s.runID, said: s.said, limit: time.Duration(minutes) * suiteMinute, minutes: minutes, timeout: s.timeout}
	planCtx, cancelPlan := context.WithTimeout(ctx, suitePlanWait)
	full, err := launchFull(planCtx, s.run)
	cancelPlan()
	if err == nil {
		if mkErr := os.MkdirAll(filepath.Join(s.run.Dir(), suitesDir), 0o700); mkErr != nil {
			err = fmt.Errorf("%w: %s", errSuiteFolder, shownError(mkErr))
		}
	}
	if err != nil {
		r.none, host.none = err, err
		s.route.useSuites(host)
		return r
	}
	r.reg = suite.New(ctx, suite.Config{
		Source: s.source, Root: filepath.Join(s.run.Dir(), suitesDir), Full: full, Limit: r.limit,
		Run: suiteRunner(s.run, s.spec, s.runID), Mask: secretmask.Default().Replace, Ended: r.ended,
	})
	host.reg = r.reg
	s.route.useSuites(host)
	return r
}

// launchFull is the full row of the build plan that the run's work item has at the run's launch,
// read through the run API, from the work item's artifacts. An error is [errNoBuildPlan] or an error of [suite.FullRow].
func launchFull(ctx context.Context, run openedRun) (suite.Full, error) {
	httpClient, base, err := run.RunAPI()
	if err != nil {
		return suite.Full{}, fmt.Errorf("%w: %s", errNoBuildPlan, shownError(err))
	}
	client, err := runapi.New(base, httpClient)
	if err != nil {
		return suite.Full{}, fmt.Errorf("%w: %s", errNoBuildPlan, shownError(err))
	}
	text, err := readBuildPlan(ctx, client)
	if err != nil {
		return suite.Full{}, err
	}
	return suite.FullRow(text)
}

// readBuildPlan is the text of the newest version of the artifact build-plan, as the index
// lists it: the name main, or the one name; a withdrawn name has none.
func readBuildPlan(ctx context.Context, client *runapi.Client) (string, error) {
	index, err := client.Artifacts(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %s", errNoBuildPlan, shownError(err))
	}
	var plans []runapi.Artifact
	for _, a := range index.Artifacts {
		if a.Type == buildPlanType && !withdrawn(a) {
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
		return "", errNoBuildPlan
	}
	version := newestBeforeWithdrawal(plans[chosen])
	if version == nil {
		return "", errNoBuildPlan
	}
	for _, file := range version.Files {
		if file.Filename != buildPlanFile {
			continue
		}
		if file.Size > maxBuildPlan {
			return "", fmt.Errorf("%w: %s is over %d bytes", errNoBuildPlan, buildPlanFile, maxBuildPlan)
		}
		var text bytes.Buffer
		if err := client.Download(ctx, version.ID, file, &text); err != nil {
			return "", fmt.Errorf("%w: %s", errNoBuildPlan, shownError(err))
		}
		return text.String(), nil
	}
	return "", errNoBuildPlan
}

// ended prints the line of a suite that ended.
func (r *suiteRun) ended(s suite.Snapshot) {
	r.said(fmt.Sprintf(textSuiteEnded, s.ID, shortCommit(s.Commit), s.State, countsSuffix(s.Counts)))
}

// countsSuffix is the counts of a suite as the end of its line shows them: ", 2 of 140 tests
// failed" or ", 140 tests", and "" for no counts.
func countsSuffix(c *suite.Counts) string {
	switch {
	case c == nil:
		return ""
	case c.Failed > 0:
		return fmt.Sprintf(", %d of %d tests failed", c.Failed, c.Tests)
	}
	return fmt.Sprintf(", %d tests", c.Tests)
}

// close stops every suite and removes their clones. It is safe on nil and after
// [suiteRun.after].
func (r *suiteRun) close() {
	if r == nil || r.reg == nil {
		return
	}
	if err := r.reg.Close(); err != nil {
		r.said("remove the folders of the full suites under the run's folder: " + shownError(err))
	}
	// The folder is empty after Close; a folder that is not stays for the run's removal.
	_ = os.Remove(filepath.Join(r.run.Dir(), suitesDir))
}

// after waits for the newest suite once the session exited with out, before the push step, and
// posts the verdict of a red suite: see [suiteRun.post]. It returns the outcome the run closes
// with, and the error that sets the exit status; a cut wait or a failed post changes them.
func (r *suiteRun) after(ctx, again context.Context, limit time.Time, stopNow func() stopRead, out session.Outcome) (session.Outcome, error) {
	if r == nil || r.reg == nil {
		return out, nil
	}
	defer r.reg.Stop()
	if out.State != event.StateSucceeded {
		return out, nil
	}
	if now, ok := r.reg.Newest(); ok && now.State == suite.Running {
		r.said(fmt.Sprintf(textSuiteWaiting, now.ID, shortCommit(now.Commit)))
	}
	wait, cancelWait := ctx, context.CancelFunc(func() {})
	if !limit.IsZero() {
		wait, cancelWait = context.WithDeadline(ctx, limit)
	}
	snap, started, waitErr := r.reg.Wait(wait)
	cut := waitErr != nil
	cancelWait()
	if !started {
		return out, nil
	}
	if cut {
		r.reg.Stop()
		snap, _ = r.reg.Newest()
	}
	if snap.State == suite.Passed {
		r.said(textSuiteGreen)
		return out, nil
	}
	postCtx, cancelPost := pushContext(ctx, again, limit, pushTimes.limit, pushTimes.signal)
	defer cancelPost()
	tries := maxBranchTries
	if cut {
		var cancelCut context.CancelFunc
		postCtx, cancelCut = context.WithTimeout(postCtx, suiteCutPost)
		defer cancelCut()
		tries = 1
	}
	posted, err := r.post(postCtx, snap, tries)
	if err != nil {
		r.said(fmt.Sprintf(textSuiteNoPost, shownError(err)))
	} else if posted != "" {
		r.said(posted)
	}
	if cut {
		return r.cutOutcome(stopNow())
	}
	if err != nil {
		return session.Outcome{State: event.StateFailed, ExitCode: 1}, &exitError{code: 1}
	}
	return out, nil
}

// cutOutcome is the outcome of a run whose wait for the suite was cut, and the exit status:
// as a cut push step closes the run, interrupted for a stop and timeout for the time limit.
func (r *suiteRun) cutOutcome(now stopRead) (session.Outcome, error) {
	switch {
	case now.again:
		r.said(textSuiteCutSecond)
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}, &exitError{code: 1}
	case now.stopped:
		r.said(fmt.Sprintf(textSuiteCutStop, now.got))
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}, &exitError{code: 1}
	}
	r.said(fmt.Sprintf(textSuiteCutLimit, r.timeout))
	return session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: exitTimeout}, &exitError{code: exitTimeout}
}

// post posts the verdict of the red suite snap, with at most tries requests, and returns the line that describes what it posted: ""
// when the agent's verdict stands and nothing went out.
func (r *suiteRun) post(ctx context.Context, snap suite.Snapshot, tries int) (string, error) {
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
	point := runapi.Point{Text: suitePoint(snap, r.minutes), Action: "run_again"}
	switch {
	case agent == nil || agent.Verdict == runapi.VerdictPassed:
		body, err := runapi.VerdictBody(runapi.VerdictChangesRequested, []runapi.Point{point})
		if err != nil {
			return "", err
		}
		if err := postVerdict(ctx, client, body, tries); err != nil {
			return "", err
		}
		return fmt.Sprintf(textSuitePosted, runapi.VerdictChangesRequested), nil
	case agent.Verdict == runapi.VerdictEscalate && len(agent.Points) < maxVerdictPoints:
		agent.Points = append(agent.Points, point)
		body, err := encodeVerdict(agent)
		if err != nil {
			return "", err
		}
		if err := postVerdict(ctx, client, body, tries); err != nil {
			return "", err
		}
		return fmt.Sprintf(textSuitePosted, runapi.VerdictEscalate), nil
	case agent.Verdict == runapi.VerdictEscalate:
		return textSuiteNoRoom, nil
	}
	return fmt.Sprintf(textSuiteStands, agent.Verdict), nil
}

// suitePoint is the point of the verdict for the red suite snap, whose time limit was minutes.
func suitePoint(snap suite.Snapshot, minutes int) string {
	commit := shortCommit(snap.Commit)
	switch {
	case snap.State == suite.Timeout:
		return fmt.Sprintf(textPointTimeout, commit, minutes)
	case snap.State == suite.Stopped:
		return fmt.Sprintf(textPointStopped, commit)
	case snap.Counts != nil && snap.Counts.Tests == 0:
		return fmt.Sprintf(textPointNoTests, commit)
	case snap.Counts != nil && snap.Counts.Failed > 0:
		return fmt.Sprintf(textPointFailedTests, commit, snap.Counts.Failed, snap.Counts.Tests)
	}
	return fmt.Sprintf(textPointFailed, commit)
}

// verdictDocument is a verdict as the run's version of it has it.
type verdictDocument struct {
	Verdict string          `json:"verdict"`
	Points  []runapi.Point  `json:"points,omitempty"`
	Track   json.RawMessage `json:"track,omitempty"`
}

// encodeVerdict is the body of POST verdict for d.
func encodeVerdict(d *verdictDocument) ([]byte, error) {
	return json.Marshal(d)
}

// agentVerdict is the newest verdict that the run wrote, nil when it wrote none. It is read
// from the artifact index and its file, since the work item's newest verdict can be an earlier
// run's.
func agentVerdict(ctx context.Context, client *runapi.Client, runID string) (*verdictDocument, error) {
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
	var d verdictDocument
	if err := json.Unmarshal(file.Bytes(), &d); err != nil {
		return nil, fmt.Errorf("the run's verdict is not a verdict document: %w", err)
	}
	return &d, nil
}

// postVerdict posts body as the run's verdict, up to tries times: a verdict sent twice is stored
// twice with the same content, so a request that got no response is sent again.
func postVerdict(ctx context.Context, client *runapi.Client, body []byte, tries int) error {
	var last error
	for try := 1; try <= tries; try++ {
		if try > 1 {
			if err := branchWait(ctx, retryAfter(last)); err != nil {
				return err
			}
		}
		one, cancel := context.WithTimeout(ctx, runAPITimeout)
		_, last = client.PushVerdictDocument(one, body)
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
