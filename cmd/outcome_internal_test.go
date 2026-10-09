package cmd

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/ui"
)

// endOf is what runEnded prints of res, without the marks, and the exit status it
// returns.
func endOf(res *session.Result, e runEnd) (said string, code int) {
	var out strings.Builder
	err := runEnded(ui.New(&out), res, e)
	said = strings.NewReplacer("✓ ", "", "✗ ", "").Replace(out.String())
	return said, ExitCode(err)
}

// TestRunEndedSaysTheOutcome is the last line of qory run for each way a run that
// started ends, and qory run's exit status: a run the gateway closed by its outcome and
// reason, Forager's reasons in qory's words and a starter's as it gave it with spaces
// for underscores, 0 when it completed and 1 otherwise; the time limit, cancelled, 124;
// a signal qory run got, cancelled, 1; and the runtime's own exit, which keeps its
// status unless the outcome its starter gave at that exit differs, said in a second
// line that the status follows. No line says "issuer", "succeeded" or a raw code.
func TestRunEndedSaysTheOutcome(t *testing.T) {
	if runQuiet.String() != "1m30s" {
		t.Errorf("the silence the gateway waits is %s, want 1m30s: three heartbeats of %s", runQuiet, runHeartbeat)
	}
	closed := func(state, reason string) *session.Result {
		return &session.Result{RunClosed: true, ClosedReason: reason, State: state, Reason: reason, ExitCode: -1}
	}
	starter := func(state, reason string) *session.Result {
		return &session.Result{RunClosed: true, ClosedReason: event.ReasonStopped, State: state, Reason: reason, ExitCode: -1}
	}
	for _, c := range []struct {
		name string
		res  *session.Result
		got  string
		want string
		code int
	}{
		{"a batch refused", closed("failed", event.ReasonBatchRefused), "",
			"the run failed: its events could not be recorded, and claude was stopped\n", 1},
		{"the session silent", closed("failed", event.ReasonSessionLost), "",
			"the run was lost: it lost contact with the gateway for 1m30s, and claude was stopped\n", 1},
		{"the gateway stopped", closed("failed", event.ReasonRunClosed), "",
			"the run failed: the gateway stopped during the run, and claude was stopped\n", 1},
		{"an expiry with no time", closed("cancelled", event.ReasonCredentialExpired), "",
			"the run was cancelled: the run credential expired, and claude was stopped\n", 1},
		{"an expiry before the gateway gives its state", closed("failed", event.ReasonCredentialExpired), "",
			"the run was cancelled: the run credential expired, and claude was stopped\n", 1},
		{"no answer to the check", closed("failed", event.ReasonCredentialCheckUnreachable), "",
			"the run failed: its run credential could not be checked, and claude was stopped\n", 1},
		{"no valid answer to the check", closed("failed", event.ReasonCredentialCheckInvalid), "",
			"the run failed: its run credential could not be checked, and claude was stopped\n", 1},
		{"stopped with no outcome", closed("cancelled", event.ReasonStopped), "",
			"the run was cancelled, with no outcome given, and claude was stopped\n", 1},
		{"stopped before the gateway gives its state", closed("failed", event.ReasonStopped), "",
			"the run was cancelled, with no outcome given, and claude was stopped\n", 1},
		{"the starter's success", starter("succeeded", "all_checks_passed"), "",
			"the run completed: all checks passed, and claude was stopped\n", 0},
		{"the starter's success, no reason", starter("succeeded", ""), "",
			"the run completed, and claude was stopped\n", 0},
		{"the starter's failure", starter("failed", "checks_failed"), "",
			"the run failed: checks failed, and claude was stopped\n", 1},
		{"the starter's cancel", starter("cancelled", "no_longer_needed"), "",
			"the run was cancelled: no longer needed, and claude was stopped\n", 1},
		{"the starter's cancel, no reason", starter("cancelled", ""), "",
			"the run was cancelled, and claude was stopped\n", 1},
		{"the time limit", &session.Result{TimedOut: true, State: "cancelled", Reason: event.ReasonTimeout, ExitCode: -1, Signal: "SIGTERM"}, "",
			"the run was cancelled: it reached the time limit of 5m0s, and claude was stopped\n", exitTimeout},
		{"qory run got SIGTERM", &session.Result{State: "failed", ExitCode: -1, Signal: "SIGTERM"}, "SIGTERM",
			"the run was cancelled: qory run got SIGTERM, and claude was stopped\n", 1},
		{"qory run got SIGINT, the runtime exited 0", &session.Result{State: "succeeded"}, "SIGINT",
			"the run was cancelled: qory run got SIGINT, and claude was stopped\n", 1},
		{"a kill from elsewhere", &session.Result{State: "failed", ExitCode: -1, Signal: "SIGKILL"}, "",
			"claude ended on the signal SIGKILL\n", 1},
		{"exit 0", &session.Result{State: "succeeded"}, "", "claude exited 0\n", 0},
		{"exit 3", &session.Result{State: "failed", ExitCode: 3}, "", "claude exited 3\n", 3},
		{"exit 0, the starter agrees", &session.Result{State: "succeeded", Reason: "all_checks_passed"}, "", "claude exited 0\n", 0},
		{"exit 3, the starter agrees", &session.Result{State: "failed", Reason: "checks_failed", ExitCode: 3}, "", "claude exited 3\n", 3},
		{"exit 0, the starter's failure", &session.Result{State: "failed", Reason: "checks_failed"}, "",
			"claude exited 0\nthe run failed: checks failed\n", 1},
		{"exit 0, the starter's failure, no reason", &session.Result{State: "failed"}, "",
			"claude exited 0\nthe run failed\n", 1},
		{"exit 0, the starter's cancel", &session.Result{State: "cancelled", Reason: "no_longer_needed"}, "",
			"claude exited 0\nthe run was cancelled: no longer needed\n", 1},
		{"exit 3, the starter's success", &session.Result{State: "succeeded", Reason: "all_checks_passed", ExitCode: 3}, "",
			"claude exited 3\nthe run completed: all checks passed\n", 0},
		{"exit 3, the starter's success, no reason", &session.Result{State: "succeeded", ExitCode: 3}, "",
			"claude exited 3\nthe run completed\n", 0},
		{"a kill, the starter's success", &session.Result{State: "succeeded", ExitCode: -1, Signal: "SIGKILL"}, "",
			"claude ended on the signal SIGKILL\nthe run completed\n", 0},
		{"exit 3, the starter's cancel", &session.Result{State: "cancelled", ExitCode: 3}, "",
			"claude exited 3\nthe run was cancelled\n", 1},
	} {
		said, code := endOf(c.res, runEnd{runtime: "claude", timeout: 5 * time.Minute, got: c.got})
		if said != c.want || code != c.code {
			t.Errorf("%s: said %q, exit %d; want %q, exit %d", c.name, said, code, c.want, c.code)
		}
		for _, word := range []string{"issuer", "succeeded", "_", "ended by", "closed the run"} {
			if strings.Contains(said, word) {
				t.Errorf("%s: says %q: %q", c.name, word, said)
			}
		}
	}
}

// TestRunEndedMarksTheOutcome is the mark of each line: ✓ for a run that completed and
// for a runtime's exit 0, ✗ for every other end.
func TestRunEndedMarksTheOutcome(t *testing.T) {
	for _, c := range []struct {
		res  *session.Result
		want string
	}{
		{&session.Result{RunClosed: true, ClosedReason: event.ReasonStopped, State: "succeeded"}, "✓ the run completed, and claude was stopped\n"},
		{&session.Result{RunClosed: true, ClosedReason: event.ReasonStopped, State: "cancelled", Reason: event.ReasonStopped}, "✗ the run was cancelled, with no outcome given, and claude was stopped\n"},
		{&session.Result{State: "failed", Reason: "checks_failed"}, "✓ claude exited 0\n✗ the run failed: checks failed\n"},
		{&session.Result{State: "succeeded", ExitCode: 2}, "✗ claude exited 2\n✓ the run completed\n"},
	} {
		var out strings.Builder
		runEnded(ui.New(&out), c.res, runEnd{runtime: "claude"})
		if !strings.HasSuffix(out.String(), c.want) {
			t.Errorf("%+v: %q, want it to end with %q", c.res, out.String(), c.want)
		}
	}
}

// TestRunEndedSaysTheExpiryBySource is a run the gateway closed at the run credential's
// expiry, cancelled, in the words of where the credential came from when qory can read
// its exp, and without the time when it cannot. Exit 1 either way.
func TestRunEndedSaysTheExpiryBySource(t *testing.T) {
	exp := time.Unix(1791549000, 0).UTC().Format(time.RFC3339)
	fromVariable := &runCredential{variable: unsignedCredential(`{"exp":1791549000}`)}
	fromVariable.get(context.Background())
	fromFD := &runCredential{fd: streamOf(unsignedCredential(`{"exp":1791549000}`))}
	fromFD.get(context.Background())
	unread := &runCredential{variable: "no-exp"}
	unread.get(context.Background())
	res := &session.Result{RunClosed: true, ClosedReason: event.ReasonCredentialExpired, State: "cancelled", Reason: event.ReasonCredentialExpired, ExitCode: -1}
	for _, c := range []struct {
		credential *runCredential
		want       string
	}{
		{fromVariable, "the run was cancelled: the run credential expired at " + exp + "; QORY_RUN_CREDENTIAL_SECRET is read once, so a run longer than its credential needs session.gateway.run_credential_file or --run-credential-fd\n"},
		{fromFD, "the run was cancelled: the run credential expired at " + exp + ", and the descriptor gave no fresh one\n"},
		{unread, "the run was cancelled: the run credential expired, and claude was stopped\n"},
	} {
		said, code := endOf(res, runEnd{runtime: "claude", credential: c.credential})
		if said != c.want || code != 1 {
			t.Errorf("said %q, exit %d; want %q, exit 1", said, code, c.want)
		}
	}
}

// TestGatewayEndedResendSaysTheOutcome is qory run resend's one line of a run that had
// ended at the gateway: its outcome, and its reason in the words of qory run, or the
// starter's with spaces; a run whose end nothing says keeps the line that names the
// gateway's code.
func TestGatewayEndedResendSaysTheOutcome(t *testing.T) {
	const tail = ", so no more of its events are taken; they stay in runs/x"
	for _, c := range []struct {
		res  session.ResendResult
		want string
	}{
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonStopped, State: "cancelled", Reason: event.ReasonStopped}, "the run has ended (cancelled)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonStopped, State: "failed", Reason: event.ReasonStopped}, "the run has ended (cancelled)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonStopped, State: "succeeded", Reason: "all_checks_passed"}, "the run has ended (completed: all checks passed)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonStopped, State: "failed"}, "the run has ended (failed)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonSessionLost, State: "failed", Reason: event.ReasonSessionLost}, "the run has ended (lost: it lost contact with the gateway for 1m30s)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonBatchRefused, State: "failed", Reason: event.ReasonBatchRefused}, "the run has ended (failed: its events could not be recorded)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonCredentialExpired, State: "failed", Reason: event.ReasonCredentialExpired}, "the run has ended (cancelled: the run credential expired)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonCredentialCheckUnreachable, State: "failed", Reason: event.ReasonCredentialCheckUnreachable}, "the run has ended (failed: its run credential could not be checked)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonRunClosed, State: "succeeded"}, "the run has ended (completed)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonRunClosed, State: "cancelled", Reason: event.ReasonTimeout}, "the run has ended (cancelled)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonRunClosed, State: "failed", Reason: event.ReasonRunClosed}, "the run has ended (failed: the gateway stopped during the run)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonRunClosed, State: "failed", Reason: "run_ended_at_issuer"}, "the run has ended (cancelled)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonRunClosed, State: "failed", Reason: "issuer_unreachable"}, "the run has ended (failed: its run credential could not be checked)" + tail},
		{session.ResendResult{RunClosed: true, ClosedReason: event.ReasonRunClosed}, "the gateway ended the run with the reason run_closed, so it takes no more of this run's events; they stay in runs/x"},
	} {
		if got := gatewayEndedResend(c.res, "runs/x").Error(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.res, got, c.want)
		}
	}
}

// TestSignalContextNamesTheSignal is qory run's context, which ends at SIGTERM and says
// so, and says nothing before a signal or after one it no longer watches.
func TestSignalContextNamesTheSignal(t *testing.T) {
	ctx, got, stop := signalContext(context.Background())
	if got() != "" {
		t.Errorf("before a signal: %q", got())
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not end the context")
	}
	if got() != "SIGTERM" {
		t.Errorf("after SIGTERM: %q, want SIGTERM", got())
	}
	stop()
	stop()
	ctx, got, stop = signalContext(context.Background())
	stop()
	if ctx.Err() == nil || got() != "" {
		t.Errorf("stopped with no signal: %v, %q", ctx.Err(), got())
	}
	if signalName(os.Interrupt) != "SIGINT" {
		t.Errorf("os.Interrupt is %q, want SIGINT", signalName(os.Interrupt))
	}
}
