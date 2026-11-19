package cmd

import (
	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/reviewfiles"
)

// newReviewFiles builds the hidden review-files verb: the MCP server qory_review that a
// reviewer's Codex starts in the reviewer's container, which serves read-only file tools on
// the reviewer's copy at the folder its argument names (690 (a)). It reads JSON-RPC
// messages on standard input and writes its responses, and nothing else, on standard
// output, so it carries [inWall] and no notice of a release reaches standard output.
func newReviewFiles() *cobra.Command {
	return &cobra.Command{
		Use:         "review-files folder",
		Short:       "Serve read-only file tools over MCP on a reviewer's copy",
		Hidden:      true,
		Annotations: map[string]string{inWall: "review-files"},
		Args:        exactArgs(1, "the folder of the reviewer's copy"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return reviewfiles.Serve(args[0], cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
