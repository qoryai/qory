package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/render/claude"
)

// reasonReviewNeedsNoNestedDocker is the code of a read session that cannot run beside
// an inner Docker daemon, as Forager's read_needs_no_nested_docker is for a read run.
const reasonReviewNeedsNoNestedDocker = "review_needs_no_nested_docker"

// textReviewNeedsNoNestedDocker is the error of that session.
const textReviewNeedsNoNestedDocker = "Remove the Docker daemon from the run's image.\n  A read-only review session cannot run beside an inner Docker daemon."

// readSessionTools are the tools of a Claude Code session whose workflow has
// access: read: Read, Grep and Glob read the checkout, Skill loads the workflow's skill,
// Write writes the report, Agent starts the reviewer agents of the skill, and Bash runs
// the commands that [reviewBashAllow] allows. It has no edit tool. The agent files of
// the reviewers deny Bash, see render.WithoutBash, so the agents that Agent starts
// have no Bash, whatever this list says.
//
// shortcut: the walled test that a reviewer agent cannot run a command runs when the
// join is wired.
var readSessionTools = []string{"Read", "Grep", "Glob", "Skill", "Write", "Agent", "Bash"}

// readSessionSpec is spec adjusted for a session whose workflow has access: read: the
// workspace is bound read-only, and the runtime's arguments limit the tools. runtime
// is the runtime's name, and nestedDocker is whether the run's image runs a Docker daemon
// (session.Image.Docker). Credentials and every other member of spec stay as they are.
// It returns an error that starts with the fix, and no spec, for an image with nested
// Docker (code [reasonReviewNeedsNoNestedDocker]) and for a runtime that qory has no
// argument for that limits the tools of a read session. For a session that does not
// read only, call nothing: the spec is unchanged.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
// shortcut: Claude Code only, because no other runtime has a reviewed way to limit the
// tools; add the runtime's arguments here when one has.
func readSessionSpec(spec session.SessionSpec, runtime string, nestedDocker bool) (session.SessionSpec, error) {
	if nestedDocker {
		return session.SessionSpec{}, &reasonError{code: reasonReviewNeedsNoNestedDocker, text: textReviewNeedsNoNestedDocker}
	}
	if runtime != claude.Runtime {
		return session.SessionSpec{}, fmt.Errorf("Run the workflow on %s.\nqory has no argument that limits the tools of a read session on the runtime %q.", claude.Runtime, runtime)
	}
	spec.WorkspaceReadOnly = true
	spec.Args = append(slices.Clone(spec.Args), "--tools", strings.Join(readSessionTools, ","))
	return spec, nil
}
