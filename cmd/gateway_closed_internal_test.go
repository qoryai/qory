package cmd

import (
	"strings"
	"testing"

	"github.com/qoryai/forager/event"

	"github.com/qoryai/qory/internal/ui"
)

// TestGatewayClosedSaysWhy prints the end of a run the gateway closed by its code: a
// refused batch and a silent session each with its own line, the silence as long as the
// gateway waits, three heartbeats of qory's run; any other close of the gateway's,
// run_closed, credential_expired and run_ended_at_issuer, with no line of qory's. Each
// fails the run, exit 1, and none says the server closed it.
func TestGatewayClosedSaysWhy(t *testing.T) {
	if runQuiet.String() != "1m30s" {
		t.Errorf("the silence the gateway waits is %s, want 1m30s: three heartbeats of %s", runQuiet, runHeartbeat)
	}
	for _, c := range []struct {
		reason, want string
	}{
		{event.ReasonBatchRefused, "the gateway closed the run: it could not take an event the session sent, and claude was stopped\n"},
		{event.ReasonSessionLost, "the gateway closed the run: the session sent nothing for 1m30s, and claude was stopped\n"},
		{event.ReasonRunClosed, ""},
		{event.ReasonCredentialExpired, ""},
		{event.ReasonRunEndedAtIssuer, ""},
	} {
		var out strings.Builder
		err := gatewayClosed(ui.New(&out), c.reason, "claude")
		if ExitCode(err) != 1 {
			t.Errorf("%s: %v (exit %d), want exit 1", c.reason, err, ExitCode(err))
		}
		got := out.String()
		switch {
		case c.want == "" && got != "":
			t.Errorf("%s: printed %q, want nothing", c.reason, got)
		case c.want != "" && !strings.HasSuffix(got, " "+c.want):
			t.Errorf("%s: printed %q, want %q", c.reason, got, c.want)
		}
		if strings.Contains(got, "the server closed the run") {
			t.Errorf("%s: says the server closed the run: %q", c.reason, got)
		}
	}
}
