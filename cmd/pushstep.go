package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// of what the agent left, with the run's id. textNoBranchType is the fix of the workflow
// model, word for word.
const (
	textChangesLeft     = "Changes left by run %s"
	textPushStep        = "push %s to %s"
	textPushStepOne     = "push %s to %s and 1 further repository"
	textPushStepMany    = "push %s to %s and %d further repositories"
	textPushNoHostProxy = "take the work item in again in Qory Apiary: the run's host proxy, through which qory pushes the run's commits, did not open: %s" + codeUsed
	textPushNoRunAPI    = "take the work item in again in Qory Apiary: the run API, which stores the version of the artifact " + branchType + ", is not reachable from this run: %s" + codeUsed
	textNoBranchType    = "Define the artifact type " + branchType + " in the workspace's Artifact types, then run the round again"
	textNotStored       = "take the work item in again in Qory Apiary: qory pushed %s, and the version of the artifact " + branchType + " was not stored: %s" + codeUsed
	textStoreUnknown    = "check the artifact " + branchType + " of the work item in Qory Apiary, then take the work item in again: qory pushed %s, and the request that stores its version got no answer, so the version may or may not be stored: %s" + codeUsed
	textPushInterrupted = "the run was cancelled: qory run got %s during the push step, and the run's commits may not have reached the branch"
	textPushStopped     = "the run was cancelled during the push step, and the run's commits may not have reached the branch"
	textPushTimedOut    = "take the work item in again in Qory Apiary: the run reached its time limit of %s during the push step, and the run's commits may not have reached the branch" + codeUsed
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

// keepsWork reports whether the run pushes what its agent left: a run from a run code
// that may write its target, that no workflow or document limits to reading, with a
// branch of its work item, whose session closes the run succeeded by out. A session that
// failed, timed out or was cancelled pushes nothing and stores no version, so no half
// done work reaches a gate or a later run. [readsOnly] sets readOnly, and
// [codeRun.writes] alone is true for a workflow that declares read in documents with
// access write.
func keepsWork(fromCode *codeRun, readOnly bool, branch *preparation.WorkBranch, out session.Outcome) bool {
	return canPush(fromCode, readOnly, branch) && out.State == event.StateSucceeded
}

// canPush reports whether the run has a push step if its session succeeds.
func canPush(fromCode *codeRun, readOnly bool, branch *preparation.WorkBranch) bool {
	return fromCode.writes() && !readOnly && branch != nil
}

// pushWork is the run's step of kind push: it pushes what the agent left, through the
// run's host proxy, and stores the new version of the artifact type branch. The step
// ends as [inStep] ends it.
func pushWork(ctx context.Context, run openedRun, stderr io.Writer, branch *preparation.WorkBranch, dir, runID string, stopped func() bool) error {
	return inStep(run, stderr, event.StepPush, pushStepName(branch, run.System()), stopped, func(w io.Writer) error {
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
		document, err := preparation.Push(ctx, dir, os.Environ(), branch, fmt.Sprintf(textChangesLeft, runID), h.proxy, w)
		if err != nil {
			return &runLineError{err}
		}
		if document == nil {
			return nil
		}
		if err := storeBranch(ctx, client, runID, document); err != nil {
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
// a POST whose answer was lost sends it again only when [branchStored] reads that no
// version of this run has the document. At most [maxBranchTries] POST go out, and
// unknown_type is [textNoBranchType].
func storeBranch(ctx context.Context, client *runapi.Client, runID string, document *branchformat.Branch) error {
	content, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf(textNotStored, document.Ref, shownError(err))
	}
	if err := branchformat.Check(content); err != nil {
		return fmt.Errorf(textNotStored, document.Ref, shownError(err))
	}
	sum := sha256.Sum256(content)
	push := runapi.Push{Type: branchType, Name: defaultName, Files: []runapi.PushFile{
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
			return errors.New(textNoBranchType)
		case runapi.HasCode(last, runapi.CodeRateLimited):
		case answerLost(last):
			stored, err := branchStored(ctx, client, runID, hex.EncodeToString(sum[:]))
			switch {
			case err != nil && ctx.Err() != nil:
				return err
			case err != nil:
				return fmt.Errorf(textStoreUnknown, document.Ref, shownError(err))
			case stored:
				return nil
			}
		default:
			return fmt.Errorf(textNotStored, document.Ref, shownError(last))
		}
	}
	return fmt.Errorf(textNotStored, document.Ref, shownError(last))
}

// postBranch sends push once, within the limit of one request.
func postBranch(ctx context.Context, client *runapi.Client, push runapi.Push) error {
	ctx, cancel := context.WithTimeout(ctx, runAPITimeout)
	defer cancel()
	_, err := client.PushArtifact(ctx, push)
	return err
}

// answerLost reports whether err leaves unknown whether the POST stored its version: no
// status of the run API came back, or a status of 500 or above did, as 502
// answer_unverified and 503 unavailable are.
func answerLost(err error) bool {
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
// 429 or a lost answer reads the index again, at most [maxBranchTries] times.
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
		if ctx.Err() != nil || !runapi.HasCode(err, runapi.CodeRateLimited) && !answerLost(err) {
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

// pushOutcome is the outcome a run closes with after its push step failed with err: the
// outcome of a session that ctx or the run's time limit stopped, or failed with the
// status of err. Forager closes a run failed after a succeeded session.
func pushOutcome(ctx context.Context, stopped func() bool, err error) session.Outcome {
	switch {
	case ctx.Err() != nil:
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}
	case stopped():
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: exitTimeout}
	}
	return session.Outcome{State: event.StateFailed, ExitCode: ExitCode(err)}
}

// pushEnded prints how a push step that failed with err ended the run, which closed with
// out, and returns qory run's exit status: 1, or [exitTimeout] at the run's time limit.
func pushEnded(u *ui.UI, said func(string), out session.Outcome, err error, got string, limit time.Duration) error {
	switch {
	case out.Reason == event.ReasonTimeout:
		u.Fail(fmt.Errorf(textPushTimedOut, limit))
		return reported(&exitError{code: exitTimeout})
	case out.State == event.StateCancelled && got != "":
		u.Fail(fmt.Errorf(textPushInterrupted, got))
		return reported(&exitError{code: 1})
	case out.State == event.StateCancelled:
		u.Fail(errors.New(textPushStopped))
		return reported(&exitError{code: 1})
	}
	said(strings.TrimPrefix(err.Error(), "qory run: "))
	return reported(err)
}
