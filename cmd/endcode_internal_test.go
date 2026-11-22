package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
)

// failedCall is the error of a GET of the artifact index that got status and body from the
// run API: a [*runapi.CallError].
func failedCall(t *testing.T, status int, body string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Artifacts(context.Background())
	if !errors.As(err, new(*runapi.CallError)) {
		t.Fatalf("the read failed with %v, want a CallError", err)
	}
	return err
}

// notSent is the error of a version over the limit of a request, which the client does not
// send.
func notSent(t *testing.T) error {
	t.Helper()
	client, err := runapi.New("http://127.0.0.1:1/v1/runs/r", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.PushArtifact(context.Background(), runapi.Push{
		Type:  "pull-request",
		Files: []runapi.PushFile{{Filename: "a", Content: make([]byte, runapi.MaxBody)}},
	})
	if err == nil {
		t.Fatal("a version over the limit was sent")
	}
	return err
}

// TestAFailedPushStepClosesWithItsEndCode is [pushEnding.outcome] after a session that
// succeeded: a push step that failed closes the run failed with the end code of its error as
// the reason, when the error has one, however the error is wrapped or joined with another;
// a step that failed with no end code, a cut step, and a session that did not succeed close
// with no end code. A failed call to the run API, also under the line that shows it, closes
// with run_api_failed, and any other code in the error comes first. A request that was not
// sent has no end code.
func TestAFailedPushStepClosesWithItsEndCode(t *testing.T) {
	noCredential := &runLineError{pushProxyError(errors.New(errNowhereNoPush))}
	call := failedCall(t, http.StatusInternalServerError, "")
	shown := withCause(notStored(nil, "pull-request", shownError(call)), call)
	// A refusal that qory artifact push exits with its status for keeps exit status 1 here.
	unknownType := failedCall(t, http.StatusUnprocessableEntity, `{"error":"unknown_type"}`)
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
		"outputs invalid":            {pushEnding{session: succeeded}, &runLineError{&outputsInvalid{errors.New("the output is missing")}}, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodeOutputsInvalid), ExitCode: 1}},
		"refused, then not recorded": {pushEnding{session: succeeded}, &runLineError{errors.Join(refused, notRecorded)}, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodePushRefused), ExitCode: 1}},
		"another failure":            {pushEnding{session: succeeded}, &runLineError{errors.New("the push failed")}, session.Outcome{State: event.StateFailed, ExitCode: 1}},
		"run API call failed":        {pushEnding{session: succeeded}, &runLineError{shown}, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodeRunAPIFailed), ExitCode: 1}},
		"run API, then push refused": {pushEnding{session: succeeded}, &runLineError{errors.Join(shown, refused)}, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodePushRefused), ExitCode: 1}},
		"run API under outputs":      {pushEnding{session: succeeded}, &outputsInvalid{call}, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodeOutputsInvalid), ExitCode: 1}},
		"refused call":               {pushEnding{session: succeeded}, &runLineError{withCause(errors.New("no type"), unknownType)}, session.Outcome{State: event.StateFailed, Reason: string(contracts.EndCodeRunAPIFailed), ExitCode: 1}},
		"request not sent":           {pushEnding{session: succeeded}, &runLineError{withCause(errors.New("not stored"), notSent(t))}, session.Outcome{State: event.StateFailed, ExitCode: 1}},
		"cut at the time limit":      {pushEnding{session: succeeded, cut: true}, refused, session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: exitTimeout}},
		"session failed":             {pushEnding{session: session.Outcome{State: event.StateFailed, ExitCode: 3}}, refused, session.Outcome{State: event.StateFailed, ExitCode: 3}},
	} {
		if got := c.ending.outcome(c.err); got != c.want {
			t.Errorf("%s: the run closes with %+v, want %+v", name, got, c.want)
		}
	}
}

// TestForagersCloseTakesTheEndCodes is a run that Forager opens at a gateway in this process,
// closed failed with each end code as the reason, with exit code 1, or -1 for run_api_failed
// as qory closes a run whose session does not join: the session's check of the outcome takes
// it, and Close returns the reason as given.
func TestForagersCloseTakesTheEndCodes(t *testing.T) {
	for _, code := range []contracts.EndCode{
		contracts.EndCodePushCredentialMissing, contracts.EndCodePushRefused,
		contracts.EndCodePrebuiltImageTooLarge, contracts.EndCodePushNotRecorded,
		contracts.EndCodeNextSessionFailed, contracts.EndCodeOutputsInvalid,
		contracts.EndCodeRunAPIFailed,
	} {
		t.Run(string(code), func(t *testing.T) {
			ctx := context.Background()
			at, err := openAtGateway(ctx, session.OpenSpec{RunID: event.NewRunID(), RunsDir: t.TempDir(), Report: func(string) {}}, "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer at.g.Close(ctx)
			exit := 1
			if code == contracts.EndCodeRunAPIFailed {
				exit = -1
			}
			closed, err := at.run.Close(ctx, session.Outcome{State: event.StateFailed, Reason: string(code), ExitCode: exit})
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
// process, where a session joined and then the run closes failed with each end code of the list
// as the reason, with exit code 1, or -1 for next_session_failed as qory closes it: the
// gateway takes the code after a join, as it does before one, and Close returns the reason as
// given.
func TestForagersCloseTakesTheEndCodesAfterAJoin(t *testing.T) {
	for _, code := range []contracts.EndCode{
		contracts.EndCodePushCredentialMissing, contracts.EndCodePushRefused,
		contracts.EndCodePrebuiltImageTooLarge, contracts.EndCodePushNotRecorded,
		contracts.EndCodeNextSessionFailed, contracts.EndCodeOutputsInvalid,
		contracts.EndCodeRunAPIFailed,
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
			// A failed next-session request closes the run as a later session that does not
			// join: with exit code -1.
			exit := 1
			if code == contracts.EndCodeNextSessionFailed {
				exit = -1
			}
			closed, err := at.run.Close(ctx, session.Outcome{State: event.StateFailed, Reason: string(code), ExitCode: exit})
			if err != nil {
				t.Fatalf("Close refused the outcome after a join: %v", err)
			}
			if closed.State != event.StateFailed || closed.Reason != string(code) {
				t.Errorf("Close returned %q %q, want failed %q", closed.State, closed.Reason, code)
			}
		})
	}
}
