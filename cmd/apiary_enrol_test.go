package cmd_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/apiarydir"
	"github.com/qoryai/qory/internal/foragerdir"
)

// diagnosticsEnrolServer starts a server for the enrolment of a diagnostics key, in the
// environment emptyDir sets: an enrolment server whose codes are qecd_ codes.
func diagnosticsEnrolServer(t *testing.T) *enrolServer {
	t.Helper()
	emptyDir(t)
	s := newEnrolServer(t)
	s.kind = "diagnostics"
	return s
}

// apiaryDirOf is the directory of the diagnostics key of the server at serverURL, and its
// name.
func apiaryDirOf(t *testing.T, serverURL string) (string, string) {
	t.Helper()
	root, err := apiarydir.Root()
	if err != nil {
		t.Fatal(err)
	}
	host, err := apiarydir.HostName(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, host), host
}

// enrolApiary runs qory apiary enrol with the server and the code, and fails the test
// when the output contains the code, in the form given or the form sent, or the proof or
// the secret of a key the server received.
func enrolApiary(t *testing.T, s *enrolServer, code string) (string, string, error) {
	t.Helper()
	out, errOut, err := runApart(t, "apiary", "enrol", s.URL, code)
	text := out + errOut
	if err != nil {
		text += err.Error()
	}
	normal, nerr := accesskey.NormaliseCode(code)
	if nerr != nil {
		t.Fatal(nerr)
	}
	secrets := []string{code, normal, normal[:strings.Index(normal, ".")]}
	for _, r := range s.sent() {
		secrets = append(secrets, r.Proof)
	}
	dir, _ := apiaryDirOf(t, s.URL)
	for _, read := range []func() (*accesskey.Key, error){foragerdir.Dir(dir).ReadSecret, foragerdir.Dir(dir).ReadNewSecret} {
		if k, err := read(); err == nil {
			secrets = append(secrets, k.Secret())
		}
	}
	for _, secret := range secrets {
		if strings.Contains(text, secret) {
			t.Errorf("the output contains a secret, a proof or the code %q:\n%s", secret, text)
		}
	}
	return out, errOut, err
}

// wantMode fails the test when path is not of mode want.
func wantMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s is mode %04o, want %04o", path, got, want)
	}
}

// wantNoFile fails the test when path exists.
func wantNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s exists", path)
	}
}

// wantEnrolled fails the test when the directory of the server at s.URL does not contain
// the diagnostics key s enrolled last, with the files of a finished enrolment alone, or
// when forager.yaml was written.
func wantEnrolled(t *testing.T, s *enrolServer) {
	t.Helper()
	dir, host := apiaryDirOf(t, s.URL)
	wantMode(t, filepath.Dir(dir), 0o700)
	wantMode(t, dir, 0o700)
	for _, name := range []string{apiarydir.SecretFile, apiarydir.SettingsFile, foragerdir.InstanceFile} {
		wantMode(t, filepath.Join(dir, name), 0o600)
	}
	for _, name := range []string{foragerdir.NewSecretFile, foragerdir.PendingFile, foragerdir.AnswerFile, apiarydir.SettingsFile + ".tmp"} {
		wantNoFile(t, filepath.Join(dir, name))
	}
	wantNoFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"))
	root, _ := apiarydir.Root()
	key, err := apiarydir.Read(root, host)
	if err != nil {
		t.Fatal(err)
	}
	sent := s.sent()
	if key.Secret.PublicKey().String() != sent[len(sent)-1].PublicKey {
		t.Error("access-key-secret is not the key the server enrolled")
	}
	b, err := os.ReadFile(filepath.Join(dir, apiarydir.SettingsFile))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"server":"` + s.URL + `","access_key_id":"ak_0123456789abcdef","apiary_public_key":[{"alg":"ed25519","public_key":"` + s.signer.PublicKey().String() + `"}]}` + "\n"
	if string(b) != want {
		t.Errorf("apiary.json is\n%s\nwant\n%s", b, want)
	}
}

func TestApiaryEnrolStoresTheKeyThatDiagnoseUses(t *testing.T) {
	es := diagnosticsEnrolServer(t)
	// One server responds to both the enrolment and the request for diagnostics, signed
	// by one key, as Qory Apiary does.
	ds := newDiagnosticsServer(t, newKey(t))
	ds.signer = es.signer
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == accesskey.EnrolmentPath {
			es.Config.Handler.ServeHTTP(w, r)
			return
		}
		ds.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	es.Server.URL = front.URL
	out, errOut, err := enrolApiary(t, es, es.code(1, false))
	if err != nil {
		t.Fatalf("enrol: %v\n%s", err, errOut)
	}
	_, host := apiaryDirOf(t, front.URL)
	if want := "Enrolled diagnostics key ak_0123456789abcdef for " + host + ".\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if errOut != "" {
		t.Errorf("stderr %q", errOut)
	}
	wantEnrolled(t, es)
	if got := es.sent()[0].Code; !strings.HasPrefix(got, accesskey.CodePrefixDiagnostics) {
		t.Errorf("the request's code starts %.5s", got)
	}

	root, _ := apiarydir.Root()
	key, err := apiarydir.Read(root, host)
	if err != nil {
		t.Fatal(err)
	}
	ds.mu.Lock()
	ds.client = key.Secret
	ds.mu.Unlock()
	doc, errOut, err := runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err != nil {
		t.Fatalf("diagnose with the enrolled key: %v\n%s", err, errOut)
	}
	if doc != string(ds.body) {
		t.Errorf("diagnose printed %q", doc)
	}
}

func TestApiaryEnrolStopsAtACodeOfAnotherKindBeforeAnyRequest(t *testing.T) {
	for kind, want := range map[string]string{
		"node": "a node code (qecn_)",
		"pool": "a node pool code (qecp_)",
	} {
		t.Run(kind, func(t *testing.T) {
			s := diagnosticsEnrolServer(t)
			s.kind = kind
			_, _, err := enrolApiary(t, s, s.code(1, false))
			if err == nil {
				t.Fatal("enrol with a code of another kind succeeded")
			}
			if line := "Use a diagnostics enrolment code, qecd_..., from Instance settings > Diagnostics keys in Qory Apiary: this is " + want + ", for another kind of key."; firstLine(err.Error()) != line {
				t.Errorf("first line %q, want %q", firstLine(err.Error()), line)
			}
			if cmd.ExitCode(err) != cmd.ExitInput {
				t.Errorf("exit %d", cmd.ExitCode(err))
			}
			if n := len(s.sent()); n != 0 {
				t.Errorf("%d requests sent", n)
			}
			root, _ := apiarydir.Root()
			wantNoFile(t, root)
		})
	}
}

func TestApiaryEnrolUnauthorized(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	s.status, s.body = http.StatusUnauthorized, []byte(`{"error":"unauthorized"}`)
	_, _, err := enrolApiary(t, s, s.code(1, false))
	if err == nil {
		t.Fatal("enrol with a refused code succeeded")
	}
	dir, host := apiaryDirOf(t, s.URL)
	want := "Get a new diagnostics enrolment code from Instance settings > Diagnostics keys in Qory Apiary: " + host + " refused the code as used, expired or cancelled (unauthorized, 401)."
	if firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d", cmd.ExitCode(err))
	}
	root, _ := apiarydir.Root()
	if _, err := apiarydir.Read(root, host); !errors.Is(err, apiarydir.ErrNoKey) {
		t.Errorf("read after a refusal: %v", err)
	}
	for _, name := range []string{foragerdir.NewSecretFile, foragerdir.PendingFile} {
		wantNoFile(t, filepath.Join(dir, name))
	}
	old, _ := foragerdir.Dir(dir).OldSecrets()
	if len(old) != 1 {
		t.Errorf("secrets moved aside: %v", old)
	}
	wantIn(t, err.Error(), "qory moved the secret made for the code aside to "+filepath.Join(dir, old[0])+".")
}

func TestApiaryEnrolStopsAtAnAnswerOfAnotherKind(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	s.body = s.answer("pool")
	_, _, err := enrolApiary(t, s, s.code(1, false))
	if err == nil {
		t.Fatal("enrol with an answer for a pool succeeded")
	}
	dir, host := apiaryDirOf(t, s.URL)
	want := "Ask an instance admin of " + host + " to revoke the access key ak_0123456789abcdef: Qory Apiary enrolled it for a pool, not as a diagnostics key, so qory kept nothing."
	if firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	for _, name := range []string{apiarydir.SecretFile, apiarydir.SettingsFile, foragerdir.NewSecretFile, foragerdir.PendingFile, foragerdir.AnswerFile} {
		wantNoFile(t, filepath.Join(dir, name))
	}
	if old, _ := foragerdir.Dir(dir).OldSecrets(); len(old) != 0 {
		t.Errorf("the key of a pool was kept aside: %v", old)
	}
}

func TestApiaryEnrolStopsAtAnUnsignedAnswerThenRetriesWithTheSameKey(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	s.signBy = nil
	code := s.code(1, false)
	_, _, err := enrolApiary(t, s, code)
	if err == nil {
		t.Fatal("enrol with an unsigned answer succeeded")
	}
	dir, host := apiaryDirOf(t, s.URL)
	want := "Check that " + s.URL + " is the Qory Apiary server that issued the code, then run the same command again within 15 minutes: its response (201) is not signed with the server key whose fingerprint is in the code, so qory stored no key."
	if firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	root, _ := apiarydir.Root()
	if _, err := apiarydir.Read(root, host); !errors.Is(err, apiarydir.ErrNoKey) {
		t.Errorf("read after an unsigned answer: %v", err)
	}
	for _, name := range []string{apiarydir.SecretFile, apiarydir.SettingsFile, foragerdir.AnswerFile} {
		wantNoFile(t, filepath.Join(dir, name))
	}
	_, _, err = runApart(t, "apiary", "run", "diagnose", diagnosticsRunID)
	if err == nil || !strings.HasPrefix(err.Error(), "Run `qory apiary enrol <server> <code>` first") {
		t.Errorf("diagnose after an unsigned answer: %v", err)
	}

	s.signBy = s.signer
	out, errOut, err := enrolApiary(t, s, code)
	if err != nil {
		t.Fatalf("retry: %v\n%s", err, errOut)
	}
	wantIn(t, errOut, "retrying with the key made for it")
	wantIn(t, out, "Enrolled diagnostics key ak_0123456789abcdef")
	if sent := s.sent(); len(sent) != 2 || sent[0].PublicKey != sent[1].PublicKey {
		t.Errorf("the retry did not send the same key: %d requests", len(sent))
	}
	wantEnrolled(t, s)
}

func TestApiaryEnrolStopsWhenAKeyIsStored(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	dir := storeDiagnosticsKey(t, s.URL, json.RawMessage(s.keys()), newKey(t))
	before, err := os.ReadFile(filepath.Join(dir, apiarydir.SettingsFile))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = enrolApiary(t, s, s.code(1, false))
	if err == nil {
		t.Fatal("enrol over a stored key succeeded")
	}
	_, host := apiaryDirOf(t, s.URL)
	want := "Remove " + dir + " to enrol a new diagnostics key for " + host + ": the directory contains a diagnostics key already."
	if firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	if cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("exit %d", cmd.ExitCode(err))
	}
	if n := len(s.sent()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
	after, _ := os.ReadFile(filepath.Join(dir, apiarydir.SettingsFile))
	if string(after) != string(before) {
		t.Error("apiary.json changed")
	}
	wantNoFile(t, filepath.Join(dir, foragerdir.NewSecretFile))
}

// TestApiaryEnrolFinishesFromTheKeptAnswer stops the enrolment after each step that
// follows the server's answer, as a crash would, and runs the same command again: it
// finishes from enrolment-answer without a second request.
func TestApiaryEnrolFinishesFromTheKeptAnswer(t *testing.T) {
	for _, step := range []string{"answer", "secret", "settings", "pending"} {
		t.Run(step, func(t *testing.T) {
			s := diagnosticsEnrolServer(t)
			code := s.code(1, false)
			restore := cmd.SetAfterStep(func(got string) error {
				if got == step {
					return errors.New("stopped after " + step)
				}
				return nil
			})
			_, _, err := enrolApiary(t, s, code)
			restore()
			if err == nil || err.Error() != "stopped after "+step {
				t.Fatalf("enrol: %v", err)
			}
			out, errOut, err := enrolApiary(t, s, code)
			if err != nil {
				t.Fatalf("enrol again: %v\n%s", err, errOut)
			}
			wantIn(t, out, "Enrolled diagnostics key ak_0123456789abcdef")
			if n := len(s.sent()); n != 1 {
				t.Errorf("%d requests sent, want 1", n)
			}
			wantEnrolled(t, s)
		})
	}
}

func TestApiaryEnrolStopsAtAKeptAnswerForAnotherCode(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	restore := cmd.SetAfterStep(func(got string) error {
		if got == "answer" {
			return errors.New("stopped")
		}
		return nil
	})
	_, _, _ = enrolApiary(t, s, s.code(1, false))
	restore()
	_, _, err := enrolApiary(t, s, s.code(2, false))
	if err == nil {
		t.Fatal("enrol with another code beside a kept answer succeeded")
	}
	dir, _ := apiaryDirOf(t, s.URL)
	want := "Run the earlier enrolment again with its code to finish it, or remove " + dir + ": " + filepath.Join(dir, foragerdir.AnswerFile) + " is the response to an enrolment with another code."
	if firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	if n := len(s.sent()); n != 1 {
		t.Errorf("%d requests sent, want 1", n)
	}
}

func TestApiaryEnrolStopsAtAServerURLThatIsNotHTTPS(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	_, _, err := runApart(t, "apiary", "enrol", "http://apiary.example", s.code(1, false))
	if err == nil || !strings.HasPrefix(err.Error(), "Give the server as an https URL, such as https://apiary.example:") {
		t.Errorf("enrol over http to another host: %v", err)
	}
	if cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("exit %d", cmd.ExitCode(err))
	}
}

func TestApiaryEnrolStopsAtAServerURLWithAFragment(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	for _, server := range []string{s.URL + "#", s.URL + "#x", s.URL + "/#"} {
		_, _, err := runApart(t, "apiary", "enrol", server, s.code(1, false))
		want := "Give the server as a URL of its host alone, such as https://apiary.example: the URL has a \"#\"."
		if err == nil || firstLine(err.Error()) != want {
			t.Errorf("enrol %s: %v, want %q", server, err, want)
		}
		if cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("enrol %s: exit %d", server, cmd.ExitCode(err))
		}
	}
	if n := len(s.sent()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
	root, _ := apiarydir.Root()
	wantNoFile(t, root)
}

func TestApiaryEnrolNeverPrintsACodeGivenAsTheServer(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	code := s.code(1, false)
	for _, server := range []string{"http://" + code, "ftp://" + code, "https://" + code + "/v1"} {
		out, errOut, err := runApart(t, "apiary", "enrol", server, code)
		if err == nil {
			t.Fatalf("enrol %s succeeded", server)
		}
		text := strings.ToLower(out + errOut + err.Error())
		if strings.Contains(text, accesskey.CodePrefixDiagnostics) {
			t.Errorf("the output contains the code:\n%s", text)
		}
	}
	if n := len(s.sent()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

// TestApiaryEnrolStopsAtAKeptAnswerForAnotherServerURL stops an enrolment with
// http://localhost:<port> after the response is kept, then runs it with
// http://LOCALHOST:<port>, a URL of the same directory: it stops before any request.
func TestApiaryEnrolStopsAtAKeptAnswerForAnotherServerURL(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	port := s.URL[strings.LastIndex(s.URL, ":")+1:]
	s.Server.URL = "http://localhost:" + port
	code := s.code(1, false)
	restore := cmd.SetAfterStep(func(got string) error {
		if got == "answer" {
			return errors.New("stopped")
		}
		return nil
	})
	_, _, _ = enrolApiary(t, s, code)
	restore()
	if n := len(s.sent()); n != 1 {
		t.Fatalf("%d requests sent, want 1", n)
	}
	first := s.URL
	s.Server.URL = "http://LOCALHOST:" + port
	_, _, err := enrolApiary(t, s, code)
	if err == nil {
		t.Fatal("enrol with another server URL beside a kept response succeeded")
	}
	dir, _ := apiaryDirOf(t, s.URL)
	want := "Run the earlier enrolment again with its server URL, " + first + ", to finish it, or remove " + dir + ": " + filepath.Join(dir, foragerdir.AnswerFile) + " is the response to an enrolment with that server URL."
	if firstLine(err.Error()) != want {
		t.Errorf("first line %q, want %q", firstLine(err.Error()), want)
	}
	if n := len(s.sent()); n != 1 {
		t.Errorf("%d requests sent, want 1", n)
	}
	s.Server.URL = first
	out, errOut, err := enrolApiary(t, s, code)
	if err != nil {
		t.Fatalf("enrol with the first server URL: %v\n%s", err, errOut)
	}
	wantIn(t, out, "Enrolled diagnostics key ak_0123456789abcdef")
	wantEnrolled(t, s)
}

func TestApiaryEnrolKeepsHTTPAndHTTPSToLocalhostApart(t *testing.T) {
	s := diagnosticsEnrolServer(t)
	port := s.URL[strings.LastIndex(s.URL, ":")+1:]
	https := "https://localhost:" + port
	dir := storeDiagnosticsKey(t, https, json.RawMessage(s.keys()), newKey(t))
	s.Server.URL = "http://localhost:" + port
	out, errOut, err := enrolApiary(t, s, s.code(1, false))
	if err != nil {
		t.Fatalf("enrol over http beside a key for https: %v\n%s", err, errOut)
	}
	wantIn(t, out, "Enrolled diagnostics key ak_0123456789abcdef for http_localhost_"+port+".")
	wantEnrolled(t, s)
	if filepath.Base(dir) != "localhost_"+port {
		t.Errorf("the directory for %s is %s", https, dir)
	}
	root, _ := apiarydir.Root()
	if key, err := apiarydir.Read(root, filepath.Base(dir)); err != nil || key.Server != https {
		t.Errorf("the key for %s: %v", https, err)
	}
}
