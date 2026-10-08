package cmd_test

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	for _, name := range []string{runnerdir.NewSecretFile, runnerdir.ReplacedFile, runnerdir.PendingFile} {
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
	return "the old key " + name + " still works on the server until an owner or administrator revokes it on the node's page; revoke it there\n"
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
	wants(t, out, "moved the new key of an earlier --replace, made for another code or more than 15 minutes ago, aside to "+dir.Path(runnerdir.OldPrefix), oldLine(oldID))
	lacks(t, out, "retrying")
	sent = srv.sent()
	if len(sent) != 2 || sent[0].PublicKey == sent[1].PublicKey {
		t.Fatalf("sent %+v", sent)
	}
	if heldKey(t).PublicKey().String() != sent[1].PublicKey || movedAside(t) != 1 {
		t.Errorf("access-key-secret holds another key, or %d moved aside", movedAside(t))
	}
}

// TestEnrolReplaceFinishesAfterAStop is the state each step of the swap leaves when the
// command stops after it, and the same command run again: each finishes the
// replacement, and the copy of the old secret is gone. A swap whose first step fails
// leaves the old key in place, and the same command finishes once it can.
func TestEnrolReplaceFinishesAfterAStop(t *testing.T) {
	link := func(t *testing.T) {
		dir := configDir()
		if err := os.Link(dir.Path(runnerdir.SecretFile), dir.Path(runnerdir.ReplacedFile)); err != nil {
			t.Fatal(err)
		}
	}
	rename := func(t *testing.T) {
		dir := configDir()
		if err := os.Rename(dir.Path(runnerdir.NewSecretFile), dir.Path(runnerdir.SecretFile)); err != nil {
			t.Fatal(err)
		}
	}
	write := func(t *testing.T, srv *enrolServer) {
		if err := config.WriteEnrolment(runnerFile(), config.Enrolment{URL: srv.URL, AccessKeyID: newID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		stop  func(t *testing.T, srv *enrolServer)
		plain bool
		// last is the line the output ends with; "" is the runner file's line.
		last func(old *accesskey.Key) string
	}{
		{"after the old secret is linked", func(t *testing.T, _ *enrolServer) { link(t) }, false,
			func(*accesskey.Key) string { return oldLine(oldID) }},
		{"after the new secret takes its place", func(t *testing.T, _ *enrolServer) { link(t); rename(t) }, false,
			func(*accesskey.Key) string { return oldLine(oldID) }},
		{"after the new secret takes its place, run without --replace", func(t *testing.T, _ *enrolServer) { link(t); rename(t) }, true,
			func(*accesskey.Key) string { return oldLine(oldID) }},
		{"after the runner file names the new key", func(t *testing.T, srv *enrolServer) { link(t); rename(t); write(t, srv) }, false,
			func(old *accesskey.Key) string { return oldLine(old.Fingerprint()) }},
		{"after the old secret is removed", func(t *testing.T, srv *enrolServer) {
			link(t)
			rename(t)
			write(t, srv)
			if err := os.Remove(configDir().Path(runnerdir.ReplacedFile)); err != nil {
				t.Fatal(err)
			}
		}, false, func(*accesskey.Key) string { return "" }},
	} {
		emptyDir(t)
		srv := newEnrolServer(t)
		old := newKey(t)
		code := srv.code(1, false)
		failOnce(t, srv, old, code)
		c.stop(t, srv)
		args := []string{"access-key", "enrol", "--replace", srv.URL, code}
		if c.plain {
			args = []string{"access-key", "enrol", srv.URL, code}
		}
		out, err := run(t, args...)
		if err != nil {
			t.Errorf("%s: %v\n%s", c.name, err, out)
			continue
		}
		last := c.last(old)
		if last == "" {
			last = "wrote server.access_key_id to " + runnerFile() + "\n"
		}
		if !strings.HasSuffix(out, last) {
			t.Errorf("%s: the output ends\n%s\nwant %q", c.name, out, last)
		}
		replaced(t, srv, old)
	}

	// The first step fails: a directory where the old secret's second name goes.
	emptyDir(t)
	srv := newEnrolServer(t)
	old := newKey(t)
	code := srv.code(1, false)
	file := failOnce(t, srv, old, code)
	dir := configDir()
	writeFile(t, filepath.Join(dir.Path(runnerdir.ReplacedFile), "x"), "")
	_, err := run(t, "access-key", "enrol", "--replace", srv.URL, code)
	if err == nil || !strings.HasPrefix(err.Error(), "the new key is enrolled, and remove "+dir.Path(runnerdir.ReplacedFile)+": ") || !strings.HasSuffix(err.Error(), "; the old key is still in place: run the same command again within 15 minutes") {
		t.Errorf("a step that fails: %v", err)
	}
	if b, err := os.ReadFile(dir.Path(runnerdir.SecretFile)); err != nil || string(b) != old.Secret()+"\n" || readRunnerFile(t) != file {
		t.Error("a step that fails changed the old key")
	}
	if !exists(dir.Path(runnerdir.NewSecretFile)) || !exists(dir.Path(runnerdir.PendingFile)) {
		t.Error("a step that fails dropped the new key")
	}
	if err := os.RemoveAll(dir.Path(runnerdir.ReplacedFile)); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, "access-key", "enrol", "--replace", srv.URL, code); err != nil {
		t.Fatalf("%v\n%s", err, out)
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

// TestEnrolReplaceNeedsNoOldKeyOnTheServer is --replace on a machine whose key the
// server has revoked: the enrolment carries the code and the new key alone, so it
// succeeds.
func TestEnrolReplaceNeedsNoOldKeyOnTheServer(t *testing.T) {
	emptyDir(t)
	srv := newEnrolServer(t)
	old := newKey(t)
	enrolled(t, srv, old)
	srv.revoked = map[string]bool{old.PublicKey().String(): true}
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
