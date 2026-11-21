package cmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
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

	"github.com/qoryai/qory/internal/runapi"
)

// endAnswer and sessionAnswer are answers to the next-session request: the end of the run, and
// a session 2 of the workflow fix on codex with a model key.
const (
	endAnswer     = `{"next":"end"}`
	sessionAnswer = `{"next":"session","session":2,"workflow":"fix","model_choice":{"runtime":"codex"},"model_provider_credential_name":"example-provider-1","time_limit_minutes":30,"full_suite":true,"inputs":{"version":1}}`
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
	before := submissionWait
	submissionWait = func(ctx context.Context, d time.Duration) error {
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
	t.Cleanup(func() { submissionWait = before })
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
	if a.Next != contracts.NextSessionKindSession || a.Workflow != "fix" || a.ModelChoice.Runtime != "codex" || a.ModelProviderCredentialName != "example-provider-1" || a.TimeLimitMinutes != 30 || !a.FullSuite {
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
	if first != "Check the gateway's connection to Qory Apiary, then take the work item in again." || len(first) > 100 {
		t.Errorf("the first line is %q", first)
	}
	if !strings.Contains(err.Error(), "after session 2 had no successful response within 400ms") || !strings.Contains(err.Error(), "The gateway returned 503") {
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

// nextRefusalStatus is the status of each code of Qory Apiary's refusals of the next-session
// request, as the description of next-session-refusal.schema.json lists them. The schema's
// enum has the codes, and only its description has their statuses.
var nextRefusalStatus = map[string]int{
	"bad_request":                  http.StatusBadRequest,
	"unsupported_contract_version": http.StatusBadRequest,
	"not_found":                    http.StatusNotFound,
	"run_ended":                    http.StatusConflict,
	"payload_too_large":            http.StatusRequestEntityTooLarge,
	"invalid_request":              http.StatusBadRequest,
	"session_mismatch":             http.StatusConflict,
	"rate_limited":                 http.StatusTooManyRequests,
	"unavailable":                  http.StatusServiceUnavailable,
}

// TestTheNextSessionRefusalCodesAreThePinnedSchemas reads the codes of the enum error in
// next-session-refusal.schema.json of the pinned integrations: they are the codes of
// nextRefusalStatus, which TestEachRefusalCodeOfTheNextSessionRequest sends, so a code that
// the contract adds fails here until the test sets its status and what qory does with it.
// Each code of nextRetried, and run_ended, which runEndedLine reads, is one of them.
func TestTheNextSessionRefusalCodesAreThePinnedSchemas(t *testing.T) {
	doc, err := contracts.RunDocument("next-session-refusal.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	if props, ok := doc.(map[string]any)["properties"].(map[string]any); ok {
		if e, ok := props["error"].(map[string]any); ok {
			enum, _ := e["enum"].([]any)
			for _, c := range enum {
				s, _ := c.(string)
				codes = append(codes, s)
			}
		}
	}
	if len(codes) == 0 {
		t.Fatalf("next-session-refusal.schema.json has no enum at /properties/error: %v", doc)
	}
	slices.Sort(codes)
	if tested := slices.Sorted(maps.Keys(nextRefusalStatus)); !slices.Equal(codes, tested) {
		t.Errorf("the schema's codes are %q, and the test sends %q", codes, tested)
	}
	for _, c := range append(slices.Clone(nextRetried), runapi.CodeRunEnded) {
		if !slices.Contains(codes, c) {
			t.Errorf("qory reads the code %q, which the schema's codes %q do not contain", c, codes)
		}
	}
}

// TestEachRefusalCodeOfTheNextSessionRequest is a request whose first send gets each code of
// nextRefusalStatus from Qory Apiary at its status, a status of 500 or above from Qory Apiary,
// a refusal of the gateway's, a status with no coded refusal, as the link returns it
// ([server.StatusError]), and a failure with no response, and whose second send gets the
// end. rate_limited, unavailable, a 500 of Qory Apiary's with any code, the gateway's
// refusals, an uncoded 503 or 429 and no response are sent again after 1 s; run_ended is one
// send and the end line; every other code of Qory Apiary's, and an uncoded 400, is one send
// and an error with the fix first and the status and code.
func TestEachRefusalCodeOfTheNextSessionRequest(t *testing.T) {
	apiary := func(status int, code string) error {
		return &accesskey.Refusal{Status: status, Code: code, From: accesskey.FromApiary}
	}
	type refusalCase struct {
		err     error
		retried bool
		ended   string
		// detail is the error's second line, for a status the gateway returned.
		detail string
	}
	cases := map[string]refusalCase{
		"500 internal":        {err: apiary(http.StatusInternalServerError, "internal"), retried: true},
		"500 invalid_request": {err: apiary(http.StatusInternalServerError, "invalid_request"), retried: true},
		"gateway refusal":     {err: &accesskey.Refusal{Status: http.StatusBadGateway, Code: "control_plane_unavailable", From: accesskey.FromGateway}, retried: true},
		"gateway 413":         {err: &accesskey.Refusal{Status: http.StatusRequestEntityTooLarge, Code: "payload_too_large", From: accesskey.FromGateway}, retried: true},
		"uncoded 503":         {err: GatewayBusyErr(), retried: true},
		"uncoded 429":         {err: &server.StatusError{What: "the next-session request", Status: http.StatusTooManyRequests}, retried: true},
		"uncoded 400":         {err: &server.StatusError{What: "the next-session request", Status: http.StatusBadRequest}, detail: "The gateway returned 400: Bad Request"},
		"no response":         {err: errors.New("dial tcp: connection refused"), retried: true},
	}
	for code, status := range nextRefusalStatus {
		c := refusalCase{err: apiary(status, code)}
		switch code {
		case "rate_limited", "unavailable":
			c.retried = true
		case "run_ended":
			c.ended = textRunEndedLater
		}
		cases[code] = c
	}
	for name, c := range cases {
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
				detail := c.detail
				var r *accesskey.Refusal
				if errors.As(c.err, &r) {
					detail = fmt.Sprintf("Qory Apiary returned %d: %s", r.Status, r.Code)
				}
				want := "Contact support: the next-session request after session 1 failed.\n  " + detail
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
	if err.Error() != "Update qory: the answer to the next-session request after session 1 does not pass qory's checks.\n  The answer does not match next-session-answer.schema.json." {
		t.Errorf("the error is %q", err)
	}
	if link.sends() != 1 {
		t.Errorf("%d sends, want 1", link.sends())
	}
}
