package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/session/runtimes"
	"github.com/qoryai/forager/wall"
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/preparation"
)

// agentEnvArgs are the arguments that select the hidden verb a dispatched walled run of
// Claude Code starts the agent through.
var agentEnvArgs = []string{"run", "agent-env"}

// keptFile is the name of the file of kept names in the folder [wrapAgentEnv] makes.
const keptFile = "kept"

// wrapsAgentEnv reports whether a run starts its agent through the agent-env verb: a
// dispatched run of Claude Code behind a wall. A test replaces it.
var wrapsAgentEnv = func(claudeDispatched, walled bool) bool { return claudeDispatched && walled }

// wrapAgentEnv starts the agent of a walled run through the agent-env verb, which
// removes the image's values of the [preparation.Dropped] names, keeping the ones
// Forager sets for the run and a prefixed name's value that is the gateway's
// placeholder. Forager unsets the runtime's declared, reserved and denied names and the
// node's denied ones when nothing sets them, but no other variable of the image: not
// the other names with one of [agentenv.Prefixes] or among [agentenv.Names], the proxy
// variables or the variables that point at authorities, which is why the verb drops
// them. Forager's environment file wins over the image's value for every name it sets,
// so the names it sets are the ones kept: the wall's, the session's, the runtime's
// declared and reserved variables and the ones it reads a model key and its model
// endpoint from, the harness's fixed values, and the run's variables
// as resolved, which are known once the gateway answered. They go, names alone, into a file in a folder qory
// makes for it, outside the checkout and the run directory, which the container sees
// read-only at the same path; the run's resolved names are added when Forager resolves
// them, before the wall starts. A gateway's placeholder whose name qory does not know is
// kept by its value when its name has one of [agentenv.Prefixes], see
// [preparation.AgentEnv]. This guards against the values the image's environment leaves
// in place, not against the image's programs or the repository's commands, which run as
// the agent's user in the agent's container and can change what the agent reads by other
// means; against them, the boundary is the wall. The verb's words, before the launch's,
// hold no -p, --print or --settings, so Forager reads the launch's arguments as before.
// It returns what removes the folder once the run is over.
func wrapAgentEnv(spec *runSpec, report func(string)) (func(), error) {
	d, ok := spec.Wall.(*wall.Docker)
	if !ok {
		return func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "qory-agent-env-")
	if err != nil {
		return nil, err
	}
	done := func() { _ = os.RemoveAll(tmp) }
	dir, err := filepath.EvalSymlinks(tmp)
	if err == nil {
		// Read by the container's user, whoever that is; written by this user alone.
		err = os.Chmod(dir, 0o755)
	}
	if err != nil {
		done()
		return nil, err
	}
	file := filepath.Join(dir, keptFile)
	always, ca := keptNames(spec, d)
	if err := writeKept(file, always, ca); err != nil {
		done()
		return nil, err
	}
	spec.Mounts = append(spec.Mounts, wall.Mount{Path: dir, ReadOnly: true})
	spec.Args = slices.Concat(agentEnvArgs, []string{file, "--", spec.Command}, spec.Args)
	spec.Command = wall.HelperPath
	before := spec.OnVariables
	spec.OnVariables = func(a session.Applied) {
		if before != nil {
			before(a)
		}
		resolved := slices.Clone(always)
		for _, v := range a {
			if v.From != "" {
				resolved = append(resolved, v.Name)
			}
		}
		if err := writeKept(file, resolved, ca); err != nil {
			report("the names of the run's variables could not be written, so the agent starts without the values of those among the names qory removes from the image's: " + err.Error())
		}
	}
	return done, nil
}

// keptNames are the names Forager sets for a walled run before the run's variables are
// resolved: always, the wall's proxy and socket names, QORY_RUN_ID, QORY_HARNESS_HOME
// with a harness home, the runtime's declared and reserved variables and the variables
// it reads a model key from, which Forager sets behind a wall to a placeholder or to a
// value passed for them, or unsets them, the variable it reads its model endpoint from,
// which Forager sets to the start key's endpoint or to a passed value its endpoint rule
// keeps, or unsets, and the harness's fixed values; and ca, the names the wall sets
// only when the run has an authority.
func keptNames(spec *runSpec, d *wall.Docker) (always, ca []string) {
	ca = d.CAEnv
	if ca == nil {
		ca = wall.DefaultCAEnv
	}
	for _, n := range d.Sets() {
		if !slices.Contains(ca, n) {
			always = append(always, n)
		}
	}
	always = append(always, session.EnvRunID)
	if spec.HarnessHome != "" {
		always = append(always, session.EnvHarnessHome)
	}
	always = append(always, credentialVariables(spec.Runtime)...)
	// The start key's endpoint, which Forager sets and the resolved variables list no
	// entry for when no source sets the name. Behind a wall, Forager unsets the image's
	// value of it when nothing sets it, before the verb runs.
	if m, ok := spec.Runtime.(runtimes.Model); ok {
		if model, ok := m.Model(); ok && model.Endpoint.Variable != "" {
			always = append(always, model.Endpoint.Variable)
		}
	}
	for _, kv := range spec.LaunchFixed {
		name, _, _ := strings.Cut(kv, "=")
		always = append(always, name)
	}
	return always, slices.Clone(ca)
}

// writeKept writes the file of kept names through a file beside it, renamed over it, so
// the container never reads half of one.
func writeKept(file string, always, ca []string) error {
	next := file + ".next"
	if err := os.WriteFile(next, preparation.KeptNames(always, ca), 0o644); err != nil {
		return err
	}
	if err := os.Chmod(next, 0o644); err != nil {
		return err
	}
	return os.Rename(next, file)
}

// newAgentEnv builds the hidden agent-env verb, what a dispatched walled run of Claude
// Code starts the agent through, after the setup commands when they run inside the wall:
// it first makes the agent's home fresh, [preparation.FreshHome], and fails on anything
// the image or the setup commands put there; it then reads the kept names from the file
// its first argument names, removes from its environment every [preparation.Dropped] name the file
// does not keep, but a prefixed name holding the gateway's placeholder, and executes the
// launch after -- with its arguments unchanged. It parses no flag. It prints nothing; on
// a failure it prints one line that names no value, and the agent does not start.
func newAgentEnv() *cobra.Command {
	return &cobra.Command{
		Use:                "agent-env file -- command [argument...]",
		Short:              "Start the agent without the image's values of the variables qory removes",
		Hidden:             true,
		Annotations:        map[string]string{inWall: "agent-env"},
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Last before the agent: its home, made fresh.
			err := preparation.FreshHome(os.Getenv)
			var path string
			var argv, env []string
			if err == nil {
				path, argv, env, err = agentEnvLaunch(args, os.Environ())
			}
			if err == nil {
				err = syscall.Exec(path, argv, env)
			}
			err = fmt.Errorf("qory run agent-env: %w", err)
			fmt.Fprintln(cmd.ErrOrStderr(), err)
			return reported(err)
		},
	}
}

// agentEnvLaunch is what the agent-env verb executes for its arguments, the kept names'
// file, -- and the launch: the program's path, found as the shell finds it, the launch
// as given, and env without the names it drops.
func agentEnvLaunch(args, env []string) (string, []string, []string, error) {
	if len(args) < 3 || args[1] != "--" {
		return "", nil, nil, errors.New("takes the file of kept names, --, and the command")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return "", nil, nil, err
	}
	keep, err := preparation.KeepFor(data, wall.BundlePath)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%s: %w", args[0], err)
	}
	argv := args[2:]
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return "", nil, nil, err
	}
	return path, argv, preparation.AgentEnv(env, keep), nil
}
