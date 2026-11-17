package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/server"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
)

// The words of the report of a push to Qory Apiary. textNotRecorded starts with the prefix
// "qory run: ", which [pushEnding.ended] trims before it prints the line with the prefix once,
// and which stays when the text follows another error. It has the commit and the
// branch, then the status and the message or code of Qory Apiary or of the gateway, whichever
// returned it, or the cause of a request with no response. No text has a retry step: qory has
// sent the call again, or the run is stopped. textRunEndedLater is the line for a run that
// Qory Apiary ended, textRunEndedAtGateway the line for any other end, with the gateway's
// code, one of [server.EndCodes]. textLastPushUnrecorded follows either line: the push that
// the report was about is on the forge, and Qory Apiary has no record of it.
const (
	textNotRecorded       = "qory run: Contact support: Qory Apiary did not record the last push.\n  qory pushed %s to %s.\n%s"
	textRecordApiary      = "Qory Apiary returned %d: %s"
	textRecordGateway     = "The gateway returned %d: %s"
	textRecordNoReply     = "Qory Apiary sent no response: %s"
	textRunEndedLater     = "qory run: Qory Apiary ended this run, so qory pushes nothing more and no later session starts."
	textRunEndedAtGateway = "qory run: The run ended (%s), so qory pushes nothing more and no later session starts."
	// textLastPushUnrecorded is the second line of the run's end, with the same prefix.
	textLastPushUnrecorded = "qory run: The last push is on the branch, and Qory Apiary did not record it."
)

// The bounds of the report of a push: recordAttempts is the first send and the three that
// follow, after the waits of recordWaits. The link has no Retry-After, so the waits are the
// same for every status.
const recordAttempts = 4

var recordWaits = [recordAttempts - 1]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// recordAttemptLimit is the time one send of the report has. The gateway waits 60 s for each
// call to Qory Apiary, not counting a reopen of the run, so 65 s lets the gateway's response
// arrive before qory cuts the send. A test sets a shorter one.
var recordAttemptLimit = 65 * time.Second

// pushedSender sends the body of one report over the link of the run's gateway, and returns
// the response's body, empty for Qory Apiary's 204: [session.Run.PushedCommits].
type pushedSender func(ctx context.Context, body []byte) ([]byte, error)

// recordPushed reports what the last push of branch moved to Qory Apiary, with at most
// [runapi.MaxPushedCommits] repositories and [runapi.MaxPushedCommitsBytes] bytes in a call, one
// call after the other, in the order the push moved them, through the run's link to the gateway.
// A push that moved nothing sends no call. It returns ended, the line that says how the run
// ended, when the run is over, and "" otherwise: Qory Apiary returned run_ended, or the gateway
// returned 410, see [runEndedLine]. The run's end stands as it was recorded, and no call
// follows. Any other 4xx status, retries used up, and a context that ended during the report
// return the error that ends the run, see [recordFailed]. Qory Apiary takes a repeat of a
// commit as a no-op, so each call is sent again with the same body.
func recordPushed(ctx context.Context, run openedRun, branch *preparation.WorkBranch) (ended string, err error) {
	return recordLanded(ctx, run.PushedCommits, branch.Ref, branch.Landed())
}

// recordLanded is [recordPushed] for the repositories landed, sent with send, with ref as the
// branch the failure line names.
func recordLanded(ctx context.Context, send pushedSender, ref string, landed []preparation.Landed) (ended string, err error) {
	for _, commits := range pushedCalls(landed) {
		body, err := runapi.PushedCommitsBody(commits)
		if err != nil {
			return "", recordFailed(ref, commits[0].Commit, err)
		}
		err = sendPushed(ctx, send, body)
		if err == nil {
			continue
		}
		if line, over := runEndedLine(err); over {
			return line, nil
		}
		return "", recordFailed(ref, commits[0].Commit, err)
	}
	return "", nil
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

// sendPushed sends body, again after a failure that leaves open whether Qory Apiary has it: a
// request with no response, a status of 500 or above, and 429. It waits as [recordWaits] has it.
// It returns the last error, also when ctx ended.
func sendPushed(ctx context.Context, send pushedSender, body []byte) error {
	var last error
	for attempt := range recordAttempts {
		if attempt > 0 {
			if err := branchWait(ctx, recordWaits[attempt-1]); err != nil {
				return err
			}
		}
		last = sendPushedOnce(ctx, send, body)
		if last == nil || ctx.Err() != nil || !retryPushed(last) {
			return last
		}
	}
	return last
}

// sendPushedOnce sends body once, within [recordAttemptLimit].
func sendPushedOnce(ctx context.Context, send pushedSender, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, recordAttemptLimit)
	defer cancel()
	_, err := send(ctx, body)
	return err
}

// linkResponse is the status and the text of a response to a report that is not a 2xx, read from
// the error of [session.Run.PushedCommits], and who returned it, from: [accesskey.FromApiary]
// for a refusal of Qory Apiary's, which the gateway passes on, and [accesskey.FromGateway] for a
// refusal or status that the gateway returns itself, such as its 503 while it closes or reopens
// the run, and for a [*server.StatusError], which has no body that says otherwise. The text is
// the message of Qory Apiary's refusal, else its code. For a status from the gateway it is the
// code alone, because the gateway's message can tell the reader to send the call again, and a
// line of qory has no such step. ok is false for an error with no status: a transport failure,
// no response in time, or a cancelled context.
func linkResponse(err error) (status int, text, from string, ok bool) {
	var refusal *accesskey.Refusal
	var failure *server.StatusError
	switch {
	case errors.As(err, &refusal) && refusal.Status != 0:
		if refusal.From == accesskey.FromApiary {
			return refusal.Status, cmp.Or(refusal.Text, refusal.Code), accesskey.FromApiary, true
		}
		return refusal.Status, refusal.Code, accesskey.FromGateway, true
	case errors.As(err, &failure):
		return failure.Status, failure.Code, accesskey.FromGateway, true
	}
	return 0, "", "", false
}

// runEndedLine reports whether err shows that the run is over, and the line that says it: Qory
// Apiary's 409 run_ended, or the gateway's 410, which the link returns when the run ended during
// the call. The line is [textRunEndedLater] when Qory Apiary ended the run: a 409 run_ended
// that the link passes on with From apiary, or a 410 whose From is apiary. Otherwise it is
// [textRunEndedAtGateway] with the code of the end. The gateway's 410 has From gateway for each
// code of [server.EndCodes]: run_closed, stopped (the run's starter ended it), session_lost and
// batch_refused. A 410 of Qory Apiary never arrives as a 410: the gateway returns it as 502
// control_plane_unavailable. A 409 run_ended with From gateway ends no run.
func runEndedLine(err error) (line string, ended bool) {
	if end, over := server.Ended(err); over {
		if end.From == accesskey.FromApiary {
			return textRunEndedLater, true
		}
		return fmt.Sprintf(textRunEndedAtGateway, cmp.Or(end.Code, accesskey.CodeRunClosed)), true
	}
	var refusal *accesskey.Refusal
	if errors.As(err, &refusal) && refusal.Status == http.StatusConflict && refusal.Code == runapi.CodeRunEnded && refusal.From == accesskey.FromApiary {
		return textRunEndedLater, true
	}
	return "", false
}

// retryPushed reports whether err is one that a second send may not meet: no status came
// back, a status of 500 or above did (the gateway's 502 and 503 among them, from the gateway or
// passed on from Qory Apiary), or the status is 429.
func retryPushed(err error) bool {
	status, _, _, ok := linkResponse(err)
	return !ok || status >= http.StatusInternalServerError || status == http.StatusTooManyRequests
}

// recordFailed is the error that ends a run whose push Qory Apiary did not record: the
// first commit of the call that failed, the branch ref, then the status with the message or
// code of Qory Apiary or of the gateway, whichever returned it, or the cause of a request with
// no response. The text has no step to
// retry, because qory has sent the call again, or ctx ended. The commits stay on the forge.
func recordFailed(ref, commit string, err error) error {
	var detail string
	status, text, from, responded := linkResponse(err)
	switch {
	case responded:
		format := textRecordGateway
		if from == accesskey.FromApiary {
			format = textRecordApiary
		}
		detail = fmt.Sprintf(format, status, shownError(errors.New(cmp.Or(text, http.StatusText(status)))))
	case errors.Is(err, context.Canceled):
		detail = fmt.Sprintf(textRecordNoReply, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		detail = fmt.Sprintf(textRecordNoReply, context.DeadlineExceeded)
	default:
		detail = fmt.Sprintf(textRecordNoReply, shownError(err))
	}
	return fmt.Errorf(textNotRecorded, commit, ref, detail)
}
