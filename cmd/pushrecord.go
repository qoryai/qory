package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
)

// The words of the report of a push to Qory Apiary. textNotRecorded has the commit and the
// branch, then the status and Qory Apiary's message or code, or the cause of a request with
// no response. No text has a retry step: qory has sent the call again, or the run is
// stopped.
const (
	textNotRecorded   = "Contact support: qory pushed %s to %s, and Qory Apiary did not record it.\n%s"
	textRecordStatus  = "Qory Apiary returned %d: %s"
	textRecordNoReply = "Qory Apiary sent no response: %s"
	textRunEndedLater = "qory run: Qory Apiary ended this run, so qory pushes nothing more and no later session starts."
)

// The bounds of the report of a push: recordAttempts is the first send and the three that
// follow, after the waits of recordWaits, and recordMaxWait the longest wait, whatever the
// value of Retry-After.
const (
	recordAttempts = 4
	recordMaxWait  = 30 * time.Second
)

var recordWaits = [recordAttempts - 1]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// recordAttemptLimit is the time one send of the report has; a test sets a shorter one.
var recordAttemptLimit = 30 * time.Second

// recordPushed reports what the last push of branch moved to Qory Apiary, with at most
// [runapi.MaxPushedCommits] repositories and [runapi.MaxPushedCommitsBytes] bytes in a call, one
// call after the other, in the order the push moved them. A push that moved nothing sends no
// call. It returns ended true when Qory Apiary returned run_ended: the run's end stands as Qory
// Apiary has it, and no call follows. Any other 4xx status, retries used up, and a context
// that ended during the report return the error that ends the run, see [recordFailed]. Qory
// Apiary takes a repeat of a commit as a no-op, so each call is sent again with the same body.
func recordPushed(ctx context.Context, client *runapi.Client, branch *preparation.WorkBranch) (ended bool, err error) {
	return recordLanded(ctx, client, branch.Ref, branch.Landed())
}

// recordLanded is [recordPushed] for the repositories landed, with ref as the branch the
// failure line names.
func recordLanded(ctx context.Context, client *runapi.Client, ref string, landed []preparation.Landed) (ended bool, err error) {
	for _, commits := range pushedCalls(landed) {
		switch err := sendPushed(ctx, client, commits); {
		case err == nil:
		case runapi.HasCode(err, runapi.CodeRunEnded):
			return true, nil
		default:
			return false, recordFailed(ref, commits[0].Commit, err)
		}
	}
	return false, nil
}

// pushedCalls is landed as the bodies of the calls that report it, in the order they landed,
// split as [runapi.SplitPushedCommits] has it.
func pushedCalls(landed []preparation.Landed) [][]runapi.PushedCommit {
	commits := make([]runapi.PushedCommit, len(landed))
	for i, l := range landed {
		commits[i] = runapi.PushedCommit{System: l.System, Path: l.Path, Commit: l.Commit}
	}
	return runapi.SplitPushedCommits(commits)
}

// sendPushed sends commits, again after a failure that leaves open whether Qory Apiary has
// them: a request with no response, a status of 500 or above, and 429. It waits as
// [recordWaits] has it, or [recordWait] for a response with Retry-After. It returns the last
// error, also when ctx ended.
func sendPushed(ctx context.Context, client *runapi.Client, commits []runapi.PushedCommit) error {
	var last error
	for attempt := range recordAttempts {
		if attempt > 0 {
			if err := branchWait(ctx, recordWait(recordWaits[attempt-1], last)); err != nil {
				return err
			}
		}
		last = sendPushedOnce(ctx, client, commits)
		if last == nil || ctx.Err() != nil || !retryPushed(last) {
			return last
		}
	}
	return last
}

// sendPushedOnce sends commits once, within [recordAttemptLimit].
func sendPushedOnce(ctx context.Context, client *runapi.Client, commits []runapi.PushedCommit) error {
	ctx, cancel := context.WithTimeout(ctx, recordAttemptLimit)
	defer cancel()
	return client.PushedCommits(ctx, commits)
}

// retryPushed reports whether err is one that a second send may not meet: no status came
// back, a status of 500 or above did, or the status is 429.
func retryPushed(err error) bool {
	var apiError *runapi.Error
	return !errors.As(err, &apiError) || apiError.Status >= http.StatusInternalServerError ||
		apiError.Status == http.StatusTooManyRequests
}

// recordWait is the wait before a send after err: the response's Retry-After when it has one,
// else normal, at most [recordMaxWait].
func recordWait(normal time.Duration, err error) time.Duration {
	var apiError *runapi.Error
	if errors.As(err, &apiError) && apiError.RetryAfter > 0 {
		normal = time.Duration(apiError.RetryAfter) * time.Second
	}
	return min(normal, recordMaxWait)
}

// recordFailed is the error that ends a run whose push Qory Apiary did not record: the
// first commit of the call that failed, the branch ref, then the status and Qory Apiary's
// message, or its code, or the cause of a request with no response. The text has no step to
// retry, because qory has sent the call again, or ctx ended. The commits stay on the forge.
func recordFailed(ref, commit string, err error) error {
	var apiError *runapi.Error
	var detail string
	switch {
	case errors.As(err, &apiError) && apiError.Status != 0:
		text := cmp.Or(apiError.Message, apiError.Code, http.StatusText(apiError.Status))
		detail = fmt.Sprintf(textRecordStatus, apiError.Status, shownError(errors.New(text)))
	case errors.Is(err, context.Canceled):
		detail = fmt.Sprintf(textRecordNoReply, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		detail = fmt.Sprintf(textRecordNoReply, context.DeadlineExceeded)
	default:
		detail = fmt.Sprintf(textRecordNoReply, shownError(err))
	}
	return fmt.Errorf(textNotRecorded, commit, ref, detail)
}
