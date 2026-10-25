package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/gateway"
	"github.com/qoryai/forager/policy"
	"github.com/qoryai/forager/refusal"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes/catalog"
	"github.com/qoryai/forager/sink"
	"github.com/qoryai/forager/wall"
	"github.com/qoryai/integrations/contracts"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/foragerdir"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/provisioner"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
)

// DescriptorsDir is the directory of runtime descriptor overrides, <runtime>.yaml,
// under the user's configuration directory.
const DescriptorsDir = "runtimes"

// newRun builds the run verb, which starts a runtime on the composed harness through
// Forager's session: the launch spec is what qory harness launch prints, the policy
// and the server are forager.yaml in the user's configuration directory or named
// by flag, and Forager records the run in qory's state directory, in a folder of
// the checkout's. The verb opens the run at its gateway, composes the harness, joins
// one session to the run, closes the run with qory's outcome, and exits with the
// runtime's status.
func newRun() *cobra.Command {
	var h homeOptions
	var composing composeOptions
	var local, headless, relabel bool
	var o wallOptions
	var policyFile, runID string
	var labels []string
	var kind, title, details string
	var subjects []string
	var timeout, grace time.Duration
	var stopSignal string
	var secretFD, keyFD int
	var codeFile, gatewayURL string
	c := &cobra.Command{
		Use:   "run [runtime] [-- argument...]",
		Short: "Run the agent on its harness, observed and recorded",
		Long: `Compose the harness, then run the agent on it, inside Forager's session.

Every connection goes through a proxy on this machine and is recorded. The record,
events.jsonl and output.log, goes to a folder of the checkout's under
~/.local/state/qory/runs ($XDG_STATE_HOME/qory/runs when that is set; qory refuses a
relative XDG_STATE_HOME), and qory names it when the run ends. The exit status is the
agent's when it exits by itself. When the run ends in another way, or its starter says
it went otherwise, the exit status is 0 if the run completed, 124 at the time limit,
and 1 otherwise.

qory run opens the run, then composes the harness, as qory harness compose does, into
the same home, and prints the result on stderr. -f, --model, --effort, --tier and
--tier-effort mean what they mean there. With a run key, the home is outside the folder
the run runs in, under qory/homes in the cache directory, unless --home or harness.home
sets another, and a home inside the folder stops the run.

The agent is the runtime of the stack's target. Name another first, and the run composes
for it instead; name one when the target has several. Arguments after -- go to the
agent. At a terminal the agent runs with its interface; --headless, no terminal, or a
headless argument such as -p runs it on pipes.

` + config.ForagerFileName + ` in ~/.config/qory sets what Forager does on this machine. A repository
cannot set it:

  gateway.egress        the hosts the agent may reach: enforce or observe, allow and deny
  wall                  run the agent in a container whose one way out is the proxy
  gateway.credentials   tokens the proxy sets on requests; behind a wall the agent never has them
  gateway.integrations  programs that supply such tokens, such as qory-github
  gateway.server        the server every run reports to; --local skips it
  session.run           a time limit, and how the agent is stopped

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md`,
		Example: `  qory run                                          # the agent, at your terminal
  qory run claude -- -p "Reply pong"                # one headless turn
  qory run --model claude-opus-5-5 -- -p "$prompt"  # composed with this model
  qory run --wall docker --image agent:1            # in a container
  qory run --image go-docker                        # in an image forager.yaml defines
  qory run --policy ~/policy.yaml -- -p "$prompt"   # with this run's policy file
  qory run --timeout 5h30m -- -p "$prompt"          # stop it after five and a half hours
  qory run --subject type=ticket,ref=7              # say which ticket the run works on`,
		Args: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash < 0 {
				dash = len(args)
			}
			if dash > 1 {
				return input(fmt.Errorf("one runtime at most; arguments for the runtime go after --"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// The access key's descriptor is read and closed first, whatever comes next, so
			// nothing qory starts inherits it.
			var fdKey *accesskey.Key
			if cmd.Flags().Changed(secretFDFlag) {
				k, err := readSecretFD(secretFD)
				if err != nil {
					return err
				}
				fdKey = k
			}
			// A run code is read here and redeemed for the run key last before the run
			// starts, once every check that can stop the run has passed, so that a run they
			// stop leaves the one-use code unused.
			stdin := cmd.InOrStdin()
			code, err := givenRunCode(cmd.Flags().Changed(runCodeFileFlag), cmd.Flags().Changed(gatewayFlag), cmd.Flags().Changed(runKeyFDFlag), codeFile, gatewayURL, stdin)
			if err != nil {
				return err
			}
			// A run whose starter registered it, a run key being given, goes through the
			// separate gateway its run key names: this machine starts no gateway of its own
			// and holds no access key. The run key's descriptor is read and closed here.
			key, err := givenRunKey(cmd.Flags().Changed(runKeyFDFlag), keyFD, stdin)
			if err != nil {
				return err
			}
			if code != nil && code.stdin || key != nil && key.fromFD && keyFD == 0 {
				// The run code or the run key took stdin, to its end: the agent gets none.
				stdin = strings.NewReader("")
			}
			remote := key != nil || code != nil
			runtime, extra := splitAtDash(cmd, args)
			if code != nil {
				if err := fromAnswerOnly(cmd, runtime, extra); err != nil {
					return err
				}
			}
			at, conf, err := locateRun(h, remote, true)
			if err != nil {
				return err
			}
			if err := enrolmentCodeFileFor(conf.Forager, "qory run"); err != nil {
				return err
			}
			if remote {
				var given []string
				for _, f := range registrationFlags {
					if cmd.Flags().Changed(f) {
						given = append(given, f)
					}
				}
				if err := behindGateway(conf.Forager, local, policyFile != "", relabel, fdKey != nil, given); err != nil {
					return err
				}
			}
			if relabel && local {
				return relabelLocal()
			}
			// A run given a run key is a dispatched run, which Qory Apiary started against
			// a repository this machine does not trust: Claude Code starts without the
			// checkout's settings, with no MCP server but those --mcp-config names, see
			// [dispatchedArgs]. It is decided here, before a run through via is given the
			// run key it registers.
			dispatched := remote
			// The run composes its harness once the run is open, before its session joins.
			if err := checkoutsHome(at); err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			user, err := config.UserDir()
			if err != nil {
				return input(err)
			}
			machine := foragerdir.Dir(user)
			var pol *gateway.Policy
			var server *gateway.Server
			if r := conf.Forager; r != nil {
				pol = machinePolicy(r)
				if r.Server != nil {
					if err := checkVia(r); err != nil {
						return err
					}
					if !local && !remote {
						if err := viaGatewayKeys(r); err != nil {
							return err
						}
					}
					server = gatewayServer(r.Server)
				}
			}
			if policyFile != "" {
				if server != nil && !local {
					return input(fmt.Errorf("--policy is a run's policy file without a server; with server configured the server's run configuration is the policy, and --local runs with the files alone"))
				}
				if pol, err = runPolicy(policyFile, at.root, pol); err != nil {
					return err
				}
			}
			// Behind a separate gateway the run's labels and what it is about are its
			// starter's registration's: the session sends none.
			var named map[string]string
			var about *session.About
			var keep *keptLabels
			if !remote {
				explicit, err := parseLabels(labels)
				if err != nil {
					return err
				}
				// The origin remote is read once: the labels the run sends are the ones
				// checked against the kept labels, and kept.
				origin, err := originLabels(at.root)
				if err != nil {
					return err
				}
				named = withOrigin(explicit, origin)
				if err := session.CheckLabels(named); err != nil {
					return input(err)
				}
				if about, err = aboutFrom(kind, title, subjects, details, stdin, isTerminal(stdin)); err != nil {
					return err
				}
				// A run against the server, directly or through via, runs only with the
				// labels its checkout's origin remote gave when the server first accepted
				// one of its runs, or with --relabel.
				if server != nil && !local {
					if keep, err = checkLabels(machine, at.root, explicit, origin, relabel); err != nil {
						return err
					}
				}
			}
			if runID != "" {
				if err := session.CheckRunID(runID); err != nil {
					return input(fmt.Errorf("--run-id: %w", err))
				}
			}
			if timeout < 0 || grace < 0 {
				return input(fmt.Errorf("--timeout and --stop-grace are not negative"))
			}
			if r := conf.Forager; r != nil {
				if !cmd.Flags().Changed("timeout") {
					timeout = r.Timeout
				}
				if !cmd.Flags().Changed("stop-grace") {
					grace = r.StopGrace
				}
				if !cmd.Flags().Changed("stop-signal") {
					stopSignal = r.StopSignal
				}
			}
			if err := session.CheckStopSignal(stopSignal); err != nil {
				return input(fmt.Errorf("--stop-signal: %w", exports.HideValueError(err, stopSignal)))
			}
			state, err := stateDir()
			if err != nil {
				return err
			}
			runs, err := runsDir(state, at.root)
			if err != nil {
				return err
			}
			stderr := cmd.ErrOrStderr()
			var id *serverIdentity
			if server != nil && !local {
				if id, err = identify(conf.Forager, stderr, "run", fdKey); err != nil {
					return err
				}
			}
			ctx, got, stop := signalContext(cmd.Context())
			defer stop()
			spec := runSpec{
				OpenSpec: session.OpenSpec{
					RunsDir:        runs,
					ForagerVersion: build().title(),
					RunID:          runID,
					Labels:         named,
					About:          about,
				},
				SessionSpec: session.SessionSpec{
					Env:         os.Environ(),
					Dir:         cwd,
					Interactive: !headless && isTerminal(stdin) && isTerminal(cmd.OutOrStdout()),
					Stdin:       stdin,
					Stdout:      cmd.OutOrStdout(),
					Stderr:      stderr,
					Forwarder:   append([]string{exe}, forwardArgs...),
					StopSignal:  stopSignal,
					StopGrace:   grace,
					SocketGrace: socketGrace,
				},
			}
			switch {
			case spec.RunID == "" && key != nil:
				spec.RunID = key.key.RunID
			case spec.RunID == "" && code != nil:
				// The run's id is the run key's, known once the code is redeemed.
			case spec.RunID == "":
				spec.RunID = newRunID()
			}
			// The gateway is the node toward the server: it signs every request with the
			// access key. --local runs with the files alone. Its link is served in memory
			// alone, to the session in this process: no socket, which another process could
			// reach.
			gw := gateway.Config{Policy: pol, Version: build().title(), RunDir: func(id string) string { return filepath.Join(spec.RunsDir, id) }, NoLinkSocket: true, Heartbeat: runHeartbeat}
			if id != nil {
				gw.Server = server
				gw.Server.AccessKey, gw.Server.InstanceID, gw.Server.InstanceName = id.key.key, id.instanceID, id.instanceName
			}
			selected := ""
			if pol != nil {
				selected = pol.Image
			}
			if err := enclose(&spec, conf.Forager, o, selected, remote || server != nil && !local, exe, at.root, at.home); err != nil {
				return err
			}
			// QORY_RUN_KEY is in the environment qory started with, which an unwalled agent
			// can read on macOS, and on Linux from the program that started qory, even once
			// qory unset it.
			if key != nil && key.inEnvironment() && spec.Wall == nil {
				return runKeyNeedsWall()
			}
			if spec.Wall == nil {
				if err := unwalledHere(at.root, cwd); err != nil {
					return err
				}
			}
			if spec.Wall != nil && h.home == "" {
				if err := userHome(at, conf, remote); err != nil {
					return err
				}
			}
			if policyFile != "" {
				for _, m := range spec.Mounts {
					if abs, _ := filepath.Abs(policyFile); !m.ReadOnly && reallyWithin(m.Path, abs) {
						return input(fmt.Errorf("--policy %s is inside %s, which the container may write; keep it outside or mount that read-only", exports.Value(policyFile, false), exports.Value(m.Path, false)))
					}
				}
			}
			// user is absolute or empty: config.UserDir refuses a relative one.
			foragerDir := user
			if err := makeRunsDir(state, spec.RunsDir); err != nil {
				return err
			}
			// A run from a run code takes its stack, runtime, compose flags, inputs and time
			// limit from the documents of the answer to the code; its workflow's time_limit
			// is known once the stack is composed, see prepareLaunch.
			var fromCode *codeRun
			var in string
			if code != nil {
				var documents json.RawMessage
				if key, documents, err = code.redeem(ctx, conf.Forager.InstanceNameOrDefault()); err != nil {
					return err
				}
				if spec.RunID == "" {
					spec.RunID = key.key.RunID
				}
				var doc contracts.Documents
				if doc, err = codeDocumentsOf(documents); err != nil {
					return err
				}
				// A prebuild opens the run itself, with no wall and no session, and ends
				// with the build. It starts as an unwalled run does: startRun stops it on
				// a machine with an access key, and marks its lock file unwalled, so a
				// command that makes a key exits with an error while the prebuild runs.
				// prebuildKeyed stops it there first, with a line of a prebuild: the fix in
				// startRun's line, a wall, does not apply to a prebuild.
				if doc.Step == provisioner.StepPrebuild {
					if err := prebuildKeyed(machine, id); err != nil {
						return err
					}
					runLock, err := startRun(machine, spec.RunID, false, id)
					if err != nil {
						return err
					}
					defer runLock.Release()
					return runCodePrebuild(ctx, stderr, conf.Forager, state, user, key, doc, spec.RunID)
				}
				if fromCode, err = codeRunOf(doc); err != nil {
					return err
				}
				if err := fromCode.apply(cmd); err != nil {
					return err
				}
				if spec.Wall != nil {
					if err := fromCode.reportExposed(at.root, cwd, at.home, at.report, spec.given); err != nil {
						return err
					}
				}
				runtime, composing.file = fromCode.runtime, fromCode.stack
				// A run whose harness is hidden composes a home with no link, which Forager
				// reads to mask the harness's text in the session's log and events.
				composing.hidden, spec.HarnessHidden = fromCode.hidden, fromCode.hidden
				timeout = provisioner.TimeLimit(fromCode.minutes, 0)
				var remove func()
				in, remove, err = fromCode.writeInputs()
				defer remove()
				if err != nil {
					return fmt.Errorf("check the temporary folder %s: qory run could not write the run's inputs there: %w%s", exports.Value(os.TempDir(), false), exports.HideError(err), codeUsed)
				}
				if spec.Wall != nil {
					spec.Mounts = append(spec.Mounts, wall.Mount{Path: in, ReadOnly: true})
				}
			}
			walled := spec.Wall != nil
			runLock, err := startRun(machine, spec.RunID, walled, id)
			if err != nil {
				return err
			}
			defer runLock.Release()
			// What the session holds until the run is over, the agent's environment folder
			// and the hold on the walled run's places, is let go in the reverse order.
			var cleanups []func()
			defer func() {
				for i := len(cleanups) - 1; i >= 0; i-- {
					cleanups[i]()
				}
			}()
			// A machine with its own access key and gateway.server.via registers the run
			// itself, through via, and runs it through the gateway the run key names, as a
			// run its starter registered does. The run key it keeps beside forager.yaml goes
			// once nothing of the run is left to send.
			kept := runsThroughVia(conf.Forager, id)
			said := func(line string) { fmt.Fprintln(stderr, "qory run:", line) }
			switch {
			case kept:
				if key, err = registerThroughVia(ctx, conf.Forager, id, spec.RunID, named, about, stderr); err != nil {
					return err
				}
				// The server accepted the run.
				keep.keep(said)
				remote = true
				spec.Labels, spec.About = nil, nil
			case id != nil:
				gw.Discovered = discovered(machine, id, "run", stderr)
			}
			apiary := ""
			if server != nil {
				apiary = server.URL
			}
			spec.OnVariables = unusedEnv(stderr, apiary)
			if !kept && keep != nil {
				// Forager resolves the session's variables once the gateway's join answer
				// came, and the gateway answers the run once the server accepted the run's
				// registration.
				unused := spec.OnVariables
				spec.OnVariables = func(a session.Applied) {
					unused(a)
					keep.keep(said)
				}
			}
			if r := conf.Forager; r != nil && !remote {
				gw.LockMemory, gw.RunLimits, gw.StackHosts = r.LockMemory, gatewayRunLimits(r), r.StackHosts
				e, err := expansion(r, pol, server != nil && !local, at.root, cwd, spec.Mounts)
				if err != nil {
					return err
				}
				if gw.Credentials, err = machineCredentials(ctx, r, e, "run", stderr); err != nil {
					return err
				}
				if gw.Tools, err = machineTools(r, e.Workspace); err != nil {
					return err
				}
			}
			// The files the settings name are known once the integrations are described.
			runner, named := runnerFiles()
			own := foragerFiles(foragerDir, state, spec.RunsDir, append(settingFiles(conf.Forager), runner...), named)
			spec.ForagerFiles = own.files
			record := filepath.Join(spec.RunsDir, spec.RunID)
			u := ui.New(stderr)
			// Forager's lines that say what qory's own line says of the run's end are held
			// back until qory knows whether it says it.
			lines := runLines(func(line string) { fmt.Fprintln(stderr, "qory run:", line) }, spec.RunID)
			spec.Report = lines.line
			// The run opens at its gateway first, then the harness is composed, then the
			// session joins, and the run closes with qory's outcome.
			var g *gateway.Gateway
			if remote {
				// The gateway the run key names: the session reaches it over TLS and sends the
				// run key on every request, once the connection is bound to a key the run
				// key names.
				spec.Gateway = key.remoteGateway()
				fmt.Fprintln(stderr, throughGatewayLine(key, spec.RunID))
				if fromCode != nil {
					fmt.Fprintln(stderr, fromCode.startLine(spec.RunID))
				}
			} else {
				// One gateway for the run, on this machine: the session speaks to it over
				// its local link, in this process's memory.
				gw.Report = lines.line
				var startErr error
				if g, startErr = startGateway(ctx, gw); startErr != nil {
					lines.done(nil)
					return explain(startErr, id)
				}
				defer g.Close(ctx)
				spec.Gateway = session.LocalGateway(g.LocalLink())
			}
			// closeGateway has the run's gateway deliver the run's last events, before qory
			// says how the run ended, and before qory exits.
			var delivery gateway.Delivery
			closeGateway := func() {
				if g == nil {
					return
				}
				var closeErr error
				if delivery, closeErr = g.Close(ctx); closeErr != nil {
					gw.Report(closeErr.Error())
				}
			}
			// failed says the error of a run that did not open, or whose session did not
			// start, and returns it.
			failed := func(err error) error {
				lines.done(nil)
				// A run id another run of this checkout has: refused before this run made
				// anything, so the folder of that id is the other run's, and goes unnamed.
				if reused := runIDReused(err, record, spec.RunID); reused != nil {
					return reused
				}
				// A --run-id that is not the run key's: the session refuses it, in its own
				// words, before anything starts, and the mistake is in the input.
				if remote && runID != "" && runID != key.key.RunID {
					return input(err)
				}
				if m := mountRefused(err, passed{foragerDir: foragerDir, stateDir: state, spec: &spec, root: at.root, own: own}); m != nil {
					err = m
				} else if used := runIDUsed(err, spec.RunID); used != nil {
					err = used
				} else {
					err = explain(err, id)
				}
				return withRecord(stderr, err, record)
			}
			run, err := openRun(ctx, spec.OpenSpec)
			if err != nil {
				closeGateway()
				if kept && nothingLeftToSend(record, nil) {
					dropKeptRunKey(spec.RunID, said)
				}
				return failed(err)
			}
			// The open listed a walled run among the walled runs going under its id, which
			// no other run can be listed under: the entry of that id is this run's.
			if walled {
				defer openingRun(spec.RunID)()
			}
			// The run's time limit counts from its open: the compose and the session.
			opened := time.Now()
			// closeRun closes the run with out, and returns what the close came to, nil when
			// it failed, which Forager's line says.
			closeRun := func(out session.Outcome) *session.Closed {
				closed, err := run.Close(ctx, out)
				if err != nil {
					lines.line(err.Error())
				}
				closeGateway()
				if kept && nothingLeftToSend(record, closed) {
					dropKeptRunKey(spec.RunID, said)
				}
				return closed
			}
			name := ""
			// prepareLaunch composes the harness into the home the session starts on and
			// makes the session's launch from it, writing the compose's lines to w;
			// claudeDispatched is true for a dispatched run of Claude Code.
			prepareLaunch := func(w io.Writer) (claudeDispatched bool, err error) {
				harness, workflows, err := composeForRun(w, composing, at, runtime, dispatched, walled)
				if err != nil && composing.git != nil && composing.git.Named != nil {
					return false, namedComposeError(err, composing.git.Named, fromCode.reference(), composing.checkoutGit.Proxy)
				}
				if err != nil && composing.git != nil {
					return false, proxyComposeError(err, composing.git.Proxy)
				}
				if err != nil {
					return false, err
				}
				rep, err := readReport(at)
				if err != nil {
					return false, err
				}
				var launch render.Launch
				if name, launch, err = resolveLaunch(rep, conf, runtime, at.home); err != nil {
					return false, err
				}
				claudeDispatched = dispatched && isClaude(name)
				if fromCode != nil {
					if extra, err = fromCode.workflowArgs(name, rep, in); err != nil {
						return false, err
					}
					// The time limit counts from the open, so the compose's time is in it.
					timeout = provisioner.TimeLimit(fromCode.minutes, workflows[fromCode.workflow].TimeLimit)
				}
				if dispatched {
					launch.Args = dispatchedArgs(name, launch.Args)
				}
				// Claude Code's settings go to Forager as their JSON, which qory read from the
				// home itself, after the home's check: Forager reads no file of the home.
				launchArgs := append(append([]string{}, launch.Args...), extra...)
				if isClaude(name) {
					if launchArgs, err = inlineSettings(launch.Args, extra, at.home); err != nil {
						return false, err
					}
				}
				rt, err := catalog.Lookup(name, filepath.Join(user, DescriptorsDir))
				if err != nil {
					return false, input(err)
				}
				// A walled run given a run key takes the model's credential through the
				// gateway its run key names alone, so neither --env nor wall.env passes it in.
				// An unwalled one, whose run key came by --run-key-fd, is as before.
				if dispatched && walled {
					var wallEnv []string
					if conf.Forager != nil && conf.Forager.Wall != nil {
						wallEnv = conf.Forager.Wall.Env
					}
					if err := modelCredentialPassed(rt, o.env, wallEnv); err != nil {
						return false, err
					}
				}
				spec.Runtime, spec.Command, spec.Args, spec.Declared, spec.Harness = rt, launch.Command, launchArgs, rep.Hosts(), harness
				spec.LaunchFixed, spec.LaunchDefaults, spec.HarnessHome = launch.Fixed, launch.Defaults, launch.HarnessHome
				if claudeDispatched && walled {
					spec.LaunchFixed = withAgentHome(spec.LaunchFixed)
				}
				return claudeDispatched, nil
			}
			// prepareSession makes the rest of the session's spec once its launch is made:
			// for a walled run the record of each place the container may write.
			prepareSession := func(claudeDispatched bool) error {
				if wrapsAgentEnv(claudeDispatched, walled) {
					done, err := wrapAgentEnv(&spec, said)
					if err != nil {
						return err
					}
					cleanups = append(cleanups, done)
				}
				// Every place the container may write is recorded before the session starts,
				// so qory reads what a run could have written there against what the place
				// held before, and held until the session has registered the run, from when
				// qory's checks find it.
				if walled {
					hold, err := recordPlaces(ctx, &spec, at.root, at)
					if err != nil {
						return err
					}
					cleanups = append(cleanups, hold.Release)
					registered := spec.OnVariables
					spec.OnVariables = func(a session.Applied) {
						hold.Release()
						if registered != nil {
							registered(a)
						}
					}
					// Forager ends the join before the runtime starts when it reads a file for
					// the run from any of those places, or reaches a mount or workspace through
					// a link inside one.
					if spec.Written, err = writtenPlaces(at.root, spec.Mounts); err != nil {
						return err
					}
				}
				return nil
			}
			// hostWork is the run's work on this machine before its session joins, in two
			// steps of the run's record: the fetch, when the run configuration lists
			// repositories, and the compose. When the run configuration lists repositories,
			// or the run was opened with a run key, the run's host proxy opens: the run's
			// repositories are fetched through it into the working folder, and the compose of
			// a run opened with a run key fetches its git sources through it, into the run's
			// cache. The compose of a named stack opens the run's stack proxy as well, and
			// fetches the stack and every git source it lists through it, with lines that
			// show names in place of addresses; the git sources the checkout's document lists
			// stay on the host proxy. Both close before the session joins; Join closes them
			// too. A run with no control plane opens neither, and composes with the person's
			// git.
			hostWork := func() error {
				stopped := func() bool {
					return ctx.Err() != nil || timeout > 0 && time.Since(opened) >= timeout
				}
				var h *hostProxy
				openProxy := func() error {
					p, err := run.HostProxy()
					if err != nil {
						return &runLineError{noHostProxy(err, code != nil)}
					}
					h = &p
					return nil
				}
				defer func() {
					if h != nil {
						h.close()
					}
				}()
				if run.Repositories() != "" {
					err := inStep(run, stderr, event.StepFetch, fetchStepName(runValues(run)), stopped, func(w io.Writer) error {
						if err := openProxy(); err != nil {
							return err
						}
						fetchCtx, cancel := ctx, func() {}
						if timeout > 0 {
							fetchCtx, cancel = context.WithDeadline(ctx, opened.Add(timeout))
						}
						defer cancel()
						if _, err := preparation.Run(fetchCtx, cwd, os.Environ(), runValues(run), h.proxy, w); err != nil {
							return &runLineError{err}
						}
						return nil
					})
					if err != nil {
						return err
					}
				}
				var claudeDispatched bool
				err := inStep(run, stderr, event.StepCompose, composeStepName(runtime), stopped, func(w io.Writer) error {
					if remote {
						if h == nil {
							if err := openProxy(); err != nil {
								return err
							}
						}
						composing.git = proxyGit(*h, w)
						composing.cache = filepath.Join(run.Dir(), runSources)
					}
					if remote && fromCode != nil && fromCode.named != nil {
						lines := &namedLines{w: w, n: fromCode.named}
						defer lines.flush()
						w = lines
						s, err := run.StackProxy()
						if err != nil {
							return &runLineError{noStackProxy(fromCode.named, err)}
						}
						defer s.close()
						composing.checkoutGit = proxyGit(*h, w)
						composing.git = stackGit(s, w, fromCode.named)
					}
					var err error
					claudeDispatched, err = prepareLaunch(w)
					return err
				})
				if err != nil {
					return err
				}
				return prepareSession(claudeDispatched)
			}
			// setupWork runs the setup of a walled run whose run configuration lists
			// repositories, see [runSetup], within the run's time limit. A setup command that
			// exits otherwise than 0 is the run's error line, and a mount refusal of the
			// setup container's places, or of the places AgentEnv checks, gets the line a
			// join's mount refusal gets.
			setupWork := func() error {
				setupCtx, cancel := ctx, func() {}
				if timeout > 0 {
					setupCtx, cancel = context.WithDeadline(ctx, opened.Add(timeout))
				}
				defer cancel()
				err := runSetup(setupCtx, run, &spec)
				var exited *session.SetupError
				switch {
				case err == nil:
					return nil
				case errors.As(err, &exited):
					return &runLineError{err}
				}
				if m := mountRefused(err, passed{foragerDir: foragerDir, stateDir: state, spec: &spec, root: at.root, own: own}); m != nil {
					return m
				}
				return err
			}
			// A run stopped or out of time before its session joins prepares nothing more.
			var prepErr error
			if ctx.Err() == nil && (timeout == 0 || time.Since(opened) < timeout) {
				prepErr = hostWork()
				if prepErr == nil && walled && run.Repositories() != "" {
					prepErr = setupWork()
				}
				if beforeJoin != nil {
					beforeJoin()
				}
			}
			left := time.Duration(0)
			if timeout > 0 {
				left = timeout - time.Since(opened)
			}
			switch {
			case ctx.Err() != nil:
				closeRun(session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1})
				lines.done(nil)
				u.Fail(errors.New(cancelledBeforeJoin(got(), name)))
				said("the record is in " + record)
				return reported(&exitError{code: 1})
			case timeout > 0 && left <= 0:
				closeRun(session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: exitTimeout})
				lines.done(nil)
				line := timedOutBeforeJoin(timeout, name)
				if fromCode != nil {
					line = fromCode.timedOutLine(timeout, name)
				}
				u.Fail(errors.New(line))
				said("the record is in " + record)
				return reported(&exitError{code: exitTimeout})
			case prepErr != nil:
				// A run no session joined is failed with no_session.
				closeRun(session.Outcome{State: event.StateFailed, ExitCode: -1})
				lines.done(nil)
				var line *runLineError
				if errors.As(prepErr, &line) {
					said(line.Error())
					if recorded(record) {
						said("the record is in " + record)
					}
					return reported(prepErr)
				}
				if errors.Is(prepErr, ErrReported) {
					said("the record is in " + record)
					return prepErr
				}
				return withRecord(stderr, prepErr, record)
			}
			spec.Timeout = left
			res, err := joinSession(run, ctx, spec.SessionSpec)
			closed := closeRun(sessionOutcome(res, err))
			if err != nil {
				return failed(err)
			}
			defer fmt.Fprintln(stderr, "qory run: the record is in", record)
			end := runEnd{runtime: name, timeout: timeout}
			if res.Cancelled {
				// qory run's context had ended when the session saw the runtime exit: the
				// signal qory got stopped the run, and is named. A signal that came later,
				// as the run closed or the run's last events were delivered, did not end the
				// run.
				end.got = got()
			}
			switch {
			case res.RunClosed:
				// The session's result has the end the gateway closed the run with.
			case delivery.RunClosed:
				// The gateway qory run started recorded the run's end after its runtime exited.
				end.gateway = &gatewayEnd{state: delivery.State, reason: delivery.Reason, code: delivery.ClosedReason}
			case closed != nil && closed.RunClosed:
				// The gateway the run key names ended the run after its runtime exited.
				end.gateway = &gatewayEnd{state: closed.State, reason: closed.Reason, code: closed.ClosedReason}
			}
			lines.done(func(kind int) bool {
				switch kind {
				case heldUndelivered:
					return delivery.Undelivered > 0
				case heldLimit:
					return res.TimedOut && !res.RunClosed
				case heldRunEnds:
					return res.RunClosed || end.gateway != nil && end.saysOutcome(res)
				}
				return false
			})
			if delivery.Undelivered > 0 {
				u.Fail(fmt.Errorf("%d events did not reach the server; %s/undelivered contains them", delivery.Undelivered, ui.Short(res.Dir, at.root)))
			}
			return runEnded(u, res, end)
		},
	}
	c.Flags().BoolVar(&local, "local", false, "run without the server: record to files, under the machine's policy")
	c.Flags().BoolVar(&relabel, relabelFlag, false, relabelUsage)
	c.Flags().BoolVar(&headless, "headless", false, "run on pipes even at a terminal; -p for claude implies it")
	c.Flags().StringVar(&policyFile, "policy", "", "this run's policy file, kept outside the checkout; it narrows gateway.egress of "+config.ForagerFileName+", never widens it (with a server: needs --local)")
	c.Flags().StringVar(&runID, "run-id", "", "the run's id, a UUID in lower case (default a new one)")
	c.Flags().StringArrayVar(&labels, "label", nil, "a key=value name for the run, reported in its events; repeatable (forge and repository come from the origin remote)")
	c.Flags().StringVar(&kind, "kind", "", "what kind of run it is, such as review or fix, reported when the run starts")
	c.Flags().StringVar(&title, "title", "", "the run's title, for a person to read, reported when the run starts")
	c.Flags().StringArrayVar(&subjects, "subject", nil, "what the run works on, type=<type>,ref=<ref>[,url=<url>][,title=<title>], such as type=ticket,ref=7; title takes the rest of the value, commas too; repeatable")
	c.Flags().StringVar(&details, "details", "", "a JSON object of the run's details, read from this file, or - for stdin when stdin is not a terminal; at most 8192 bytes compacted, 4 levels deep")
	c.Flags().DurationVar(&timeout, "timeout", 0, "stop the run after this long, counted from its open, compose included, such as 5h30m, and exit "+fmt.Sprint(exitTimeout)+" (default no limit; "+config.ForagerFileName+": session.run.timeout)")
	c.Flags().StringVar(&stopSignal, "stop-signal", "", "the signal that stops the agent: SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1 or SIGUSR2 (default SIGTERM; "+config.ForagerFileName+": session.run.stop_signal)")
	c.Flags().DurationVar(&grace, "stop-grace", 0, "the time between the stop signal and SIGKILL (default 10s; "+config.ForagerFileName+": session.run.stop_grace)")
	c.Flags().StringVar(&o.name, "wall", "", "run the agent in a container whose one way out is the proxy: "+config.WallDocker+", or none ("+config.ForagerFileName+": wall.adapter)")
	c.Flags().StringVar(&o.image, "image", "", "the container's image unless the run's policy selects one: a name of wall.images, or a reference ("+config.ForagerFileName+": wall.image)")
	c.Flags().StringArrayVar(&o.env, "env", nil, "a variable of this shell to pass to the agent, by name, with a wall or without; it wins over wall.env of "+config.ForagerFileName+"; repeatable")
	c.Flags().StringArrayVar(&o.mounts, "mount", nil, "a path of this machine the container sees too, :ro for read-only; repeatable ("+config.ForagerFileName+": wall.mounts)")
	c.Flags().StringVar(&o.limits.CPUs, "cpus", "", "how many CPUs the container gets, such as 1.5 ("+config.ForagerFileName+": wall.cpus)")
	c.Flags().StringVar(&o.limits.Memory, "memory", "", "the most memory the container gets, such as 8g ("+config.ForagerFileName+": wall.memory)")
	c.Flags().IntVar(&o.limits.PIDs, "pids-limit", 0, "the most processes and threads in the container ("+config.ForagerFileName+": wall.pids_limit)")
	c.Flags().IntVar(&secretFD, secretFDFlag, 0, secretFDUsage)
	c.Flags().IntVar(&keyFD, runKeyFDFlag, 0, runKeyFDUsage)
	c.Flags().StringVar(&codeFile, runCodeFileFlag, "", runCodeFileUsage)
	c.Flags().StringVar(&gatewayURL, gatewayFlag, "", gatewayUsage)
	c.Flags().StringVar(&o.limits.ShmSize, "shm-size", "", "the size of /dev/shm in the container, such as 2g ("+config.ForagerFileName+": wall.shm_size)")
	homeFlags(c, &h)
	composeFlags(c, &composing)
	c.AddCommand(newResend(), newRegister(), newEnd(), newForward(), newRelay(), newNest(), newPrepare(), newAgentEnv(), newExec(), newHold(), newSetupCommand(), newSeed(), newPool())
	return c
}

// machinePolicy is the policy forager.yaml's gateway.egress sets for every run of the
// machine's gateway, nil when it has none.
func machinePolicy(r *config.Forager) *gateway.Policy {
	if r.Egress == nil {
		return nil
	}
	return &gateway.Policy{Version: 1, Egress: gateway.PolicyEgress{Mode: r.Egress.Mode, Allow: r.Egress.Allow, Deny: r.Egress.Deny, Paths: r.Egress.Paths}}
}

// machineCredentials describes the integrations forager.yaml declares as e selects, and
// returns the credentials the machine's gateway holds: the file's own and the ones the
// integrations define. It prints, after "qory <verb>:", a line for each credential
// gateway.credentials shadows and for each integration described. An adapter's program
// is found as [config.Program] finds it, and the gateway is given the path it checked.
func machineCredentials(ctx context.Context, r *config.Forager, e config.Expansion, verb string, report io.Writer) ([]gateway.Credential, error) {
	for _, key := range r.Shadowed() {
		fmt.Fprintf(report, "qory %s: %s\n", verb, shadowed(key))
	}
	if err := r.Expand(ctx, e); err != nil {
		return nil, input(err)
	}
	for _, in := range r.Integrations {
		if in.Path != "" {
			// The program's path contains the key, qory-<key>, unless forager.yaml sets
			// another program.
			fmt.Fprintf(report, "qory %s: integration %s: %s %s\n", verb, accesskey.ShownName(in.Key, false), accesskey.ShownName(in.Path, false), exports.Value(in.Version, false))
		}
	}
	var out []gateway.Credential
	for _, c := range r.Credentials {
		def := gateway.Credential{Name: c.Name, Env: c.Env, File: c.File, Adapter: c.Adapter, Argument: c.Argument, Hosts: c.Hosts, Scheme: c.Scheme, Username: c.Username, Header: c.Header, Paths: c.Paths, Placeholders: c.Placeholders,
			Format: c.Format, Endpoint: c.Endpoint, OAuth: c.OAuth, KeyPrefixes: c.KeyPrefixes}
		if err := def.Check(); err != nil {
			return nil, input(foragerRefusal(c.Name, err))
		}
		if c.Integration == "" && len(c.Adapter) > 0 {
			// The gateway runs the adapter by the path checked here.
			found, err := config.Program(c.Adapter[0], e.Workspace, "an adapter")
			if err != nil {
				return nil, input(fmt.Errorf("%s: gateway.credentials.%s.adapter: %w", config.ForagerFileName, accesskey.ShownName(c.Name, false), exports.HideValueError(err, c.Adapter[0])))
			}
			def.Adapter = append([]string{found}, c.Adapter[1:]...)
		}
		out = append(out, def)
	}
	return out, nil
}

// gatewayRunLimits is gateway.run_limits of r as Forager's gateway takes it: a field r
// does not set is zero, Forager's default.
func gatewayRunLimits(r *config.Forager) gateway.RunLimits {
	return gateway.RunLimits{Connections: r.RunLimits.Connections, RequestsPerSecond: r.RunLimits.RequestsPerSecond, Burst: r.RunLimits.Burst}
}

// foragerRefusal is err, Forager's refusal of the definition of the credential or the
// tool name, after forager.yaml's name. When name contains an access key secret or a
// run key, each word of err that contains name prints as [accesskey.ShownName] prints
// it.
func foragerRefusal(name string, err error) error {
	if accesskey.ContainsSecret(name) {
		return fmt.Errorf("%s: %s", config.ForagerFileName, exports.HideName(err.Error(), name))
	}
	return fmt.Errorf("%s: %w", config.ForagerFileName, err)
}

// machineTools returns the tools forager.yaml defines, the machine's gateway's, each one
// Forager's check takes: a definition it refuses is an input error, in its words. A
// tool's program is found as [config.Program] finds it, and the gateway is given the
// path it checked: one in workspace, the places a run may write, or one the PATH does
// not find, is refused.
func machineTools(r *config.Forager, workspace []string) ([]gateway.Tool, error) {
	var out []gateway.Tool
	for _, t := range r.Tools {
		def := gateway.Tool{Name: t.Name, Command: t.Command, Argument: t.Argument, Serves: t.Serves, Placeholders: t.Placeholders}
		if err := def.Check(); err != nil {
			return nil, input(foragerRefusal(t.Name, err))
		}
		if len(t.Command) > 0 {
			// The gateway runs the tool by the path checked here.
			found, err := config.Program(t.Command[0], workspace, "a tool")
			if err != nil {
				return nil, input(fmt.Errorf("%s: gateway.tools.%s.command: %w", config.ForagerFileName, accesskey.ShownName(t.Name, false), exports.HideValueError(err, t.Command[0])))
			}
			def.Command = append([]string{found}, t.Command[1:]...)
		}
		out = append(out, def)
	}
	return out, nil
}

// runHeartbeat is the heartbeat interval of a run's gateway, Forager's default: the
// session sends one every interval, and the gateway ends a run whose session sends
// nothing for three (gateway.Config.Heartbeat). It is [config.Heartbeat], which qory
// gateway uses too.
const runHeartbeat = config.Heartbeat

// runQuiet is how long a run's session may send its gateway nothing before the gateway
// ends the run, session_lost.
const runQuiet = 3 * runHeartbeat

// socketGrace is how long a run's socket keeps accepting after the runtime exits,
// session.SessionSpec.SocketGrace: zero, which is Forager's 200 ms. Only the tests
// shorten it.
var socketGrace time.Duration

// runSpec is what qory run gives Forager: the open's spec of the run, and the spec of
// the session that joins it.
type runSpec struct {
	session.OpenSpec
	session.SessionSpec
	// setupWhere is where a walled run's setup commands run, wall.setup:
	// [config.SetupOutside] or [config.SetupInside]; empty without a wall.
	setupWhere string
	// given are the mounts of a walled run that wall.mounts and --mount add, in
	// spec.Mounts too; none without a wall.
	given []givenMount
}

// openedRun is a run open at its gateway, as qory run uses it: a [*session.Run], as
// [foragerRun] gives it.
type openedRun interface {
	Join(ctx context.Context, spec session.SessionSpec) (*session.Result, error)
	// Setup runs the run's setup commands in the setup container, before the join.
	Setup(ctx context.Context, spec session.SetupSpec) error
	Close(ctx context.Context, out session.Outcome) (*session.Closed, error)
	// StartStep starts a step of the run's host work, of kind fetch or compose.
	StartStep(kind, name string) (hostStep, error)
	// HostProxy starts the run's host proxy, for the fetches on this machine.
	HostProxy() (hostProxy, error)
	// StackProxy starts the run's stack proxy, for the fetch of a named stack on this
	// machine, before the run's first Join.
	StackProxy() (hostProxy, error)
	// AgentEnv is the environment a session of spec gets, NAME=value sorted by name,
	// resolved as Join resolves it; qory run gives it to the setup container outside.
	AgentEnv(spec session.SessionSpec) ([]string, error)
	// System and Repositories are the server's values of [preparation.EnvSystem] and
	// [preparation.EnvRepositories], "" when it sets none; the fetch on this machine
	// reads them.
	System() string
	Repositories() string
	// Dir is the run directory.
	Dir() string
}

// hostProxy is the run's host proxy as qory run uses it: the proxy git fetches through,
// and what ends it.
type hostProxy struct {
	proxy proxygit.Proxy
	close func() error
}

// foragerRun is a [*session.Run] as an [openedRun].
type foragerRun struct{ *session.Run }

// StartStep is [session.Run.StartStep].
func (r foragerRun) StartStep(kind, name string) (hostStep, error) {
	s, err := r.Run.StartStep(kind, name)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// HostProxy is [session.Run.HostProxy]: its ProxyURL and CABundle, and its Close.
func (r foragerRun) HostProxy() (hostProxy, error) {
	h, err := r.Run.HostProxy()
	if err != nil {
		return hostProxy{}, err
	}
	return hostProxy{proxy: proxygit.Proxy{URL: h.ProxyURL, CABundle: h.CABundle}, close: h.Close}, nil
}

// StackProxy is [session.Run.StackProxy]: its ProxyURL, and its Close. Its CABundle is
// empty: the stack proxy passes the machine's authorities alone, and [proxygit.Env]
// drops SSL_CERT_FILE and GIT_SSL_*. So a named stack's host needs a certificate that
// the run machine's system trust store accepts. For a private CA, the instance admin
// adds the CA to the machine's image.
func (r foragerRun) StackProxy() (hostProxy, error) {
	h, err := r.Run.StackProxy()
	if err != nil {
		return hostProxy{}, err
	}
	return hostProxy{proxy: proxygit.Proxy{URL: h.ProxyURL, CABundle: h.CABundle}, close: h.Close}, nil
}

// openRun opens a run at its gateway, and joinSession runs a session in it; a test
// replaces openRun to open a run that reaches no gateway, and joinSession to read the
// session's spec.
var (
	openRun = func(ctx context.Context, spec session.OpenSpec) (openedRun, error) {
		r, err := session.Open(ctx, spec)
		if err != nil {
			return nil, err
		}
		return foragerRun{r}, nil
	}
	joinSession = func(r openedRun, ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		return r.Join(ctx, spec)
	}
)

// beforeJoin, when not nil, is called once the harness is composed, before the run's
// session joins; a test sets it.
var beforeJoin func()

// sessionOutcome is the run's outcome qory closes the run with once its one session
// ended with res, or did not start, err: the time limit's, cancelled with timeout and
// 124; a stop from where the run was started, cancelled with interrupted and the
// runtime's exit code; otherwise succeeded or failed by the session's state, with the
// runtime's exit code. A session that did not start, or whose run the gateway closed,
// is failed with -1: Forager closes a run the gateway closed with the gateway's end.
func sessionOutcome(res *session.Result, err error) session.Outcome {
	switch {
	case err != nil || res == nil || res.RunClosed:
		return session.Outcome{State: event.StateFailed, ExitCode: -1}
	case res.TimedOut:
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: exitTimeout}
	case res.Cancelled:
		return session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: res.ExitCode}
	case res.State == event.StateSucceeded:
		return session.Outcome{State: event.StateSucceeded, ExitCode: res.ExitCode}
	}
	return session.Outcome{State: event.StateFailed, ExitCode: res.ExitCode}
}

// cancelledBeforeJoin is the line of a run that qory run's signal, got, stopped before
// its session joined, and runtime the agent's name, "" when it is not known yet.
func cancelledBeforeJoin(got, runtime string) string {
	if got == "" {
		return "the run was cancelled before " + agentName(runtime) + " started"
	}
	return "the run was cancelled: qory run got " + got + " before " + agentName(runtime) + " started"
}

// timedOutBeforeJoin is the line of a run that reached its time limit, limit, before
// its session joined, and runtime the agent's name, "" when it is not known yet.
func timedOutBeforeJoin(limit time.Duration, runtime string) string {
	return fmt.Sprintf("give the run a longer --timeout: the run was cancelled at its time limit of %s, before %s started", limit, agentName(runtime))
}

// agentName is the runtime's name as a line says it, "the agent" when it is not known.
func agentName(runtime string) string {
	if runtime == "" {
		return "the agent"
	}
	return accesskey.ShownName(runtime, false)
}

// withRecord says err of a run that has its record at record, on stderr, with the line
// that names the record after it, and returns err marked as printed. When record is
// not there, the run made nothing, and err comes back as it is, for the caller to print.
func withRecord(stderr io.Writer, err error, record string) error {
	if _, statErr := os.Stat(record); statErr != nil {
		return err
	}
	u := ui.New(stderr)
	if !ui.Marked() {
		u.Title("qory")
	}
	u.Fail(err)
	fmt.Fprintln(stderr, "qory run: the record is in", record)
	return reported(err)
}

// runEnd is what qory knows of a run besides its result: its runtime's name, its time
// limit, the signal qory run got that ended the run, "" for none, and the run's end its
// gateway recorded after the runtime exited by itself, nil when it recorded none: the
// end the gateway qory run started recorded, or the end of a separate gateway that
// closed the run before qory run closed it, such as the run's starter's outcome.
type runEnd struct {
	runtime string
	timeout time.Duration
	got     string
	gateway *gatewayEnd
}

// gatewayEnd is the end of a run its gateway recorded: the state and the reason of its
// dev.qory.run.exited, and the code of the gateway's 410.
type gatewayEnd struct {
	state, reason, code string
}

// exitOutcome is the outcome said beside the runtime's own exit: the end the run's
// gateway recorded after the runtime exited, when it recorded one, which is what the
// server shows; otherwise the session's state and reason. An end the gateway recorded
// with no state is the outcome of its code.
func (e runEnd) exitOutcome(res *session.Result) outcome {
	if e.gateway == nil {
		return outcomeOf(res.State, res.Reason)
	}
	o := outcomeOf(e.gateway.state, e.gateway.reason)
	if !o.known {
		o = outcomeOf("", e.gateway.code)
	}
	return o
}

// cancelled reports whether the run was cancelled by a signal qory run got: the session
// saw the runtime exit after qory run's context ended, and the signal is known. A
// context that ended otherwise names no signal, and the runtime's own exit says the run.
func (e runEnd) cancelled(res *session.Result) bool {
	return res.Cancelled && e.got != ""
}

// saysOutcome reports whether qory says the run's outcome in a line after the runtime's
// own: the runtime exited by itself, and the outcome differs from what its exit says.
func (e runEnd) saysOutcome(res *session.Result) bool {
	if res.RunClosed || res.TimedOut || e.cancelled(res) {
		return false
	}
	o := e.exitOutcome(res)
	return o.known && !o.agrees(res.Signal == "" && res.ExitCode == 0)
}

// runEnded says how a run that started ended, in one line, and returns qory run's exit
// status. A run the gateway closed ends with its outcome and reason ([outcomeOf]): 0 when
// it completed, else 1. A run stopped at its time limit was cancelled, exit 124;
// one whose context a signal of qory run's had ended when the session saw the runtime
// exit, res.Cancelled, was cancelled, exit 1. Otherwise the
// runtime's own exit says it, with its status, or 1 for a signal; when the outcome
// differs from what the exit says, a second line says the outcome, and the exit status
// follows it. That outcome is the end the run's gateway recorded after the runtime
// exited ([runEnd.exitOutcome]).
func runEnded(u *ui.UI, res *session.Result, e runEnd) error {
	say := func(o outcome, text string) {
		if o.completed {
			u.Success("%s", text)
		} else {
			u.Fail(errors.New(text))
		}
	}
	switch {
	case res.RunClosed:
		o := outcomeOf(res.State, res.Reason)
		say(o, o.said()+", and "+accesskey.ShownName(e.runtime, false)+" was stopped")
		return reported(&exitError{code: o.exitCode()})
	case res.TimedOut:
		u.Fail(fmt.Errorf("the run was cancelled: it reached the time limit of %s, and %s was stopped", e.timeout, accesskey.ShownName(e.runtime, false)))
		return reported(&exitError{code: exitTimeout})
	case e.cancelled(res):
		u.Fail(fmt.Errorf("the run was cancelled: qory run got %s, and %s was stopped", e.got, accesskey.ShownName(e.runtime, false)))
		return reported(&exitError{code: 1})
	}
	code := res.ExitCode
	switch {
	case res.Signal != "":
		u.Fail(fmt.Errorf("%s ended on the signal %s", accesskey.ShownName(e.runtime, false), res.Signal))
		code = 1
	case res.ExitCode != 0:
		u.Fail(fmt.Errorf("%s exited %d", accesskey.ShownName(e.runtime, false), res.ExitCode))
	default:
		u.Success("%s exited 0", accesskey.ShownName(e.runtime, false))
	}
	if e.saysOutcome(res) {
		o := e.exitOutcome(res)
		say(o, o.said())
		code = o.exitCode()
	}
	if code == 0 {
		return nil
	}
	return reported(&exitError{code: code})
}

// signalContext is parent until qory gets SIGINT or SIGTERM; got names the signal it got,
// "" before one. stop ends the watch, and a signal after it acts as it would without.
func signalContext(parent context.Context) (ctx context.Context, got func() string, stop func()) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case s := <-signals:
			cancel(gotSignal{name: signalName(s)})
		case <-done:
		}
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			signal.Stop(signals)
			close(done)
			cancel(context.Canceled)
		})
	}
	got = func() string {
		var g gotSignal
		if errors.As(context.Cause(ctx), &g) {
			return g.name
		}
		return ""
	}
	return ctx, got, stop
}

// gotSignal is the cause of qory run's context ending at a signal it got.
type gotSignal struct{ name string }

func (g gotSignal) Error() string { return "qory run got " + g.name }

// signalName is the name of a signal qory run watches, as qory says it.
func signalName(s os.Signal) string {
	if s == syscall.SIGTERM {
		return "SIGTERM"
	}
	return "SIGINT"
}

// runIDUsed is the gateway's refusal of a run whose id a run on this machine has
// already used, a --run-id given again, said for the person; nil for any other error.
// It unwraps to the refusal.
func runIDUsed(err error, runID string) error {
	var ref *session.Refusal
	if !errors.As(err, &ref) || ref.Code != refusal.RunIDUsed {
		return nil
	}
	return &saidError{text: runIDUsedText(runID), err: err}
}

// runIDReused is the session's refusal of a run whose record, record, holds the
// events of another run already: the open of record/events.jsonl that exists, said as
// the gateway's run_id_used is; nil for any other error. It unwraps to the session's
// error.
func runIDReused(err error, record, runID string) error {
	var pe *fs.PathError
	if !errors.As(err, &pe) || !errors.Is(err, fs.ErrExist) || pe.Path != filepath.Join(record, sink.EventsFile) {
		return nil
	}
	return &saidError{text: runIDUsedText(runID), err: err}
}

// runIDUsedText says a run id is another run's, and how to go on.
func runIDUsedText(runID string) string {
	return fmt.Sprintf("the run id %s is already used by another run; leave out --run-id, or give a new one", runID)
}

// saidError is an error qory words itself in full, and that unwraps to the one it
// replaces.
type saidError struct {
	text string
	err  error
}

func (e *saidError) Error() string { return e.text }

func (e *saidError) Unwrap() error { return e.err }

// expansion is what a run describes of the integrations forager.yaml declares. A
// policy this process has lists the credentials the run selects, so the integrations it
// selects are the ones described; a policy the server supplies arrives once
// Forager starts, so every declared integration is described before it does. An
// integration whose key the credentials section defines itself defines nothing for the
// run and is not described. A program the run may write is refused: one in the
// checkout, in the working directory when that is inside the checkout, or in a
// read-write mount of the wall's container, or in a place a walled run has used.
func expansion(r *config.Forager, pol *gateway.Policy, fromServer bool, root, cwd string, mounts []wall.Mount) (config.Expansion, error) {
	recorded, err := recordedPlaces()
	if err != nil {
		return config.Expansion{}, err
	}
	e := config.Expansion{Workspace: append([]string{root}, recorded...)}
	if cwd != root && reallyWithin(root, cwd) {
		e.Workspace = append(e.Workspace, cwd)
	}
	for _, m := range mounts {
		if !m.ReadOnly {
			e.Workspace = append(e.Workspace, m.Path)
		}
	}
	shadowed := r.Shadowed()
	selected := map[string]bool{}
	if pol != nil {
		for _, c := range pol.Credentials {
			selected[c.Name] = true
		}
	}
	e.Only = func(key string) bool {
		return !slices.Contains(shadowed, key) && (fromServer || selected[key])
	}
	return e, nil
}

// shadowed is the line that reports that the credentials section defines the credential
// an integration of the same key defines otherwise. A key that contains an access key
// secret or a run key is printed as [accesskey.ShownName] prints it.
func shadowed(key string) string {
	key = accesskey.ShownName(key, false)
	return fmt.Sprintf("%s: gateway.credentials.%s defines the credential %s, and gateway.integrations.%s defines none", config.ForagerFileName, key, key, key)
}

// wallOptions are the run verb's wall flags.
type wallOptions struct {
	name   string
	image  string
	env    []string
	mounts []string
	limits wall.Limits
}

// walled reports whether a flag that means something only behind a wall was given:
// --env is the run's own variables, with a wall or without.
func (o wallOptions) walled() bool {
	return o.image != "" || len(o.mounts) > 0 || o.limits != (wall.Limits{})
}

// exitTimeout is the exit status of a run stopped at its --timeout, timeout(1)'s.
const exitTimeout = 124

// runPolicy reads one run's own policy and puts it under the machine's. The file is
// kept outside the checkout, as forager.yaml is: inside, the agent it constrains
// could write it.
func runPolicy(file, root string, machine *gateway.Policy) (*gateway.Policy, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, input(err)
	}
	if reallyWithin(root, abs) {
		return nil, input(fmt.Errorf("--policy %s is inside the checkout, where the agent it constrains could write it; keep it outside", exports.Value(file, false)))
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, input(exports.HideError(err))
	}
	p, err := gateway.ReadPolicy(filepath.Base(abs), b)
	if err != nil {
		return nil, input(exports.HideError(err))
	}
	return p.Under(machine), nil
}

// parseLabels reads --label key=value; Forager checks what a key and a value may be.
func parseLabels(labels []string) (map[string]string, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, l := range labels {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return nil, input(fmt.Errorf("--label %s is not key=value", exports.Value(l, false)))
		}
		if _, dup := out[k]; dup {
			return nil, input(fmt.Errorf("--label %s is set twice", accesskey.ShownName(k, false)))
		}
		out[k] = v
	}
	return out, nil
}

// withOrigin adds the labels the checkout's origin remote gives, origin as
// [originLabels] read it, to the caller's, which win: a caller that names them knows
// better than the remote, and one that names neither gets what [checkout.Origin] reads.
// A checkout with no origin, or one on this machine, adds nothing: [checkout.Origin]
// gives both labels or neither.
func withOrigin(labels map[string]string, origin foragerdir.Labels) map[string]string {
	if origin["forge"] == "" {
		return labels
	}
	out := map[string]string{"forge": origin["forge"], "repository": origin["repository"]}
	for k, v := range labels {
		out[k] = v
	}
	return out
}

// wallOff is the --wall value that runs without the wall forager.yaml sets.
const wallOff = "none"

// enclose puts the spec behind a wall when a flag or forager.yaml sets one, and sets
// the run's own variables, --env, with a wall or without. The runtime then runs in a
// container, so what refers to this machine changes: it inherits nothing of the
// process's environment, the machine's variables, wall.env, go in beside the run's, and
// the forwarder is the helper's path inside the container. The launch's variables, its
// fixed ones, its defaults and its home, go to Forager behind a wall or not: the
// checkout, the composed home, which is all a launch template's paths point into, and
// the mounts keep their paths inside the container. home is the one qory computes for
// the checkout, see [ownHome], never one a report names. wall.ca_env goes to Forager
// with a wall or without.
//
// The images wall.images defines go to Forager, which reads the default, --image or
// wall.image, as the name of one of them first and as a reference otherwise, and
// starts the one the policy selects instead. The prefixes of wall.images_allow go to
// Forager too: it starts an image a policy names by a reference pinned by digest only
// under one of them, and under the default's runtime, so a list needs a default.
// selected is the image the policy this process holds selects, empty when it selects
// none: qory refuses a selection by name Forager would refuse before the run, so it is
// an input error, and leaves a reference to Forager. A run whose policy selects an
// image needs no default unless wall.images_allow lists prefixes. fromServer says the
// server's run configuration is the policy; it arrives once the run starts, and may
// select no image, so such a run needs a default.
func enclose(spec *runSpec, r *config.Forager, o wallOptions, selected string, fromServer bool, exe, root, home string) error {
	var section config.ForagerWall
	if r != nil && r.Wall != nil {
		section = *r.Wall
	}
	name := o.name
	if name == "" {
		name = section.Adapter
	}
	run, err := passedVariables(o.env)
	if err != nil {
		return err
	}
	spec.Variables.Run = run
	if name == "" || name == wallOff {
		if o.walled() {
			return input(fmt.Errorf("--image, --mount and the limits are for a run behind a wall; --wall %s starts one", config.WallDocker))
		}
		if selected != "" {
			return input(fmt.Errorf("the policy selects the image %s, which needs a wall: wall in %s, or --wall %s", exports.Value(selected, false), config.ForagerFileName, config.WallDocker))
		}
		// The variables that point the run's programs at the bundle Forager writes in
		// the run directory when the gateway gives the run an authority; nil is
		// Forager's defaults, as for the wall.
		spec.CAEnv = section.CAEnv
		return nil
	}
	if name != config.WallDocker {
		return input(fmt.Errorf("--wall %s: the walls are %s, and %s for a run without one", exports.Value(name, false), config.WallDocker, wallOff))
	}
	if selected != "" && !policy.ImageReference(selected) && !section.Defines(selected) {
		return input(fmt.Errorf("the policy selects the image %s, which wall.images in %s does not define", exports.Value(selected, false), config.ForagerFileName))
	}
	spec.Image = o.image
	if spec.Image == "" {
		spec.Image = section.Image
	}
	if spec.Image == "" && selected == "" {
		err := fmt.Errorf("a wall needs the container's image: --image, or wall.image in %s, a name of wall.images or a reference", config.ForagerFileName)
		if fromServer {
			err = fmt.Errorf("%w; with a server, set one even when its run configuration selects an image: that arrives once the run starts, and may select none", err)
		}
		err = fmt.Errorf("%w; to build Qory's and check yours, see https://github.com/qoryai/qory/blob/main/docs/run.md#qorys-images", err)
		return input(err)
	}
	if len(section.ImagesAllow) > 0 && spec.Image == "" {
		return input(fmt.Errorf("wall.images_allow in %s needs a default image, which an image by reference starts under: --image, or wall.image", config.ForagerFileName))
	}
	spec.ImagesAllow = section.ImagesAllow
	for _, i := range section.Images {
		spec.Images = append(spec.Images, i.Session())
	}
	machine, err := passedVariables(section.Env)
	if err != nil {
		return err
	}
	var more []wall.Mount
	for _, m := range section.Mounts {
		more = append(more, wall.Mount{Path: m.Path, ReadOnly: m.ReadOnly})
		spec.given = append(spec.given, givenMount{path: m.Path, from: "wall.mounts in " + config.ForagerFileName})
	}
	for _, v := range o.mounts {
		m, err := config.ParseMount(v)
		if err != nil {
			return input(fmt.Errorf("--mount: %w", exports.HideValueError(err, v)))
		}
		if _, err := os.Stat(m.Path); err != nil {
			return input(fmt.Errorf("--mount: %w", exports.HideError(err)))
		}
		more = append(more, wall.Mount{Path: m.Path, ReadOnly: m.ReadOnly})
		spec.given = append(spec.given, givenMount{path: m.Path, from: "--mount"})
	}
	helper, err := wallHelper(section, exe)
	if err != nil {
		return err
	}
	spec.Env = []string{}
	spec.Variables.Machine = machine
	spec.Wall = &wall.Docker{Command: section.Command, Helper: helper, RelayArgs: relayArgs, NestArgs: nestArgs, HoldArgs: holdArgs, User: section.User, CAEnv: section.CAEnv}
	spec.Forwarder = append([]string{wall.HelperPath}, forwardArgs...)
	// Forager starts the launch through the exec verb, which unsets the names nothing
	// sets for the run before the launch starts; a walled run without it is refused.
	spec.Exec = append([]string{wall.HelperPath}, execArgs...)
	// The setup commands of a run whose run configuration lists repositories run
	// outside, in the setup container, unless wall.setup is inside: see [runSetup]. Only
	// a run from a control plane gets repositories, so only its run.started has setup.
	spec.setupWhere = section.Setup
	if spec.setupWhere == "" {
		spec.setupWhere = config.SetupOutside
	}
	if fromServer {
		spec.OpenSpec.Setup = setupWhere(spec.setupWhere)
	}
	spec.Mounts = []wall.Mount{{Path: root}}
	if !reallyWithin(root, home) {
		spec.Mounts = append(spec.Mounts, wall.Mount{Path: home, ReadOnly: true})
	}
	spec.Mounts = append(spec.Mounts, more...)
	spec.Limits = wall.Limits{CPUs: section.CPUs, Memory: section.Memory, PIDs: section.PIDs, ShmSize: section.ShmSize}
	if o.limits.CPUs != "" {
		spec.Limits.CPUs = o.limits.CPUs
	}
	if o.limits.Memory != "" {
		spec.Limits.Memory = o.limits.Memory
	}
	if o.limits.PIDs != 0 {
		spec.Limits.PIDs = o.limits.PIDs
	}
	if o.limits.ShmSize != "" {
		spec.Limits.ShmSize = o.limits.ShmSize
	}
	return nil
}

// unusedEnv is what a run does once Forager has resolved its variables: it prints a
// line for each --env value that another source's value or a rule left out, saying
// which. serverURL is the server's, which names the host whose value won, empty for
// none.
func unusedEnv(report io.Writer, serverURL string) func(session.Applied) {
	host := "the server"
	if u, err := url.Parse(serverURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return func(applied session.Applied) {
		for _, v := range applied {
			for _, l := range v.Lost {
				if l.From != session.FromRun {
					continue
				}
				fmt.Fprintf(report, "qory run: %s from --env is not used: %s\n", accesskey.ShownName(v.Name, false), whyUnused(v.From, l.Why, host))
			}
		}
	}
}

// whyUnused says why a value of --env was left out: why is Forager's reason, and from
// the source whose value the run applies instead, host naming the server's.
func whyUnused(from, why, host string) string {
	switch why {
	case session.WhyDenied:
		return "no source may set it"
	case session.WhyFixed:
		return "the harness sets it"
	case session.WhyOverridden:
		switch from {
		case session.FromApiary:
			return host + " sets it"
		case session.FromFixed:
			return "the harness sets it"
		}
		return "the " + from + " value wins"
	}
	return "Forager left it out (" + why + ")"
}

// passed is what qory passed Forager that its refusal of a mount names: foragerDir,
// the configuration directory, absolute, "" for none; stateDir, qory's state directory;
// the spec, whose RunsDir, Mounts and Dir the refusal may name; root, the checkout,
// which is the workspace's root in Mounts behind a wall; and own, Forager's files
// qory passed, with the links among them.
type passed struct {
	foragerDir, stateDir string
	spec                 *runSpec
	root                 string
	own                  ownFiles
}

// workspace reports whether path is the workspace: the checkout root as Mounts holds
// it, or Dir.
func (p passed) workspace(path string) bool {
	if path == p.spec.Dir {
		return true
	}
	return path == p.root && slices.ContainsFunc(p.spec.Mounts, func(m wall.Mount) bool { return m.Path == p.root })
}

// ofRun reports whether path is one of the run's places: the workspace, or a mount.
func (p passed) ofRun(path string) bool {
	return p.workspace(path) || slices.ContainsFunc(p.spec.Mounts, func(m wall.Mount) bool { return m.Path == path })
}

// place is how a refusal names one of the run's places: the workspace, or a mount.
func (p passed) place(path string) string {
	if p.workspace(path) {
		return "the workspace " + path
	}
	return "the mount " + path
}

// placeShown is [passed.place] with path printed through hostgit.Shown.
func (p passed) placeShown(path string) string {
	if p.workspace(path) {
		return "the workspace " + hostgit.Shown(path)
	}
	return "the mount " + hostgit.Shown(path)
}

// modes is how the run passed path: writable, read-only, or both, from the entries of
// Mounts with that path, and Dir, writable. changed is, for a path passed with both,
// the mode of the first entry whose mode differs from an earlier one's, in the order
// Forager checks them, Mounts in order and then Dir: the entry it refuses.
func (p passed) modes(path string) (writable, readOnly, changed bool) {
	add := func(w bool) {
		if writable != readOnly && w != writable {
			changed = w
		}
		writable, readOnly = writable || w, readOnly || !w
	}
	for _, m := range p.spec.Mounts {
		if m.Path == path {
			add(!m.ReadOnly)
		}
	}
	if path == p.spec.Dir {
		add(true)
	}
	return writable, readOnly, changed
}

// mountRefused words Forager's refusals of the run's places behind a wall for the
// person, each before the run starts. Any other error is nil here.
//
// mount_contains_forager_files is a place that is, contains or lies inside one of
// Forager's files. A refusal of the configuration directory says the agent could read
// the access key when access-key-secret is there and the place is or contains it; one
// of the runs directory or of qory's state directory, that it could read the run
// records through a read-only mount and change them through any other; one of a file a
// runner's step file variable names, the variable and that the agent could read the
// file through a read-only mount and change what the runner's next steps run through any
// other; one of any other path, or of the directory without the key, that it could read
// one of Forager's files through a read-only mount and change it through any other. A
// link's place, on the way to the records or to the configuration, is named as
// the link, which leads to them; for a place that is not read-only, the text adds that
// the agent could point it elsewhere. One on the way to a runner's step file is named
// by that file and its variable, the place leading through a link to it, with the same
// addition.
//
// mount_mode_conflict is a place inside another of the other mode, with the modes
// qory passed. Forager refuses only two places of different modes: the inner's is
// the one it was passed with, and the outer's the other one. For an inner passed with
// both modes, when the two names are one path the inner is the entry Forager
// refuses, the first whose mode differs from an earlier one's, Mounts in order and then
// Dir; for two paths, the outer's one mode decides, else that same entry.
//
// mount_through_link is a place, writable or read-only, reached through a link inside a
// writable mount, the workspace or a place a walled run has used, where its path
// resolves outside that place: the agent could repoint the link. The text names the
// place, the link and the place that holds it; a place that is the link itself, its
// absolute, cleaned form equal to the link, is named as a link inside that place.
//
// mount_shared_with_run is a place that is, holds or lies inside one another walled run
// also uses, its mounts or its run directory, while that run is still going or its
// containers are left: one agent could read or change what the other uses. The text
// says how to find and remove containers left behind, by the label dev.qory.run=<id>
// the wall gives them. A git worktree inside the other run's checkout is the common
// case, and the text says where to make one. Forager names the runs directory for
// this run's own records. Where Forager can no longer tell how two paths stand, such
// as a place reached through a link, the text says they overlap.
//
// engine_unreachable is a walled run that cannot ask the container engine whether an
// earlier walled run is still going, with that run's id the first name and, when
// Forager gives it, the path of that run's registry entry the second. With both, the
// text gives the docker ps command that lists that run's containers, by the id, which
// Forager checks as a run id, and names the entry as the file to delete when it lists
// none or the Docker engine of that run no longer exists; with the id alone, it leaves
// the id out. Both values go through [hostgit.Shown].
//
// A place is the workspace when it is the checkout root or Dir, and a mount otherwise.
func mountRefused(err error, p passed) error {
	var ref *session.Refusal
	if !errors.As(err, &ref) {
		return nil
	}
	var text string
	switch {
	case ref.Code == codeMountContainsForagerFiles && len(ref.Names) == 2:
		mount, path := ref.Names[0], ref.Names[1]
		how := overlap(mount, path)
		// A place the run passed with no mode is a mount it no longer knows: the agent
		// may be able to write it.
		writable, readOnly, _ := p.modes(mount)
		readOnly = readOnly && !writable
		link, isLink := p.own.links[path]
		runner, isRunnerLink := p.own.runnerLinks[path]
		switch {
		case isLink && isRunnerLink:
			// A link on the way to a runner's step file: the agent that points it elsewhere
			// chooses what the runner's next steps take.
			text = fmt.Sprintf("%s leads through a link to %s, the file %s names for the runner's next steps; the agent could point it elsewhere, so the run does not start. Mount a narrower path", p.place(mount), runner.file, runner.variable)
			if readOnly {
				text = fmt.Sprintf("%s leads through a link to %s, the file %s names for the runner's next steps; the agent could read it, so the run does not start. Mount a narrower path", p.place(mount), runner.file, runner.variable)
			}
		case isLink:
			// The link's place: named as the link, which leads to the records or to one
			// of Forager's files.
			what := "one of Forager's files"
			if p.own.records[path] {
				what = "qory's run records"
			}
			text = fmt.Sprintf("%s %s %s, which leads to %s; the agent could point it elsewhere, so the run does not start. Mount a narrower path", p.place(mount), how, link, what)
			if readOnly {
				text = fmt.Sprintf("%s %s %s, which leads to %s, so the run does not start. Mount a narrower path", p.place(mount), how, link, what)
			}
		case p.own.runner[path] != "":
			// A runner's step file: what a step writes there, the runner's next steps take.
			text = fmt.Sprintf("%s %s %s, the file %s names for the runner's next steps; the agent could change what they run, so the run does not start. Mount a narrower path", p.place(mount), how, path, p.own.runner[path])
			if readOnly {
				text = fmt.Sprintf("%s %s %s, the file %s names for the runner's next steps; the agent could read it, so the run does not start. Mount a narrower path", p.place(mount), how, path, p.own.runner[path])
			}
		case path == p.spec.RunsDir || path == p.stateDir || p.own.records[path]:
			what := "change"
			if readOnly {
				what = "read"
			}
			text = fmt.Sprintf("%s %s %s, which holds qory's run records; the agent could %s them, so the run does not start. Mount a narrower path", p.place(mount), how, path, what)
		default:
			// The key is at stake only for a place that is or contains the key's own file.
			key := p.foragerDir != "" && path == p.foragerDir
			if key {
				at := session.Overlap(mount, foragerdir.Dir(p.foragerDir).Path(foragerdir.SecretFile))
				key = at == "is" || at == "contains"
			}
			what := "change"
			if readOnly {
				what = "read"
			}
			text = fmt.Sprintf("%s %s %s, which holds one of Forager's files; the agent could %s it, so the run does not start. Mount a narrower path", p.place(mount), how, path, what)
			if key && foragerdir.Dir(p.foragerDir).HasSecret() {
				text = fmt.Sprintf("%s %s %s, which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path", p.place(mount), how, path)
			}
		}
	case ref.Code == codeMountModeConflict && len(ref.Names) == 2:
		inner, outer := ref.Names[0], ref.Names[1]
		inW, inR, inChanged := p.modes(inner)
		outW, outR, _ := p.modes(outer)
		var innerWritable bool
		switch {
		case inW != inR:
			innerWritable = inW
		case inW && inner == outer:
			innerWritable = inChanged
		case outW != outR:
			innerWritable = !outW
		case inW:
			innerWritable = inChanged
		default:
			return nil
		}
		text = fmt.Sprintf("the mount %s (%s) lies inside %s, which is %s: a part of a mount can't have another mode, so the run does not start. Give both the same mode, or leave %s out", inner, mode(innerWritable), outer, mode(!innerWritable), inner)
	case ref.Code == codeMountThroughLink && len(ref.Names) == 3:
		place, link, outer := ref.Names[0], ref.Names[1], ref.Names[2]
		// The link and the places are paths a walled agent could have named, so each is
		// printed through hostgit.Shown.
		inside := p.placeShown(outer)
		if !p.ofRun(outer) {
			// A place of SessionSpec.Written that is none of this run's: an earlier walled run used it.
			inside = hostgit.Shown(outer) + ", a place a walled run has used"
		}
		text = fmt.Sprintf("%s is reached through the link %s inside %s, which a walled agent can change, so the run does not start. List the link's target itself", p.placeShown(place), hostgit.Shown(link), inside)
		if abs, err := filepath.Abs(place); err == nil && abs == link {
			text = fmt.Sprintf("%s is a link inside %s, which a walled agent can change, so the run does not start. List the link's target itself", p.placeShown(place), inside)
		}
	case ref.Code == codeMountSharedWithRun && len(ref.Names) == 3 && ref.Names[0] == p.spec.RunsDir:
		runs, other, otherPath := ref.Names[0], ref.Names[1], ref.Names[2]
		how := map[string]string{"is": "is", "lies inside": "lie inside", "contains": "contain"}[session.Overlap(runs, otherPath)]
		if how == "" {
			how = "overlap"
		}
		text = fmt.Sprintf("this run's records %s %s %s, which the run %s also uses: it is still going on this machine, or its containers were left behind. Its agent could read or change them, so the run does not start. Wait for %s to end; if it has ended, remove its containers, which docker ps --all --filter label=dev.qory.run=%s lists; or keep qory's state directory out of its mounts", runs, how, otherPath, other, other, other)
	case ref.Code == codeMountSharedWithRun && len(ref.Names) == 3:
		path, other, otherPath := ref.Names[0], ref.Names[1], ref.Names[2]
		text = fmt.Sprintf("%s %s %s, which the run %s also uses: it is still going on this machine, or its containers were left behind. One agent could read or change what the other uses, so the run does not start. Wait for %s to end; if it has ended, remove its containers, which docker ps --all --filter label=dev.qory.run=%s lists; or work in a separate checkout", p.place(path), overlap(path, otherPath), otherPath, other, other, other)
		if info, err := os.Lstat(filepath.Join(path, ".git")); err == nil && info.Mode().IsRegular() {
			text += "; make the worktree beside the checkout, not inside it"
		}
	case ref.Code == codeEngineUnreachable && len(ref.Names) == 1:
		text = "The check for a container of an earlier walled run failed, so the run does not start"
	case ref.Code == codeEngineUnreachable && len(ref.Names) == 2:
		text = "The check for a container of an earlier walled run failed, so the run does not start. If docker ps --all --filter label=dev.qory.run=" + hostgit.Shown(ref.Names[0]) + " lists no container, or the Docker engine of that run no longer exists, delete " + hostgit.Shown(ref.Names[1])
	default:
		return nil
	}
	return &refusedError{text: exports.HideValues(text), err: &session.Refusal{Code: ref.Code}}
}

// overlap is how a place and a path stand to each other, as Forager judges it, and
// "overlaps" when it no longer can.
func overlap(place, path string) string {
	if how := session.Overlap(place, path); how != "" {
		return how
	}
	return "overlaps"
}

// mode is a place's mode as a refusal words it.
func mode(writable bool) string {
	if writable {
		return "writable"
	}
	return "read-only"
}

// ownFiles is what qory passes Forager as its files: files, in order; links, which
// maps a link's place to the link, for the person; records, the ones that guard the
// run records; runner, which maps a file a runner's step file leads to to the variable
// that names it; and runnerLinks, which maps the place of a link on the way to a
// runner's step file to that file and its variable.
type ownFiles struct {
	files       []string
	links       map[string]string
	records     map[string]bool
	runner      map[string]string
	runnerLinks map[string]runnerFile
}

// runnerFile is a runner's step file, as its variable names it, and the variable.
type runnerFile struct {
	file, variable string
}

// foragerFiles is what qory passes Forager as its files: the configuration directory
// foragerDir, absolute, when there is one; then qory's state directory, which holds the
// run records, and what [recordLinks] adds for it and the runs directory runs; then
// what [configLinks] adds; then each file of settings, the paths [settingFiles] returns,
// as [configLinks] passes its files: every link on the way as its place, [linkPlace],
// since the agent could point a link at a file of its own, and where the path leads, or
// the path as it is when a part of it does not exist yet, loops or cannot be read.
// named maps a file of settings that is a runner's step file to the variable that
// names it, which runner then holds for where the file leads.
func foragerFiles(foragerDir, state, runs string, settings []string, named map[string]string) ownFiles {
	own := ownFiles{links: map[string]string{}, records: map[string]bool{}, runner: map[string]string{}, runnerLinks: map[string]runnerFile{}}
	if foragerDir != "" {
		own.files = append(own.files, foragerDir)
	}
	own.files = append(own.files, state)
	files, shown := recordLinks(state, runs)
	for _, f := range files {
		own.records[f] = true
	}
	own.files = append(own.files, files...)
	if foragerDir != "" {
		more, links := configLinks(foragerDir)
		for _, f := range more {
			if !slices.Contains(own.files, f) {
				own.files = append(own.files, f)
			}
		}
		maps.Copy(shown, links)
	}
	add := func(p string) {
		if !slices.Contains(own.files, p) {
			own.files = append(own.files, p)
		}
	}
	for _, f := range settings {
		links, target, err := followLinks(f)
		name, isRunner := named[f]
		for _, l := range links {
			place := linkPlace(l)
			shown[place] = l
			add(place)
			if _, done := own.runnerLinks[place]; isRunner && !done {
				own.runnerLinks[place] = runnerFile{file: f, variable: name}
			}
		}
		if err != nil || target == "" {
			target = f
		}
		add(target)
		if isRunner {
			if _, done := own.runner[target]; !done {
				own.runner[target] = name
			}
		}
	}
	own.links = shown
	return own
}

// settingFiles are the files the settings of the integrations r describes for the run
// name: the value of each setting <name>_file, a string, absolute, from the working
// directory when it is relative, as the program reads it.
func settingFiles(r *config.Forager) []string {
	var out []string
	if r == nil {
		return nil
	}
	for _, in := range r.Integrations {
		if in.Path == "" {
			continue
		}
		var settings map[string]any
		if json.Unmarshal(in.Settings, &settings) != nil {
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(settings)) {
			v, ok := settings[name].(string)
			if before, file := strings.CutSuffix(name, "_file"); !file || before == "" || !ok || v == "" {
				continue
			}
			if abs, err := filepath.Abs(v); err == nil && !slices.Contains(out, abs) {
				out = append(out, abs)
			}
		}
	}
	return out
}

// runnerVariables name the files a GitHub Actions runner gives a job's steps, which a
// step writes to set the next steps' variables, PATH, outputs and summary.
var runnerVariables = []string{"GITHUB_ENV", "GITHUB_PATH", "GITHUB_OUTPUT", "GITHUB_STATE", "GITHUB_STEP_SUMMARY"}

// runnerFiles are the files [runnerVariables] name, each one set to a path of a file,
// absolute: a mount of one would let the agent change what the job's next steps run.
// named maps each file to the first of the variables that names it.
func runnerFiles() (out []string, named map[string]string) {
	named = map[string]string{}
	for _, name := range runnerVariables {
		v := os.Getenv(name)
		if v == "" {
			continue
		}
		abs, err := filepath.Abs(v)
		if err != nil {
			continue
		}
		if info, err := os.Stat(abs); err == nil && info.Mode().IsRegular() && !slices.Contains(out, abs) {
			out = append(out, abs)
			named[abs] = name
		}
	}
	return out, named
}

// recordLinks is what qory passes Forager for the run records when a link is on the
// way to them: the path of the runs directory runs, absolute, under the state directory
// state, is followed one link at a time, as [configLinks] follows its files, and every
// link on the way outside state goes as its place, [linkPlace], since the agent could
// point it at a directory of its own; shown maps the place back to the link. Where the
// path leads goes too when a link takes it out of state, and the path as it is when it
// loops or cannot be read, which Forager then refuses.
func recordLinks(state, runs string) (files []string, shown map[string]string) {
	resolved, err := filepath.EvalSymlinks(state)
	exists := err == nil
	inside := func(p string) bool { return exists && within(resolved, p) }
	shown = map[string]string{}
	links, target, err := followLinks(runs)
	for _, l := range links {
		if !inside(filepath.Dir(l)) {
			place := linkPlace(l)
			shown[place] = l
			files = append(files, place)
		}
	}
	switch {
	case err != nil:
		files = append(files, runs)
	case target != "" && !inside(target):
		files = append(files, target)
	}
	return files, shown
}

// configLinks is what qory passes Forager for the files it reads from its
// configuration directory dir, absolute, when a link takes one out of it: dir itself,
// forager.yaml, qory.yaml or qory.yml, run-keys/, labels/, runtimes/ and the descriptors in it,
// runtimes/*.yaml in any case, as a disk that ignores case opens them. Each is followed
// from dir as given, one link at a time, each part of its path that is a link included,
// dir's own, and every link on the way and where it leads is one of Forager's files,
// since a mount of either would let the agent change what the next run reads. dir is
// followed even when it, or a part of it, does not exist yet: a link on the way could
// still be pointed at a directory of the agent's. Forager resolves its files through
// links, so a link goes as its place, [linkPlace], and shown maps that back to the link
// for the person. What lies in the resolved dir is left out: dir is one of Forager's
// files itself. A file whose chain ends at a part that does not exist yet is passed as
// it is, and Forager follows it to where the target will be; one whose chain loops
// or cannot be read is passed as it is too, and Forager, unable to resolve it,
// refuses the run.
func configLinks(dir string) (files []string, shown map[string]string) {
	// Without dir, nothing lies in it yet.
	resolved, err := filepath.EvalSymlinks(dir)
	exists := err == nil
	inside := func(p string) bool { return exists && within(resolved, p) }
	names := append([]string{".", config.ForagerFileName, DescriptorsDir, foragerdir.RunKeysDir, foragerdir.LabelsDir}, config.Names...)
	if entries, err := os.ReadDir(filepath.Join(dir, DescriptorsDir)); err == nil {
		for _, e := range entries {
			if strings.EqualFold(filepath.Ext(e.Name()), ".yaml") {
				names = append(names, filepath.Join(DescriptorsDir, e.Name()))
			}
		}
	}
	shown = map[string]string{}
	add := func(p string) {
		if !slices.Contains(files, p) {
			files = append(files, p)
		}
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err != nil && name != "." {
			continue
		}
		links, target, err := followLinks(path)
		for _, l := range links {
			if !inside(filepath.Dir(l)) {
				place := linkPlace(l)
				shown[place] = l
				add(place)
			}
		}
		switch {
		case name == ".":
			// dir is passed itself, and Forager follows it.
		case err != nil || target == "":
			add(path)
		case !inside(target):
			add(target)
		}
	}
	return files, shown
}

// maxLinks is how many links [followLinks] follows in one path before it takes the
// path for a loop, as the system does.
const maxLinks = 40

// followLinks follows path, absolute, one link at a time, each part of it that is a link
// included: the links it meets, in order, and where it leads. A part that does not exist
// ends the walk with an empty target. A loop, a part that cannot be read, or more than
// maxLinks links is an error.
func followLinks(path string) (links []string, target string, err error) {
	done := "/"
	rest := strings.Split(path, "/")
	for hops := 0; len(rest) > 0; {
		part := rest[0]
		rest = rest[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			done = filepath.Dir(done)
			continue
		}
		next := filepath.Join(done, part)
		info, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) {
			return links, "", nil
		}
		if err != nil {
			return links, "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			done = next
			continue
		}
		if hops++; hops > maxLinks {
			return links, "", fmt.Errorf("%s: too many links", path)
		}
		to, err := os.Readlink(next)
		if err != nil {
			return links, "", err
		}
		links = append(links, next)
		if filepath.IsAbs(to) {
			done = "/"
		}
		rest = append(strings.Split(to, "/"), rest...)
	}
	return links, done, nil
}

// linkPlace is the link at path as Forager can compare it: its directory, and its
// name as a pattern that matches that one name. Forager resolves each of its files
// through links, and the link's own path to where it leads, which leaves the link
// unguarded. The pattern names no file, so Forager compares it by name: a mount of
// the link's directory, or of one above it, contains it, and one beside the link in that
// directory does not.
func linkPlace(path string) string {
	dir, name := filepath.Split(path)
	var b strings.Builder
	for i, r := range name {
		special := strings.ContainsRune(`*?[\`, r)
		switch {
		case i == 0:
			b.WriteByte('[')
			if special || strings.ContainsRune(`]-^`, r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
			b.WriteByte(']')
		case special:
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return dir + b.String()
}

// Forager's refusals of the places a walled run lists: one that is, contains or lies
// inside one of Forager's files; one inside another of the other mode; one reached
// through a link a walled agent can change that leads out of the place holding it; one
// another walled run, still going or with its containers left, could change, or that
// holds one of its places; and a run that cannot ask the container engine whether an
// earlier walled run is still going.
const (
	codeMountContainsForagerFiles = refusal.MountContainsForagerFiles
	codeMountModeConflict         = "mount_mode_conflict"
	codeMountThroughLink          = "mount_through_link"
	codeMountSharedWithRun        = "mount_shared_with_run"
	codeEngineUnreachable         = "engine_unreachable"
)

// recorded reports whether the run directory dir is there: a run that started has one.
func recorded(dir string) bool {
	_, err := os.Lstat(dir)
	return err == nil
}

// nothingLeftToSend reports whether a run through a separate gateway, closed as closed
// says, nil when it did not open or its close failed, has nothing left to send, so the
// run key kept for it can go: it failed before it made its record, at record, or it
// closed with every event delivered or with its run closed by the gateway. A run that
// made its record and did not close, or that left events undelivered on a run the
// gateway did not close, keeps its key for qory run resend.
func nothingLeftToSend(record string, closed *session.Closed) bool {
	if closed == nil {
		return !recorded(record)
	}
	return closed.Undelivered == 0 || closed.RunClosed
}

// runsDir is the folder of the run records of the checkout at root, under qory's state
// directory: runs/<name>-<hash>, the checkout's name and the first 12 hex digits of the
// SHA-256 of its path, both taken where the path really is, so a checkout reached
// through a link has one folder.
func runsDir(state, root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(state, "runs", filepath.Base(abs)+"-"+hex.EncodeToString(sum[:])[:12]), nil
}

// makeRunsDir makes the folder runs, under state, and leaves state, its runs directory
// and the folder mode 0700: the records are this user's alone.
func makeRunsDir(state, runs string) error {
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return err
	}
	for _, d := range []string{state, filepath.Join(state, "runs"), runs} {
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// passedVariables is the variables named, by name, with their values in this process's
// environment, NAME=value; a name the environment does not set passes nothing. A name
// of Forager's own is refused.
func passedVariables(names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		if config.ForagersOwn(n) {
			return nil, input(fmt.Errorf("--env %s: the variable is Forager's and never the agent's", n))
		}
		if v, ok := os.LookupEnv(n); ok {
			out = withEnv(out, map[string]string{n: v})
		}
	}
	return out, nil
}

// wallHelper is the static Linux build of qory the wall mounts into its containers, as
// the relay and the hook forwarder, and qory image check runs as the probe:
// wall.helper, else on Linux exe, the binary running. Elsewhere this binary cannot run in
// a container, and a section without wall.helper is an input error.
func wallHelper(section config.ForagerWall, exe string) (string, error) {
	if section.Helper != "" {
		return section.Helper, nil
	}
	if runtime.GOOS != "linux" {
		return "", input(fmt.Errorf("the container runs qory's Linux build as its relay, its hook forwarder and what starts nested Docker, and this is the %s build; set wall.helper in %s to the Linux one", runtime.GOOS, config.ForagerFileName))
	}
	return exe, nil
}

// newResend builds the resend verb: the last step of a job that started a run, whatever
// happened before it. A record a session made behind a separate gateway goes to that
// gateway with the run key QORY_RUN_KEY holds ([resendThroughGateway]); any other to the
// server.
func newResend() *cobra.Command {
	var wait time.Duration
	var secretFD, keyFD int
	c := &cobra.Command{
		Use:   "resend <run-id>",
		Short: "Send a finished run's record to the server again",
		Long: `Send a finished run's record to the server in ` + config.ForagerFileName + ` again: after a Forager process that
died, or a server that was away. A job runs it last, whatever happened before.

Only what the server has not accepted is sent. When the run started its gateway itself, a
record Forager left open is closed first, unless the server never opened the run. The
containers and networks its wall left are removed. A run that is still running is refused.

The exit status is 0 when the server has everything, or never opened the run, and 1 when
events remain, or the server said stop.

` + resendThroughGatewayHelp + `

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#resending-a-runs-record`,
		Example: `  qory run resend "$run_id"             # the last step of a job
  qory run resend "$run_id" --wait 10m  # keep trying for ten minutes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// As qory run reads it: the access key's descriptor first, and closed.
			var fdKey *accesskey.Key
			if cmd.Flags().Changed(secretFDFlag) {
				k, err := readSecretFD(secretFD)
				if err != nil {
					return err
				}
				fdKey = k
			}
			key, err := givenRunKey(cmd.Flags().Changed(runKeyFDFlag), keyFD, cmd.InOrStdin())
			if err != nil {
				return err
			}
			at, conf, err := locateRun(homeOptions{}, key != nil, false)
			if err != nil {
				return err
			}
			r := conf.Forager
			if err := session.CheckRunID(args[0]); err != nil {
				return input(err)
			}
			state, err := stateDir()
			if err != nil {
				return err
			}
			runs, err := runsDir(state, at.root)
			if err != nil {
				return err
			}
			stderr := cmd.ErrOrStderr()
			report := func(line string) { fmt.Fprintln(stderr, "qory run resend:", line) }
			// A record its session made behind a separate gateway goes to that gateway,
			// with the run key, or Forager says it is needed.
			dir := filepath.Join(runs, args[0])
			behind, err := session.BehindGateway(dir)
			if err != nil {
				return err
			}
			if behind {
				// A run this machine registered through gateway.server.via has its run key
				// kept beside forager.yaml, which goes once nothing is left to send.
				if key == nil {
					if key, err = keptRunKey(args[0]); err != nil {
						return err
					}
				}
				sig, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				return resendThroughGateway(sig, wait, stderr, r, key, args[0], dir, at.root, report)
			}
			// Any other record goes to the server: one a gateway of the run's own made,
			// which a run key has no part in.
			if key != nil {
				if _, err := os.Lstat(filepath.Join(dir, sink.EventsFile)); err == nil {
					return input(ownGatewayResend(args[0], nil))
				}
				if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
					return input(fmt.Errorf("no run %s is recorded in this checkout", args[0]))
				}
			}
			if r == nil || r.Server == nil {
				return input(fmt.Errorf("%s defines no server to send the record to", config.ForagerFileName))
			}
			if r.Server.Via != nil {
				return ownGatewayViaResend(args[0])
			}
			id, err := identify(r, stderr, "run resend", fdKey)
			if err != nil {
				return err
			}
			server := gatewayServer(r.Server)
			server.AccessKey, server.InstanceID, server.InstanceName = id.key.key, id.instanceID, id.instanceName
			lines := resendLines(report)
			spec := gateway.ResendConfig{
				Dir:     filepath.Join(runs, args[0]),
				Server:  server,
				Version: build().title(),
				Report:  lines.line,
			}
			sig, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ctx, cancel := context.WithTimeout(sig, wait)
			defer cancel()
			res, err := gateway.Resend(ctx, spec)
			lines.done(func(kind int) bool {
				switch kind {
				case heldNotOpened:
					return err == nil && res.NotOpened && res.Undelivered == 0
				case heldUndelivered:
					return err == nil && res.Undelivered > 0
				case heldServerStop:
					return err == nil && res.Stopped && res.Undelivered > 0
				}
				return false
			})
			switch {
			case errors.Is(err, gateway.ErrRunning):
				return input(fmt.Errorf("the run %s is running", args[0]))
			case errors.Is(err, os.ErrNotExist):
				return input(fmt.Errorf("no run %s is recorded in this checkout", args[0]))
			case err != nil:
				return explain(err, id)
			}
			reaped := reapWall(sig, r, args[0], report)
			u := ui.New(stderr)
			if res.Completed {
				u.Success("the record had no end, and now ends as %s", endsAs(res.State, res.Reason))
			}
			if reaped > 0 {
				u.Success("removed %d containers and networks the run left", reaped)
			}
			if res.Stopped && res.Undelivered > 0 {
				// The server's signed 410 during this resend: nothing of what was not
				// sent is spooled, and all of it is still in the record's events.jsonl.
				u.Fail(fmt.Errorf("the server wants no more events of the run %s; %d were accepted and %d were not sent; they stay in %s", args[0], res.Sent, res.Undelivered, ui.Short(spec.Dir, at.root)))
				return reported(&exitError{code: 1})
			}
			if res.Stopped && res.Sent == 0 && res.Undelivered == 0 {
				// The server stopped the run during the run, so nothing is sent: Forager's
				// own line says so, and the resend fails.
				return reported(&exitError{code: 1})
			}
			if res.Undelivered > 0 {
				u.Fail(fmt.Errorf("%d events were accepted and %d were not; %s/undelivered contains them", res.Sent, res.Undelivered, ui.Short(spec.Dir, at.root)))
				return reported(&exitError{code: 1})
			}
			if res.NotOpened {
				// The server never accepted the run's registration, or the run had no server: as
				// behind a gateway, nothing failed now, and the resend succeeds. Forager's
				// own line that says so is left out ([resendLines]).
				u.Success("the server never opened run %s, so there is nothing to send; its record stays in %s", args[0], ui.Short(spec.Dir, at.root))
				return nil
			}
			u.Success("%d events were accepted; the server has the whole record", res.Sent)
			return nil
		},
	}
	c.Flags().DurationVar(&wait, "wait", 2*time.Minute, "how long to keep trying a server that does not accept")
	c.Flags().IntVar(&secretFD, secretFDFlag, 0, secretFDUsage)
	c.Flags().IntVar(&keyFD, runKeyFDFlag, 0, runKeyFDUsage)
	return c
}

// resendThroughGatewayHelp is the paragraph of qory run resend's help on a run that went
// through a separate gateway.
const resendThroughGatewayHelp = `A run that went through a separate gateway sends its record to that gateway instead,
with its run key: QORY_RUN_KEY, or, on a machine that registered the run itself, the run
key qory kept for it. The exit status is 0 when nothing is left to send, and 1 when events
remain or the gateway takes no more of them.`

// The kinds of Forager's report lines qory holds back until it knows whether its own
// line says the same: a run that never opened, gateway.ResendNotOpened and
// gateway.ResendNoServer; events the server did not accept; the session stopped at the
// time limit; the run's gateway ending the run, in a run with a gateway of its own; and
// the server's stop, in a resend.
const (
	heldNotOpened = iota + 1
	heldUndelivered
	heldLimit
	heldRunEnds
	heldServerStop
)

// foragerUndelivered is Forager's report line of events the server did not accept,
// "<n> events were not accepted by the server; see <dir>".
var foragerUndelivered = regexp.MustCompile(`^[0-9]+ events were not accepted by the server; see `)

// foragerLimit begins the session's report line of a runtime stopped at the time limit.
const foragerLimit = "the runtime was stopped at the limit of "

// foragerServerStop is Forager's report line of the server's signed 410, which wants no
// more events of the run.
const foragerServerStop = "the server wants no more events of this run; the run goes on"

// heldLines passes Forager's report lines on to report as they come, but holds back the
// ones kind gives a kind, which repeat what qory may say itself, until done. done is
// told which kinds qory says: those lines are left out, and the others passed on, in
// the order they came. Every line kind gives no kind, 0, is passed on at once.
type heldLines struct {
	report func(string)
	kind   func(string) int
	mu     sync.Mutex
	held   []heldLine
}

// heldLine is a line held back, and its kind.
type heldLine struct {
	kind int
	line string
}

// resendLines are the lines of a resend, to the server or behind a gateway: Forager's
// lines that the run never opened, that the server did not accept events, and that the
// server wants no more events of the run, are held back.
func resendLines(report func(string)) *heldLines {
	return &heldLines{report: report, kind: func(l string) int {
		switch {
		case l == gateway.ResendNotOpened || l == gateway.ResendNoServer:
			return heldNotOpened
		case foragerUndelivered.MatchString(l):
			return heldUndelivered
		case l == foragerServerStop:
			return heldServerStop
		}
		return 0
	}}
}

// runLines are the lines of qory run of the run runID: the session's at the time limit,
// the gateway's of events the server did not accept, and the gateway's that it ends the
// run, "run <id>: ...; the run ends...", are held back.
func runLines(report func(string), runID string) *heldLines {
	return &heldLines{report: report, kind: func(l string) int {
		switch {
		case strings.HasPrefix(l, foragerLimit):
			return heldLimit
		case foragerUndelivered.MatchString(l):
			return heldUndelivered
		case strings.HasPrefix(l, "run "+runID+": ") && strings.Contains(l, "; the run ends"):
			return heldRunEnds
		}
		return 0
	}}
}

// line is a Report of Forager's.
func (h *heldLines) line(l string) {
	if k := h.kind(l); k != 0 {
		h.mu.Lock()
		h.held = append(h.held, heldLine{kind: k, line: l})
		h.mu.Unlock()
		return
	}
	h.report(l)
}

// done ends the held lines: those of a kind said says qory says itself are left out,
// and the others passed on.
func (h *heldLines) done(said func(kind int) bool) {
	h.mu.Lock()
	held := h.held
	h.held = nil
	h.mu.Unlock()
	for _, l := range held {
		if said == nil || !said(l.kind) {
			h.report(l.line)
		}
	}
}

// reapWall removes what the run's wall left once its record is no longer held, when r
// has a wall, within ctx: the wait the signals allow, not the one a delivery may have
// used up. It returns how many containers and networks it removed; a failure is
// reported.
func reapWall(ctx context.Context, r *config.Forager, runID string, report func(string)) int {
	if r.Wall == nil {
		return 0
	}
	reaped, err := (&wall.Docker{Command: r.Wall.Command}).Reap(ctx, runID)
	if err != nil {
		report("removing what the wall left: " + err.Error())
	}
	return reaped
}

// reallyWithin is [within] by where both paths really are: a temporary directory and a
// home are often reached through a link.
func reallyWithin(root, path string) bool {
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	return within(root, path)
}

// The arguments that select the hidden verbs the wall runs from the qory binary: the
// relay, the start of a nested Docker, the hook forwarder, which also runs on this
// machine without a wall, the preparation of the working folder qory run fetched the
// run's code into, the exec step that starts the launch with the names nothing sets
// unset, the program that keeps the setup container running, and the runner of a setup
// command inside the wall; [agentEnvArgs] selects agent-env, which starts the agent of
// a dispatched walled run of Claude Code. Each verb carries [inWall], so no look for a
// release runs inside a container.
var (
	relayArgs        = []string{"run", "relay"}
	nestArgs         = []string{"run", "nest"}
	forwardArgs      = []string{"run", "forward"}
	prepareArgs      = []string{"run", "prepare"}
	execArgs         = []string{"run", "exec"}
	holdArgs         = []string{"run", "hold"}
	setupCommandArgs = []string{"run", "setup-command"}
)

// inWall is the annotation of a verb the wall runs inside a container.
const inWall = "qory.dev/in-wall"

// newRelay builds the hidden relay verb, what the wall starts in the relay's container
// from qory's own binary: it listens on a port for each forward, port=host:port, and
// copies every connection to that address, the gateway's proxy. It is the one peer the
// runtime's container reaches.
func newRelay() *cobra.Command {
	return &cobra.Command{
		Use:         "relay port=host:port...",
		Short:       "Forward the wall's fixed ports to the run's proxy",
		Hidden:      true,
		Annotations: map[string]string{inWall: "relay"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return wall.Relay(ctx, args, cmd.OutOrStdout())
		},
	}
}

// newNest builds the hidden nest verb, what the wall starts from the qory binary as the
// entry point of a container whose image has a nested Docker: it runs as the
// container's root, which the runtime maps to a user of the machine's that is not
// root, starts dockerd on its socket alone, drops every capability and executes the
// launch as the agent's user. Its arguments, --user uid:gid, -- and the launch, go to
// [wall.Nest] as they are, so it parses no flag of its own. It returns only on an error.
func newNest() *cobra.Command {
	return &cobra.Command{
		Use:                "nest --user uid:gid -- command [argument...]",
		Short:              "Start nested Docker, then the agent as its user",
		Hidden:             true,
		Annotations:        map[string]string{inWall: "nest"},
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return wall.Nest(args)
		},
	}
}

// newExec builds the hidden exec verb, what Forager starts a walled run's launch
// through, SessionSpec.Exec, when the run has names to unset: it removes the names each
// --unset gives from its environment and executes the program after -- in its place,
// with its arguments unchanged, as [session.Exec] does. It parses no flag, so the
// launch's words reach the program as given. It returns only on an error,
// exiting as a shell does: 2 for arguments it cannot read, 127 for a program it does
// not find and 126 for one it cannot execute, its reason on stderr.
func newExec() *cobra.Command {
	return &cobra.Command{
		Use:                "exec [--unset name]... -- command [argument...]",
		Short:              "Start the launch with the names nothing sets unset",
		Hidden:             true,
		Annotations:        map[string]string{inWall: "exec"},
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(session.Exec(args, os.Stderr))
		},
	}
}

// newForward builds the hidden forward verb, the command qory run installs as the
// runtime's hook: it reads the hook's input from stdin and hands it to the run that
// installed it, over the socket the run named in the environment. It prints nothing
// and exits 0 whatever happened, so a runtime never sees a decision in it; a failure
// goes to stderr, which a runtime shows only for a hook that failed.
func newForward() *cobra.Command {
	return &cobra.Command{
		Use:         "forward",
		Short:       "Forward a hook's input to the run that installed it",
		Hidden:      true,
		Annotations: map[string]string{inWall: "forward"},
		Args:        noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := session.Forward(cmd.Context(), cmd.InOrStdin()); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "qory run forward:", err)
			}
			return nil
		},
	}
}

// prepareSeed is where qory run prepare looks for a prebuilt image's copy of the run's
// repository, [preparation.Seed]; a test points it elsewhere.
var prepareSeed = preparation.Seed

// newPrepare builds the hidden prepare verb, the setup command of a walled run whose run
// configuration lists repositories, [setupCommands]: it runs from the qory binary as the
// agent's user, in the working folder, in the setup container or, with wall.setup
// inside, in the agent's container before the agent. qory run fetched the repositories
// [preparation.EnvRepositories] names from the host [preparation.EnvSystem] names into
// the working folder on this machine, before the setup, so the verb fetches nothing:
// when the first variable names repositories, it adds to the working folder the files of
// the image's copy at [prepareSeed] that the fetched commit does not track, when there
// is a copy, as [preparation.Prepare] has it, then runs the start commands of the run's
// repository, as [preparation.StartLogged] has them, with a file that it copies to the
// verb's stderr as their stdout and stderr; it does neither when the variable is unset
// or empty. The verb runs as the agent's user in a container of the run's image, as the
// image's programs and the start commands do, so it guards against what the image and
// the repository's files leave in place, not against a repository that acts against it,
// such as through a program the start commands leave running; against that, the
// boundary is the wall. It prints its lines on stderr, what it did or why it failed, and
// exits non-zero on a failure, which fails its setup step, so the agent does not start.
func newPrepare() *cobra.Command {
	c := &cobra.Command{
		Use:         "prepare",
		Short:       "Prepare the working folder qory run fetched the run's repositories into",
		Hidden:      true,
		Annotations: map[string]string{inWall: "prepare"},
		Args:        noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			stderr := cmd.ErrOrStderr()
			listed, err := preparation.Prepare(ctx, ".", prepareSeed, os.Getenv, stderr)
			if err == nil && listed {
				err = preparation.StartLogged(ctx, ".", os.Environ(), stderr)
			}
			if err != nil {
				fmt.Fprintln(stderr, err)
				return reported(err)
			}
			return nil
		},
	}
	return c
}

// newHold builds the hidden hold verb, the entry point the wall starts the setup
// container with from the qory binary, [wall.Docker.HoldArgs]: it runs [wall.Hold], so
// the container and the services the setup commands start keep running until the run
// closes and the wall stops the container, with SIGTERM.
func newHold() *cobra.Command {
	return &cobra.Command{
		Use:         "hold",
		Short:       "Keep the setup container running until the run closes",
		Hidden:      true,
		Annotations: map[string]string{inWall: "hold"},
		Args:        noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return wall.Hold(ctx)
		},
	}
}

// newSetupCommand builds the hidden setup-command verb, SessionSpec.SetupRunner of a run
// with wall.setup inside: Forager's script starts it inside the agent's container once
// for each setup command, with the command's number, -- and the command's words, and it
// runs the command and reports it on the run's socket, as [session.RunSetupCommand]
// does. It parses no flag, so the command's words reach it as given, and exits with the
// command's exit status, or 125 when it cannot run the command or reach the socket.
func newSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "setup-command number -- command [argument...]",
		Short:              "Run one setup command inside the wall and report it to the run",
		Hidden:             true,
		Annotations:        map[string]string{inWall: "setup-command"},
		DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(session.RunSetupCommand(args, os.Stderr))
		},
	}
}

// setupStep is the name of the setup step of qory run prepare.
const setupStep = "prepare"

// setupCommands are the setup commands of a walled run whose run configuration lists
// repositories: qory run prepare, in one setup step, which adds the image's copy of the
// run's repository and then runs the start commands of the repository's development
// container configuration.
func setupCommands() []session.SetupCommand {
	return []session.SetupCommand{{Name: setupStep, Args: append([]string{wall.HelperPath}, prepareArgs...)}}
}

// runSetup makes the setup of a walled run whose run configuration lists repositories,
// once its session's spec is made and before the session joins. With wall.setup inside,
// the setup commands go into the session's spec, and Forager runs them inside the
// agent's container through qory run setup-command before the agent starts. Outside,
// the default, run.Setup runs them now in the setup container, a separate container of
// the run's image on the wall's network, which runs until the run closes. The setup
// container is checked and limited as the session is: it gets the session's working
// folder, ForagerFiles, Written and Limits, and the agent's variables, see [setupEnv].
// Inside, the setup commands get the agent's environment.
func runSetup(ctx context.Context, run openedRun, spec *runSpec) error {
	commands := setupCommands()
	if spec.setupWhere == config.SetupInside {
		spec.SessionSpec.Setup = commands
		spec.SetupRunner = append([]string{wall.HelperPath}, setupCommandArgs...)
		return nil
	}
	env, err := setupEnv(run, spec)
	if err != nil {
		return err
	}
	return run.Setup(ctx, session.SetupSpec{
		Dir:          spec.Dir,
		Env:          env,
		Commands:     commands,
		ForagerFiles: spec.ForagerFiles,
		Written:      spec.Written,
		Limits:       spec.Limits,
	})
}

// setupEnv is the variables of the setup container outside, after the ones Forager puts
// first, the wall's and the run's placeholders: the agent's, as [session.Run.AgentEnv]
// resolves them for the session's spec, with the deny lists and the placeholders of the
// join applied. They contain the server's values of [preparation.EnvSystem] and
// [preparation.EnvRepositories] when it sets them, which qory run prepare reads.
// AgentEnv's error is the one the join would return. A variable_reserved or
// placeholder_conflict refusal is [setupVariableError] when wall.env or --env passes
// every refused name and neither LaunchFixed nor LaunchDefaults, the harness's values,
// sets any of them. Any other error is AgentEnv's as it is: setupWork gives a mount
// refusal the line a join's gets, and qory run prints every other one after its mark.
func setupEnv(run openedRun, spec *runSpec) ([]string, error) {
	env, err := run.AgentEnv(spec.SessionSpec)
	if err == nil {
		return env, nil
	}
	var ref *session.Refusal
	if !errors.As(err, &ref) || ref.Code != refusal.VariableReserved && ref.Code != refusal.PlaceholderConflict || len(ref.Names) == 0 {
		return nil, err
	}
	passed := slices.Concat(envNames(spec.Variables.Machine), envNames(spec.Variables.Run))
	harness := slices.Concat(envNames(spec.LaunchFixed), envNames(spec.LaunchDefaults))
	for _, name := range ref.Names {
		if !slices.Contains(passed, name) || slices.Contains(harness, name) {
			return nil, err
		}
	}
	return nil, &setupVariableError{names: ref.Names, err: err}
}

// envNames is the names of env, NAME=value.
func envNames(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		out = append(out, name)
	}
	return out
}

// setupVariableError is AgentEnv's refusal of variables of wall.env or --env, names,
// as the setup of a walled run gets them: the fix first, then Forager's text. It
// unwraps to the refusal.
type setupVariableError struct {
	names []string
	err   error
}

func (e *setupVariableError) Error() string {
	return "leave " + strings.Join(e.names, ", ") + " out of wall.env and --env, then run again: " + e.err.Error()
}

func (e *setupVariableError) Unwrap() error { return e.err }

// setupWhere is run.started's setup for where, wall.setup's value.
func setupWhere(where string) string {
	if where == config.SetupInside {
		return event.SetupInside
	}
	return event.SetupOutside
}

// ownHome refuses a launch whose report contains a home other than the one qory computes for
// the checkout: the home is a mount of a walled run, and the report can be one a
// repository committed or a walled agent wrote, so it supplies no path a run mounts. qory
// run composes the home itself, and checks only the checkout, see [checkoutsHome].
// The home in at is [placesFor]'s, or --home's when that names a composed home; then the
// checkout comes from the report beside it, and has to be the one the home is composed
// for: a home inside it is its .qory/harness, and one outside is named by its
// [checkout.Key]. Two paths to one directory are one home. what names the verb's use of
// the home, such as "a launch", and refused is what it does not do, such as "qory
// prints no launch".
func ownHome(at places, rep report.Report, what, refused string) error {
	if !composedFor(at.root, at.home) {
		return input(fmt.Errorf("the harness report %s names the checkout %s, and %s is not that checkout's home; %s uses only its checkout's home, so %s. Run qory harness compose again", exports.Value(ui.Short(at.report, at.root), false), at.root, exports.Value(at.home, false), what, refused))
	}
	if !samePath(rep.Home, at.home) {
		return input(fmt.Errorf("the harness report %s names the home %s, and this checkout's home is %s; %s uses only the home qory computes, so %s. Run qory harness compose again", exports.Value(ui.Short(at.report, at.root), false), exports.Value(rep.Home, false), exports.Value(at.home, false), what, refused))
	}
	return nil
}

// checkoutsHome refuses a run whose --home is a composed home whose report, beside it, is
// for a checkout whose home it is not. The run would compose for that checkout and
// mount it, and the report can be one a repository committed or a walled agent wrote, so
// it chooses no checkout the run mounts. A home found by no report is the checkout's.
func checkoutsHome(at places) error {
	if composedFor(at.root, at.home) {
		return nil
	}
	return input(fmt.Errorf("remove the harness report %s, or give --home the home of the checkout %s. The report is for that checkout, and %s is not its home; a run uses only its checkout's home, so the run does not start", exports.Value(at.report, false), at.root, exports.Value(at.home, false)))
}

// composedFor reports whether home is where a compose puts the home of the checkout at
// root: its .qory/harness inside it, or a directory named by its [checkout.Key] outside.
func composedFor(root, home string) bool {
	if within(root, home) {
		return home == filepath.Join(root, config.DefaultHome)
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	return filepath.Base(home) == checkout.Key(root)
}

// samePath reports whether two absolute paths name one place: the same path, or the
// same once their links are resolved.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// userHome refuses a walled run whose home a qory.yaml other than the configuration
// directory's moves: behind a wall the home is a mount, so harness.home comes from the
// qory.yaml in [config.UserDir] alone. The checkout's file, or an ancestor directory's,
// may be one a walled agent can write, through the workspace or a writable mount. A
// value in another file that leaves the home where the configuration directory's file
// alone puts it is harmless and passes. With no configuration directory, any other file
// that sets harness.home is refused, as the run records are with no state directory.
// keyed is a run given a run key or a run code, whose home with no harness.home in the
// configuration directory's file is [runKeyHomes].
func userHome(at places, conf config.Config, keyed bool) error {
	origin := conf.Origin("harness.home")
	yours := ""
	dir, err := config.UserDir()
	if err != nil {
		return input(err)
	}
	if dir != "" {
		f, err := config.FileIn(dir)
		if err != nil {
			return input(err)
		}
		yours = f
		if yours == "" {
			yours = filepath.Join(dir, config.FileName)
		}
	}
	if origin == config.Default || origin == yours {
		return nil
	}
	if yours == "" {
		return fmt.Errorf("no configuration directory: set HOME or XDG_CONFIG_HOME")
	}
	own, err := config.LoadUser()
	if err != nil {
		return input(err)
	}
	mine := conf
	mine.Home = own.Home
	if keyed {
		if mine, err = withRunKeyHome(at.root, mine); err != nil {
			return err
		}
	}
	if want, err := placesFor(at.root, mine, homeOptions{}); err == nil && samePath(want.home, at.home) {
		return nil
	}
	file := ui.Short(origin, "")
	if filepath.Dir(origin) == at.root {
		file = "the checkout's " + filepath.Base(origin)
	}
	return input(fmt.Errorf("%s sets harness.home, and a walled run takes the home from %s alone, so the run does not start. Set harness.home in %s, or remove it from %s", file, ui.Short(yours, ""), ui.Short(yours, ""), file))
}

// resolveLaunch is what qory harness launch and qory run share: the runtime, named or
// the one the harness is composed for, and its launch spec from the runtime's template
// with harness.launch over it, resolved against home, and the effort the harness was
// composed with in the place the runtime's descriptor lists. The report supplies the
// runtimes composed, the effort and the harness's variables, each a default.
func resolveLaunch(rep report.Report, conf config.Config, runtime, home string) (string, render.Launch, error) {
	name := runtime
	if name == "" {
		if len(rep.Target.Runtimes) != 1 {
			return "", render.Launch{}, input(fmt.Errorf("the harness is composed for %s; --runtime selects which to start", strings.Join(accesskey.ShownNames(rep.Target.Runtimes), ", ")))
		}
		name = rep.Target.Runtimes[0]
	}
	if !slices.Contains(rep.Target.Runtimes, name) {
		return "", render.Launch{}, input(fmt.Errorf("the harness is not composed for %s; composed: %s", accesskey.ShownName(name, false), strings.Join(accesskey.ShownNames(rep.Target.Runtimes), ", ")))
	}
	rt, err := render.Lookup(name)
	if err != nil {
		return "", render.Launch{}, input(err)
	}
	launch, err := render.LaunchFor(rt, home, launchOverride(conf.Launch, name), report.ComposeVars(rep.LaunchEnv[name]))
	if err != nil {
		return "", render.Launch{}, input(err)
	}
	return name, withRunEffort(launch, name, rep.Target.Effort), nil
}

// launchOverride is harness.launch.<runtime> as a template over the runtime's own, nil
// when the configuration has none.
func launchOverride(launch map[string]config.Launch, runtime string) *render.Template {
	l, ok := launch[runtime]
	if !ok {
		return nil
	}
	return &render.Template{Command: l.Command, Args: l.Args, Env: l.Env}
}

// splitAtDash is the runtime named before -- , if one was, and the arguments after it.
func splitAtDash(cmd *cobra.Command, args []string) (string, []string) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		dash = len(args)
	}
	runtime := ""
	if dash == 1 {
		runtime = args[0]
	}
	return runtime, args[dash:]
}

// withEnv is base with the variables of m set, replacing any of the same names.
func withEnv(base []string, m map[string]string) []string {
	if len(m) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(m))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := m[name]; !ok {
			out = append(out, kv)
		}
	}
	for _, k := range sortedKeys(m) {
		out = append(out, k+"="+m[k])
	}
	return out
}

// isTerminal reports whether a stream is a terminal.
func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// exitError carries a runtime's exit status out of qory run: the process exits with
// it, and the message was printed already, so it matches [ErrReported] through
// [reported].
type exitError struct{ code int }

// Error returns the message with the status.
func (e *exitError) Error() string { return fmt.Sprintf("the runtime exited %d", e.code) }

// ExitStatus is the status the process exits with.
func (e *exitError) ExitStatus() int { return e.code }

// exitStatus is the status an error asks for, when it is an [exitError].
func exitStatus(err error) (int, bool) {
	var e *exitError
	if errors.As(err, &e) {
		return e.code, true
	}
	return 0, false
}

// exitStatusOf reports whether err is an [exitError].
func exitStatusOf(err error) bool {
	_, ok := exitStatus(err)
	return ok
}

// composeForRun composes the harness of a qory run before its session starts: the
// compose of qory harness compose, with the flags o contains and the run's runtime as its
// --runtime, into the home at that the run starts on. It returns the git sources the
// compose resolved and the composed stack's workflows. inCheckout is set for a run given a
// run key, see [extendsInCheckout], and walled for a run behind a wall, whose home
// contains copies of the modules' files, see [copies], as the home of a run whose
// harness is hidden, o.hidden, does. stdout is the agent's, so the compose prints there
// nothing; on stderr it prints a collision as compose does, a line per git source whose
// remote could not be reached, and its result line last.
func composeForRun(stderr io.Writer, o composeOptions, at places, runtime string, inCheckout, walled bool) ([]session.HarnessSource, map[string]stack.Workflow, error) {
	o.runtime, o.at, o.run, o.inCheckout, o.walled = runtime, &at, true, inCheckout, walled
	c, err := composeHome(io.Discard, stderr, o)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range c.warnings {
		fmt.Fprintln(stderr, "qory run:", w)
	}
	names := make([]string, 0, len(c.targets))
	for _, rt := range c.targets {
		names = append(names, rt.Name())
	}
	fmt.Fprintf(stderr, "qory run: composed %s from %s for %s\n", count(len(c.rep.Entries), "entry", "entries"), count(len(c.rep.Modules), "module", "modules"), strings.Join(names, ", "))
	return c.harness, c.workflows, nil
}

// harnessSources is the harness's git sources a compose resolved, each with the full
// commit it used, as the session records them: the stack res extends, when it is a git
// source, then each module's git source in stack order, each source once. A source is
// as the report writes it, the password of a URL's user part printed as (hidden); in
// the compose of a named stack, n, a source of the stack is its name, such as stack
// core or module tools, as [source.Named.Of] has it, once per name and commit. Path
// sources have no commit and are left out; nil when no source is a git source.
func harnessSources(res *compose.Result, n *source.Named) []session.HarnessSource {
	var out []session.HarnessSource
	seen := map[string]bool{}
	add := func(src, commit string) {
		key := src
		if phrase, ok := namedOf(n, src); ok {
			src, key = strings.TrimPrefix(phrase, "the "), phrase+"@"+commit
		}
		src = exports.HideURLs(src)
		if commit == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, session.HarnessSource{Source: src, Commit: commit})
	}
	if res.Base != nil {
		add(res.Base.Source, res.Base.Commit)
	}
	for _, m := range res.Modules {
		add(m.Source, m.Commit)
	}
	return out
}

// namedOf is the phrase of src, a git source as a compose's result has it, in the
// compose of the named stack n, as [source.Named.Of] has it; ok is false for no named
// stack and for a source not of the stack.
func namedOf(n *source.Named, src string) (string, bool) {
	if n == nil {
		return "", false
	}
	return n.Of(src)
}
