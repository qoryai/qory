package cmd_test

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
)

// The lines qory run says of a separate gateway that could not open a run, or ended
// one, because of the run credential's issuer: its introspection endpoint could not be
// reached, or gave no valid answer.
const (
	openUnreachable = "qory run: the gateway could not open the run: it could not reach the run credential's issuer; try again\n"
	openInvalid     = "qory run: the gateway could not open the run: the run credential's issuer gave the gateway no valid answer\n"
	endUnreachable  = "qory run: the gateway ended the run: it could not reach the run credential's issuer\n"
	endInvalid      = "qory run: the gateway ended the run: the run credential's issuer gave the gateway no valid answer\n"
)

// TestRunBehindAGatewayWhoseIssuerCannotBeReached is qory gateway on a loopback port
// whose issuer's introspection endpoint refuses every connection: the gateway tries it
// and answers the run request with its 503 issuer_unreachable, and qory says so in a
// line of its own, then the record, exit 1. The runtime never runs.
func TestRunBehindAGatewayWhoseIssuerCannotBeReached(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, credentialRuntime(t, filepath.Join(t.TempDir(), "forbidden")))
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://" + closed.Addr().String() + "/introspect"
	closed.Close()
	credentials := gatewayCredentials + "      introspection:\n        url: " + endpoint + "\n        client_id: example-gateway\n        client_secret_file: issuer-introspection-secret\n"
	addr, ca, iss, gwOut, _ := separateGatewayWith(t, credentials, func(dir string) {
		writeRunCredential(t, filepath.Join(dir, "issuer-introspection-secret"), issuerSecret+"\n")
	})
	writeFile(t, foragerFile(), sessionGateway(addr, ca, ""))
	credential := iss.credential(t, nil)
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)
	out, err := run(t, "run")
	if cmd.ExitCode(err) != 1 || !strings.Contains(out, "\n"+openUnreachable+"qory run: the record is in ") {
		t.Fatalf("an issuer out of reach: %v (exit %d), want %q, exit 1\n%s\n%s", err, cmd.ExitCode(err), openUnreachable, out, gwOut)
	}
	if n := strings.Count(out, "qory run: the gateway could not open the run"); n != 1 {
		t.Errorf("the line is said %d times, want once\n%s", n, out)
	}
	lacks(t, out, "✗", "introspection endpoint", "runtime with", credential)
}

// TestRunBehindAGatewaySaysTheIssuersFailures is each answer of a separate gateway that
// says the run credential's issuer failed it, as qory says it, exit 1: at the run
// request its 503 issuer_unreachable and its 502 issuer_answer_invalid, the runtime
// never run; and once the runtime runs, its 410 with either code, the runtime stopped.
// The gateway's own message is not said, nor is a refusal's code.
func TestRunBehindAGatewaySaysTheIssuersFailures(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	started := filepath.Join(t.TempDir(), "started")
	runtime := filepath.Join(t.TempDir(), "patient-runtime")
	writeFile(t, runtime, "#!/bin/sh\necho 'runtime started'\ntouch '"+started+"'\nexec sleep 30\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	link := newFakeLink(t)
	link.interval = 1
	writeFile(t, foragerFile(), sessionGateway(strings.TrimPrefix(link.URL, "https://"), link.ca, ""))
	credential := "opaque-run-credential-" + fmt.Sprint(time.Now().UnixNano())
	const answer = `{"version":1,"run_id":"{run_id}","credential":"issuer","labels":{"forge":"git.example.com","repository":"acme/app"},"applied":{"mode":"observe","allow":[],"source":"none"},"proxy_secret":"example-proxy-secret-000000000000000000001"}`
	for _, c := range []struct {
		name      string
		runStatus int
		runBody   string
		end       string
		want      string
		started   bool
	}{
		{name: "unreachable at the open", runStatus: http.StatusServiceUnavailable, want: openUnreachable,
			runBody: `{"error":"issuer_unreachable","from":"gateway","message":"the gateway could not open the run: the issuer's introspection endpoint could not be reached; try again"}`},
		{name: "no valid answer at the open", runStatus: http.StatusBadGateway, want: openInvalid,
			runBody: `{"error":"issuer_answer_invalid","from":"gateway","message":"the gateway could not open the run: the issuer's introspection endpoint gave no valid answer"}`},
		{name: "unreachable while live", runStatus: http.StatusOK, runBody: answer, want: endUnreachable, started: true,
			end: `{"error":"issuer_unreachable","from":"gateway"}`},
		{name: "no valid answer while live", runStatus: http.StatusOK, runBody: answer, want: endInvalid, started: true,
			end: `{"error":"issuer_answer_invalid","from":"gateway"}`},
	} {
		clearRuns(t, root)
		os.Remove(started)
		link.answer(http.StatusGone, c.end)
		link.mu.Lock()
		link.runStatus, link.runBody, link.after = c.runStatus, c.runBody, started
		link.mu.Unlock()
		// qory takes the run credential out of its environment as it starts.
		t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)
		out, err := run(t, "run")
		if cmd.ExitCode(err) != 1 || !strings.Contains(out, "\n"+c.want) {
			t.Errorf("%s: %v (exit %d), want %q, exit 1\n%s", c.name, err, cmd.ExitCode(err), c.want, out)
		}
		wants(t, out, c.want+"qory run: the record is in ")
		if n := strings.Count(out, "qory run: the gateway "); n != 1 {
			t.Errorf("%s: the gateway is said %d times, want once\n%s", c.name, n, out)
		}
		lacks(t, out, "✗", "introspection endpoint", "issuer_unreachable", "issuer_answer_invalid", credential)
		if got := strings.Contains(out, "runtime started"); got != c.started {
			t.Errorf("%s: the runtime started: %v, want %v\n%s", c.name, got, c.started, out)
		}
	}
}
