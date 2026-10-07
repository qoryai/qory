package cmd_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/runner/accesskey"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/runnerdir"
)

// enrolServer stands in for a server's enrolment endpoint: it checks each request's
// proof and answers with the status and body it is set to, signed under its signing
// key, or under another key, or not at all. It keeps the requests it verified and the
// User-Agent of each.
type enrolServer struct {
	*httptest.Server
	signer *accesskey.Key
	// next is the server's next key, listed after signer while rotate is set.
	next *accesskey.Key
	mu   sync.Mutex
	// status and body are the answer; a nil body is the 201 of [enrolServer.answer].
	status int
	body   []byte
	// signBy signs the answer, nil for an unsigned one.
	signBy   *accesskey.Key
	rotate   bool
	requests []accesskey.EnrolmentRequest
	agents   []string
}

func newEnrolServer(t *testing.T) *enrolServer {
	t.Helper()
	s := &enrolServer{signer: newKey(t), next: newKey(t), status: http.StatusCreated}
	s.signBy = s.signer
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		var req accesskey.EnrolmentRequest
		if r.Method != http.MethodPost || r.URL.Path != accesskey.EnrolmentPath || json.Unmarshal(b, &req) != nil || !req.VerifyProof() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.requests = append(s.requests, req)
		s.agents = append(s.agents, r.Header.Get("User-Agent"))
		body := s.body
		if body == nil {
			body = s.answer(true)
		}
		if s.signBy != nil && s.status != http.StatusUnauthorized {
			w.Header().Set(accesskey.HeaderSignature, s.signBy.SignAnswer(accesskey.Answer{Enrolment: true, Status: s.status, RequestSignature: req.Proof, Body: body}))
		}
		w.WriteHeader(s.status)
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

// keys is the server's keys as an answer lists them: the signer, then the next key
// while rotate is set.
func (s *enrolServer) keys() string {
	l := `{"alg":"ed25519","public_key":"` + s.signer.PublicKey().String() + `"}`
	if s.rotate {
		l += `,{"alg":"ed25519","public_key":"` + s.next.PublicKey().String() + `"}`
	}
	return "[" + l + "]"
}

// answer is the 201 for the node nd_0123456789abcdef.
func (s *enrolServer) answer(node bool) []byte {
	id, kind := "nd_0123456789abcdef", "node"
	if !node {
		id, kind = "np_0123456789abcdef", "pool"
	}
	return []byte(`{"version":1,"access_key_id":"ak_0123456789abcdef","node_id":"` + id + `","node_kind":"` + kind + `","stored_secrets":false,"apiary_public_key":` + s.keys() + `}`)
}

// refusal is a signed 409 of code, listing the server's keys.
func (s *enrolServer) refusal(code string, names ...string) {
	b, _ := json.Marshal(names)
	s.status = http.StatusConflict
	s.body = []byte(`{"error":"` + code + `","names":` + string(b) + `,"apiary_public_key":` + s.keys() + `}`)
	if len(names) == 0 {
		s.body = []byte(`{"error":"` + code + `","apiary_public_key":` + s.keys() + `}`)
	}
}

// code is an enrolment code of the server, in groups, carrying the signer's
// fingerprint, and the next key's too when both is set.
func (s *enrolServer) code(n byte, both bool) string {
	c := "qec_F1XT-0RE0-0000-0000-0000-0000-0" + string('0'+n) + "." + s.signer.Fingerprint()
	if both {
		c += "." + s.next.Fingerprint()
	}
	return c
}

// sent is the requests the server verified.
func (s *enrolServer) sent() []accesskey.EnrolmentRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]accesskey.EnrolmentRequest(nil), s.requests...)
}

// runnerFile is the path of the test environment's runner file.
func runnerFile() string { return filepath.Join(string(configDir()), config.RunnerFileName) }

// readRunnerFile reads the runner file.
func readRunnerFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(runnerFile())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// heldKey is the access key access-key-secret holds.
func heldKey(t *testing.T) *accesskey.Key {
	t.Helper()
	k, err := configDir().ReadSecret()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// mode is a file's permission bits.
func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// exists reports whether a path exists.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// movedAside is the number of secrets moved aside.
func movedAside(t *testing.T) int {
	t.Helper()
	old, err := configDir().OldSecrets()
	if err != nil {
		t.Fatal(err)
	}
	return len(old)
}

// commentedRunner is a runner file with comments and sections enrol leaves alone.
const commentedRunner = `# This machine's runner file.
apiVersion: qory.dev/v1alpha1
egress:
  mode: observe  # everything, for now
# The name the server shows.
instance:
  name: build-01
run:
  timeout: 5h
`

// TestEnrolWritesTheServerSectionAndKeepsTheRest is an enrolment with a code typed in
// groups: qory writes the marker and the secret, mode 0600, in the directory made
// mode 0700, prints the fingerprint before it posts, sends the normalised code, the
// name instance.name sets and the User-Agent of the runner's requests, then writes the
// server's URL, the key's id and the pin into the runner file, keeping its comments and
// every other key, and removes the pending enrolment.
func TestEnrolWritesTheServerSectionAndKeepsTheRest(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	writeFile(t, runnerFile(), commentedRunner)
	out, errOut, err := runSplit(t, "", "access-key", "enrol", srv.URL+"/", srv.code(1, false))
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if out != "" {
		t.Errorf("stdout carries %q", out)
	}
	key := heldKey(t)
	fp := key.Fingerprint()
	if want := string(configDir()) + " is now mode 0700: it holds the access key's secret\n" +
		"access key fingerprint " + fp + "\n" +
		"enrolled as ak_0123456789abcdef in the node nd_0123456789abcdef\n" +
		"stored secrets: no\n" +
		"the key is active: runs can start\n" +
		"wrote server.url, server.access_key_id and server.apiary_public_key to " + runnerFile() + "\n"; errOut != want {
		t.Errorf("stderr\n%s\nwant\n%s", errOut, want)
	}
	if strings.Contains(out+errOut, key.Secret()) {
		t.Error("the secret was printed")
	}
	sent := srv.sent()
	if len(sent) != 1 || sent[0].Code != "qec_F1XT0RE0000000000000000001."+srv.signer.Fingerprint() || sent[0].Name != "build-01" || sent[0].PublicKey != key.PublicKey().String() {
		t.Fatalf("sent %+v", sent)
	}
	if !strings.HasPrefix(srv.agents[0], "qory-runner/") {
		t.Errorf("User-Agent %q", srv.agents[0])
	}
	dir := configDir()
	if m := mode(t, string(dir)); m != 0o700 {
		t.Errorf("the directory is mode %v", m)
	}
	for _, name := range []string{runnerdir.SecretFile, runnerdir.MarkerFile} {
		if m := mode(t, dir.Path(name)); m != 0o600 {
			t.Errorf("%s is mode %v", name, m)
		}
	}
	if exists(dir.Path(runnerdir.PendingFile)) {
		t.Error("enrolment-pending is still there")
	}
	got := readRunnerFile(t)
	wants(t, got, "# This machine's runner file.", "mode: observe # everything, for now", "# The name the server shows.", "name: build-01", "timeout: 5h",
		"server:\n  url: "+srv.URL+"\n  access_key_id: ak_0123456789abcdef\n  apiary_public_key:\n    - {alg: ed25519, public_key: "+srv.signer.PublicKey().String()+"}\n")
	if strings.Index(got, "egress:") > strings.Index(got, "instance:") || strings.Index(got, "instance:") > strings.Index(got, "run:") {
		t.Errorf("the sections moved:\n%s", got)
	}
	r, err := config.LoadRunner()
	if err != nil {
		t.Fatal(err)
	}
	if r.Server.URL != srv.URL || r.Server.AccessKeyID != "ak_0123456789abcdef" || len(r.Server.Pin) != 1 || r.InstanceName != "build-01" || r.Timeout.Hours() != 5 {
		t.Errorf("the runner file reads as %+v, %+v", r, r.Server)
	}
}

// TestEnrolCreatesTheRunnerFileAndPinsOnlyTheCodesKeys is an enrolment on a machine
// with no runner file during a rotation of the server's key: the answer lists both of
// the server's keys, and the code carries the current key's fingerprint alone, so the
// new file, mode 0600, pins that key alone. A node pool is said as such.
func TestEnrolCreatesTheRunnerFileAndPinsOnlyTheCodesKeys(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	srv.rotate = true
	srv.body = srv.answer(false)
	out, err := run(t, "access-key", "enrol", srv.URL, srv.code(1, false))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "in the node pool np_0123456789abcdef", "the key is active: runs can start")
	if m := mode(t, runnerFile()); m != 0o600 {
		t.Errorf("the runner file is mode %v", m)
	}
	got := readRunnerFile(t)
	if !strings.HasPrefix(got, "apiVersion: qory.dev/v1alpha1\nserver:\n") || strings.Contains(got, srv.next.PublicKey().String()) {
		t.Errorf("the runner file:\n%s", got)
	}
	r, err := config.LoadRunner()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Server.Pin) != 1 || r.Server.Pin[0].PublicKey != srv.signer.PublicKey().String() {
		t.Errorf("the pin %v", r.Server.Pin)
	}
}

// TestEnrolKeepsThePinThereIs is a machine that pins the server's current key, and a
// code made during a rotation, carrying both fingerprints: the answer would pin both,
// and qory writes the key's id and leaves the pin as it is.
func TestEnrolKeepsThePinThereIs(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	srv.rotate = true
	before := "server:\n  url: " + srv.URL + "  # the server\n  apiary_public_key: " + pinLine(srv.signer) + "  # pinned by hand\n  access_key_id: ak_0000000000000000\ninstance:\n  name: build-01\n"
	writeFile(t, runnerFile(), before)
	out, err := run(t, "access-key", "enrol", srv.URL, srv.code(2, true))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "wrote server.access_key_id to "+runnerFile())
	got := readRunnerFile(t)
	if strings.Contains(got, srv.next.PublicKey().String()) || !strings.Contains(got, "# pinned by hand") || !strings.Contains(got, "access_key_id: ak_0123456789abcdef") || strings.Contains(got, "ak_0000000000000000") {
		t.Errorf("the runner file:\n%s", got)
	}
}

// TestEnrolRefusesBeforeItMakesAKey is each refusal that comes before a key is made: a
// code for a server the pin does not name, another server than the runner file's, a
// code that is not one, a server that is not https, the access key's id or the pin in
// the environment, and a run without a wall that is live. Nothing is written and
// nothing is sent.
func TestEnrolRefusesBeforeItMakesAKey(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	other := newKey(t)
	pinned := "server:\n  url: " + srv.URL + "\n  apiary_public_key: " + pinLine(other) + "\ninstance:\n  name: build-01\n"
	for _, c := range []struct {
		name, file string
		env        map[string]string
		args       []string
		want       string
	}{
		{"another server's code", pinned, nil, []string{srv.URL, srv.code(1, false)}, "the enrolment code is for a server whose key this machine does not pin: the code is from another server, or the pin is out of date"},
		{"another server's code, the pin from the environment", "instance:\n  name: build-01\n", map[string]string{"QORY_APIARY_PUBLIC_KEY": `[{"alg":"ed25519","public_key":"` + other.PublicKey().String() + `"}]`}, []string{"--print", srv.URL, srv.code(1, false)}, "the enrolment code is for a server whose key this machine does not pin"},
		{"another server", "server:\n  url: https://apiary.example\n", nil, []string{srv.URL, srv.code(1, false)}, runnerFile() + " names the server https://apiary.example, and this command " + srv.URL + ": enrol with the server runner.yaml names, or change its server.url first"},
		{"not a code", "", nil, []string{srv.URL, "qec_F1XT0RE000000000000000000U." + srv.signer.Fingerprint()}, "which is not a character of Crockford's base32"},
		{"plain http elsewhere", "", nil, []string{"http://apiary.example", srv.code(1, false)}, `server.url "http://apiary.example" is http to a host that is not this machine`},
		{"a path", "", nil, []string{"https://apiary.example/x", srv.code(1, false)}, "is more than a scheme and a host"},
		{"http to another loopback address", "", nil, []string{"http://127.0.0.2:8080", srv.code(1, false)}, "the server http://127.0.0.2:8080 is http to a host other than localhost, 127.0.0.1 or [::1], which enrolment does not reach; use https"},
		{"the id in the environment", "", map[string]string{"QORY_ACCESS_KEY_ID": "ak_0123456789abcdef"}, []string{srv.URL, srv.code(1, false)}, "QORY_ACCESS_KEY_ID is set, and qory access-key enrol keeps the key in this machine's files, which it would contradict: unset it, or use --print"},
		{"the secret in the environment", "", map[string]string{"QORY_ACCESS_KEY_SECRET": newKey(t).Secret()}, []string{srv.URL, srv.code(1, false)}, "QORY_ACCESS_KEY_SECRET is set, and qory access-key enrol keeps the key in this machine's files, which it would contradict: unset it, or use --print"},
		{"all three in the environment", "", map[string]string{"QORY_ACCESS_KEY_ID": "ak_0123456789abcdef", "QORY_ACCESS_KEY_SECRET": newKey(t).Secret(), "QORY_APIARY_PUBLIC_KEY": `[{"alg":"ed25519","public_key":"` + srv.signer.PublicKey().String() + `"}]`}, []string{srv.URL, srv.code(1, false)}, "QORY_ACCESS_KEY_ID, QORY_ACCESS_KEY_SECRET and QORY_APIARY_PUBLIC_KEY are set, and qory access-key enrol keeps the key in this machine's files, which they would contradict: unset them, or use --print"},
		{"the pin in the environment", "", map[string]string{"QORY_APIARY_PUBLIC_KEY": `[{"alg":"ed25519","public_key":"` + srv.signer.PublicKey().String() + `"}]`}, []string{srv.URL, srv.code(1, false)}, "QORY_APIARY_PUBLIC_KEY is set, and qory access-key enrol keeps the key"},
	} {
		os.RemoveAll(string(configDir()))
		if c.file != "" {
			writeFile(t, runnerFile(), c.file)
		}
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		_, err := run(t, append([]string{"access-key", "enrol"}, c.args...)...)
		if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		for k := range c.env {
			t.Setenv(k, "")
		}
		dir := configDir()
		for _, name := range []string{runnerdir.SecretFile, runnerdir.MarkerFile, runnerdir.PendingFile} {
			if exists(dir.Path(name)) {
				t.Errorf("%s: %s was written", c.name, name)
			}
		}
	}
	if n := len(srv.sent()); n != 0 {
		t.Errorf("%d requests were sent", n)
	}

	// A run without a wall that is live: its lock is held.
	os.RemoveAll(string(configDir()))
	writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
	lock, err := configDir().LockRun("0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"enrol", srv.URL, srv.code(1, false)}, {"enrol", "--print", srv.URL, srv.code(1, false)}, {"create"}, {"create", "--print"}} {
		out, err := run(t, append([]string{"access-key"}, args...)...)
		if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "a run without a wall is running on this machine (0191f2a4-3c5e-7b8d-9e0f-1a2b3c4d5e6f): it must end before a key is made") {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
	}
	if exists(configDir().Path(runnerdir.SecretFile)) || len(srv.sent()) != 0 {
		t.Error("a key was made while an unwalled run was live")
	}
	lock.Release()
}

// TestEnrolRetriesWithTheSameKey is an answer that does not verify, an unsigned 429
// and a signed 201 under another key, and then the same code again: each keeps the
// secret and the pending enrolment and writes nothing into the runner file, and the
// retry sends the same public key. Another code then is refused and keeps the enrolled
// key.
func TestEnrolRetriesWithTheSameKey(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
	code := srv.code(3, false)
	dir := configDir()
	for _, c := range []struct {
		name   string
		status int
		body   []byte
		signBy *accesskey.Key
		want   string
	}{
		{"unsigned 429", http.StatusTooManyRequests, []byte(`{"error":"rate_limited"}`), nil, "the server did not enrol the key (HTTP 429, unsigned); try again later"},
		{"unsigned 400", http.StatusBadRequest, []byte(`{"error":"invalid_request"}`), nil, "(HTTP 400, unsigned)"},
		{"signed by another key", http.StatusCreated, nil, newKey(t), "(HTTP 201, unsigned)"},
		{"a signed 409 under another key", http.StatusConflict, []byte(`{"error":"key_limit","apiary_public_key":[{"alg":"ed25519","public_key":"` + srv.signer.PublicKey().String() + `"}]}`), newKey(t), "(HTTP 409, unsigned)"},
	} {
		srv.status, srv.body, srv.signBy = c.status, c.body, c.signBy
		_, err := run(t, "access-key", "enrol", srv.URL, code)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
		if !exists(dir.Path(runnerdir.SecretFile)) || !exists(dir.Path(runnerdir.PendingFile)) || movedAside(t) != 0 {
			t.Errorf("%s: the secret or the pending enrolment is gone", c.name)
		}
		if got := readRunnerFile(t); got != "instance:\n  name: build-01\n" {
			t.Errorf("%s: the runner file changed:\n%s", c.name, got)
		}
	}
	srv.status, srv.body, srv.signBy = http.StatusCreated, nil, srv.signer
	head, fingerprint, _ := strings.Cut(code, ".")
	out, err := run(t, "access-key", "enrol", srv.URL, strings.ToLower(head)+"."+fingerprint)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "this code was tried within the last 15 minutes: retrying with the key made for it")
	sent := srv.sent()
	for _, r := range sent[1:] {
		if r.PublicKey != sent[0].PublicKey {
			t.Fatal("a retry sent another key")
		}
	}
	if heldKey(t).PublicKey().String() != sent[0].PublicKey {
		t.Error("access-key-secret holds another key")
	}
	if _, err := run(t, "access-key", "enrol", srv.URL, srv.code(4, false)); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), keyHere) {
		t.Errorf("another code: %v", err)
	}
	if len(srv.sent()) != len(sent) || heldKey(t).PublicKey().String() != sent[0].PublicKey || movedAside(t) != 0 {
		t.Error("another code replaced the enrolled key")
	}
}

// keyHere is the refusal of an enrolment on a machine that holds a key, after the path
// of access-key-secret.
const keyHere = " already holds this machine's access key secret, and enrol does not replace it: move it aside yourself first to enrol a new key, or use --print for a key kept elsewhere"

// TestEnrolNeverReplacesTheKey is a successful enrolment, then the same command again,
// another code, and the same code once its 15 minutes are over: each is refused before
// it makes a key or sends anything, and names access-key-secret; the secret, the runner
// file and the rest of the directory stay as they are, and nothing is moved aside.
func TestEnrolNeverReplacesTheKey(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
	code := srv.code(1, false)
	out, err := run(t, "access-key", "enrol", srv.URL, code)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	dir := configDir()
	key := heldKey(t)
	expired := func() {
		normal, err := accesskey.NormaliseCode(code)
		if err != nil {
			t.Fatal(err)
		}
		if err := dir.WritePending(normal, key.PublicKey(), time.Now().Add(-runnerdir.PendingFor-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		setup func()
		code  string
	}{
		{"the same command again", func() {}, code},
		{"another code", func() {}, srv.code(2, false)},
		{"the same code after its 15 minutes", expired, code},
	} {
		c.setup()
		before := snapshot(t, string(dir))
		_, err := run(t, "access-key", "enrol", srv.URL, c.code)
		if cmd.ExitCode(err) != cmd.ExitInput || err.Error() != dir.Path(runnerdir.SecretFile)+keyHere {
			t.Errorf("%s: %v", c.name, err)
		}
		after := snapshot(t, string(dir))
		delete(after, filepath.Join("locks", runnerdir.KeyLock))
		delete(before, filepath.Join("locks", runnerdir.KeyLock))
		for p, v := range after {
			if before[p] != v {
				t.Errorf("%s: %s changed", c.name, p)
			}
		}
		if len(after) != len(before) {
			t.Errorf("%s: files before %v, after %v", c.name, before, after)
		}
		if heldKey(t).PublicKey() != key.PublicKey() || movedAside(t) != 0 {
			t.Errorf("%s: the key was replaced", c.name)
		}
		if n := len(srv.sent()); n != 1 {
			t.Errorf("%s: %d enrolments sent", c.name, n)
		}
	}
}

// TestEnrolAfterARefusal is a key_limit, after which the same code succeeds with the
// same key, and a 401, after which the secret made for the code is moved aside and
// another code enrols a new key.
func TestEnrolAfterARefusal(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
	srv.refusal("key_limit")
	if _, err := run(t, "access-key", "enrol", srv.URL, srv.code(1, false)); err == nil || !strings.Contains(err.Error(), "the node already holds two keys") {
		t.Fatalf("key_limit: %v", err)
	}
	srv.status, srv.body = http.StatusCreated, nil
	out, err := run(t, "access-key", "enrol", srv.URL, srv.code(1, false))
	if err != nil {
		t.Fatalf("the same code after key_limit: %v\n%s", err, out)
	}
	wants(t, out, "retrying with the key made for it", "enrolled as ak_0123456789abcdef")
	sent := srv.sent()
	if len(sent) != 2 || sent[1].PublicKey != sent[0].PublicKey || heldKey(t).PublicKey().String() != sent[0].PublicKey {
		t.Error("the same code after key_limit did not keep the key")
	}

	emptyDir(t)
	writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
	srv.status, srv.body = http.StatusUnauthorized, []byte(`{"error":"unauthorized"}`)
	if _, err := run(t, "access-key", "enrol", srv.URL, srv.code(2, false)); err == nil || !strings.Contains(err.Error(), "this code was used or has expired") {
		t.Fatalf("unauthorized: %v", err)
	}
	if exists(configDir().Path(runnerdir.SecretFile)) || movedAside(t) != 1 {
		t.Fatal("the secret made for the used code was not moved aside")
	}
	srv.status, srv.body = http.StatusCreated, nil
	out, err = run(t, "access-key", "enrol", srv.URL, srv.code(3, false))
	if err != nil {
		t.Fatalf("a new code after unauthorized: %v\n%s", err, out)
	}
	sent = srv.sent()
	if last := sent[len(sent)-1]; last.PublicKey == sent[len(sent)-2].PublicKey || heldKey(t).PublicKey().String() != last.PublicKey {
		t.Error("a new code after unauthorized did not make a new key")
	}
	wants(t, readRunnerFile(t), "access_key_id: ak_0123456789abcdef")
}

// TestEnrolRetriesOnlyWithTheKeyMadeForTheCode is a key_limit, after which the key made
// for the code is moved aside by hand and another key takes its place, by create or by
// hand, before the same code is run again within its 15 minutes: the other key is this
// machine's key, so the same code is refused before it sends anything, as any other
// code would be, and the key is not moved aside. create removes the pending enrolment.
func TestEnrolRetriesOnlyWithTheKeyMadeForTheCode(t *testing.T) {
	for _, byCreate := range []bool{true, false} {
		emptyDir(t)
		srv := newEnrolServer(t)
		writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
		dir := configDir()
		code := srv.code(1, false)
		srv.refusal("key_limit")
		if _, err := run(t, "access-key", "enrol", srv.URL, code); err == nil || !strings.Contains(err.Error(), "the node already holds two keys") {
			t.Fatalf("key_limit: %v", err)
		}
		if !exists(dir.Path(runnerdir.PendingFile)) {
			t.Fatal("key_limit ended the pending enrolment")
		}
		if err := os.Rename(dir.Path(runnerdir.SecretFile), dir.Path("kept-by-hand")); err != nil {
			t.Fatal(err)
		}
		if byCreate {
			if out, err := run(t, "access-key", "create"); err != nil {
				t.Fatalf("create: %v\n%s", err, out)
			}
			if exists(dir.Path(runnerdir.PendingFile)) {
				t.Error("create left the pending enrolment")
			}
		} else if err := dir.WriteSecret(newKey(t)); err != nil {
			t.Fatal(err)
		}
		mine := heldKey(t)
		srv.status, srv.body = http.StatusUnauthorized, []byte(`{"error":"unauthorized"}`)
		out, err := run(t, "access-key", "enrol", srv.URL, code)
		if cmd.ExitCode(err) != cmd.ExitInput || err.Error() != dir.Path(runnerdir.SecretFile)+keyHere {
			t.Errorf("create %v: the same code over another key: %v", byCreate, err)
		}
		lacks(t, out, "retrying")
		if n := len(srv.sent()); n != 1 {
			t.Errorf("create %v: %d enrolments sent", byCreate, n)
		}
		if heldKey(t).PublicKey() != mine.PublicKey() || movedAside(t) != 0 {
			t.Errorf("create %v: the machine's key was moved or replaced", byCreate)
		}
	}
}

// TestCreateEndsAPendingEnrolment is a pending enrolment create finds: the key it makes
// was made by no enrolment, so it removes the record.
func TestCreateEndsAPendingEnrolment(t *testing.T) {
	emptyDir(t)
	dir := configDir()
	if _, err := dir.Ensure(); err != nil {
		t.Fatal(err)
	}
	normal, err := accesskey.NormaliseCode("qec_F1XT-0RE0-0000-0000-0000-0000-01.uoES-kuj1vk0sq0qoGlmAg")
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.WritePending(normal, newKey(t).PublicKey(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "access-key", "create"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if exists(dir.Path(runnerdir.PendingFile)) {
		t.Error("create left the pending enrolment")
	}
}

// TestEnrolActsOnTheRefusalsCode is each refusal of the server: unauthorized and a
// signed key_invalid move the secret aside, end the pending enrolment and say a new
// code is needed; a signed key_limit keeps both and says the same command succeeds once
// a key is revoked. The runner file stays as it is.
func TestEnrolActsOnTheRefusalsCode(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	dir := configDir()
	for i, c := range []struct {
		name  string
		setup func()
		keep  bool
		want  []string
	}{
		{"unauthorized", func() { srv.status, srv.body = http.StatusUnauthorized, []byte(`{"error":"unauthorized"}`) }, false, []string{
			"this code was used or has expired; if you did not use it, tell your administrator, who must revoke the key it enrolled. Enrolling needs a new code; the secret made for it was moved aside to " + dir.Path(runnerdir.OldPrefix),
			"(enrolment: the code was used, has expired or was cancelled: unauthorized (status 401))"}},
		{"key_invalid", func() { srv.refusal("key_invalid", "public_key") }, false, []string{
			"the server refused the key (public_key). Enrolling needs a new code; the secret made for it was moved aside to "}},
		{"key_limit", func() { srv.refusal("key_limit") }, true, []string{
			"the node already holds two keys: once an owner or administrator has revoked one, the same command, run within the code's 15 minutes, succeeds"}},
	} {
		os.RemoveAll(string(dir))
		writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
		c.setup()
		_, err := run(t, "access-key", "enrol", srv.URL, srv.code(byte(i), false))
		if err == nil || cmd.ExitCode(err) == cmd.ExitInput {
			t.Fatalf("%s: %v", c.name, err)
		}
		wants(t, err.Error(), c.want...)
		if exists(dir.Path(runnerdir.SecretFile)) != c.keep || exists(dir.Path(runnerdir.PendingFile)) != c.keep || (movedAside(t) == 1) == c.keep {
			t.Errorf("%s: secret %v, pending %v, moved aside %d", c.name, exists(dir.Path(runnerdir.SecretFile)), exists(dir.Path(runnerdir.PendingFile)), movedAside(t))
		}
		if got := readRunnerFile(t); got != "instance:\n  name: build-01\n" {
			t.Errorf("%s: the runner file changed:\n%s", c.name, got)
		}
	}
}

// TestEnrolRateLimitedKeepsTheKey is the server's signed 429, rate_limited: the same
// command later retries, so the secret and the pending enrolment stay, and the runner
// file is not changed; with --print the text is the same. An unsigned 429 is an answer
// that does not verify, as any other. A runner that does not verify a signed 429 reads it
// as unsigned, and the test is skipped.
func TestEnrolRateLimitedKeepsTheKey(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	dir := configDir()
	writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
	srv.status, srv.body, srv.signBy = http.StatusTooManyRequests, []byte(`{"error":"rate_limited"}`), nil
	_, err := run(t, "access-key", "enrol", srv.URL, srv.code(1, false))
	if want := "the server did not enrol the key (HTTP 429, unsigned); try again later (enrolment: answer_unsigned (status 429))"; err == nil || err.Error() != want {
		t.Errorf("an unsigned 429: %v, want %q", err, want)
	}
	if !exists(dir.Path(runnerdir.SecretFile)) || !exists(dir.Path(runnerdir.PendingFile)) || movedAside(t) != 0 {
		t.Error("an unsigned 429: the secret or the pending enrolment is gone")
	}
	srv.status, srv.body, srv.signBy = http.StatusTooManyRequests, []byte(`{"error":"rate_limited","apiary_public_key":`+srv.keys()+`}`), srv.signer
	_, err = run(t, "access-key", "enrol", srv.URL, srv.code(1, false))
	if err != nil && strings.Contains(err.Error(), "(HTTP 429, unsigned)") {
		t.Skip("needs the runner pin with rate_limited")
	}
	const want = "the server refused the attempt: this code was tried too often; run the same command again later, within the code's 15 minutes (rate_limited)"
	if err == nil || cmd.ExitCode(err) == cmd.ExitInput || err.Error() != want {
		t.Errorf("a signed 429: %v, want %q", err, want)
	}
	if !exists(dir.Path(runnerdir.SecretFile)) || !exists(dir.Path(runnerdir.PendingFile)) || movedAside(t) != 0 {
		t.Error("a signed 429: the secret or the pending enrolment is gone")
	}
	if got := readRunnerFile(t); got != "instance:\n  name: build-01\n" {
		t.Errorf("the runner file changed:\n%s", got)
	}
	if _, _, err := runSplit(t, "", "access-key", "enrol", "--print", srv.URL, srv.code(2, false)); err == nil || err.Error() != want {
		t.Errorf("a signed 429 with --print: %v, want %q", err, want)
	}
}

// TestEnrolAfterTheWorkspaceKeysAreRemoved is a runner file that still holds a
// workspace access key: enrol refuses it, and the refusal says to remove the keys
// first, since enrol reads the file too. Once they are removed and the server section
// keeps only its URL, the same command enrols.
func TestEnrolAfterTheWorkspaceKeysAreRemoved(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	for i, c := range []struct{ keys, want string }{
		{"  access_key: ak_f1xt0re000000000\n  secret: qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA\n", "server.access_key is a workspace access key, which servers no longer accept; remove server.access_key and server.secret from runner.yaml, then enrol this machine as a node: qory access-key enrol <server> <code>"},
		{"  secret: qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA\n", "server.secret is a workspace access key's secret, which servers no longer accept; remove server.access_key and server.secret from runner.yaml, then enrol this machine as a node: qory access-key enrol <server> <code>"},
	} {
		os.RemoveAll(string(configDir()))
		writeFile(t, runnerFile(), "server:\n  url: "+srv.URL+"\n"+c.keys+"instance:\n  name: build-01\n")
		_, err := run(t, "access-key", "enrol", srv.URL, srv.code(byte(i), false))
		if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
			t.Fatalf("%d: %v", i, err)
		}
		wants(t, err.Error(), runnerFile()+": "+c.want)
		if exists(configDir().Path(runnerdir.SecretFile)) {
			t.Errorf("%d: a key was made", i)
		}
	}
	if len(srv.sent()) != 0 {
		t.Fatalf("sent %+v", srv.sent())
	}
	writeFile(t, runnerFile(), "server:\n  url: "+srv.URL+"\ninstance:\n  name: build-01\n")
	out, err := run(t, "access-key", "enrol", srv.URL, srv.code(3, false))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "enrolled as ak_0123456789abcdef", "wrote server.access_key_id and server.apiary_public_key to "+runnerFile())
	if len(srv.sent()) != 1 {
		t.Errorf("sent %d requests", len(srv.sent()))
	}
}

// TestEnrolRefusesAFixturePin is a server that signs with the runner contract's
// published fixture signing key: its answer verifies, and qory refuses to pin it.
// --print, on a machine with a key of its own, says nothing was written, and no file of
// the directory changes. Without it, once that key is moved aside by hand, the runner
// file is left as it is, the secret made for the code is moved aside, the pending
// enrolment ends, and the message names the secret's new path.
func TestEnrolRefusesAFixturePin(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	seed, err := base64.RawURLEncoding.DecodeString("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVpbXF1eX2A")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := accesskey.NewKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	srv.signer, srv.signBy = fixture, fixture
	writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
	own := newKey(t)
	writeSecret(t, own)
	dir := configDir()
	const refused = "the server's answer lists the runner contract's published fixture key, whose secret anyone can read: it is no server to pin; "

	// --print writes nothing.
	before := snapshot(t, string(dir))
	out, errOut, err := runSplit(t, "", "access-key", "enrol", "--print", srv.URL, srv.code(2, false))
	if err == nil || err.Error() != refused+"nothing was written" {
		t.Errorf("--print: %v", err)
	}
	if strings.Contains(out, "QORY_") || strings.Contains(errOut, "QORY_ACCESS_KEY_SECRET=") {
		t.Errorf("--print printed the key:\n%s", out)
	}
	after := snapshot(t, string(dir))
	delete(after, "locks")
	delete(after, filepath.Join("locks", runnerdir.KeyLock))
	delete(before, "locks")
	delete(before, filepath.Join("locks", runnerdir.KeyLock))
	for p, v := range after {
		if before[p] != v {
			t.Errorf("--print: %s changed", p)
		}
	}
	if len(after) != len(before) {
		t.Errorf("--print: files before %v, after %v", before, after)
	}

	// Without --print, once the machine's own key is moved aside by hand, the runner
	// file stays and the secret made for the code is moved aside.
	if err := os.Rename(dir.Path(runnerdir.SecretFile), dir.Path("own-secret")); err != nil {
		t.Fatal(err)
	}
	out, errOut, enrolErr := runSplit(t, "", "access-key", "enrol", srv.URL, srv.code(1, false))
	if enrolErr == nil {
		t.Fatalf("enrolled:\n%s%s", out, errOut)
	}
	if strings.Contains(out, "QORY_") || strings.Contains(errOut, "QORY_ACCESS_KEY_SECRET=") {
		t.Errorf("printed the key:\n%s", out)
	}
	old, err := dir.OldSecrets()
	if err != nil || len(old) != 1 {
		t.Fatalf("moved aside %v, %v", old, err)
	}
	b, err := os.ReadFile(dir.Path(old[0]))
	if err != nil {
		t.Fatal(err)
	}
	k, err := runnerdir.ParseSecret(b)
	if err != nil {
		t.Fatal(err)
	}
	sent := srv.sent()
	if k.PublicKey().String() != sent[len(sent)-1].PublicKey {
		t.Fatal("the secret moved aside is not the one made for the code")
	}
	want := refused + "runner.yaml was not changed; the secret made for it was moved aside to " + dir.Path(old[0])
	if enrolErr.Error() != want {
		t.Errorf("the refusal\n%v\nwant\n%s", enrolErr, want)
	}
	if exists(dir.Path(runnerdir.SecretFile)) || exists(dir.Path(runnerdir.PendingFile)) {
		t.Errorf("secret %v, pending %v", exists(dir.Path(runnerdir.SecretFile)), exists(dir.Path(runnerdir.PendingFile)))
	}
	if got := readRunnerFile(t); got != "instance:\n  name: build-01\n" {
		t.Errorf("the runner file changed:\n%s", got)
	}
}

// TestEnrolPrintWritesNothing is --print on a machine with a key of its own and a
// runner file naming another server and pinning another key, which --print leaves
// aside: stdout is exactly the three settings, unquoted, the pin compact JSON of
// the keys the code carries; stderr says the rest; no file of the directory changes,
// and the machine's own key stays.
func TestEnrolPrintWritesNothing(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	srv.rotate = true
	// The machine's own server and pin are another's: the key is for another machine.
	writeFile(t, runnerFile(), commentedRunner+"server:\n  url: https://apiary.example\n  access_key_id: ak_0000000000000000\n  apiary_public_key: "+pinLine(newKey(t))+"\n")
	own := newKey(t)
	writeSecret(t, own)
	dir := configDir()
	before := snapshot(t, string(dir))
	out, errOut, err := runSplit(t, "", "access-key", "enrol", "--print", srv.URL, srv.code(1, true))
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "QORY_ACCESS_KEY_ID=") || !strings.HasPrefix(lines[1], "QORY_ACCESS_KEY_SECRET=") || !strings.HasPrefix(lines[2], "QORY_APIARY_PUBLIC_KEY=") {
		t.Fatalf("stdout:\n%s", out)
	}
	if lines[0] != "QORY_ACCESS_KEY_ID=ak_0123456789abcdef" {
		t.Errorf("the id line %q", lines[0])
	}
	key, err := runnerdir.ParseSecret([]byte(strings.TrimPrefix(lines[1], "QORY_ACCESS_KEY_SECRET=")))
	if err != nil || key.PublicKey().String() != srv.sent()[0].PublicKey {
		t.Errorf("the secret printed is not the key sent: %v", err)
	}
	want := `QORY_APIARY_PUBLIC_KEY=[{"alg":"ed25519","public_key":"` + srv.signer.PublicKey().String() + `"},{"alg":"ed25519","public_key":"` + srv.next.PublicKey().String() + `"}]`
	if lines[2] != want {
		t.Errorf("the pin line %q, want %q", lines[2], want)
	}
	pin, err := accesskey.ParsePin([]byte(strings.TrimPrefix(lines[2], "QORY_APIARY_PUBLIC_KEY=")))
	if err != nil || len(pin) != 2 {
		t.Errorf("the pin printed does not read: %v", err)
	}
	wants(t, errOut, "access key fingerprint "+key.Fingerprint(), "enrolled as ak_0123456789abcdef in the node nd_0123456789abcdef", "stored secrets: no", "the key is active: runs can start",
		"Only QORY_ACCESS_KEY_SECRET belongs in the CI's secret store; QORY_ACCESS_KEY_ID and QORY_APIARY_PUBLIC_KEY are plain settings. No key or setting was written on this machine.")
	if strings.Contains(errOut, key.Secret()) {
		t.Error("stderr carries the secret")
	}
	after := snapshot(t, string(dir))
	delete(after, "locks")
	delete(after, filepath.Join("locks", runnerdir.KeyLock))
	delete(before, "locks")
	for p, v := range after {
		if before[p] != v {
			t.Errorf("%s changed", p)
		}
	}
	if len(after) != len(before) {
		t.Errorf("files before %v, after %v", before, after)
	}
	if heldKey(t).PublicKey() != own.PublicKey() {
		t.Error("the machine's own key changed")
	}

	// A lost answer cannot be retried.
	srv.status, srv.body, srv.signBy = http.StatusServiceUnavailable, []byte(`{}`), nil
	_, _, err = runSplit(t, "", "access-key", "enrol", "--print", srv.URL, srv.code(2, false))
	if err == nil || !strings.Contains(err.Error(), "(HTTP 503, unsigned); try again later; a --print enrolment cannot be retried: get a new code, and have your administrator revoke the key ") || !strings.Contains(err.Error(), " should it have been enrolled (") {
		t.Errorf("a lost answer: %v", err)
	}

	// The variables are a CI's own business with --print, on a machine whose runner file
	// sets no server.
	srv.status, srv.body, srv.signBy = http.StatusCreated, nil, srv.signer
	writeFile(t, runnerFile(), commentedRunner)
	t.Setenv("QORY_ACCESS_KEY_ID", "ak_0123456789abcdef")
	if _, _, err := runSplit(t, "", "access-key", "enrol", "--print", srv.URL, srv.code(3, false)); err != nil {
		t.Errorf("--print with QORY_ACCESS_KEY_ID set: %v", err)
	}
}

// TestEnrolPrintLeavesTheServerSectionUnread is --print on a machine whose runner file
// sets server.access_key_id and the pin while QORY_ACCESS_KEY_ID and
// QORY_APIARY_PUBLIC_KEY are set too, which stops a run on a value set in both: the
// key is for another machine, so the server section is not read, and the enrolment
// succeeds under instance.name. A file that is not YAML still stops it.
func TestEnrolPrintLeavesTheServerSectionUnread(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	writeFile(t, runnerFile(), commentedRunner+"server:\n  url: https://apiary.example\n  access_key_id: ak_0000000000000000\n  apiary_public_key: "+pinLine(newKey(t))+"\n")
	t.Setenv("QORY_ACCESS_KEY_ID", "ak_0123456789abcdef")
	t.Setenv("QORY_APIARY_PUBLIC_KEY", `[{"alg":"ed25519","public_key":"`+srv.signer.PublicKey().String()+`"}]`)
	out, errOut, err := runSplit(t, "", "access-key", "enrol", "--print", srv.URL, srv.code(1, false))
	if err != nil {
		t.Fatalf("--print with the server section and the variables both set: %v\n%s", err, errOut)
	}
	if !strings.HasPrefix(out, "QORY_ACCESS_KEY_ID=ak_0123456789abcdef\n") {
		t.Errorf("stdout:\n%s", out)
	}
	if sent := srv.sent(); len(sent) != 1 || sent[0].Name != "build-01" {
		t.Errorf("the enrolment sent %+v; want the name build-01", sent)
	}

	writeFile(t, runnerFile(), "instance: [build-01\n")
	_, _, err = runSplit(t, "", "access-key", "enrol", "--print", srv.URL, srv.code(2, false))
	if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), runnerFile()+": ") {
		t.Errorf("--print with a runner file that is not YAML: %v", err)
	}
	if n := len(srv.sent()); n != 1 {
		t.Errorf("%d enrolments sent; a file that is not YAML sends none", n)
	}
}

// TestCreateKeepsTheKeyAndRefusesASecondOne is create: the marker and the secret, mode
// 0600, and the public key and fingerprint printed; a second create refuses and names
// the file; the variables refuse it; --print writes nothing and prints the secret
// alone on stdout.
func TestCreateKeepsTheKeyAndRefusesASecondOne(t *testing.T) {
	emptyDir(t)
	dir := configDir()
	out, err := run(t, "access-key", "create")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	key := heldKey(t)
	wants(t, out, "public key "+key.PublicKey().String()+"\n", "fingerprint "+key.Fingerprint()+"\n",
		"An owner or administrator of the server pastes the public key into the node or node pool, where it is active at once; its page then shows the server lines for runner.yaml.",
		"wrote the secret to "+dir.Path(runnerdir.SecretFile))
	lacks(t, out, key.Secret())
	for _, name := range []string{runnerdir.SecretFile, runnerdir.MarkerFile} {
		if m := mode(t, dir.Path(name)); m != 0o600 {
			t.Errorf("%s is mode %v", name, m)
		}
	}
	if exists(runnerFile()) {
		t.Error("create wrote a runner file")
	}
	_, err = run(t, "access-key", "create")
	if cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), dir.Path(runnerdir.SecretFile)+" already holds this machine's access key secret, and create does not replace it: move it aside yourself first, or use --print for a key kept elsewhere") {
		t.Errorf("a second create: %v", err)
	}
	if heldKey(t).PublicKey() != key.PublicKey() {
		t.Error("a second create replaced the key")
	}
	os.Remove(dir.Path(runnerdir.SecretFile))
	t.Setenv("QORY_APIARY_PUBLIC_KEY", "[]")
	if _, err := run(t, "access-key", "create"); cmd.ExitCode(err) != cmd.ExitInput || !strings.Contains(err.Error(), "QORY_APIARY_PUBLIC_KEY is set, and qory access-key create keeps the key in this machine's files") {
		t.Errorf("create with the pin in the environment: %v", err)
	}
	if exists(dir.Path(runnerdir.SecretFile)) {
		t.Error("create with the pin in the environment wrote a secret")
	}

	before := snapshot(t, string(dir))
	stdout, stderr, err := runSplit(t, "", "access-key", "create", "--print")
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "QORY_ACCESS_KEY_SECRET=qak_") {
		t.Fatalf("stdout:\n%s", stdout)
	}
	printed, err := runnerdir.ParseSecret([]byte(strings.TrimPrefix(lines[0], "QORY_ACCESS_KEY_SECRET=")))
	if err != nil {
		t.Fatal(err)
	}
	wants(t, stderr, "public key "+printed.PublicKey().String(), "fingerprint "+printed.Fingerprint(), "Only QORY_ACCESS_KEY_SECRET belongs in the CI's secret store. No key or setting was written on this machine.")
	lacks(t, stderr, printed.Secret())
	if after := snapshot(t, string(dir)); len(after) != len(before) {
		t.Errorf("create --print wrote files: before %v, after %v", before, after)
	}
}
