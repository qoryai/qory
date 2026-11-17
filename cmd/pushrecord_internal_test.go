package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/server"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runapi"
)

// TestMoreThan64RepositoriesAreReportedInCallsOf64 is a push that moved 130 repositories: the
// reports are 64, 64 and 2 repositories, in the order the push moved them.
func TestMoreThan64RepositoriesAreReportedInCallsOf64(t *testing.T) {
	var landed []preparation.Landed
	for i := range 130 {
		landed = append(landed, preparation.Landed{System: "github.com", Path: fmt.Sprintf("acme/r%03d", i), Commit: strings.Repeat("a", 40)})
	}
	calls := pushedCalls(landed)
	if len(calls) != 3 || len(calls[0]) != 64 || len(calls[1]) != 64 || len(calls[2]) != 2 {
		t.Fatalf("the calls have %d, %d, ... repositories, want 64, 64 and 2", len(calls), len(calls[0]))
	}
	var order []string
	for _, call := range calls {
		for _, c := range call {
			order = append(order, c.Path)
		}
	}
	for i, path := range order {
		if want := fmt.Sprintf("acme/r%03d", i); path != want {
			t.Fatalf("repository %d is %s, want %s", i, path, want)
		}
	}
	if got := pushedCalls(nil); got != nil {
		t.Errorf("a push that moved nothing has the calls %v", got)
	}
}

// recordServer serves POST pushed-commits and returns for call n the status respond(n), or
// 204 when it is nil. It returns the sender, which reaches the server as the link does, and the number of
// repositories of each call it got, in order.
func recordServer(t *testing.T, respond func(n int) int) (pushedSender, *[]int) {
	t.Helper()
	var mu sync.Mutex
	var sizes []int
	busy := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if busy {
			t.Error("a call started before the earlier one ended")
		}
		busy = true
		mu.Unlock()
		defer func() { mu.Lock(); busy = false; mu.Unlock() }()
		var body struct {
			Repositories []runapi.PushedCommit `json:"repositories"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sizes = append(sizes, len(body.Repositories))
		n := len(sizes)
		mu.Unlock()
		if respond != nil {
			status := respond(n)
			w.WriteHeader(status)
			if status >= 400 {
				fmt.Fprint(w, `{"error":"invalid_request"}`)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return throughLink(srv.URL + "/v1/runs/r"), &sizes
}

// throughLink is a [pushedSender] that sends to the run API at base as the link of a gateway does.
func throughLink(base string) pushedSender {
	return func(ctx context.Context, body []byte) ([]byte, error) { return PushedThroughLink(ctx, base, body) }
}

func landedRepositories(n int) []preparation.Landed {
	var landed []preparation.Landed
	for i := range n {
		landed = append(landed, preparation.Landed{System: "github.com", Path: fmt.Sprintf("acme/r%03d", i), Commit: strings.Repeat("b", 40)})
	}
	return landed
}

// TestSixtyFiveRepositoriesAreSentAs64ThenOne is a push of 65 repositories: two calls, one
// after the other, the first with 64.
func TestSixtyFiveRepositoriesAreSentAs64ThenOne(t *testing.T) {
	send, sizes := recordServer(t, nil)
	ended, err := recordLanded(context.Background(), send, "qory/w-1", landedRepositories(65))
	if ended != "" || err != nil {
		t.Fatalf("ended = %q, err = %v", ended, err)
	}
	if !slices.Equal(*sizes, []int{64, 1}) {
		t.Errorf("the calls had %v repositories, want [64 1]", *sizes)
	}
}

// TestLongLabelsAreSplitBeforeABodyPasses64KiB is a push of 64 repositories with system and
// path labels of 256 bytes, mostly quotes, which the body escapes as 2 bytes each: the body
// of one call would be about 72 KiB. The report is two calls, each body at most 65536 bytes,
// with the 64 repositories in order and none twice. The bodies have `<`, `>` and `&` as they
// are, not as the 6-byte escapes of a default JSON encoder.
func TestLongLabelsAreSplitBeforeABodyPasses64KiB(t *testing.T) {
	label := func(prefix string, i int) string {
		return prefix + fmt.Sprintf("%02d", i) + "<>&" + strings.Repeat(`"`, 256-len(prefix)-5)
	}
	var landed []preparation.Landed
	for i := range 64 {
		landed = append(landed, preparation.Landed{System: label("s", i), Path: label("p", i), Commit: strings.Repeat("c", 64)})
	}
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	if ended, err := recordLanded(context.Background(), throughLink(srv.URL+"/v1/runs/r"), "qory/w-1", landed); ended != "" || err != nil {
		t.Fatalf("ended = %q, err = %v", ended, err)
	}
	if len(bodies) != 2 {
		t.Fatalf("the report is %d calls, want 2", len(bodies))
	}
	var got []runapi.PushedCommit
	for i, raw := range bodies {
		if len(raw) > runapi.MaxPushedCommitsBytes {
			t.Errorf("body %d has %d bytes, want at most %d", i, len(raw), runapi.MaxPushedCommitsBytes)
		}
		if !strings.Contains(string(raw), "<>&") || strings.Contains(string(raw), `\u003c`) {
			t.Errorf("body %d does not have `<>&` as it is", i)
		}
		var body struct {
			Repositories []runapi.PushedCommit `json:"repositories"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		got = append(got, body.Repositories...)
	}
	if len(got) != 64 {
		t.Fatalf("the calls have %d repositories, want 64", len(got))
	}
	for i, c := range got {
		if c.System != landed[i].System || c.Path != landed[i].Path {
			t.Fatalf("repository %d is %.5s %.5s, want %.5s %.5s", i, c.System, c.Path, landed[i].System, landed[i].Path)
		}
	}
}

// TestALaterCallStopsWhenAnEarlierOneFails is a push of 65 repositories whose first call gets
// 400: the second call is not sent, and the failure line names the first commit.
func TestALaterCallStopsWhenAnEarlierOneFails(t *testing.T) {
	send, sizes := recordServer(t, func(int) int { return http.StatusBadRequest })
	_, err := recordLanded(context.Background(), send, "qory/w-1", landedRepositories(65))
	if err == nil || !strings.HasPrefix(err.Error(), "qory run: Contact support: Qory Apiary did not record the last push.\n  qory pushed "+strings.Repeat("b", 40)+" to qory/w-1.\nQory Apiary returned 400: ") {
		t.Errorf("err = %v", err)
	}
	if !slices.Equal(*sizes, []int{64}) {
		t.Errorf("the calls had %v repositories, want [64]", *sizes)
	}
}

// TestAReportCutDuringTheCallStillGivesTheContactSupportLine is a context that ends during the
// report, as Ctrl-C or the time limit ends it: just after the link returned 503, while the
// request has no response, and while qory waits to send again. Each returns the whole Contact
// support line, with no step to retry.
func TestAReportCutDuringTheCallStillGivesTheContactSupportLine(t *testing.T) {
	const head = "qory run: Contact support: Qory Apiary did not record the last push.\n  qory pushed bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb to qory/w-1.\n"
	for name, c := range map[string]struct {
		send func(ctx context.Context, cancel context.CancelFunc) ([]byte, error)
		wait bool
		want string
	}{
		"503 returned": {func(_ context.Context, cancel context.CancelFunc) ([]byte, error) {
			cancel()
			return nil, GatewayBusy()
		}, false, "The gateway returned 503: Service Unavailable"},
		"no response": {func(ctx context.Context, cancel context.CancelFunc) ([]byte, error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		}, false, "Qory Apiary sent no response: context canceled"},
		"wait before the second send": {func(context.Context, context.CancelFunc) ([]byte, error) {
			return nil, GatewayBusy()
		}, true, "Qory Apiary sent no response: context canceled"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if c.wait {
				before := branchWait
				branchWait = func(ctx context.Context, _ time.Duration) error {
					cancel()
					return ctx.Err()
				}
				defer func() { branchWait = before }()
			}
			send := func(ctx context.Context, _ []byte) ([]byte, error) { return c.send(ctx, cancel) }
			ended, err := recordLanded(ctx, send, "qory/w-1", landedRepositories(1))
			if ended != "" || err == nil {
				t.Fatalf("ended = %q, err = %v", ended, err)
			}
			if got := err.Error(); got != head+c.want {
				t.Errorf("err = %q, want %q", got, head+c.want)
			}
			for _, retry := range []string{"Try again", "Run the command again"} {
				if strings.Contains(err.Error(), retry) {
					t.Errorf("err has %q: %v", retry, err)
				}
			}
		})
	}
}

// TestTheResponsesToAReportAreSortedByWhoReturnedThem is every response of the link that is not a
// 2xx: whether it ends the run and with which line, whether qory sends the report again, and the
// second line of the failure, which names Qory Apiary or the gateway by From.
func TestTheResponsesToAReportAreSortedByWhoReturnedThem(t *testing.T) {
	apiary := func(status int, code string) error {
		return &accesskey.Refusal{Code: code, Status: status, From: accesskey.FromApiary}
	}
	gateway410 := func(code string) error { return GatewayRefusal(410, code) }
	ranAtGateway := func(code string) string {
		return "qory run: The run ended (" + code + "), so qory pushes nothing more and no later session starts."
	}
	const apiaryEnded = "qory run: Qory Apiary ended this run, so qory pushes nothing more and no later session starts."
	for name, c := range map[string]struct {
		err   error
		ended string
		again bool
		// failure is the second line of the Contact support text, "" for a response that ends
		// the run.
		failure string
	}{
		"apiary 409 run_ended":                  {apiary(409, "run_ended"), apiaryEnded, false, ""},
		"apiary 410 with From apiary":           {&accesskey.Refusal{Code: "run_closed", Status: 410, From: accesskey.FromApiary}, apiaryEnded, false, ""},
		"gateway 409 run_ended":                 {GatewayRefusal(409, "run_ended"), "", false, "The gateway returned 409: run_ended"},
		"apiary 409 not_writable":               {apiary(409, "not_writable"), "", false, "Qory Apiary returned 409: not_writable"},
		"apiary 400":                            {apiary(400, "invalid_request"), "", false, "Qory Apiary returned 400: invalid_request"},
		"apiary 413":                            {apiary(413, "payload_too_large"), "", false, "Qory Apiary returned 413: payload_too_large"},
		"apiary 429":                            {apiary(429, "rate_limited"), "", true, "Qory Apiary returned 429: rate_limited"},
		"apiary 500":                            {apiary(500, "unavailable"), "", true, "Qory Apiary returned 500: unavailable"},
		"gateway 410 run_closed":                {gateway410("run_closed"), ranAtGateway("run_closed"), false, ""},
		"gateway 410 stopped":                   {gateway410("stopped"), ranAtGateway("stopped"), false, ""},
		"gateway 410 session_lost":              {gateway410("session_lost"), ranAtGateway("session_lost"), false, ""},
		"gateway 410 batch_refused":             {gateway410("batch_refused"), ranAtGateway("batch_refused"), false, ""},
		"gateway 413":                           {GatewayRefusal(413, "payload_too_large"), "", false, "The gateway returned 413: payload_too_large"},
		"gateway 502 control_plane_unavailable": {GatewayRefusal(502, "control_plane_unavailable"), "", true, "The gateway returned 502: control_plane_unavailable"},
		"gateway 503 with no code":              {GatewayBusy(), "", true, "The gateway returned 503: Service Unavailable"},
		"gateway 500 internal":                  {&server.StatusError{What: "the pushed-commits request", Status: 500, Code: "internal"}, "", true, "The gateway returned 500: internal"},
		"no status":                             {errors.New("the link closed"), "", true, "Qory Apiary sent no response: the link closed"},
	} {
		line, ended := runEndedLine(c.err)
		if line != c.ended || ended != (c.ended != "") {
			t.Errorf("%s: the end is %q (%v), want %q", name, line, ended, c.ended)
		}
		if got := retryPushed(c.err); got != c.again {
			t.Errorf("%s: sent again = %v, want %v", name, got, c.again)
		}
		if c.failure != "" {
			want := "qory run: Contact support: Qory Apiary did not record the last push.\n  qory pushed " + strings.Repeat("b", 40) + " to qory/w-1.\n" + c.failure
			if got := recordFailed("qory/w-1", strings.Repeat("b", 40), c.err).Error(); got != want {
				t.Errorf("%s: the failure is %q, want %q", name, got, want)
			}
		}
	}
}

// TestASendHas65SecondsAndTheWaitsAre1And2And4 are the bounds of the report: each send is cut at
// 65 s, 5 s after the gateway's 60 s for a call ([server.HostCallTime]), and there are 3 waits.
func TestASendHas65SecondsAndTheWaitsAre1And2And4(t *testing.T) {
	if recordAttemptLimit != 65*time.Second {
		t.Errorf("a send has %s, want 65s", recordAttemptLimit)
	}
	if recordAttemptLimit <= server.HostCallTime {
		t.Errorf("a send has %s, but the gateway waits %s for a call: the gateway's response must arrive first", recordAttemptLimit, server.HostCallTime)
	}
	if recordWaits != [3]time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		t.Errorf("the waits are %v, want [1s 2s 4s]", recordWaits)
	}
}

// TestASendThatGetsNoResponseIsCutAtTheLimit is a send that gets no response from the gateway: the send ends
// when its limit does, with the context's deadline error, and the second send follows.
func TestASendThatGetsNoResponseIsCutAtTheLimit(t *testing.T) {
	before := recordAttemptLimit
	recordAttemptLimit = 20 * time.Millisecond
	defer func() { recordAttemptLimit = before }()
	waits := branchWait
	branchWait = func(context.Context, time.Duration) error { return nil }
	defer func() { branchWait = waits }()
	var sends int
	send := func(ctx context.Context, _ []byte) ([]byte, error) {
		sends++
		if sends == 2 {
			return nil, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ended, err := recordLanded(context.Background(), send, "qory/w-1", landedRepositories(1))
	if ended != "" || err != nil || sends != 2 {
		t.Errorf("ended = %q, err = %v, sends = %d, want \"\", nil and 2", ended, err, sends)
	}
}
