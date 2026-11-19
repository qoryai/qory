package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"time"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/runtimeinstall"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/stack"
)

// A run of Claude Code or Codex installs its runtime's program before its first session,
// once the compose named the runtime: [installRuntime] reads the release of the runtime
// that the composed stack pins, or else its newest release, from the runtime cache of
// the instance's registry when the machine has one, and from npm otherwise, and keeps
// each version in the runtimes folder, so a later run on the machine with the same
// version reads no tarball. qory run prints one line,
// "qory run: installed claude 2.1.320", and the session gets the version's folder,
// [useInstalledRuntime]. The run holds the version's folder until it ends.

// textNotPublished is the error of a run whose stack pins a version of its runtime that
// the source of the releases does not have: the fix, with the runtime and the name of the
// file that pins it, then the cause, with the runtime and the version. The file's name,
// qory.yaml or qory-stack.yaml, keeps the fix line within 100 characters.
const textNotPublished = "Set runtimes.%s.version in %s to a version the registry has.\n  The registry has no %s %s."

// textNoRuntimeProxy is the error of a run whose host proxy, through which qory reads the
// runtime, did not open: the fix line, then the cause, with the runtime and Forager's error.
const textNoRuntimeProxy = "Run again\nThe run's host proxy, through which qory installs %s, did not open:\n  %s"

// The seams of the install that a test replaces: the npm registry, whether a run
// installs at all, which the tests of other parts of qory run turn off, and where the
// run reads the address of its Qory Apiary instance.
var (
	npmRegistry     = runtimeinstall.NpmRegistry
	installRuntimes = true
	// runtimeKeys are the keys an install checks npm's signatures with; nil stands for
	// the keys built into qory.
	runtimeKeys []runtimeinstall.SigningKey
	instanceOf  = instanceAddress
	// installStandIn is nil outside the tests. A test sets it, with installRuntimes off,
	// to give a session of Claude Code the folder of a program in place of an install:
	// Forager joins no session of Claude Code without one. It gets the session's Command.
	installStandIn func(command string) (*runtimeinstall.Installed, error)
)

// runtimeRegistry is the instance's registry, as the runtime's install reads it from
// forager.yaml: the host of pool.registry, with its port, see
// [runtimeinstall.RegistryHost]; "" when forager.yaml sets no pool.registry.
func runtimeRegistry(r *config.Forager) string {
	if r == nil || r.Pool == nil {
		return ""
	}
	return runtimeinstall.RegistryHost(r.Pool.Registry)
}

// instanceAddress is the address of the Qory Apiary instance that the run uses, the URL
// of gateway.server in forager.yaml; "" for a run with no server there, such as a run
// from a run key. The registry's token realm is checked against it, see
// [runtimeinstall.RegistryFetch].
func instanceAddress(r *config.Forager) string {
	if r == nil || r.Server == nil {
		return ""
	}
	return r.Server.URL
}

// installRuntime installs the program of runtime, the runtime of the run's session, for
// the platform linux/<this machine's architecture>, the platform of the container
// engine. It returns nil for a runtime qory installs no program of. pin is the composed
// stack's pin of runtime, empty for none, which installs the newest release.
//
// The source is the registry of machine, the lines after a run code that qory run pool
// writes, or else the registry of r, [runtimeRegistry], or else npm. The registry's token
// realm must be the token route of the instance at machine's address, or else at r's,
// [instanceAddress]. The versions go to
// machine's folder, the runtimes folder that a pool's runs share, or else to
// <state>/runtimes. A walled run, and a run with a control plane, reads through the
// run's host proxy, proxied set; a run on a person's machine with neither sends its
// requests directly. The host proxy closes once the install ends.
func installRuntime(ctx context.Context, run openedRun, r *config.Forager, machine runtimeinstall.Machine, proxied bool, runtime string, pin stack.RuntimePin, state string, report func(string)) (*runtimeinstall.Installed, error) {
	if !installRuntimes || !slices.Contains(runtimeinstall.Runtimes(), runtime) {
		return nil, nil
	}
	registry := machine.Registry
	if registry == "" {
		registry = runtimeRegistry(r)
	}
	// A run from a run code takes the instance's address from the line that the pool
	// wrote after the code; any other run, from gateway.server.url in forager.yaml.
	instance := machine.Instance
	if instance == "" {
		instance = instanceOf(r)
	}
	folder := machine.Folder
	if folder == "" {
		folder = filepath.Join(state, "runtimes")
	}
	client := runtimeinstall.DirectClient()
	if proxied {
		h, err := run.HostProxy()
		if err != nil {
			return nil, fmt.Errorf(textNoRuntimeProxy, runtime, integration.Printable(exports.HideURLError(err).Error()))
		}
		secretmask.Default().AddURLPassword(h.proxy.URL)
		defer h.close()
		if client, err = runtimeinstall.ProxyClient(h.proxy.URL, h.proxy.CABundle); err != nil {
			return nil, err
		}
	}
	fetch := runtimeinstall.NpmFetch(client, npmRegistry, goruntime.GOARCH)
	if registry != "" {
		fetch = runtimeinstall.RegistryFetch(client, registry, goruntime.GOARCH, instance)
	}
	// A container of an earlier run may still run from a version's folder, with or without
	// a wall in this run's forager.yaml: the engine is the wall's command, else docker.
	command := ""
	if r != nil && r.Wall != nil {
		command = r.Wall.Command
	}
	opts := runtimeinstall.Options{Fetch: fetch, Runtime: runtime, Version: pin.Version, Folder: folder, Mounted: engineMounts(command), Progress: report, Keys: runtimeKeys}
	in, err := runtimeinstall.Install(ctx, opts)
	var missing *runtimeinstall.NotPublishedError
	if errors.As(err, &missing) {
		return nil, fmt.Errorf(textNotPublished, runtime, filepath.Base(pin.From), missing.Runtime, missing.Version)
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}

// installedLine is the line qory run prints once the runtime is installed.
func installedLine(in runtimeinstall.Installed) string {
	return "installed " + in.Runtime + " " + in.Version
}

// installFailed is err, an error of [installRuntime], with each line printable and
// every secret hidden: its first line is the fix.
func installFailed(err error) error {
	lines := strings.Split(strings.TrimSuffix(err.Error(), "\n"), "\n")
	for i, l := range lines {
		lines[i] = integration.Printable(exports.HideValues(l))
	}
	return errors.New(strings.Join(lines, "\n"))
}

// causeOnly is err, an install's error of a fix line and the cause on the lines after it,
// as a dispatched run shows it, under the user's ruling 771 (a): the cause alone, with no
// indent on its first line and its first letter in lower case, as [fetchImage] shows a
// refused image, and no full stop at its end, since [codeUsed] may follow. A long first
// line goes on after its first comma, on an indented line. Qory Apiary shows the run's
// output, and the fix line advises a person who runs qory. An error of one line stays as
// it is.
func causeOnly(err error) error {
	_, cause, ok := strings.Cut(err.Error(), "\n")
	if !ok || cause == "" {
		return err
	}
	cause = strings.TrimSuffix(lowerFirst(strings.TrimLeft(cause, " ")), ".")
	// A first line that, with "qory run: " and [codeUsed], has more than 100 characters
	// goes on at its first comma, with the words after it on an indented line.
	first, rest, _ := strings.Cut(cause, "\n")
	if before, after, ok := strings.Cut(first, ", "); ok && len("qory run: ")+len(first)+len(codeUsed) > 100 {
		cause = before + ",\n  " + after
		if rest != "" {
			cause += "\n" + rest
		}
	}
	return errors.New(cause)
}

// useInstalledRuntime is the one place where the session's spec gets the installed
// runtime, before [wrapAgentEnv] wraps the session's command for the wall. It sets
// RuntimeInstall to the version's folder and Command to the installed program, which
// Forager requires for a runtime whose descriptor has install_required, such as
// Claude Code. Forager binds the folder read-only behind the wall at
// /qory/runtime/<runtime> and writes the program's path there. A session that has no
// install keeps the Command of its launch, from the image.
func useInstalledRuntime(spec *runSpec, in *runtimeinstall.Installed) {
	spec.installed = in
	spec.RuntimeInstall = &session.RuntimeInstall{Dir: in.Dir, Version: in.Version}
	spec.Command = in.Program()
}

// engineMountsWait is the most time that the engine's commands of [engineMounts] take.
const engineMountsWait = 30 * time.Second

// engineMounts returns a function that lists the host folders that the running
// containers of the engine mount, through the program command, docker when it is empty.
// The install keeps a version's folder that one of them mounts, since the run that held
// it may have ended while its container goes on. A machine with no such program has no
// running container, so the function returns none for it. It returns an error for any
// other failure of the engine's commands, and the install then removes no version's
// folder. The commands get this process's environment without the access key's variables
// and the run key.
func engineMounts(command string) func(context.Context) ([]string, error) {
	if command == "" {
		command = "docker"
	}
	return func(ctx context.Context) ([]string, error) {
		ctx, cancel := context.WithTimeout(ctx, engineMountsWait)
		defer cancel()
		ps := exec.CommandContext(ctx, command, "ps", "--quiet", "--no-trunc")
		ps.Env = accesskey.WithoutVariables(os.Environ())
		ids, err := ps.Output()
		if errors.Is(err, exec.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing the engine's running containers failed: %w", err)
		}
		args := []string{"inspect", "--format", "{{range .Mounts}}{{println .Source}}{{end}}"}
		if len(strings.Fields(string(ids))) == 0 {
			return nil, nil
		}
		inspect := exec.CommandContext(ctx, command, append(args, strings.Fields(string(ids))...)...)
		inspect.Env = accesskey.WithoutVariables(os.Environ())
		out, err := inspect.Output()
		if err != nil {
			return nil, fmt.Errorf("reading the mounts of the engine's running containers failed: %w", err)
		}
		var mounts []string
		for _, line := range strings.Split(string(out), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				mounts = append(mounts, line)
			}
		}
		return mounts, nil
	}
}
