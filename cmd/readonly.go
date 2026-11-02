package cmd

import (
	"fmt"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// The lines of a run from a run code whose workflow only reads the checkout, and that
// cannot see the checkout read-only, each with the workflow's name: one with no wall,
// one whose setup runs inside the agent's container, and one whose image runs nested
// Docker, which also takes the image's name or, with none, its reference.
const (
	textReadNeedsWall           = "run this workflow with a wall: workflow %s only reads the checkout, and a run with no wall mounts nothing read-only"
	textReadNeedsSetupOutside   = "set wall.setup to outside in " + config.ForagerFileName + ", or remove it: workflow %s only reads the checkout, and setup inside runs in the agent's container, which sees the checkout read-only"
	textReadNeedsNoNestedDocker = "use an image without nested Docker for read workflow %s, or declare it write: image %s has nested Docker, so the checkout cannot be made read-only"
)

// readsOnly reports whether the run from the run code r starts a workflow that only
// reads the checkout: one whose access in workflows, the composed stack's, is read,
// which is also the access of a workflow the stack does not declare. A run with no run
// code starts no workflow.
func readsOnly(r *codeRun, workflows map[string]stack.Workflow) bool {
	return r != nil && (&stack.Stack{Workflows: workflows}).AccessOf(r.workflow) == stack.AccessRead
}

// readOnlyCheckout makes spec, the run of a workflow that only reads the checkout at
// root, show the checkout read-only to the agent: Forager binds the workspace
// read-only, and the checkout's mount is read-only, since a writable mount that
// contains the workspace stops the join. The checkout stays among the places the run
// may write, which the setup container and the host steps still write. img is the
// image chosen at the run's open, [session.Run.Image], whichever way it was selected:
// the policy, --image or wall.image. A run that cannot see the checkout read-only stops
// before the join with a [*reasonError], each line ending with [codeUsed]: one with no
// wall, read_needs_wall; one with wall.setup inside, read_needs_setup_outside; and one
// whose image sets docker: true, read_needs_no_nested_docker. An image selected by its
// reference runs no nested Docker. A walled run with no image passes, and fails at the
// join.
func readOnlyCheckout(spec *runSpec, root, workflow string, img session.Image) error {
	refused := func(code, text string) error {
		return &reasonError{code: code, text: text + " (" + code + ")" + codeUsed}
	}
	switch {
	case spec.Wall == nil:
		return refused(event.ReasonReadNeedsWall, fmt.Sprintf(textReadNeedsWall, workflow))
	case spec.setupWhere == config.SetupInside:
		return refused(event.ReasonReadNeedsSetupOutside, fmt.Sprintf(textReadNeedsSetupOutside, workflow))
	case img.Docker:
		shown := img.Name
		if shown == "" {
			shown = img.Ref
		}
		return refused(event.ReasonReadNeedsNoNestedDocker, fmt.Sprintf(textReadNeedsNoNestedDocker, workflow, accesskey.ShownName(shown, false)))
	}
	spec.WorkspaceReadOnly = true
	for i := range spec.Mounts {
		if spec.Mounts[i].Path == root {
			spec.Mounts[i].ReadOnly = true
		}
	}
	return nil
}
