package cmd_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/foragerdir"
)

// kindRefused is the error of a verified 201 for a kind enrol does not take.
func kindRefused(fingerprint string) string {
	return "PLACEHOLDER-B1D-490 fingerprint=" + fingerprint
}

// sentKey is the public key of the server's last verified request.
func sentKey(t *testing.T, srv *enrolServer) accesskey.PublicKey {
	t.Helper()
	sent := srv.sent()
	if len(sent) == 0 {
		t.Fatal("no request reached the server")
	}
	pub, err := accesskey.ParsePublicKey(sent[len(sent)-1].PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

// keptNowhere fails the test for every file of the directory that holds the secret of
// the key whose public key is pub, moved aside or not.
func keptNowhere(t *testing.T, pub accesskey.PublicKey) {
	t.Helper()
	err := filepath.WalkDir(string(configDir()), func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if k, err := foragerdir.ParseSecret(b); err == nil && k.PublicKey() == pub {
			t.Errorf("%s holds the secret of the key the server enrolled", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// noEnrolmentLeft fails the test for a new secret, a secret moved aside, a pending
// enrolment or a kept answer left in the directory.
func noEnrolmentLeft(t *testing.T, name string) {
	t.Helper()
	dir := configDir()
	for _, file := range []string{foragerdir.NewSecretFile, foragerdir.PendingFile, foragerdir.AnswerFile, foragerdir.ReplacedFile} {
		if exists(dir.Path(file)) {
			t.Errorf("%s: %s is there", name, file)
		}
	}
	if n := movedAside(t); n != 0 {
		t.Errorf("%s: %d secrets moved aside", name, n)
	}
}

// TestEnrolKeepsNoKeyForAnotherKind is a verified 201 that enrols the key for an
// integration, node_kind "adapter", to an enrolment on a machine without a key and to a
// --replace on a machine with one: the error is the one line naming the key's
// fingerprint, with no retry and no code; the new secret is deleted, not moved aside,
// the pending enrolment ends and no answer is kept; access-key-secret and forager.yaml
// are the same, byte for byte; and nothing after the fingerprint is printed.
func TestEnrolKeepsNoKeyForAnotherKind(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "a machine without a key"
		emptyDir(t)
		srv := newEnrolServer(t)
		file := commentedForager
		var own *accesskey.Key
		args := []string{"access-key", "enrol", srv.URL, srv.code(1, false)}
		if replace {
			name = "--replace"
			own = newKey(t)
			file = enrolled(t, srv, own)
			args = []string{"access-key", "enrol", "--replace", srv.URL, srv.code(1, false)}
		} else {
			writeFile(t, foragerFile(), file)
		}
		srv.body = srv.answer("adapter")
		out, errOut, err := runSplit(t, "", args...)
		pub := sentKey(t, srv)
		if err == nil || err.Error() != kindRefused(pub.Fingerprint()) {
			t.Fatalf("%s: %v, want %q", name, err, kindRefused(pub.Fingerprint()))
		}
		lacks(t, err.Error(), "retr", "(", "moved aside")
		if out != "" {
			t.Errorf("%s: stdout carries %q", name, out)
		}
		if !strings.HasSuffix(errOut, "access key fingerprint "+pub.Fingerprint()+"\n") {
			t.Errorf("%s: stderr goes on after the fingerprint:\n%s", name, errOut)
		}
		lacks(t, errOut, "enrolled as", "stored secrets", "the key is active", "wrote", "retr", "moved")
		noEnrolmentLeft(t, name)
		keptNowhere(t, pub)
		if replace {
			oldStays(t, name, own, file)
		} else if exists(configDir().Path(foragerdir.SecretFile)) {
			t.Errorf("%s: access-key-secret is there", name)
		}
		if got := readForagerFile(t); got != file {
			t.Errorf("%s: forager.yaml changed:\n%s", name, got)
		}
		if n := len(srv.sent()); n != 1 {
			t.Errorf("%s: %d requests reached the server", name, n)
		}
	}
}

// TestEnrolRetryKeepsNoKeyForAnotherKind is the same command again within the code's 15
// minutes, after a swap put the key made for the code in access-key-secret and lost its
// kept answer, now answered for an integration: access-key-secret, which the enrolment
// never touches, stays byte for byte, and the pending enrolment ends.
func TestEnrolRetryKeepsNoKeyForAnotherKind(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	code := srv.code(1, false)
	key := newKey(t)
	writeSecret(t, key)
	writeFile(t, foragerFile(), "session:\n  instance:\n    name: build-01\n")
	if err := configDir().WritePending(mustNormalise(t, code), key.PublicKey(), time.Now()); err != nil {
		t.Fatal(err)
	}
	active := heldSecret(foragerdir.SecretFile)
	srv.body = srv.answer("adapter")
	_, err := run(t, "access-key", "enrol", srv.URL, code)
	if err == nil || err.Error() != kindRefused(key.Fingerprint()) {
		t.Fatalf("%v, want %q", err, kindRefused(key.Fingerprint()))
	}
	if heldSecret(foragerdir.SecretFile) != active {
		t.Error("access-key-secret changed")
	}
	noEnrolmentLeft(t, "a retry")
}

// TestEnrolPrintKeepsNoKeyForAnotherKind is --print answered for an integration: the
// same one line, nothing on stdout, nothing on stderr after the fingerprint, and no file
// of the directory changes.
func TestEnrolPrintKeepsNoKeyForAnotherKind(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	file := commentedForager + "  server:\n    url: https://apiary.example\n    access_key_id: ak_0000000000000000\n    apiary_public_key: " + pinLine(newKey(t)) + "\n"
	writeFile(t, foragerFile(), file)
	writeSecret(t, newKey(t))
	dir := configDir()
	before := snapshot(t, string(dir))
	srv.body = srv.answer("adapter")
	out, errOut, err := runSplit(t, "", "access-key", "enrol", "--print", srv.URL, srv.code(1, false))
	pub := sentKey(t, srv)
	if err == nil || err.Error() != kindRefused(pub.Fingerprint()) {
		t.Fatalf("%v, want %q", err, kindRefused(pub.Fingerprint()))
	}
	if out != "" {
		t.Errorf("stdout carries %q", out)
	}
	if errOut != "access key fingerprint "+pub.Fingerprint()+"\n" {
		t.Errorf("stderr:\n%s", errOut)
	}
	after := snapshot(t, string(dir))
	for _, s := range []map[string]string{before, after} {
		delete(s, "locks")
		delete(s, filepath.Join("locks", foragerdir.KeyLock))
	}
	for p, v := range after {
		if before[p] != v {
			t.Errorf("%s changed", p)
		}
	}
	if len(after) != len(before) {
		t.Errorf("files before %v, after %v", before, after)
	}
	if got := readForagerFile(t); got != file {
		t.Errorf("forager.yaml changed:\n%s", got)
	}
}

// keepAnswerFor plants in enrolment-answer the server's verified 201 of kind to the
// enrolment of key with code, as an earlier run would have kept it, and records the
// pending enrolment.
func keepAnswerFor(t *testing.T, srv *enrolServer, key *accesskey.Key, code, kind string) {
	t.Helper()
	now := time.Now()
	req, err := accesskey.NewEnrolmentRequest(key, code, "build-01", now)
	if err != nil {
		t.Fatal(err)
	}
	body := srv.answer(kind)
	record, err := json.Marshal(map[string]any{
		"version":           1,
		"server":            srv.URL,
		"request":           req,
		"status":            http.StatusCreated,
		"body":              body,
		"signature":         srv.signer.SignAnswer(accesskey.Answer{Enrolment: true, Status: http.StatusCreated, RequestSignature: req.Proof, Body: body}),
		"configuration":     "",
		"run_configuration": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := configDir()
	if err := dir.WriteAnswer(record); err != nil {
		t.Fatal(err)
	}
	if err := dir.WritePending(mustNormalise(t, code), key.PublicKey(), now); err != nil {
		t.Fatal(err)
	}
}

// TestEnrolFinishesNoKeptAnswerForAnotherKind is a kept answer that enrols the key for
// an integration, which enrol no longer keeps but an earlier build could have: with the
// key still in access-key-secret.new, the same command deletes it; with the key a swap
// put in access-key-secret, that file stays byte for byte. Either way nothing reaches
// the server, nothing is printed, forager.yaml stays, and the pending enrolment and the
// kept answer are removed. A kept node's answer still finishes, as before.
func TestEnrolFinishesNoKeptAnswerForAnotherKind(t *testing.T) {
	const file = "session:\n  instance:\n    name: build-01\n"
	for _, placed := range []bool{false, true} {
		name := "the key in access-key-secret.new"
		if placed {
			name = "the key in access-key-secret"
		}
		emptyDir(t)
		srv := newEnrolServer(t)
		writeFile(t, foragerFile(), file)
		dir := configDir()
		code := srv.code(1, false)
		key := newKey(t)
		if placed {
			writeSecret(t, key)
		} else {
			if _, err := dir.Ensure(); err != nil {
				t.Fatal(err)
			}
			if err := dir.WriteNewSecret(key); err != nil {
				t.Fatal(err)
			}
		}
		keepAnswerFor(t, srv, key, code, "adapter")
		active := heldSecret(foragerdir.SecretFile)
		out, errOut, err := runSplit(t, "", "access-key", "enrol", srv.URL, code)
		if err == nil || err.Error() != kindRefused(key.Fingerprint()) {
			t.Fatalf("%s: %v, want %q", name, err, kindRefused(key.Fingerprint()))
		}
		if out != "" || errOut != "" {
			t.Errorf("%s: printed\n%s%s", name, out, errOut)
		}
		if n := len(srv.sent()); n != 0 {
			t.Errorf("%s: %d requests reached the server", name, n)
		}
		noEnrolmentLeft(t, name)
		if placed {
			if heldSecret(foragerdir.SecretFile) != active {
				t.Errorf("%s: access-key-secret changed", name)
			}
		} else {
			keptNowhere(t, key.PublicKey())
			if exists(dir.Path(foragerdir.SecretFile)) {
				t.Errorf("%s: access-key-secret is there", name)
			}
		}
		if got := readForagerFile(t); got != file {
			t.Errorf("%s: forager.yaml changed:\n%s", name, got)
		}
	}

	// A kept node's answer, planted the same way, finishes the enrolment.
	emptyDir(t)
	srv := newEnrolServer(t)
	writeFile(t, foragerFile(), file)
	dir := configDir()
	if _, err := dir.Ensure(); err != nil {
		t.Fatal(err)
	}
	key := newKey(t)
	if err := dir.WriteNewSecret(key); err != nil {
		t.Fatal(err)
	}
	keepAnswerFor(t, srv, key, srv.code(1, false), "node")
	out, err := run(t, "access-key", "enrol", srv.URL, srv.code(1, false))
	if err != nil {
		t.Fatalf("a node's kept answer: %v\n%s", err, out)
	}
	wants(t, out, "enrolled as "+newID+" in the node nd_0123456789abcdef")
	if heldKey(t).PublicKey() != key.PublicKey() || len(srv.sent()) != 0 {
		t.Errorf("a node's kept answer: access-key-secret holds another key, or %d requests were sent", len(srv.sent()))
	}
}
