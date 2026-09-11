package cmd

import (
	"encoding/json"
	"fmt"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/stack"
	"github.com/qoryai/qory/internal/ui"
)

// newVersion builds the version verb, which prints what the running binary is: its
// version, the commit it was built from, whether it is a release or a source build, and
// the harness and report formats it reads and writes. With --json the same fields come out
// as one JSON object, for a script that installs or checks qory.
func newVersion() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "version",
		Short: "Print the version, the build and the harness format this qory reads",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			b := build()
			if asJSON {
				data, err := json.MarshalIndent(b, "", "  ")
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return err
			}
			u := ui.New(cmd.OutOrStdout())
			u.Title("qory", b.title())
			var rows [][2]string
			if b.Commit != "" {
				commit := b.Commit
				if b.Dirty {
					commit += " (dirty)"
				}
				rows = append(rows, [2]string{"commit", commit})
			}
			rows = append(rows,
				[2]string{"source", b.Source},
				[2]string{"harness format", b.HarnessFormat},
				[2]string{"report version", strconv.Itoa(b.ReportVersion)},
			)
			u.Fields(rows)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the fields as one JSON object")
	return c
}

// buildInfo is what qory version reports about the running binary. The JSON form is the
// --json output, and every field is always present, so a script reads it without
// checking for a key.
type buildInfo struct {
	// Version is the version without a leading v: 0.3.0 for the release v0.3.0 and for a
	// source build at that tag, the pseudo-version Go stamped for a source build between
	// tags, and "" for a build that carries none.
	Version string `json:"version"`
	// Commit is the commit the binary was built from, twelve characters, "" when the
	// build carries none.
	Commit string `json:"commit"`
	// Dirty is set when the tree had uncommitted changes at build time.
	Dirty bool `json:"dirty"`
	// Source is "release" for a binary whose version was set at build time, which is how
	// a release is built, and "source" for a go install or go build.
	Source string `json:"source"`
	// HarnessFormat is the apiVersion this qory reads.
	HarnessFormat string `json:"harnessFormat"`
	// ReportVersion is the report format version a compose writes.
	ReportVersion int `json:"reportVersion"`
}

// build reads what the binary knows about itself: [Version] and [Commit] set at build
// time, and what Go recorded about the build. A release build carries its version in
// [Version]. A source build carries "dev" there and its version is what Go recorded for
// the main module: the tag for a build at a clean tagged commit, a pseudo-version for a
// build between tags, and nothing for a build without version control. The commit is
// Go's vcs.revision, else [Commit].
func build() buildInfo {
	b := buildInfo{Commit: Commit, Source: "release", HarnessFormat: stack.APIVersion, ReportVersion: report.Version}
	if Version != "dev" {
		b.Version = strings.TrimPrefix(Version, "v")
	} else {
		b.Source = "source"
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	if b.Source == "source" {
		b.Version, b.Dirty = stamped(info.Main.Version)
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" {
				b.Commit = s.Value
			}
		case "vcs.modified":
			if s.Value == "true" {
				b.Dirty = true
			}
		}
	}
	if len(b.Commit) > 12 {
		b.Commit = b.Commit[:12]
	}
	return b
}

// title is what the title line shows after the name: the version when the build has one,
// else "dev" and the commit, marked dirty when the tree had uncommitted changes.
func (b buildInfo) title() string {
	if b.Version != "" {
		return b.Version
	}
	if b.Commit == "" {
		return "dev"
	}
	if b.Dirty {
		return "dev " + b.Commit + "-dirty"
	}
	return "dev " + b.Commit
}

// stamped reads the version Go recorded for the main module and returns it without the
// leading v, and whether the build metadata marks it dirty. "(devel)" and "" are no
// version. Go 1.24 stamps a build from a checkout with the tag at a clean tagged commit,
// else a pseudo-version, v0.2.2-0.<time>-<commit>, and appends +dirty when the tree had
// uncommitted changes; that metadata is read and then dropped, so the version compares
// as one.
func stamped(v string) (version string, dirty bool) {
	if v == "" || v == "(devel)" {
		return "", false
	}
	if i := strings.IndexByte(v, '+'); i >= 0 {
		dirty = strings.Contains(v[i+1:], "dirty")
		v = v[:i]
	}
	return strings.TrimPrefix(v, "v"), dirty
}

// pseudoVersion is the end of a Go pseudo-version: a fourteen-digit timestamp and twelve
// hex characters of a commit. A dash precedes the timestamp when no tag came before,
// v0.0.0-<time>-<commit>, and a dot when one did, v0.2.2-0.<time>-<commit> and
// v0.3.0-rc.1.0.<time>-<commit>.
var pseudoVersion = regexp.MustCompile(`[-.][0-9]{14}-[0-9a-f]{12}$`)

// pseudo reports whether v is a Go pseudo-version, one ending in a timestamp and a
// commit, rather than a tag. A tag such as v1.0.20 is not one. Build metadata is dropped
// first, so a stamped version and one [stamped] has read both answer the same.
func pseudo(v string) bool {
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	return pseudoVersion.MatchString(v)
}
