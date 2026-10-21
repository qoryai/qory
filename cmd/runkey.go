package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/runkey"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes"
	"github.com/qoryai/forager/sink"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/ui"
)

// runKey is the run key qory was given, white space around it trimmed, and what it
// says, read as a runner reads it, unverified: the run, and the separate gateway the
// run goes through. fromFD says it came from the descriptor --run-key-fd names, not
// from QORY_RUN_KEY, which an unwalled agent can read on macOS, and on Linux from the
// program that started qory. It is a secret: it is handed to Forager's session alone,
// and fmt prints a runKey by its gateway and its run, never its value.
type runKey struct {
	value  string
	key    runkey.Key
	fromFD bool
}

// String names the run key by its run and its gateway, never its value.
func (k runKey) String() string {
	return "run key for the run " + k.key.RunID + " through " + gatewayHost(k.key.GatewayURL)
}

// GoString is k as %#v prints it.
func (k runKey) GoString() string { return k.String() }

// Format prints k as String does, under every verb and flag.
func (k runKey) Format(f fmt.State, _ rune) { io.WriteString(f, k.String()) }

// runKeyFDFlag is the flag that names the descriptor the run key is read from.
const runKeyFDFlag = "run-key-fd"

// runKeyFDUsage is the help of qory run's and qory run resend's --run-key-fd.
const runKeyFDUsage = "read the run key from this file descriptor, 0 for stdin, in place of " + config.EnvRunKey + ", which an unwalled agent may read"

// maxRunKeyFD is the most read from the run key's descriptor: a run key is at most
// runkey.MaxLength characters, and white space may surround it.
const maxRunKeyFD = 4 << 10

// givenRunKey is the run key the run was given, nil for none: read from the descriptor
// fd when set says --run-key-fd was given, stdin for 0, else the one QORY_RUN_KEY held
// when qory started. The descriptor is read once, to its end, and closed, whatever comes
// next, so nothing qory starts inherits it; both ways at once are refused. A value that
// does not read as a run key is refused, naming the part that is wrong and never the
// value. Every refusal is input.
func givenRunKey(set bool, fd int, stdin io.Reader) (*runKey, error) {
	if !set {
		return takenRunKey()
	}
	raw, err := readRunKeyFD(fd, stdin)
	defer clear(raw)
	if err != nil {
		return nil, err
	}
	if config.TakenRunKey() != "" {
		return nil, input(fmt.Errorf("--%s and %s both give a run key; give it one way", runKeyFDFlag, config.EnvRunKey))
	}
	v := strings.TrimSpace(string(raw))
	k, err := runkey.Parse(v)
	if err != nil {
		return nil, input(fmt.Errorf("--%s %d gives no run key: %s", runKeyFDFlag, fd, runKeyReason(err)))
	}
	return &runKey{value: v, key: k, fromFD: true}, nil
}

// readRunKeyFD reads what the descriptor n holds, to its end, at most maxRunKeyFD bytes,
// and closes it: stdin for 0. The standard output and error are refused.
func readRunKeyFD(n int, stdin io.Reader) ([]byte, error) {
	if n == 1 || n == 2 {
		return nil, input(fmt.Errorf("--%s %d: the standard output and error carry no run key; name 0 for stdin, or a descriptor of 3 or above", runKeyFDFlag, n))
	}
	var r io.Reader = stdin
	if n != 0 {
		f := os.NewFile(uintptr(n), "--"+runKeyFDFlag)
		if f == nil || n < 0 {
			return nil, input(fmt.Errorf("--%s %d: %w", runKeyFDFlag, n, syscall.EBADF))
		}
		r = f
	}
	if c, ok := r.(io.Closer); ok {
		defer c.Close()
	}
	b, err := io.ReadAll(io.LimitReader(r, maxRunKeyFD+1))
	if err != nil {
		clear(b)
		return nil, input(fmt.Errorf("--%s %d: %w", runKeyFDFlag, n, err))
	}
	if len(b) > maxRunKeyFD {
		clear(b)
		return nil, input(fmt.Errorf("--%s %d: more than a run key", runKeyFDFlag, n))
	}
	return b, nil
}

// takenRunKey is the run key QORY_RUN_KEY held when qory started, nil when it was unset
// or empty. A value that does not read as a run key is [runKeyNotAKey]'s refusal, which
// names the part that is wrong and never the value.
func takenRunKey() (*runKey, error) {
	raw := config.TakenRunKey()
	if raw == "" {
		return nil, nil
	}
	v := strings.TrimSpace(raw)
	k, err := runkey.Parse(v)
	if err != nil {
		return nil, runKeyNotAKey(err)
	}
	return &runKey{value: v, key: k}, nil
}

// runKeyNotAKey is the refusal of a QORY_RUN_KEY that is not a run key, with the part
// that is wrong as Forager's runkey.Parse names it.
func runKeyNotAKey(err error) error {
	return input(fmt.Errorf("QORY_RUN_KEY is not a run key: %s", runKeyReason(err)))
}

// runKeyReason is the part of a run key that is wrong, as Forager's runkey.Parse names
// it, never the value.
func runKeyReason(err error) string {
	if re := (*runkey.Error)(nil); errors.As(err, &re) {
		return re.Reason
	}
	return err.Error()
}

// codeRunKeyNeedsWall is the refusal of an unwalled run whose run key QORY_RUN_KEY
// holds: the environment qory started with, which an unwalled agent can read on macOS,
// and on Linux from the program that started qory, holds the key.
const codeRunKeyNeedsWall = "run_key_needs_wall"

// runKeyNeedsWall is the run_key_needs_wall refusal, as input.
func runKeyNeedsWall() error {
	return input(&refusedError{
		text: fmt.Sprintf("%s is in the environment qory started with, which an unwalled agent can read (on macOS always, on Linux through the program that started qory), so a run with it needs a wall: --wall %s, or wall in %s; or give the run key with --%s, such as --%s 0 for stdin", config.EnvRunKey, config.WallDocker, config.ForagerFileName, runKeyFDFlag, runKeyFDFlag),
		err:  &session.Refusal{Code: codeRunKeyNeedsWall},
	})
}

// remoteGateway is the separate gateway the run key names, as the session reaches it.
func (k *runKey) remoteGateway() session.RemoteGateway {
	return session.RemoteGateway{RunKey: k.value}
}

// throughGatewayLine is the first line of a run behind the separate gateway its run key
// names: the gateway's host and port, and the run.
func throughGatewayLine(k *runKey, runID string) string {
	return fmt.Sprintf("qory run: through the gateway %s, run %s", gatewayHost(k.key.GatewayURL), runID)
}

// gatewayHost is the host of the gateway's URL, and its port when it names one, as the
// run's first line names the gateway.
func gatewayHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// The flags of qory run that go into a run's registration, which the run's starter made
// when a run key is given.
var registrationFlags = []string{"label", "kind", "title", "subject", "details"}

// runKeyGiven says how a run key reaches qory, in the refusals of what a run whose
// starter registered it cannot have.
const runKeyGiven = "a run key is given, by QORY_RUN_KEY or --run-key-fd"

// behindGateway refuses, before anything starts, what a run whose starter registered it,
// a run key being given, cannot have: --local and --policy, which are for a gateway of
// the run's own; an access key on this machine, in a file beside forager.yaml, a
// variable or the descriptor --access-key-secret-fd names, which keyFD says was given;
// a gateway section in r; and the flags of the run's registration that were given,
// given. r is nil without forager.yaml. --relabel, which relabel says was given, is
// refused after --policy: the run's starter chose the run's labels.
func behindGateway(r *config.Forager, local, policy, relabel, keyFD bool, given []string) error {
	if local {
		return input(errors.New("--local runs with a gateway the run starts itself and no server, and " + runKeyGiven + ": the run goes through the gateway its run key names; give no run key to run --local"))
	}
	if policy {
		return input(errors.New("--policy is a run's policy file for a gateway the run starts itself, and " + runKeyGiven + ": the gateway its run key names takes the run's policy from the server"))
	}
	if relabel {
		return relabelRunKey()
	}
	dir, err := machineDir()
	if err != nil {
		return err
	}
	if dir != "" {
		held, err := heldSecret(dir)
		if err != nil {
			return err
		}
		if held != "" {
			return input(fmt.Errorf("%s exists, and %s: a machine whose runs a starter registers holds no access key; remove the file", held, runKeyGiven))
		}
	}
	v := config.TakenServerVariables()
	for _, set := range []struct{ name, value string }{{accesskey.EnvID, v.AccessKeyID}, {accesskey.EnvSecret, v.AccessKeySecret}, {accesskey.EnvPin, v.ApiaryPublicKey}} {
		if set.value != "" {
			return input(fmt.Errorf("%s is set, and %s: a machine whose runs a starter registers holds no access key; unset %s", set.name, runKeyGiven, set.name))
		}
	}
	if keyFD {
		return input(fmt.Errorf("--%s and a run key are both given: a machine whose runs a starter registers holds no access key; leave out --%s", secretFDFlag, secretFDFlag))
	}
	if r != nil && r.Gateway {
		return input(fmt.Errorf("%s: gateway: %s, so the run goes through the gateway its run key names, which holds the server, the policy, the credentials and the tools, and this machine runs no gateway: remove the gateway section, or give no run key", r.File, runKeyGiven))
	}
	if len(given) > 0 {
		return input(registeredAlready(given))
	}
	return nil
}

// credentialVariables are the variables rt reads a credential from: the ones its
// descriptor declares a secret in, the ones it reserves, and the ones its model section
// lists a model key in, for Claude Code ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN and
// CLAUDE_CODE_OAUTH_TOKEN. A name may be listed twice. Nil for a runtime that describes
// none.
func credentialVariables(rt runtimes.Runtime) []string {
	var names []string
	if s, ok := rt.(runtimes.Secrets); ok {
		secrets := s.Secrets()
		names = slices.Clone(secrets.Reserves)
		for _, d := range secrets.Declares {
			names = append(names, d.Name)
		}
	}
	if m, ok := rt.(runtimes.Model); ok {
		if model, ok := m.Model(); ok {
			for _, k := range model.Keys {
				names = append(names, k.Variable)
			}
		}
	}
	return names
}

// modelCredentialPassed refuses, as input, a variable of the model's credential that a
// walled run given a run key would pass to its agent: such a run takes the credential
// through the gateway its run key names alone. The variables are [credentialVariables]
// of rt. This machine holds no credentials section, so the runtime names them. env is
// --env, each entry NAME or NAME=value, and wallEnv is wall.env. Names match exactly, as
// in a Linux environment. The line names the variable, never a value.
func modelCredentialPassed(rt runtimes.Runtime, env, wallEnv []string) error {
	names := credentialVariables(rt)
	refused := func(name, from string) error {
		return input(fmt.Errorf("qory run: %s is the model's credential, which a walled run given a run key takes only through the gateway; remove it from %s", name, from))
	}
	for _, n := range env {
		if name, _, _ := strings.Cut(n, "="); slices.Contains(names, name) {
			return refused(name, "--env")
		}
	}
	for _, name := range wallEnv {
		if slices.Contains(names, name) {
			return refused(name, "wall.env in "+config.ForagerFileName)
		}
	}
	return nil
}

// registeredAlready is the refusal of the flags of a run's registration, names, given
// to qory run while a run key is given: the run's starter registered it already.
func registeredAlready(names []string) error {
	flags := make([]string, len(names))
	for i, n := range names {
		flags[i] = "--" + n
	}
	joined, goes, it := flags[0], "goes", "it"
	if len(flags) > 1 {
		joined = strings.Join(flags[:len(flags)-1], ", ") + " and " + flags[len(flags)-1]
		goes, it = "go", "them"
	}
	return fmt.Errorf("%s %s into the run's registration, and %s: its starter registered the run already, so give %s to qory run register", joined, goes, runKeyGiven, it)
}

// resendThroughGateway sends the separate gateway a run spoke to what the session of the
// run runID did not deliver to it, from dir, the run's directory, with the run key k, nil
// when QORY_RUN_KEY is unset, within wait of sig: Forager's session then says the run key
// is needed, when anything is owed. It says what came of it on w: what the gateway
// accepted, what it did not, a run it has ended, and a run it never opened, whose record
// has nothing to send and stays, exit 0. A record its session still holds, a run that is
// not recorded, and a record a gateway of the run's own made, which goes to the server,
// are refused as input. root is the checkout's, against which dir is shown. r, nil
// without forager.yaml, names the wall whose leftovers are removed.
func resendThroughGateway(sig context.Context, wait time.Duration, w io.Writer, r *config.Forager, k *runKey, runID, dir, root string, report func(string)) error {
	ctx, cancel := context.WithTimeout(sig, wait)
	defer cancel()
	lines := resendLines(report)
	gw := session.RemoteGateway{}
	if k != nil {
		gw = k.remoteGateway()
	}
	res, err := session.Resend(ctx, session.ResendSpec{
		Gateway:        gw,
		Dir:            dir,
		ForagerVersion: build().title(),
		Report:         lines.line,
	})
	lines.done(func(kind int) bool {
		return kind == heldNotOpened && err == nil && res.NotOpened && !res.RunClosed && res.Undelivered == 0
	})
	if err == nil && (res.Undelivered == 0 || res.RunClosed) {
		// Nothing is left to send, or the gateway ended the run: a run key this machine
		// kept for the run has done its work.
		dropKeptRunKey(runID, report)
	}
	shown := ui.Short(dir, root)
	var pe *fs.PathError
	switch {
	case errors.Is(err, session.ErrRunning):
		return input(fmt.Errorf("the run %s is running", runID))
	case errors.As(err, &pe) && errors.Is(err, fs.ErrNotExist) && pe.Path == filepath.Join(dir, sink.SessionFile):
		return input(fmt.Errorf("no run %s is recorded in this checkout", runID))
	case errors.As(err, &pe) && errors.Is(err, fs.ErrExist) && pe.Path == filepath.Join(dir, sink.EventsFile):
		return input(ownGatewayResend(runID, err))
	case err != nil && k == nil && strings.HasSuffix(err.Error(), runKeyNeeded):
		// Forager's own line, word for word: the run key is missing from the input.
		return input(err)
	case err != nil:
		return explain(err, nil)
	}
	reaped := 0
	if r != nil {
		reaped = reapWall(sig, r, runID, report)
	}
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

// runKeyNeeded ends Forager's error of a resend through a gateway without the run key,
// which has no sentinel of its own.
const runKeyNeeded = ": the run key is needed to send to its gateway; set QORY_RUN_KEY"

// ownGatewayResend is the refusal of a resend with a run key given of the run runID,
// which ran with a gateway of its own on this machine: its record goes to the server.
// It unwraps to err.
func ownGatewayResend(runID string, err error) error {
	return &saidError{text: fmt.Sprintf("the run %s started its gateway itself on this machine, so its record goes to the server, not to a gateway: resend it without a run key", runID), err: err}
}

// gatewayEndedResend says that the run had ended when its events were sent again, and
// how it ended, by the state and the reason of its end ([outcomeOf]). The events stay in
// the run directory, shown. A run whose end nothing records, a 410 of a record with no
// exit, ends as the code of the gateway's 410 says, qory run's outcome and words for it;
// a 410 with a code Forager does not end a run with is run_closed, as Forager reads it.
func gatewayEndedResend(res session.ResendResult, shown string) error {
	o := outcomeOf(res.State, res.Reason)
	if !o.known {
		o = outcomeOf("", res.ClosedReason)
	}
	if !o.known {
		o = outcomeOf("", event.ReasonRunClosed)
	}
	ended := o.word()
	if o.reason != "" && !o.noOutcome {
		ended += ": " + o.reason
	}
	return fmt.Errorf("the run has ended (%s), so no more of its events are taken; they stay in %s", ended, shown)
}
