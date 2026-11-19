package cmd

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/provisioner"
)

// The lines of the next-session request, which qory sends after each session of a run whose
// documents set shared_run, see [askNextSession]. Each starts with "qory run: " and its fix.
const (
	textNextNoAnswer = "qory run: Check the gateway's connection to Qory Apiary, then take the work item in again.\n  The next-session request after session %d got no answer in %s.\n  %s"
	textNextRefused  = "qory run: Contact support: Qory Apiary refused the next-session request after session %d.\n  %s"
	textNextUnread   = "qory run: Update qory: the answer to the next-session request after session %d does not pass qory's checks.\n  The answer %s."
	textNextNoBody   = "qory run: Update qory: qory could not write the next-session request after session %d.\n  %s"
)

// The retries of the next-session request: after a failure that leaves open whether Qory
// Apiary has the request, qory waits nextFirstWait, doubles the wait up to nextMaxWait, and
// sends the request again, for nextSessionLimit in all (C-1, settled point 4). Qory Apiary
// answers a repeat for the same session with the same body. A test sets a shorter limit.
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
// A request with no response, a status of 500 or above, 429 and every refusal of the
// gateway's are sent again, see [retryNext].
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
			return contracts.NextSessionAnswer{}, "", fmt.Errorf(textNextNoAnswer, session, nextSessionLimit, nextDetail(err))
		}
		wait = min(2*wait, nextMaxWait)
	}
	a, err := provisioner.ReadNextSessionAnswer(answer)
	if err != nil {
		return contracts.NextSessionAnswer{}, "", fmt.Errorf(textNextUnread, session, err)
	}
	return a, "", nil
}

// retryNext reports whether the next-session request is sent again after err: as
// [retryPushed] has it, and also after any refusal of the gateway's, which the card of the
// session loop takes as a transport error.
func retryNext(err error) bool {
	_, _, from, ok := linkResponse(err)
	return retryPushed(err) || ok && from == accesskey.FromGateway
}

// nextRefused is the error of a refusal of the next-session request that is no end of the
// run and is not sent again.
//
// shortcut: refusal codes not in the pinned contract yet; replace when C-1/T-02 carry them.
// Until then every refusal from Qory Apiary ends the run with its line, here, the one place
// that reads a refusal's code.
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
