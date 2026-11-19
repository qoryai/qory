package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/integrations/contracts"
)

// endAnswer and sessionAnswer are answers to the next-session request: the end of the run, and
// a session 2 of the workflow fix on codex with a model key.
const (
	endAnswer     = `{"next":"end"}`
	sessionAnswer = `{"next":"session","session":2,"workflow":"fix","model_choice":{"runtime":"codex"},"model_credential":"example-provider-1","time_limit_minutes":30,"full_suite":true,"inputs":{"version":1}}`
)

// nextLink is a link that returns, for send n from 1, the answer or the error of respond(n),
// and records each body it got.
type nextLink struct {
	mu      sync.Mutex
	bodies  []string
	respond func(n int) ([]byte, error)
}

// send is the link's [nextSender].
func (l *nextLink) send(_ context.Context, body []byte) ([]byte, error) {
	l.mu.Lock()
	l.bodies = append(l.bodies, string(body))
	n := len(l.bodies)
	l.mu.Unlock()
	return l.respond(n)
}

// sends is how many requests the link got.
func (l *nextLink) sends() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

// shortWaits makes each wait between two sends a thousandth of its length, at most until ctx
// ends, and records each wait at its full length. It returns the waits and what restores
// the default.
func shortWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	var mu sync.Mutex
	waits := &[]time.Duration{}
	before := branchWait
	branchWait = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		*waits = append(*waits, d)
		mu.Unlock()
		select {
		case <-time.After(d / 1000):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { branchWait = before })
	return waits
}

// succeededOut is the outcome of a session that succeeded.
var succeededOut = session.Outcome{State: event.StateSucceeded}

// TestTheNextSessionAnswerIsRead is a request after session 1 that gets a session, and one
// after a session cancelled at its time limit that gets the end: one send each, with the
// session's number, state and reason in the body.
func TestTheNextSessionAnswerIsRead(t *testing.T) {
	link := &nextLink{respond: func(int) ([]byte, error) { return []byte(sessionAnswer), nil }}
	a, ended, err := askNextSession(context.Background(), link.send, 1, succeededOut)
	if err != nil || ended != "" {
		t.Fatalf("askNextSession: %q, %v", ended, err)
	}
	if a.Next != contracts.NextSessionKindSession || a.Workflow != "fix" || a.ModelChoice.Runtime != "codex" || a.ModelCredential != "example-provider-1" || a.TimeLimitMinutes != 30 || !a.FullSuite {
		t.Errorf("the answer is %+v", a)
	}
	if want := []string{`{"session":1,"state":"succeeded","reason":null}`}; !slices.Equal(link.bodies, want) {
		t.Errorf("the link got %q, want %q", link.bodies, want)
	}
	link = &nextLink{respond: func(int) ([]byte, error) { return []byte(endAnswer), nil }}
	a, ended, err = askNextSession(context.Background(), link.send, 3, session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout})
	if err != nil || ended != "" || a.Next != contracts.NextSessionKindEnd {
		t.Fatalf("askNextSession: %+v, %q, %v", a, ended, err)
	}
	if want := []string{`{"session":3,"state":"cancelled","reason":"timeout"}`}; !slices.Equal(link.bodies, want) {
		t.Errorf("the link got %q, want %q", link.bodies, want)
	}
}

// TestTheNextSessionRequestWaitsDoublingAndStopsAfterItsLimit is a request that gets the
// gateway's 503 every time: qory sends it again after 1 s, 2 s, 4 s and so on, each wait
// twice the one before up to 1 minute, until its limit of 10 minutes, here a shorter clock,
// and then the run cannot go on, with the fix first and the last status after it.
func TestTheNextSessionRequestWaitsDoublingAndStopsAfterItsLimit(t *testing.T) {
	waits := shortWaits(t)
	before := nextSessionLimit
	nextSessionLimit = 400 * time.Millisecond
	t.Cleanup(func() { nextSessionLimit = before })
	link := &nextLink{respond: func(int) ([]byte, error) { return nil, GatewayBusyErr() }}
	_, ended, err := askNextSession(context.Background(), link.send, 2, succeededOut)
	if ended != "" || err == nil {
		t.Fatalf("askNextSession: %q, %v, want an error", ended, err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	if len(*waits) < len(want) || !slices.Equal((*waits)[:len(want)], want) {
		t.Errorf("the waits are %v, want %v first", *waits, want)
	}
	for _, w := range *waits {
		if w > time.Minute {
			t.Errorf("a wait of %v, over 1 minute", w)
		}
	}
	if link.sends() != len(*waits) && link.sends() != len(*waits)+1 {
		t.Errorf("%d sends after %d waits", link.sends(), len(*waits))
	}
	first, _, _ := strings.Cut(err.Error(), "\n")
	if first != "qory run: Check the gateway's connection to Qory Apiary, then take the work item in again." || len(first) > 100 {
		t.Errorf("the first line is %q", first)
	}
	if !strings.Contains(err.Error(), "after session 2 got no answer in 400ms") || !strings.Contains(err.Error(), "The gateway returned 503") {
		t.Errorf("the error is %q", err)
	}
}

// GatewayBusyErr is the gateway's 503 with no code, as the link returns it.
func GatewayBusyErr() error {
	return &server.StatusError{What: "the next-session request", Status: http.StatusServiceUnavailable, Message: "Send the next-session request again."}
}

// TestAnEndedRunGetsOneNextSessionRequest is a request that finds the run over: Qory
// Apiary's 409 run_ended, the gateway's 410, and an error that wraps session.ErrRunClosed.
// Each is one send, no error, and the line of runEndedLine.
func TestAnEndedRunGetsOneNextSessionRequest(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		line string
	}{
		"run_ended":  {&accesskey.Refusal{Status: http.StatusConflict, Code: "run_ended", From: accesskey.FromApiary}, textRunEndedLater},
		"410":        {&accesskey.Refusal{Status: http.StatusGone, Code: "stopped", From: accesskey.FromGateway}, fmt.Sprintf(textRunEndedAtGateway, "stopped")},
		"run closed": {fmt.Errorf("Open another run to send the next-session request: %w.", session.ErrRunClosed), fmt.Sprintf(textRunEndedAtGateway, accesskey.CodeRunClosed)},
	} {
		t.Run(name, func(t *testing.T) {
			waits := shortWaits(t)
			link := &nextLink{respond: func(int) ([]byte, error) { return nil, c.err }}
			_, ended, err := askNextSession(context.Background(), link.send, 1, succeededOut)
			if err != nil || ended != c.line {
				t.Errorf("askNextSession: %q, %v, want %q", ended, err, c.line)
			}
			if link.sends() != 1 || len(*waits) != 0 {
				t.Errorf("%d sends and %d waits, want 1 and 0", link.sends(), len(*waits))
			}
		})
	}
}

// TestEachRefusalCodeOfTheNextSessionRequest is a request whose first send gets each code of
// next-session-refusal.schema.json from Qory Apiary, a refusal of the gateway's and a failure
// with no response, and whose second send gets the end. rate_limited, unavailable, the
// gateway's refusal and no response are sent again after 1 s; run_ended is one send and the
// end line; every other code of Qory Apiary's is one send and an error with the fix first and
// Qory Apiary's status and code.
func TestEachRefusalCodeOfTheNextSessionRequest(t *testing.T) {
	apiary := func(status int, code string) error {
		return &accesskey.Refusal{Status: status, Code: code, From: accesskey.FromApiary}
	}
	for name, c := range map[string]struct {
		err     error
		retried bool
		ended   string
	}{
		"bad_request":                  {err: apiary(http.StatusBadRequest, "bad_request")},
		"unsupported_contract_version": {err: apiary(http.StatusBadRequest, "unsupported_contract_version")},
		"not_found":                    {err: apiary(http.StatusNotFound, "not_found")},
		"run_ended":                    {err: apiary(http.StatusConflict, "run_ended"), ended: textRunEndedLater},
		"payload_too_large":            {err: apiary(http.StatusRequestEntityTooLarge, "payload_too_large")},
		"invalid_request":              {err: apiary(http.StatusBadRequest, "invalid_request")},
		"session_mismatch":             {err: apiary(http.StatusConflict, "session_mismatch")},
		"rate_limited":                 {err: apiary(http.StatusTooManyRequests, "rate_limited"), retried: true},
		"unavailable":                  {err: apiary(http.StatusServiceUnavailable, "unavailable"), retried: true},
		"gateway refusal":              {err: &accesskey.Refusal{Status: http.StatusBadGateway, Code: "control_plane_unavailable", From: accesskey.FromGateway}, retried: true},
		"gateway 413":                  {err: &accesskey.Refusal{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", From: accesskey.FromGateway}, retried: true},
		"uncoded 503":                  {err: GatewayBusyErr(), retried: true},
		"no response":                  {err: errors.New("dial tcp: connection refused"), retried: true},
	} {
		t.Run(name, func(t *testing.T) {
			waits := shortWaits(t)
			link := &nextLink{respond: func(n int) ([]byte, error) {
				if n == 1 {
					return nil, c.err
				}
				return []byte(endAnswer), nil
			}}
			a, ended, err := askNextSession(context.Background(), link.send, 1, succeededOut)
			switch {
			case c.retried:
				if err != nil || ended != "" || a.Next != contracts.NextSessionKindEnd {
					t.Errorf("askNextSession: %+v, %q, %v; want the end after a second send", a, ended, err)
				}
				if link.sends() != 2 || !slices.Equal(*waits, []time.Duration{time.Second}) {
					t.Errorf("%d sends after the waits %v, want 2 after 1s", link.sends(), *waits)
				}
				return
			case c.ended != "":
				if err != nil || ended != c.ended {
					t.Errorf("askNextSession: %q, %v, want %q", ended, err, c.ended)
				}
			default:
				var r *accesskey.Refusal
				errors.As(c.err, &r)
				want := fmt.Sprintf("qory run: Contact support: Qory Apiary refused the next-session request after session 1.\n  Qory Apiary returned %d: %s", r.Status, r.Code)
				if ended != "" || err == nil || err.Error() != want {
					t.Errorf("askNextSession: %q, %v, want the error %q", ended, err, want)
				}
			}
			if link.sends() != 1 || len(*waits) != 0 {
				t.Errorf("%d sends and %d waits, want 1 and 0", link.sends(), len(*waits))
			}
		})
	}
}

// TestACoded503IsSentAgainUntilItPasses is Qory Apiary's 503 unavailable on the first three
// sends and a session on the fourth: the request is sent four times, after 1 s, 2 s and 4 s,
// and the session is read.
func TestACoded503IsSentAgainUntilItPasses(t *testing.T) {
	waits := shortWaits(t)
	link := &nextLink{respond: func(n int) ([]byte, error) {
		if n < 4 {
			return nil, &accesskey.Refusal{Status: http.StatusServiceUnavailable, Code: "unavailable", From: accesskey.FromApiary}
		}
		return []byte(sessionAnswer), nil
	}}
	a, ended, err := askNextSession(context.Background(), link.send, 1, succeededOut)
	if err != nil || ended != "" || a.Next != contracts.NextSessionKindSession {
		t.Fatalf("askNextSession: %+v, %q, %v", a, ended, err)
	}
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}; link.sends() != 4 || !slices.Equal(*waits, want) {
		t.Errorf("%d sends after the waits %v, want 4 after %v", link.sends(), *waits, want)
	}
}

// TestAnAnswerOutsideItsSchemaEndsTheRun is an answer with a member the schema does not have:
// one send, and an error whose first line is its fix and which has no value of the answer.
func TestAnAnswerOutsideItsSchemaEndsTheRun(t *testing.T) {
	link := &nextLink{respond: func(int) ([]byte, error) { return []byte(`{"next":"end","branch":"secret-branch"}`), nil }}
	_, ended, err := askNextSession(context.Background(), link.send, 1, succeededOut)
	if ended != "" || err == nil {
		t.Fatalf("askNextSession: %q, %v, want an error", ended, err)
	}
	if !strings.HasPrefix(err.Error(), "qory run: Update qory: the answer to the next-session request after session 1 does not pass qory's checks.\n") || strings.Contains(err.Error(), "secret-branch") {
		t.Errorf("the error is %q", err)
	}
	if link.sends() != 1 {
		t.Errorf("%d sends, want 1", link.sends())
	}
}
