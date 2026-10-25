package cmd

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/runkey"
	"github.com/qoryai/forager/server"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
)

// A machine that a provisioner creates for one run gets a run code, qecr_, and the
// gateway's URL in its start data in place of the run key, since a cloud may keep start
// data readable for the machine's life. qory run --run-code-file reads the code, makes
// a key pair in memory, redeems the code through --gateway's relay for the run's run
// key, and runs with that key, as a run given --run-key-fd does. The key pair and the
// run key stay in memory: qory writes neither to a file, a log or the record. qory run
// writes the code to none of them either. qory run pool writes the code to a file in the
// pool's folder, mode 0600, opens it for reading and unlinks it before it starts qory run
// with the open file as descriptor 3, so no file name leads to the code while qory run
// reads it. No accesskey.RunAnswer is kept past the redemption: fmt and slog print one in
// a struct field that is not exported in full, run key included.

// The flags of a run code.
const (
	runCodeFileFlag = "run-code-file"
	gatewayFlag     = "gateway"
)

// The help of --run-code-file and --gateway.
const (
	runCodeFileUsage = "read a run code (qecr_…) from <file>, redeem it through --gateway for the run's key and documents, and run the workflow they set; the key stays in memory"
	gatewayUsage     = "the gateway that --run-code-file redeems the code through; the run key must be for this gateway"
)

// maxRunCodeFile is the most read from the run code's file, plus one byte: a run code is
// a prefix, 26 characters and one or two fingerprints, well under this.
const maxRunCodeFile = 1 << 10

// runCodeTimeout bounds each send of the redemption, as Forager's enrolment client does.
const runCodeTimeout = 10 * time.Second

// The seams a test replaces: the gateway a run code is redeemed through, with its trust,
// the system's roots; and the pause before the one retry of a redemption whose answer
// was lost.
var (
	runCodeVia   = func(gatewayURL string) server.Via { return server.Via{URL: gatewayURL} }
	runCodePause = time.Second
)

// runCode is a run code read from the file of --run-code-file, normalised, and the URL
// that --gateway is set to. It is a secret: fmt prints a runCode by its gateway alone,
// through its String method; a struct that contains a runCode, printed with %+v or %s,
// prints the code, so no error or log line prints such a struct.
type runCode struct {
	code    string
	gateway string
	// stdin is set when the code was read from stdin, see [namesStdin].
	stdin bool
}

// String returns the run code's gateway, never its value.
func (c runCode) String() string { return "run code for the gateway " + gatewayHost(c.gateway) }

// GoString is c as %#v prints it.
func (c runCode) GoString() string { return c.String() }

// Format prints c as String does, under every verb and flag.
func (c runCode) Format(f fmt.State, _ rune) { io.WriteString(f, c.String()) }

// givenRunCode is the run code in file, the file of --run-code-file, nil when neither
// flag is set. Neither flag goes with --run-key-fd, given when keyFD is true, or
// QORY_RUN_KEY, and each needs the other. The file is read once, before anything is
// sent: its content is the code, with at most one newline after it. A file that is
// stdin is read from stdin, the reader qory has, see [readRunCode]. Every refusal is
// input, and none contains the code. Each descriptor of 3 or above that qory inherited
// open on the file, a regular file, is closed before givenRunCode returns, on every
// path, see [closeRunCodeFD]: the file is the one qory read, or, when a refusal of the
// flags comes first, the one that file leads to then.
func givenRunCode(fileSet, gatewaySet, keyFD bool, file, gatewayURL string, stdin io.Reader) (*runCode, error) {
	if err := runCodeFlags(fileSet, gatewaySet, keyFD, gatewayURL); err != nil || !fileSet {
		if fileSet {
			// Nothing is read, and stdin is open.
			closeRunCodeFD(runCodeFileInfo(file, stdin))
		}
		return nil, err
	}
	code, fromStdin, info, err := readRunCode(file, stdin)
	// The descriptors are closed while stdin is open, so that their list, read from
	// /dev/fd, does not take descriptor 0; stdin is closed after them.
	closeRunCodeFD(info)
	if c, ok := stdin.(io.Closer); ok && fromStdin {
		c.Close()
	}
	if err != nil {
		return nil, err
	}
	return &runCode{code: code, gateway: strings.TrimSuffix(gatewayURL, "/"), stdin: fromStdin}, nil
}

// runCodeFlags is the refusal of --run-code-file and --gateway, set when fileSet and
// gatewaySet are true, beside --run-key-fd, set when keyFD is true, and QORY_RUN_KEY;
// nil when they go together, or when neither is set. gatewayURL is --gateway's value.
func runCodeFlags(fileSet, gatewaySet, keyFD bool, gatewayURL string) error {
	envKey := config.TakenRunKey() != ""
	switch {
	case !fileSet && !gatewaySet:
		return nil
	case fileSet && keyFD:
		return input(fmt.Errorf("leave out --%s or --%s: a run takes its run key from one of them", runKeyFDFlag, runCodeFileFlag))
	case fileSet && envKey:
		return input(fmt.Errorf("unset %s, or leave out --%s: a run takes its run key from one of them", config.EnvRunKey, runCodeFileFlag))
	case keyFD:
		return input(fmt.Errorf("leave out --%s: it goes with --%s alone, and this run takes its run key from --%s", gatewayFlag, runCodeFileFlag, runKeyFDFlag))
	case !fileSet && envKey:
		return input(fmt.Errorf("leave out --%s: it goes with --%s alone, and this run takes its run key from %s", gatewayFlag, runCodeFileFlag, config.EnvRunKey))
	case !gatewaySet:
		return input(fmt.Errorf("add --%s <https URL>: --%s redeems the run code through that gateway", gatewayFlag, runCodeFileFlag))
	case !fileSet:
		return input(fmt.Errorf("add --%s <file>, or leave out --%s: --%s is the gateway that a run code is redeemed through", runCodeFileFlag, gatewayFlag, gatewayFlag))
	}
	if err := config.CheckViaURL("--"+gatewayFlag, gatewayURL); err != nil {
		return input(fmt.Errorf("set --%s to the gateway's https URL, a host and an optional port: %w", gatewayFlag, err))
	}
	return nil
}

// runCodeFileInfo is the file that file leads to, nil when there is none or qory cannot
// open it: stdin's file for a path that [namesStdin] matches, else the file opened by its
// path, without blocking, and closed.
func runCodeFileInfo(file string, stdin io.Reader) fs.FileInfo {
	if namesStdin(file) {
		return stdinInfo(stdin)
	}
	f, err := openRunCodeFile(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	return info
}

// readRunCode reads the run code from file, at most maxRunCodeFile bytes, and returns it
// normalised, whether it came from stdin, and the file it was read from, nil when qory
// could not open or stat it, or when stdin is not a file. It leaves stdin open. A file
// that is stdin is read as stdin, see [readRunCodeStdin]: a path that [namesStdin]
// matches, which qory does not open, or a file that, once opened, is the same file as
// stdin, by device and inode, which qory then closes unread. Any other file is opened
// without blocking and must be a regular file, so a named pipe or a device does not
// stop qory. One newline after the code is left out; anything else in the file, and a
// code that is not a run code, is refused as input, without the code. Each refusal
// prints the path once.
func readRunCode(file string, stdin io.Reader) (string, bool, fs.FileInfo, error) {
	if namesStdin(file) {
		info := stdinInfo(stdin)
		code, err := readRunCodeStdin(file, stdin)
		return code, true, info, err
	}
	f, err := openRunCodeFile(file)
	if err != nil {
		return "", false, nil, unreadRunCode(file, err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return "", false, nil, unreadRunCode(file, err)
	}
	if isStdin(info, stdin) {
		f.Close()
		code, err := readRunCodeStdin(file, stdin)
		return code, true, info, err
	}
	defer f.Close()
	if !info.Mode().IsRegular() {
		return "", false, info, input(fmt.Errorf("give --%s a regular file: %s is not one", runCodeFileFlag, exports.Value(file, false)))
	}
	code, err := runCodeFrom(f, file)
	return code, false, info, err
}

// isStdin reports whether info is the file that stdin is, as [os.SameFile] compares
// them, by device and inode. It is false when stdin is not a file, as in a test. The Go
// runtime opens /dev/null on a standard descriptor that is closed when qory starts, so
// while stdin is open, no file that qory opens has descriptor 0; [givenRunCode] closes
// stdin only once the code was read from it, after its last call of isStdin.
func isStdin(info fs.FileInfo, stdin io.Reader) bool {
	in := stdinInfo(stdin)
	return in != nil && os.SameFile(info, in)
}

// stdinInfo is the file that stdin is, nil when stdin is not a file, as in a test, or
// its stat fails.
func stdinInfo(stdin io.Reader) fs.FileInfo {
	s, ok := stdin.(interface{ Stat() (fs.FileInfo, error) })
	if !ok {
		return nil
	}
	info, err := s.Stat()
	if err != nil {
		return nil
	}
	return info
}

// readRunCodeStdin reads the run code from stdin, which file leads to, as --run-key-fd 0
// reads it; [givenRunCode] then closes stdin. Stdin may be a regular file, a pipe or a
// terminal, and is read from its current offset to its end, at most maxRunCodeFile
// bytes and one more; a stdin with more is refused, and the run stops. The refusals are
// [readRunCode]'s, with file as the path, but for the one of a file that is not regular.
func readRunCodeStdin(file string, stdin io.Reader) (string, error) {
	return runCodeFrom(stdin, file)
}

// unreadRunCode is the refusal of a --run-code-file, file, that qory could not open or
// read, for err.
func unreadRunCode(file string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return input(fmt.Errorf("give --%s a file that qory can read: %s: %w", runCodeFileFlag, exports.Value(file, false), err))
}

// runCodeFrom reads r, at most maxRunCodeFile bytes and one more, and returns the run
// code in it, normalised, see [readRunCode]. Each refusal prints file once.
func runCodeFrom(r io.Reader, file string) (string, error) {
	shown := exports.Value(file, false)
	b, err := io.ReadAll(io.LimitReader(r, maxRunCodeFile+1))
	defer clear(b)
	if err != nil {
		return "", unreadRunCode(file, err)
	}
	raw := bytes.TrimSuffix(b, []byte("\n"))
	switch {
	case len(raw) == 0:
		return "", input(fmt.Errorf("write the run code into %s: the file is empty", shown))
	case len(b) > maxRunCodeFile || bytes.ContainsAny(raw, " \t\r\n\v\f"):
		return "", input(fmt.Errorf("write only the run code into %s, with at most one newline after it: the file contains more", shown))
	}
	code, err := accesskey.NormaliseCode(string(raw))
	if err != nil {
		return "", input(fmt.Errorf("write a run code (qecr_…) into %s: the file does not contain an enrolment code of Qory Apiary", shown))
	}
	if kind, _ := accesskey.CodeKind(code); kind != "run" {
		return "", input(fmt.Errorf("write a run code (qecr_…) into %s: the file contains the enrolment code of an access key of kind %s", shown, kind))
	}
	return code, nil
}

// namesStdin reports whether file, made absolute and clean, is a path that leads to
// qory's descriptor 0, the standard input: /dev/stdin, or /dev/fd/0, /proc/self/fd/0,
// /proc/thread-self/fd/0 or /proc/<qory's process id>/fd/0, with one or more zeros for
// the 0. Linux has no path with more than one zero, such as /dev/fd/00, and macOS opens
// /dev/fd/00 as descriptor 0; qory reads such a path as stdin on both. qory reads stdin
// and does not open the path, so stdin is read where the path cannot be opened: on
// macOS, which has no /proc, and on Linux for a socket, or a file that qory may not
// open.
func namesStdin(file string) bool {
	p, err := filepath.Abs(file)
	if err != nil {
		return false
	}
	if p == "/dev/stdin" {
		return true
	}
	dir, n := path.Split(p)
	if n == "" || strings.Trim(n, "0") != "" {
		return false
	}
	switch dir {
	case "/dev/fd/", "/proc/self/fd/", "/proc/thread-self/fd/", "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/":
		return true
	}
	return false
}

// redeem makes a key pair in memory and redeems c with it, as name, through c's
// gateway, see [redeemRunCode].
func (c *runCode) redeem(ctx context.Context, name string) (*runKey, json.RawMessage, error) {
	k, err := generateKey()
	if err != nil {
		return nil, nil, err
	}
	return redeemRunCode(ctx, k, c, name, keyClock())
}

// redeemRunCode sends c's code with a proof under k, made at now, to the server's
// enrolment endpoint through c's gateway's relay, and returns the run key of the
// verified answer, parsed as --run-key-fd parses one, and the answer's documents, see
// [codeDocumentsOf]. The run key's gateway must be c's gateway, compared as [sameGateway]
// compares them. A send that fails in transport, and so may have lost an answer, is sent
// again once, the same request with the same key: the contract gives that key the same
// run key within the code's 15 minutes. k, the code and the run key stay in memory, and
// no error contains the code or the run key.
func redeemRunCode(ctx context.Context, k *accesskey.Key, c *runCode, name string, now time.Time) (*runKey, json.RawMessage, error) {
	if accesskey.CheckName(name) != nil {
		// The name is the access key's, and a run code makes no access key: a host name
		// that does not fit stops nothing.
		name = "qory-run"
	}
	req, err := accesskey.NewEnrolmentRequest(k, c.code, name, now)
	if err != nil {
		return nil, nil, hiddenRunSecrets(err)
	}
	t, err := runCodeVia(c.gateway).Transport()
	if err != nil {
		return nil, nil, input(fmt.Errorf("set --%s to a gateway that qory can reach: %w", gatewayFlag, hiddenRunSecrets(err)))
	}
	hc := &http.Client{Transport: t, Timeout: runCodeTimeout}
	// Forager's errors contain the endpoint under the server's URL. qory has the gateway's
	// URL alone, and the request goes to the gateway, so the errors contain the gateway's.
	ans, err := req.PostRunVia(ctx, hc, c.gateway, c.gateway, userAgent())
	retried := false
	if err != nil && ctx.Err() == nil && transportFailed(err) {
		retried = true
		select {
		case <-ctx.Done():
		case <-time.After(runCodePause):
			ans, err = req.PostRunVia(ctx, hc, c.gateway, c.gateway, userAgent())
		}
	}
	if err != nil {
		return nil, nil, redeemFailed(ctx, c, err, retried)
	}
	value := strings.TrimSpace(ans.RunKey)
	documents := runAnswerDocuments(ans)
	ans = nil
	key, err := runkey.Parse(value)
	if err != nil {
		return nil, nil, input(fmt.Errorf("ask for a new run code: the run key that the code was redeemed for does not read as a run key: %s", runKeyReason(err)))
	}
	if !sameGateway(key.GatewayURL, c.gateway) {
		return nil, nil, input(fmt.Errorf("ask for a new run code, and give qory run the gateway that the run key is for: the run key is for the gateway %s, and --%s is %s; the code is used", gatewayHost(key.GatewayURL), gatewayFlag, gatewayHost(c.gateway)))
	}
	return &runKey{value: value, key: key, redeemed: true}, documents, nil
}

// transportFailed reports whether err is a send that failed in transport: no answer
// came, or it did not come whole. A signed or unsigned answer that came is not.
func transportFailed(err error) bool {
	var ue *url.Error
	var ne net.Error
	return errors.As(err, &ue) || errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF)
}

// againWithin starts the fix of a redemption that may not have used the code: run qory
// again, and the code may be used already, if the answer was lost on its way back.
const againWithin = "run qory run again within the run code's 15 minutes, and ask for a new run code if the redemption then returns 401: "

// redeemFailed is the error of a redemption that did not return a verified run key,
// after ctx ended or not, and after one retry or none. Each line starts with its fix.
// What needs a new run code is input, exit 2: a 401, and a signed answer that used the
// code. The rest exit 1. None contains the code or the run key.
func redeemFailed(ctx context.Context, c *runCode, err error, retried bool) error {
	err = hiddenRunSecrets(err)
	var ref *accesskey.Refusal
	var kind *accesskey.KindError
	var cert *tls.CertificateVerificationError
	switch {
	case errors.As(err, &kind):
		// Forager's line starts with its fix: use another run code.
		return input(err)
	case errors.As(err, &ref):
		switch ref.Code {
		case accesskey.CodeUnauthorized:
			return input(&refusedError{text: "ask for a new run code: the redemption returned 401, so the code was used, has expired or was cancelled, or it was redeemed with another key first", err: &accesskey.Refusal{Code: ref.Code}})
		case accesskey.CodeAnswerUnsigned:
			return &refusedError{text: fmt.Sprintf("check that --%s is the run's gateway, then %sthe response to the redemption (HTTP %d) does not verify under the server key whose fingerprint is first in the code", gatewayFlag, againWithin, ref.Status), err: &accesskey.Refusal{Code: ref.Code}}
		case accesskey.CodeRateLimited:
			return &refusedError{text: "run qory run again later, within the run code's 15 minutes: Qory Apiary returned 429 to the redemption, too many attempts with this code", err: &accesskey.Refusal{Code: ref.Code}}
		}
		// A signed 409: Qory Apiary did not take the key pair qory made, and a run of qory
		// again makes another.
		return &refusedError{text: fmt.Sprintf("run qory run again within the run code's 15 minutes: Qory Apiary returned %d to the key pair that qory made for the redemption", ref.Status), err: &accesskey.Refusal{Code: ref.Code}}
	case strings.HasPrefix(err.Error(), "use another run code: "):
		// Forager's line of a signed answer that used the code.
		return input(err)
	case strings.HasPrefix(err.Error(), "update Forager "):
		return input(errors.New("update qory, then ask for a new run code: the response to the redemption is of a version that this qory does not read; the code is used"))
	case strings.Contains(err.Error(), "with a code this package does not read"):
		return fmt.Errorf("update qory, then run qory run again within the run code's 15 minutes: Qory Apiary returned a signed refusal whose code this qory does not read: %w", err)
	case ctx.Err() != nil:
		return fmt.Errorf("%sqory run stopped before the redemption completed: %w", againWithin, context.Cause(ctx))
	case retried && errors.As(err, &cert):
		// The gateway's certificate does not verify: an authority that is not trusted,
		// another host name, a time outside its validity or a key usage that does not
		// allow a server. A second send fails the same way.
		return fmt.Errorf("check that --%s is the run's gateway, then %sthe redemption through the gateway %s failed twice: %w", gatewayFlag, againWithin, gatewayHost(c.gateway), err)
	case retried:
		return fmt.Errorf("%sthe redemption through the gateway %s failed twice: %w", againWithin, gatewayHost(c.gateway), err)
	}
	return fmt.Errorf("%sthe redemption through the gateway %s did not complete: %w", againWithin, gatewayHost(c.gateway), err)
}

// sameGateway reports whether the gateway URLs a and b are one gateway, as Forager's
// link reaches a run key's gateway: https, the host in any letter case, and the port,
// 443 when the URL has none.
func sameGateway(a, b string) bool {
	host := func(raw string) (string, string, bool) {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return "", "", false
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		return strings.ToLower(u.Hostname()), port, true
	}
	ah, ap, aok := host(a)
	bh, bp, bok := host(b)
	return aok && bok && net.JoinHostPort(ah, ap) == net.JoinHostPort(bh, bp)
}

// hiddenRunSecrets is err, or, when its text contains what starts an access key
// secret, a run key, a run code or an enrolment code, qak_, qrk_, qecr_, qecn_, qecp_,
// qecg_ or qeci_ in any letter case, an error whose text is [accesskey.ShownError]'s
// phrase in its place. It unwraps to err.
func hiddenRunSecrets(err error) error {
	return accesskey.ShownError(err)
}
