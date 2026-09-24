package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/qoryai/runner/runtimes/catalog"
	"github.com/qoryai/runner/session"
	"github.com/qoryai/runner/wall"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/ui"
)

// DescriptorsDir is the directory of runtime descriptor overrides, <runtime>.yaml,
// under the user's configuration directory.
const DescriptorsDir = "runtimes"

// newRun builds the run verb, which starts a runtime on the composed harness through
// the session runner: the launch spec is what qory harness launch prints, the policy
// and the server are the runner file in the user's configuration directory or named
// by flag, and the runner records the session under .qory/runs in the checkout. The verb is thin:
// it resolves the spec, hands it to the runner and exits with the runtime's status.
func newRun() *cobra.Command {
	var h homeOptions
	var local, headless bool
	var o wallOptions
	var policyFile, runID string
	var labels []string
	var timeout, grace time.Duration
	var stopSignal string
	c := &cobra.Command{
		Use:   "run [runtime] [-- argument...]",
		Short: "Start a runtime on the composed harness, observed and recorded",
		Long: `Start a runtime on the composed harness, the way qory harness launch would, inside
the session runner: every connection the runtime makes goes through a proxy on this
machine and is recorded, and the session's output, the runner's observations and the
runtime's own reports are written as events. The runtime is the one the harness is
composed for, or named as the first argument when it is composed for several;
arguments after -- go to the runtime after the launch template's own, so
qory run claude -- -p 'say hello' runs one headless turn.

What the runner does on this machine is ` + config.RunnerFileName + ` in the configuration
directory, ~/.config/qory, and nowhere else: a repository cannot set it. Its egress
section is the policy, which can only narrow what the runtime reaches: its allow list,
and its deny list, whose hosts are denied in either mode, under observe as under
enforce, whatever allow says. No section means every connection is allowed and
recorded, and a file that does not read means no run.
The hosts the harness declares, its modules' and the runtime's in the report, are
reported beside the policy as harness_hosts and narrow nothing; the policy alone says
what the runtime reaches. --policy names one run's own policy, a file in the runner
contract's policy format kept outside the checkout, for a machine without a server
that serves runs of different kinds. It narrows only: under a section in mode enforce
the run reaches the file's hosts the section covers, and with no section, or one in
mode observe, the file stands as it is; the deny lists of both hold either way. The
file's server section names the server
every run reports to, with the access key and the secret the server issued this
machine: the runner fetches the server's configuration first, signed, and does not
start unless the server answers; the events go where the configuration says, and when
it names a run configuration that is the run's policy, fetched with the run's labels,
the checkout's forge and repository among them, and reloaded when the server says it
changed, so --policy is refused.
--local runs with the files alone and the machine's policy; the server is not
contacted.

Any runtime the harness is composed for runs this way. What qory run knows of one, how
its hooks are installed, what its output means and which signal asks it to leave, is a
descriptor in the runner contract's format: the runner's own, Claude Code's today, or
<runtime>.yaml under ` + DescriptorsDir + ` in the same directory, which describes a
runtime the runner ships nothing for or replaces what it ships. A runtime with neither
runs all the same: the run, its log and its egress are recorded, the events of the
session inside it are not.

A wall starts the runtime in a container with no route out except to that proxy, so a
program that ignores the proxy reaches nothing instead of going unseen: --wall docker,
or a wall section in ` + config.RunnerFileName + `, and --wall none for one run without the
section's. --image, or wall.image, names the container's image, which holds the runtime
and the project's toolchain; qory builds none. The container sees the checkout and the
composed home, at their own paths, and nothing else of this machine; of the environment
it gets the launch template's variables and the ones --env or wall.env names, the model
credential say, and nothing else. --mount, or wall.mounts, shows it more of this machine
at its own path, a sibling checkout say, with :ro after the path for what it must not
change; never a socket. --cpus, --memory, --pids-limit and --shm-size, or the keys of
the same names under wall, limit what it uses; a browser wants more /dev/shm than an
engine gives by default. Inside, the relay and the hook forwarder are qory's
own Linux build, mounted read-only: this binary on Linux, wall.helper elsewhere. With
the engine in a virtual machine, on a Mac, the runtime's hooks do not reach the runner.

At a terminal the session runs on a pseudo-terminal, so the runtime's own interface
works and its bytes are still captured; --headless, or no terminal, runs it on pipes and
reads its structured output. An argument the runtime's descriptor names as headless,
-p for Claude Code, runs it on pipes as well, since with it the runtime has no interface
whoever started it: qory run claude -- -p '…' needs no flag. Either way the record is
.qory/runs/<id>/ in the checkout:
events.jsonl, one event per line, and output.log, the session's bytes. The exit status
is the runtime's. qory run resend sends a finished run's record to the server again,
after a runner that died or a server that was away.

A run holds no credential it can be spared. The credentials section of ` + config.RunnerFileName + `
defines what this machine has: a token from a variable of qory's environment, from a
file, or from an adapter, a program of yours that knows one kind of host, a source code
host say, and prints the token with the hosts, the scheme and the paths it is for. A
run's policy selects credentials by name, with an argument for an adapter, a repository
say, and defines none. Behind a wall the runner keeps each outside the container and
its proxy sets it on the requests to the hosts it is for, ending the container's TLS
for those hosts alone with an authority made for the run, which the container is given
to trust beside its image's own. Of those hosts the run reaches the paths the
credential names and no other, another organization's repositories say, and every
other host stays a tunnel nobody reads. The policy's egress.paths holds a host to
paths the same way with no credential.

A caller that starts runs for a system of its own names them: --run-id gives the run
the id the caller already holds, a UUID in lower case, and --label key=value, repeatable,
puts the caller's own names, a key in a queue, a repository, an issue, into
ai.qory.run.started and onto the run configuration request, where a server finds them.
Two come from the checkout's origin remote unless --label names them: forge, the
remote's host, and repository, its path without the leading slash and .git, github.com
and acme/shop say; a checkout with no remote, or one on this machine, carries neither.
--timeout stops a runtime that still runs after that long, 5h30m say: ai.qory.run.exited says the limit was the reason, and
the exit status is ` + fmt.Sprint(exitTimeout) + `, as timeout(1) has it. Stopped at the limit or by a signal to
qory run, the runtime gets --stop-signal, SIGTERM unless named or the runtime's descriptor names one, and, --stop-grace later,
10s unless named, SIGKILL: the time a session needs to close what it has open. Runtimes
differ in what a signal means, one closes its session on SIGINT and drops it on SIGTERM,
so the signal is yours to name: SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1 or SIGUSR2.
run.timeout, run.stop_signal and run.stop_grace in ` + config.RunnerFileName + ` set them for
every run on the machine; --timeout 0 lifts the file's.

--verbose adds nothing here.`,
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
					server = &session.Server{Version: 1, URL: r.Server.URL, AccessKey: r.Server.AccessKey, Secret: r.Server.Secret}
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
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			stderr := cmd.ErrOrStderr()
			spec := session.Spec{
				Runtime:       rt,
				Command:       launch.Command,
				Args:          append(append([]string{}, launch.Args...), extra...),
				Env:           withEnv(withoutRunners(os.Environ()), launch.Env),
				Dir:           cwd,
				Interactive:   !headless && isTerminal(cmd.InOrStdin()) && isTerminal(cmd.OutOrStdout()),
				Stdin:         cmd.InOrStdin(),
				Stdout:        cmd.OutOrStdout(),
				Stderr:        stderr,
				Policy:        pol,
				Server:        server,
				Local:         local,
				Declared:      rep.Hosts(),
				RunsDir:       filepath.Join(at.root, ".qory", "runs"),
				Forwarder:     []string{exe, "run", "forward"},
				RunnerVersion: build().title(),
				RunID:         runID,
				Labels:        named,
				Timeout:       timeout,
				StopSignal:    stopSignal,
				StopGrace:     grace,
			}
			if r := conf.Runner; r != nil {
				for _, c := range r.Credentials {
					def := session.Credential{Name: c.Name, Env: c.Env, File: c.File, Adapter: c.Adapter, Argument: c.Argument, Hosts: c.Hosts, Scheme: c.Scheme, Username: c.Username, Header: c.Header, Paths: c.Paths, Placeholders: c.Placeholders}
					if err := def.Check(); err != nil {
						return input(fmt.Errorf("%s: %w", config.RunnerFileName, err))
					}
					spec.Credentials = append(spec.Credentials, def)
				}
			}
			if err := enclose(&spec, conf.Runner, o, exe, at.root, rep.Home, launch.Env); err != nil {
				return err
			}
			if policyFile != "" {
				for _, m := range spec.Mounts {
					if abs, _ := filepath.Abs(policyFile); !m.ReadOnly && reallyWithin(m.Path, abs) {
						return input(fmt.Errorf("--policy %s is inside %s, which the container may write; keep it outside or mount that read-only", policyFile, m.Path))
					}
				}
			}
			res, err := session.Run(ctx, spec)
			if err != nil {
				return err
			}
			u := ui.New(stderr)
			record := ui.Short(res.Dir, at.root)
			if res.Undelivered > 0 {
				u.Fail(fmt.Errorf("%d events did not reach the server; %s/undelivered holds them", res.Undelivered, record))
			}
			switch {
			case res.TimedOut:
				u.Fail(fmt.Errorf("%s was stopped at the limit of %s; recorded in %s", name, timeout, record))
				return reported(&exitError{code: exitTimeout})
			case res.Signal != "":
				u.Fail(fmt.Errorf("%s was ended by %s; recorded in %s", name, res.Signal, record))
				return reported(&exitError{code: 1})
			case res.ExitCode != 0:
				u.Fail(fmt.Errorf("%s exited %d; recorded in %s", name, res.ExitCode, record))
				return reported(&exitError{code: res.ExitCode})
			}
			u.Success("%s exited 0; recorded in %s", name, record)
			return nil
		},
	}
	c.Flags().BoolVar(&local, "local", false, "record to files only and run under the machine's policy, even when a server is configured; the server is not contacted")
	c.Flags().BoolVar(&headless, "headless", false, "run on pipes even at a terminal, and read the runtime's structured output; implied by an argument the runtime's descriptor names as headless, -p for claude")
	c.Flags().StringVar(&policyFile, "policy", "", "this run's own policy, a file outside the checkout in the runner contract's policy format; it narrows the egress section of "+config.RunnerFileName+" and never widens it, and is refused with a server configured unless --local")
	c.Flags().StringVar(&runID, "run-id", "", "the run's id when the caller already holds one: a UUID in lower case (default a new one)")
	c.Flags().StringArrayVar(&labels, "label", nil, "the caller's own name for the run, key=value, reported in ai.qory.run.started; repeatable. forge and repository come from the origin remote unless named")
	c.Flags().DurationVar(&timeout, "timeout", 0, "stop a runtime that still runs after this long, 5h30m say, and exit "+fmt.Sprint(exitTimeout)+" (default no limit; "+config.RunnerFileName+": run.timeout)")
	c.Flags().StringVar(&stopSignal, "stop-signal", "", "the signal that asks the runtime to leave when the runner stops it: SIGTERM, SIGINT, SIGHUP, SIGQUIT, SIGUSR1 or SIGUSR2 (default SIGTERM; "+config.RunnerFileName+": run.stop_signal)")
	c.Flags().DurationVar(&grace, "stop-grace", 0, "how long the runtime gets between the stop signal and SIGKILL when the runner stops it (default 10s; "+config.RunnerFileName+": run.stop_grace)")
	c.Flags().StringVar(&o.name, "wall", "", "start the runtime in a container with no route out except to the proxy: "+config.WallDocker+", or none ("+config.RunnerFileName+": wall.adapter)")
	c.Flags().StringVar(&o.image, "image", "", "the container's image under a wall ("+config.RunnerFileName+": wall.image)")
	c.Flags().StringArrayVar(&o.env, "env", nil, "a variable of this environment that goes into the container under a wall, by name; repeatable ("+config.RunnerFileName+": wall.env)")
	c.Flags().StringArrayVar(&o.mounts, "mount", nil, "a file or directory of this machine the container sees as well, at its own path, with :ro after it for one it cannot change; repeatable ("+config.RunnerFileName+": wall.mounts)")
	c.Flags().StringVar(&o.limits.CPUs, "cpus", "", "how many processors' worth of time the container gets ("+config.RunnerFileName+": wall.cpus)")
	c.Flags().StringVar(&o.limits.Memory, "memory", "", "the most memory the container gets, 8g say ("+config.RunnerFileName+": wall.memory)")
	c.Flags().IntVar(&o.limits.PIDs, "pids-limit", 0, "the most processes and threads in the container ("+config.RunnerFileName+": wall.pids_limit)")
	c.Flags().StringVar(&o.limits.ShmSize, "shm-size", "", "the size of /dev/shm in the container, 2g say ("+config.RunnerFileName+": wall.shm_size)")
	homeFlags(c, &h)
	c.AddCommand(newResend(), newForward(), newRelay())
	return c
}

// wallOptions are the run verb's wall flags.
type wallOptions struct {
	name   string
	image  string
	env    []string
	mounts []string
	limits wall.Limits
}

// walled reports whether a flag that means something only behind a wall was given.
func (o wallOptions) walled() bool {
	return o.image != "" || len(o.env) > 0 || len(o.mounts) > 0 || o.limits != (wall.Limits{})
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
			return nil, input(fmt.Errorf("--label %s is given twice", k))
		}
		out[k] = v
	}
	return out, nil
}

// withoutRunners is the environment without the runner's own variables, which are never
// the session's: with the server's secret a session could sign requests of its own.
func withoutRunners(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); name != config.EnvServerSecret {
			out = append(out, kv)
		}
	}
	return out
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

// wallOff is the --wall value that runs without the wall the runner file names.
const wallOff = "none"

// enclose puts the spec behind a wall when a flag or the runner file asks for one. The
// runtime then runs in a container, so everything that named this machine is renamed:
// the environment is the launch template's and the named variables, never the
// process's; the forwarder is the helper's path inside; and the container is shown the
// checkout and the composed home, which is all a launch template's paths point into.
func enclose(spec *session.Spec, r *config.Runner, o wallOptions, exe, root, home string, launchEnv map[string]string) error {
	var section config.RunnerWall
	if r != nil && r.Wall != nil {
		section = *r.Wall
	}
	name := o.name
	if name == "" {
		name = section.Adapter
	}
	if name == "" || name == wallOff {
		if o.walled() {
			return input(fmt.Errorf("--image, --env, --mount and the limits are for a run behind a wall; --wall %s starts one", config.WallDocker))
		}
		return nil
	}
	if name != config.WallDocker {
		return input(fmt.Errorf("--wall %s: the walls are %s, and %s for a run without one", name, config.WallDocker, wallOff))
	}
	spec.Image = o.image
	if spec.Image == "" {
		spec.Image = section.Image
	}
	if spec.Image == "" {
		return input(fmt.Errorf("a wall needs the container's image: --image, or wall.image in %s; qory builds none", config.RunnerFileName))
	}
	env := withEnv(nil, launchEnv)
	for _, n := range append(append([]string{}, section.Env...), o.env...) {
		if n == config.EnvServerSecret {
			return input(fmt.Errorf("--env %s: the variable is the runner's own and never the session's", n))
		}
		if v, ok := os.LookupEnv(n); ok {
			env = withEnv(env, map[string]string{n: v})
		}
	}
	if env == nil {
		env = []string{}
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
	helper := section.Helper
	if helper == "" {
		if runtime.GOOS != "linux" {
			return input(fmt.Errorf("the container runs qory's Linux build as its relay and hook forwarder, and this is the %s build; name the Linux one as wall.helper in %s", runtime.GOOS, config.RunnerFileName))
		}
		helper = exe
	}
	spec.Env = env
	spec.Wall = &wall.Docker{Command: section.Command, Helper: helper, RelayArgs: []string{"run", "relay"}, User: section.User, CAEnv: section.CAEnv}
	spec.Forwarder = []string{wall.HelperPath, "run", "forward"}
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

// newResend builds the resend verb: the last step of a job that started a run, whatever
// happened before it.
func newResend() *cobra.Command {
	var wait time.Duration
	c := &cobra.Command{
		Use:   "resend <run-id>",
		Short: "Send a finished run's record to the server again, completing it first",
		Long: `Send the record of a run that is over to the server of ` + config.RunnerFileName + `, for a run
whose runner died or whose server was away: the step a job runs last, whatever
happened before it. The run is named by its id, the directory under .qory/runs in this
checkout. The server's configuration is fetched first, signed, and says where the
events go.

The run directory says what the server accepted, so only the rest is sent, in order,
until it is accepted or --wait is over. A record with no ai.qory.run.exited, which a
runner that died leaves, gets one first, with the reason runner_lost, and the
containers and networks the run's wall left are removed. A run whose runner still
lives is refused. A server may see an event twice and discards it by its id.

The exit status is 0 when the server has everything, 1 when events remain, which
are under the run directory's undelivered then.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			at, conf, err := locate(homeOptions{})
			if err != nil {
				return err
			}
			if err := session.CheckRunID(args[0]); err != nil {
				return input(err)
			}
			r := conf.Runner
			if r == nil || r.Server == nil {
				return input(fmt.Errorf("%s names no server to send the record to", config.RunnerFileName))
			}
			spec := session.ResendSpec{
				Dir:           filepath.Join(at.root, ".qory", "runs", args[0]),
				Server:        &session.Server{Version: 1, URL: r.Server.URL, AccessKey: r.Server.AccessKey, Secret: r.Server.Secret},
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
				return input(fmt.Errorf("the run %s is still going", args[0]))
			case errors.Is(err, os.ErrNotExist):
				return input(fmt.Errorf("no run %s is recorded in this checkout", args[0]))
			case err != nil:
				return err
			}
			u := ui.New(cmd.ErrOrStderr())
			if res.Closed {
				u.Success("the record had no exit and was closed with the reason runner_lost")
			}
			if res.Reaped > 0 {
				u.Success("removed %d containers and networks the run left", res.Reaped)
			}
			if res.Undelivered > 0 {
				u.Fail(fmt.Errorf("%d events were accepted and %d still are not; %s/undelivered holds them", res.Sent, res.Undelivered, ui.Short(spec.Dir, at.root)))
				return reported(&exitError{code: 1})
			}
			u.Success("%d events were accepted; the server has the whole record", res.Sent)
			return nil
		},
	}
	c.Flags().DurationVar(&wait, "wait", 2*time.Minute, "how long to keep trying a server that does not accept")
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

// newRelay builds the hidden relay verb, what the wall starts in the relay's container
// from qory's own binary: it listens on a port for each forward, port=host:port, and
// copies every connection to that address, the runner's proxy. It is the one peer the
// runtime's container reaches.
func newRelay() *cobra.Command {
	return &cobra.Command{
		Use:    "relay port=host:port...",
		Short:  "Forward the wall's fixed ports to the run's proxy",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return wall.Relay(ctx, args, cmd.OutOrStdout())
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
		Use:    "forward",
		Short:  "Forward a hook's input to the run that installed it",
		Hidden: true,
		Args:   noArgs,
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
			return "", render.Launch{}, input(fmt.Errorf("the harness is composed for %s; --runtime says which to start", strings.Join(rep.Target.Runtimes, ", ")))
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
	var override *render.Template
	if l, ok := conf.Launch[name]; ok {
		override = &render.Template{Command: l.Command, Args: l.Args, Env: l.Env}
	}
	launch, err := render.LaunchFor(rt, rep.Home, override)
	if err != nil {
		return "", render.Launch{}, input(err)
	}
	return name, launch, nil
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

// Error names the status.
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
