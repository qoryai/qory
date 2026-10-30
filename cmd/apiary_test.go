package cmd_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/apiarydir"
)

// diagnosticsRunID is the run of the diagnostics fixture.
const diagnosticsRunID = "rn_0123456789abcdef"

// diagnosticsFixture is the path of a diagnostics document of the diagnostics
// contract's fixtures, resolved before any test changes the working directory. It is
// read only by the tests that use it: this binary also runs as qory in other
// directories, where testdata is not.
var diagnosticsFixture, _ = filepath.Abs(filepath.Join("testdata", "diagnostics.json"))

// diagnosticsKeyID is the id of the diagnostics key the tests store.
const diagnosticsKeyID = "ak_0123456789abcdef"

// diagnosticsServer stands in for Qory Apiary's diagnostics route: it verifies each
// request's signature under the diagnostics key with its timestamp, and responds with
// the status and body it is set to, signed under its signing key unless unsigned is
// set. A request that does not verify gets an unsigned 401, as Qory Apiary does.
type diagnosticsServer struct {
	*httptest.Server
	signer   *accesskey.Key
	client   *accesskey.Key
	mu       sync.Mutex
	status   int
	body     []byte
	unsigned bool
	// requests counts the requests that verified, and headers keeps the last one's.
	requests int
	headers  http.Header
}

func newDiagnosticsServer(t *testing.T, client *accesskey.Key) *diagnosticsServer {
	t.Helper()
	body, err := os.ReadFile(diagnosticsFixture)
	if err != nil {
		t.Fatal(err)
	}
	s := &diagnosticsServer{signer: newKey(t), client: client, status: http.StatusOK, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		sig := r.Header.Get(accesskey.HeaderSignature)
		req := accesskey.Request{
			AccessKeyID: r.Header.Get(accesskey.HeaderAccessKeyID),
			InstanceID:  r.Header.Get(accesskey.HeaderInstanceID),
			Method:      r.Method,
			Target:      r.URL.RequestURI(),
			Timestamp:   r.Header.Get(accesskey.HeaderTimestamp),
		}
		if r.Method != http.MethodGet || req.AccessKeyID != diagnosticsKeyID || req.Timestamp == "" ||
			r.Header.Get("X-Qory-Contract-Version") != "1" || !s.client.PublicKey().VerifyRequest(req, sig) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/runs/"+diagnosticsRunID+"/diagnostics" {
			s.respond(w, sig, http.StatusNotFound, []byte(`{"error":"not_found"}`))
			return
		}
		s.requests++
		s.headers = r.Header.Clone()
		s.respond(w, sig, s.status, s.body)
	}))
	t.Cleanup(s.Close)
	return s
}

// respond writes status and body, signed for the request of signature sig unless the
// server is set to respond unsigned or the status is 401.
func (s *diagnosticsServer) respond(w http.ResponseWriter, sig string, status int, body []byte) {
	if !s.unsigned && status != http.StatusUnauthorized {
		w.Header().Set(accesskey.HeaderSignature, s.signer.SignAnswer(accesskey.Answer{Status: status, RequestSignature: sig, Body: body}))
	}
	w.WriteHeader(status)
	w.Write(body)
}

// set sets the server's next status and body, and whether it signs them.
func (s *diagnosticsServer) set(status int, body []byte, unsigned bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body, s.unsigned = status, body, unsigned
}

// pin is the server's key as apiary.json lists it.
func (s *diagnosticsServer) pin() json.RawMessage {
	return json.RawMessage(`[{"alg":"ed25519","public_key":"` + s.signer.PublicKey().String() + `"}]`)
}

// storeDiagnosticsKey writes key as the diagnostics key for the server at serverURL with
// the pin pin, under the configuration directory emptyDir set, and returns the server's
// directory.
func storeDiagnosticsKey(t *testing.T, serverURL string, pin json.RawMessage, key *accesskey.Key) string {
	t.Helper()
	root, err := apiarydir.Root()
	if err != nil {
		t.Fatal(err)
	}
	if err := apiarydir.Write(root, apiarydir.Settings{Server: serverURL, AccessKeyID: diagnosticsKeyID, ApiaryPublicKey: pin}, key); err != nil {
		t.Fatal(err)
	}
	host, err := apiarydir.HostName(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, host)
}

// diagnoseKeyed starts a diagnostics server and stores a diagnostics key for it, in the
// environment emptyDir sets.
func diagnoseKeyed(t *testing.T) (*diagnosticsServer, string) {
	t.Helper()
	emptyDir(t)
	key := newKey(t)
	s := newDiagnosticsServer(t, key)
	dir := storeDiagnosticsKey(t, s.URL, s.pin(), key)
	return s, dir
}

func TestApiaryDiagnosePrintsTheVerifiedDocumentAlone(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	out, errOut, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err != nil {
		t.Fatalf("diagnose: %v\n%s", err, errOut)
	}
	if out != string(s.body) {
		t.Errorf("stdout is not the document as sent:\n%s", out)
	}
	if errOut != "" {
		t.Errorf("stderr is not empty:\n%s", errOut)
	}
	h := s.headers
	if h.Get(accesskey.HeaderInstanceID) == "" || h.Get(accesskey.HeaderInstanceName) == "" {
		t.Errorf("the request lacks the instance id or name: %v", h)
	}
	if h.Get("Cookie") != "" || h.Get("Content-Length") != "" {
		t.Errorf("the request has a cookie or a body: %v", h)
	}
}

func TestApiaryDiagnoseEndsTheDocumentWithALineEnding(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	s.set(http.StatusOK, []byte(`{"version":1}`), false)
	out, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"version":1}`+"\n" {
		t.Errorf("stdout %q", out)
	}
}

func TestApiaryDiagnoseNotFound(t *testing.T) {
	_, _ = diagnoseKeyed(t)
	out, _, err := runApart(t, "apiary", "run", "diagnose", "rn_1123456789abcdef")
	if err == nil {
		t.Fatal("diagnose of a run the server does not have succeeded")
	}
	host := hostOf(t)
	if want := "Check the run id: " + host + " has no run rn_1123456789abcdef (not_found)."; firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	if out != "" || cmd.ExitCode(err) != 1 {
		t.Errorf("stdout %q, exit %d", out, cmd.ExitCode(err))
	}
}

func TestApiaryDiagnoseUnauthorized(t *testing.T) {
	emptyDir(t)
	// The server verifies under another key: the stored one gets the unsigned 401.
	s := newDiagnosticsServer(t, newKey(t))
	dir := storeDiagnosticsKey(t, s.URL, s.pin(), newKey(t))
	out, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil {
		t.Fatal("diagnose with a key the server does not accept succeeded")
	}
	want := "Remove " + dir + ", then run `qory apiary enrol " + s.URL + " <code>` again: " + hostOf(t) + " did not accept the diagnostics key " + diagnosticsKeyID + " (401)."
	if firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	if out != "" {
		t.Errorf("stdout %q", out)
	}
}

func TestApiaryDiagnoseSignedRefusalOfAnotherCode(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	s.set(http.StatusConflict, []byte(`{"error":"acme_conflict"}`), false)
	_, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil {
		t.Fatal("diagnose of a refused request succeeded")
	}
	if want := "Check the run with the instance admin of " + hostOf(t) + ": the request was not accepted: acme_conflict (409)."; firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
}

func TestApiaryDiagnoseSignedRefusal(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	s.set(http.StatusBadRequest, []byte(`{"error":"unsupported_contract_version"}`), false)
	_, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil {
		t.Fatal("diagnose of a refused request succeeded")
	}
	if want := "Update qory to a release that works with " + hostOf(t) + ": the request was not accepted: unsupported_contract_version (400)."; firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
}

func TestApiaryDiagnoseUnsignedPrintsNothing(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	s.set(http.StatusOK, s.body, true)
	out, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil {
		t.Fatal("diagnose of an unsigned 200 succeeded")
	}
	if out != "" {
		t.Errorf("an unsigned document reached stdout:\n%s", out)
	}
	wantIn(t, firstLine(err.Error()), "is not signed with the pinned server key, so qory printed nothing")
}

func TestApiaryDiagnoseSignedUnderAnotherKeyPrintsNothing(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	s.mu.Lock()
	s.signer = newKey(t)
	s.mu.Unlock()
	out, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil || out != "" {
		t.Fatalf("diagnose of a document signed under another key: err %v, stdout %q", err, out)
	}
	wantIn(t, firstLine(err.Error()), "is not signed with the pinned server key")
}

func TestApiaryDiagnoseStopsAtADocumentOverTheLimit(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	big := append([]byte(`{"version":1,"pad":"`), bytes.Repeat([]byte("a"), 8<<20)...)
	big = append(big, []byte(`"}`)...)
	s.set(http.StatusOK, big, false)
	out, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil || out != "" {
		t.Fatalf("diagnose of a document over 8 MiB: err %v, stdout %d bytes", err, len(out))
	}
	wantIn(t, firstLine(err.Error()), "larger than 8 MiB")
	// Over the most qory reads, the body is not verified at all.
	s.set(http.StatusOK, append(big, bytes.Repeat([]byte(" "), 64<<10)...), false)
	out, _, err = runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil || out != "" {
		t.Fatalf("diagnose of a body over the read limit: err %v, stdout %d bytes", err, len(out))
	}
	wantIn(t, firstLine(err.Error()), "is not signed with the pinned server key")
}

func TestApiaryDiagnoseStopsAtABadRunID(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	for _, id := range []string{"rn_0123456789abcdeF", "rn_0123456789abcdei", "rn_0123", "wi_0123456789abcdef", "rn_0123456789abcdef0"} {
		out, _, err := runApart(t, "apiary", "run", "diagnose", id)
		if err == nil {
			t.Fatalf("diagnose %s succeeded", id)
		}
		if cmd.ExitCode(err) != cmd.ExitInput || out != "" {
			t.Errorf("diagnose %s: exit %d, stdout %q", id, cmd.ExitCode(err), out)
		}
		wantIn(t, firstLine(err.Error()), "Pass the run id from the run's page in Qory Apiary")
	}
	if s.requests != 0 {
		t.Errorf("a bad run id reached the server %d times", s.requests)
	}
}

func TestApiaryDiagnoseWithoutAKey(t *testing.T) {
	emptyDir(t)
	root, _ := apiarydir.Root()
	_, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil {
		t.Fatal("diagnose without a key succeeded")
	}
	if want := "Run `qory apiary enrol <server> <code>` first: no diagnostics key in " + root + "."; firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	if cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("exit %d", cmd.ExitCode(err))
	}
}

func TestApiaryDiagnoseWithSeveralKeys(t *testing.T) {
	s, dir := diagnoseKeyed(t)
	other := newKey(t)
	storeDiagnosticsKey(t, "https://apiary.example", s.pin(), other)
	root := filepath.Dir(dir)
	_, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil {
		t.Fatal("diagnose with keys for two servers and no --server succeeded")
	}
	want := "Pass --server <url>: diagnostics keys for several servers are in " + root + ": apiary.example, " + filepath.Base(dir) + "."
	if firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	if cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("exit %d", cmd.ExitCode(err))
	}
	// --server selects the test server's key.
	out, errOut, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID, "--server", s.URL)
	if err != nil {
		t.Fatalf("diagnose --server: %v\n%s", err, errOut)
	}
	if out != string(s.body) {
		t.Errorf("stdout is not the document:\n%s", out)
	}
	// The error for --server of a server without a key contains its directory.
	_, _, err = runApart(t, "apiary", "run", "diagnose", diagnosticsRunID, "--server", "https://other.example:8443")
	if want := "Run `qory apiary enrol https://other.example:8443 <code>` first: no diagnostics key in " + filepath.Join(root, "other.example_8443") + "."; err == nil || firstLine(err.Error()) != want {
		t.Errorf("diagnose --server for no key: %v, want %q", err, want)
	}
}

func TestApiaryDiagnoseStopsAtASecretOthersCanRead(t *testing.T) {
	s, dir := diagnoseKeyed(t)
	secret := filepath.Join(dir, apiarydir.SecretFile)
	if err := os.Chmod(secret, 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil || out != "" {
		t.Fatalf("diagnose with a secret of mode 0644: err %v, stdout %q", err, out)
	}
	if want := "Run `chmod 600 " + secret + "`"; !strings.HasPrefix(firstLine(err.Error()), want) {
		t.Errorf("first line %q does not start with %q", firstLine(err.Error()), want)
	}
	if s.requests != 0 {
		t.Errorf("the request went out with a secret others can read")
	}
}

func TestApiaryDiagnoseNetworkError(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	url := s.URL
	s.Close()
	_, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil {
		t.Fatal("diagnose of a server that is gone succeeded")
	}
	wantIn(t, firstLine(err.Error()), "Check that "+url+" is reachable: ")
}

func TestApiaryDiagnoseNeverPrintsTheSecretOrTheSignature(t *testing.T) {
	emptyDir(t)
	key := newKey(t)
	s := newDiagnosticsServer(t, newKey(t))
	dir := storeDiagnosticsKey(t, s.URL, s.pin(), key)
	b, err := os.ReadFile(filepath.Join(dir, apiarydir.SecretFile))
	if err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil {
		t.Fatal("diagnose with a key the server does not accept succeeded")
	}
	all := out + errOut + err.Error()
	for _, unwanted := range []string{strings.TrimSpace(string(b)), accesskey.SecretPrefix, accesskey.HeaderSignature, accesskey.HeaderTimestamp} {
		if strings.Contains(all, unwanted) {
			t.Errorf("the output contains %q:\n%s", unwanted, all)
		}
	}
}

func TestApiaryDiagnoseCountsOnlyDirectoriesWithAKey(t *testing.T) {
	s, dir := diagnoseKeyed(t)
	// A directory whose key was moved aside, as a refused enrolment leaves it, is no
	// server with a key.
	other := filepath.Join(filepath.Dir(dir), "apiary.example")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, apiarydir.SecretFile+".old.1700000000"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err != nil {
		t.Fatalf("diagnose: %v\n%s", err, errOut)
	}
	if out != string(s.body) {
		t.Errorf("stdout is not the document:\n%s", out)
	}
}

func TestApiaryDiagnoseStopsAtAServerURLWithAFragment(t *testing.T) {
	s, _ := diagnoseKeyed(t)
	for _, server := range []string{s.URL + "#", s.URL + "#x", s.URL + "/#"} {
		_, _, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID, "--server", server)
		want := "Give the server as a URL of its host alone, such as https://apiary.example: the URL has a \"#\"."
		if err == nil || firstLine(err.Error()) != want {
			t.Errorf("--server %s: %v, want %q", server, err, want)
		}
		if cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("--server %s: exit %d", server, cmd.ExitCode(err))
		}
	}
	if s.requests != 0 {
		t.Errorf("%d requests sent", s.requests)
	}
}

// hostOf is the name of the one server directory the test stored.
func hostOf(t *testing.T) string {
	t.Helper()
	root, err := apiarydir.Root()
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := apiarydir.Hosts(root)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("hosts %v, %v", hosts, err)
	}
	return hosts[0]
}

// wantIn fails the test when s does not contain want.
func wantIn(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Errorf("%q lacks %q", s, want)
	}
}
