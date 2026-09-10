package cmd

import (
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/ui"
)

func newVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and the harness format this qory reads",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u := ui.New(cmd.OutOrStdout())
			u.Title("qory", version())
			u.Fields([][2]string{{"harness format", profile.APIVersion}})
			return nil
		},
	}
}

// version is the release version set at build time, else the module version Go recorded
// (a tag for `go install github.com/qoryai/qory@v1.2.3`), else the commit of a checkout
// build, marked dirty when the tree had uncommitted changes.
func version() string {
	if Version != "dev" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return Version
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if modified == "true" {
		rev += "-dirty"
	}
	return "dev " + rev
}
