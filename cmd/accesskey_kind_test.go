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

// kindRefused is the error of a verified 201 for another kind than the code's, or for
// a kind enrol does not take: the id the server gave the key, and what it enrolled it
// for, as "a pool" or "an integration".
func kindRefused(what string) string {
	return "the server enrolled the key " + newID + " for " + what + " instead, so qory did not use it; the key stays active until you revoke it in Qory Apiary"
}

// noSecret fails the test for each text that carries an access key secret.
func noSecret(t *testing.T, name string, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if accesskey.ContainsSecret(text) {
			t.Errorf("%s: a secret was printed or returned", name)
		}
	}
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

// TestEnrolKeepsNoKeyForAnotherKind is a qecn_ code answered with a verified 201 that
// enrols the key for an integration, node_kind "adapter", a [accesskey.KindError], to an
// enrolment on a machine without a key and to a --replace on a machine with one, and
// answered with one for a pool: the error is the one line naming the key's id and what
// the server enrolled it for, with no retry, no code and no secret; the new secret is
// deleted, not moved aside, the pending enrolment ends and no answer is kept;
// access-key-secret and forager.yaml are the same, byte for byte; and nothing after the
// fingerprint is printed.
func TestEnrolKeepsNoKeyForAnotherKind(t *testing.T) {
	for _, c := range []struct {
		kind, what string
		replace    bool
	}{
		{"adapter", "an integration", false},
		{"adapter", "an integration", true},
		{"pool", "a pool", false},
	} {
		replace := c.replace
		name := "a machine without a key, answered for " + c.what
		emptyDir(t)
		srv := newEnrolServer(t)
		file := commentedForager
		var own *accesskey.Key
		args := []string{"access-key", "enrol", srv.URL, srv.code(1, false)}
		if replace {
			name = "--replace, answered for " + c.what
			own = newKey(t)
			file = enrolled(t, srv, own)
			args = []string{"access-key", "enrol", "--replace", srv.URL, srv.code(1, false)}
		} else {
			writeFile(t, foragerFile(), file)
		}
		srv.body = srv.answer(c.kind)
		out, errOut, err := runSplit(t, "", args...)
		pub := sentKey(t, srv)
		if err == nil || err.Error() != kindRefused(c.what) {
			t.Fatalf("%s: %v, want %q", name, err, kindRefused(c.what))
		}
		lacks(t, err.Error(), "retr", "(", "moved aside")
		noSecret(t, name, err.Error(), out, errOut)
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

// TestEnrolRetryKeepsNoKeyForAnotherKind is a retry, planted by hand: access-key-secret
// holds the key the pending enrolment records for the qecn_ code, and the server answers
// the retry for an integration. access-key-secret, which the enrolment never touches, stays
// byte for byte, and the pending enrolment ends.
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
	if err == nil || err.Error() != kindRefused("an integration") {
		t.Fatalf("%v, want %q", err, kindRefused("an integration"))
	}
	noSecret(t, "a retry", err.Error())
	if heldSecret(foragerdir.SecretFile) != active {
		t.Error("access-key-secret changed")
	}
	noEnrolmentLeft(t, "a retry")
}

// TestEnrolPrintKeepsNoKeyForAnotherKind is --print with a qecn_ code answered for an
// integration, on a machine with an enrolment of another code pending, its new key in
// access-key-secret.new: the same one line, nothing on stdout, nothing on stderr after
// the fingerprint, and no file of the directory changes, the pending enrolment and the
// new key of the other code included.
func TestEnrolPrintKeepsNoKeyForAnotherKind(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	file := commentedForager + "  server:\n    url: https://apiary.example\n    access_key_id: ak_0000000000000000\n    apiary_public_key: " + pinLine(newKey(t)) + "\n"
	writeFile(t, foragerFile(), file)
	writeSecret(t, newKey(t))
	dir := configDir()
	other := newKey(t)
	if err := dir.WriteNewSecret(other); err != nil {
		t.Fatal(err)
	}
	if err := dir.WritePending(mustNormalise(t, srv.code(2, false)), other.PublicKey(), time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, staged := heldSecret(foragerdir.PendingFile), heldSecret(foragerdir.NewSecretFile)
	before := snapshot(t, string(dir))
	srv.body = srv.answer("adapter")
	out, errOut, err := runSplit(t, "", "access-key", "enrol", "--print", srv.URL, srv.code(1, false))
	pub := sentKey(t, srv)
	if err == nil || err.Error() != kindRefused("an integration") {
		t.Fatalf("%v, want %q", err, kindRefused("an integration"))
	}
	noSecret(t, "--print", err.Error(), out, errOut)
	if out != "" {
		t.Errorf("stdout carries %q", out)
	}
	if heldSecret(foragerdir.PendingFile) != pending || heldSecret(foragerdir.NewSecretFile) != staged {
		t.Error("the other code's pending enrolment or new key changed")
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
// enrolment of key with code, in the form enrol keeps an answer in, and records the
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

// TestEnrolFinishesNoKeptAnswerForAnotherKind is a kept answer to a qecn_ code that
// enrols the key for an integration, a [accesskey.KindError] when it is verified, planted
// by hand as a guard: no build of qory keeps such an answer, so this is a state no build
// makes. With the key in access-key-secret.new, the same
// command deletes it; with the key in access-key-secret, that file stays byte for byte.
// Either way nothing reaches the server, nothing is printed, forager.yaml stays, and
// the pending enrolment and the kept answer are removed. A kept node's answer, planted
// the same way, still finishes, as before.
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
		if err == nil || err.Error() != kindRefused("an integration") {
			t.Fatalf("%s: %v, want %q", name, err, kindRefused("an integration"))
		}
		noSecret(t, name, err.Error(), out, errOut)
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

// TestEnrolRefusesAnIntegrationsCode is a qeci_ code, an integration's, given to enrol
// on a machine without a key, with --print, with --replace on a machine with one and
// with --via: each is refused with the one line, before a key is made, anything is
// written or anything is sent, so no file of the directory changes and the server gets
// no request. A bare qec_ code is refused before any request too, as a code of no kind.
func TestEnrolRefusesAnIntegrationsCode(t *testing.T) {
	const refused = "this code is for an integration: use a code made for this machine or its pool"
	for _, c := range []struct {
		name  string
		flags []string
		key   bool
	}{
		{"a machine without a key", nil, false},
		{"--print", []string{"--print"}, true},
		{"--replace", []string{"--replace"}, true},
		{"--via", []string{"--via", "https://gateway.example:8443"}, false},
	} {
		emptyDir(t)
		srv := newEnrolServer(t)
		srv.kind = "adapter"
		var file string
		if c.key {
			file = enrolled(t, srv, newKey(t))
		} else {
			file = commentedForager
			writeFile(t, foragerFile(), file)
		}
		dir := configDir()
		before := snapshot(t, string(dir))
		args := append(append([]string{"access-key", "enrol"}, c.flags...), srv.URL, srv.code(1, false))
		out, errOut, err := runSplit(t, "", args...)
		if err == nil || err.Error() != refused {
			t.Errorf("%s: %v, want %q", c.name, err, refused)
		}
		if out != "" || errOut != "" {
			t.Errorf("%s: printed\n%s%s", c.name, out, errOut)
		}
		if n := len(srv.sent()); n != 0 {
			t.Errorf("%s: %d requests reached the server", c.name, n)
		}
		after := snapshot(t, string(dir))
		for p, v := range after {
			if before[p] != v {
				t.Errorf("%s: %s changed", c.name, p)
			}
		}
		if len(after) != len(before) {
			t.Errorf("%s: files before %v, after %v", c.name, before, after)
		}
		if got := readForagerFile(t); got != file {
			t.Errorf("%s: forager.yaml changed:\n%s", c.name, got)
		}
	}

	// A bare qec_ code names no kind.
	emptyDir(t)
	srv := newEnrolServer(t)
	writeFile(t, foragerFile(), commentedForager)
	code := "qec_" + strings.TrimPrefix(srv.code(1, false), accesskey.CodePrefixNode)
	_, err := run(t, "access-key", "enrol", srv.URL, code)
	if want := "an enrolment code is qecn_, qecp_, qecg_ or qeci_, 26 characters, then a dot and a fingerprint"; err == nil || err.Error() != want {
		t.Errorf("a qec_ code: %v, want %q", err, want)
	}
	if n := len(srv.sent()); n != 0 {
		t.Errorf("a qec_ code: %d requests reached the server", n)
	}
	if exists(configDir().Path(foragerdir.NewSecretFile)) || exists(configDir().Path(foragerdir.PendingFile)) {
		t.Error("a qec_ code: a key was made")
	}
}
