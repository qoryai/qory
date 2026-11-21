package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/submission"
	"github.com/qoryai/qory/internal/ui"
)

// The words of the push step of a write run. textChangesLeft is the message of the commit
// of what the agent left, with the run's number. textNoSubmissionType has the fix of the workflow
// model, with the fix of [preparation.WorkBranch.DeleteBranch].
const (
	textChangesLeft      = "Changes left by run %d"
	textNoRunNumber      = "Check the run's gateway. The response to GET /work-item has no run number" + codeUsed
	textPushStep         = "push %s to %s"
	textPushStepOne      = "push %s to %s and 1 further repository"
	textPushStepMany     = "push %s to %s and %d further repositories"
	textPushNoProxy      = "take the work item in again in Qory Apiary: the run's push proxy, through which qory pushes the run's commits, did not open: %s" + codeUsed
	textPushNoCred       = "Contact support: this run has no push credential, so qory pushed nothing.\n  The run configuration has no credential with purpose push, so qory pushed nothing" + codeUsed
	textPushNoReadProxy  = "Take the work item in again in Qory Apiary: the run's host proxy did not open.\n  qory could not read the head of the branch, so it pushed nothing.\n  Cause: %s" + codeUsed
	textHandoffIgnored   = "qory run: The hand-off file is not usable, so the submission has no text and no steps.\n  File: %s, from " + handoffEnv + ".\n  Cause: %s"
	textHandoffDropped   = "qory run: The hand-off file has a part the submission format does not allow, so qory dropped it."
	textPushNoRunAPI     = "take the work item in again in Qory Apiary: the run API, which stores the version of the artifact " + submissionType + ", is not reachable from this run: %s" + codeUsed
	textNoSubmissionType = "Define the artifact type " + submissionType + " in the workspace's Artifact types, then %s, then run the round again"
	textNotStored        = "%s, then take the work item in again: qory pushed %s, and the version of the artifact " + submissionType + " was not stored: %s" + codeUsed
	textStoreUnknown     = "check the artifact " + submissionType + " of the work item in Qory Apiary; with no version of this run, %s; then take the work item in again: qory pushed %s, and the request that stores its version ended with no response, so the version may or may not be stored: %s" + codeUsed
	textSetupStays       = "take the work item in again in Qory Apiary: the setup container of this run was not removed, so a process in it could write the working folder, and qory pushed nothing: %s" + codeUsed
	textPushCut          = "take the work item in again in Qory Apiary: %s during the push step, and the run's commits may not have reached the branch" + codeUsed
	textPushCutPushed    = "check the artifact " + submissionType + " of the work item in Qory Apiary; with no version of this run, %s; then take the work item in again: qory pushed %s, and %s, so the version may or may not be stored" + codeUsed
	textCutSecondSignal  = "qory run got a second signal"
	textCutTimeLimit     = "the run reached its time limit of %s"
	textCutPushLimit     = "the step reached its limit of %s"
	textCutSignal        = "qory run got %s, and " + textCutPushLimit
	textRunEnded         = "The run ended %s: %s."
)

// submissionFile is the name of the one file of a version of the artifact type submission.
const submissionFile = "submission.json"

// The bounds of the requests that store a version of the artifact type submission:
// maxSubmissionTries is how many times the push step sends or reads, and maxSubmissionWait the
// longest wait between two tries, whatever the value of Retry-After.
const (
	maxSubmissionTries = 5
	maxSubmissionWait  = time.Minute
)

// submissionWait waits for d, or until ctx ends; a test replaces it.
var submissionWait = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The time the push step has after a stop. A signal leaves pushAfterSignal: it fits inside
// the 30 seconds that [child.DefaultGrace] sets for a pool's run after SIGTERM, with the 10 seconds
// of a runtime's default stop and the close. Past a run's time limit no process stops
// qory run, so the step has pushAfterLimit, and the run can pass its limit by that time.
const (
	pushAfterSignal = 15 * time.Second
	pushAfterLimit  = 120 * time.Second
)

// pushTimes is [pushAfterLimit] and [pushAfterSignal]; a test sets shorter ones.
var pushTimes = struct{ limit, signal time.Duration }{pushAfterLimit, pushAfterSignal}

// keepsWork reports whether the run pushes what its agent left: a run from a run code
// that may write and push its target, that no workflow or document limits to a lower
// level, with a branch of its work item and an agent session, however that session
// ended. A join that returned an error has no [session.Result], so nothing shows that
// an agent ran. A run that Qory Apiary closed during the session takes no version.
// level is [accessOf]: [codeRun.writes] alone is true for a workflow that declares read
// or write-no-push in documents with access write.
func keepsWork(
	fromCode *codeRun, level string, branch *preparation.WorkBranch, res *session.Result,
	joinErr error,
) bool {
	return canPush(fromCode, level, branch) && joinErr == nil && res != nil && !res.RunClosed
}

// pushContext is the context of the push step: not ended by ctx, which a stop ended
// already. It ends at limit, or afterLimit from now when that is later; with a zero limit
// there is no such end. It ends afterSignal after ctx ends or after this call, whichever is
// later, unless it ends sooner, and at once when again ends. A signal comes before this call when
// it stopped the agent, so the agent's stop adds to afterSignal. Run passes [pushAfterLimit] and [pushAfterSignal].
func pushContext(ctx, again context.Context, limit time.Time, afterLimit, afterSignal time.Duration) (context.Context, func()) {
	push, cancel := context.WithCancel(context.WithoutCancel(ctx))
	wait := time.Duration(math.MaxInt64)
	if !limit.IsZero() {
		wait = max(time.Until(limit), afterLimit)
	}
	started := time.Now()
	timer := time.AfterFunc(wait, cancel)
	stopFirst := context.AfterFunc(ctx, func() { timer.Reset(min(wait-time.Since(started), afterSignal)) })
	stopAgain := context.AfterFunc(again, cancel)
	return push, func() {
		stopFirst()
		stopAgain()
		timer.Stop()
		cancel()
	}
}

// failedEnd is the line that shows how a run that did not succeed ended, in the commit of
// what its agent left and in the note of its version; "" for out succeeded.
func failedEnd(out session.Outcome) string {
	if out.State == event.StateSucceeded {
		return ""
	}
	reason := out.Reason
	if reason == "" {
		reason = fmt.Sprintf("exit %d", out.ExitCode)
	}
	return fmt.Sprintf(textRunEnded, out.State, reason)
}

// canPush reports whether the run has a push step if its session succeeds: the
// documents' access is write, and level, the run's [accessOf] level, is write too, so a
// workflow that declares read or write-no-push gives a run with no push step.
func canPush(fromCode *codeRun, level string, branch *preparation.WorkBranch) bool {
	return fromCode.writes() && level == stack.AccessWrite && branch != nil
}

// returnContext is the context of the return of a local run's work: a stop has ended ctx, so
// it is not ended by ctx. It ends at a second signal, again, and [returnAfterSignal] after ctx
// ends, so a first signal leaves the return that time, and the git session and each check have
// their limits within it. started is what prints the line of the wait, when the first signal
// comes; it does not print for a return that ended first.
func returnContext(ctx, again context.Context, started func(time.Duration)) (context.Context, func()) {
	back, stop := pushContext(ctx, again, time.Time{}, returnAfterSignal, returnAfterSignal)
	printed := make(chan struct{})
	stopLine := context.AfterFunc(ctx, func() {
		defer close(printed)
		started(returnAfterSignal)
	})
	return back, func() {
		// A line that started is written before the run's last lines.
		if !stopLine() {
			<-printed
		}
		stop()
	}
}

// returnAfterSignal is how long the return of a local run's work has after a first signal: the
// removal of the setup container, the git session, and the checks of its bundles. A test sets a
// shorter one.
var returnAfterSignal = 30 * time.Minute

// sessionPush is what the push step after a session of a write run needs, the same for
// each session of the run: see [sessionPush.after].
type sessionPush struct {
	// ctx is qory run's context, which a first signal ends, and again the context a second
	// signal ends. limit is when the run's time limit ends, the zero time for none.
	ctx, again context.Context
	limit      time.Time
	// stopNow reads what stopped the run, for [pushEnding.settle].
	stopNow func() stopRead
	run     openedRun
	stderr  io.Writer
	branch  *preparation.WorkBranch
	// dir is the working folder, runID the run's id and runNumber its number in the work
	// item, as [pushWork] takes them.
	dir, runID string
	runNumber  int
	// agentStopped is whether the stop of the session's agent is proven, see
	// [preparation.Push]: true for a walled run.
	agentStopped bool
	checks       *runPushChecks
	// handoff is the checkout that has the session's hand-off file, "" for none.
	handoff string
	// ended is the line that says the report of pushed commits found the run over, Qory
	// Apiary's run_ended or the gateway's 410, see [runEndedLine]: no later push and no later
	// session of the run goes on. It is "" while the run goes on. A loop over the run's
	// sessions reads it after each [sessionPush.after].
	ended string
}

// after is the push step after a session that ended with out: [removeThenPush] under
// [pushContext], so the step has the run's time limit, and at least [pushTimes] after the
// limit or a signal. The step commits what the agent left, pushes the commits that the
// session made past the branch's tip, and stores a new version; with no new commit, it
// pushes and stores nothing. A later call of the same run pushes from the tip that the
// earlier call left, see [preparation.Push]. after sets ending's state of the step, and
// returns the outcome the run closes with: out, or [pushEnding.outcome] of the step's
// error, with that error.
func (p *sessionPush) after(ending *pushEnding, out session.Outcome) (session.Outcome, error) {
	if p.ended != "" {
		return out, nil
	}
	ctx, cancel := pushContext(p.ctx, p.again, p.limit, pushTimes.limit, pushTimes.signal)
	defer cancel()
	var err error
	ending.noStep, err = removeThenPush(ctx, p.run, p.stderr, p.branch, p.dir, p.runID, p.runNumber, out, p.agentStopped, p.checks, &p.ended, p.handoff)
	ending.settle(p.stopNow())
	ending.cut = ctx.Err() != nil
	if p.ended != "" {
		// A push error alongside it stays the step's. Without one, the run closes with
		// the end that the gateway recorded: out, as for a run that Qory Apiary closed
		// during its session. No new exit status is added.
		fmt.Fprintln(p.stderr, p.ended)
		fmt.Fprintln(p.stderr, textLastPushUnrecorded)
	}
	if err != nil {
		return ending.outcome(err), err
	}
	return out, nil
}

// pushAfterSession is [sessionPush.after], which qory run calls once, after its session.
// A test replaces it to stand in for further sessions of the run around the step.
var pushAfterSession = func(p *sessionPush, ending *pushEnding, out session.Outcome) (session.Outcome, error) {
	return p.after(ending, out)
}

// removeThenPush removes the setup container of a run that gets the push step, then runs
// [pushWork], with ctx as the bound of both. A process in that container can write the
// working folder, so the check of the step is valid only once the container is gone. After a
// failed removal [openedRun.PushProxy] fails and [openedRun.StartStep] still works, so this
// check alone stops the push, and the run has no step.
func removeThenPush(ctx context.Context, run openedRun, stderr io.Writer, branch *preparation.WorkBranch, dir, runID string, runNumber int, out session.Outcome, agentStopped bool, checks *runPushChecks, ended *string, handoffRoot string) (noStep bool, err error) {
	if err := run.RemoveSetup(ctx); err != nil {
		return true, fmt.Errorf(textSetupStays, shownError(err))
	}
	return false, pushWork(ctx, run, stderr, branch, dir, runID, runNumber, out, agentStopped, checks, ended, handoffRoot)
}

// pushWork is the run's step of kind push: it pushes what the agent left, through the
// run's push proxy, which it starts at the push, after [openedRun.RemoveSetup], and closes
// when the push returns, and stores the new version of the artifact type submission. The step
// ends as [inStep] ends it, cancelled when ctx ended. out is how the session ended, which
// the commit and the version show when it did not succeed, see [failedEnd]. agentStopped
// is whether the agent's stop is proven, see [preparation.Push]. runNumber is the number
// in the commit's message, and runID the run that the version's check reads. checks
// checks the version before it is stored, see [checkSubmissionVersion]. After the push, the
// step reports what the push moved, see [recordPushed], before it stores the version, and
// that also when a later repository's push failed: the repositories that landed are
// reported, and the push's error stays the step's. *ended is set to the line of
// [runEndedLine] when the report found the run over, Qory Apiary's run_ended or the gateway's
// 410, also when the push failed. The step then stores no version, and ends with no error
// unless the push failed.
func pushWork(ctx context.Context, run openedRun, stderr io.Writer, branch *preparation.WorkBranch, dir, runID string, runNumber int, out session.Outcome, agentStopped bool, checks *runPushChecks, ended *string, handoffRoot string) error {
	cut := func() bool { return ctx.Err() != nil }
	return inStep(run, stderr, event.StepPush, pushStepName(branch, run.System()), cut, func(w io.Writer) error {
		httpClient, base, err := run.RunAPI()
		if err != nil {
			return &runLineError{fmt.Errorf(textPushNoRunAPI, shownError(err))}
		}
		client, err := runapi.New(base, httpClient)
		if err != nil {
			return &runLineError{fmt.Errorf(textPushNoRunAPI, shownError(err))}
		}
		h, err := run.PushProxy()
		if err != nil {
			return &runLineError{pushProxyError(err)}
		}
		secretmask.Default().AddURLPassword(h.proxy.URL)
		defer h.close()
		// The head of the branch is read through the host proxy, with the read credential: the
		// push proxy has the push credential alone, and git's ls-remote and fetch are
		// requests that its push list does not have.
		read, err := run.HostProxy()
		if err != nil {
			return &runLineError{fmt.Errorf(textPushNoReadProxy, shownError(err))}
		}
		secretmask.Default().AddURLPassword(read.proxy.URL)
		defer read.close()
		end := failedEnd(out)
		message := fmt.Sprintf(textChangesLeft, runNumber)
		if end != "" {
			message += "\n\n" + end
		}
		repositories, pushErr := preparation.Push(ctx, dir, os.Environ(), branch, message, read.proxy, h.proxy, agentStopped, w)
		// The read proxy and the push proxy end with the push: the check and the store that
		// follow reach no forge.
		read.close()
		h.close()
		runEnded, recordErr := recordPushed(ctx, run, branch)
		*ended = runEnded
		switch {
		case pushErr != nil && recordErr != nil:
			return &runLineError{errors.Join(pushErr, recordErr)}
		case pushErr != nil:
			return &runLineError{pushErr}
		case recordErr != nil:
			return &runLineError{recordErr}
		case runEnded != "":
			return nil
		case repositories == nil:
			return nil
		}
		// Every pushing session stores a version, with the verdict none when it wrote none.
		// shortcut: a version is stored after every pushing session, replace it when Q-35b moves
		// the call to the end of the round.
		verdict, err := newestVerdict(ctx, client, runID)
		if err != nil {
			return &runLineError{fmt.Errorf(textNotStored, branch.DeleteBranch(), branch.Ref, shownError(err))}
		}
		given := readHandoffPrinting(w, handoffRoot)
		document := assembleSubmission(branch, repositories, verdict, given)
		check := func(content []byte) error {
			return checkSubmissionVersion(ctx, checks, branch, document.Ref, content)
		}
		if err := storeSubmission(ctx, client, runID, end, branch, document, check); err != nil {
			return &runLineError{err}
		}
		return nil
	})
}

// noPushProxyWords is the part of Forager's error from [session.Run.PushProxy] for a run
// with no push credential: "Add a push credential to the run configuration: this run has
// no push proxy." Forager has no error value for it. qory prints [textPushNoCred] instead.
const noPushProxyWords = "this run has no push proxy"

// noPushCredential is [textPushNoCred] as an error that unwraps to Forager's error.
type noPushCredential struct{ err error }

func (e *noPushCredential) Error() string { return textPushNoCred }

func (e *noPushCredential) Unwrap() error { return e.err }

// pushProxyError is the push step's error for err, the error of PushProxy: a
// [*noPushCredential] for a run with no push credential, [textPushNoProxy] with Forager's
// error otherwise.
func pushProxyError(err error) error {
	if strings.Contains(err.Error(), noPushProxyWords) {
		return &noPushCredential{err}
	}
	return fmt.Errorf(textPushNoProxy, shownError(err))
}

// shownError is err as a line shows it: printable, with every URL's password and every
// secret hidden.
func shownError(err error) string {
	return integration.Printable(exports.HideURLError(err).Error())
}

// pushStepName is the name of the push step of branch on system: the branch, the first
// repository as <system>/<path>, and how many further repositories it has.
func pushStepName(branch *preparation.WorkBranch, system string) string {
	first := integration.Printable(exports.Value(system+"/"+branch.Repositories[0].Path, false))
	switch n := len(branch.Repositories) - 1; n {
	case 0:
		return fmt.Sprintf(textPushStep, branch.Ref, first)
	case 1:
		return fmt.Sprintf(textPushStepOne, branch.Ref, first)
	default:
		return fmt.Sprintf(textPushStepMany, branch.Ref, first, n)
	}
}

// storeSubmission stores document as a new version of the artifact main of the type submission.
// The gateway never retries a POST and Qory Apiary does not detect one sent twice, so:
// a 429, which neither forwarded nor stored the POST, sends it again after Retry-After;
// a POST with no response sends it again only when [submissionStored] reads that no
// version of this run has the document. At most [maxSubmissionTries] POST go out, and
// unknown_type is [textNoSubmissionType]. Every failure has the fix of
// [preparation.WorkBranch.DeleteBranch], as the push landed. check gets the version's
// file before the first POST, and its error sends none.
func storeSubmission(ctx context.Context, client *runapi.Client, runID, note string, branch *preparation.WorkBranch, document *submission.Submission, check func(content []byte) error) error {
	content, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf(textNotStored, branch.DeleteBranch(), document.Ref, shownError(err))
	}
	if err := submission.Check(content); err != nil {
		return fmt.Errorf(textNotStored, branch.DeleteBranch(), document.Ref, shownError(err))
	}
	if err := check(content); err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	push := runapi.Push{Type: submissionType, Name: defaultName, Note: note, Files: []runapi.PushFile{
		{Filename: submissionFile, ContentType: "application/json", Content: content},
	}}
	var last error
	for try := 1; try <= maxSubmissionTries; try++ {
		if try > 1 {
			if err := submissionWait(ctx, retryAfter(last)); err != nil {
				return err
			}
		}
		last = postSubmission(ctx, client, push)
		switch {
		case last == nil:
			return nil
		case ctx.Err() != nil:
			return last
		case runapi.HasCode(last, runapi.CodeUnknownType):
			return fmt.Errorf(textNoSubmissionType, branch.DeleteBranch())
		case runapi.HasCode(last, runapi.CodeRateLimited):
		case noResponse(last):
			stored, err := submissionStored(ctx, client, runID, hex.EncodeToString(sum[:]))
			switch {
			case err != nil && ctx.Err() != nil:
				return err
			case err != nil:
				return fmt.Errorf(textStoreUnknown, branch.DeleteBranch(), document.Ref, shownError(err))
			case stored:
				return nil
			}
		default:
			return fmt.Errorf(textNotStored, branch.DeleteBranch(), document.Ref, shownError(last))
		}
	}
	return fmt.Errorf(textNotStored, branch.DeleteBranch(), document.Ref, shownError(last))
}

// postSubmission sends push once, within the limit of one request.
func postSubmission(ctx context.Context, client *runapi.Client, push runapi.Push) error {
	ctx, cancel := context.WithTimeout(ctx, runAPITimeout)
	defer cancel()
	_, err := client.PushArtifact(ctx, push)
	return err
}

// noResponse reports whether err leaves unknown whether the POST stored its version: no
// status of the run API came back, or a status of 500 or above did, as 502
// answer_unverified and 503 unavailable are.
func noResponse(err error) bool {
	var apiError *runapi.Error
	return !errors.As(err, &apiError) || apiError.Status >= http.StatusInternalServerError
}

// retryAfter is the wait after err: its Retry-After, 1 second with none, at most
// [maxSubmissionWait].
func retryAfter(err error) time.Duration {
	wait := time.Second
	var apiError *runapi.Error
	if errors.As(err, &apiError) && apiError.RetryAfter > 0 {
		wait = time.Duration(apiError.RetryAfter) * time.Second
	}
	return min(wait, maxSubmissionWait)
}

// submissionStored reports whether the index of the work item has a version of the artifact
// main of the type submission that the run runID wrote, with one file of the SHA-256 sum. A
// 429 or a request with no response reads the index again, at most [maxSubmissionTries] times.
func submissionStored(ctx context.Context, client *runapi.Client, runID, sum string) (bool, error) {
	var last error
	for try := 1; try <= maxSubmissionTries; try++ {
		if try > 1 {
			if err := submissionWait(ctx, retryAfter(last)); err != nil {
				return false, err
			}
		}
		read, cancel := context.WithTimeout(ctx, runAPITimeout)
		index, err := client.Artifacts(read)
		cancel()
		if err == nil {
			return hasSubmissionVersion(index, runID, sum), nil
		}
		last = err
		if ctx.Err() != nil || !runapi.HasCode(err, runapi.CodeRateLimited) && !noResponse(err) {
			return false, err
		}
	}
	return false, last
}

// hasSubmissionVersion reports whether index has the version [submissionStored] looks for.
func hasSubmissionVersion(index *runapi.Index, runID, sum string) bool {
	for _, artifact := range index.Artifacts {
		if artifact.Type != submissionType || artifact.Name != defaultName {
			continue
		}
		for _, version := range artifact.Versions {
			if version.Writer.Kind == "run" && version.Writer.RunID == runID &&
				len(version.Files) == 1 && strings.EqualFold(version.Files[0].SHA256, sum) {
				return true
			}
		}
	}
	return false
}

// pushEnding is what the end of a run needs to know of its push step.
type pushEnding struct {
	// session is the outcome of the session, and limit the run's time limit.
	session session.Outcome
	limit   time.Duration
	// stopped is whether qory run got a signal, again whether it got a second, and got the
	// first signal: as [pushEnding.settle] sets them. cut is whether the step's context ended
	// as the step exited, or [runCopy.cut] on a run with a copy. noStep is whether the removal
	// of the setup container failed, so no push step ran.
	stopped, again, cut, noStep bool
	got                         string
	// afterSignal is the time a first signal leaves the step, 0 for [pushTimes].signal.
	afterSignal time.Duration
	// lineRead is what the last [pushEnding.cutLine] read, nil when none ran.
	lineRead *stopRead
}

// stopRead is one read of what stopped a run: whether qory run got a signal, whether it got a
// second, and the first signal.
type stopRead struct {
	stopped, again bool
	got            string
}

// cutLine is [pushEnding.cutBy] for a line, with now as what stopped the run and afterSignal as
// the time a first signal leaves the step. [pushEnding.settle] uses now for the close, so a
// signal after this line does not change the close.
func (e *pushEnding) cutLine(now stopRead, afterSignal time.Duration) string {
	e.lineRead = &now
	line := *e
	line.stopped, line.again, line.got, line.afterSignal = now.stopped, now.again, now.got, afterSignal
	return line.cutBy()
}

// settle sets what stopped the run, for the close: what the last [pushEnding.cutLine] read, or
// now when none ran.
func (e *pushEnding) settle(now stopRead) {
	if e.lineRead != nil {
		now = *e.lineRead
	}
	e.stopped, e.again, e.got = now.stopped, now.again, now.got
}

// outcome is the outcome a run closes with after its push step failed with err. A refusal
// because the branch moved ([*preparation.HeadChanged]) closes the run failed with the end code
// push_head_changed after a session that succeeded, failed or timed out, since the next run of
// the work item needs that code; after a session that failed or timed out, the close keeps the
// session's exit code. The run's time limit that cuts the step after the refusal does not change
// that close. A stop that landed after the refusal still wins, and a session that was
// interrupted, cancelled or paused keeps its end. Any other failure after a session that did not
// succeed keeps the session's end, whatever the step did. After a session that succeeded, a cut
// step with no refusal closes the run as a stop does, and a failed one closes it failed with the status of err.
func (e pushEnding) outcome(err error) session.Outcome {
	changed := errors.As(err, new(*preparation.HeadChanged))
	switch {
	case e.session.State != event.StateSucceeded:
		failedOrTimedOut := e.session.State == event.StateFailed ||
			e.session.State == event.StateCancelled && e.session.Reason == event.ReasonTimeout
		if changed && failedOrTimedOut && !(e.cut && e.stopped) {
			return session.Outcome{State: event.StateFailed, ExitCode: e.session.ExitCode, Reason: string(contracts.EndCodePushHeadChanged)}
		}
		return e.session
	case e.cut && e.stopped:
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}
	case e.cut && !changed:
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: exitTimeout}
	}
	out := session.Outcome{State: event.StateFailed, ExitCode: ExitCode(err)}
	if changed {
		out.Reason = string(contracts.EndCodePushHeadChanged)
	}
	return out
}

// copyOutcome is the outcome a run closes with when its copy step did not succeed or did not
// open, code being [runCopy.stepCode]: as [pushEnding.outcome], with code as the exit code of a
// run that closes failed.
func (e pushEnding) copyOutcome(code int) session.Outcome {
	return e.outcome(&exitError{code: code})
}

// cutBy is what ended a step whose context ended, as a line has it.
func (e pushEnding) cutBy() string {
	switch {
	case e.again:
		return textCutSecondSignal
	case e.stopped:
		if e.afterSignal > 0 {
			return fmt.Sprintf(textCutSignal, e.got, e.afterSignal)
		}
		return fmt.Sprintf(textCutSignal, e.got, pushTimes.signal)
	case e.session.Reason == event.ReasonTimeout:
		return fmt.Sprintf(textCutPushLimit, pushTimes.limit)
	}
	return fmt.Sprintf(textCutTimeLimit, e.limit)
}

// ended prints how a push step that failed with err ended, for a run on branch, and
// returns qory run's exit status: ended, the session's, when the session did not succeed,
// [exitTimeout] for a step cut at the run's time limit, and 1 otherwise. Each cut line
// has the fix for a push that landed with no version stored. A step that a refusal because the
// branch moved ended prints the refusal, not a cut line.
func (e pushEnding) ended(u *ui.UI, said func(string), err error, branch *preparation.WorkBranch, ended error) error {
	out := e.outcome(err)
	// A run that closes push_head_changed prints the refusal, also when the run's time limit cut the
	// step after it: the cut line would say that the commits may have reached the branch.
	if e.cut && !e.noStep && out.Reason != string(contracts.EndCodePushHeadChanged) {
		if pushed := branch.PushedPaths(); pushed != "" {
			u.Fail(fmt.Errorf(textPushCutPushed, branch.DeleteBranch(), pushed, e.cutBy()))
		} else {
			u.Fail(fmt.Errorf(textPushCut, e.cutBy()))
		}
	} else {
		said(strings.TrimPrefix(err.Error(), "qory run: "))
	}
	switch {
	case e.session.State != event.StateSucceeded:
		return ended
	case out.Reason == event.ReasonTimeout:
		return reported(&exitError{code: exitTimeout})
	case e.cut:
		return reported(&exitError{code: 1})
	}
	return reported(err)
}
