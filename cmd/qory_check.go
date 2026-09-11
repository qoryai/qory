package cmd

import (
	"fmt"

	"github.com/qoryai/qory/internal/stack"
)

// versionError is the refusal of a document whose qory key excludes the running qory. It
// names the file, what in it wants the range, the range and the version, so the person
// knows which qory to install or whom to ask. [ExitCode] gives it [ExitVersion].
type versionError struct {
	file    string
	what    string
	want    stack.Constraint
	version string
}

// Error is one line: the file, the version, the range, and what to do.
func (e *versionError) Error() string {
	return fmt.Sprintf("%s: this qory is %s, and %s wants %s; install a qory in that range, or ask its owner", e.file, e.version, e.what, e.want)
}

// qoryChecks checks the documents a command reads against the running qory, one call
// per document, and collects the row a compose prints for a build the check skips.
type qoryChecks struct {
	version string
	rows    [][2]string
	seen    map[string]bool
}

// newQoryChecks starts the checks for the running build.
func newQoryChecks() *qoryChecks {
	return &qoryChecks{version: checkedVersion(), seen: map[string]bool{}}
}

// check refuses a document whose range excludes the running qory. what names the range's
// owner in the message, "the stack" or "the base stack core@a1b2c3". A build without a
// definite version, one from source between tags, passes with a row saying the range
// was not checked; one row per file, since a checkout's qory.yaml is read as the
// configuration and as the stack.
func (c *qoryChecks) check(file, what string, want stack.Constraint) error {
	if want.Empty() {
		return nil
	}
	if c.version == "" {
		if !c.seen[file] {
			c.seen[file] = true
			c.rows = append(c.rows, [2]string{"qory", build().title() + "  (a build from source; not checked against " + want.String() + " in " + file + ")"})
		}
		return nil
	}
	if !want.Allows(c.version) {
		return &versionError{file: file, what: what, want: want, version: c.version}
	}
	return nil
}

// checkedVersion is the version a qory key is checked against: the release version, or
// the tag Go stamped into a source build made at a clean tagged commit, as [build]
// reads them. It is "" for a build with no definite version: a source build between
// tags, which Go stamps with a pseudo-version, or one with no version at all.
func checkedVersion() string {
	b := build()
	if b.Source == "source" && pseudo(b.Version) {
		return ""
	}
	return b.Version
}
