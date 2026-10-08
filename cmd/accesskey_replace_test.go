package cmd_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/runner/accesskey"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/runnerdir"
)

// oldID is the id of the key a machine holds before it is moved to a new one.
const oldID = "ak_01d0000000000000"

// newID is the id the enrolment server gives every key it enrols.
const newID = "ak_0123456789abcdef"

// enrolled is a machine that holds the key old for srv, its runner file naming the
// server, oldID and the server's key, and returns the runner file's content.
func enrolled(t *testing.T, srv *enrolServer, old *accesskey.Key) string {
	t.Helper()
	file := "server:\n  url: " + srv.URL + "\n  access_key_id: " + oldID + "\n  apiary_public_key: " + pinLine(srv.signer) + "\ninstance:\n  name: build-01\n"
	writeFile(t, runnerFile(), file)
	writeSecret(t, old)
	return file
}

// oldStays checks that access-key-secret still holds old and the runner file reads
// file, byte for byte, and that no secret was put aside for a replacement.
func oldStays(t *testing.T, name string, old *accesskey.Key, file string) {
	t.Helper()
	dir := configDir()
	b, err := os.ReadFile(dir.Path(runnerdir.SecretFile))
	if err != nil || string(b) != old.Secret()+"\n" {
		t.Errorf("%s: access-key-secret changed (%v)", name, err)
	}
	if got := readRunnerFile(t); got != file {
		t.Errorf("%s: the runner file changed:\n%s", name, got)
	}
	if exists(dir.Path(runnerdir.ReplacedFile)) {
		t.Errorf("%s: %s exists", name, runnerdir.ReplacedFile)
	}
}

// noneHolds reports whether no file of the directory holds the secret of k.
func noneHolds(t *testing.T, k *accesskey.Key) bool {
	t.Helper()
	for path, content := range snapshot(t, string(configDir())) {
		if strings.Contains(content, k.Secret()) {
			t.Logf("%s holds the secret", path)
			return false
		}
	}
	return true
}

// replaced checks the end of a replacement: access-key-secret holds the key srv's
// last request carried, mode 0600, the runner file names newID and keeps the pin,
// and neither the new secret's file, the replaced one's, the pending enrolment nor
// the old secret is left.
func replaced(t *testing.T, srv *enrolServer, old *accesskey.Key) {
	t.Helper()
	dir := configDir()
	sent := srv.sent()
	if key := heldKey(t); key.PublicKey().String() != sent[len(sent)-1].PublicKey {
		t.Error("access-key-secret does not hold the key enrolled")
	}
	if m := mode(t, dir.Path(runnerdir.SecretFile)); m != 0o600 {
		t.Errorf("access-key-secret is mode %v", m)
	}
	r, err := config.LoadRunner()
	if err != nil {
		t.Fatal(err)
	}
	if r.Server.AccessKeyID != newID || len(r.Server.Pin) != 1 || r.Server.Pin[0].PublicKey != srv.signer.PublicKey().String() {
		t.Errorf("the runner file reads as %+v", r.Server)
	}
	for _, name := range []string{runnerdir.NewSecretFile, runnerdir.ReplacedFile, runnerdir.PendingFile, runnerdir.AnswerFile} {
		if exists(dir.Path(name)) {
			t.Errorf("%s is still there", name)
		}
	}
	if movedAside(t) != 0 {
		t.Error("a secret was moved aside")
	}
	if !noneHolds(t, old) {
		t.Error("the old secret is still in the directory")
	}
}

// oldLine is the line a replacement ends with, naming the old key by name.
func oldLine(name string) string {
	return "revoke the old key " + name + " on the node's page, unless it is revoked already: until then it still works on the server\n"
}

// TestEnrolReplaceMovesTheMachineToANewKey is --replace on a machine that holds a key:
// the new key's secret takes the old one's place, the runner file names the new key's
// id and keeps the rest, the old secret is gone, and the output ends with the line
// naming the old key by its id, or by its fingerprint when the runner file names no id.
func TestEnrolReplaceMovesTheMachineToANewKey(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	old := newKey(t)
	enrolled(t, srv, old)
	out, errOut, err := runSplit(t, "", "access-key", "enrol", "--replace", srv.URL, srv.code(1, false))
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if out != "" {
		t.Errorf("stdout carries %q", out)
	}
	key := heldKey(t)
	if want := "access key fingerprint " + key.Fingerprint() + "\n" +
		"enrolled as " + newID + " in the node nd_0123456789abcdef\n" +
		"stored secrets: no\n" +
		"the key is active: runs can start\n" +
		"wrote server.access_key_id to " + runnerFile() + "\n" +
		oldLine(oldID); errOut != want {
		t.Errorf("stderr\n%s\nwant\n%s", errOut, want)
	}
	replaced(t, srv, old)

	// A runner file with no access_key_id: the old key is named by its fingerprint.
	emptyDir(t)
	srv = newEnrolServer(t)
	old = newKey(t)
	writeFile(t, runnerFile(), "server:\n  url: "+srv.URL+"\n  apiary_public_key: "+pinLine(srv.signer)+"\ninstance:\n  name: build-01\n")
	writeSecret(t, old)
	out, err = run(t, "access-key", "enrol", "--replace", srv.URL, srv.code(1, false))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.HasSuffix(out, oldLine(old.Fingerprint())) {
		t.Errorf("the output ends\n%s\nwant %q", out, oldLine(old.Fingerprint()))
	}
	replaced(t, srv, old)
}

// TestEnrolReplaceKeepsTheOldKeyOnAFailure is each way --replace fails before the
// server's signed 201: access-key-secret and the runner file stay byte for byte, so
// the old key keeps working. A lost or unsigned answer, key_limit and rate_limited
// keep the new key's secret and the pending enrolment for a retry; a used code, a
// refused key and an answer that pins a fixture key move the new key's secret aside,
// as a plain enrolment does; a code of another server makes no key.
func TestEnrolReplaceKeepsTheOldKeyOnAFailure(t *testing.T) {
	seed, err := base64.RawURLEncoding.DecodeString("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVpbXF1eX2A")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := accesskey.NewKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	const (
		kept = iota
		discarded
		none
	)
	for _, c := range []struct {
		name  string
		setup func(srv *enrolServer)
		// code is the code to enrol with, srv's own when nil.
		code func(srv *enrolServer) string
		// file is the runner file, enrolled's when empty.
		file  func(srv *enrolServer) string
		state int
		want  string
	}{
		{"no answer", func(srv *enrolServer) { srv.Close() }, nil, nil, kept,
			"; run the same command again within 15 minutes and it retries with the same key"},
		{"an unsigned 201", func(srv *enrolServer) { srv.signBy = nil }, nil, nil, kept,
			"the server did not enrol the key (HTTP 201, unsigned); try again later (enrolment: answer_unsigned (status 201))"},
		{"key_limit", func(srv *enrolServer) { srv.refusal("key_limit") }, nil, nil, kept,
			"the node already holds two keys: once an owner or administrator has revoked one, the same command, run within the code's 15 minutes, succeeds"},
		{"rate_limited", func(srv *enrolServer) {
			srv.status, srv.body = http.StatusTooManyRequests, []byte(`{"error":"rate_limited","apiary_public_key":`+srv.keys()+`}`)
		}, nil, nil, kept,
			"the server refused the attempt: this code was tried too often; run the same command again later, within the code's 15 minutes (rate_limited)"},
		{"unauthorized", func(srv *enrolServer) {
			srv.status, srv.body = http.StatusUnauthorized, []byte(`{"error":"unauthorized"}`)
		}, nil, nil, discarded,
			"this code was used or has expired; if you did not use it, tell your administrator, who must revoke the key it enrolled. Enrolling needs a new code; the secret made for it was moved aside to "},
		{"key_invalid", func(srv *enrolServer) { srv.refusal("key_invalid", "public_key") }, nil, nil, discarded,
			"the server refused the key (public_key). Enrolling needs a new code; the secret made for it was moved aside to "},
		{"a fixture pin", func(srv *enrolServer) { srv.signer, srv.signBy = fixture, fixture }, nil,
			func(srv *enrolServer) string {
				return "server:\n  url: " + srv.URL + "\n  access_key_id: " + oldID + "\ninstance:\n  name: build-01\n"
			}, discarded,
			"the server's answer lists the runner contract's published fixture key, whose secret anyone can read: it is no server to pin; runner.yaml was not changed; the secret made for it was moved aside to "},
		{"another server's code", nil, func(srv *enrolServer) string {
			return "qec_F1XT-0RE0-0000-0000-0000-0000-01." + newKey(t).Fingerprint()
		}, nil, none,
			"the enrolment code is for a server whose key this machine does not pin"},
	} {
		emptyDir(t)
		srv := newEnrolServer(t)
		old := newKey(t)
		file := enrolled(t, srv, old)
		if c.file != nil {
			file = c.file(srv)
			writeFile(t, runnerFile(), file)
		}
		if c.setup != nil {
			c.setup(srv)
		}
		code := srv.code(1, false)
		if c.code != nil {
			code = c.code(srv)
		}
		_, err := run(t, "access-key", "enrol", "--replace", srv.URL, code)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		oldStays(t, c.name, old, file)
		dir := configDir()
		staged, pending := exists(dir.Path(runnerdir.NewSecretFile)), exists(dir.Path(runnerdir.PendingFile))
		switch c.state {
		case kept:
			if !staged || !pending || movedAside(t) != 0 {
				t.Errorf("%s: new secret %v, pending %v, moved aside %d", c.name, staged, pending, movedAside(t))
			}
			if staged {
				if m := mode(t, dir.Path(runnerdir.NewSecretFile)); m != 0o600 {
					t.Errorf("%s: the new secret is mode %v", c.name, m)
				}
			}
		case discarded:
			if staged || pending || movedAside(t) != 1 {
				t.Errorf("%s: new secret %v, pending %v, moved aside %d", c.name, staged, pending, movedAside(t))
			}
		case none:
			if staged || pending || movedAside(t) != 0 || len(srv.sent()) != 0 {
				t.Errorf("%s: new secret %v, pending %v, moved aside %d, sent %d", c.name, staged, pending, movedAside(t), len(srv.sent()))
			}
		}
	}
}

// failOnce is a machine whose --replace with code got an unsigned 201: the new key's
// secret and the pending enrolment are there, the old key in place. The server answers
// a signed 201 again from then on.
func failOnce(t *testing.T, srv *enrolServer, old *accesskey.Key, code string) string {
	t.Helper()
	file := enrolled(t, srv, old)
	srv.signBy = nil
	if _, err := run(t, "access-key", "enrol", "--replace", srv.URL, code); err == nil {
		t.Fatal("an unsigned 201 enrolled")
	}
	srv.signBy = srv.signer
	if !exists(configDir().Path(runnerdir.NewSecretFile)) {
		t.Fatal("no new secret was kept")
	}
	return file
}

// TestEnrolReplaceRetriesWithTheNewKey is an answer that does not verify, then the same
// command: it retries with the new key made for the code, and finishes the replacement.
// Another code then over a stale new secret moves that secret aside and makes another.
func TestEnrolReplaceRetriesWithTheNewKey(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	old := newKey(t)
	code := srv.code(1, false)
	failOnce(t, srv, old, code)
	out, err := run(t, "access-key", "enrol", "--replace", srv.URL, code)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "this code was tried within the last 15 minutes: retrying with the key made for it\n", oldLine(oldID))
	sent := srv.sent()
	if len(sent) != 2 || sent[0].PublicKey != sent[1].PublicKey {
		t.Fatalf("sent %+v", sent)
	}
	replaced(t, srv, old)

	// A new secret a --replace with another code left: moved aside, and a new key made.
	emptyDir(t)
	srv = newEnrolServer(t)
	failOnce(t, srv, old, srv.code(1, false))
	dir := configDir()
	out, err = run(t, "access-key", "enrol", "--replace", srv.URL, srv.code(2, false))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "moved the new key of an earlier enrolment, made for another code or more than 15 minutes ago, aside to "+dir.Path(runnerdir.OldPrefix), oldLine(oldID))
	lacks(t, out, "retrying")
	sent = srv.sent()
	if len(sent) != 2 || sent[0].PublicKey == sent[1].PublicKey {
		t.Fatalf("sent %+v", sent)
	}
	if heldKey(t).PublicKey().String() != sent[1].PublicKey || movedAside(t) != 1 {
		t.Errorf("access-key-secret holds another key, or %d moved aside", movedAside(t))
	}
}

// stopAt makes the enrolments stop after step, as a crash there would, until the test
// ends or the returned function is called.
func stopAt(t *testing.T, step string) func() {
	t.Helper()
	restore := cmd.SetAfterStep(func(s string) error {
		if s == step {
			return errors.New("stopped after " + step)
		}
		return nil
	})
	t.Cleanup(restore)
	return restore
}

// heldSecret is what a file of the directory holds, "" when there is none.
func heldSecret(name string) string {
	b, _ := os.ReadFile(configDir().Path(name))
	return string(b)
}

// TestEnrolReplaceFinishesAfterAStop is a --replace that stops after each step that
// follows the server's 201, and the same command run again: it finishes from the
// answer the first run kept, with no request to the server, which would refuse the
// used code, and the copy of the old secret is gone. Run without --replace, or once the
// code's 15 minutes are over, it finishes all the same. A swap whose first step fails
// leaves the old key in place, and the same command finishes once it can.
func TestEnrolReplaceFinishesAfterAStop(t *testing.T) {
	for _, c := range []struct {
		step string
		// newSecret says access-key-secret holds the new key at the stop.
		newSecret bool
		plain     bool
		expired   bool
		// last is the line the output ends with.
		last func(old *accesskey.Key) string
	}{
		{"answer", false, false, false, func(*accesskey.Key) string { return oldLine(oldID) }},
		{"answer", false, true, true, func(*accesskey.Key) string { return oldLine(oldID) }},
		{"replaced", false, false, false, func(*accesskey.Key) string { return oldLine(oldID) }},
		{"secret", true, false, false, func(*accesskey.Key) string { return oldLine(oldID) }},
		{"secret", true, true, true, func(*accesskey.Key) string { return oldLine(oldID) }},
		{"runner", true, false, false, func(old *accesskey.Key) string { return oldLine(old.Fingerprint()) }},
		{"unlinked", true, false, false, func(*accesskey.Key) string { return "wrote server.access_key_id to " + runnerFile() + "\n" }},
		{"pending", true, false, true, func(*accesskey.Key) string { return "wrote server.access_key_id to " + runnerFile() + "\n" }},
	} {
		name := fmt.Sprintf("a stop after %s, run again (without --replace %v, after 15 minutes %v)", c.step, c.plain, c.expired)
		emptyDir(t)
		srv := newEnrolServer(t)
		old := newKey(t)
		file := enrolled(t, srv, old)
		code := srv.code(1, false)
		restore := stopAt(t, c.step)
		if _, err := run(t, "access-key", "enrol", "--replace", srv.URL, code); err == nil || err.Error() != "stopped after "+c.step {
			t.Fatalf("%s: %v", name, err)
		}
		restore()
		dir := configDir()
		if m := mode(t, dir.Path(runnerdir.AnswerFile)); m != 0o600 {
			t.Errorf("%s: the kept answer is mode %v", name, m)
		}
		if (heldSecret(runnerdir.SecretFile) == old.Secret()+"\n") == c.newSecret {
			t.Errorf("%s: access-key-secret holds the new key %v at the stop", name, !c.newSecret)
		}
		if !c.newSecret && readRunnerFile(t) != file {
			t.Errorf("%s: the runner file changed before the new key was in place", name)
		}
		if c.expired {
			normal, err := accesskey.NormaliseCode(code)
			if err != nil {
				t.Fatal(err)
			}
			pub, err := accesskey.ParsePublicKey(srv.sent()[0].PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			if err := dir.WritePending(normal, pub, time.Now().Add(-runnerdir.PendingFor-time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"access-key", "enrol", "--replace", srv.URL, code}
		if c.plain {
			args = []string{"access-key", "enrol", srv.URL, code}
		}
		out, err := run(t, args...)
		if err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
			continue
		}
		if n := len(srv.sent()); n != 1 {
			t.Errorf("%s: %d requests reached the server", name, n)
		}
		lacks(t, out, "retrying")
		if last := c.last(old); !strings.HasSuffix(out, last) {
			t.Errorf("%s: the output ends\n%s\nwant %q", name, out, last)
		}
		replaced(t, srv, old)
	}

	// The first step fails: a directory where the old secret's second name goes.
	emptyDir(t)
	srv := newEnrolServer(t)
	old := newKey(t)
	code := srv.code(1, false)
	file := enrolled(t, srv, old)
	dir := configDir()
	writeFile(t, filepath.Join(dir.Path(runnerdir.ReplacedFile), "x"), "")
	_, err := run(t, "access-key", "enrol", "--replace", srv.URL, code)
	if err == nil || !strings.HasPrefix(err.Error(), "the new key is enrolled, and remove "+dir.Path(runnerdir.ReplacedFile)+": ") || !strings.HasSuffix(err.Error(), "; the old key is still in place: run the same command again") {
		t.Errorf("a step that fails: %v", err)
	}
	if heldSecret(runnerdir.SecretFile) != old.Secret()+"\n" || readRunnerFile(t) != file {
		t.Error("a step that fails changed the old key")
	}
	if !exists(dir.Path(runnerdir.NewSecretFile)) || !exists(dir.Path(runnerdir.AnswerFile)) {
		t.Error("a step that fails dropped the new key or its answer")
	}
	if err := os.RemoveAll(dir.Path(runnerdir.ReplacedFile)); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "access-key", "enrol", "--replace", srv.URL, code); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := len(srv.sent()); n != 1 {
		t.Errorf("%d requests reached the server", n)
	}
	replaced(t, srv, old)

	// A copy of an old secret a swap left goes when the next --replace swaps.
	writeFile(t, dir.Path(runnerdir.ReplacedFile), newKey(t).Secret()+"\n")
	if err := os.Chmod(dir.Path(runnerdir.ReplacedFile), 0o600); err != nil {
		t.Fatal(err)
	}
	before := heldKey(t)
	if out, err := run(t, "access-key", "enrol", "--replace", srv.URL, srv.code(2, false)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	replaced(t, srv, before)
}

// TestEnrolFinishesFromTheKeptAnswer is a plain enrolment whose runner file cannot be
// written after the server's 201: the message says to run the same command again,
// which finishes from the kept answer with no request to the server; a stop before the
// runner file is written finishes the same way.
func TestEnrolFinishesFromTheKeptAnswer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only directory")
	}
	emptyDir(t)
	srv := newEnrolServer(t)
	elsewhere := filepath.Join(tempDir(t), "conf")
	writeFile(t, filepath.Join(elsewhere, "runner.yaml"), "instance:\n  name: build-01\n")
	if err := os.MkdirAll(string(configDir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "runner.yaml"), runnerFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(elsewhere, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(elsewhere, 0o700) })
	code := srv.code(1, false)
	_, err := run(t, "access-key", "enrol", srv.URL, code)
	if err == nil || !strings.HasPrefix(err.Error(), "the key is enrolled, and "+runnerFile()+" could not be written: ") || !strings.HasSuffix(err.Error(), "; run the same command again") {
		t.Fatalf("a runner file that cannot be written: %v", err)
	}
	if err := os.Chmod(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "access-key", "enrol", srv.URL, code)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := len(srv.sent()); n != 1 {
		t.Errorf("%d requests reached the server", n)
	}
	wants(t, out, "enrolled as "+newID, "wrote server.url, server.access_key_id and server.apiary_public_key to "+runnerFile()+"\n")
	if key := heldKey(t); key.PublicKey().String() != srv.sent()[0].PublicKey {
		t.Error("access-key-secret holds another key")
	}
	for _, name := range []string{runnerdir.PendingFile, runnerdir.AnswerFile} {
		if exists(configDir().Path(name)) {
			t.Errorf("%s is still there", name)
		}
	}

	// A stop after the answer is kept, before the runner file is written.
	emptyDir(t)
	srv = newEnrolServer(t)
	writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
	restore := stopAt(t, "answer")
	if _, err := run(t, "access-key", "enrol", srv.URL, srv.code(1, false)); err == nil {
		t.Fatal("did not stop")
	}
	restore()
	if out, err := run(t, "access-key", "enrol", srv.URL, srv.code(1, false)); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := len(srv.sent()); n != 1 {
		t.Errorf("%d requests reached the server", n)
	}
	wants(t, readRunnerFile(t), "access_key_id: "+newID)
}

// TestEnrolRefusesATamperedAnswer is a kept answer changed after the stop: the same
// command refuses it, sends nothing and moves nothing aside, and the old key stays. A
// --replace with another code leaves the new key the kept answer names where it is.
func TestEnrolRefusesATamperedAnswer(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	old := newKey(t)
	file := enrolled(t, srv, old)
	code := srv.code(1, false)
	restore := stopAt(t, "answer")
	if _, err := run(t, "access-key", "enrol", "--replace", srv.URL, code); err == nil {
		t.Fatal("did not stop")
	}
	restore()
	dir := configDir()
	path := dir.Path(runnerdir.AnswerFile)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var kept map[string]any
	if err := json.Unmarshal(b, &kept); err != nil {
		t.Fatal(err)
	}
	body, err := base64.StdEncoding.DecodeString(kept["body"].(string))
	if err != nil {
		t.Fatal(err)
	}
	kept["body"] = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(body), newID, "ak_0123456789abcdee", 1)))
	b, _ = json.Marshal(kept)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	staged := heldSecret(runnerdir.NewSecretFile)
	for _, args := range [][]string{{"--replace", srv.URL, code}, {srv.URL, code}} {
		_, err := run(t, append([]string{"access-key", "enrol"}, args...)...)
		if err == nil || !strings.HasPrefix(err.Error(), path+" holds no answer of the server that verifies (") || !strings.HasSuffix(err.Error(), "), so the enrolment cannot be finished from it; nothing was changed: see https://github.com/qoryai/qory/blob/main/docs/run.md#when-enrolment-answer-is-refused") {
			t.Errorf("%v: %v", args, err)
		}
		oldStays(t, "a tampered answer", old, file)
		if heldSecret(runnerdir.NewSecretFile) != staged || movedAside(t) != 0 || len(srv.sent()) != 1 {
			t.Errorf("%v: the new key moved, %d moved aside, %d sent", args, movedAside(t), len(srv.sent()))
		}
	}
	// Another code: the kept answer is for another enrolment, whose key stays.
	_, err = run(t, "access-key", "enrol", "--replace", srv.URL, srv.code(2, false))
	if want := dir.Path(runnerdir.NewSecretFile) + " holds the new key of an enrolment the server answered for another code: run that command again to finish it"; err == nil || err.Error() != want {
		t.Errorf("another code: %v, want %q", err, want)
	}
	if heldSecret(runnerdir.NewSecretFile) != staged || movedAside(t) != 0 || len(srv.sent()) != 1 {
		t.Error("another code moved the answered key")
	}
}

// TestEnrolReplaceNeedsNoOldKeyOnTheServer is --replace on a machine whose key the
// server has revoked: the enrolment carries the code and the new key alone, so it
// succeeds.
func TestEnrolReplaceNeedsNoOldKeyOnTheServer(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	old := newKey(t)
	enrolled(t, srv, old)
	srv.enrolled[old.PublicKey().String()] = true
	out, err := run(t, "access-key", "enrol", "--replace", srv.URL, srv.code(1, false))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	sent := srv.sent()
	if len(sent) != 1 || sent[0].PublicKey == old.PublicKey().String() {
		t.Fatalf("sent %+v", sent)
	}
	wants(t, out, oldLine(oldID))
	replaced(t, srv, old)
}

// TestEnrolReplaceRefusesPrint is --replace with --print: refused before anything is
// read, written or sent.
func TestEnrolReplaceRefusesPrint(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	old := newKey(t)
	file := enrolled(t, srv, old)
	before := snapshot(t, string(configDir()))
	_, err := run(t, "access-key", "enrol", "--replace", "--print", srv.URL, srv.code(1, false))
	if want := "--replace and --print do not go together: --print keeps nothing on this machine, so it replaces nothing"; cmd.ExitCode(err) != cmd.ExitInput || err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
	oldStays(t, "--replace --print", old, file)
	if after := snapshot(t, string(configDir())); len(after) != len(before) || len(srv.sent()) != 0 {
		t.Errorf("files before %v, after %v; sent %d", before, after, len(srv.sent()))
	}
}

// TestEnrolReplaceWithNoKeyEnrols is --replace on a machine that holds no key: it
// enrols as the command does without it, with no line about an old key.
func TestEnrolReplaceWithNoKeyEnrols(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
	out, errOut, err := runSplit(t, "", "access-key", "enrol", "--replace", srv.URL, srv.code(1, false))
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	key := heldKey(t)
	if want := string(configDir()) + " is now mode 0700: it holds the access key's secret\n" +
		"access key fingerprint " + key.Fingerprint() + "\n" +
		"enrolled as " + newID + " in the node nd_0123456789abcdef\n" +
		"stored secrets: no\n" +
		"the key is active: runs can start\n" +
		"wrote server.url, server.access_key_id and server.apiary_public_key to " + runnerFile() + "\n"; errOut != want {
		t.Errorf("stderr\n%s\nwant\n%s", errOut, want)
	}
	dir := configDir()
	for _, name := range []string{runnerdir.NewSecretFile, runnerdir.ReplacedFile, runnerdir.PendingFile} {
		if exists(dir.Path(name)) {
			t.Errorf("%s is there", name)
		}
	}
}

// fixtureSigner is the runner contract's published fixture signing key.
func fixtureSigner(t *testing.T) *accesskey.Key {
	t.Helper()
	seed, err := base64.RawURLEncoding.DecodeString("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVpbXF1eX2A")
	if err != nil {
		t.Fatal(err)
	}
	k, err := accesskey.NewKey(seed)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestARefusalNeverTouchesTheActiveKey is a refusal, a 401, a signed key_invalid and a
// 201 that pins the fixture key, to an enrolment run on a machine whose
// access-key-secret holds an active key: one a finished enrolment left, with --replace
// and a new key staged in access-key-secret.new, and one a swap put in place before its
// kept answer was lost, with the pending enrolment of the code for it, which the same
// command retries. access-key-secret is the same, byte for byte, afterwards; only a
// staged access-key-secret.new is moved aside.
func TestARefusalNeverTouchesTheActiveKey(t *testing.T) {
	says := map[string]string{
		"unauthorized": "this code was used or has expired",
		"key_invalid":  "the server refused the key",
		"fixture":      "the server's answer lists the runner contract's published fixture key",
	}
	for _, refusal := range []string{"unauthorized", "key_invalid", "fixture"} {
		// A finished enrolment, then --replace, which stages a new key.
		emptyDir(t)
		srv := newEnrolServer(t)
		if refusal == "fixture" {
			srv.signer, srv.signBy = fixtureSigner(t), fixtureSigner(t)
			writeSecret(t, newKey(t))
			writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
		} else {
			enrolled(t, srv, newKey(t))
		}
		code := srv.code(1, false)
		switch refusal {
		case "unauthorized":
			srv.used[mustNormalise(t, code)] = true
		case "key_invalid":
			srv.refusal("key_invalid", "public_key")
		}
		active := heldSecret(runnerdir.SecretFile)
		_, err := run(t, "access-key", "enrol", "--replace", srv.URL, code)
		if err == nil || !strings.Contains(err.Error(), says[refusal]) {
			t.Fatalf("%s after a finished enrolment: %v", refusal, err)
		}
		if heldSecret(runnerdir.SecretFile) != active {
			t.Errorf("%s after a finished enrolment: access-key-secret changed", refusal)
		}
		if exists(configDir().Path(runnerdir.NewSecretFile)) || movedAside(t) != 1 {
			t.Errorf("%s after a finished enrolment: the staged key was not moved aside (%d moved)", refusal, movedAside(t))
		}

		// A swap that put the new key in place, its kept answer lost, then the same
		// command again, with --replace and without: it retries with the key in
		// access-key-secret.
		for _, plain := range []bool{false, true} {
			emptyDir(t)
			srv := newEnrolServer(t)
			if refusal == "fixture" {
				srv.signer, srv.signBy = fixtureSigner(t), fixtureSigner(t)
			}
			code := srv.code(1, false)
			if refusal == "fixture" {
				// The fixture server's 201 is refused before any swap, so the state is
				// made by hand.
				key := newKey(t)
				writeSecret(t, key)
				writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
				if err := configDir().WritePending(mustNormalise(t, code), key.PublicKey(), time.Now()); err != nil {
					t.Fatal(err)
				}
			} else {
				enrolled(t, srv, newKey(t))
				restore := stopAt(t, "secret")
				if _, err := run(t, "access-key", "enrol", "--replace", srv.URL, code); err == nil {
					t.Fatal("did not stop")
				}
				restore()
				if err := os.Remove(configDir().Path(runnerdir.AnswerFile)); err != nil {
					t.Fatal(err)
				}
				if refusal == "key_invalid" {
					// The code is not used up yet, so the server answers the enrolled key.
					srv.used = map[string]bool{}
				}
			}
			active := heldSecret(runnerdir.SecretFile)
			args := []string{"--replace", srv.URL, code}
			if plain {
				args = args[1:]
			}
			out, err := run(t, append([]string{"access-key", "enrol"}, args...)...)
			if err == nil || !strings.Contains(err.Error(), says[refusal]) {
				t.Fatalf("%s after a swap that lost its answer, %v: %v", refusal, args, err)
			}
			wants(t, out, "retrying with the key made for it")
			lacks(t, err.Error(), "moved aside")
			if heldSecret(runnerdir.SecretFile) != active || movedAside(t) != 0 {
				t.Errorf("%s after a swap that lost its answer, %v: access-key-secret changed, %d moved aside", refusal, args, movedAside(t))
			}
		}
	}
}

// mustNormalise is code as the server keeps it.
func mustNormalise(t *testing.T, code string) string {
	t.Helper()
	normal, err := accesskey.NormaliseCode(code)
	if err != nil {
		t.Fatal(err)
	}
	return normal
}

// TestALostAnswerKeepsTheActiveKey is an answer that could not be kept, then a step
// after the swap that fails, and then the same command again, which posts the used code
// and gets a 401; and an answer kept and then deleted after the swap, with the same
// rerun. Both with --replace and without: the key the swap put in access-key-secret,
// active on the server, stays there, byte for byte, and nothing is moved aside.
func TestALostAnswerKeepsTheActiveKey(t *testing.T) {
	for _, lost := range []string{"not kept", "deleted"} {
		for _, replace := range []bool{true, false} {
			name := fmt.Sprintf("an answer %s, --replace %v", lost, replace)
			emptyDir(t)
			srv := newEnrolServer(t)
			if replace {
				enrolled(t, srv, newKey(t))
			} else {
				writeFile(t, runnerFile(), "instance:\n  name: build-01\n")
			}
			dir := configDir()
			if lost == "not kept" {
				// A non-empty directory where the answer is first written.
				writeFile(t, filepath.Join(dir.Path(runnerdir.AnswerFile+".tmp"), "x"), "")
			}
			args := []string{"access-key", "enrol", srv.URL, srv.code(1, false)}
			if replace {
				args = []string{"access-key", "enrol", "--replace", srv.URL, srv.code(1, false)}
			}
			restore := stopAt(t, "secret")
			if _, err := run(t, args...); err == nil || err.Error() != "stopped after secret" {
				t.Fatalf("%s: %v", name, err)
			}
			restore()
			if lost == "deleted" {
				if err := os.Remove(dir.Path(runnerdir.AnswerFile)); err != nil {
					t.Fatal(err)
				}
			} else if exists(dir.Path(runnerdir.AnswerFile)) {
				t.Fatalf("%s: the answer was kept", name)
			}
			k, err := dir.ReadSecret()
			if err != nil || k.PublicKey().String() != srv.sent()[0].PublicKey {
				t.Fatalf("%s: access-key-secret does not hold the enrolled key: %v", name, err)
			}
			active := heldSecret(runnerdir.SecretFile)
			_, err = run(t, args...)
			if err == nil || !strings.Contains(err.Error(), "this code was used or has expired") {
				t.Errorf("%s: %v", name, err)
			}
			if heldSecret(runnerdir.SecretFile) != active || movedAside(t) != 0 {
				t.Errorf("%s: access-key-secret changed, %d moved aside", name, movedAside(t))
			}
			if n := len(srv.sent()); n != 2 {
				t.Errorf("%s: %d requests", name, n)
			}
		}
	}
}
