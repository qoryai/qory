package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/qoryai/forager/link"
)

// The codes and the words of the answers that [qoryRoute] writes itself. Each text starts
// with its fix.
const (
	routeSessionNotRunning = "session_not_running"
	routeNotFound          = "not_found"

	textRouteNotRunning = "Send the request from the session that runs: session %d does not run."
	textRouteNoNumber   = "Send the request from a running session: Qory-Session is missing or is not a number of 1 or more."
	textRouteNotFound   = "Check the request's path: qory serves nothing at it."
)

// qoryRoute is the handler of a run's route to qory, OpenSpec.QoryRoute: a program in a
// session of the run sends a plain HTTP request to QORY_HOST_URL, and the session's
// forwarder hands it here with the session's number in the header Qory-Session. The route
// serves only the session that [qoryRoute.open] opened it to: a request of another
// session gets 503, as the forwarder answers a request of a session that does not run, and
// so does a request whose Qory-Session is missing or malformed, which Forager's forwarder
// never sends. It serves no path, so every other request gets 404. Each answer it writes
// is JSON, {"error":<code>,"message":<text>}, the form of the responses that Forager's
// forwarder writes.
type qoryRoute struct {
	mu sync.Mutex
	// session is the number of the session the route serves, 0 for none.
	session int
}

// newQoryRoute is a run's route, open to no session.
func newQoryRoute() *qoryRoute { return &qoryRoute{} }

// open opens the route to session n, whose join starts, and returns what closes it again
// once the join returned.
func (q *qoryRoute) open(n int) (closeRoute func()) {
	q.mu.Lock()
	q.session = n
	q.mu.Unlock()
	return sync.OnceFunc(func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.session == n {
			q.session = 0
		}
	})
}

// ServeHTTP answers a request of the route.
func (q *qoryRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n, ok := sessionNumber(r.Header.Values(link.QorySessionHeader))
	q.mu.Lock()
	running := q.session
	q.mu.Unlock()
	if !ok {
		routeAnswer(w, http.StatusServiceUnavailable, routeSessionNotRunning, textRouteNoNumber)
		return
	}
	if n != running {
		routeAnswer(w, http.StatusServiceUnavailable, routeSessionNotRunning, fmt.Sprintf(textRouteNotRunning, n))
		return
	}
	routeAnswer(w, http.StatusNotFound, routeNotFound, textRouteNotFound)
}

// sessionNumber is the session number of a request's Qory-Session values: one value, a
// whole number from 1 in decimal with no sign and no leading zero. ok is false for any
// other values.
func sessionNumber(values []string) (n int, ok bool) {
	if len(values) != 1 {
		return 0, false
	}
	n, err := strconv.Atoi(values[0])
	if err != nil || n < 1 || strconv.Itoa(n) != values[0] {
		return 0, false
	}
	return n, true
}

// routeAnswer writes an answer of the route with status, code and text.
func routeAnswer(w http.ResponseWriter, status int, code, text string) {
	body, _ := json.Marshal(struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}{code, text})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
