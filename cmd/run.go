package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/qoryai/runner/accesskey"
	"github.com/qoryai/runner/runtimes/catalog"
	"github.com/qoryai/runner/session"
	"github.com/qoryai/runner/wall"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/runnerdir"
	"github.com/qoryai/qory/internal/ui"
)

// DescriptorsDir is the directory of runtime descriptor overrides, <runtime>.yaml,
// under the user's configuration directory.
const DescriptorsDir = "runtimes"

// newRun builds the run verb, which starts a runtime on the composed harness through
// the session runner: the launch spec is what qory harness launch prints, the policy
// and the server are the runner file in the user's configuration directory or named
// by flag, and the runner records the session in qory's state directory, in a folder of
// the checkout's. The verb is thin: it resolves the spec, hands it to the runner and
// exits with the runtime's status.
func newRun() *cobra.Command {
	var h homeOptions
	var local, headless bool
	var o wallOptions
	var policyFile, runID string
	var labels []string
	var timeout, grace time.Duration
	var stopSignal string
	var secretFD int
	c := &cobra.Command{
		Use:   "run [runtime] [-- argument...]",
		Short: "Run the agent on its harness, observed and recorded",
		Long: `Run the agent on the composed harness, inside the session runner.

Every connection goes through a proxy on this machine and is recorded. The record,
events.jsonl and output.log, goes to a folder of the checkout's under
~/.local/state/qory/runs ($XDG_STATE_HOME/qory/runs when that is set to an absolute
path), and qory names it when the run ends. The exit status is the agent's.

The agent is the runtime the harness is composed for. Name one first when it is composed
for several. Arguments after -- go to the agent. At a terminal the agent runs with its
own interface; --headless, no terminal, or a headless argument such as -p runs it on
pipes.

` + config.RunnerFileName + ` in ~/.config/qory sets what the runner does on this machine. A repository
cannot set it:

  egress        the hosts the agent may reach: enforce or observe, allow and deny
  wall          run the agent in a container whose one way out is the proxy
  credentials   tokens the proxy sets on requests; behind a wall the agent never has them
  integrations  programs that supply such tokens, such as qory-github
  server        the server every run reports to; --local skips it
  run           a time limit, and how the agent is stopped

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md`,
		Example: `  qory run                                          # the agent, at your terminal
  qory run claude -- -p "Reply pong"                # one headless turn
  qory run --wall docker --image agent:1            # in a container
  qory run --image go-docker                        # in an image runner.yaml defines
  qory run --policy ~/policy.yaml -- -p "$prompt"   # with this run's own policy
  qory run --timeout 5h30m -- -p "$prompt"          # stop it after five and a half hours`,
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
			// The descriptor is read and closed first, whatever comes next, so nothing
			// qory starts inherits it.
			var fdKey *accesskey.Key
			if cmd.Flags().Changed(secretFDFlag) {
				k, err := readSecretFD(secretFD)
				if err != nil {
					return err
				}
				fdKey = k
			}
			runtime, extra := splitAtDash(cmd, args)
			at, conf, err := locate(h)
			if err != nil {
				return err
			}
			rep, err := readReport(at)
			if err != nil {
				return err
			}
			name, launch, err := resolveLaunch(rep, conf, runtime)
			if err != nil {
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
			user := config.UserDir()
			var pol *session.Policy
			var server *session.Server
			if r := conf.Runner; r != nil {
				if r.Egress != nil {
					pol = &session.Policy{Version: 1, Egress: session.PolicyEgress{Mode: r.Egress.Mode, Allow: r.Egress.Allow, Deny: r.Egress.Deny}}
				}
				if r.Server != nil {
					server = sessionServer(r.Server)
				}
			}
			if policyFile != "" {
				if server != nil && !local {
					return input(fmt.Errorf("--policy is the run's own policy without a server; with server configured the server's run configuration is the policy, and --local runs with the files alone"))
				}
				if pol, err = runPolicy(policyFile, at.root, pol); err != nil {
					return err
				}
			}
			named, err := parseLabels(labels)
			if err != nil {
				return err
			}
			named = withOrigin(named, at.root)
			if err := session.CheckLabels(named); err != nil {
				return input(err)
			}
			if runID != "" {
				if err := session.CheckRunID(runID); err != nil {
					return input(fmt.Errorf("--run-id: %w", err))
				}
			}
			if timeout < 0 || grace < 0 {
				return input(fmt.Errorf("--timeout and --stop-grace are not negative"))
			}
			if r := conf.Runner; r != nil {
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
			rt, err := catalog.Lookup(name, filepath.Join(user, DescriptorsDir))
			if err != nil {
				return input(err)
			}
			if err := session.CheckStopSignal(stopSignal); err != nil {
				return input(fmt.Errorf("--stop-signal: %w", err))
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
				if id, err = identify(conf.Runner, stderr, "run", fdKey); err != nil {
					return err
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			spec := session.Spec{
				Runtime:       rt,
				Command:       launch.Command,
				Args:          append(append([]string{}, launch.Args...), extra...),
				Env:           os.Environ(),
				Dir:           cwd,
				Interactive:   !headless && isTerminal(cmd.InOrStdin()) && isTerminal(cmd.OutOrStdout()),
				Stdin:         cmd.InOrStdin(),
				Stdout:        cmd.OutOrStdout(),
				Stderr:        stderr,
				Policy:        pol,
				Server:        server,
				Local:         local,
				Declared:      rep.Hosts(),
				RunsDir:       runs,
				Forwarder:     append([]string{exe}, forwardArgs...),
				RunnerVersion: build().title(),
				RunID:         runID,
				Labels:        named,
				Timeout:       timeout,
				StopSignal:    stopSignal,
				StopGrace:     grace,
			}
			if spec.RunID == "" {
				spec.RunID = newRunID()
			}
			if id != nil {
				spec.AccessKey, spec.InstanceID, spec.InstanceName = id.key.key, id.instanceID, id.instanceName
			}
			selected := ""
			if pol != nil {
				selected = pol.Image
			}
			if err := enclose(&spec, conf.Runner, o, selected, server != nil && !local, exe, at.root, rep.Home); err != nil {
				return err
			}
			if policyFile != "" {
				for _, m := range spec.Mounts {
					if abs, _ := filepath.Abs(policyFile); !m.ReadOnly && reallyWithin(m.Path, abs) {
						return input(fmt.Errorf("--policy %s is inside %s, which the container may write; keep it outside or mount that read-only", policyFile, m.Path))
					}
				}
			}
			runnerDir := ""
			if d := config.UserDir(); d != "" {
				if runnerDir, err = filepath.Abs(d); err != nil {
					return err
				}
			}
			if err := makeRunsDir(state, spec.RunsDir); err != nil {
				return err
			}
			own := runnerFiles(runnerDir, state, spec.RunsDir)
			spec.RunnerFiles = own.files
			walled := spec.Wall != nil
			spec.LaunchFixed, spec.LaunchDefaults, spec.HarnessHome = launch.Fixed, launch.Defaults, launch.HarnessHome
			runLock, err := startRun(machineDir(), spec.RunID, walled, id == nil)
			if err != nil {
				return err
			}
			defer runLock.Release()
			if id != nil {
				spec.Discovered = discovered(machineDir(), id, walled, stderr)
			}
			apiary := ""
			if server != nil {
				apiary = server.URL
			}
			spec.OnVariables = unusedEnv(stderr, apiary)
			if r := conf.Runner; r != nil {
				for _, key := range r.Shadowed() {
					fmt.Fprintln(stderr, "qory run:", shadowed(key))
				}
				if err := r.Expand(ctx, expansion(r, pol, server != nil && !local, at.root, cwd, spec.Mounts)); err != nil {
					return input(err)
				}
				for _, in := range r.Integrations {
					if in.Path != "" {
						fmt.Fprintf(stderr, "qory run: integration %s: %s %s\n", in.Key, in.Path, in.Version)
					}
				}
				for _, c := range r.Credentials {
					def := session.Credential{Name: c.Name, Env: c.Env, File: c.File, Adapter: c.Adapter, Argument: c.Argument, Hosts: c.Hosts, Scheme: c.Scheme, Username: c.Username, Header: c.Header, Paths: c.Paths, Placeholders: c.Placeholders}
					if err := def.Check(); err != nil {
						return input(fmt.Errorf("%s: %w", config.RunnerFileName, err))
					}
					spec.Credentials = append(spec.Credentials, def)
				}
			}
			record := filepath.Join(spec.RunsDir, spec.RunID)
			u := ui.New(stderr)
			res, err := session.Run(ctx, spec)
			if err != nil {
				if m := mountRefused(err, passed{runnerDir: runnerDir, stateDir: state, spec: &spec, root: at.root, own: own}); m != nil {
					err = m
				} else {
					err = explain(err, id)
				}
				// A run that started has a record, however it ended: the line naming it
				// follows the error, so qory prints the error itself.
				if _, statErr := os.Stat(record); statErr != nil {
					return err
				}
				if !ui.Marked() {
					u.Title("qory")
				}
				u.Fail(err)
				fmt.Fprintln(stderr, "qory run: the record is in", record)
				return reported(err)
			}
			defer fmt.Fprintln(stderr, "qory run: the record is in", record)
			if res.Undelivered > 0 {
				u.Fail(fmt.Errorf("%d events did not reach the server; %s/undelivered contains them", res.Undelivered, ui.Short(res.Dir, at.root)))
			}
			switch {
			case res.RunClosed:
				u.Fail(fmt.Errorf("the server closed the run, and %s was stopped", name))
				return reported(&exitError{code: 1})
			case res.TimedOut:
				u.Fail(fmt.Errorf("%s was stopped at the limit of %s", name, timeout))
				return reported(&exitError{code: exitTimeout})
			case res.Signal != "":
				u.Fail(fmt.Errorf("%s was ended by %s", name, res.Signal))
				return reported(&exitError{code: 1})
			case res.ExitCode != 0:
				u.Fail(fmt.Errorf("%s exited %d", name, res.ExitCode))
				return reported(&exitError{code: res.ExitCode})
			}
			u.Success("%s exited 0", name)
			return nil
		},
	}
	c.Flags().BoolVar(&local, "local", false, "run without the server: record to files, under the machine's policy")
	c.Flags().BoolVar(&headless, "headless", false, "run on pipes even at a terminal; -p for claude implies it")
	c.Flags().StringVar(&policyFile, "policy", "", "this run's own policy file, kept outside the checkout; it narrows the egress of "+config.RunnerFileName+", never widens it (with a server: needs --local)")
	c.Flags().StringVar(&runID, "run-id", "", "the run's id, a UUID in lower case (default a new one)")
	c.Flags().StringArrayVar(&labels, "label", nil, "a key=value name for the run, reported in its events; repeatable (forge and repository come from the origin remote)")
	c.Flags().DurationVar(&timeout, "timeout", 0, "stop the agent after this long, such as 5h30m, and exit "+fmt.Sprint(exitTimeout)+" (default no limit; "+config.RunnerFileName+": run.timeout)")
	c.Flags().StringVar(&stopSignal, "stop-signal", "", "the signal that stops the agent: SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1 or SIGUSR2 (default SIGTERM; "+config.RunnerFileName+": run.stop_signal)")
	c.Flags().DurationVar(&grace, "stop-grace", 0, "the time between the stop signal and SIGKILL (default 10s; "+config.RunnerFileName+": run.stop_grace)")
	c.Flags().StringVar(&o.name, "wall", "", "run the agent in a container whose one way out is the proxy: "+config.WallDocker+", or none ("+config.RunnerFileName+": wall.adapter)")
	c.Flags().StringVar(&o.image, "image", "", "the container's image unless the run's policy selects one: a name of wall.images, or a reference ("+config.RunnerFileName+": wall.image)")
	c.Flags().StringArrayVar(&o.env, "env", nil, "a variable of this shell to pass to the agent, by name, with a wall or without; it wins over wall.env of "+config.RunnerFileName+"; repeatable")
	c.Flags().StringArrayVar(&o.mounts, "mount", nil, "a path of this machine the container sees too, :ro for read-only; repeatable ("+config.RunnerFileName+": wall.mounts)")
	c.Flags().StringVar(&o.limits.CPUs, "cpus", "", "how many CPUs the container gets, such as 1.5 ("+config.RunnerFileName+": wall.cpus)")
	c.Flags().StringVar(&o.limits.Memory, "memory", "", "the most memory the container gets, such as 8g ("+config.RunnerFileName+": wall.memory)")
	c.Flags().IntVar(&o.limits.PIDs, "pids-limit", 0, "the most processes and threads in the container ("+config.RunnerFileName+": wall.pids_limit)")
	c.Flags().IntVar(&secretFD, secretFDFlag, 0, secretFDUsage)
	c.Flags().StringVar(&o.limits.ShmSize, "shm-size", "", "the size of /dev/shm in the container, such as 2g ("+config.RunnerFileName+": wall.shm_size)")
	homeFlags(c, &h)
	c.AddCommand(newResend(), newForward(), newRelay(), newNest())
	return c
}

// expansion is what a run describes of the integrations the runner file declares. A
// policy this process has lists the credentials the run selects, so the integrations it
// selects are the ones described; a policy the server supplies arrives once the
// runner starts, so every declared integration is described before it does. An
// integration whose key the credentials section defines itself defines nothing for the
// run and is not described. A program the run may write is refused: one in the
// checkout, in the working directory when that is inside the checkout, or in a
// read-write mount of the wall's container.
func expansion(r *config.Runner, pol *session.Policy, fromServer bool, root, cwd string, mounts []wall.Mount) config.Expansion {
	e := config.Expansion{Workspace: []string{root}}
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
	return e
}

// shadowed is the line that reports that the credentials section defines the credential
// an integration of the same key defines otherwise.
func shadowed(key string) string {
	return fmt.Sprintf("%s: credentials.%s defines the credential %s, and integrations.%s defines none", config.RunnerFileName, key, key, key)
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
// kept outside the checkout, as the runner file is: inside, the agent it constrains
// could write it.
func runPolicy(file, root string, machine *session.Policy) (*session.Policy, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, input(err)
	}
	if reallyWithin(root, abs) {
		return nil, input(fmt.Errorf("--policy %s is inside the checkout, where the agent it constrains could write it; keep it outside", file))
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, input(err)
	}
	p, err := session.ReadPolicy(filepath.Base(abs), b)
	if err != nil {
		return nil, input(err)
	}
	return p.Under(machine), nil
}

// parseLabels reads --label key=value; the runner checks what a key and a value may be.
func parseLabels(labels []string) (map[string]string, error) {
	if len(labels) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, l := range labels {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			return nil, input(fmt.Errorf("--label %s is not key=value", l))
		}
		if _, dup := out[k]; dup {
			return nil, input(fmt.Errorf("--label %s is set twice", k))
		}
		out[k] = v
	}
	return out, nil
}

// withOrigin adds the labels the checkout's origin remote gives, forge and repository,
// to the caller's, which win: a caller that names them knows better than the remote,
// and one that names neither gets what [checkout.Origin] reads. A checkout with no
// origin, or one on this machine, adds nothing.
func withOrigin(labels map[string]string, root string) map[string]string {
	forge, repository, ok := checkout.Origin(root)
	if !ok {
		return labels
	}
	out := map[string]string{"forge": forge, "repository": repository}
	for k, v := range labels {
		out[k] = v
	}
	return out
}

// wallOff is the --wall value that runs without the wall the runner file sets.
const wallOff = "none"

// enclose puts the spec behind a wall when a flag or the runner file sets one, and sets
// the run's own variables, --env, with a wall or without. The runtime then runs in a
// container, so what refers to this machine changes: it inherits nothing of the
// process's environment, the machine's variables, wall.env, go in beside the run's, and
// the forwarder is the helper's path inside the container. The launch's variables, its
// fixed ones, its defaults and its home, go to the runner behind a wall or not: the
// checkout, the composed home, which is all a launch template's paths point into, and
// the mounts keep their paths inside the container.
//
// The images wall.images defines go to the runner, which reads the default, --image or
// wall.image, as the name of one of them first and as a reference otherwise, and
// starts the one the policy selects instead. selected is the image the policy this
// process holds selects, empty when it selects none: qory refuses a selection the runner
// would refuse before the run, so it is an input error, and a run whose policy selects
// an image needs no default. fromServer says the server's run configuration is the
// policy; it arrives once the run starts, and may select no image, so such a run needs a
// default.
func enclose(spec *session.Spec, r *config.Runner, o wallOptions, selected string, fromServer bool, exe, root, home string) error {
	var section config.RunnerWall
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
			return input(fmt.Errorf("the policy selects the image %s, which needs a wall: wall in %s, or --wall %s", selected, config.RunnerFileName, config.WallDocker))
		}
		return nil
	}
	if name != config.WallDocker {
		return input(fmt.Errorf("--wall %s: the walls are %s, and %s for a run without one", name, config.WallDocker, wallOff))
	}
	if selected != "" && !section.Defines(selected) {
		return input(fmt.Errorf("the policy selects the image %s, which wall.images in %s does not define", selected, config.RunnerFileName))
	}
	spec.Image = o.image
	if spec.Image == "" {
		spec.Image = section.Image
	}
	if spec.Image == "" && selected == "" {
		err := fmt.Errorf("a wall needs the container's image: --image, or wall.image in %s, a name of wall.images or a reference", config.RunnerFileName)
		if fromServer {
			err = fmt.Errorf("%w; with a server, set one even when its run configuration selects an image: that arrives once the run starts, and may select none", err)
		}
		err = fmt.Errorf("%w; to build Qory's and check yours, see https://github.com/qoryai/qory/blob/main/docs/run.md#qorys-images", err)
		return input(err)
	}
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
	}
	for _, v := range o.mounts {
		m, err := config.ParseMount(v)
		if err != nil {
			return input(fmt.Errorf("--mount: %w", err))
		}
		if _, err := os.Stat(m.Path); err != nil {
			return input(fmt.Errorf("--mount: %w", err))
		}
		more = append(more, wall.Mount{Path: m.Path, ReadOnly: m.ReadOnly})
	}
	helper, err := wallHelper(section, exe)
	if err != nil {
		return err
	}
	spec.Env = []string{}
	spec.Variables.Machine = machine
	spec.Wall = &wall.Docker{Command: section.Command, Helper: helper, RelayArgs: relayArgs, NestArgs: nestArgs, User: section.User, CAEnv: section.CAEnv}
	spec.Forwarder = append([]string{wall.HelperPath}, forwardArgs...)
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

// unusedEnv is what a run does once the runner has resolved its variables: it prints a
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
				fmt.Fprintf(report, "qory run: %s from --env is not used: %s\n", v.Name, whyUnused(v.From, l.Why, host))
			}
		}
	}
}

// whyUnused says why a value of --env was left out: why is the runner's reason, and from
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
	return "the runner left it out (" + why + ")"
}

// passed is what qory passed the runner that its refusal of a mount names: runnerDir,
// the configuration directory, absolute, "" for none; stateDir, qory's state directory;
// the spec, whose RunsDir, Mounts and Dir the refusal may name; root, the checkout,
// which is the workspace's root in Mounts behind a wall; and own, the runner's files
// qory passed, with the links among them.
type passed struct {
	runnerDir, stateDir string
	spec                *session.Spec
	root                string
	own                 ownFiles
}

// workspace reports whether path is the workspace: the checkout root as Mounts holds
// it, or Dir.
func (p passed) workspace(path string) bool {
	if path == p.spec.Dir {
		return true
	}
	return path == p.root && slices.ContainsFunc(p.spec.Mounts, func(m wall.Mount) bool { return m.Path == p.root })
}

// place is how a refusal names one of the run's places: the workspace, or a mount.
func (p passed) place(path string) string {
	if p.workspace(path) {
		return "the workspace " + path
	}
	return "the mount " + path
}

// modes is how the run passed path: writable, read-only, or both, from the entries of
// Mounts with that path, and Dir, writable. last is the mode of the last of them, Dir
// counting after every mount, as the runner counts it.
func (p passed) modes(path string) (writable, readOnly, last bool) {
	for _, m := range p.spec.Mounts {
		if m.Path == path {
			writable, readOnly, last = writable || !m.ReadOnly, readOnly || m.ReadOnly, !m.ReadOnly
		}
	}
	if path == p.spec.Dir {
		writable, last = true, true
	}
	return writable, readOnly, last
}

// mountRefused words the runner's refusals of the run's places behind a wall for the
// person, each before the run starts. Any other error is nil here.
//
// mount_contains_runner_files is a place that is, contains or lies inside one of the
// runner's files. A refusal of the configuration directory says the agent could read
// the access key when access-key-secret is there and the place is or contains it; one
// of the runs directory, of qory's state directory or of a link on the way to them,
// that it could read the run records through a read-only mount and change them through
// any other; one of any other path, or of the directory without the key, that it could
// change one of the runner's files. A link's place is named as the link.
//
// mount_mode_conflict is a place inside another of the other mode, with the modes
// qory passed. The runner refuses only two places of different modes: the inner's is
// the one it was passed with, and the outer's the other one. An inner passed with both
// modes is the later entry when the two names are one path, Dir counting last; for two
// paths, the outer's one mode decides, else the inner's last entry.
//
// mount_shared_with_run is a place another walled run still going also mounts: one
// agent could change what the other mounts. A git worktree inside the other run's
// checkout is the common case, and the text says where to make one. The runner names
// the runs directory for this run's own records, which another run can write.
//
// A place is the workspace when it is the checkout root or Dir, and a mount otherwise.
func mountRefused(err error, p passed) error {
	var ref *session.Refusal
	if !errors.As(err, &ref) {
		return nil
	}
	var text string
	switch {
	case ref.Code == codeMountContainsRunnerFiles && len(ref.Names) == 2:
		mount, path := ref.Names[0], ref.Names[1]
		how := overlap(mount, path)
		shown := path
		if l, ok := p.own.links[path]; ok {
			shown = l
		}
		switch {
		case path == p.spec.RunsDir || path == p.stateDir || p.own.records[path]:
			// A place the run passed with no mode is a mount it no longer knows: the
			// agent may be able to write it.
			writable, readOnly, _ := p.modes(mount)
			what := "change"
			if readOnly && !writable {
				what = "read"
			}
			text = fmt.Sprintf("%s %s %s, which holds qory's run records; the agent could %s them, so the run does not start. Mount a narrower path", p.place(mount), how, shown, what)
		default:
			// The key is at stake only for a place that is or contains the key's own file.
			key := p.runnerDir != "" && path == p.runnerDir
			if key {
				at := session.Overlap(mount, runnerdir.Dir(p.runnerDir).Path(runnerdir.SecretFile))
				key = at == "is" || at == "contains"
			}
			text = fmt.Sprintf("%s %s %s, which holds one of the runner's files; the agent could change it, so the run does not start. Mount a narrower path", p.place(mount), how, shown)
			if key && runnerdir.Dir(p.runnerDir).HasSecret() {
				text = fmt.Sprintf("%s %s %s, which holds this machine's access key; the agent could read the key, so the run does not start. Mount a narrower path", p.place(mount), how, shown)
			}
		}
	case ref.Code == codeMountModeConflict && len(ref.Names) == 2:
		inner, outer := ref.Names[0], ref.Names[1]
		inW, inR, inLast := p.modes(inner)
		outW, outR, _ := p.modes(outer)
		var innerWritable bool
		switch {
		case inW != inR:
			innerWritable = inW
		case inW && inner == outer:
			innerWritable = inLast
		case outW != outR:
			innerWritable = !outW
		case inW:
			innerWritable = inLast
		default:
			return nil
		}
		text = fmt.Sprintf("the mount %s (%s) lies inside %s, which is %s: a part of a mount can't have another mode, so the run does not start. Give both the same mode, or leave %s out", inner, mode(innerWritable), outer, mode(!innerWritable), inner)
	case ref.Code == codeMountSharedWithRun && len(ref.Names) == 3 && ref.Names[0] == p.spec.RunsDir:
		runs, other, otherPath := ref.Names[0], ref.Names[1], ref.Names[2]
		how := map[string]string{"is": "is", "lies inside": "lie inside", "contains": "contain"}[session.Overlap(runs, otherPath)]
		if how == "" {
			how = "overlap"
		}
		text = fmt.Sprintf("this run's records %s %s %s, which the run %s, still going on this machine, can write: its agent could change them, so the run does not start. Wait for %s to end, or keep qory's state directory out of its mounts", runs, how, otherPath, other, other)
	case ref.Code == codeMountSharedWithRun && len(ref.Names) == 3:
		path, other, otherPath := ref.Names[0], ref.Names[1], ref.Names[2]
		text = fmt.Sprintf("%s %s %s, which the run %s, still going on this machine, also mounts: one agent could change what the other mounts, so the run does not start. Wait for %s to end, or work in a checkout of its own", p.place(path), overlap(path, otherPath), otherPath, other, other)
		if info, err := os.Lstat(filepath.Join(path, ".git")); err == nil && info.Mode().IsRegular() {
			text += "; make the worktree beside the checkout, not inside it"
		}
	default:
		return nil
	}
	return &refusedError{text: text, err: &session.Refusal{Code: ref.Code}}
}

// overlap is how a place and a path stand to each other, as the runner judges it, and
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

// ownFiles is what qory passes the runner as its files: files, in order; links, which
// maps a link's place to the link, for the person; and records, the ones that guard the
// run records.
type ownFiles struct {
	files   []string
	links   map[string]string
	records map[string]bool
}

// runnerFiles is what qory passes the runner as its files: the configuration directory
// runnerDir, absolute, when there is one; then qory's state directory, which holds the
// run records, and what [recordLinks] adds for it and the runs directory runs; then
// what [configLinks] adds.
func runnerFiles(runnerDir, state, runs string) ownFiles {
	own := ownFiles{links: map[string]string{}, records: map[string]bool{}}
	if runnerDir != "" {
		own.files = append(own.files, runnerDir)
	}
	own.files = append(own.files, state)
	files, shown := recordLinks(state, runs)
	for _, f := range files {
		own.records[f] = true
	}
	own.files = append(own.files, files...)
	if runnerDir != "" {
		more, links := configLinks(runnerDir)
		for _, f := range more {
			if !slices.Contains(own.files, f) {
				own.files = append(own.files, f)
			}
		}
		maps.Copy(shown, links)
	}
	own.links = shown
	return own
}

// recordLinks is what qory passes the runner for the run records when a link is on the
// way to them: the path of the runs directory runs, absolute, under the state directory
// state, is followed one link at a time, as [configLinks] follows its files, and every
// link on the way outside state goes as its place, [linkPlace], since the agent could
// point it at a directory of its own; shown maps the place back to the link. Where the
// path leads goes too when a link takes it out of state, and the path as it is when it
// loops or cannot be read, which the runner then refuses.
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

// configLinks is what qory passes the runner for the files it reads from its
// configuration directory dir, absolute, when a link takes one out of it: dir itself,
// runner.yaml, qory.yaml or qory.yml, runtimes/ and the descriptors in it,
// runtimes/*.yaml in any case, as a disk that ignores case opens them. Each is followed
// from dir as given, one link at a time, each part of its path that is a link included,
// dir's own, and every link on the way and where it leads is one of the runner's files,
// since a mount of either would let the agent change what the next run reads. dir is
// followed even when it, or a part of it, does not exist yet: a link on the way could
// still be pointed at a directory of the agent's. The runner resolves its files through
// links, so a link goes as its place, [linkPlace], and shown maps that back to the link
// for the person. What lies in the resolved dir is left out: dir is one of the runner's
// files itself. A file whose chain ends at a part that does not exist yet is passed as
// it is, and the runner follows it to where the target will be; one whose chain loops
// or cannot be read is passed as it is too, and the runner, unable to resolve it,
// refuses the run.
func configLinks(dir string) (files []string, shown map[string]string) {
	// Without dir, nothing lies in it yet.
	resolved, err := filepath.EvalSymlinks(dir)
	exists := err == nil
	inside := func(p string) bool { return exists && within(resolved, p) }
	names := append([]string{".", config.RunnerFileName, DescriptorsDir}, config.Names...)
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
			// dir is passed itself, and the runner follows it.
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

// linkPlace is the link at path as the runner can compare it: its directory, and its
// name as a pattern that matches that one name. The runner resolves each of its files
// through links, and the link's own path to where it leads, which leaves the link
// unguarded. The pattern names no file, so the runner compares it by name: a mount of
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

// The runner's refusals of the places a walled run lists: one that is, contains or lies
// inside one of the runner's files; one inside another of the other mode; and one
// another walled run still going could change, or that holds one of its places.
const (
	codeMountContainsRunnerFiles = "mount_contains_runner_files"
	codeMountModeConflict        = "mount_mode_conflict"
	codeMountSharedWithRun       = "mount_shared_with_run"
)

// stateDir is qory's state directory, absolute: $XDG_STATE_HOME/qory, else
// ~/.local/state/qory when XDG_STATE_HOME is unset or not absolute.
func stateDir() (string, error) {
	if base := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(base) {
		return filepath.Join(base, "qory"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no state directory: set HOME or XDG_STATE_HOME")
	}
	return filepath.Abs(filepath.Join(home, ".local", "state", "qory"))
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
// of the runner's own is refused.
func passedVariables(names []string) ([]string, error) {
	var out []string
	for _, n := range names {
		if config.RunnersOwn(n) {
			return nil, input(fmt.Errorf("--env %s: the variable is the runner's own and never the session's", n))
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
func wallHelper(section config.RunnerWall, exe string) (string, error) {
	if section.Helper != "" {
		return section.Helper, nil
	}
	if runtime.GOOS != "linux" {
		return "", input(fmt.Errorf("the container runs qory's Linux build as its relay, its hook forwarder and what starts an image's own Docker, and this is the %s build; set wall.helper in %s to the Linux one", runtime.GOOS, config.RunnerFileName))
	}
	return exe, nil
}

// newResend builds the resend verb: the last step of a job that started a run, whatever
// happened before it.
func newResend() *cobra.Command {
	var wait time.Duration
	var secretFD int
	c := &cobra.Command{
		Use:   "resend <run-id>",
		Short: "Send a finished run's record to the server again",
		Long: `Send a finished run's record to the server in ` + config.RunnerFileName + ` again: after a runner that
died, or a server that was away. A job runs it last, whatever happened before.

Only what the server has not accepted is sent. A record the runner left open is closed
first, and the containers and networks its wall left are removed. A run that is still
running is refused.

The exit status is 0 when the server has everything, and 1 when events remain.

--verbose adds nothing here.

More: https://github.com/qoryai/qory/blob/main/docs/run.md#resending-a-runs-record`,
		Example: `  qory run resend "$run_id"             # the last step of a job
  qory run resend "$run_id" --wait 10m  # keep trying for ten minutes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var fdKey *accesskey.Key
			if cmd.Flags().Changed(secretFDFlag) {
				k, err := readSecretFD(secretFD)
				if err != nil {
					return err
				}
				fdKey = k
			}
			at, conf, err := locate(homeOptions{})
			if err != nil {
				return err
			}
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
			r := conf.Runner
			if r == nil || r.Server == nil {
				return input(fmt.Errorf("%s defines no server to send the record to", config.RunnerFileName))
			}
			id, err := identify(r, cmd.ErrOrStderr(), "run resend", fdKey)
			if err != nil {
				return err
			}
			spec := session.ResendSpec{
				Dir:           filepath.Join(runs, args[0]),
				Server:        sessionServer(r.Server),
				AccessKey:     id.key.key,
				InstanceID:    id.instanceID,
				InstanceName:  id.instanceName,
				RunnerVersion: build().title(),
				Report:        func(line string) { fmt.Fprintln(cmd.ErrOrStderr(), "qory run resend:", line) },
			}
			if r.Wall != nil {
				spec.Wall = &wall.Docker{Command: r.Wall.Command}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ctx, cancel := context.WithTimeout(ctx, wait)
			defer cancel()
			res, err := session.Resend(ctx, spec)
			switch {
			case errors.Is(err, session.ErrRunning):
				return input(fmt.Errorf("the run %s is running", args[0]))
			case errors.Is(err, os.ErrNotExist):
				return input(fmt.Errorf("no run %s is recorded in this checkout", args[0]))
			case err != nil:
				return explain(err, id)
			}
			u := ui.New(cmd.ErrOrStderr())
			if res.Closed {
				u.Success("the record had no exit and was closed with the reason runner_lost")
			}
			if res.Reaped > 0 {
				u.Success("removed %d containers and networks the run left", res.Reaped)
			}
			if res.Undelivered > 0 {
				u.Fail(fmt.Errorf("%d events were accepted and %d were not; %s/undelivered contains them", res.Sent, res.Undelivered, ui.Short(spec.Dir, at.root)))
				return reported(&exitError{code: 1})
			}
			u.Success("%d events were accepted; the server has the whole record", res.Sent)
			return nil
		},
	}
	c.Flags().DurationVar(&wait, "wait", 2*time.Minute, "how long to keep trying a server that does not accept")
	c.Flags().IntVar(&secretFD, secretFDFlag, 0, secretFDUsage)
	return c
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

// The arguments that select the hidden verbs the wall runs from qory's own binary: the
// relay, the start of a Docker of the agent's own, and the hook forwarder, which also
// runs on this machine without a wall. Each verb carries [inWall], so no look for a
// release runs inside a container.
var (
	relayArgs   = []string{"run", "relay"}
	nestArgs    = []string{"run", "nest"}
	forwardArgs = []string{"run", "forward"}
)

// inWall is the annotation of a verb the wall runs inside a container.
const inWall = "qory.dev/in-wall"

// newRelay builds the hidden relay verb, what the wall starts in the relay's container
// from qory's own binary: it listens on a port for each forward, port=host:port, and
// copies every connection to that address, the runner's proxy. It is the one peer the
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

// newNest builds the hidden nest verb, what the wall starts from qory's own binary as the
// entry point of a container whose image has a Docker of the agent's own: it runs as
// the container's root, which the runtime maps to a user of the machine's that is not
// root, starts dockerd on its socket alone, drops every capability and executes the
// launch as the agent's user. Its arguments, --user uid:gid, -- and the launch, go to
// [wall.Nest] as they are, so it parses no flag of its own. It returns only on an error.
func newNest() *cobra.Command {
	return &cobra.Command{
		Use:                "nest --user uid:gid -- command [argument...]",
		Short:              "Start the container's own Docker, then the agent as its user",
		Hidden:             true,
		Annotations:        map[string]string{inWall: "nest"},
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return wall.Nest(args)
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

// resolveLaunch is what qory harness launch and qory run share: the runtime, named or
// the one the harness is composed for, and its launch spec from the runtime's template
// with harness.launch over it, resolved against the home.
func resolveLaunch(rep report.Report, conf config.Config, runtime string) (string, render.Launch, error) {
	name := runtime
	if name == "" {
		if len(rep.Target.Runtimes) != 1 {
			return "", render.Launch{}, input(fmt.Errorf("the harness is composed for %s; --runtime selects which to start", strings.Join(rep.Target.Runtimes, ", ")))
		}
		name = rep.Target.Runtimes[0]
	}
	if !slices.Contains(rep.Target.Runtimes, name) {
		return "", render.Launch{}, input(fmt.Errorf("the harness is not composed for %s; composed: %s", name, strings.Join(rep.Target.Runtimes, ", ")))
	}
	rt, err := render.Lookup(name)
	if err != nil {
		return "", render.Launch{}, input(err)
	}
	launch, err := render.LaunchFor(rt, rep.Home, launchOverride(conf.Launch, name), report.ComposeVars(rep.LaunchEnv[name]))
	if err != nil {
		return "", render.Launch{}, input(err)
	}
	return name, launch, nil
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
