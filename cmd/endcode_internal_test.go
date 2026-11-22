package cmd

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/preparation"
)

// TestAFailedPushStepClosesWithItsEndCode is [pushEnding.outcome] after a session that
// succeeded: a push step that failed closes the run failed with the end code of its error as
// the reason, when the error has one, however the error is wrapped or joined with another;
// a step that failed with no end code, a cut step, and a session that did not succeed close
// with no end code.
func TestAFailedPushStepClosesWithItsEndCode(t *testing.T) {
	noCredential := &runLineError{pushProxyError(errors.New(errNowhereNoPush))}
	refused := &runLineError{&preparation.PushRefusedError{}}
	notRecorded := &runLineError{recordFailed("qory/w-1", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", errors.New("the link closed"))}
	succeeded := session.Outcome{State: event.StateSucceeded}
	for name, c := range map[string]struct {
		ending pushEnding
		err    error
		want   session.Outcome
	}{
		"no push credential":         {pushEnding{session: succeeded}, noCredential, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodePushCredentialMissing), ExitCode: 1}},
		"push refused":               {pushEnding{session: succeeded}, refused, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodePushRefused), ExitCode: 1}},
		"push not recorded":          {pushEnding{session: succeeded}, notRecorded, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodePushNotRecorded), ExitCode: 1}},
		"refused, then not recorded": {pushEnding{session: succeeded}, &runLineError{errors.Join(refused, notRecorded)}, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodePushRefused), ExitCode: 1}},
		"another failure":            {pushEnding{session: succeeded}, &runLineError{errors.New("the push failed")}, session.Outcome{State: event.StateFailed, ExitCode: 1}},
		"cut at the time limit":      {pushEnding{session: succeeded, cut: true}, refused, session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: exitTimeout}},
		"session failed":             {pushEnding{session: session.Outcome{State: event.StateFailed, ExitCode: 3}}, refused, session.Outcome{State: event.StateFailed, ExitCode: 3}},
	} {
		if got := c.ending.outcome(c.err); got != c.want {
			t.Errorf("%s: the run closes with %+v, want %+v", name, got, c.want)
		}
	}
}

// TestForagersCloseTakesTheEndCodes is a run that Forager opens at a gateway in this process,
// closed failed with each end code as the reason: the session's check of the outcome takes it,
// and Close returns the reason as given.
func TestForagersCloseTakesTheEndCodes(t *testing.T) {
	for _, code := range []contracts.EndCode{
		contracts.EndCodePushCredentialMissing, contracts.EndCodePushRefused,
		contracts.EndCodePrebuiltImageTooLarge, contracts.EndCodePushNotRecorded,
	} {
		t.Run(string(code), func(t *testing.T) {
			ctx := context.Background()
			at, err := openAtGateway(ctx, session.OpenSpec{RunID: event.NewRunID(), RunsDir: t.TempDir(), Report: func(string) {}}, "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer at.g.Close(ctx)
			closed, err := at.run.Close(ctx, session.Outcome{State: event.StateFailed, Reason: string(code), ExitCode: 1})
			if err != nil {
				t.Fatalf("Close refused the outcome: %v", err)
			}
			if closed.State != event.StateFailed || closed.Reason != string(code) {
				t.Errorf("Close returned %q %q, want failed %q", closed.State, closed.Reason, code)
			}
		})
	}
}

// TestForagersCloseTakesTheEndCodesAfterAJoin is a run that Forager opens at a gateway in this
// process, where a session joined and then the run closes failed with each end code of a push
// failure as the reason, with exit code 1: the gateway takes the code after a join, as it does
// before one, and Close returns the reason as given.
func TestForagersCloseTakesTheEndCodesAfterAJoin(t *testing.T) {
	for _, code := range []contracts.EndCode{
		contracts.EndCodePushCredentialMissing, contracts.EndCodePushRefused,
		contracts.EndCodePrebuiltImageTooLarge, contracts.EndCodePushNotRecorded,
	} {
		t.Run(string(code), func(t *testing.T) {
			ctx := context.Background()
			at, err := openAtGateway(ctx, session.OpenSpec{RunID: event.NewRunID(), RunsDir: t.TempDir(), Report: func(string) {}}, "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer at.g.Close(ctx)
			spec := session.SessionSpec{Command: "/usr/bin/true", Dir: t.TempDir(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}
			if _, err := at.run.Join(ctx, spec); err != nil {
				t.Fatal(err)
			}
			closed, err := at.run.Close(ctx, session.Outcome{State: event.StateFailed, Reason: string(code), ExitCode: 1})
			if err != nil {
				t.Fatalf("Close refused the outcome after a join: %v", err)
			}
			if closed.State != event.StateFailed || closed.Reason != string(code) {
				t.Errorf("Close returned %q %q, want failed %q", closed.State, closed.Reason, code)
			}
		})
	}
}
