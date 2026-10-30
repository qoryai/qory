package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/pool/prebuild"
	"github.com/qoryai/qory/internal/secretmask"
)

// newPrebuildStep builds the hidden prebuild-step verb: what a prebuild from a run code
// runs from the qory helper behind the wall of its run, as the wall user, in the working
// folder qory run fetched the run's code into. It builds the code's development container
// on the wall's nested Docker, pushes the images through the run's proxy with no login,
// and writes the result to the out folder its arguments name, as [prebuild.RunStep] has
// it. Its arguments, as [prebuild.Step.Args] writes them, go to [prebuild.ParseStep], and
// cobra parses no flag of them. Its lines go to standard output and the programs' output
// to standard error, which the session passes on; its failure is in the result, and on
// standard error.
func newPrebuildStep() *cobra.Command {
	return &cobra.Command{
		Use:                "prebuild-step --out folder --run-id id --image-destination name ...",
		Short:              "Build and push a prebuild's images behind its wall",
		Hidden:             true,
		Annotations:        map[string]string{inWall: "prebuild-step"},
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			step, err := prebuild.ParseStep(args)
			if err != nil {
				return input(err)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			// Its lines and the programs' output are masked with the process's mask list,
			// which replaces the value of a URL's signature or token.
			out, log := secretmask.Default().Writer(cmd.OutOrStdout()), secretmask.Default().Writer(cmd.ErrOrStderr())
			defer out.Flush()
			defer log.Flush()
			err = prebuild.RunStep(ctx, step, prebuild.StepSettings{Dir: dir, Env: os.Environ(), Out: out, Log: log})
			if err != nil {
				fmt.Fprintln(log, "qory run prebuild-step:", err)
				return reported(err)
			}
			return nil
		},
	}
}
