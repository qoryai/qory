package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/forager/session"
)

// TestARunKeepsItsKeyWhileEventsRemain is qory run's own side of the run key a machine
// behind gateway.server.via keeps: the key stays while events of the run remain to be
// sent, whether the run ended with events the gateway did not accept or failed after it
// made its record, and goes once nothing is left: every event delivered, the run closed,
// or a run that failed before it made its record.
//
// A test of qory run itself is impractical to set up with events left undelivered behind
// a separate gateway: a run makes its record, and sends events, only once its session is
// attached at the gateway the run key names, over TLS that Forager checks against the
// system's roots alone outside its own tests, and Go loads those roots once per process,
// so a gateway a test starts would have to be trusted by the whole test binary.
func TestARunKeepsItsKeyWhileEventsRemain(t *testing.T) {
	record := filepath.Join(t.TempDir(), "0192f0c1-7d4e-7a2b-8c3d-444444444444")
	failed := errors.New("the session failed")
	if !nothingLeftToSend(failed, record, nil) {
		t.Error("a run that failed before it made its record keeps its key")
	}
	if err := os.Mkdir(record, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		err  error
		res  *session.Result
		goes bool
	}{
		{"events undelivered", nil, &session.Result{Undelivered: 2}, false},
		{"failed with its record", failed, nil, false},
		{"every event delivered", nil, &session.Result{}, true},
		{"events undelivered, the run closed", nil, &session.Result{Undelivered: 2, RunClosed: true}, true},
	} {
		if got := nothingLeftToSend(c.err, record, c.res); got != c.goes {
			t.Errorf("%s: the key goes %v, want %v", c.name, got, c.goes)
		}
	}
}
