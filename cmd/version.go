package cmd

import (
	"regexp"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
)

// newVersion builds the version verb, which prints the version and the harness format
// this build reads.
func newVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and the harness format this qory reads",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			u := ui.New(cmd.OutOrStdout())
			u.Title("qory", version())
			u.Fields([][2]string{{"harness format", stack.APIVersion}})
			return nil
		},
	}
}

// version is the release version set at build time, else the module version Go recorded
// (a tag for `go install github.com/qoryai/qory@v1.2.3`), else the commit of a checkout
// build, marked dirty when the tree had uncommitted changes. Go stamps a checkout build
// with a pseudo-version, v0.1.1-0.<time>-<commit>, which names no release, so that form
// is read as no version and the commit is printed instead.
func version() string {
	if Version != "dev" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	if v := info.Main.Version; v != "" && v != "(devel)" && !pseudo(v) {
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

// pseudoVersion is the end of a Go pseudo-version: a fourteen-digit timestamp and twelve
// hex characters of a commit, after a dash.
var pseudoVersion = regexp.MustCompile(`-[0-9]{14}-[0-9a-f]{12}$`)

// pseudo reports whether v is a Go pseudo-version, one ending in a timestamp and a
// commit, rather than a tag. A tag such as v1.0.20 is not one.
func pseudo(v string) bool {
	return pseudoVersion.MatchString(v)
}
