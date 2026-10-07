package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/runner/accesskey"
	"github.com/qoryai/runner/contracts"

	"github.com/qoryai/qory/internal/runnerdir"
)

// contractFile reads one file of the runner contract, by its path under runner/v1.
func contractFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(contracts.FS, name)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(b)
}

// knownAnswer is one answer of fixtures/known-answers/signatures.json.
type knownAnswer struct {
	Note      string   `json:"note"`
	Body      string   `json:"body"`
	Lines     []string `json:"lines"`
	Signature string   `json:"signature"`
}

// knownEnrolment is the published enrolment requests and the answers to them.
func knownEnrolment(t *testing.T) (requests []accesskey.EnrolmentRequest, answers []knownAnswer) {
	t.Helper()
	var v struct {
		Answers []knownAnswer `json:"answers"`
	}
	if err := json.Unmarshal(contractFile(t, "fixtures/known-answers/signatures.json"), &v); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fixtures/enrolment/request.json", "fixtures/enrolment/request-two-fingerprints.json"} {
		var r accesskey.EnrolmentRequest
		if err := json.Unmarshal(contractFile(t, name), &r); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	for _, a := range v.Answers {
		if strings.HasPrefix(a.Body, "fixtures/enrolment/") {
			answers = append(answers, a)
		}
	}
	return requests, answers
}

// fixtureAccessKey is the contract's published fixture access key.
func fixtureAccessKey(t *testing.T) *accesskey.Key {
	t.Helper()
	var k struct {
		AccessKey struct {
			Secret string `json:"secret"`
		} `json:"access_key"`
	}
	if err := json.Unmarshal(contractFile(t, "fixtures/known-answers/keys.json"), &k); err != nil {
		t.Fatal(err)
	}
	key, err := accesskey.ParseSecret(k.AccessKey.Secret)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestEnrolmentKnownAnswers posts the published enrolment requests, under the fixture
// access key, to a server that answers each with a published answer and its published
// signature, and acts on each as enrol does: the 201 verifies and pins the fixture
// signing key, which enrol refuses to pin; a signed key_limit, and a signed 429
// rate_limited where the contract publishes one, keeps the secret and the pending
// enrolment; a signed key_invalid moves the secret aside and ends it; the same answer
// tampered with is answer_unsigned and acts on nothing.
func TestEnrolmentKnownAnswers(t *testing.T) {
	requests, answers := knownEnrolment(t)
	key := fixtureAccessKey(t)
	n := 0
	for _, a := range answers {
		for _, tamper := range []bool{false, true} {
			body := contractFile(t, a.Body)
			if tamper {
				body = bytes.Replace(body, []byte(`"key_`), []byte(`"key_x`), 1)
				body = bytes.Replace(body, []byte(`"rate_`), []byte(`"rate_x`), 1)
				body = bytes.Replace(body, []byte(`"stored_secrets":false`), []byte(`"stored_secrets":true`), 1)
			}
			status, err := strconv.Atoi(a.Lines[1])
			if err != nil {
				t.Fatalf("%s: status %q", a.Note, a.Lines[1])
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(accesskey.HeaderSignature, a.Signature)
				w.WriteHeader(status)
				w.Write(body)
			}))
			var published accesskey.EnrolmentRequest
			for _, r := range requests {
				if r.Proof == a.Lines[2] {
					published = r
				}
			}
			req, err := accesskey.NewEnrolmentRequest(key, published.Code, published.Name, time.Unix(published.Timestamp, 0))
			if err != nil || req.Proof != published.Proof {
				t.Fatalf("%s: the request is not the published one: %v", a.Note, err)
			}
			ans, err := req.Post(context.Background(), nil, srv.URL, userAgent())
			srv.Close()
			if status == http.StatusCreated && !tamper {
				if err != nil || !ans.Pin.Fixture() || len(ans.Pin) != 1 {
					t.Errorf("%s: %+v, %v", a.Note, ans, err)
				}
				n++
				continue
			}
			var ref *accesskey.Refusal
			if !errors.As(err, &ref) {
				t.Fatalf("%s, tampered %v: %v", a.Note, tamper, err)
			}
			want := accesskey.CodeAnswerUnsigned
			if !tamper {
				want = map[bool]string{true: accesskey.CodeKeyLimit, false: accesskey.CodeKeyInvalid}[strings.Contains(a.Body, "key-limit")]
				if status == http.StatusTooManyRequests {
					want = codeRateLimited
				}
			}
			if ref.Code != want {
				t.Errorf("%s, tampered %v: %s, want %s", a.Note, tamper, ref.Code, want)
			}
			dir := runnerdir.Dir(t.TempDir())
			if _, err := dir.Ensure(); err != nil {
				t.Fatal(err)
			}
			mine, _ := accesskey.Generate()
			now := time.Now()
			if err := dir.WriteSecret(mine); err != nil {
				t.Fatal(err)
			}
			if err := dir.WritePending(published.Code, mine.PublicKey(), now); err != nil {
				t.Fatal(err)
			}
			got := enrolFailed(dir, err, mine, false, now)
			if !errors.As(got, &ref) {
				t.Errorf("%s: the refusal is lost: %v", a.Note, got)
			}
			keep := want != accesskey.CodeKeyInvalid
			if dir.HasSecret() != keep || dir.Pending(published.Code, mine.PublicKey(), now) != keep {
				t.Errorf("%s, tampered %v: secret %v, pending %v", a.Note, tamper, dir.HasSecret(), dir.Pending(published.Code, mine.PublicKey(), now))
			}
			n++
		}
	}
	if n != 2*len(answers) || n < 10 {
		t.Errorf("%d answers acted on of %d; want the 201 and every refusal, four at least, each tampered too", n, len(answers))
	}
}

// TestKeyCommandsRefuseAFixtureKey is a key source that yields the published fixture
// access key: enrol, create and their --print refuse it, write no secret and print
// nothing of it.
func TestKeyCommandsRefuseAFixtureKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, name := range []string{accesskey.EnvSecret, accesskey.EnvID, accesskey.EnvPin} {
		t.Setenv(name, "")
	}
	fixture := fixtureAccessKey(t)
	generateKey = func() (*accesskey.Key, error) { return fixture, nil }
	t.Cleanup(func() { generateKey = accesskey.Generate })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request was sent")
	}))
	defer srv.Close()
	signer, _ := accesskey.Generate()
	code := "qec_F1XT0RE0000000000000000000." + signer.Fingerprint()
	dir := machineDir()
	if err := os.MkdirAll(string(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir.Path("runner.yaml"), []byte("instance:\n  name: build-01\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"enrol", srv.URL, code}, {"enrol", "--print", srv.URL, code}, {"create"}, {"create", "--print"}} {
		var out bytes.Buffer
		root := Root()
		root.SetArgs(append([]string{"access-key"}, args...))
		root.SetOut(&out)
		root.SetErr(&out)
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "the new key is one of the runner contract's published fixture keys, whose secret anyone can read; no key was kept") {
			t.Errorf("%v: %v", args, err)
		}
		if strings.Contains(out.String(), fixture.Secret()) || strings.Contains(out.String(), fixture.Fingerprint()) {
			t.Errorf("%v printed the key:\n%s", args, out.String())
		}
		if dir.HasSecret() {
			t.Fatalf("%v wrote the fixture secret", args)
		}
	}
}

// TestARefusalMovesAsideOnlyTheKeyMadeForTheCode is unauthorized, key_invalid and a
// fixture pin while access-key-secret holds a key other than the one the enrolment
// sent: the pending enrolment ends, that key stays, and no message says a secret was
// moved aside.
func TestARefusalMovesAsideOnlyTheKeyMadeForTheCode(t *testing.T) {
	sent, err := accesskey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		act  func(runnerdir.Dir, time.Time) error
	}{
		{"unauthorized", func(dir runnerdir.Dir, now time.Time) error {
			return enrolFailed(dir, &accesskey.Refusal{Code: accesskey.CodeUnauthorized, Status: http.StatusUnauthorized}, sent, false, now)
		}},
		{"key_invalid", func(dir runnerdir.Dir, now time.Time) error {
			return enrolFailed(dir, &accesskey.Refusal{Code: accesskey.CodeKeyInvalid, Status: http.StatusConflict}, sent, false, now)
		}},
		{"a fixture pin", func(dir runnerdir.Dir, now time.Time) error {
			return refuseFixturePin(dir, sent, false, now)
		}},
	} {
		dir := runnerdir.Dir(t.TempDir())
		if _, err := dir.Ensure(); err != nil {
			t.Fatal(err)
		}
		mine, err := accesskey.Generate()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if err := dir.WriteSecret(mine); err != nil {
			t.Fatal(err)
		}
		if err := dir.WritePending("qec_F1XT0RE0000000000000000000.uoES-kuj1vk0sq0qoGlmAg", sent.PublicKey(), now); err != nil {
			t.Fatal(err)
		}
		got := c.act(dir, now)
		if got == nil || strings.Contains(got.Error(), "moved aside") {
			t.Errorf("%s: %v", c.name, got)
		}
		if old, _ := dir.OldSecrets(); !dir.SameSecret(mine) || len(old) != 0 {
			t.Errorf("%s: this machine's key was moved aside", c.name)
		}
		if _, err := os.Lstat(dir.Path(runnerdir.PendingFile)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: the pending enrolment stayed: %v", c.name, err)
		}
	}
}
