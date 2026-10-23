package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/prebuild"
)

// textSeedFailed is the line of a seed step that failed, which the build reads as its
// last line.
const textSeedFailed = prebuild.Prefix + "%s"

// newSeed builds the hidden seed verb, what a pool's prebuild runs from the qory helper
// as the entry point of a container of the image, as the wall user: it runs the
// configuration's onCreateCommand and updateContentCommand in /workspace and moves the
// tree to /qory/seed, as [prebuild.Seed] has it. Its lines and its failure go to
// standard output, where the build passes them on, and the commands' output to standard
// error.
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
				fmt.Fprintf(cmd.OutOrStdout(), textSeedFailed+"\n", err)
				return reported(err)
			}
			return nil
		},
	}
}
