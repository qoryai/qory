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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/sink"
	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/ui"
)

// runCredentialFDFlag is the flag that names the descriptor the run credential is read
// from, on a machine whose runs go through a gateway.
const runCredentialFDFlag = "run-credential-fd"

// runCredentialFDUsage is the help of --run-credential-fd.
const runCredentialFDUsage = "read the run credential from this open file descriptor, for a machine whose runs go through a gateway (" + config.ForagerFileName + ": session.gateway.run_credential_file)"

// maxCredentialFD is the longest line read from the run credential's descriptor, its
// line end, \n or \r\n, aside.
const maxCredentialFD = 64 << 10

// credentialStream is the run credential --run-credential-fd streams: whoever starts
// qory run keeps the descriptor open and writes each fresh run credential to it as a
// new line, and the latest complete line read is the credential.
type credentialStream struct {
	// f is the descriptor while it is read; nil when it ended before the run started.
	f *os.File
	// done is closed when the descriptor is no longer read, and its O_NONBLOCK is as
	// qory found it.
	done chan struct{}
	// restore puts the descriptor's O_NONBLOCK back as qory found it, once the
	// descriptor is closed and no longer read.
	restore func()

	mu sync.Mutex
	// v is the latest complete line.
	v string
}

// latest is the latest complete line the descriptor gave.
func (s *credentialStream) latest() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v
}

// stop closes the descriptor and waits for its reader to end, which puts the
// descriptor's O_NONBLOCK back as qory found it. The run credential read last stays.
func (s *credentialStream) stop() {
	if s.f != nil {
		s.f.Close()
	}
	<-s.done
}

// readCredentialFD reads the run credential from file descriptor n: its first line that
// is not empty, read before qory starts anything, or, when the writer closes it before
// any line end, all it wrote, so one credential written without a newline still works.
// The descriptor is close-on-exec from the start, so nothing qory starts inherits it,
// and is then read until the run ends ([credentialStream.stop]): each further complete
// line replaces the credential, an empty one is skipped, and a line cut short by the
// end of the descriptor is dropped. The standard input, output and error are refused,
// and so is a first line longer than a run credential; a later one is skipped. No error
// contains the value. Surrounding white space is not part of the credential.
//
// The descriptor is read non-blocking. O_NONBLOCK belongs to the open file description,
// which whoever started qory shares, a terminal's included, so qory puts the flag back
// as it found it once the descriptor is no longer read: when it ends, when qory refuses
// what it gave, and at [credentialStream.stop].
func readCredentialFD(n int) (*credentialStream, error) {
	if n < 3 {
		return nil, input(fmt.Errorf("--%s %d: the standard input, output and error carry no run credential; name a descriptor of 3 or above", runCredentialFDFlag, n))
	}
	// An inherited descriptor may lack close-on-exec, and os/exec closes nothing else in
	// the programs it starts. Non-blocking, the descriptor is the poller's, so closing it
	// ends a read that waits on it. A descriptor that is not open fails here.
	syscall.CloseOnExec(n)
	restore, err := setNonblock(n)
	if err != nil {
		return nil, input(fmt.Errorf("--%s %d: %w", runCredentialFDFlag, n, err))
	}
	f := os.NewFile(uintptr(n), "--"+runCredentialFDFlag)
	s := &credentialStream{done: make(chan struct{}), restore: restore}
	l := newCredentialLines(f)
	line, long, err := l.next()
	if err == io.EOF {
		line, long = l.rest()
	}
	switch {
	case long:
		err = input(fmt.Errorf("--%s %d: more than a run credential", runCredentialFDFlag, n))
	case err == io.EOF:
		err = nil
	case err != nil:
		err = input(fmt.Errorf("--%s %d: %w", runCredentialFDFlag, n, err))
	default:
		s.v = string(line)
		clear(line)
		s.f = f
		go s.follow(l)
		return s, nil
	}
	f.Close()
	l.clear()
	restore()
	close(s.done)
	if err != nil {
		return nil, err
	}
	s.v = string(line)
	clear(line)
	return s, nil
}

// follow reads the descriptor's lines after the first until it ends or is closed: each
// complete one that is not empty and not longer than a run credential replaces it.
func (s *credentialStream) follow(l *credentialLines) {
	// In this order: the descriptor is closed, which ends a read that waits on it, and
	// its flag is put back once nothing reads it, so no read waits on it blocking.
	defer close(s.done)
	defer s.restore()
	defer l.clear()
	defer s.f.Close()
	for {
		line, long, err := l.next()
		if err != nil {
			return
		}
		if long {
			continue
		}
		s.mu.Lock()
		s.v = string(line)
		s.mu.Unlock()
		clear(line)
	}
}

// setNonblock sets O_NONBLOCK on descriptor n, and returns what puts the flag back as
// it was. Set already, the flag is left, and restore does nothing. Otherwise restore
// clears it through a duplicate of n, made close-on-exec here, which shares n's open file
// description and outlives n: qory closes n to end a read that waits on it, and only
// then clears the flag, so that no read waits on the descriptor blocking. restore closes
// the duplicate, and is called once. Nothing is changed when err is not nil.
func setNonblock(n int) (restore func(), err error) {
	flags, err := unix.FcntlInt(uintptr(n), unix.F_GETFL, 0)
	if err != nil {
		return nil, err
	}
	if flags&unix.O_NONBLOCK != 0 {
		return func() {}, nil
	}
	keep, err := unix.FcntlInt(uintptr(n), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(n, true); err != nil {
		unix.Close(keep)
		return nil, err
	}
	return func() {
		unix.SetNonblock(keep, false)
		unix.Close(keep)
	}, nil
}

// credentialLines splits what a reader gives into lines, keeping no more of a line
// than a run credential: a longer one is dropped as it comes, up to its line end, and
// reported once.
type credentialLines struct {
	r     io.Reader
	chunk []byte
	// buf is what was read after the last line end.
	buf []byte
	// long says buf's line passed maxCredentialFD, was reported, and its start was
	// dropped.
	long bool
	// err is the reader's error, returned once buf holds no line end.
	err error
}

func newCredentialLines(r io.Reader) *credentialLines {
	const chunk = 4 << 10
	// buf never grows past its capacity, so clear reaches every byte it held.
	return &credentialLines{r: r, chunk: make([]byte, chunk), buf: make([]byte, 0, maxCredentialFD+2+chunk)}
}

// next is the next complete line that is not empty, trimmed of its line end and
// surrounding white space, or, with long, that a line longer than a run credential is
// being dropped, as soon as it is known, before its line end. err is the reader's error,
// io.EOF at its end, once no complete line is left. The caller clears the line.
func (l *credentialLines) next() (line []byte, long bool, err error) {
	for {
		if i := bytes.IndexByte(l.buf, '\n'); i >= 0 {
			raw := bytes.TrimSuffix(l.buf[:i], []byte("\r"))
			v := bytes.TrimSpace(raw)
			// A line already reported as too long ends here, and is gone.
			dropped := l.long
			long := !dropped && len(raw) > maxCredentialFD
			var out []byte
			if !dropped && !long && len(v) > 0 {
				out = bytes.Clone(v)
			}
			rest := copy(l.buf, l.buf[i+1:])
			clear(l.buf[rest:])
			l.buf, l.long = l.buf[:rest], false
			switch {
			case long:
				return nil, true, nil
			case out != nil:
				return out, false, nil
			}
			continue
		}
		// More than a run credential and a \r with no line end yet: drop it.
		if len(l.buf) > maxCredentialFD+1 {
			clear(l.buf)
			l.buf = l.buf[:0]
			if !l.long {
				l.long = true
				return nil, true, nil
			}
		}
		if l.err != nil {
			return nil, false, l.err
		}
		n, err := l.r.Read(l.chunk)
		l.buf = append(l.buf, l.chunk[:n]...)
		clear(l.chunk[:n])
		l.err = err
	}
}

// rest is what came after the last line end, trimmed, once the reader has ended; long
// says it is longer than a run credential, and then it is nil. The caller clears it.
func (l *credentialLines) rest() (line []byte, long bool) {
	v := bytes.TrimSpace(l.buf)
	if l.long || len(bytes.TrimSuffix(l.buf, []byte("\r"))) > maxCredentialFD {
		return nil, true
	}
	return bytes.Clone(v), false
}

// clear wipes what was read and not handed out.
func (l *credentialLines) clear() {
	clear(l.buf[:cap(l.buf)])
	l.buf = l.buf[:0]
}

// runCredential is where a run behind a separate gateway takes its run credential from:
// the latest line --run-credential-fd gave, QORY_RUN_CREDENTIAL_SECRET, read once, or the
// file session.gateway.run_credential_file names, read again before each request. It
// keeps the expiry of the last run credential it handed out, to word the run's end at
// it.
type runCredential struct {
	// fd is the descriptor's stream; nil unless the credential comes from it.
	fd *credentialStream
	// file is the file's path, resolved; empty unless the credential comes from it.
	file string
	// variable is the credential QORY_RUN_CREDENTIAL_SECRET held; empty unless it comes
	// from it.
	variable string

	mu  sync.Mutex
	exp time.Time
}

// credentialSource is the run credential of a machine whose runs go through the gateway
// r's session.gateway names: the stream of --run-credential-fd when fd is not nil, else
// QORY_RUN_CREDENTIAL_SECRET, as qory took it when it started, else the file
// session.gateway.run_credential_file names. nil is none, a descriptor that gave no
// credential included.
func credentialSource(r *config.Forager, fd *credentialStream) *runCredential {
	if fd != nil {
		if fd.latest() == "" {
			return nil
		}
		return &runCredential{fd: fd}
	}
	if v := strings.TrimSpace(config.TakenServerVariables().RunCredentialSecret); v != "" {
		return &runCredential{variable: v}
	}
	if f := r.SessionGateway.RunCredentialFile; f != "" {
		return &runCredential{file: r.Path(f)}
	}
	return nil
}

// get is the run credential now: the latest line the descriptor gave, the file's
// content, read again, or the variable's. The file is refused, unread, when its mode
// grants the group or others read or write ([credentialFileMode]). Its error never holds
// the credential.
func (c *runCredential) get(context.Context) (string, error) {
	v := c.variable
	switch {
	case c.fd != nil:
		v = c.fd.latest()
	case c.file != "":
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

// checkFile refuses, before anything starts, a run credential file that is the source in
// use and that qory cannot stat, as one that is not there, a directory, or a regular file
// qory cannot open, worded as session.gateway.ca_file's refusals are, after forager, the
// file that names it; and one whose mode grants the group or others read or write
// ([credentialFileMode]). A named pipe or another special file is not opened, since
// opening it may wait for its writer: the read before the first request is the first to
// open it. It goes by the file a link leads to, which is the one read, and reads nothing
// of it. A run credential from the descriptor or the variable has no file, and the file
// is then not checked. The read before each request is not changed.
func (c *runCredential) checkFile(forager string) error {
	if c.file == "" {
		return nil
	}
	refuse := func(err error) error {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return fmt.Errorf("%s: session.gateway.run_credential_file %s: %w", forager, c.file, err)
	}
	info, err := os.Stat(c.file)
	if err != nil {
		return refuse(err)
	}
	if info.IsDir() {
		// As session.gateway.ca_file's read of a directory words it.
		return refuse(syscall.EISDIR)
	}
	if err := credentialFileMode(c.file, info); err != nil {
		return err
	}
	// Opening a named pipe would wait for its writer: only a regular file is opened.
	if info.Mode().IsRegular() {
		f, err := os.Open(c.file)
		if err != nil {
			return refuse(err)
		}
		f.Close()
	}
	return nil
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
	at := expiryTime(exp)
	switch {
	case c.fd != nil:
		return fmt.Sprintf("the run credential expired at %s, and the descriptor gave no fresh one", at)
	case c.file != "":
		return fmt.Sprintf("the run credential expired at %s, and its file holds no fresh one", at)
	}
	return fmt.Sprintf("the run credential expired at %s; %s is read once, so a run longer than its credential needs session.gateway.run_credential_file or --%s", at, config.EnvRunCredential, runCredentialFDFlag)
}

// expiredAt is the expiry of the last run credential c handed out, as qory's texts say
// it, when it has passed at now; ok is false when it has not, or when no credential qory
// handed out had an exp it could read.
func (c *runCredential) expiredAt(now time.Time) (at string, ok bool) {
	c.mu.Lock()
	exp := c.exp
	c.mu.Unlock()
	if exp.IsZero() || exp.After(now) {
		return "", false
	}
	return expiryTime(exp), true
}

// expiryTime is a run credential's exp as qory's texts say it.
func expiryTime(exp time.Time) string {
	return exp.UTC().Format(time.RFC3339)
}

// gatewayNotOpened is the line qory says of a run a separate gateway could not open
// because its run credential could not be checked: the introspection endpoint could
// not be reached, the gateway's 503 credential_check_unreachable, or gave no valid
// answer, its 502 credential_check_invalid. "" for any other error.
func gatewayNotOpened(err error) string {
	var ref *session.Refusal
	if !errors.As(err, &ref) || ref.From != accesskey.FromGateway {
		return ""
	}
	switch {
	case ref.Code == event.ReasonCredentialCheckUnreachable && ref.Status == http.StatusServiceUnavailable:
		return "qory run: the run did not start: its run credential could not be checked; try again"
	case ref.Code == event.ReasonCredentialCheckInvalid && ref.Status == http.StatusBadGateway:
		return "qory run: the run did not start: its run credential could not be checked"
	}
	return ""
}

// behindGateway is a run's check, before anything starts, on a machine whose runs go
// through the gateway r's session.gateway names, and the run credential it then sends:
// such a machine holds no access key, in a file, a variable or a descriptor; --local and
// --label are for a gateway of the run's own; the CA file must hold a certificate; and
// the run needs a run credential, whose file, when it comes from one, is there, is not a
// directory, opens when it is a regular file, and grants the group and others no read or
// write. keyFD says
// --access-key-secret-fd was given, and credentialFD is the stream of
// --run-credential-fd, nil when it was not given.
func behindGateway(r *config.Forager, local, labels, keyFD bool, credentialFD *credentialStream) (*runCredential, error) {
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
	if err := c.checkFile(r.File); err != nil {
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

// resendThroughGateway sends the gateway r's session.gateway names what the session of
// the run runID did not deliver to it, from dir, the run's directory, through the
// gateway the run spoke to, [remoteGateway], with the run credential c, within wait of
// sig. It says what came of it on w: what the gateway accepted, what it did not, a run
// it has ended, a run it never opened, whose record has nothing to send and stays, exit
// 0, and its refusals of the run credential, each worded for the person
// ([gatewayRefusedResend], [gatewayEndedResend]). A record its session still holds, a
// run that is not recorded, and a record a gateway of the run's own made, which goes to
// the server, are refused as input. root is the checkout's, against which dir is shown.
func resendThroughGateway(sig context.Context, wait time.Duration, w io.Writer, r *config.Forager, c *runCredential, runID, dir, root string, report func(string)) error {
	ctx, cancel := context.WithTimeout(sig, wait)
	defer cancel()
	lines := resendLines(report)
	res, err := session.Resend(ctx, session.ResendSpec{
		Gateway:        remoteGateway(r, c),
		Dir:            dir,
		ForagerVersion: build().title(),
		Report:         lines.line,
	})
	lines.done(func(kind int) bool {
		return kind == heldNotOpened && err == nil && res.NotOpened && !res.RunClosed && res.Undelivered == 0
	})
	shown := ui.Short(dir, root)
	var pe *fs.PathError
	switch {
	case errors.Is(err, session.ErrRunning):
		return input(fmt.Errorf("the run %s is running", runID))
	case errors.As(err, &pe) && errors.Is(err, fs.ErrNotExist) && pe.Path == filepath.Join(dir, sink.SessionFile):
		return input(fmt.Errorf("no run %s is recorded in this checkout", runID))
	case errors.As(err, &pe) && errors.Is(err, fs.ErrExist) && pe.Path == filepath.Join(dir, sink.EventsFile):
		return input(&saidError{text: fmt.Sprintf("the run %s ran with a gateway of its own on this machine, so its record goes to the server, not through session.gateway: resend it with a %s that defines the server and no session.gateway", runID, config.ForagerFileName), err: err})
	case err != nil:
		if refused := gatewayRefusedResend(err, c, dir, shown, time.Now()); refused != nil {
			return refused
		}
		return explain(err, nil)
	}
	reaped := reapWall(sig, r, runID, report)
	u := ui.New(w)
	if reaped > 0 {
		u.Success("removed %d containers and networks the run left", reaped)
	}
	switch {
	case res.RunClosed:
		u.Fail(gatewayEndedResend(res, shown))
		return reported(&exitError{code: 1})
	case res.Undelivered > 0:
		u.Fail(fmt.Errorf("%d events were accepted and %d were not; %s/undelivered contains them", res.Sent, res.Undelivered, shown))
		return reported(&exitError{code: 1})
	case res.NotOpened:
		// A run refused at its run request, say: nothing failed now, and nothing would
		// come of sending again, so the resend succeeds, as one with nothing left does.
		u.Success("the gateway never opened run %s, so there is nothing to send; its record stays in %s", runID, shown)
		return nil
	}
	u.Success("%d events were accepted; nothing is left to send to the gateway", res.Sent)
	return nil
}

// gatewayRefusedResend words the gateway's refusals of a resend's run credential for
// the person: the 401 run_credential_refused, said as the run directory's missing
// run-secret when dir has none, since the gateway answers a batch without the run's
// secret so too, as the credential's expiry when its exp has passed at now, and
// otherwise as qory run says it; and the 403 of a run credential that differs from the
// one the run started with, target_differs_from_credential or differs_from_credential.
// dir is the run directory and shown the same as the person reads it, where the events
// stay. nil for any other error. It unwraps to the refusal.
func gatewayRefusedResend(err error, c *runCredential, dir, shown string, now time.Time) error {
	var ref *session.Refusal
	if !errors.As(err, &ref) {
		return nil
	}
	switch ref.Code {
	case refusal.RunCredentialRefused:
		// Only whether the file is there: the run's secret is never read here.
		if _, serr := os.Stat(filepath.Join(dir, runSecretFile)); errors.Is(serr, fs.ErrNotExist) {
			return &saidError{text: fmt.Sprintf("this run's record has no run-secret file, which the gateway needs to accept its events; they stay in %s", shown), err: err}
		}
		if at, ok := c.expiredAt(now); ok {
			return &saidError{text: fmt.Sprintf("the run credential expired at %s, so no more of this run's events are taken; they stay in %s", at, shown), err: err}
		}
		return &saidError{text: "the gateway refused this run credential", err: err}
	case refusal.TargetDiffersFromCredential, refusal.DiffersFromCredential:
		return &saidError{text: "the gateway refused this run credential: it differs from the one the run started with", err: err}
	}
	return nil
}

// runSecretFile is the file of a run directory behind a separate gateway that keeps the
// run's secret, which every batch of a resend carries; Forager writes and removes it.
const runSecretFile = "run-secret"

// gatewayEndedResend says that the run had ended when its events were sent again, and
// how it ended, by the state and the reason of its end ([outcomeOf]). The events stay in
// the run directory, shown. A run whose end nothing says, a 410 run_closed of a record
// with no exit, is said by its code.
func gatewayEndedResend(res session.ResendResult, shown string) error {
	o := outcomeOf(res.State, res.Reason)
	if !o.known {
		return fmt.Errorf("the gateway ended the run with the reason %s, so it takes no more of this run's events; they stay in %s", res.ClosedReason, shown)
	}
	ended := o.word()
	if o.reason != "" && !o.noOutcome {
		ended += ": " + o.reason
	}
	return fmt.Errorf("the run has ended (%s), so no more of its events are taken; they stay in %s", ended, shown)
}
