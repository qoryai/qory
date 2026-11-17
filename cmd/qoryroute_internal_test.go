package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qoryai/forager/link"
)

// routeAsk sends q a request of method to path, with the Qory-Session values given, and
// returns the status, the Content-Type, and the error code and the text of the body.
func routeAsk(t *testing.T, q *qoryRoute, method, path string, sessions ...string) (status int, contentType, code, text string) {
	t.Helper()
	r := httptest.NewRequest(method, link.QoryHostURL+path, strings.NewReader(`{"commit":"abc"}`))
	for _, s := range sessions {
		r.Header.Add(link.QorySessionHeader, s)
	}
	w := httptest.NewRecorder()
	q.ServeHTTP(w, r)
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the body %q: %v", w.Body.String(), err)
	}
	return w.Code, w.Header().Get("Content-Type"), body.Error, body.Message
}

// TestTheRouteOfARunWithNoWallServesNoPath is a request of the session the route is open to,
// of a run whose route has no suites: every path and method gets 404 not_found, the suite
// paths included.
func TestTheRouteOfARunWithNoWallServesNoPath(t *testing.T) {
	q := newQoryRoute()
	done := q.open(1)
	defer done()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodPost, "/v1/suites"},
		{http.MethodGet, "/v1/suites/su_1?wait=1"},
		{http.MethodDelete, "/anything/else"},
	} {
		status, contentType, code, text := routeAsk(t, q, c.method, c.path, "1")
		if status != http.StatusNotFound || code != routeNotFound || text != textRouteNotFound {
			t.Errorf("%s %s: %d %s %q, want 404 %s", c.method, c.path, status, code, text, routeNotFound)
		}
		if contentType != "application/json" {
			t.Errorf("%s %s: Content-Type %q", c.method, c.path, contentType)
		}
	}
}

// TestTheRouteRefusesAnotherSession is a request whose Qory-Session is not the session the
// route is open to: 503 session_not_running, also once that session's join returned and
// before the route opens; a request whose Qory-Session is missing or malformed counts as
// no open session and gets 503 too, with the route open or closed.
func TestTheRouteRefusesAnotherSession(t *testing.T) {
	q := newQoryRoute()
	if status, _, code, _ := routeAsk(t, q, http.MethodGet, "/", "1"); status != http.StatusServiceUnavailable || code != routeSessionNotRunning {
		t.Errorf("before the route opens: %d %s, want 503 %s", status, code, routeSessionNotRunning)
	}
	done := q.open(2)
	for _, n := range []int{1, 3} {
		status, _, code, text := routeAsk(t, q, http.MethodGet, "/", fmt.Sprint(n))
		if status != http.StatusServiceUnavailable || code != routeSessionNotRunning || text != fmt.Sprintf(textRouteNotRunning, n) {
			t.Errorf("session %d: %d %s %q, want 503 %s", n, status, code, text, routeSessionNotRunning)
		}
	}
	for _, sessions := range [][]string{nil, {""}, {"0"}, {"-2"}, {"+2"}, {"02"}, {" 2"}, {"two"}, {"2", "2"}} {
		status, _, code, text := routeAsk(t, q, http.MethodGet, "/", sessions...)
		if status != http.StatusServiceUnavailable || code != routeSessionNotRunning || text != textRouteNoNumber {
			t.Errorf("Qory-Session %q, route open: %d %s %q, want 503 %s %q", sessions, status, code, text, routeSessionNotRunning, textRouteNoNumber)
		}
	}
	done()
	if status, _, code, _ := routeAsk(t, q, http.MethodGet, "/", "2"); status != http.StatusServiceUnavailable || code != routeSessionNotRunning {
		t.Errorf("after the join returned: %d %s, want 503 %s", status, code, routeSessionNotRunning)
	}
	for _, sessions := range [][]string{nil, {""}, {"0"}, {"2", "2"}} {
		if status, _, code, text := routeAsk(t, q, http.MethodGet, "/", sessions...); status != http.StatusServiceUnavailable || code != routeSessionNotRunning || text != textRouteNoNumber {
			t.Errorf("Qory-Session %q, route closed: %d %s %q, want 503 %s %q", sessions, status, code, text, routeSessionNotRunning, textRouteNoNumber)
		}
	}
	later := q.open(3)
	defer later()
	done()
	if status, _, code, _ := routeAsk(t, q, http.MethodGet, "/", "3"); status != http.StatusNotFound || code != routeNotFound {
		t.Errorf("an earlier session's close closed the route to session 3: %d %s", status, code)
	}
}

// TestTheRoutesTextsStartWithTheFix holds each text of the route to one line of at most
// 100 characters.
func TestTheRoutesTextsStartWithTheFix(t *testing.T) {
	for _, text := range []string{fmt.Sprintf(textRouteNotRunning, 12), textRouteNoNumber, textRouteNotFound} {
		if strings.Contains(text, "\n") || len(text) > 100 {
			t.Errorf("%q is more than one line of 100 characters", text)
		}
	}
}
