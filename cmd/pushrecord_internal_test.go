package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

// TestTheWaitBeforeASecondSendIsRetryAfterAtMost30Seconds is the wait after a 429 or a 503.
func TestTheWaitBeforeASecondSendIsRetryAfterAtMost30Seconds(t *testing.T) {
	for _, c := range []struct {
		retryAfter int
		want       string
	}{{0, "1s"}, {3, "3s"}, {90, "30s"}} {
		got := recordWait(1e9, &runapi.Error{Status: 429, RetryAfter: c.retryAfter})
		if got.String() != c.want {
			t.Errorf("Retry-After %d waits %s, want %s", c.retryAfter, got, c.want)
		}
	}
}

// recordServer serves POST pushed-commits and responds to call n with the status answer(n), or
// 204 when it is nil. It returns the client and the number of repositories of each call it got, in order.
func recordServer(t *testing.T, answer func(n int) int) (*runapi.Client, *[]int) {
	t.Helper()
	var mu sync.Mutex
	var sizes []int
	busy := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if answer != nil {
			w.WriteHeader(answer(n))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, &sizes
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
	client, sizes := recordServer(t, nil)
	ended, err := recordLanded(context.Background(), client, "qory/w-1", landedRepositories(65))
	if ended || err != nil {
		t.Fatalf("ended = %v, err = %v", ended, err)
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	client, err := runapi.New(server.URL+"/v1/runs/r", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if ended, err := recordLanded(context.Background(), client, "qory/w-1", landed); ended || err != nil {
		t.Fatalf("ended = %v, err = %v", ended, err)
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
	client, sizes := recordServer(t, func(int) int { return http.StatusBadRequest })
	_, err := recordLanded(context.Background(), client, "qory/w-1", landedRepositories(65))
	if err == nil || !strings.HasPrefix(err.Error(), "Contact support: qory pushed "+strings.Repeat("b", 40)+" to qory/w-1, and Qory Apiary did not record it.\nQory Apiary returned 400: ") {
		t.Errorf("err = %v", err)
	}
	if !slices.Equal(*sizes, []int{64}) {
		t.Errorf("the calls had %v repositories, want [64]", *sizes)
	}
}

// cancelAfterResponse is a transport that cancels cancel once the response of the server has
// arrived, so a context ends while the report has its 503.
type cancelAfterResponse struct {
	http.RoundTripper
	cancel context.CancelFunc
}

func (c cancelAfterResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := c.RoundTripper.RoundTrip(r)
	c.cancel()
	return response, err
}

// TestAReportCutDuringTheCallStillGivesTheContactSupportLine is a context that ends during the
// report, as Ctrl-C or the time limit ends it: just after Qory Apiary returned 503, while the
// request has no response, and while qory waits to send again. Each returns the whole Contact
// support line, with no step to retry.
func TestAReportCutDuringTheCallStillGivesTheContactSupportLine(t *testing.T) {
	const head = "Contact support: qory pushed bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb to qory/w-1, and Qory Apiary did not record it.\n"
	for name, c := range map[string]struct {
		serve func(cancel context.CancelFunc, w http.ResponseWriter, r *http.Request)
		wrap  bool
		wait  bool
		want  string
	}{
		"503 returned": {func(_ context.CancelFunc, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}, true, false, "Qory Apiary returned 503: Service Unavailable"},
		"no response": {func(cancel context.CancelFunc, _ http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			cancel()
			<-r.Context().Done()
		}, false, false, "Qory Apiary sent no response: context canceled"},
		"wait before the second send": {func(_ context.CancelFunc, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}, false, true, "Qory Apiary sent no response: context canceled"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c.serve(cancel, w, r)
			}))
			defer server.Close()
			httpClient := server.Client()
			if c.wrap {
				httpClient.Transport = cancelAfterResponse{httpClient.Transport, cancel}
			}
			if c.wait {
				before := branchWait
				branchWait = func(ctx context.Context, _ time.Duration) error {
					cancel()
					return ctx.Err()
				}
				defer func() { branchWait = before }()
			}
			client, err := runapi.New(server.URL+"/v1/runs/r", httpClient)
			if err != nil {
				t.Fatal(err)
			}
			ended, err := recordLanded(ctx, client, "qory/w-1", landedRepositories(1))
			if ended || err == nil {
				t.Fatalf("ended = %v, err = %v", ended, err)
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
