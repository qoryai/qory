package cmd_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/runkey"
	"github.com/qoryai/forager/session"
	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/cmd"
)

// codeRelay stands in for a gateway's relay of a run code to Qory Apiary: a loopback
// TLS 1.3 server, whose certificate caFile contains, that returns to each verified
// enrolment request what Qory Apiary returns to a run code. The first public key that
// redeems the code gets the run's id and its run key, signed under signer, whose
// fingerprint is the code's; the same key again gets the same body, signed again over
// its new proof, and another key gets 401. The 201 contains documents as its documents
// member, and no documents member when documents is nil. mode changes what the relay
// returns, for the tests of what qory does with a wrong answer or none.
type codeRelay struct {
	*httptest.Server
	caFile string
	signer *accesskey.Key
	mu     sync.Mutex
	// mode is "" for Qory Apiary's answer, or one of: "401"; "other-gateway", a run key
	// of another gateway; "answer-signature", an answer signed under another key;
	// "run-key-signature", a run key signed under another key; "replay", an answer
	// signed over the proof of another request; "node", the 201 of a node's access key;
	// "409", "409-unknown" and "429", signed refusals; "drop-first" and "drop-all", a
	// connection closed before the answer, on the first request or on every one.
	mode string
	// documents is the 201's documents, [codeDocuments] unchanged unless a test sets it
	// before the run.
	documents json.RawMessage
	redeemer  string
	body      []byte
	requests  []accesskey.EnrolmentRequest
	// redeeming, when a test sets it, runs as each request arrives, before the answer.
	redeeming func()
}

// newCodeRelay serves until the test ends. Its CA file is in a directory that no other
// file is in, and qory run trusts it and sends a lost redemption again without a pause.
func newCodeRelay(t *testing.T) *codeRelay {
	t.Helper()
	g := &codeRelay{signer: newKey(t), documents: codeDocuments(t, nil)}
	keys := func() string {
		return `[{"alg":"ed25519","public_key":"` + g.signer.PublicKey().String() + `"}]`
	}
	g.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		var req accesskey.EnrolmentRequest
		if r.Method != http.MethodPost || r.URL.Path != accesskey.EnrolmentPath || json.Unmarshal(b, &req) != nil || !req.VerifyProof() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		g.requests = append(g.requests, req)
		if g.redeeming != nil {
			g.redeeming()
		}
		if g.mode == "401" || (g.redeemer != "" && g.redeemer != req.PublicKey) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		status, proof := http.StatusCreated, req.Proof
		var body []byte
		switch g.mode {
		case "409":
			status, body = http.StatusConflict, []byte(`{"error":"key_invalid","apiary_public_key":`+keys()+`}`)
		case "409-unknown":
			status, body = http.StatusConflict, []byte(`{"error":"key_retired","apiary_public_key":`+keys()+`}`)
		case "429":
			status, body = http.StatusTooManyRequests, []byte(`{"error":"rate_limited","apiary_public_key":`+keys()+`}`)
		case "node":
			body = []byte(`{"version":1,"access_key_id":"ak_0123456789abcdef","node_id":"nd_0123456789abcdef","node_kind":"node","stored_secrets":false,"apiary_public_key":` + keys() + `}`)
		}
		if body == nil {
			if g.body == nil {
				g.redeemer = req.PublicKey
				gatewayURL, keySigner := g.URL, g.signer
				switch g.mode {
				case "other-gateway":
					gatewayURL = "https://gateway.example:8443"
				case "run-key-signature":
					keySigner = newKey(t)
				}
				issued := time.Now().UTC().Truncate(time.Second)
				key, err := runkey.Key{
					RunID: keyRunID, Workspace: "ws_0123456789abcdef", NodeID: "np_0123456789abcdef",
					Gateway: testGatewayNode, InstanceID: "i_gYKDhIWGh4iJiouMjY6PkA", GatewayURL: gatewayURL,
					GatewayKeys: []accesskey.PublicKey{newKey(t).PublicKey()},
					Issued:      issued, AttachBy: issued.Add(600 * time.Second),
				}.Sign(keySigner)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				documents := ""
				if g.documents != nil {
					documents = `,"documents":` + string(g.documents)
				}
				g.body = []byte(`{"version":1,"run_id":"` + keyRunID + `","run_key":"` + key + `","apiary_public_key":` + keys() + documents + `}`)
			}
			body = g.body
		}
		if g.mode == "drop-all" || (g.mode == "drop-first" && len(g.requests) == 1) {
			if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
				c.Close()
			}
			return
		}
		signBy := g.signer
		switch g.mode {
		case "answer-signature":
			signBy = newKey(t)
		case "replay":
			other, err := accesskey.NewEnrolmentRequest(newKey(t), g.code(), "run-01", time.Now())
			if err != nil {
				t.Error(err)
			}
			proof = other.Proof
		}
		w.Header().Set(accesskey.HeaderSignature, signBy.SignAnswer(accesskey.Answer{Enrolment: true, Status: status, RequestSignature: proof, Body: body}))
		w.WriteHeader(status)
		w.Write(body)
	}))
	g.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	g.Config.ErrorLog = log.New(io.Discard, "", 0)
	g.StartTLS()
	t.Cleanup(g.Close)
	g.caFile = filepath.Join(t.TempDir(), "gateway-ca.pem")
	writeFile(t, g.caFile, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.Certificate().Raw})))
	t.Cleanup(cmd.SetRunCodeCAFile(g.caFile))
	t.Cleanup(cmd.SetRunCodePause(0))
	return g
}

// code is a run code of the relay's server key, in groups.
func (g *codeRelay) code() string {
	return "qecr_F1XT-0RE0-0000-0000-0000-0000-00." + g.signer.Fingerprint()
}

// runKey is the run key the relay redeemed the code for, empty before.
func (g *codeRelay) runKey() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var a struct {
		RunKey string `json:"run_key"`
	}
	json.Unmarshal(g.body, &a)
	return a.RunKey
}

// sent is the requests the relay verified.
func (g *codeRelay) sent() []accesskey.EnrolmentRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]accesskey.EnrolmentRequest(nil), g.requests...)
}

// host is the relay's host and port, as qory run's first line shows a gateway.
func (g *codeRelay) host() string { return strings.TrimPrefix(g.URL, "https://") }

// codeFile writes content to a file in a new temporary directory and returns its path.
func codeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run-code")
	writeFile(t, path, content)
	return path
}

// TestARunCodeNeedsItsGatewayAndNoRunKey is --run-code-file and --gateway each without
// the other, and with --run-key-fd or QORY_RUN_KEY: each is refused as input, exit 2,
// before the code is sent, and the descriptor of --run-key-fd is neither read nor
// closed.
func TestARunCodeNeedsItsGatewayAndNoRunKey(t *testing.T) {
	root := runKeyCheckout(t)
	g := newCodeRelay(t)
	file := codeFile(t, g.code()+"\n")
	key := mintRunKey(t, keyRunID, g.URL)
	var fds []string
	fd := func() string {
		n := descriptor(t, key)
		fds = append(fds, n)
		return n
	}
	for _, c := range []struct {
		args   []string
		envKey string
		want   string
	}{
		{[]string{"run", "--run-code-file", file}, "", "add --gateway <https URL>: --run-code-file redeems the run code through that gateway"},
		{[]string{"run", "--gateway", g.URL}, "", "add --run-code-file <file>, or leave out --gateway: --gateway is the gateway that a run code is redeemed through"},
		{[]string{"run", "--run-code-file", file, "--gateway", g.URL, "--run-key-fd", fd()}, "", "leave out --run-key-fd or --run-code-file: a run takes its run key from one of them"},
		{[]string{"run", "--run-code-file", file, "--run-key-fd", fd()}, "", "leave out --run-key-fd or --run-code-file: a run takes its run key from one of them"},
		{[]string{"run", "--gateway", g.URL, "--run-key-fd", fd()}, "", "leave out --gateway: it goes with --run-code-file alone, and this run takes its run key from --run-key-fd"},
		{[]string{"run", "--run-code-file", file, "--gateway", g.URL}, key, "unset QORY_RUN_KEY, or leave out --run-code-file: a run takes its run key from one of them"},
		{[]string{"run", "--gateway", g.URL}, key, "leave out --gateway: it goes with --run-code-file alone, and this run takes its run key from QORY_RUN_KEY"},
		{[]string{"run", "--run-code-file", file, "--gateway", "http://" + g.host()}, "", "set --gateway to the gateway's https URL, a host and an optional port: --gateway for http://" + g.host() + " is not https: an https URL of a host and an optional port, with nothing after"},
		{[]string{"run", "--run-code-file", file, "--gateway", g.URL, "--local"}, "", "--local runs with a gateway the run starts itself and no server, and a run key is given, by QORY_RUN_KEY, --run-key-fd or --run-code-file: the run goes through the gateway its run key names; give no run key to run --local"},
	} {
		t.Setenv("QORY_RUN_KEY", c.envKey)
		out, err := run(t, c.args...)
		if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v: %v (exit %d), want %q", c.args, err, cmd.ExitCode(err), c.want)
		}
		lacks(t, out, "F1XT", "hello from")
	}
	for _, n := range fds {
		fd, _ := strconv.Atoi(n)
		b := make([]byte, len(key)+1)
		got, err := syscall.Read(fd, b)
		if err != nil || string(b[:got]) != key {
			t.Errorf("--run-key-fd %s was read or closed: %d bytes, %v", n, got, err)
		}
		syscall.Close(fd)
	}
	if n := len(g.sent()); n != 0 {
		t.Errorf("refused runs sent the code %d times", n)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("refused runs left records %v", ids)
	}
}

// TestARunCodeWaitsForTheChecksOfTheRun is a run code beside what the run's later checks
// stop: a --run-id that is not one, a negative --timeout, a --stop-signal that is not
// one, a --wall that is not one, and a --home that is the checkout's composed
// .qory/harness, inside the folder the run runs in, which only the check of a run with a
// run key stops, with its line. Each stops the run before the code is sent.
func TestARunCodeWaitsForTheChecksOfTheRun(t *testing.T) {
	root := runKeyCheckout(t)
	g := newCodeRelay(t)
	file := codeFile(t, g.code())
	for _, extra := range [][]string{
		{"--run-id", "run-1"},
		{"--timeout", "-1s"},
		{"--stop-signal", "SIGFOO"},
		{"--wall", "box"},
	} {
		out, err := run(t, append([]string{"run", "--run-code-file", file, "--gateway", g.URL}, extra...)...)
		if err == nil {
			t.Errorf("%v: the run went on:\n%s", extra, out)
		}
		lacks(t, out, "qory run: through the gateway")
	}
	home := filepath.Join(root, ".qory", "harness")
	out, err := run(t, "run", "--run-code-file", file, "--gateway", g.URL, "--home", home)
	want := "set --home to a folder outside " + root + ": " + home + " is inside " + root + ", and a run with a run key writes no harness into the folder it runs in"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("--home %s: %v (exit %d), want %q\n%s", home, err, cmd.ExitCode(err), want, out)
	}
	if n := len(g.sent()); n != 0 {
		t.Errorf("runs that later checks stopped sent the code %d times", n)
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("runs that later checks stopped left records %v", ids)
	}
}

// TestARunCodeFileThatHoldsNoCodeIsRefused is a --run-code-file that cannot be read, is
// not a regular file, is empty, contains more than the code and one newline, or
// contains no run code: each is refused as input, exit 2, with its path once and
// without the file's content, before anything is sent. A named pipe that no program
// writes to does not stop qory.
func TestARunCodeFileThatHoldsNoCodeIsRefused(t *testing.T) {
	runKeyCheckout(t)
	g := newCodeRelay(t)
	missing := filepath.Join(t.TempDir(), "none")
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, c := range []struct {
		file, want string
	}{
		{missing, "give --run-code-file a file that qory can read: %s: no such file or directory"},
		{fifo, "give --run-code-file a regular file: %s is not one"},
		{dir, "give --run-code-file a regular file: %s is not one"},
		{codeFile(t, ""), "write the run code into %s: the file is empty"},
		{codeFile(t, "\n"), "write the run code into %s: the file is empty"},
		{codeFile(t, g.code()+"\n\n"), "write only the run code into %s, with at most one newline after it: the file contains more"},
		{codeFile(t, g.code()+"\r\n"), "write only the run code into %s, with at most one newline after it: the file contains more"},
		{codeFile(t, " "+g.code()), "write only the run code into %s, with at most one newline after it: the file contains more"},
		{codeFile(t, strings.Repeat("A", 1025)), "write only the run code into %s, with at most one newline after it: the file contains more"},
		{codeFile(t, "qecr_F1XT-0RE0-0000.x"), "write a run code (qecr_…) into %s: the file does not contain an enrolment code of Qory Apiary"},
		{codeFile(t, "qecn_F1XT-0RE0-0000-0000-0000-0000-00."+g.signer.Fingerprint()), "write a run code (qecr_…) into %s: the file contains the enrolment code of an access key of kind node"},
	} {
		want := strings.Replace(c.want, "%s", c.file, 1)
		done := make(chan struct{})
		var out string
		var err error
		go func() {
			defer close(done)
			out, err = run(t, "run", "--run-code-file", c.file, "--gateway", g.URL)
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatalf("%s: qory run did not return", c.file)
		}
		if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%s: %v (exit %d), want %q", c.file, err, cmd.ExitCode(err), want)
		}
		if err != nil && strings.Count(err.Error(), c.file) != 1 {
			t.Errorf("%s: the path is not in the line once: %v", c.file, err)
		}
		lacks(t, out, "F1XT", "AAAA")
	}
	if n := len(g.sent()); n != 0 {
		t.Errorf("refused runs sent a code %d times", n)
	}
}

// TestARunCodeIsRedeemedThroughItsGateway is a run code redeemed through the gateway's
// relay: qory sends the code with a proof under a key made in memory, and the run goes
// through the gateway that the run key is for, as with --run-key-fd. An answer lost on
// its way back is asked for again once, with the same request. Neither the code nor
// the run key is in qory's output or in a file under HOME, where the state and the
// record are, or in the checkout.
func TestARunCodeIsRedeemedThroughItsGateway(t *testing.T) {
	for _, mode := range []string{"", "drop-first"} {
		t.Run("mode="+mode, func(t *testing.T) {
			root := runKeyCheckout(t)
			g := newCodeRelay(t)
			g.mode = mode
			out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()+"\n"), "--gateway", g.URL+"/")
			if err == nil {
				t.Fatalf("the run went on without its gateway:\n%s", out)
			}
			if line := firstLine(out); line != "qory run: through the gateway "+g.host()+", run "+keyRunID {
				t.Errorf("the first line is %q\n%s", line, out)
			}
			sent := g.sent()
			want := 1
			if mode == "drop-first" {
				want = 2
			}
			if len(sent) != want || sent[0].Code != "qecr_F1XT0RE0000000000000000000."+g.signer.Fingerprint() {
				t.Fatalf("the relay got %d requests, want %d: %+v", len(sent), want, sent)
			}
			if want == 2 && sent[1] != sent[0] {
				t.Errorf("the retry is not the same request: %+v", sent)
			}
			key := g.runKey()
			lacks(t, out+err.Error(), key, key[4:40], "F1XT")
			for _, dir := range []string{os.Getenv("HOME"), root} {
				noValueUnder(t, dir, key)
				noValueUnder(t, dir, key[4:40])
				noValueUnder(t, dir, "F1XT")
			}
		})
	}
}

// TestARunCodeThatIsNotRedeemedStopsTheRun is a redemption whose answer qory does not
// run with, or that gets none. Each stops the run before it starts, with a line that
// starts with its fix and contains neither the code nor the run key. What needs a new
// run code is input, exit 2; the rest exit 1. Only a lost answer is sent again.
func TestARunCodeThatIsNotRedeemedStopsTheRun(t *testing.T) {
	const again = "run qory run again within the run code's 15 minutes, and ask for a new run code if the redemption then returns 401: "
	const unsigned = "check that --gateway is the run's gateway, then " + again + "the response to the redemption (HTTP 201) does not verify under the server key whose fingerprint is first in the code (answer_unsigned)"
	for _, c := range []struct {
		mode, want string
		input      bool
		sends      int
	}{
		{"401", "ask for a new run code: the redemption returned 401, so the code was used, has expired or was cancelled, or it was redeemed with another key first (unauthorized)", true, 1},
		{"other-gateway", "ask for a new run code, and give qory run the gateway that the run key is for: the run key is for the gateway gateway.example:8443, and --gateway is %s; the code is used", true, 1},
		{"run-key-signature", "use another run code: the enrolment answer's run key is not valid: ", true, 1},
		{"node", `use another run code: the server's 201 to the run code enrols an access key of node_kind "node" and contains no run key; the code is used`, true, 1},
		{"answer-signature", unsigned, false, 1},
		{"replay", unsigned, false, 1},
		{"409", "run qory run again within the run code's 15 minutes: Qory Apiary returned 409 to the key pair that qory made for the redemption (key_invalid)", false, 1},
		{"409-unknown", "update qory, then run qory run again within the run code's 15 minutes: Qory Apiary returned a signed refusal whose code this qory does not read: ", false, 1},
		{"429", "run qory run again later, within the run code's 15 minutes: Qory Apiary returned 429 to the redemption, too many attempts with this code (rate_limited)", false, 1},
		{"drop-all", again + "the redemption through the gateway %s failed twice: ", false, 2},
	} {
		t.Run(c.mode, func(t *testing.T) {
			root := runKeyCheckout(t)
			g := newCodeRelay(t)
			g.mode = c.mode
			out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL)
			want := strings.Replace(c.want, "%s", g.host(), 1)
			if err == nil || !strings.HasPrefix(err.Error(), want) || (cmd.ExitCode(err) == cmd.ExitInput) != c.input {
				t.Errorf("%v (exit %d), want %q", err, cmd.ExitCode(err), want)
			}
			lacks(t, out, "qory run: through the gateway", "hello from")
			if key := g.runKey(); key != "" {
				lacks(t, out+err.Error(), key, key[4:40])
				noValueUnder(t, os.Getenv("HOME"), key)
			}
			lacks(t, out+err.Error(), "F1XT")
			if n := len(g.sent()); n != c.sends {
				t.Errorf("the code was sent %d times, want %d", n, c.sends)
			}
			if ids := recorded(t, root); len(ids) != 0 {
				t.Errorf("a run that did not start left records %v", ids)
			}
		})
	}
}

// TestARunCodeWithNoGatewayListeningStopsTheRun is a --gateway at which nothing
// listens: qory tries twice, and the run stops with exit 1 before it starts.
func TestARunCodeWithNoGatewayListeningStopsTheRun(t *testing.T) {
	root := runKeyCheckout(t)
	g := newCodeRelay(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", "https://"+addr)
	want := "run qory run again within the run code's 15 minutes, and ask for a new run code if the redemption then returns 401: the redemption through the gateway " + addr + " failed twice: "
	if err == nil || !strings.HasPrefix(err.Error(), want) || cmd.ExitCode(err) != 1 {
		t.Errorf("%v (exit %d), want %q", err, cmd.ExitCode(err), want)
	}
	lacks(t, out+err.Error(), "F1XT", "qory run: through the gateway")
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("a run that did not start left records %v", ids)
	}
}

// TestARunCodeThroughACertificateThatFailsStopsTheRun is a --gateway whose TLS
// certificate does not verify: one of an authority that qory run does not trust, and one
// for another host name. qory tries twice, nothing reaches the relay, and the run stops
// with exit 1 before it starts, with a line that starts with checking --gateway.
func TestARunCodeThroughACertificateThatFailsStopsTheRun(t *testing.T) {
	for _, c := range []struct{ name, x509 string }{
		{"unknown authority", "x509: certificate signed by unknown authority"},
		{"other host name", "x509: certificate is valid for "},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := runKeyCheckout(t)
			g := newCodeRelay(t)
			gatewayURL, host := g.URL, g.host()
			if c.name == "unknown authority" {
				t.Cleanup(cmd.SetRunCodeCAFile(otherCAFile(t)))
			} else {
				// The relay's certificate is for 127.0.0.1, ::1 and example.com.
				gatewayURL = strings.Replace(g.URL, "127.0.0.1", "localhost", 1)
				host = strings.TrimPrefix(gatewayURL, "https://")
			}
			out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", gatewayURL)
			want := "check that --gateway is the run's gateway, then run qory run again within the run code's 15 minutes, and ask for a new run code if the redemption then returns 401: the redemption through the gateway " + host + " failed twice: "
			if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), c.x509) || cmd.ExitCode(err) != 1 {
				t.Errorf("%v (exit %d), want %q and %q", err, cmd.ExitCode(err), want, c.x509)
			}
			lacks(t, out+err.Error(), "F1XT", "qory run: through the gateway")
			if n := len(g.sent()); n != 0 {
				t.Errorf("the relay got %d requests, want none", n)
			}
			if ids := recorded(t, root); len(ids) != 0 {
				t.Errorf("a run that did not start left records %v", ids)
			}
		})
	}
}

// otherCAFile writes a self-signed certificate for 127.0.0.1, which signed none of the
// relay's, to a file in a new temporary directory and returns its path.
func otherCAFile(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "other.example"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "other-ca.pem")
	writeFile(t, path, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	return path
}

// TestARunCodeRedeemedAgainGetsTheSameRunKey is the redemption of one run code twice by
// the same key, with a new timestamp: Qory Apiary signs the same run key again over the
// new proof, and qory reads the same run and run key from both. Another key then gets
// 401, input.
func TestARunCodeRedeemedAgainGetsTheSameRunKey(t *testing.T) {
	emptyDir(t)
	g := newCodeRelay(t)
	file := codeFile(t, g.code()+"\n")
	k := newKey(t)
	now := time.Now()
	id1, key1, err := cmd.RedeemRunCode(context.Background(), k, file, g.URL, now)
	if err != nil {
		t.Fatal(err)
	}
	id2, key2, err := cmd.RedeemRunCode(context.Background(), k, file, g.URL, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if id1 != keyRunID || id2 != id1 || key2 != key1 {
		t.Errorf("the retry read run %s, want %s, and the same run key: %t", id2, id1, key2 == key1)
	}
	if sent := g.sent(); len(sent) != 2 || sent[0].Proof == sent[1].Proof {
		t.Errorf("the relay got %d requests, want 2 with different proofs", len(sent))
	}
	_, _, err = cmd.RedeemRunCode(context.Background(), newKey(t), file, g.URL, now)
	if err == nil || !strings.HasPrefix(err.Error(), "ask for a new run code: the redemption returned 401") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("another key: %v (exit %d)", err, cmd.ExitCode(err))
	}
}

// TestARefusedRunCodeDescriptorIsClosed is a --run-code-file of /dev/fd/N, an inherited
// descriptor without close-on-exec, whose content qory refuses, and one beside a missing
// --gateway: each run stops, and the descriptor is closed by then, so no program that
// qory starts later inherits it. A number that qory opened again by then is
// close-on-exec, as qory opens every file.
func TestARefusedRunCodeDescriptorIsClosed(t *testing.T) {
	runKeyCheckout(t)
	g := newCodeRelay(t)
	for _, c := range []struct {
		name, content string
		gateway       []string
	}{
		{"more than the code", g.code() + "\n\n", []string{"--gateway", g.URL}},
		{"no --gateway", g.code(), nil},
	} {
		file := codeFile(t, c.content)
		n, err := syscall.Open(file, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		out, err := run(t, append([]string{"run", "--run-code-file", "/dev/fd/" + strconv.Itoa(n)}, c.gateway...)...)
		if cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%s: %v (exit %d), want a refusal\n%s", c.name, err, cmd.ExitCode(err), out)
		}
		if flags, err := unix.FcntlInt(uintptr(n), unix.F_GETFD, 0); err == nil && flags&unix.FD_CLOEXEC == 0 {
			syscall.Close(n)
			t.Errorf("%s: the descriptor %d is open without close-on-exec after the refusal", c.name, n)
		}
	}
}

// stdinForms are the paths of --run-code-file that lead to qory's standard input.
func stdinForms() []string {
	return []string{"/dev/fd/0", "/dev/stdin", "/dev/fd/00", "/proc/self/fd/0", "/proc/thread-self/fd/0", "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/0"}
}

// stdinPipe is the read end of a pipe that contains content, its write end closed.
func stdinPipe(t *testing.T, content string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	if _, err := w.WriteString(content); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return r
}

// stdinFile is a regular file that contains content, open for reading at its start.
func stdinFile(t *testing.T, content string) *os.File {
	t.Helper()
	f, err := os.Open(codeFile(t, content))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestARunCodeOnStdinLeavesTheAgentNone is a --run-code-file that leads to stdin, in
// each of its forms, with stdin a pipe and a regular file: qory reads the code from the
// stdin it has, not from the path opened again, redeems it and closes stdin, and the
// session joins with an empty stdin.
func TestARunCodeOnStdinLeavesTheAgentNone(t *testing.T) {
	for _, form := range stdinForms() {
		for _, kind := range []struct {
			name string
			open func(*testing.T, string) *os.File
		}{{"pipe", stdinPipe}, {"regular file", stdinFile}} {
			t.Run(form+" "+kind.name, func(t *testing.T) {
				var in *os.File
				r := runFromCodeGiven(t, nil, nil, nil, nil, nil, nil, func(code string) (string, io.Reader) {
					in = kind.open(t, code+"\n")
					return form, in
				})
				if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
					t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
				}
				if b, err := io.ReadAll(r.spec.Stdin); err != nil || len(b) != 0 {
					t.Errorf("the session's stdin contains %q, %v; want it empty", b, err)
				}
				if _, err := in.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
					t.Errorf("stdin is open after the run: %v", err)
				}
				lacks(t, r.out, "F1XT")
			})
		}
	}
}

// TestARunCodeOnStdinThatContainsNoCodeIsRefused is a --run-code-file that leads to stdin,
// where stdin is empty or contains more than the code and one newline: each is refused
// as input, exit 2, with the path once and without the code, before anything is sent,
// and stdin is closed.
func TestARunCodeOnStdinThatContainsNoCodeIsRefused(t *testing.T) {
	runKeyCheckout(t)
	g := newCodeRelay(t)
	for _, c := range []struct {
		content, want string
	}{
		{"", "write the run code into /dev/stdin: the file is empty"},
		{g.code() + "\n" + g.code() + "\n", "write only the run code into /dev/stdin, with at most one newline after it: the file contains more"},
	} {
		in := stdinPipe(t, c.content)
		out, err := runReading(t, in, "run", "--run-code-file", "/dev/stdin", "--gateway", g.URL)
		if err == nil || err.Error() != c.want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%q: %v (exit %d), want %q", c.content, err, cmd.ExitCode(err), c.want)
		}
		lacks(t, out, "F1XT")
		if _, err := in.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Errorf("%q: stdin is open after the refusal: %v", c.content, err)
		}
	}
	if n := len(g.sent()); n != 0 {
		t.Errorf("refused runs sent a code %d times", n)
	}
}

// TestARunCodeDescriptorIsClosedBeforeTheJoin is a --run-code-file that leads to N, an
// inherited descriptor of 3 or above without close-on-exec, whose code qory redeems, in
// each spelling: /dev/fd/N, with a double slash, a dot or a zero before N on macOS; a
// link to /dev/fd/N; the file's path, with the file inherited as N too; and on Linux,
// N under /proc/self/fd, /proc/thread-self/fd and /proc/<qory's process id>/fd. By the
// session's join, N is closed, or its number is one that qory opened again,
// close-on-exec, so the agent does not inherit it.
func TestARunCodeDescriptorIsClosedBeforeTheJoin(t *testing.T) {
	spellings := map[string]func(t *testing.T, file string, n int) string{
		"/dev/fd/N":   func(_ *testing.T, _ string, n int) string { return "/dev/fd/" + strconv.Itoa(n) },
		"/dev//fd/N":  func(_ *testing.T, _ string, n int) string { return "/dev//fd/" + strconv.Itoa(n) },
		"/dev/fd/./N": func(_ *testing.T, _ string, n int) string { return "/dev/fd/./" + strconv.Itoa(n) },
		"a link to /dev/fd/N": func(t *testing.T, _ string, n int) string {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink("/dev/fd/"+strconv.Itoa(n), link); err != nil {
				t.Fatal(err)
			}
			return link
		},
		"the file's path": func(_ *testing.T, file string, _ int) string { return file },
	}
	switch runtime.GOOS {
	case "darwin":
		spellings["/dev/fd/0N"] = func(_ *testing.T, _ string, n int) string { return "/dev/fd/0" + strconv.Itoa(n) }
	case "linux":
		for _, dir := range []string{"/proc/self/fd/", "/proc/thread-self/fd/", "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/"} {
			spellings[dir+"N"] = func(_ *testing.T, _ string, n int) string { return dir + strconv.Itoa(n) }
		}
	}
	for name, spell := range spellings {
		t.Run(name, func(t *testing.T) {
			n := -1
			open := true
			t.Cleanup(cmd.SetJoinSession(func(session.SessionSpec) {
				flags, err := unix.FcntlInt(uintptr(n), unix.F_GETFD, 0)
				open = err == nil && flags&unix.FD_CLOEXEC == 0
			}))
			r := runFromCodeGiven(t, nil, nil, nil, nil, nil, nil, func(code string) (string, io.Reader) {
				file := codeFile(t, code)
				var err error
				if n, err = syscall.Open(file, syscall.O_RDONLY, 0); err != nil {
					t.Fatal(err)
				}
				return spell(t, file, n), nil
			})
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if open {
				syscall.Close(n)
				t.Errorf("the descriptor %d is open without close-on-exec at the join", n)
			}
		})
	}
}

// TestARunCodeInTheFileThatIsStdinLeavesTheAgentNone is a --run-code-file that
// [namesStdin] does not match but that is the same file as stdin, with stdin a pipe and
// a regular file, and a copy of stdin's descriptor inherited as N, without
// close-on-exec: /dev/fd/N, and, for a regular file, its path and a link to it. qory
// reads stdin, not the file opened by its path, and closes it, and the session joins
// with an empty stdin. For a regular file, N is closed by the join, or its number is
// one that qory opened again, close-on-exec.
func TestARunCodeInTheFileThatIsStdinLeavesTheAgentNone(t *testing.T) {
	link := func(t *testing.T, in *os.File, _ int) string {
		l := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(in.Name(), l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	byCopy := func(_ *testing.T, _ *os.File, n int) string { return "/dev/fd/" + strconv.Itoa(n) }
	for _, c := range []struct {
		name    string
		regular bool
		open    func(*testing.T, string) *os.File
		path    func(*testing.T, *os.File, int) string
	}{
		{"pipe, a copy of its descriptor", false, stdinPipe, byCopy},
		{"regular file, a copy of its descriptor", true, stdinFile, byCopy},
		{"regular file, its path", true, stdinFile, func(_ *testing.T, in *os.File, _ int) string { return in.Name() }},
		{"regular file, a link to it", true, stdinFile, link},
	} {
		t.Run(c.name, func(t *testing.T) {
			var in *os.File
			n := -1
			open := true
			t.Cleanup(cmd.SetJoinSession(func(session.SessionSpec) { open = openWithoutCloseOnExec(n) }))
			r := runFromCodeGiven(t, nil, nil, nil, nil, nil, nil, func(code string) (string, io.Reader) {
				in = c.open(t, code+"\n")
				n = inheritedCopy(t, in)
				return c.path(t, in, n), in
			})
			if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
				t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			if b, err := io.ReadAll(r.spec.Stdin); err != nil || len(b) != 0 {
				t.Errorf("the session's stdin contains %q, %v; want it empty", b, err)
			}
			if _, err := in.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
				t.Errorf("stdin is open after the run: %v", err)
			}
			if c.regular && open {
				t.Errorf("the copy %d of stdin is open without close-on-exec at the join", n)
			}
		})
	}
}

// inheritedCopy is a copy of f's descriptor without close-on-exec, as a descriptor
// that qory inherited is. The test closes it at its end, unless qory closed it.
func inheritedCopy(t *testing.T, f *os.File) int {
	t.Helper()
	n, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// A number that Go opened again since qory closed the copy is close-on-exec.
		if openWithoutCloseOnExec(n) {
			syscall.Close(n)
		}
	})
	return n
}

// openWithoutCloseOnExec reports whether the descriptor n is open without close-on-exec.
func openWithoutCloseOnExec(n int) bool {
	flags, err := unix.FcntlInt(uintptr(n), unix.F_GETFD, 0)
	return err == nil && flags&unix.FD_CLOEXEC == 0
}

// TestARunCodeOnTheRealStdinClosesItsCopy is a --run-code-file of /dev/stdin and of
// /dev/fd/0 in a separate process whose descriptor 0 is the code's file, a regular file,
// with a copy of it inherited as descriptor 3: qory reads the code from stdin, and
// descriptor 3 is closed, or its number is one that qory opened again, close-on-exec.
func TestARunCodeOnTheRealStdinClosesItsCopy(t *testing.T) {
	file := codeFile(t, "qecr_F1XT-0RE0-0000-0000-0000-0000-00."+newKey(t).Fingerprint()+"\n")
	for _, path := range []string{"/dev/stdin", "/dev/fd/0"} {
		in, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		copied, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		c := exec.Command(os.Args[0], "--", path)
		c.Env = append(os.Environ(), "QORY_TEST_RUN_CODE=1", "QORY_RUN_KEY=")
		c.Stdin, c.ExtraFiles = in, []*os.File{copied}
		out, err := c.CombinedOutput()
		in.Close()
		copied.Close()
		if got := string(out); err != nil || (got != "stdin=true err=<nil> fd3=closed\n" && got != "stdin=true err=<nil> fd3=close-on-exec\n") {
			t.Errorf("%s: %v\n%s", path, err, got)
		}
	}
}

// TestARunCodeFileThatIsNotStdinLeavesStdin is a --run-code-file that is another file
// than stdin, a regular file: qory reads the code from the file, and stdin, a regular
// file too, stays open and is the session's stdin.
func TestARunCodeFileThatIsNotStdinLeavesStdin(t *testing.T) {
	var in *os.File
	r := runFromCodeGiven(t, nil, nil, nil, nil, nil, nil, func(code string) (string, io.Reader) {
		in = stdinFile(t, "the agent's input\n")
		return codeFile(t, code), in
	})
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if r.spec.Stdin != io.Reader(in) {
		t.Errorf("the session's stdin is %T, want the command's stdin", r.spec.Stdin)
	}
	if b, err := io.ReadAll(in); err != nil || string(b) != "the agent's input\n" {
		t.Errorf("stdin contains %q, %v; want it unread", b, err)
	}
}
