package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/runkey"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/sink"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/ui"
)

// runKey is the run key qory took from QORY_RUN_KEY, white space around it trimmed, and
// what it says, read as a runner reads it, unverified: the run, and the separate gateway
// the run goes through. It is a secret: it is handed to Forager's session alone, and fmt
// prints a runKey by its gateway and its run, never its value.
type runKey struct {
	value string
	key   runkey.Key
}

// String names the run key by its run and its gateway, never its value.
func (k runKey) String() string {
	return "run key for the run " + k.key.RunID + " through " + gatewayHost(k.key.GatewayURL)
}

// GoString is k as %#v prints it.
func (k runKey) GoString() string { return k.String() }

// Format prints k as String does, under every verb and flag.
func (k runKey) Format(f fmt.State, _ rune) { io.WriteString(f, k.String()) }

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
	reason := err.Error()
	if re := (*runkey.Error)(nil); errors.As(err, &re) {
		reason = re.Reason
	}
	return input(fmt.Errorf("QORY_RUN_KEY is not a run key: %s", reason))
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
// when QORY_RUN_KEY is set.
var registrationFlags = []string{"label", "kind", "title", "subject", "details"}

// behindGateway refuses, before anything starts, what a run whose starter registered it,
// QORY_RUN_KEY being set, cannot have: --local and --policy, which are for a gateway of
// the run's own; an access key on this machine, in a file beside forager.yaml, a
// variable or the descriptor --access-key-secret-fd names, which keyFD says was given;
// a gateway section in r; and the flags of the run's registration that were given,
// given. r is nil without forager.yaml.
func behindGateway(r *config.Forager, local, policy, keyFD bool, given []string) error {
	if local {
		return input(errors.New("--local runs with a gateway of this run's own and no server, and QORY_RUN_KEY is set: the run goes through the gateway its run key names; unset QORY_RUN_KEY to run --local"))
	}
	if policy {
		return input(errors.New("--policy is the run's own policy for a gateway the run starts itself, and QORY_RUN_KEY is set: the gateway its run key names takes the run's policy from the server"))
	}
	if dir := machineDir(); dir != "" {
		if held := heldSecret(dir); held != "" {
			return input(fmt.Errorf("%s exists, and QORY_RUN_KEY is set: a machine whose runs a starter registers holds no access key; remove the file", held))
		}
	}
	v := config.TakenServerVariables()
	for _, set := range []struct{ name, value string }{{accesskey.EnvID, v.AccessKeyID}, {accesskey.EnvSecret, v.AccessKeySecret}, {accesskey.EnvPin, v.ApiaryPublicKey}} {
		if set.value != "" {
			return input(fmt.Errorf("%s and QORY_RUN_KEY are both set: a machine whose runs a starter registers holds no access key; unset %s", set.name, set.name))
		}
	}
	if keyFD {
		return input(fmt.Errorf("--%s and QORY_RUN_KEY are both given: a machine whose runs a starter registers holds no access key; leave out --%s", secretFDFlag, secretFDFlag))
	}
	if r != nil && r.Gateway {
		return input(fmt.Errorf("%s: gateway: QORY_RUN_KEY is set, so the run goes through the gateway its run key names, which holds the server, the policy and the credentials, and this machine runs no gateway: remove the gateway section, or unset QORY_RUN_KEY", r.File))
	}
	if len(given) > 0 {
		return input(registeredAlready(given))
	}
	return nil
}

// registeredAlready is the refusal of the flags of a run's registration, names, given
// to qory run while QORY_RUN_KEY is set: the run's starter registered it already.
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
	return fmt.Errorf("%s %s into the run's registration, and QORY_RUN_KEY is set: its starter registered the run already, so give %s to qory run register", joined, goes, it)
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
	shown := ui.Short(dir, root)
	var pe *fs.PathError
	switch {
	case errors.Is(err, session.ErrRunning):
		return input(fmt.Errorf("the run %s is running", runID))
	case errors.As(err, &pe) && errors.Is(err, fs.ErrNotExist) && pe.Path == filepath.Join(dir, sink.SessionFile):
		return input(fmt.Errorf("no run %s is recorded in this checkout", runID))
	case errors.As(err, &pe) && errors.Is(err, fs.ErrExist) && pe.Path == filepath.Join(dir, sink.EventsFile):
		return input(ownGatewayResend(runID, err))
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

// ownGatewayResend is the refusal of a resend with QORY_RUN_KEY set of the run runID,
// which ran with a gateway of its own on this machine: its record goes to the server.
// It unwraps to err.
func ownGatewayResend(runID string, err error) error {
	return &saidError{text: fmt.Sprintf("the run %s ran with a gateway of its own on this machine, so its record goes to the server, not to a gateway: resend it without QORY_RUN_KEY", runID), err: err}
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
