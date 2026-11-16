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

// A walled run installs its runtime's program before its first session, once the
// compose named the runtime: [installRuntime] reads the runtime's newest release from
// the runtime cache of the instance's registry, through the run's host proxy, and keeps
// each version in <state>/runtimes/<runtime>/<version>, so a later run on the machine
// with the same version reads no tarball. qory run prints one line,
// "qory run: installed claude 2.1.320", and the session gets the version's folder,
// [useInstalledRuntime].

// installedRuntime is the runtime whose program qory run installs: claude, Forager's
// descriptor name of Claude Code.
const installedRuntime = "claude"

// textNoRuntimeProxy is the line of a run whose host proxy, through which qory reads the
// runtime from the registry, did not open, with the runtime and Forager's error after it.
const textNoRuntimeProxy = "run again: the run's host proxy, through which qory installs %s, did not open: %s"

// runtimeRegistry is the instance's registry, as the runtime's install reads it: the
// host of pool.registry in forager.yaml, with its port; "" when forager.yaml sets no
// pool.registry, or when its first part is no host, one with a dot or a colon, or
// localhost.
func runtimeRegistry(r *config.Forager) string {
	if r == nil || r.Pool == nil {
		return ""
	}
	host, rest, ok := strings.Cut(r.Pool.Registry, "/")
	if !ok || rest == "" || !strings.ContainsAny(host, ".:") && host != "localhost" {
		return ""
	}
	return host
}

// installRuntime installs the program of runtime, the runtime of the run's session,
// into <state>/runtimes, from the registry of r, [runtimeRegistry], through the run's
// host proxy, for the platform linux/<this machine's architecture>, the platform of
// the container engine. It returns nil for a runtime qory installs no program of, and
// when r sets no registry. The host proxy closes once the install ends.
func installRuntime(ctx context.Context, run openedRun, r *config.Forager, runtime, state string) (*runtimeinstall.Installed, error) {
	registry := runtimeRegistry(r)
	if runtime != installedRuntime || registry == "" {
		return nil, nil
	}
	h, err := run.HostProxy()
	if err != nil {
		return nil, fmt.Errorf(textNoRuntimeProxy, runtime, integration.Printable(exports.HideURLError(err).Error()))
	}
	secretmask.Default().AddURLPassword(h.proxy.URL)
	defer h.close()
	client, err := runtimeinstall.ProxyClient(h.proxy.URL, h.proxy.CABundle)
	if err != nil {
		return nil, err
	}
	in, err := runtimeinstall.Install(ctx, runtimeinstall.Options{
		Fetch:   runtimeinstall.RegistryFetch(client, registry, goruntime.GOARCH),
		Runtime: runtime,
		Folder:  filepath.Join(state, "runtimes"),
	})
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
