package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/runtimeinstall"
)

// A walled run installs its runtime's program before its first session, once the
// compose named the runtime: [installRuntime] fetches the runtime's newest release and
// keeps each version in <state>/runtimes/<runtime>/<version>, so a later run on the
// machine with the same version reads no tarball. qory run prints one line,
// "qory run: installed claude 2.1.320", and the session gets the version's folder,
// [useInstalledRuntime].

// installedRuntime is the runtime whose program qory run installs: claude, Forager's
// descriptor name of Claude Code.
const installedRuntime = "claude"

// fetchRuntime returns the newest release of the runtime for the run; nil until the
// source of the releases is decided, and qory run then installs nothing. A test sets it.
var fetchRuntime func(ctx context.Context, run openedRun, runtime string) (runtimeinstall.Release, error)

// installRuntime installs the program of runtime, the runtime of the run's session,
// into <state>/runtimes: [runtimeinstall.Install] with [fetchRuntime]. It returns nil
// for a runtime qory installs no program of, and when fetchRuntime is nil.
func installRuntime(ctx context.Context, run openedRun, runtime, state string) (*runtimeinstall.Installed, error) {
	fetch := fetchRuntime
	if fetch == nil || runtime != installedRuntime {
		return nil, nil
	}
	in, err := runtimeinstall.Install(ctx, runtimeinstall.Options{
		Fetch: func(ctx context.Context, name string) (runtimeinstall.Release, error) {
			return fetch(ctx, run, name)
		},
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
