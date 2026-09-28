package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/ui"
)

// newConfig builds the config verb, which prints every effective setting with its value
// and the file it came from, for the checkout the process stands in. It reads the
// configuration files, and runs describe of each integration the runner file declares,
// and needs no git working tree, so a person can check what a compose or a run would
// read before there is anything to compose.
func newConfig() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Show every setting, its value, and the file it came from",
		Long: `Print every setting, its value, and the file it came from.

Every setting has a default. The qory.yaml files apply in this order, each over the one
before it:

  1. yours, in ~/.config/qory ($XDG_CONFIG_HOME/qory)
  2. those in the directories above the checkout that you own, the farthest first
  3. the checkout's own

A compose flag overrides every file.

The machine's ` + config.RunnerFileName + ` is listed under runner. Each integration it declares is
described as before a run; one that does not describe is an error. An integration whose
name the credentials section defines too is listed as shadowed.

--verbose adds nothing here.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, err := checkout.Root(cwd)
			if err != nil {
				return err
			}
			conf, err := config.Load(root, true)
			if err != nil {
				return input(err)
			}
			for _, key := range conf.Runner.Shadowed() {
				fmt.Fprintln(cmd.ErrOrStderr(), "qory config:", shadowed(key))
			}
			// A directory outside a git working tree is no checkout. The wall's read-write
			// mounts are what a run may write besides.
			var workspace []string
			if checkout.ExcludeFile(root) != "" {
				workspace = []string{root}
			}
			if r := conf.Runner; r != nil && r.Wall != nil {
				for _, m := range r.Wall.Mounts {
					if !m.ReadOnly {
						workspace = append(workspace, m.Path)
					}
				}
			}
			if err := conf.Runner.Expand(cmd.Context(), config.Expansion{Workspace: workspace}); err != nil {
				return input(err)
			}
			u := ui.New(cmd.OutOrStdout())
			u.Title("qory config", ui.Short(root, ""))
			var rows [][]string
			for _, r := range conf.Rows() {
				origin := r.Origin
				if origin != config.Default {
					origin = ui.Short(origin, root)
				}
				rows = append(rows, []string{r.Key, r.Value, origin})
			}
			u.Table(rows)
			if len(conf.Files) == 0 {
				u.Blank()
				u.Text("No " + config.FileName + " or " + config.AltFileName + " was found; every value is its default.")
			}
			return nil
		},
	}
}
