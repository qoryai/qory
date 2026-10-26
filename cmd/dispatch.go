package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/render"
)

// dispatchedArgs are the launch arguments of a dispatched run of rt, one given a run
// key: the runtime's [render.Dispatcher] arguments, and args as given for a runtime that
// is none.
func dispatchedArgs(rt render.Runtime, args []string) []string {
	if d, ok := rt.(render.Dispatcher); ok {
		return d.DispatchedArgs(args)
	}
	return args
}

// withAgentHome is the fixed variables of a dispatched walled run of a runtime that is a
// [render.AgentHomer], with its variable, name, pointing at [preparation.AgentHome],
// which qory run agent-env makes new and empty as the last step before the agent starts
// when name is [preparation.EnvAgentHome], Claude Code's.
// A fixed value wins over every other source, and the runtime's deny list leaves it in.
func withAgentHome(fixed []string, name string) []string {
	out := slices.DeleteFunc(slices.Clone(fixed), func(kv string) bool {
		return strings.HasPrefix(kv, name+"=")
	})
	out = append(out, name+"="+preparation.AgentHome)
	slices.Sort(out)
	return out
}

// homeInFolder stops a run given a run key whose harness home is inside the folder at
// root, the folder the run runs in, by their paths or with their links resolved: such
// a run writes nothing into that folder, no home, no link and no exclude line, since
// the repository's files and commands can change what is there.
func homeInFolder(root, home string) error {
	if !within(root, home) && !within(realPath(root), realPath(home)) {
		return nil
	}
	return input(fmt.Errorf("set --home to a folder outside %[1]s: %[2]s is inside %[1]s, and a run with a run key writes no harness into the folder it runs in", exports.Value(root, false), exports.Value(home, false)))
}

// runKeyHomes is the root of the homes of runs given a run key when neither --home nor
// harness.home sets one: qory/homes under the user's cache directory, ~/Library/Caches
// on macOS and $XDG_CACHE_HOME or ~/.cache on Linux, one home per checkout under it. It
// is not under qory's state directory: a walled run mounts its home, and Forager stops
// a run with a mount inside that directory, which contains the run records. root is the
// checkout the run runs in, which the error of a failed lookup prints.
func runKeyHomes(root string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", input(fmt.Errorf("set HOME, or give --home a folder outside %s: a run with a run key composes its harness under the cache directory, and the lookup of that directory failed: %w", exports.Value(root, false), err))
	}
	return filepath.Join(base, "qory", "homes"), nil
}

// withRunKeyHome is conf with the home of a run given a run key in the checkout at root:
// harness.home, else [runKeyHomes].
func withRunKeyHome(root string, conf config.Config) (config.Config, error) {
	if conf.Home != "" {
		return conf, nil
	}
	homes, err := runKeyHomes(root)
	if err != nil {
		return conf, err
	}
	conf.Home = homes
	return conf, nil
}

// locateRun is [locate] for qory run and qory run resend: given a run key or a run code,
// keyed, the checkout's qory.yaml sets no machine key, harness.launch and env among
// them, as under extends, so the repository a dispatched run works on does not configure
// how it starts. composes is set for qory run, which composes the harness: the home is
// outside the checkout unless --home or harness.home puts it inside, see
// [withRunKeyHome], and a home inside the checkout or inside the working directory stops
// the run with [homeInFolder] before any other check of the home, and before anything
// is written.
func locateRun(o homeOptions, keyed, composes bool) (places, config.Config, error) {
	if !keyed {
		return locate(o)
	}
	var check func(root, home string) error
	if composes {
		check = homeInFolders
	}
	return locateWith(o, func(root string) (config.Config, error) {
		if err := configInCheckout(root); err != nil {
			return config.Defaults(), input(err)
		}
		conf, err := config.Load(root, false)
		if err != nil {
			return conf, input(err)
		}
		if !composes || o.home != "" {
			return conf, nil
		}
		return withRunKeyHome(root, conf)
	}, check)
}

// homeInFolders is [homeInFolder] for the checkout at root, then for the working
// directory, the folder the session runs in: a home --home names by its report is for
// the checkout the report names, which may be another one.
func homeInFolders(root, home string) error {
	if err := homeInFolder(root, home); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return input(fmt.Errorf("change to a folder that exists and that you can read, and run again: the lookup of the working directory failed: %w", err))
	}
	return homeInFolder(cwd, home)
}
