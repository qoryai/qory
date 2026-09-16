package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/qoryai/runner/session"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/ui"
)

// The files qory run reads from the user's configuration directory when no flag names
// them, and the directory of runtime descriptor overrides beside them.
const (
	PolicyFile     = "policy.yaml"
	WebhookFile    = "webhook.yaml"
	DescriptorsDir = "runtimes"
)

// newRun builds the run verb, which starts a runtime on the composed harness through
// the session runner: the launch spec is what qory harness launch prints, the policy
// and the webhook are files in the user's configuration directory or named by flag, and
// the runner records the session under .qory/runs in the checkout. The verb is thin:
// it resolves the spec, hands it to the runner and exits with the runtime's status.
func newRun() *cobra.Command {
	var h homeOptions
	var policyPath, webhookPath string
	var local, headless bool
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

The policy is ` + PolicyFile + ` in the configuration directory, ~/.config/qory, or the
file --policy names; it can only narrow what the runtime reaches. No policy file means
every connection is allowed and recorded; a policy that does not read means no run.
The webhook is ` + WebhookFile + ` beside it, or the file --webhook names; when one is
configured the runner pings it first and does not start unless it answers, and posts
every event to it. --local runs with the files alone, webhook or not. A descriptor
override, <runtime>.yaml under ` + DescriptorsDir + ` in the same directory, replaces
the built-in description of how the runtime's output and hooks map to events.

At a terminal the session runs on a pseudo-terminal, so the runtime's own interface
works and its bytes are still captured; --headless, or no terminal, runs it on pipes and
reads its structured output. Either way the record is .qory/runs/<id>/ in the checkout:
events.jsonl, one event per line, and output.log, the session's bytes. The exit status
is the runtime's.

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
			if policyPath == "" {
				policyPath = present(filepath.Join(user, PolicyFile))
			}
			if webhookPath == "" {
				webhookPath = present(filepath.Join(user, WebhookFile))
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			stderr := cmd.ErrOrStderr()
			spec := session.Spec{
				Runtime:       name,
				Command:       launch.Command,
				Args:          append(append([]string{}, launch.Args...), extra...),
				Env:           withEnv(os.Environ(), launch.Env),
				Dir:           cwd,
				Interactive:   !headless && isTerminal(cmd.InOrStdin()) && isTerminal(cmd.OutOrStdout()),
				Stdin:         cmd.InOrStdin(),
				Stdout:        cmd.OutOrStdout(),
				Stderr:        stderr,
				PolicyPath:    policyPath,
				WebhookPath:   webhookPath,
				Local:         local,
				RunsDir:       filepath.Join(at.root, ".qory", "runs"),
				Descriptors:   filepath.Join(user, DescriptorsDir),
				Forwarder:     []string{exe, "run", "forward"},
				RunnerVersion: build().title(),
			}
			res, err := session.Run(ctx, spec)
			if err != nil {
				return err
			}
			u := ui.New(stderr)
			record := ui.Short(res.Dir, at.root)
			if res.Undelivered > 0 {
				u.Fail(fmt.Errorf("%d events did not reach the webhook; %s/undelivered holds them", res.Undelivered, record))
			}
			switch {
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
	c.Flags().StringVar(&policyPath, "policy", "", "the policy file; "+PolicyFile+" in the configuration directory when left out")
	c.Flags().StringVar(&webhookPath, "webhook", "", "the webhook configuration; "+WebhookFile+" in the configuration directory when left out")
	c.Flags().BoolVar(&local, "local", false, "record to files only, even when a webhook is configured")
	c.Flags().BoolVar(&headless, "headless", false, "run on pipes even at a terminal, and read the runtime's structured output")
	homeFlags(c, &h)
	c.AddCommand(newForward())
	return c
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

// present is path when a file is there, else "".
func present(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
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
