package cmd

import (
	"errors"
	"fmt"
	"slices"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/qoryai/qory/internal/config"
)

// fetchOnlyFlag is the hidden flag of qory run, and of qory run prepare, that selects
// the fetch-only mode: the run fetches its code and does nothing else.
//
// In qory run, the mode is for a run given a run key alone, behind the wall: the
// launch is qory run prepare --fetch-only from the helper inside the container, which
// fetches the head of the default branch of the run's repository into the working
// folder, through the run's proxy, and runs no start command; there is no agent, no
// harness and no preparation before it. The run starts in the wall's image, the
// default: the machine's other images are not given to the run, so a run
// configuration that selects one, such as an image built from the repository, is no
// run. The flags that could start anything else are refused, see [fetchOnlyAllowed].
const fetchOnlyFlag = "fetch-only"

// fetchOnlyAllowed are the flags qory run takes in the fetch-only mode, beside
// --fetch-only: the run key's descriptor, the wall, and the time limit and how the
// fetch is stopped at it. --verbose adds nothing to qory run. Every other flag given is
// refused.
var fetchOnlyAllowed = []string{fetchOnlyFlag, runKeyFDFlag, "wall", "timeout", "stop-grace", "verbose"}

// fetchOnlyArgs are the launch of a fetch-only run: qory run prepare --fetch-only, from
// the helper inside the container.
var fetchOnlyArgs = append(append([]string{}, prepareArgs...), "--"+fetchOnlyFlag)

// The words of the fetch-only mode's refusals. Each is one format string.
const (
	textFetchOnlyNoKey   = "--fetch-only fetches the code of a run given a run key, and no run key is given: give it with --run-key-fd"
	textFetchOnlyRuntime = "--fetch-only fetches the run's code and starts no agent, so it takes no runtime and no arguments after --"
	textFetchOnlyFlag    = "--fetch-only fetches the run's code and starts no agent, so it takes no --%s"
	textFetchOnlyNoWall  = "--fetch-only fetches the run's code inside the wall, and the run has none: --wall %s, or wall in %s"
	textFetchOnlyNothing = "qory run prepare: --fetch-only fetches the run's repository, and %s names none"
)

// fetchOnlyRefusal refuses, as input, what a fetch-only run cannot have, before
// anything else of the run is read: no run key, given as key; a runtime or arguments
// for an agent; and a flag that is not one of [fetchOnlyAllowed], the first by name.
func fetchOnlyRefusal(cmd *cobra.Command, key *runKey, runtime string, extra []string) error {
	if key == nil {
		return input(errors.New(textFetchOnlyNoKey))
	}
	if runtime != "" || len(extra) > 0 || cmd.ArgsLenAtDash() >= 0 {
		return input(errors.New(textFetchOnlyRuntime))
	}
	var refused string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if refused == "" && !slices.Contains(fetchOnlyAllowed, f.Name) {
			refused = f.Name
		}
	})
	if refused != "" {
		return input(fmt.Errorf(textFetchOnlyFlag, refused))
	}
	return nil
}

// fetchOnlySpec makes spec, enclosed behind its wall, the spec of a fetch-only run: no
// runtime and no preparation, the launch [fetchOnlyArgs] from the helper, on pipes, in
// the wall's default image with no other image to select: of the machine's images, the
// default's definition alone stays, when the default is one of them, and no reference
// is allowed. A spec with no wall is refused, as input.
func fetchOnlySpec(spec *session.Spec) error {
	if spec.Wall == nil {
		return input(fmt.Errorf(textFetchOnlyNoWall, config.WallDocker, config.ForagerFileName))
	}
	spec.Runtime = nil
	spec.Preparation = nil
	spec.Command = wall.HelperPath
	spec.Args = slices.Clone(fetchOnlyArgs)
	spec.Interactive = false
	// The default's definition alone, when the default is a name of the machine's.
	var kept []session.Image
	for _, i := range spec.Images {
		if i.Name == spec.Image {
			kept = append(kept, i)
		}
	}
	spec.Images, spec.ImagesAllow = kept, nil
	return nil
}
