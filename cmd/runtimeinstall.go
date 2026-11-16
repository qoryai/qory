package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/runtimeinstall"
	"github.com/qoryai/qory/internal/secretmask"
)

// A run of Claude Code installs its runtime's program before its first session, once
// the compose named the runtime: [installRuntime] reads the runtime's newest release,
// from the runtime cache of the instance's registry when the machine has one, and from
// npm otherwise, and keeps each version in the runtimes folder, so a later run on the
// machine with the same version reads no tarball. qory run prints one line,
// "qory run: installed claude 2.1.320", and the session gets the version's folder,
// [useInstalledRuntime]. The run holds the version's folder until it ends.

// installedRuntime is the runtime whose program qory run installs: claude, Forager's
// descriptor name of Claude Code. qory installs no other runtime.
const installedRuntime = "claude"

// textNoRuntimeProxy is the line of a run whose host proxy, through which qory reads the
// runtime, did not open, with the runtime and Forager's error after it.
const textNoRuntimeProxy = "run again: the run's host proxy, through which qory installs %s, did not open: %s"

// The seams of the install that a test replaces: the npm registry, whether a run
// installs at all, which the tests of other parts of qory run turn off, and where the
// run reads the address of its Qory Apiary instance.
var (
	npmRegistry     = runtimeinstall.NpmRegistry
	installRuntimes = true
	instanceOf      = instanceAddress
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
// of gateway.server in forager.yaml; "" for a run with no server there, such as a run of
// a pool, whose runs' forager.yaml has no server section, and a run from a run key. The
// registry's token realm is checked against it, see [runtimeinstall.RegistryFetch].
func instanceAddress(r *config.Forager) string {
	if r == nil || r.Server == nil {
		return ""
	}
	return r.Server.URL
}

// installRuntime installs the program of runtime, the runtime of the run's session, for
// the platform linux/<this machine's architecture>, the platform of the container
// engine. It returns nil for a runtime qory installs no program of.
//
// The source is the registry of machine, the lines after a run code that qory run pool
// writes, or else the registry of r, [runtimeRegistry], or else npm. The versions go to
// machine's folder, the runtimes folder that a pool's runs share, or else to
// <state>/runtimes. A walled run, and a run with a control plane, reads through the
// run's host proxy, proxied set; a run on a person's machine with neither sends its
// requests directly. The host proxy closes once the install ends.
func installRuntime(ctx context.Context, run openedRun, r *config.Forager, machine runtimeinstall.Machine, proxied bool, runtime, state string) (*runtimeinstall.Installed, error) {
	if !installRuntimes || runtime != installedRuntime {
		return nil, nil
	}
	registry := machine.Registry
	if registry == "" {
		registry = runtimeRegistry(r)
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
		fetch = runtimeinstall.RegistryFetch(client, registry, goruntime.GOARCH, instanceOf(r))
	}
	in, err := runtimeinstall.Install(ctx, runtimeinstall.Options{Fetch: fetch, Runtime: runtime, Folder: folder})
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

// useInstalledRuntime is the one place where the session's spec gets the installed
// runtime, before [wrapAgentEnv] wraps the session's command for the wall. It keeps
// the install in the spec.
//
// Pin for F-08: once qory pins a Forager whose SessionSpec has RuntimeInstall, this
// function also sets
//
//	spec.RuntimeInstall = &session.RuntimeInstall{Dir: in.Dir, Version: in.Version}
//	spec.Command = in.Program()
//
// and Forager binds Dir read-only behind the wall and writes the program's path there.
func useInstalledRuntime(spec *runSpec, in *runtimeinstall.Installed) {
	spec.installed = in
}
