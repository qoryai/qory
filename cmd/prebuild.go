package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/prebuild"
)

// ExitNoConfiguration is the status of qory run prebuild for a repository with no
// development container configuration: nothing is built, which is no failure, and the
// job that runs it tells it apart from a build that made an image, 0, a refusal,
// [ExitInput], and a failure, 1. No other verb exits with it.
const ExitNoConfiguration = 7

// seedArgs are the arguments that select the hidden seed verb, which qory run prebuild
// runs from the helper inside a container of the image.
var seedArgs = []string{"run", "seed"}

// The words of qory run prebuild's command line. Each is one format string.
const (
	textPrebuildHelper = "qory run prebuild: this is the %s build; the container runs qory's Linux build, so give it with --helper"
	textPrebuildFailed = "qory run prebuild: %s"
)

// newPrebuild builds the hidden prebuild verb, which a pool's prebuild job runs on a
// repository it fetched: it builds the image a walled run starts from, as
// [prebuild.Build] has it, and prints a line per step. The exit status is 0 when it
// built the image, [ExitNoConfiguration] when the repository has no configuration,
// [ExitInput] for a refusal, [exitTimeout] at the time limit, and 1 for every other
// failure.
func newPrebuild() *cobra.Command {
	var o prebuild.Options
	c := &cobra.Command{
		Use:    "prebuild --repository folder --agent-image name@sha256:digest --user uid:gid --tag image",
		Short:  "Build a repository's development container into the image a run starts from",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if o.Helper == "" {
				if runtime.GOOS != "linux" {
					fmt.Fprintf(out, textPrebuildHelper+"\n", runtime.GOOS)
					return reported(input(fmt.Errorf(textPrebuildHelper, runtime.GOOS)))
				}
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				o.Helper = exe
			}
			o.SeedArgs = seedArgs
			o.Env = os.Environ()
			o.Out, o.Log = out, cmd.ErrOrStderr()
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err := prebuild.Build(ctx, o)
			var shown *prebuild.ShownError
			var refusal *prebuild.RefusedError
			switch {
			case err == nil:
				return nil
			case errors.Is(err, prebuild.ErrNoConfiguration):
				return reported(&exitError{code: ExitNoConfiguration})
			case errors.As(err, &shown):
				return reported(err)
			}
			fmt.Fprintf(out, textPrebuildFailed+"\n", err)
			switch {
			case errors.Is(err, prebuild.ErrLimit):
				return reported(&exitError{code: exitTimeout})
			case errors.As(err, &refusal):
				return reported(input(err))
			}
			return reported(err)
		},
	}
	f := c.Flags()
	f.StringVar(&o.Repository, "repository", "", "the folder of the repository, fetched at a commit")
	f.StringVar(&o.AgentImage, "agent-image", "", "the agent image, by digest: name@sha256:digest")
	f.StringVar(&o.User, "user", "", "the wall user the image runs as, uid:gid")
	f.StringVar(&o.Tag, "tag", "", "the name of the image to make")
	f.StringVar(&o.Helper, "helper", "", "qory's static Linux build for the engine's architecture; on Linux, this binary")
	return c
}

// newSeed builds the hidden seed verb, what qory run prebuild runs from the qory helper
// as the entry point of a container of the image, as the wall user: it runs the
// configuration's onCreateCommand and updateContentCommand in /workspace and moves the
// tree to /qory/seed, as [prebuild.Seed] has it. Its lines and its failure go to
// standard output, where qory run prebuild passes them on, and the commands' output to
// standard error.
func newSeed() *cobra.Command {
	return &cobra.Command{
		Use:         "seed",
		Short:       "Run the image's creation commands and store the tree",
		Hidden:      true,
		Annotations: map[string]string{inWall: "seed"},
		Args:        noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err := prebuild.Seed(ctx, prebuild.ContainerPaths, os.Environ(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), textPrebuildFailed+"\n", err)
				return reported(err)
			}
			return nil
		},
	}
}
