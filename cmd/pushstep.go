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

	"github.com/qoryai/qory/internal/branchformat"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/ui"
)

// The words of the push step of a write run. textChangesLeft is the message of the commit
// of what the agent left, with the run's id. textNoBranchType has the fix of the workflow
// model, with the two ways of [preparation.WorkBranch.VersionWays].
const (
	textChangesLeft     = "Changes left by run %s"
	textPushStep        = "push %s to %s"
	textPushStepOne     = "push %s to %s and 1 further repository"
	textPushStepMany    = "push %s to %s and %d further repositories"
	textPushNoHostProxy = "take the work item in again in Qory Apiary: the run's host proxy, through which qory pushes the run's commits, did not open: %s" + codeUsed
	textPushNoRunAPI    = "take the work item in again in Qory Apiary: the run API, which stores the version of the artifact " + branchType + ", is not reachable from this run: %s" + codeUsed
	textNoBranchType    = "Define the artifact type " + branchType + " in the workspace's Artifact types, then %s, then run the round again"
	textNotStored       = "%s, then take the work item in again: qory pushed %s, and the version of the artifact " + branchType + " was not stored: %s" + codeUsed
	textStoreUnknown    = "check the artifact " + branchType + " of the work item in Qory Apiary; with no version of this run, %s; then take the work item in again: qory pushed %s, and the request that stores its version ended with no response, so the version may or may not be stored: %s" + codeUsed
	textPushCut         = "take the work item in again in Qory Apiary: %s during the push step, and the run's commits may not have reached the branch" + codeUsed
	textPushCutPushed   = "check the artifact " + branchType + " of the work item in Qory Apiary; with no version of this run, %s; then take the work item in again: qory pushed %s, and %s, so the version may or may not be stored" + codeUsed
	textCutSecondSignal = "qory run got a second signal"
	textCutTimeLimit    = "the run reached its time limit of %s"
	textCutPushLimit    = "the step reached its limit of %s"
	textCutSignal       = "qory run got %s, and " + textCutPushLimit
	textRunEnded        = "The run ended %s: %s."
)

// branchFile is the name of the one file of a version of the artifact type branch.
const branchFile = "branch.json"

// The bounds of the requests that store a version of the artifact type branch:
// maxBranchTries is how many times the push step sends or reads, and maxBranchWait the
// longest wait between two tries, whatever the value of Retry-After.
const (
	maxBranchTries = 5
	maxBranchWait  = time.Minute
)

// branchWait waits for d, or until ctx ends; a test replaces it.
var branchWait = func(ctx context.Context, d time.Duration) error {
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
// the 30 seconds [child.DefaultGrace] gives a pool's run after SIGTERM, with the 10 seconds
// of a runtime's default stop and the close. Past a run's time limit no process stops
// qory run, so the step has pushAfterLimit, and the run can pass its limit by that time.
const (
	pushAfterSignal = 15 * time.Second
	pushAfterLimit  = 120 * time.Second
)

// pushTimes is [pushAfterLimit] and [pushAfterSignal]; a test sets shorter ones.
var pushTimes = struct{ limit, signal time.Duration }{pushAfterLimit, pushAfterSignal}

// keepsWork reports whether the run pushes what its agent left: a run from a run code
// that may write its target, that no workflow or document limits to reading, with a
// branch of its work item and an agent session, however that session ended. A join that
// returned an error has no [session.Result], so nothing shows that an agent ran. A run
// that Qory Apiary closed during the session takes no version. [readsOnly] sets readOnly,
// and [codeRun.writes] alone is true for a workflow that declares read in documents with
// access write.
func keepsWork(fromCode *codeRun, readOnly bool, branch *preparation.WorkBranch, res *session.Result, joinErr error) bool {
	return canPush(fromCode, readOnly, branch) && joinErr == nil && res != nil && !res.RunClosed
}

// pushContext is the context of the push step: not ended by ctx, which a stop ended
// already. It ends at limit, or afterLimit from now when that is later; with a zero limit
// there is no such end. It ends afterSignal after ctx ends, unless it ends sooner, and at
// once when again ends. Run passes [pushAfterLimit] and [pushAfterSignal].
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

// canPush reports whether the run has a push step if its session succeeds.
func canPush(fromCode *codeRun, readOnly bool, branch *preparation.WorkBranch) bool {
	return fromCode.writes() && !readOnly && branch != nil
}

// pushWork is the run's step of kind push: it pushes what the agent left, through the
// run's host proxy, and stores the new version of the artifact type branch. The step
// ends as [inStep] ends it, cancelled when ctx ended. out is how the session ended, which
// the commit and the version show when it did not succeed, see [failedEnd]. agentStopped
// is whether the agent's stop is proven, see [preparation.Push].
func pushWork(ctx context.Context, run openedRun, stderr io.Writer, branch *preparation.WorkBranch, dir, runID string, out session.Outcome, agentStopped bool) error {
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
		h, err := run.HostProxy()
		if err != nil {
			return &runLineError{fmt.Errorf(textPushNoHostProxy, shownError(err))}
		}
		secretmask.Default().AddURLPassword(h.proxy.URL)
		defer h.close()
		end := failedEnd(out)
		message := fmt.Sprintf(textChangesLeft, runID)
		if end != "" {
			message += "\n\n" + end
		}
		document, err := preparation.Push(ctx, dir, os.Environ(), branch, message, h.proxy, agentStopped, w)
		if err != nil {
			return &runLineError{err}
		}
		if document == nil {
			return nil
		}
		if err := storeBranch(ctx, client, runID, end, branch, document); err != nil {
			return &runLineError{err}
		}
		return nil
	})
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

// storeBranch stores document as a new version of the artifact main of the type branch.
// The gateway never retries a POST and Qory Apiary does not detect one sent twice, so:
// a 429, which neither forwarded nor stored the POST, sends it again after Retry-After;
// a POST with no response sends it again only when [branchStored] reads that no
// version of this run has the document. At most [maxBranchTries] POST go out, and
// unknown_type is [textNoBranchType]. Every failure has the fix of
// [preparation.WorkBranch.VersionWays], as the push landed.
func storeBranch(ctx context.Context, client *runapi.Client, runID, note string, branch *preparation.WorkBranch, document *branchformat.Branch) error {
	content, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf(textNotStored, branch.VersionWays(), document.Ref, shownError(err))
	}
	if err := branchformat.Check(content); err != nil {
		return fmt.Errorf(textNotStored, branch.VersionWays(), document.Ref, shownError(err))
	}
	sum := sha256.Sum256(content)
	push := runapi.Push{Type: branchType, Name: defaultName, Note: note, Files: []runapi.PushFile{
		{Filename: branchFile, ContentType: "application/json", Content: content},
	}}
	var last error
	for try := 1; try <= maxBranchTries; try++ {
		if try > 1 {
			if err := branchWait(ctx, retryAfter(last)); err != nil {
				return err
			}
		}
		last = postBranch(ctx, client, push)
		switch {
		case last == nil:
			return nil
		case ctx.Err() != nil:
			return last
		case runapi.HasCode(last, runapi.CodeUnknownType):
			return fmt.Errorf(textNoBranchType, branch.VersionWays())
		case runapi.HasCode(last, runapi.CodeRateLimited):
		case noResponse(last):
			stored, err := branchStored(ctx, client, runID, hex.EncodeToString(sum[:]))
			switch {
			case err != nil && ctx.Err() != nil:
				return err
			case err != nil:
				return fmt.Errorf(textStoreUnknown, branch.VersionWays(), document.Ref, shownError(err))
			case stored:
				return nil
			}
		default:
			return fmt.Errorf(textNotStored, branch.VersionWays(), document.Ref, shownError(last))
		}
	}
	return fmt.Errorf(textNotStored, branch.VersionWays(), document.Ref, shownError(last))
}

// postBranch sends push once, within the limit of one request.
func postBranch(ctx context.Context, client *runapi.Client, push runapi.Push) error {
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
// [maxBranchWait].
func retryAfter(err error) time.Duration {
	wait := time.Second
	var apiError *runapi.Error
	if errors.As(err, &apiError) && apiError.RetryAfter > 0 {
		wait = time.Duration(apiError.RetryAfter) * time.Second
	}
	return min(wait, maxBranchWait)
}

// branchStored reports whether the index of the work item has a version of the artifact
// main of the type branch that the run runID wrote, with one file of the SHA-256 sum. A
// 429 or a request with no response reads the index again, at most [maxBranchTries] times.
func branchStored(ctx context.Context, client *runapi.Client, runID, sum string) (bool, error) {
	var last error
	for try := 1; try <= maxBranchTries; try++ {
		if try > 1 {
			if err := branchWait(ctx, retryAfter(last)); err != nil {
				return false, err
			}
		}
		read, cancel := context.WithTimeout(ctx, runAPITimeout)
		index, err := client.Artifacts(read)
		cancel()
		if err == nil {
			return hasBranchVersion(index, runID, sum), nil
		}
		last = err
		if ctx.Err() != nil || !runapi.HasCode(err, runapi.CodeRateLimited) && !noResponse(err) {
			return false, err
		}
	}
	return false, last
}

// hasBranchVersion reports whether index has the version [branchStored] looks for.
func hasBranchVersion(index *runapi.Index, runID, sum string) bool {
	for _, artifact := range index.Artifacts {
		if artifact.Type != branchType || artifact.Name != defaultName {
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
	// stopped is whether qory run got a signal, again whether it got a second, and cut
	// whether the step's context ended, each as the step exited. got is the first signal.
	stopped, again, cut bool
	got                 string
}

// outcome is the outcome a run closes with after its push step failed with err. A session
// that did not succeed keeps its end, whatever the step did. After a session that
// succeeded, a cut step closes the run as a stop does, and a failed one closes it failed
// with the status of err.
func (e pushEnding) outcome(err error) session.Outcome {
	switch {
	case e.session.State != event.StateSucceeded:
		return e.session
	case e.cut && e.stopped:
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}
	case e.cut:
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: exitTimeout}
	}
	return session.Outcome{State: event.StateFailed, ExitCode: ExitCode(err)}
}

// cutBy is what ended a step whose context ended, as a line has it.
func (e pushEnding) cutBy() string {
	switch {
	case e.again:
		return textCutSecondSignal
	case e.stopped:
		return fmt.Sprintf(textCutSignal, e.got, pushTimes.signal)
	case e.session.Reason == event.ReasonTimeout:
		return fmt.Sprintf(textCutPushLimit, pushTimes.limit)
	}
	return fmt.Sprintf(textCutTimeLimit, e.limit)
}

// ended prints how a push step that failed with err ended, for a run on branch, and
// returns qory run's exit status: ended, the session's, when the session did not succeed,
// [exitTimeout] for a step cut at the run's time limit, and 1 otherwise. Each cut line
// has the two ways out when a push landed with no version stored.
func (e pushEnding) ended(u *ui.UI, said func(string), err error, branch *preparation.WorkBranch, ended error) error {
	if e.cut {
		if pushed := branch.PushedPaths(); pushed != "" {
			u.Fail(fmt.Errorf(textPushCutPushed, branch.VersionWays(), pushed, e.cutBy()))
		} else {
			u.Fail(fmt.Errorf(textPushCut, e.cutBy()))
		}
	} else {
		said(strings.TrimPrefix(err.Error(), "qory run: "))
	}
	out := e.outcome(err)
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
