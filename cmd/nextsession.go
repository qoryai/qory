package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
)

// The lines of the next-session request, which qory sends after each session of a run whose
// documents set shared_run, see [askNextSession]. Each starts with its fix; qory run prints
// it after "qory run: ".
const (
	textNextNoAnswer = "Check the gateway's connection to Qory Apiary, then take the work item in again.\n  The next-session request after session %d had no successful response within %s.\n  %s"
	textNextRefused  = "Contact support: the next-session request after session %d failed.\n  %s"
	textNextUnread   = "Update qory: the answer to the next-session request after session %d does not pass qory's checks.\n  %s."
	textNextNoBody   = "Update qory: qory could not write the next-session request after session %d.\n  %s"
)

// The retries of the next-session request: after a failure that leaves open whether Qory
// Apiary has the request, qory waits nextFirstWait, doubles the wait up to nextMaxWait, and
// sends the request again, for nextSessionLimit in all. Qory Apiary returns the same body for
// a repeat for the same session. A test sets a shorter limit.
const (
	nextFirstWait = time.Second
	nextMaxWait   = time.Minute
)

var nextSessionLimit = 10 * time.Minute

// nextSender sends the body of one next-session request over the link of the run's gateway
// and returns the answer's body: [session.Run.NextSession].
type nextSender func(ctx context.Context, body []byte) ([]byte, error)

// askNextSession sends the next-session request after the session that Forager numbered
// session, which ended as out has it, and reads the answer. It returns:
//
//   - the answer, a next session or the end of the run, with ended "" and a nil error;
//   - ended, the line of [runEndedLine], when the run is over at Qory Apiary or at the
//     gateway: one send, no retry, and the run closes as it was;
//   - an error whose first line is its fix, when the run cannot go on: a refusal of Qory
//     Apiary's, an answer outside its schema, or no answer after nextSessionLimit. The run
//     then closes failed.
//
// A request with no response, a refusal of the gateway's, and a refusal of Qory Apiary's
// with a status of 500 or above or the code rate_limited are sent again, see [retryNext].
func askNextSession(ctx context.Context, send nextSender, session int, out session.Outcome) (contracts.NextSessionAnswer, string, error) {
	body, err := provisioner.NextSessionBody(session, out.State, out.Reason)
	if err != nil {
		return contracts.NextSessionAnswer{}, "", fmt.Errorf(textNextNoBody, session, shownError(err))
	}
	ctx, cancel := context.WithTimeout(ctx, nextSessionLimit)
	defer cancel()
	wait := nextFirstWait
	var answer []byte
	for {
		answer, err = send(ctx, body)
		if err == nil {
			break
		}
		if line, over := runEndedLine(err); over {
			return contracts.NextSessionAnswer{}, line, nil
		}
		if !retryNext(err) {
			return contracts.NextSessionAnswer{}, "", nextRefused(session, err)
		}
		// shortcut: the link of the pinned Forager passes no Retry-After on, so every wait
		// doubles; read Retry-After here once Forager's link error has it.
		if ctx.Err() != nil || branchWait(ctx, wait) != nil {
			return contracts.NextSessionAnswer{}, "", fmt.Errorf(textNextNoAnswer, session, limitText(nextSessionLimit), nextDetail(err))
		}
		wait = min(2*wait, nextMaxWait)
	}
	a, err := provisioner.ReadNextSessionAnswer(answer)
	if err != nil {
		return contracts.NextSessionAnswer{}, "", fmt.Errorf(textNextUnread, session, unreadAnswer(err))
	}
	return a, "", nil
}

// unreadAnswer is the line of an answer that err, an error of
// [provisioner.ReadNextSessionAnswer], shows unread.
func unreadAnswer(err error) string {
	if strings.HasPrefix(err.Error(), "it breaks ") {
		return "The answer does not match " + strings.TrimPrefix(err.Error(), "it breaks ")
	}
	return "The answer does not pass them: " + err.Error()
}

// nextRetried are the codes of Qory Apiary's refusals of the next-session request that
// qory sends again at any status: rate_limited, a 429, and unavailable, a 503 for a step Qory
// Apiary did not store. A refusal below 500 with any other code ends the run, see
// [nextRefused]. The codes are those of run/v1 next-session-refusal.schema.json, which a
// test reads.
var nextRetried = []string{"rate_limited", "unavailable"}

// retryNext reports whether the next-session request is sent again after err, the one place
// that reads a refusal's code: a refusal of Qory Apiary's with a status of 500 or above,
// whatever its code, or with a code of [nextRetried]; and, as a transport error, every
// refusal of the gateway's and a request with no response. A status with no coded refusal,
// a [*server.StatusError], is sent again as [retryPushed] has it: a status of 500 or above,
// such as the gateway's 503 while it reopens the run, and 429. Any other such status ends
// the run. An error that wraps [session.ErrRunClosed] is not sent again: the run sent
// nothing and is closed, which [runEndedLine] reads first.
func retryNext(err error) bool {
	var refusal *accesskey.Refusal
	if errors.As(err, &refusal) && refusal.Status != 0 {
		if refusal.From == accesskey.FromApiary {
			return refusal.Status >= http.StatusInternalServerError || slices.Contains(nextRetried, refusal.Code)
		}
		return true
	}
	return retryPushed(err)
}

// nextRefused is the error of a response that ends the run and is not sent again, see
// [retryNext]: the fix first, then the status and the text of Qory Apiary or the gateway.
func nextRefused(session int, err error) error {
	return fmt.Errorf(textNextRefused, session, nextDetail(err))
}

// nextDetail is the status and the text of err as a line of the next-session request shows
// them: who returned it, or the cause of a request with no response.
func nextDetail(err error) string {
	status, text, from, responded := linkResponse(err)
	switch {
	case responded && from == accesskey.FromApiary:
		return fmt.Sprintf(textRecordApiary, status, shownError(errors.New(cmp.Or(text, http.StatusText(status)))))
	case responded:
		return fmt.Sprintf(textRecordGateway, status, shownError(errors.New(cmp.Or(text, http.StatusText(status)))))
	case errors.Is(err, context.Canceled):
		return fmt.Sprintf(textRecordNoReply, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf(textRecordNoReply, context.DeadlineExceeded)
	}
	return fmt.Sprintf(textRecordNoReply, shownError(err))
}
