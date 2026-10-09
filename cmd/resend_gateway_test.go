package cmd_test

import (
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/ui"
)

// The texts qory run resend says behind a gateway, the run directory shown as %s.
const (
	resendExpired  = "the run credential expired at %s, so the gateway takes no more of this run's events; they stay in %s"
	resendIssuer   = "the run credential's issuer reports that the run has ended, so the gateway takes no more of this run's events; they stay in %s"
	resendEnded    = "the gateway ended the run with the reason %s, so it takes no more of this run's events; they stay in %s"
	resendRefused  = "the gateway refused this run credential"
	resendDiffers  = "the gateway refused this run credential: it differs from the one the run started with"
	resendSent     = "%d events were accepted; nothing is left to send to the gateway"
	resendUnopened = "the gateway never opened run %s, so there is nothing to send; its record stays in %s"
	resendNotSent  = "%d events were accepted and %d were not; %s/undelivered contains them"
	resendOwnLocal = "the run %s ran with a gateway of its own on this machine, so its record goes to the server, not through session.gateway: resend it with a forager.yaml that defines the server and no session.gateway"
)

// failed is everything a command said, as a person reads it: its output, and its error
// when the command did not print it itself.
func failed(out string, err error) string {
	if err != nil && !errors.Is(err, cmd.ErrReported) {
		return out + err.Error()
	}
	return out
}

// TestResendThroughASeparateGateway is qory run resend on a machine whose runs go
// through the gateway session.gateway names, here qory gateway on a loopback port. The
// run's run credential file goes away after the run opened, so the gateway does not get
// the rest of the session's record, which stays in the run directory. The resend then
// presents the run credential as qory run does: an expired one, one the gateway does not
// take, and ones that differ from the one the run started with are each refused in their
// words, exit 1, from the file, the variable or the descriptor; the run's own sends the
// rest, and a resend after it has nothing left to send. The gateway completes the run
// toward the server. The run credential is never in the output, an error or the record.
func TestResendThroughASeparateGateway(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	file := filepath.Join(t.TempDir(), "run-credential")
	// The runtime takes the run credential's file away once the run has opened: the
	// session's later batches cannot be sent.
	runtime := filepath.Join(t.TempDir(), "offline-runtime")
	writeFile(t, runtime, "#!/bin/sh\nsleep 1\nrm -f "+file+"\nsleep 1\necho done\nexit 0\n")
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, runtime)
	addr, ca, iss, gwOut, srv := separateGateway(t)
	writeFile(t, foragerFile(), sessionGateway(addr, ca, "    run_credential_file: "+file+"\n"))
	const runKey = "queue/resend-1"
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e80"
	good := iss.credential(t, map[string]any{"sub": runKey})
	writeRunCredential(t, file, good+"\n")
	if out, err := run(t, "run", "--run-id", id); err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, gwOut)
	}
	dir := filepath.Join(runsDir(t, root), id)
	if spooled, _ := os.ReadDir(filepath.Join(dir, "undelivered")); len(spooled) == 0 {
		t.Fatalf("the run left nothing under undelivered/\n%s", gwOut)
	}
	// The gateway's run secret, which the run directory keeps while events are owed.
	secret := keptSecret(t, dir)
	shown := ui.Short(dir, root)

	exp := time.Now().Add(-time.Hour).Unix()
	expired := iss.credential(t, map[string]any{"sub": runKey, "iat": time.Now().Add(-2 * time.Hour).Unix(), "exp": exp})
	audience := iss.credential(t, map[string]any{"sub": runKey, "aud": "another-service"})
	requester := iss.credential(t, map[string]any{"sub": runKey, "requester": "someone-else"})
	repository := iss.credential(t, map[string]any{"sub": runKey, "project": "other"})
	sent := []string{good, expired, audience, requester, repository}
	for _, c := range []struct {
		name, file, env, fd, want string
	}{
		{name: "an expired run credential", file: expired, want: fmt.Sprintf(resendExpired, time.Unix(exp, 0).UTC().Format(time.RFC3339), shown)},
		{name: "a run credential the gateway does not take", file: good, env: audience, want: resendRefused},
		{name: "another requester", file: good, fd: requester, want: resendDiffers},
		{name: "another repository", file: good, fd: repository, want: resendDiffers},
	} {
		writeRunCredential(t, file, c.file+"\n")
		t.Setenv("QORY_RUN_CREDENTIAL_SECRET", c.env)
		args := []string{"run", "resend", id}
		if c.fd != "" {
			args = append(args, "--run-credential-fd", descriptor(t, c.fd))
		}
		out, err := run(t, args...)
		if err == nil || cmd.ExitCode(err) != 1 || err.Error() != c.want {
			t.Errorf("%s: %v (exit %d), want %q\n%s", c.name, err, cmd.ExitCode(err), c.want, out)
		}
		lacks(t, failed(out, err), sent...)
		noSecretIn(t, c.name+": the output", failed(out, err), secret)
		if spooled, _ := os.ReadDir(filepath.Join(dir, "undelivered")); len(spooled) == 0 {
			t.Errorf("%s: the events are no longer under undelivered/", c.name)
		}
	}

	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", "")
	writeRunCredential(t, file, good+"\n")
	out, err := run(t, "run", "resend", id)
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, out, gwOut)
	}
	lacks(t, out, sent...)
	noSecretIn(t, "the resend's output", out, secret)
	if !strings.Contains(out, " events were accepted; nothing is left to send to the gateway") || strings.Contains(out, " 0 events were accepted") {
		t.Errorf("the run's own run credential:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "undelivered")); err == nil {
		t.Error("undelivered/ is left after the gateway took everything")
	}
	if _, err := os.Stat(filepath.Join(dir, "run-secret")); err == nil {
		t.Error("run-secret is left after the gateway took everything")
	}
	if out, err := run(t, "run", "resend", id); err != nil || !strings.Contains(out, fmt.Sprintf(resendSent, 0)) {
		t.Errorf("a resend with nothing left to send: %v\n%s", err, out)
	}
	// The gateway now has the session's run.exited, and its record reaches the server: the
	// one run of this test's gateway.
	var exited []map[string]any
	for deadline := time.Now().Add(10 * time.Second); len(exited) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		exited = srv.byType()["dev.qory.run.exited"]
	}
	if len(exited) != 1 || exited[0]["state"] != "succeeded" || exited[0]["reason"] != nil {
		t.Errorf("the server has the run.exited %v\n%s", exited, gwOut)
	}
	lacks(t, gwOut.String(), sent...)
	noSecretIn(t, "the gateway's output", gwOut.String(), secret)
	noCredentialUnder(t, os.Getenv("XDG_STATE_HOME"), good)
	noRunSecretUnder(t, os.Getenv("XDG_STATE_HOME"), secret)
}

// fakeLink is a separate gateway's link as qory reaches it, over TLS 1.3 on loopback: its
// discovery, and its answer to every batch of events, status and body. It records the
// run credential of each request, the run secret of each batch and how many batches it
// got.
type fakeLink struct {
	*httptest.Server
	// ca is the file of its certificate, for session.gateway.ca_file.
	ca string

	mu sync.Mutex
	// discovery, when not 0, is the status the discovery is refused with,
	// run_credential_refused.
	discovery int
	status    int
	body      string
	bearers   []string
	batches   int
	// secrets are the X-Qory-Run-Secret values of the batches, in order, joined by a
	// comma when one carried several, empty for none.
	secrets []string
	// interval is the heartbeat interval its discovery announces, in seconds: 30 when 0.
	interval int
	// runStatus and runBody, when runStatus is not 0, answer the run request, the
	// request's run id in place of {run_id}; without them it is not found.
	runStatus int
	runBody   string
	// after, when set, is a file: until it exists every batch is accepted, and status
	// and body answer it once it does.
	after string
}

// newFakeLink starts a fake link that accepts every batch; it stops when the test ends.
func newFakeLink(t *testing.T) *fakeLink {
	t.Helper()
	f := &fakeLink{status: http.StatusOK}
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	f.StartTLS()
	t.Cleanup(f.Close)
	f.ca = filepath.Join(t.TempDir(), "link-ca.pem")
	writeFile(t, f.ca, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.Certificate().Raw})))
	return f
}

// answer sets the fake's answer to every batch from now on.
func (f *fakeLink) answer(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body, f.batches, f.secrets = status, body, 0, nil
}

// wantSecret fails the test unless every batch the fake got since its last answer
// carried the run secret want, once.
func (f *fakeLink) wantSecret(t *testing.T, name, want string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.secrets {
		if s != want {
			t.Errorf("%s: batch %d did not carry the run's secret, once", name, i+1)
		}
	}
}

func (f *fakeLink) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bearers = append(f.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/qory-configuration":
		if f.discovery != 0 {
			w.WriteHeader(f.discovery)
			w.Write([]byte(`{"error":"run_credential_refused","from":"gateway","message":"the gateway refused this run credential"}`))
			return
		}
		interval := f.interval
		if interval == 0 {
			interval = 30
		}
		json.NewEncoder(w).Encode(map[string]any{
			"version": 1,
			"events":  map[string]any{"url": f.URL + "/events", "types": []string{"*"}, "interval_seconds": interval},
			"run":     map[string]any{"url": f.URL + "/run"},
			"proxy":   map[string]any{"address": strings.TrimPrefix(f.URL, "https://")},
		})
	case "/run":
		if f.runStatus == 0 || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req struct {
			RunID string `json:"run_id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(f.runStatus)
		w.Write([]byte(strings.ReplaceAll(f.runBody, "{run_id}", req.RunID)))
	case "/events":
		f.batches++
		f.secrets = append(f.secrets, strings.Join(r.Header.Values("X-Qory-Run-Secret"), ","))
		if f.after != "" {
			if _, err := os.Stat(f.after); err != nil {
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		w.WriteHeader(f.status)
		w.Write([]byte(f.body))
	default:
		http.NotFound(w, r)
	}
}

// recordedSecret is the run secret a record gatewayRecord writes keeps.
const recordedSecret = "example-recorded-run-secret-00000000001"

// gatewayRecord writes the record a session behind a separate gateway leaves of run id:
// two heartbeats in session.jsonl, a delivered.log that names neither, so both are
// owed, and the run's secret, recordedSecret, in run-secret. It returns the run
// directory.
func gatewayRecord(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(runsDir(t, root), id)
	var lines strings.Builder
	for i := 1; i <= 2; i++ {
		fmt.Fprintf(&lines, `{"specversion":"1.0","id":"%s-%d","source":"/qory/forager","type":"dev.qory.run.heartbeat","time":"2026-10-09T12:00:0%dZ","sequence":"%d","data":{}}`+"\n", id, i, i, i)
	}
	writeFile(t, filepath.Join(dir, "session.jsonl"), lines.String())
	writeFile(t, filepath.Join(dir, "delivered.log"), "")
	writeRunCredential(t, filepath.Join(dir, "run-secret"), recordedSecret+"\n")
	return dir
}

// TestResendThroughAGatewaySaysWhatItAnswered is each answer of a separate gateway to a
// resend that qory words: the run ended at the credential's issuer, or by the gateway
// for another reason, named, its own run_closed, issuer_unreachable and
// issuer_answer_invalid among them; a batch it refuses, which
// ends the run; no answer that accepts within --wait; every batch accepted; and the
// discovery's 401 to a run credential with no exp qory can read. Each run ended or
// refused is exit 1, the events kept; what is accepted is exit 0. The run credential
// goes to the gateway on every request, and nowhere else.
func TestResendThroughAGatewaySaysWhatItAnswered(t *testing.T) {
	root := newCheckout(t)
	link := newFakeLink(t)
	writeFile(t, foragerFile(), sessionGateway(strings.TrimPrefix(link.URL, "https://"), link.ca, ""))
	credential := "opaque-run-credential-" + fmt.Sprint(time.Now().UnixNano())
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)
	for i, c := range []struct {
		name      string
		discovery int
		status    int
		body      string
		args      []string
		want      func(shown string) string
		code      int
	}{
		{name: "ended at the issuer", status: http.StatusGone, body: `{"error":"run_ended_at_issuer","from":"gateway"}`,
			want: func(s string) string { return fmt.Sprintf(resendIssuer, s) }, code: 1},
		{name: "session_lost", status: http.StatusGone, body: `{"error":"session_lost","from":"gateway"}`,
			want: func(s string) string { return fmt.Sprintf(resendEnded, "session_lost", s) }, code: 1},
		{name: "credential_expired", status: http.StatusGone, body: `{"error":"credential_expired","from":"gateway"}`,
			want: func(s string) string { return fmt.Sprintf(resendEnded, "credential_expired", s) }, code: 1},
		{name: "a batch refused", status: http.StatusBadRequest, body: `{"error":"invalid_request","from":"gateway"}`,
			want: func(s string) string { return fmt.Sprintf(resendEnded, "batch_refused", s) }, code: 1},
		{name: "run_closed", status: http.StatusGone, body: `{"error":"run_closed","from":"gateway"}`,
			want: func(s string) string { return fmt.Sprintf(resendEnded, "run_closed", s) }, code: 1},
		{name: "issuer_unreachable", status: http.StatusGone, body: `{"error":"issuer_unreachable","from":"gateway"}`,
			want: func(s string) string { return fmt.Sprintf(resendEnded, "issuer_unreachable", s) }, code: 1},
		{name: "issuer_answer_invalid", status: http.StatusGone, body: `{"error":"issuer_answer_invalid","from":"gateway"}`,
			want: func(s string) string { return fmt.Sprintf(resendEnded, "issuer_answer_invalid", s) }, code: 1},
		{name: "no answer that accepts", status: http.StatusServiceUnavailable, args: []string{"--wait", "2s"},
			want: func(s string) string { return fmt.Sprintf(resendNotSent, 0, 2, s) }, code: 1},
		{name: "accepted", status: http.StatusOK,
			want: func(string) string { return fmt.Sprintf(resendSent, 2) }},
		{name: "a run credential with no exp refused", discovery: http.StatusUnauthorized,
			want: func(string) string { return resendRefused }, code: 1},
	} {
		id := fmt.Sprintf("0191f2a4-3c5e-7b8d-9e0f-%012d", i+1)
		dir := gatewayRecord(t, root, id)
		link.answer(c.status, c.body)
		link.mu.Lock()
		link.discovery, link.bearers = c.discovery, nil
		link.mu.Unlock()
		t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)
		out, err := run(t, append([]string{"run", "resend", id}, c.args...)...)
		want := c.want(ui.Short(dir, root))
		if cmd.ExitCode(err) != c.code || !strings.Contains(failed(out, err), want) {
			t.Errorf("%s: %v (exit %d), want %q, exit %d\n%s", c.name, err, cmd.ExitCode(err), want, c.code, out)
		}
		lacks(t, failed(out, err), credential)
		link.mu.Lock()
		if len(link.bearers) == 0 {
			t.Errorf("%s: no request reached the gateway", c.name)
		}
		for _, b := range link.bearers {
			if b != credential {
				t.Errorf("%s: a request carried %q, not the run credential", c.name, b)
			}
		}
		if c.discovery == 0 && link.batches == 0 {
			t.Errorf("%s: no batch reached the gateway", c.name)
		}
		link.mu.Unlock()
		link.wantSecret(t, c.name, recordedSecret)
		if c.code != 0 {
			if b, err := os.ReadFile(filepath.Join(dir, "session.jsonl")); err != nil || strings.Count(string(b), "\n") != 2 {
				t.Errorf("%s: the run directory lost its events: %v", c.name, err)
			}
		}
	}
	noCredentialUnder(t, os.Getenv("XDG_STATE_HOME"), credential)
}

// TestResendOfARunTheGatewayNeverOpened is a record with no delivered.log, of a run the
// gateway never opened: the resend says so, sends nothing, with no request, leaves the
// record as it is, and is exit 0. A record that owes nothing still says that nothing is
// left to send.
func TestResendOfARunTheGatewayNeverOpened(t *testing.T) {
	root := newCheckout(t)
	link := newFakeLink(t)
	writeFile(t, foragerFile(), sessionGateway(strings.TrimPrefix(link.URL, "https://"), link.ca, ""))
	credential := "opaque-run-credential-" + fmt.Sprint(time.Now().UnixNano())
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)

	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5ea0"
	dir := gatewayRecord(t, root, id)
	if err := os.Remove(filepath.Join(dir, "delivered.log")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "run", "resend", id)
	want := fmt.Sprintf(resendUnopened, id, ui.Short(dir, root))
	if err != nil || !strings.HasSuffix(out, " "+want+"\n") || strings.Count(out, "\n") != 1 {
		t.Errorf("a run the gateway never opened: %v (exit %d), want %q, exit 0\n%s", err, cmd.ExitCode(err), want, out)
	}
	lacks(t, failed(out, err), credential)
	link.mu.Lock()
	if len(link.bearers) != 0 {
		t.Errorf("a run the gateway never opened: %d requests reached the gateway", len(link.bearers))
	}
	link.mu.Unlock()
	if after, err := os.ReadFile(filepath.Join(dir, "session.jsonl")); err != nil || string(after) != string(before) {
		t.Errorf("the record changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "delivered.log")); err == nil {
		t.Error("the resend wrote a delivered.log")
	}
	if _, err := os.Stat(filepath.Join(dir, "undelivered")); err == nil {
		t.Error("the resend wrote undelivered/")
	}

	const complete = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5ea1"
	gatewayRecord(t, root, complete)
	// qory takes the variable out of its environment each time a command starts.
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)
	if out, err := run(t, "run", "resend", complete); err != nil || !strings.Contains(out, fmt.Sprintf(resendSent, 2)) {
		t.Fatalf("the first resend: %v\n%s", err, out)
	}
	t.Setenv("QORY_RUN_CREDENTIAL_SECRET", credential)
	out, err = run(t, "run", "resend", complete)
	if err != nil || !strings.HasSuffix(out, " "+fmt.Sprintf(resendSent, 0)+"\n") || strings.Contains(out, "never opened") {
		t.Errorf("a record that owes nothing: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	lacks(t, failed(out, err), credential)
}

// TestResendBehindAGatewayRefusesBeforeAnythingIsSent is every refusal of qory run
// resend behind a gateway before any request, each in its exact words and an input
// error: the access key in a descriptor or a variable; no run credential, or its file
// not there or open to others; a descriptor qory does not read; a record its session
// still holds; a run that is not recorded; and a record a gateway of the run's own made,
// which goes to the server. The gateway is not reachable, so a request would fail
// otherwise.
func TestResendBehindAGatewayRefusesBeforeAnythingIsSent(t *testing.T) {
	root := newCheckout(t)
	file := foragerFile()
	dir := string(configDir())
	writeCertificate(t)
	credential := filepath.Join(t.TempDir(), "run-credential")
	writeRunCredential(t, credential, "header.claims.signature\n")
	with := sessionGateway("gateway.example:8443", "gateway.pem", "    run_credential_file: "+credential+"\n")
	const through = "this machine's runs go through the gateway session.gateway.url names"
	const id = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e90"
	record := gatewayRecord(t, root, id)
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) []string
		want  string
	}{
		{"--access-key-secret-fd", func(t *testing.T) []string {
			return []string{"run", "resend", id, "--access-key-secret-fd", descriptor(t, newKey(t).Secret())}
		}, "--access-key-secret-fd is set, and " + through + ": a machine behind a gateway holds no access key; unset it"},
		{"QORY_ACCESS_KEY_ID", func(t *testing.T) []string {
			t.Setenv("QORY_ACCESS_KEY_ID", testAccessKey)
			return []string{"run", "resend", id}
		}, "QORY_ACCESS_KEY_ID is set, and " + through + ": a machine behind a gateway holds no access key; unset it"},
		{"no run credential", func(t *testing.T) []string {
			writeFile(t, file, sessionGateway("gateway.example:8443", "gateway.pem", ""))
			return []string{"run", "resend", id}
		}, through + ", and there is no run credential: set session.gateway.run_credential_file, --run-credential-fd or QORY_RUN_CREDENTIAL_SECRET"},
		{"a run credential file that is not there", func(t *testing.T) []string {
			writeFile(t, file, sessionGateway("gateway.example:8443", "gateway.pem", "    run_credential_file: "+filepath.Join(dir, "missing-credential")+"\n"))
			return []string{"run", "resend", id}
		}, file + ": session.gateway.run_credential_file " + filepath.Join(dir, "missing-credential") + ": no such file or directory"},
		{"a run credential file others may read", func(t *testing.T) []string {
			os.Chmod(credential, 0o644)
			t.Cleanup(func() { os.Chmod(credential, 0o600) })
			return []string{"run", "resend", id}
		}, credential + " is mode 0644, which grants access to the group or others: chmod 600 " + credential},
		{"--run-credential-fd 1", func(*testing.T) []string { return []string{"run", "resend", id, "--run-credential-fd", "1"} },
			"--run-credential-fd 1: the standard input, output and error carry no run credential; name a descriptor of 3 or above"},
		{"a record its session holds", func(t *testing.T) []string {
			held, err := os.OpenFile(filepath.Join(record, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { held.Close() })
			return []string{"run", "resend", id}
		}, "the run " + id + " is running"},
		{"a run that is not recorded", func(*testing.T) []string {
			return []string{"run", "resend", "0191f2a4-3c5e-7b8d-9e0f-000000000000"}
		}, "no run 0191f2a4-3c5e-7b8d-9e0f-000000000000 is recorded in this checkout"},
		{"a record of a gateway of the run's own", func(t *testing.T) []string {
			const own = "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e91"
			ownDir := gatewayRecord(t, root, own)
			writeFile(t, filepath.Join(ownDir, "events.jsonl"), "{}\n")
			return []string{"run", "resend", own}
		}, fmt.Sprintf(resendOwnLocal, "0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e91")},
	} {
		t.Run(c.name, func(t *testing.T) {
			writeFile(t, file, with)
			args := c.setup(t)
			out, err := run(t, args...)
			if cmd.ExitCode(err) != cmd.ExitInput || err == nil || err.Error() != c.want {
				t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), c.want, out)
			}
		})
	}
}
