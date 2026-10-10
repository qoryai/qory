package cmd_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
)

// The lines qory run says of a separate gateway that could not open a run because its
// run credential could not be checked: the introspection endpoint could not be reached,
// or gave no valid answer.
const (
	openUnreachable = "qory run: the run did not start: its run credential could not be checked; try again\n"
	openInvalid     = "qory run: the run did not start: its run credential could not be checked\n"
)

// TestRunBehindAGatewayWhoseCredentialCannotBeChecked is qory gateway on a loopback port
// whose introspection endpoint refuses every connection: the gateway tries it and
// answers the run request with its 503 credential_check_unreachable, and qory says so
// in a line of its own, then the record, exit 1. The runtime never runs.
func TestRunBehindAGatewayWhoseCredentialCannotBeChecked(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, credentialRuntime(t, filepath.Join(t.TempDir(), "forbidden")))
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://" + closed.Addr().String() + "/introspect"
	closed.Close()
	credentials := gatewayCredentials + "      introspection:\n        url: " + endpoint + "\n        client_id: example-gateway\n        client_secret_file: introspection-secret\n"
	addr, ca, iss, gwOut, _ := separateGatewayWith(t, credentials, func(dir string) {
		writeRunCredential(t, filepath.Join(dir, "introspection-secret"), issuerSecret+"\n")
	})
	writeFile(t, foragerFile(), sessionGateway(addr, ca, ""))
	credential := iss.credential(t, nil)
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)
	out, err := run(t, "run")
	if cmd.ExitCode(err) != 1 || !strings.Contains(out, "\n"+openUnreachable+"qory run: the record is in ") {
		t.Fatalf("an endpoint out of reach: %v (exit %d), want %q, exit 1\n%s\n%s", err, cmd.ExitCode(err), openUnreachable, out, gwOut)
	}
	if n := strings.Count(out, "the run did not start"); n != 1 {
		t.Errorf("the line is said %d times, want once\n%s", n, out)
	}
	lacks(t, out, "✗", "introspection endpoint", "issuer", "runtime with", credential)
}

// endingLink is a fake separate gateway for a run whose runtime waits: its run answer
// opens the run with the run secret runSecret, and once the runtime has started,
// started, every batch is answered as the case says.
func endingLink(t *testing.T, started string) (*fakeLink, string) {
	t.Helper()
	link := newFakeLink(t)
	link.interval = 1
	writeFile(t, foragerFile(), sessionGateway(strings.TrimPrefix(link.URL, "https://"), link.ca, ""))
	return link, `{"version":1,"run_id":"{run_id}","credential":"starter","labels":{"forge":"git.example.com","repository":"acme/app"},"applied":{"mode":"observe","allow":[],"source":"none"},"proxy_secret":"example-proxy-secret-000000000000000000001","run_secret":"` + runSecret + `"}`
}

// runSecret is the run secret endingLink's run answer gives.
const runSecret = "example-run-secret-0000000000000000000001"

// TestRunBehindAGatewaySaysHowTheRunEnded is each answer of a separate gateway that ends
// a run or keeps it from starting, as qory says it, with qory run's exit status: at the
// run request, its 503 credential_check_unreachable and its 502 credential_check_invalid,
// and its 410 of a run that has ended already, the runtime never run; once the runtime
// runs, its 410 with the run's outcome and reason, the runtime stopped: a run credential
// that could not be checked, failed; the starter's end with no outcome, cancelled; and
// the starter's outcome with its reason as given, with spaces, exit 0 when it completed.
// No line says "issuer", the code or the status, nor the gateway's own message of the
// run's end, "the run has ended: <state>[, <reason words>]", which qory's line says.
func TestRunBehindAGatewaySaysHowTheRunEnded(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	started := filepath.Join(t.TempDir(), "started")
	runtime := filepath.Join(t.TempDir(), "patient-runtime")
	writeFile(t, runtime, "#!/bin/sh\necho 'runtime started'\ntouch '"+started+"'\nexec sleep 30\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	link, answer := endingLink(t, started)
	credential := "opaque-run-credential-" + fmt.Sprint(time.Now().UnixNano())
	const stopped = "and claude was stopped\n"
	for _, c := range []struct {
		name      string
		runStatus int
		runBody   string
		end       string
		want      string
		code      int
		started   bool
	}{
		{name: "unreachable at the open", runStatus: http.StatusServiceUnavailable, want: openUnreachable, code: 1,
			runBody: `{"error":"credential_check_unreachable","from":"gateway","message":"the gateway could not open the run: the introspection endpoint could not be reached; try again"}`},
		{name: "no valid answer at the open", runStatus: http.StatusBadGateway, want: openInvalid, code: 1,
			runBody: `{"error":"credential_check_invalid","from":"gateway","message":"the gateway could not open the run: the introspection endpoint gave no valid answer"}`},
		{name: "ended before it started", runStatus: http.StatusGone, want: "✗ the run did not start: it has ended already\n", code: 1,
			runBody: `{"error":"stopped","from":"gateway","message":"the run has ended: cancelled, no outcome given","state":"cancelled","reason":"stopped"}`},
		{name: "unreachable while live", runStatus: http.StatusOK, runBody: answer, started: true, code: 1,
			end:  `{"error":"credential_check_unreachable","from":"gateway","message":"the run has ended"}`,
			want: "✗ the run failed: its run credential could not be checked, " + stopped},
		{name: "no valid answer while live", runStatus: http.StatusOK, runBody: answer, started: true, code: 1,
			end:  `{"error":"credential_check_invalid","from":"gateway","message":"the run has ended: failed, couldn't check whether the run may go on: unreadable answer","state":"failed","reason":"credential_check_invalid"}`,
			want: "✗ the run failed: its run credential could not be checked, " + stopped},
		{name: "stopped with no outcome", runStatus: http.StatusOK, runBody: answer, started: true, code: 1,
			end:  `{"error":"stopped","from":"gateway","message":"the run has ended: cancelled, no outcome given","state":"cancelled","reason":"stopped"}`,
			want: "✗ the run was cancelled, with no outcome given, " + stopped},
		{name: "the starter's success", runStatus: http.StatusOK, runBody: answer, started: true, code: 0,
			end:  `{"error":"stopped","from":"gateway","message":"the run has ended: succeeded, all checks passed","state":"succeeded","reason":"all_checks_passed"}`,
			want: "✓ the run completed: all checks passed, " + stopped},
		{name: "the starter's failure", runStatus: http.StatusOK, runBody: answer, started: true, code: 1,
			end:  `{"error":"stopped","from":"gateway","message":"the run has ended: failed, checks failed","state":"failed","reason":"checks_failed"}`,
			want: "✗ the run failed: checks failed, " + stopped},
		{name: "the starter's cancel", runStatus: http.StatusOK, runBody: answer, started: true, code: 1,
			end:  `{"error":"stopped","from":"gateway","message":"the run has ended: cancelled, no longer needed","state":"cancelled","reason":"no_longer_needed"}`,
			want: "✗ the run was cancelled: no longer needed, " + stopped},
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
		if cmd.ExitCode(err) != c.code || !strings.Contains(out, "\n"+c.want+"qory run: the record is in ") {
			t.Errorf("%s: %v (exit %d), want %q, exit %d\n%s", c.name, err, cmd.ExitCode(err), c.want, c.code, out)
		}
		if n := strings.Count(out, strings.TrimSuffix(c.want, "\n")); n != 1 {
			t.Errorf("%s: the line is said %d times, want once\n%s", c.name, n, out)
		}
		lacks(t, out, "issuer", "introspection endpoint", "credential_check", "all_checks_passed", "checks_failed", "no_longer_needed", "(status", "ended by", "closed the run", "the run has ended", "succeeded", credential)
		if !c.started {
			lacks(t, out, "✓")
			if c.runStatus != http.StatusGone {
				lacks(t, out, "✗")
			}
		}
		if got := strings.Contains(out, "runtime started"); got != c.started {
			t.Errorf("%s: the runtime started: %v, want %v\n%s", c.name, got, c.started, out)
		}
		if c.started {
			link.wantSecret(t, c.name, runSecret)
		} else {
			link.wantNoBatch(t, c.name)
		}
	}
}

// exitedOf is the state and the reason of the run.exited in the session's record of the
// one run of the checkout root, and its exit code.
func exitedOf(t *testing.T, root string) (state, reason string, exitCode float64) {
	t.Helper()
	runs, err := os.ReadDir(runsDir(t, root))
	if err != nil || len(runs) != 1 {
		t.Fatalf("the runs: %v, %v", runs, err)
	}
	b, err := os.ReadFile(filepath.Join(runsDir(t, root), runs[0].Name(), "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				State    string  `json:"state"`
				Reason   string  `json:"reason"`
				ExitCode float64 `json:"exit_code"`
			} `json:"data"`
		}
		if json.Unmarshal(s.Bytes(), &ev) == nil && ev.Type == "dev.qory.run.exited" {
			return ev.Data.State, ev.Data.Reason, ev.Data.ExitCode
		}
	}
	t.Fatalf("no run.exited in the record:\n%s", b)
	return "", "", 0
}

// TestRunBehindAGatewaySaysTheStartersOutcomeAtTheExit is a runtime that exits by
// itself behind a separate gateway, whose session asks the gateway once for the outcome
// the run's starter gave. With none, or one that agrees with the exit, qory says the
// exit alone and exits with its status. With one that differs, a second line says the
// outcome, its reason as given with spaces, and qory's exit status follows it: 0 when
// the run completed, 1 otherwise. The record keeps the runtime's exit code and the
// outcome as its state.
func TestRunBehindAGatewaySaysTheStartersOutcomeAtTheExit(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	code := filepath.Join(t.TempDir(), "exit-code")
	runtime := filepath.Join(t.TempDir(), "exiting-runtime")
	writeFile(t, runtime, "#!/bin/sh\nexit $(cat '"+code+"')\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	link, answer := endingLink(t, "")
	link.answer(http.StatusOK, "")
	credential := "opaque-run-credential-" + fmt.Sprint(time.Now().UnixNano())
	for _, c := range []struct {
		name, outcome   string
		exit            int
		want            string
		code            int
		state, reason   string
		lacksSecondLine bool
	}{
		{name: "exit 0, no outcome", exit: 0, want: "✓ claude exited 0\n", code: 0, state: "succeeded", lacksSecondLine: true},
		{name: "exit 3, no outcome", exit: 3, want: "✗ claude exited 3\n", code: 3, state: "failed", lacksSecondLine: true},
		{name: "exit 0, the starter agrees", exit: 0, outcome: `{"state":"succeeded","reason":"all_checks_passed"}`,
			want: "✓ claude exited 0\n", code: 0, state: "succeeded", reason: "all_checks_passed", lacksSecondLine: true},
		{name: "exit 0, the starter's failure", exit: 0, outcome: `{"state":"failed","reason":"checks_failed"}`,
			want: "✓ claude exited 0\n✗ the run failed: checks failed\n", code: 1, state: "failed", reason: "checks_failed"},
		{name: "exit 0, the starter's cancel", exit: 0, outcome: `{"state":"cancelled"}`,
			want: "✓ claude exited 0\n✗ the run was cancelled\n", code: 1, state: "cancelled"},
		{name: "exit 3, the starter's success", exit: 3, outcome: `{"state":"succeeded","reason":"all_checks_passed"}`,
			want: "✗ claude exited 3\n✓ the run completed: all checks passed\n", code: 0, state: "succeeded", reason: "all_checks_passed"},
	} {
		clearRuns(t, root)
		writeFile(t, code, fmt.Sprint(c.exit))
		link.mu.Lock()
		link.runStatus, link.runBody, link.outcome, link.asked = http.StatusOK, answer, c.outcome, 0
		link.mu.Unlock()
		t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)
		out, err := run(t, "run")
		if cmd.ExitCode(err) != c.code || !strings.Contains(out, "\n"+c.want+"qory run: the record is in ") {
			t.Errorf("%s: %v (exit %d), want %q, exit %d\n%s", c.name, err, cmd.ExitCode(err), c.want, c.code, out)
		}
		if c.lacksSecondLine {
			lacks(t, out, "the run completed", "the run failed", "the run was")
		}
		lacks(t, out, "issuer", "succeeded", "all_checks_passed", "checks_failed", credential)
		link.mu.Lock()
		asked := link.asked
		link.mu.Unlock()
		if asked != 1 {
			t.Errorf("%s: the outcome was asked for %d times, want once", c.name, asked)
		}
		if state, reason, exit := exitedOf(t, root); state != c.state || reason != c.reason || exit != float64(c.exit) {
			t.Errorf("%s: the record's run.exited is %s, %q, exit %v; want %s, %q, exit %d", c.name, state, reason, exit, c.state, c.reason, c.exit)
		}
	}
}

// TestRunSaysTheExitOfARuntimeThatExitedBeforeASignal is a runtime that exits 0 by
// itself, with a gateway of the run's own, and SIGINT to qory run once the server has the
// run's dev.qory.run.exited: after the runtime exited, while the gateway delivers the
// run's last events. The signal did not end the run: qory says the runtime's exit, exit
// 0, and not that the run was cancelled.
func TestRunSaysTheExitOfARuntimeThatExitedBeforeASignal(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	srv := newFakeServer(t, "")
	serverFile(t, srv, "")
	var once sync.Once
	srv.mu.Lock()
	srv.onExited = func() {
		once.Do(func() {
			// session.Run has returned well before this: the session's record is
			// closed once its gateway has the event, and the server has it after.
			time.Sleep(time.Second)
			syscall.Kill(os.Getpid(), syscall.SIGINT)
			time.Sleep(300 * time.Millisecond)
		})
	}
	srv.mu.Unlock()
	t.Setenv("QORY_TEST_EXIT", "0")
	out, err := run(t, "run")
	if err != nil || !strings.Contains(out, "✓ claude exited 0\n") {
		t.Errorf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "cancelled", "qory run got")
	if state, _, exit := exitedOf(t, root); state != "succeeded" || exit != 0 {
		t.Errorf("the record's run.exited is %s, exit %v", state, exit)
	}
}

// TestRunSaysTheExitOfARuntimeThatExitedBeforeTheOutcomeAsk is a runtime that exits 0
// by itself behind a separate gateway, and SIGINT to qory run while the session asks the
// gateway for the outcome at that exit: after the runtime exited. The signal did not end
// the run: qory says the runtime's exit, exit 0, and not that the run was cancelled.
func TestRunSaysTheExitOfARuntimeThatExitedBeforeTheOutcomeAsk(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	runtime := filepath.Join(t.TempDir(), "exiting-runtime")
	writeFile(t, runtime, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	link, answer := endingLink(t, "")
	link.answer(http.StatusOK, "")
	link.mu.Lock()
	link.runStatus, link.runBody = http.StatusOK, answer
	link.onAsk = func() {
		syscall.Kill(os.Getpid(), syscall.SIGINT)
		time.Sleep(300 * time.Millisecond)
	}
	link.mu.Unlock()
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", "opaque-run-credential-"+fmt.Sprint(time.Now().UnixNano()))
	out, err := run(t, "run")
	if err != nil || !strings.Contains(out, "\n✓ claude exited 0\nqory run: the record is in ") {
		t.Errorf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "cancelled", "qory run got")
	link.mu.Lock()
	asked := link.asked
	link.mu.Unlock()
	if asked != 1 {
		t.Errorf("the outcome was asked for %d times, want once", asked)
	}
	if state, _, exit := exitedOf(t, root); state != "succeeded" || exit != 0 {
		t.Errorf("the record's run.exited is %s, exit %v", state, exit)
	}
}

// TestRunSaysASignalThatStoppedTheRuntime is SIGINT to qory run while the runtime runs,
// with a gateway of the run's own: the signal ended the run, and qory says it was
// cancelled, names the signal, exit 1.
func TestRunSaysASignalThatStoppedTheRuntime(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	started := filepath.Join(t.TempDir(), "started")
	runtime := filepath.Join(t.TempDir(), "waiting-runtime")
	writeFile(t, runtime, "#!/bin/sh\ntouch '"+started+"'\nexec sleep 30\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	srv := newFakeServer(t, "")
	serverFile(t, srv, "")
	go func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				syscall.Kill(os.Getpid(), syscall.SIGINT)
				return
			}
		}
	}()
	out, err := run(t, "run")
	if cmd.ExitCode(err) != 1 || !strings.Contains(out, "\n✗ the run was cancelled: qory run got SIGINT, and claude was stopped\n") {
		t.Errorf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "exited 0")
	if state, reason, _ := exitedOf(t, root); state != "cancelled" || reason != "interrupted" {
		t.Errorf("the record's run.exited is %s, %q", state, reason)
	}
}

// TestRunSaysTheStartersOutcomeAfterASignalInTheOutcomeAsk is a runtime that exits 0 by
// itself behind a separate gateway whose starter says the run failed, and SIGINT to qory
// run while the session asks for that outcome. The signal came after the exit and cuts
// neither the ask nor the record: qory says the exit and the starter's outcome, exit 1.
func TestRunSaysTheStartersOutcomeAfterASignalInTheOutcomeAsk(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	runtime := filepath.Join(t.TempDir(), "exiting-runtime")
	writeFile(t, runtime, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	link, answer := endingLink(t, "")
	link.answer(http.StatusOK, "")
	link.mu.Lock()
	link.runStatus, link.runBody = http.StatusOK, answer
	link.outcome = `{"state":"failed","reason":"checks_failed"}`
	link.onAsk = func() {
		syscall.Kill(os.Getpid(), syscall.SIGINT)
		time.Sleep(300 * time.Millisecond)
	}
	link.mu.Unlock()
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", "opaque-run-credential-"+fmt.Sprint(time.Now().UnixNano()))
	out, err := run(t, "run")
	if cmd.ExitCode(err) != 1 || !strings.Contains(out, "\n✓ claude exited 0\n✗ the run failed: checks failed\nqory run: the record is in ") {
		t.Errorf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "cancelled", "qory run got", "checks_failed")
	if state, reason, exit := exitedOf(t, root); state != "failed" || reason != "checks_failed" || exit != 0 {
		t.Errorf("the record's run.exited is %s, %q, exit %v", state, reason, exit)
	}
}

// TestRunSaysASignalThatStoppedARuntimeThatExited0 is SIGINT to qory run while the
// runtime runs, and a runtime that exits 0 at the stop: the signal ended the run, so qory
// says it was cancelled, exit 1, and the record says cancelled, interrupted, exit 0.
func TestRunSaysASignalThatStoppedARuntimeThatExited0(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	started := filepath.Join(t.TempDir(), "started")
	runtime := filepath.Join(t.TempDir(), "stopping-runtime")
	writeFile(t, runtime, "#!/bin/sh\ntrap 'exit 0' INT TERM\ntouch '"+started+"'\nwhile :; do sleep 0.1; done\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	srv := newFakeServer(t, "")
	serverFile(t, srv, "")
	go func() {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				syscall.Kill(os.Getpid(), syscall.SIGINT)
				return
			}
		}
	}()
	out, err := run(t, "run")
	if cmd.ExitCode(err) != 1 || !strings.Contains(out, "\n✗ the run was cancelled: qory run got SIGINT, and claude was stopped\nqory run: the record is in ") {
		t.Errorf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, out, "exited 0", "interrupted", "context canceled")
	if state, reason, exit := exitedOf(t, root); state != "cancelled" || reason != "interrupted" || exit != 0 {
		t.Errorf("the record's run.exited is %s, %q, exit %v", state, reason, exit)
	}
}
