package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/runkey"
	"github.com/qoryai/forager/server"
	"github.com/qoryai/forager/session"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/ui"
)

// The verbs of a run's starter, as their lines name them after "qory".
const (
	verbRegister = "run register"
	verbEnd      = "run end"
)

// registerRunKeyFDUsage is the help of qory run register's --run-key-fd.
const registerRunKeyFDUsage = "write the run key to this file descriptor, 3 or above; required"

// newRegister builds the register verb: a run's starter registers the run with the
// server under the node pool's access key, and hands the run key the server answers
// with to the job ([giveRunKey]). Nothing is written on this machine.
func newRegister() *cobra.Command {
	var runID, instanceID string
	var attachWithin time.Duration
	var labels, subjects []string
	var kind, title, details string
	var secretFD, keyFD int
	c := &cobra.Command{
		Use:   "register",
		Short: "Register a run with the server, and write its run key for the job to a descriptor",
		Long: `Register a run with the server in ` + config.ForagerFileName + `, for a job a runner behind a gateway runs.
This is the starter's command: it signs the registration with the access key of the node
pool, and the key stays on this machine. The server answers with the run's run key, which
names the run and the gateway it goes through.

` + registerOutputHelp + `

The access key's secret comes from --access-key-secret-fd, else QORY_ACCESS_KEY_SECRET,
else access-key-secret, as for qory run. With gateway.server.via set, the registration goes
through that gateway, unchanged.

Use a new run id for each attempt, a retry included. --instance-id names the runner the run
takes a place on: by default the run's id, for a runner used once. --attach-within is
how long the runner has to start the run; by default the server's window, and never more
than the server allows.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#for-a-starter`,
		Example: registerExamples,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Every line is masked with the command's mask list; each secret value joins
			// it as qory reads the value.
			streams := newCommandStreams(cmd.OutOrStdout(), cmd.ErrOrStderr())
			defer streams.flush()
			// The access key's descriptor is read and closed first, whatever comes next.
			var fdKey *accesskey.Key
			if cmd.Flags().Changed(secretFDFlag) {
				k, err := readSecretFD(secretFD)
				if err != nil {
					return err
				}
				fdKey = k
				secretmask.Default().Add(k.Secret())
			}
			out, err := runKeyOut(cmd.Flags().Changed(runKeyFDFlag), keyFD)
			if err != nil {
				return err
			}
			defer out.close()
			if runID == "" {
				runID = newRunID()
			} else if err := session.CheckRunID(runID); err != nil {
				return input(fmt.Errorf("--run-id: %w", err))
			}
			if instanceID == "" {
				instanceID = runID
			} else if err := accesskey.CheckInstanceID(instanceID); err != nil {
				return input(fmt.Errorf("--instance-id: %w", err))
			}
			attach := 0
			if cmd.Flags().Changed("attach-within") {
				if attachWithin < time.Second || attachWithin%time.Second != 0 {
					return input(fmt.Errorf("--attach-within %s is not a whole number of seconds, 1s or more", attachWithin))
				}
				attach = int(attachWithin / time.Second)
			}
			named, err := parseLabels(labels)
			if err != nil {
				return err
			}
			if err := session.CheckLabels(named); err != nil {
				return input(err)
			}
			stdin := cmd.InOrStdin()
			about, err := aboutFrom(kind, title, subjects, details, stdin, isTerminal(stdin))
			if err != nil {
				return err
			}
			r, err := starterServer("register the run with")
			if err != nil {
				return err
			}
			if err := enrolmentCodeFileFor(r, "qory run register"); err != nil {
				return err
			}
			key, err := signingKey(r, fdKey)
			if err != nil {
				return err
			}
			body, err := server.GatewayRegistration{RunID: runID, Labels: named, About: about, AttachSeconds: attach}.Body()
			if err != nil {
				return input(err)
			}
			stderr := streams.errOut
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			client := serverClient(r, key.key, instanceID, r.InstanceNameOrDefault())
			id := &serverIdentity{key: key, instanceID: instanceID, server: r.Server.URL}
			conf, err := discover(ctx, client, key, verbRegister, stderr)
			if err != nil {
				return explainRegistration(err, id, runID, false)
			}
			ans, err := client.RegisterThroughGateway(ctx, conf.Run.URL, body)
			if err != nil {
				return explainRegistration(err, id, runID, false)
			}
			secretmask.Default().Add(ans.RunKey)
			k, err := runkey.Parse(ans.RunKey)
			if err != nil {
				// Forager read it as a run key of the run already.
				return err
			}
			fmt.Fprintf(stderr, "qory %s: run %s, through the gateway %s, to start by %s\n", verbRegister, runID, gatewayHost(k.GatewayURL), k.AttachBy.UTC().Format(time.RFC3339))
			return giveRunKey(streams.out, stderr, out, runID, ans.RunKey)
		},
	}
	c.Flags().StringVar(&runID, "run-id", "", "the run's id, a UUID in lower case (default a new one)")
	c.Flags().StringVar(&instanceID, "instance-id", "", "the runner the run takes a place on, 1 to 64 of A-Z, a-z, 0-9, dot, underscore and dash (default the run id: a runner used once)")
	c.Flags().DurationVar(&attachWithin, "attach-within", 0, "how long the runner has to start the run, in whole seconds, such as 15m (default the server's; the server allows at most its limit)")
	c.Flags().StringArrayVar(&labels, "label", nil, "a key=value name for the run, reported in its events; repeatable")
	c.Flags().StringVar(&kind, "kind", "", "what kind of run it is, such as review or fix, reported when the run starts")
	c.Flags().StringVar(&title, "title", "", "the run's title, for a person to read, reported when the run starts")
	c.Flags().StringArrayVar(&subjects, "subject", nil, "what the run works on, type=<type>,ref=<ref>[,url=<url>][,title=<title>], such as type=ticket,ref=7; title takes the rest of the value, commas too; repeatable")
	c.Flags().StringVar(&details, "details", "", "a JSON object of the run's details, read from this file, or - for stdin when stdin is not a terminal; at most 8192 bytes compacted, 4 levels deep")
	c.Flags().IntVar(&secretFD, secretFDFlag, 0, secretFDUsage)
	c.Flags().IntVar(&keyFD, runKeyFDFlag, 0, registerRunKeyFDUsage)
	return c
}

// registerOutputHelp is the paragraph of qory run register's help on where the run key
// goes, which [giveRunKey] decides.
const registerOutputHelp = `qory writes the run key to the descriptor --run-key-fd names, and every other line on
stderr; stdout gets nothing. Give the job the run key as a secret: whoever holds it can
act for this run until the run ends. On the runner, qory run takes it from QORY_RUN_KEY,
or from --run-key-fd. Nothing is written on this machine.`

// registerExamples are qory run register's examples, which show where the run key goes.
const registerExamples = `  run_key="$(qory run register --run-key-fd 3 --label issue=77 --title "Fix the parser" 3>&1)"
  run_key="$(qory run register --run-key-fd 3 --run-id "$run_id" --instance-id runner-07 --attach-within 15m 3>&1)"`

// keyOut is where qory run register writes the run key: the descriptor --run-key-fd
// names, n, open.
type keyOut struct {
	n int
	f *os.File
}

// close closes the descriptor, once.
func (o *keyOut) close() {
	if o != nil && o.f != nil {
		o.f.Close()
		o.f = nil
	}
}

// runKeyOut checks the descriptor --run-key-fd names, n, before the run registers: it is
// required, 3 or above, and open. The descriptor is held until the run key is written.
func runKeyOut(set bool, n int) (*keyOut, error) {
	if !set {
		return nil, input(fmt.Errorf("qory %s writes the run key to a descriptor, and --%s names none: name one of 3 or above, such as --%s 3", verbRegister, runKeyFDFlag, runKeyFDFlag))
	}
	if n >= 0 && n < 3 {
		return nil, input(fmt.Errorf("--%s %d: the standard input, output and error carry no run key; name a descriptor of 3 or above", runKeyFDFlag, n))
	}
	var f *os.File
	if n >= 0 {
		f = os.NewFile(uintptr(n), "--"+runKeyFDFlag)
	}
	if f == nil {
		return nil, input(fmt.Errorf("--%s %d is not an open descriptor", runKeyFDFlag, n))
	}
	if _, err := f.Stat(); err != nil {
		return nil, input(fmt.Errorf("--%s %d is not an open descriptor", runKeyFDFlag, n))
	}
	return &keyOut{n: n, f: f}, nil
}

// giveRunKey hands the run key of the run runID, value, to the starter, and says so on
// stderr: the bare key and a line feed to the descriptor out, stdout getting nothing.
// It is the one place that decides where the run key goes. The key is never written
// anywhere else, and no error contains it.
func giveRunKey(_, stderr io.Writer, out *keyOut, runID, value string) error {
	b := []byte(value + "\n")
	_, err := out.f.Write(b)
	clear(b)
	if err == nil {
		err = out.f.Close()
		out.f = nil
	}
	if err != nil {
		return fmt.Errorf("the run %s is registered, and its run key could not be written to --%s %d: %w; end the run with qory run end %s", runID, runKeyFDFlag, out.n, writeError(err), runID)
	}
	fmt.Fprintln(stderr, "The run key goes to the job as a secret: whoever holds it can act for this run until the run ends. Nothing was written on this machine.")
	return nil
}

// writeError is the cause of a failed write, without the descriptor's name, which the
// refusal says already.
func writeError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// starterServer is forager.yaml, which must define a server, the one a starter's command
// sends its request to, and whose gateway.server.via, when it sets one, Forager can send
// through; what names the request in the refusal of a file without one.
func starterServer(what string) (*config.Forager, error) {
	r, err := config.LoadForager()
	if err != nil {
		return nil, input(err)
	}
	if r == nil || r.Server == nil {
		return nil, input(fmt.Errorf("%s defines no server to %s", config.ForagerFileName, what))
	}
	if err := checkVia(r); err != nil {
		return nil, err
	}
	return r, nil
}

// discover reads the server's signed discovery with client, signed with key, and acts on
// it as a run does ([onDiscovery]): it prints the node, after "qory <verb>:", and
// refuses a gateway's access key.
func discover(ctx context.Context, client *server.Client, key *accessKey, verb string, report io.Writer) (*server.Configuration, error) {
	conf, _, err := client.Discover(ctx)
	if err != nil {
		return nil, err
	}
	dir, err := machineDir()
	if err != nil {
		return nil, err
	}
	if err := onDiscovery(dir, key, client.InstanceID, verb, report, gateway.Discovery{NodeID: conf.NodeID, Secrets: conf.Secrets != nil}); err != nil {
		return nil, err
	}
	return conf, nil
}

// explainRegistration words the server's refusal of a run's registration through a
// gateway, runID's, for the person, then Forager's own line: a starter's, or, keyed, the
// one qory run sends on a machine with its own key whose gateway.server.via names a
// gateway. Any other error is [explain]'s.
func explainRegistration(err error, id *serverIdentity, runID string, keyed bool) error {
	var ref *session.Refusal
	if !errors.As(err, &ref) {
		return err
	}
	node := "this key's node"
	if keyed {
		node = "this machine's node"
	}
	var text string
	switch ref.Code {
	case "no_default_gateway":
		text = node + ` is set to "Default gateway", and no default gateway serves its workspace: mark a gateway as default in Qory Apiary, or choose one for the node`
	case "gateway_not_serving":
		text = "the gateway " + node + " names no longer serves the node's workspace: choose another gateway for the node in Qory Apiary"
	case "gateway_without_key":
		text = "the gateway " + node + " goes through has no active access key, so no run could start there: enrol a key for the gateway, then register again"
	case "node_is_direct":
		text = `this key's node is set to "Direct": each of its machines registers each run itself, and a starter registers none. Choose a gateway for the node in Qory Apiary`
		if keyed {
			text = `this machine's node is set to "Direct", and gateway.server.via names a gateway: remove gateway.server.via, or choose a gateway for the node in Qory Apiary`
		}
	case "run_id_used":
		return &saidError{text: runIDUsedText(runID), err: err}
	}
	if text == "" && !keyed {
		switch ref.Code {
		case "gateway_key":
			text = "this access key is a gateway's, and a gateway registers no runs: register with the key of the node pool the run belongs to"
		case accesskey.CodeInstanceLimit:
			text = fmt.Sprintf("the node's live instances have reached its limit, so the run %s is not registered: wait for a run to end, or have an owner or administrator in Qory Apiary clear an instance", runID)
		case accesskey.CodeAnswerUnsigned:
			text = "an answer of the server does not verify under the pinned apiary_public_key, so the run is not registered: check gateway.server.url and the pin"
		}
	}
	if text == "" {
		return explain(err, id)
	}
	return &refusedError{text: text, err: err}
}

// The outcomes qory run end gives, by qory's word, as the server's states.
var endOutcomes = map[string]string{"completed": event.StateSucceeded, "failed": event.StateFailed, "cancelled": event.StateCancelled}

// reasonShape is a starter's reason: a code.
var reasonShape = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// newEnd builds the end verb: a run's starter gives the server the run's outcome, under
// the node pool's access key, and a run still running ends.
func newEnd() *cobra.Command {
	var outcomeWord, reason string
	var secretFD int
	c := &cobra.Command{
		Use:   "end <run-id>",
		Short: "End a run a starter registered, and say how it went",
		Long: `Say how a run went, as its starter. The server records the outcome, and a run that is still
running ends: its runtime is stopped. A run has one outcome: the same one again gets the
same answer, and a different one is refused.

--outcome is completed, failed or cancelled. Without it, the run is cancelled, with no
outcome given. --reason is a code of yours, such as checks_failed: a lower-case letter,
then lower-case letters, digits and _, at most 64. qory run says it with spaces for
underscores. --reason needs --outcome.

The request is signed with the access key of the node pool that registered the run, read as
qory run register reads it, and goes through gateway.server.via when that is set.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#how-a-run-ends-there`,
		Example: `  qory run end "$run_id" --outcome completed --reason all_checks_passed
  qory run end "$run_id" --outcome failed --reason checks_failed
  qory run end "$run_id"                                   # cancelled, with no outcome given`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return input(fmt.Errorf("qory %s takes the run id", verbEnd))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := newCommandStreams(cmd.OutOrStdout(), cmd.ErrOrStderr())
			defer streams.flush()
			var fdKey *accesskey.Key
			if cmd.Flags().Changed(secretFDFlag) {
				k, err := readSecretFD(secretFD)
				if err != nil {
					return err
				}
				fdKey = k
				secretmask.Default().Add(k.Secret())
			}
			runID := args[0]
			if err := session.CheckRunID(runID); err != nil {
				return input(err)
			}
			o, err := endOutcome(outcomeWord, reason)
			if err != nil {
				return err
			}
			r, err := starterServer("send the outcome to")
			if err != nil {
				return err
			}
			if err := enrolmentCodeFileFor(r, "qory run end"); err != nil {
				return err
			}
			key, err := signingKey(r, fdKey)
			if err != nil {
				return err
			}
			stderr := streams.errOut
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			// The outcome is signed as the run's own instance.
			client := serverClient(r, key.key, runID, r.InstanceNameOrDefault())
			id := &serverIdentity{key: key, instanceID: runID, server: r.Server.URL}
			conf, err := discover(ctx, client, key, verbEnd, stderr)
			if err != nil {
				return explainDiscoveryForEnd(err, id)
			}
			if err := client.PostOutcome(ctx, conf.Run.URL, runID, o); err != nil {
				return explainOutcome(err, id, runID)
			}
			ui.New(stderr).Success("the server records the run %s as %s", runID, recordedAs(o))
			return nil
		},
	}
	c.Flags().StringVar(&outcomeWord, "outcome", "", "how the run went: completed, failed or cancelled (default none: cancelled, with no outcome given)")
	c.Flags().StringVar(&reason, "reason", "", "why, a code of yours such as checks_failed; needs --outcome")
	c.Flags().IntVar(&secretFD, secretFDFlag, 0, secretFDUsage)
	return c
}

// endOutcome is the outcome qory run end sends for --outcome, word, and --reason: none
// for no word, else the word's state and the reason, a code of the starter's own. Each
// refusal is input, and prints the flag's value as [exports.Value] prints it.
func endOutcome(word, reason string) (server.Outcome, error) {
	var o server.Outcome
	if word != "" {
		state, ok := endOutcomes[word]
		if !ok {
			return o, input(fmt.Errorf("--outcome %s is not completed, failed or cancelled", exports.Value(word, false)))
		}
		o.State = state
	}
	if reason == "" {
		return o, nil
	}
	if word == "" {
		return o, input(errors.New("--reason needs --outcome: without one, the run is cancelled, with no outcome given"))
	}
	if !reasonShape.MatchString(reason) {
		return o, input(fmt.Errorf("--reason %s is not a code: a lower-case letter, then lower-case letters, digits and _, at most 64", exports.Value(reason, false)))
	}
	if !event.StarterReason(reason) {
		return o, input(fmt.Errorf("--reason %s is one of Forager's reasons; give one of yours", reason))
	}
	o.Reason = reason
	return o, nil
}

// recordedAs is the outcome o as qory run end says the server records it: the outcome's
// word and its reason, with spaces for underscores, or cancelled, with no outcome given.
func recordedAs(o server.Outcome) string {
	if o.State == "" {
		return "cancelled, with no outcome given"
	}
	out := outcomeOf(o.State, o.Reason)
	if out.reason == "" {
		return out.word()
	}
	return out.word() + ": " + out.reason
}

// explainDiscoveryForEnd words a refusal of the discovery qory run end reads before it
// sends the outcome: an answer that does not verify says the outcome was not sent; any
// other is [explain]'s.
func explainDiscoveryForEnd(err error, id *serverIdentity) error {
	var ref *session.Refusal
	if errors.As(err, &ref) && ref.Code == accesskey.CodeAnswerUnsigned {
		return &refusedError{text: "an answer of the server does not verify under the pinned apiary_public_key, so the outcome was not sent: check gateway.server.url and the pin", err: err}
	}
	return explain(err, id)
}

// explainOutcome words the server's refusal of the outcome of the run runID for the
// person, then Forager's own line; any other is [explain]'s.
func explainOutcome(err error, id *serverIdentity, runID string) error {
	var ref *session.Refusal
	var status *server.AnswerError
	switch {
	case errors.As(err, &ref) && ref.Code == "outcome_given":
		return &refusedError{text: fmt.Sprintf("the run %s has another outcome already, and a run has one", runID), err: err}
	case errors.As(err, &ref) && ref.Status == 404, errors.As(err, &status) && status.Status == 404:
		return &refusedError{text: fmt.Sprintf("the server knows no run %s of this key's node: it registered none of that id, or no longer keeps its record", runID), err: err}
	case errors.As(err, &ref) && ref.Code == accesskey.CodeAnswerUnsigned:
		return &refusedError{text: "an answer of the server does not verify under the pinned apiary_public_key, so qory cannot tell whether the outcome was recorded: check gateway.server.url and the pin, then run the same command again", err: err}
	}
	return explain(err, id)
}
