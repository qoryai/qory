package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// TestTheRunCredentialComesFromOneSourceInOrder is the run credential's sources:
// --run-credential-fd wins over QORY_RUN_CREDENTIAL_SECRET, which wins over
// session.gateway.run_credential_file, resolved under forager.yaml's directory; an empty
// descriptor and none of them is no run credential.
func TestTheRunCredentialComesFromOneSourceInOrder(t *testing.T) {
	t.Cleanup(func() { config.SetServerVariables(config.ServerVariables{}) })
	r := &config.Forager{File: "/etc/qory/forager.yaml", SessionGateway: &config.ForagerSessionGateway{URL: "https://gateway.example", RunCredentialFile: "run-credential"}}
	fd, empty := "from-the-descriptor", ""
	config.SetServerVariables(config.ServerVariables{RunCredentialSecret: " from-the-variable\n"})
	if c := credentialSource(r, &fd); c == nil || c.once != fd || c.file != "" {
		t.Errorf("the descriptor and the rest: %+v", c)
	}
	if c := credentialSource(r, &empty); c != nil {
		t.Errorf("an empty descriptor: %+v", c)
	}
	if c := credentialSource(r, nil); c == nil || c.once != "from-the-variable" || c.file != "" {
		t.Errorf("the variable and the file: %+v", c)
	}
	config.SetServerVariables(config.ServerVariables{})
	if c := credentialSource(r, nil); c == nil || c.once != "" || c.file != "/etc/qory/run-credential" {
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
	if err := (&runCredential{once: v}).checkMode(); err != nil {
		t.Errorf("a credential read once: %v", err)
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
// exp, in the words of where it came from: its file, or a source read once. A credential
// whose exp qory cannot read leaves it unsaid.
func TestTheRunCredentialsExpiryIsWordedBySource(t *testing.T) {
	exp := time.Date(2026, 10, 9, 12, 30, 0, 0, time.UTC)
	cred := unsignedCredential(`{"sub":"run-1","exp":` + strconv.FormatInt(exp.Unix(), 10) + `}`)
	file := filepath.Join(t.TempDir(), "run-credential")
	if err := os.WriteFile(file, []byte(cred), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile := &runCredential{file: file}
	once := &runCredential{once: cred}
	for _, c := range []*runCredential{fromFile, once} {
		if _, err := c.get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := fromFile.expired(), "the run credential expired at 2026-10-09T12:30:00Z, and its file holds no fresh one"; got != want {
		t.Errorf("from its file: %q, want %q", got, want)
	}
	if got, want := once.expired(), "the run credential expired at 2026-10-09T12:30:00Z; --run-credential-fd and QORY_RUN_CREDENTIAL_SECRET are read once, so a run longer than its credential needs session.gateway.run_credential_file"; got != want {
		t.Errorf("read once: %q, want %q", got, want)
	}
	for _, v := range []string{"not-a-jwt", unsignedCredential(`{"sub":"run-1"}`), unsignedCredential(`{"exp":"soon"}`)} {
		c := &runCredential{once: v}
		c.get(context.Background())
		if got := c.expired(); got != "" {
			t.Errorf("%s: %q, want nothing", v, got)
		}
	}
}

// TestASeparateGatewaysEndIsSaid is a run a separate gateway ended: at the run
// credential's expiry, a failure in the words of its source; at its issuer's end, the
// line of decision 131; any other close is the caller's to say.
func TestASeparateGatewaysEndIsSaid(t *testing.T) {
	c := &runCredential{once: unsignedCredential(`{"exp":1791549000}`)}
	c.get(context.Background())
	var out bytes.Buffer
	if !gatewayEnded(ui.New(&out), &out, event.ReasonRunEndedAtIssuer, c) || out.String() != "qory run: the gateway ended the run: the run credential's issuer reports that the run has ended\n" {
		t.Errorf("the issuer's end: %q", out.String())
	}
	out.Reset()
	if !gatewayEnded(ui.New(&out), &out, event.ReasonCredentialExpired, c) || !strings.HasSuffix(out.String(), " the run credential expired at "+time.Unix(1791549000, 0).UTC().Format(time.RFC3339)+"; --run-credential-fd and QORY_RUN_CREDENTIAL_SECRET are read once, so a run longer than its credential needs session.gateway.run_credential_file\n") {
		t.Errorf("the expiry: %q", out.String())
	}
	out.Reset()
	for _, reason := range []string{event.ReasonSessionLost, event.ReasonBatchRefused} {
		if gatewayEnded(ui.New(&out), &out, reason, c) || out.Len() != 0 {
			t.Errorf("%s: said %q", reason, out.String())
		}
	}
	if gatewayEnded(ui.New(&out), &out, event.ReasonCredentialExpired, &runCredential{once: "no-exp"}) || out.Len() != 0 {
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
