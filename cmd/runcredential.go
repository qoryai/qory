package cmd

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/ui"
)

// runCredentialFDFlag is the flag that names the descriptor the run credential is read
// from, on a machine whose runs go through a gateway.
const runCredentialFDFlag = "run-credential-fd"

// runCredentialFDUsage is the help of --run-credential-fd.
const runCredentialFDUsage = "read the run credential from this open file descriptor, for a machine whose runs go through a gateway (" + config.ForagerFileName + ": session.gateway.run_credential_file)"

// maxCredentialFD is the most read from the run credential's descriptor.
const maxCredentialFD = 64 << 10

// readCredentialFD reads the run credential from file descriptor n, to its end, and
// closes the descriptor, so nothing qory starts inherits it. The standard input, output
// and error are refused. No error contains the value. Surrounding white space is not
// part of the credential.
func readCredentialFD(n int) (string, error) {
	if n < 3 {
		return "", input(fmt.Errorf("--%s %d: the standard input, output and error carry no run credential; name a descriptor of 3 or above", runCredentialFDFlag, n))
	}
	// A descriptor that is not open shows as the read's error.
	f := os.NewFile(uintptr(n), "--"+runCredentialFDFlag)
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCredentialFD+1))
	if err != nil {
		return "", input(fmt.Errorf("--%s %d: %w", runCredentialFDFlag, n, err))
	}
	defer clear(b)
	if len(b) > maxCredentialFD {
		return "", input(fmt.Errorf("--%s %d: more than a run credential", runCredentialFDFlag, n))
	}
	return strings.TrimSpace(string(b)), nil
}

// runCredential is where a run behind a separate gateway takes its run credential from:
// once, from --run-credential-fd or QORY_RUN_CREDENTIAL_SECRET, or from the file
// session.gateway.run_credential_file names, read again before each request. It keeps
// the expiry of the last run credential it handed out, to word the run's end at it.
type runCredential struct {
	// file is the file's path, resolved; empty when the credential was read once.
	file string
	// once is the credential read once; empty when it comes from file.
	once string

	mu  sync.Mutex
	exp time.Time
}

// credentialSource is the run credential of a machine whose runs go through the gateway
// r's session.gateway names: the one read from --run-credential-fd when fd is not nil,
// else QORY_RUN_CREDENTIAL_SECRET, as qory took it when it started, else the file
// session.gateway.run_credential_file names. nil is none.
func credentialSource(r *config.Forager, fd *string) *runCredential {
	if fd != nil {
		if *fd == "" {
			return nil
		}
		return &runCredential{once: *fd}
	}
	if v := strings.TrimSpace(config.TakenServerVariables().RunCredentialSecret); v != "" {
		return &runCredential{once: v}
	}
	if f := r.SessionGateway.RunCredentialFile; f != "" {
		return &runCredential{file: r.Path(f)}
	}
	return nil
}

// get is the run credential now: the file's content, read again, or the one read once.
// The file is refused, unread, when its mode grants the group or others read or write
// ([credentialFileMode]). Its error never holds the credential.
func (c *runCredential) get(context.Context) (string, error) {
	v := c.once
	if c.file != "" {
		b, err := readCredentialFile(c.file)
		if err != nil {
			return "", err
		}
		v = strings.TrimSpace(string(b))
		clear(b)
	}
	if exp, ok := credentialExpiry(v); ok {
		c.mu.Lock()
		c.exp = exp
		c.mu.Unlock()
	}
	return v, nil
}

// readCredentialFile reads the run credential's file at path, after
// [credentialFileMode] passes the mode of the file it opened: the file a link leads to,
// as the read follows it.
func readCredentialFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := credentialFileMode(path, info); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// checkMode refuses, before anything starts, a run credential file whose mode grants
// the group or others read or write ([credentialFileMode]), by the file a link leads to,
// which is the one read. A file qory cannot stat is left, as before, to the read before
// the first request; a run credential read once has no file.
func (c *runCredential) checkMode() error {
	if c.file == "" {
		return nil
	}
	info, err := os.Stat(c.file)
	if err != nil {
		return nil
	}
	return credentialFileMode(c.file, info)
}

// credentialFileMode refuses the run credential's file at path when info, its mode,
// grants the group or others read or write. Execute alone grants nothing to read, and
// the owner is not checked.
func credentialFileMode(path string, info fs.FileInfo) error {
	if perm := info.Mode().Perm(); perm&0o066 != 0 {
		return fmt.Errorf("%s is mode %04o, which grants access to the group or others: chmod 600 %s", path, perm, path)
	}
	return nil
}

// credentialExpiry is the exp claim of a run credential, a JWT, read without verifying
// it: qory uses it only to word the run's end at the credential's expiry, and trusts
// nothing else from it. ok is false when the credential has no exp qory can read.
func credentialExpiry(v string) (time.Time, bool) {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp *json.Number `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == nil {
		return time.Time{}, false
	}
	f, err := claims.Exp.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > float64(math.MaxInt32)*1000 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// expired says the run ended at the expiry of its run credential, by where the credential
// came from; empty when no credential qory handed out had an exp it could read.
func (c *runCredential) expired() string {
	c.mu.Lock()
	exp := c.exp
	c.mu.Unlock()
	if exp.IsZero() {
		return ""
	}
	at := exp.UTC().Format(time.RFC3339)
	if c.file != "" {
		return fmt.Sprintf("the run credential expired at %s, and its file holds no fresh one", at)
	}
	return fmt.Sprintf("the run credential expired at %s; --%s and %s are read once, so a run longer than its credential needs session.gateway.run_credential_file", at, runCredentialFDFlag, config.EnvRunCredential)
}

// gatewayEnded says why a separate gateway ended a run, by the code it closed it with:
// the run credential expired, credential_expired, or its issuer reports that the run has
// ended, run_ended_at_issuer. It reports whether it said anything: for any other code,
// and for an expiry qory cannot read, the caller says what it says of a gateway's close.
func gatewayEnded(u *ui.UI, report io.Writer, reason string, c *runCredential) bool {
	switch reason {
	case event.ReasonCredentialExpired:
		if text := c.expired(); text != "" {
			u.Fail(errors.New(text))
			return true
		}
	case event.ReasonRunEndedAtIssuer:
		fmt.Fprintln(report, "qory run: the gateway ended the run: the run credential's issuer reports that the run has ended")
		return true
	}
	return false
}

// behindGateway is a run's check, before anything starts, on a machine whose runs go
// through the gateway r's session.gateway names, and the run credential it then sends:
// such a machine holds no access key, in a file, a variable or a descriptor; --local and
// --label are for a gateway of the run's own; the CA file must hold a certificate; and
// the run needs a run credential, whose file, when it comes from one, grants the group
// and others no read or write. keyFD says --access-key-secret-fd was given, and
// credentialFD is the credential read from --run-credential-fd, nil when it was not
// given.
func behindGateway(r *config.Forager, local, labels, keyFD bool, credentialFD *string) (*runCredential, error) {
	const through = "this machine's runs go through the gateway session.gateway.url names"
	if local {
		return nil, input(errors.New("--local runs with a gateway of this run's own and no server, and this machine has no gateway section: its runs go through the gateway session.gateway.url names"))
	}
	if dir := machineDir(); dir != "" {
		if p := dir.Path(foragerdir.SecretFile); exists(p) {
			return nil, input(fmt.Errorf("%s exists, and %s: a machine behind a gateway holds no access key; remove the file", p, through))
		}
	}
	v := config.TakenServerVariables()
	for _, set := range []struct{ name, value string }{{accesskey.EnvID, v.AccessKeyID}, {accesskey.EnvSecret, v.AccessKeySecret}, {accesskey.EnvPin, v.ApiaryPublicKey}} {
		if set.value != "" {
			return nil, input(fmt.Errorf("%s is set, and %s: a machine behind a gateway holds no access key; unset it", set.name, through))
		}
	}
	if keyFD {
		return nil, input(fmt.Errorf("--%s is set, and %s: a machine behind a gateway holds no access key; unset it", secretFDFlag, through))
	}
	if labels {
		return nil, input(errors.New("--label works only when this machine runs its own gateway; behind a gateway the run credential sets the labels"))
	}
	if ca := r.SessionGateway.CAFile; ca != "" {
		path := r.Path(ca)
		b, err := os.ReadFile(path)
		if err != nil {
			var pe *fs.PathError
			if errors.As(err, &pe) {
				err = pe.Err
			}
			return nil, input(fmt.Errorf("%s: session.gateway.ca_file %s: %w", r.File, path, err))
		}
		if !x509.NewCertPool().AppendCertsFromPEM(b) {
			return nil, input(fmt.Errorf("%s: session.gateway.ca_file %s holds no PEM certificate", r.File, path))
		}
	}
	c := credentialSource(r, credentialFD)
	if c == nil {
		return nil, input(fmt.Errorf("%s, and there is no run credential: set session.gateway.run_credential_file, --%s or %s", through, runCredentialFDFlag, config.EnvRunCredential))
	}
	if err := c.checkMode(); err != nil {
		return nil, input(err)
	}
	return c, nil
}

// exists reports whether there is anything at path, a link included.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// remoteGateway is the gateway session.gateway names, as the session reaches it, with
// the run credential c hands out before each request.
func remoteGateway(r *config.Forager, c *runCredential) session.RemoteGateway {
	g := r.SessionGateway
	return session.RemoteGateway{URL: g.URL, CAFile: r.Path(g.CAFile), CertificateSHA256: g.CertificateSHA256, Credential: c.get}
}

// gatewayHost is the host of the gateway's URL, and its port when it names one, as the
// run's first line names the gateway.
func gatewayHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// credentialRefused words the gateway's refusals of a run behind it for the person: of
// its run credential, of a checkout whose forge or repository differs from the
// credential's, labels the checkout's, and of a key of about.details, which --details
// named detailsFlag set, that differs from the credential's. nil for any other error,
// and for a refusal of a label other than forge and repository, which qory never sends
// behind a gateway. It unwraps to the refusal.
func credentialRefused(err error, labels map[string]string, detailsFlag string, about *session.About) error {
	var ref *session.Refusal
	if !errors.As(err, &ref) {
		return nil
	}
	switch ref.Code {
	case refusal.RunCredentialRefused:
		return &saidError{text: "the gateway refused this run credential", err: err}
	case refusal.TargetDiffersFromCredential:
		forge, repository := labels["forge"], labels["repository"]
		for _, n := range ref.Names {
			switch member, value, _ := strings.Cut(n, "="); member {
			case "labels.forge":
				forge = value
			case "labels.repository":
				repository = value
			}
		}
		return &saidError{text: fmt.Sprintf("this checkout is %s/%s, and the run credential is for %s/%s", labels["forge"], labels["repository"], forge, repository), err: err}
	case refusal.DiffersFromCredential:
		var sent map[string]json.RawMessage
		if about == nil || json.Unmarshal(about.Details, &sent) != nil {
			return nil
		}
		var said []string
		for _, n := range ref.Names {
			member, value, _ := strings.Cut(n, "=")
			key, ok := strings.CutPrefix(member, "about.details.")
			v, has := sent[key]
			if !ok || !has {
				return nil
			}
			var b bytes.Buffer
			if json.Compact(&b, v) != nil {
				return nil
			}
			said = append(said, fmt.Sprintf("--details %s sets %s to %s, and the run credential says %q: the credential decides; remove the key", detailsFlag, key, b.String(), value))
		}
		if len(said) == 0 {
			return nil
		}
		return &saidError{text: strings.Join(said, "; "), err: err}
	}
	return nil
}
