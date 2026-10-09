package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/ui"
)

// unsignedCredential is a run credential in a JWT's shape whose claims are claims, with
// a signature no one checks here: qory reads exp alone, and verifies nothing.
func unsignedCredential(claims string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"EdDSA","kid":"k1","typ":"JWT"}`)) + "." + enc.EncodeToString([]byte(claims)) + "." + enc.EncodeToString([]byte("signature"))
}

// streamOf is a run credential stream whose descriptor gave v and has ended.
func streamOf(v string) *credentialStream {
	s := &credentialStream{v: v, done: make(chan struct{})}
	close(s.done)
	return s
}

// TestTheRunCredentialComesFromOneSourceInOrder is the run credential's sources:
// --run-credential-fd wins over QORY_RUN_CREDENTIAL_SECRET, which wins over
// session.gateway.run_credential_file, resolved under forager.yaml's directory; an empty
// descriptor and none of them is no run credential.
func TestTheRunCredentialComesFromOneSourceInOrder(t *testing.T) {
	t.Cleanup(func() { config.SetServerVariables(config.ServerVariables{}) })
	r := &config.Forager{File: "/etc/qory/forager.yaml", SessionGateway: &config.ForagerSessionGateway{URL: "https://gateway.example", RunCredentialFile: "run-credential"}}
	fd := streamOf("from-the-descriptor")
	config.SetServerVariables(config.ServerVariables{RunCredentialSecret: " from-the-variable\n"})
	if c := credentialSource(r, fd); c == nil || c.fd != fd || c.variable != "" || c.file != "" {
		t.Errorf("the descriptor and the rest: %+v", c)
	}
	if c := credentialSource(r, streamOf("")); c != nil {
		t.Errorf("an empty descriptor: %+v", c)
	}
	if c := credentialSource(r, nil); c == nil || c.fd != nil || c.variable != "from-the-variable" || c.file != "" {
		t.Errorf("the variable and the file: %+v", c)
	}
	config.SetServerVariables(config.ServerVariables{})
	if c := credentialSource(r, nil); c == nil || c.fd != nil || c.variable != "" || c.file != "/etc/qory/run-credential" {
		t.Errorf("the file alone: %+v", c)
	}
	r.SessionGateway.RunCredentialFile = ""
	if c := credentialSource(r, nil); c != nil {
		t.Errorf("none: %+v", c)
	}
}

// TestTheRunCredentialFileIsReadBeforeEachRequest is a run credential from its file:
// read again on every request, so a refreshed one is sent from then on, its white space
// trimmed, and a file that is gone an error that names the file and holds no credential.
func TestTheRunCredentialFileIsReadBeforeEachRequest(t *testing.T) {
	file := filepath.Join(t.TempDir(), "run-credential")
	c := &runCredential{file: file}
	for _, v := range []string{"first.credential.value", "second.credential.value"} {
		if err := os.WriteFile(file, []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := c.get(context.Background()); err != nil || got != v {
			t.Errorf("read %q, %v, want %q", got, err, v)
		}
	}
	os.Remove(file)
	if _, err := c.get(context.Background()); err == nil || !strings.Contains(err.Error(), file) || strings.Contains(err.Error(), "second") {
		t.Errorf("a file that is gone: %v", err)
	}
}

// TestTheRunCredentialFileGrantsTheGroupAndOthersNoReadOrWrite is the mode of the run
// credential's file, checked before anything starts and on each read before a request:
// one that grants the group or others read or write is refused in the words of decision
// 159, unread; execute alone, and the owner's own bits, pass. A change to 0644 between
// two requests is caught at the second. No error holds the credential.
func TestTheRunCredentialFileGrantsTheGroupAndOthersNoReadOrWrite(t *testing.T) {
	const v = "header.claims.signature"
	file := filepath.Join(t.TempDir(), "run-credential")
	if err := os.WriteFile(file, []byte(v+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &runCredential{file: file}
	for _, m := range []os.FileMode{0o600, 0o400, 0o700, 0o611} {
		if err := os.Chmod(file, m); err != nil {
			t.Fatal(err)
		}
		if err := c.checkMode(); err != nil {
			t.Errorf("%04o before anything starts: %v", m, err)
		}
		if got, err := c.get(context.Background()); err != nil || got != v {
			t.Errorf("%04o: read %d bytes, %v", m, len(got), err)
		}
	}
	for _, m := range []os.FileMode{0o640, 0o604, 0o660, 0o606, 0o644} {
		if err := os.Chmod(file, m); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("%s is mode %04o, which grants access to the group or others: chmod 600 %s", file, m, file)
		if err := c.checkMode(); err == nil || err.Error() != want || strings.Contains(err.Error(), v) {
			t.Errorf("%04o before anything starts: %v, want %q", m, err, want)
		}
		got, err := c.get(context.Background())
		if got != "" || err == nil || err.Error() != want || strings.Contains(err.Error(), v) {
			t.Errorf("%04o: read %d bytes, %v, want %q", m, len(got), err, want)
		}
	}
	// Between two requests.
	os.Chmod(file, 0o600)
	if _, err := c.get(context.Background()); err != nil {
		t.Fatal(err)
	}
	os.Chmod(file, 0o644)
	if got, err := c.get(context.Background()); got != "" || err == nil || err.Error() != file+" is mode 0644, which grants access to the group or others: chmod 600 "+file {
		t.Errorf("a change to 0644 between requests: read %d bytes, %v", len(got), err)
	}
	// A file that is not there yet is left to the read before the first request.
	if err := (&runCredential{file: filepath.Join(t.TempDir(), "none")}).checkMode(); err != nil {
		t.Errorf("a file that is not there: %v", err)
	}
	if err := (&runCredential{variable: v}).checkMode(); err != nil {
		t.Errorf("a credential from the variable: %v", err)
	}
	if err := (&runCredential{fd: streamOf(v)}).checkMode(); err != nil {
		t.Errorf("a credential from the descriptor: %v", err)
	}
}

// TestTheRunCredentialFileModeIsTheLinksTarget is a run credential file that is a link:
// the mode checked is that of the file the link leads to, the one read.
func TestTheRunCredentialFileModeIsTheLinksTarget(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target"), filepath.Join(dir, "run-credential")
	if err := os.WriteFile(target, []byte("header.claims.signature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	c := &runCredential{file: link}
	if err := c.checkMode(); err != nil {
		t.Errorf("a link to a file of mode 0600: %v", err)
	}
	if _, err := c.get(context.Background()); err != nil {
		t.Errorf("a link to a file of mode 0600: %v", err)
	}
	os.Chmod(target, 0o644)
	want := link + " is mode 0644, which grants access to the group or others: chmod 600 " + link
	if err := c.checkMode(); err == nil || err.Error() != want {
		t.Errorf("a link to a file of mode 0644 before anything starts: %v, want %q", err, want)
	}
	if _, err := c.get(context.Background()); err == nil || err.Error() != want {
		t.Errorf("a link to a file of mode 0644: %v, want %q", err, want)
	}
}

// TestTheRunCredentialsExpiryIsWordedBySource is the run's end at the run credential's
// exp, in the words of where it came from: its file, the descriptor, or the variable,
// read once. A credential whose exp qory cannot read leaves it unsaid.
func TestTheRunCredentialsExpiryIsWordedBySource(t *testing.T) {
	exp := time.Date(2026, 10, 9, 12, 30, 0, 0, time.UTC)
	cred := unsignedCredential(`{"sub":"run-1","exp":` + strconv.FormatInt(exp.Unix(), 10) + `}`)
	file := filepath.Join(t.TempDir(), "run-credential")
	if err := os.WriteFile(file, []byte(cred), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile := &runCredential{file: file}
	fromFD := &runCredential{fd: streamOf(cred)}
	fromVariable := &runCredential{variable: cred}
	for _, c := range []*runCredential{fromFile, fromFD, fromVariable} {
		if _, err := c.get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := fromFile.expired(), "the run credential expired at 2026-10-09T12:30:00Z, and its file holds no fresh one"; got != want {
		t.Errorf("from its file: %q, want %q", got, want)
	}
	if got, want := fromFD.expired(), "the run credential expired at 2026-10-09T12:30:00Z, and the descriptor gave no fresh one"; got != want {
		t.Errorf("from the descriptor: %q, want %q", got, want)
	}
	if got, want := fromVariable.expired(), "the run credential expired at 2026-10-09T12:30:00Z; QORY_RUN_CREDENTIAL_SECRET is read once, so a run longer than its credential needs session.gateway.run_credential_file or --run-credential-fd"; got != want {
		t.Errorf("from the variable: %q, want %q", got, want)
	}
	for _, v := range []string{"not-a-jwt", unsignedCredential(`{"sub":"run-1"}`), unsignedCredential(`{"exp":"soon"}`)} {
		for _, c := range []*runCredential{{variable: v}, {fd: streamOf(v)}} {
			c.get(context.Background())
			if got := c.expired(); got != "" {
				t.Errorf("%s: %q, want nothing", v, got)
			}
		}
	}
}

// TestASeparateGatewaysEndIsSaid is a run a separate gateway ended: at the run
// credential's expiry, a failure in the words of its source; at its issuer's end, the
// line of decision 131; any other close is the caller's to say.
func TestASeparateGatewaysEndIsSaid(t *testing.T) {
	c := &runCredential{variable: unsignedCredential(`{"exp":1791549000}`)}
	c.get(context.Background())
	var out bytes.Buffer
	if !gatewayEnded(ui.New(&out), &out, event.ReasonRunEndedAtIssuer, c) || out.String() != "qory run: the gateway ended the run: the run credential's issuer reports that the run has ended\n" {
		t.Errorf("the issuer's end: %q", out.String())
	}
	out.Reset()
	if !gatewayEnded(ui.New(&out), &out, event.ReasonCredentialExpired, c) || !strings.HasSuffix(out.String(), " the run credential expired at "+time.Unix(1791549000, 0).UTC().Format(time.RFC3339)+"; QORY_RUN_CREDENTIAL_SECRET is read once, so a run longer than its credential needs session.gateway.run_credential_file or --run-credential-fd\n") {
		t.Errorf("the expiry: %q", out.String())
	}
	out.Reset()
	for _, reason := range []string{event.ReasonSessionLost, event.ReasonBatchRefused} {
		if gatewayEnded(ui.New(&out), &out, reason, c) || out.Len() != 0 {
			t.Errorf("%s: said %q", reason, out.String())
		}
	}
	if gatewayEnded(ui.New(&out), &out, event.ReasonCredentialExpired, &runCredential{variable: "no-exp"}) || out.Len() != 0 {
		t.Errorf("an expiry qory cannot read: said %q", out.String())
	}
}

// TestASeparateGatewaysRefusalsAreWorded is each refusal of a separate gateway's that
// qory words: of the run credential; of a checkout whose forge, repository or both differ
// from the credential's; of keys of --details that differ. A refusal of a label other
// than forge and repository, which qory never sends behind a gateway, and any other
// error are left to the caller. Each said one unwraps to the refusal.
func TestASeparateGatewaysRefusalsAreWorded(t *testing.T) {
	labels := map[string]string{"forge": "git.example.com", "repository": "acme/app"}
	about := &session.About{Details: []byte(`{"requester":"a","ticket":7,"other":"kept"}`)}
	ref := func(code string, names ...string) error {
		return &accesskey.Refusal{From: accesskey.FromGateway, Code: code, Names: names}
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{ref(refusal.RunCredentialRefused), "the gateway refused this run credential"},
		{ref(refusal.TargetDiffersFromCredential, "labels.repository=acme/other"), "this checkout is git.example.com/acme/app, and the run credential is for git.example.com/acme/other"},
		{ref(refusal.TargetDiffersFromCredential, "labels.forge=forge.example"), "this checkout is git.example.com/acme/app, and the run credential is for forge.example/acme/app"},
		{ref(refusal.TargetDiffersFromCredential, "labels.forge=forge.example", "labels.repository=acme/other"), "this checkout is git.example.com/acme/app, and the run credential is for forge.example/acme/other"},
		{ref(refusal.DiffersFromCredential, "about.details.requester=b"), `--details details.json sets requester to "a", and the run credential says "b": the credential decides; remove the key`},
		{ref(refusal.DiffersFromCredential, "about.details.requester=b", "about.details.ticket=8"), `--details details.json sets requester to "a", and the run credential says "b": the credential decides; remove the key; --details details.json sets ticket to 7, and the run credential says "8": the credential decides; remove the key`},
		{ref(refusal.DiffersFromCredential, "labels.run_key=queue/1"), ""},
		{ref(refusal.RunIDUsed), ""},
		{errors.New("not a refusal"), ""},
	} {
		got := credentialRefused(c.err, labels, "details.json", about)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%v: said %q", c.err, got)
		case c.want != "" && (got == nil || got.Error() != c.want):
			t.Errorf("%v: %v, want %q", c.err, got, c.want)
		case got != nil && !errors.Is(got, c.err):
			t.Errorf("%v: does not unwrap to the refusal", c.err)
		}
	}
}

// credentialPipe is a pipe whose read end is a raw descriptor without close-on-exec, as
// one qory run inherits, and its write end, closed when the test ends.
func credentialPipe(t *testing.T) (int, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Dup(int(r.Fd()))
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return fd, w
}

// writeLine writes s to w, failing the test on an error.
func writeLine(t *testing.T, w *os.File, s string) {
	t.Helper()
	if _, err := w.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// becomes waits for the stream's run credential to be want, and fails the test, without
// the values, when it is not within a few seconds.
func becomes(t *testing.T, s *credentialStream, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if s.latest() == want {
			return
		}
	}
	t.Fatalf("the run credential is not the %d-byte line written last", len(want))
}

// stays checks that the stream's run credential is still want a moment later.
func stays(t *testing.T, s *credentialStream, want string) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	if s.latest() != want {
		t.Fatalf("the run credential changed to a line it should not take (%d bytes)", len(s.latest()))
	}
}

// ended waits for the stream's reader to end.
func ended(t *testing.T, s *credentialStream) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the descriptor is still read")
	}
}

// TestTheRunCredentialDescriptorIsAStream is --run-credential-fd as a stream: qory starts
// with the first complete line that is not empty, and each later complete line replaces
// it. A partial line waits for its newline; empty lines, a line end of \r\n and
// surrounding white space are not credentials; the descriptor's end keeps the latest,
// and drops a line it cut short.
func TestTheRunCredentialDescriptorIsAStream(t *testing.T) {
	fd, w := credentialPipe(t)
	writeLine(t, w, "\n \r\nfirst.credential.one\r\n")
	s, err := readCredentialFD(fd)
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop()
	if got := s.latest(); got != "first.credential.one" {
		t.Fatalf("started with %d bytes, want the first line", len(got))
	}
	writeLine(t, w, "second.credential.two\nthird.credential.three\n")
	becomes(t, s, "third.credential.three")
	writeLine(t, w, "fourth.cred")
	stays(t, s, "third.credential.three")
	writeLine(t, w, "ential.four\n")
	becomes(t, s, "fourth.credential.four")
	writeLine(t, w, "\n\r\n   \n")
	stays(t, s, "fourth.credential.four")
	writeLine(t, w, "  fifth.credential.five \n")
	becomes(t, s, "fifth.credential.five")
	writeLine(t, w, "cut.short")
	w.Close()
	ended(t, s)
	if got := s.latest(); got != "fifth.credential.five" {
		t.Errorf("after the descriptor's end: %d bytes, want the latest complete line", len(got))
	}
}

// TestTheRunCredentialDescriptorStartsWithWhatItHolds is a descriptor its writer closed
// before qory read it: one credential without a newline is the credential, as before;
// several lines are read through to the latest; nothing, or empty lines alone, is no
// credential, which qory refuses as it refuses none.
func TestTheRunCredentialDescriptorStartsWithWhatItHolds(t *testing.T) {
	for _, c := range []struct{ name, wrote, want string }{
		{"one credential without a newline", "only.credential.value", "only.credential.value"},
		{"one credential with surrounding white space", "  only.credential.value \r\n", "only.credential.value"},
		{"several lines", "first.credential\nsecond.credential\nlatest.credential\n", "latest.credential"},
		{"a partial last line", "first.credential\ncut.short", "first.credential"},
		{"nothing", "", ""},
		{"empty lines", "\n\r\n \n", ""},
	} {
		fd, w := credentialPipe(t)
		writeLine(t, w, c.wrote)
		w.Close()
		s, err := readCredentialFD(fd)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		ended(t, s)
		s.stop()
		if got := s.latest(); got != c.want {
			t.Errorf("%s: %d bytes, want %d", c.name, len(got), len(c.want))
		}
		r := &config.Forager{File: "/etc/qory/forager.yaml", SessionGateway: &config.ForagerSessionGateway{URL: "https://gateway.example"}}
		if got := credentialSource(r, s); (got == nil) != (c.want == "") {
			t.Errorf("%s: a source %v, want one: %v", c.name, got != nil, c.want != "")
		}
	}
}

// TestTheRunCredentialDescriptorLimitsEachLine is the limit of a run credential, on each
// line, its line end aside: a first line longer than it is refused, at once, before its
// line end, and the refusal holds nothing of it; a later one is skipped, and the line
// after it is taken. A line of the limit itself is a credential.
func TestTheRunCredentialDescriptorLimitsEachLine(t *testing.T) {
	long := strings.Repeat("x", maxCredentialFD+1)
	exact := strings.Repeat("y", maxCredentialFD)
	for _, c := range []struct{ name, wrote string }{
		{"a first line over the limit", long + "\n"},
		{"a first line over the limit, its line end not written yet", long + "x"},
		{"a first line over the limit, with \\r\\n", long + "\r\n"},
	} {
		fd, w := credentialPipe(t)
		// More than a pipe holds: written as qory reads it, and the writer kept open.
		go w.WriteString(c.wrote)
		_, err := readCredentialFD(fd)
		want := fmt.Sprintf("--run-credential-fd %d: more than a run credential", fd)
		if err == nil || err.Error() != want || ExitCode(err) != ExitInput {
			t.Errorf("%s: %v, want %q", c.name, err, want)
		}
		w.Close()
	}
	fd, w := credentialPipe(t)
	go w.WriteString(exact + "\r\n")
	s, err := readCredentialFD(fd)
	if err != nil {
		t.Fatalf("a line of the limit: %v", err)
	}
	defer s.stop()
	if s.latest() != exact {
		t.Fatalf("a line of the limit: %d bytes", len(s.latest()))
	}
	writeLine(t, w, "next.credential\n")
	becomes(t, s, "next.credential")
	go func() {
		w.WriteString(long + "\n")
		w.WriteString("after.the.long.line\n")
	}()
	becomes(t, s, "after.the.long.line")
	go func() {
		w.WriteString(long + long)
		w.WriteString("\n")
		w.WriteString("after.two.limits\n")
	}()
	becomes(t, s, "after.two.limits")
}

// TestTheRunCredentialDescriptorIsNotInherited is the descriptor while it is read: a
// program qory starts, through os/exec, does not have it, though it had the descriptor
// as qory inherited it. When the run ends, qory closes the descriptor, and no one holds
// it.
func TestTheRunCredentialDescriptorIsNotInherited(t *testing.T) {
	fd, w := credentialPipe(t)
	has := func() string {
		t.Helper()
		out, err := exec.Command("/bin/sh", "-c", "if test -e /dev/fd/"+strconv.Itoa(fd)+"; then echo inherited; else echo not inherited; fi").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := has(); got != "inherited" {
		t.Fatalf("the descriptor as qory inherits it: %s", got)
	}
	writeLine(t, w, "first.credential\n")
	s, err := readCredentialFD(fd)
	if err != nil {
		t.Fatal(err)
	}
	if got := has(); got != "not inherited" {
		t.Errorf("while it is read: %s", got)
	}
	s.stop()
	ended(t, s)
	// No one holds the read end any more: qory closed it, and nothing it started has it.
	if _, err := w.WriteString("after.the.run\n"); !errors.Is(err, syscall.EPIPE) {
		t.Errorf("a write after the run ended: %v, want a broken pipe", err)
	}
	if got := s.latest(); got != "first.credential" {
		t.Errorf("after the run ended: %d bytes", len(got))
	}
}

// TestTheRunCredentialDescriptorsErrorsHoldNoCredential is each refusal of
// --run-credential-fd: the standard descriptors, one that is not open, and a line too
// long; none holds what the descriptor gave.
func TestTheRunCredentialDescriptorsErrorsHoldNoCredential(t *testing.T) {
	for _, n := range []int{0, 1, 2} {
		want := fmt.Sprintf("--run-credential-fd %d: the standard input, output and error carry no run credential; name a descriptor of 3 or above", n)
		if _, err := readCredentialFD(n); err == nil || err.Error() != want {
			t.Errorf("%d: %v, want %q", n, err, want)
		}
	}
	if _, err := readCredentialFD(1000); err == nil || !strings.HasPrefix(err.Error(), "--run-credential-fd 1000: ") {
		t.Errorf("a descriptor that is not open: %v", err)
	}
	const secret = "header.claims.signature"
	fd, w := credentialPipe(t)
	go w.WriteString(secret + strings.Repeat("s", maxCredentialFD) + "\n")
	if _, err := readCredentialFD(fd); err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sss") {
		t.Errorf("a line too long: %v", err)
	}
}
