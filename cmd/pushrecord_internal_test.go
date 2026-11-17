package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

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

// recordServer serves POST pushed-commits and answers call n with answer(n), or 204 when it
// is nil. It returns the client and the number of repositories of each call it got, in order.
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

// TestALaterCallStopsWhenAnEarlierOneFails is a push of 65 repositories whose first call is
// refused with 400: the second call is not sent, and the failure line names the first commit.
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
