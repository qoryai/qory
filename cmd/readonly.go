package cmd

import (
	"context"
	"fmt"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/stack"
)

// The stops of a run from a run code that only reads the checkout, [readsOnly], and that
// cannot see the checkout read-only, each in the form of [reviewStop]: the cause, with
// the workflow's name, and then the detail. One has no wall, one has a setup that runs
// inside the agent's container, and one has an image that runs nested Docker, whose detail
// has the image's name or, with none, its reference. The first three end with "so
// <subject> did not start": the subject is [startedRun] for a read run and [startedReview]
// for a review join. The last stop is that of a read run whose setup container was not
// removed, with the error of the removal.
const (
	textReadNeedsWall                 = "The workflow %s only reads the checkout and the run has no wall, so %s did not start"
	textReadNeedsWallDetail           = "A run with no wall mounts nothing read-only"
	textReadNeedsSetupOutside         = "The workflow %s only reads the checkout and wall.setup is inside, so %s did not start"
	textReadNeedsSetupOutsideDetail   = "A setup inside runs in the agent's container, which sees the checkout read-only"
	textReadNeedsNoNestedDocker       = "The workflow %s only reads the checkout, so %s did not start"
	textReadNeedsNoNestedDockerDetail = "The image %s runs nested Docker, and beside it the checkout cannot be made read-only"
	textReadNeedsSetupRemoved         = "The setup container of this read run was not removed, so the session did not start"
	textReadNeedsSetupRemovedDetail   = "A process in the container could write the checkout: %s"
)

// The subjects of the stops of [readOnlyCheckout]: a read run, and a review join.
const (
	startedRun    = "the run"
	startedReview = "the review"
)

// The stop of a run from a run code that does not only read the checkout, [readsOnly],
// and that has no wall, with the workflow's name.
const (
	textWriteNeedsWall       = "The workflow %s writes to the checkout and the run has no wall, so the run did not start"
	textWriteNeedsWallDetail = "A run with no wall runs the agent as this machine's user"
)

// accessOf is the access level of the run from the run code r: the lower of the access in
// its documents and the access of its workflow in workflows, the composed stack's, in
// the order [stack.AccessRead], [stack.AccessWriteNoPush], [stack.AccessWrite]. A
// workflow the stack does not declare is read. Documents with no target have no access,
// and the workflow's access applies alone. A run with no run code starts no workflow, and
// its level is "".
func accessOf(r *codeRun, workflows map[string]stack.Workflow) string {
	if r == nil {
		return ""
	}
	declared := (&stack.Stack{Workflows: workflows}).AccessOf(r.workflow)
	return stack.LowerAccess(declared, r.access)
}

// readsOnly reports whether the run from the run code r only reads the checkout: its
// [accessOf] level is read, so either the documents or the workflow is read.
func readsOnly(r *codeRun, workflows map[string]stack.Workflow) bool {
	return accessOf(r, workflows) == stack.AccessRead
}

// readOnlyCheckout makes spec, a run that only reads the checkout at root, show the
// checkout read-only to the agent: Forager binds the workspace read-only, and the
// checkout's mount is read-only, since a writable mount that contains the workspace
// stops the join. A run with a setup works in a copy, see [runCopy.readOnly], which the setup
// container writes until [removeSetup]; the person's checkout then stays out of the run. img is the image chosen at the run's open, [session.Run.Image], whichever way it was selected:
// the policy, --image or wall.image. Three kinds of run stop here, before the join, with
// a [*reasonError], each line ending with [codeUsed]: one with no wall, read_needs_wall;
// one with wall.setup inside, read_needs_setup_outside; and one whose image sets
// docker: true, read_needs_no_nested_docker. An image selected by its reference runs no
// nested Docker. A walled run with no image fails at the open and never gets here. The
// caller stops a run with any other writable mount, see [writableMount]. subject is
// [startedRun] or [startedReview], as the three texts end.
func readOnlyCheckout(spec *runSpec, root, workflow string, img session.Image, subject string) error {
	switch {
	case spec.Wall == nil:
		return refusedBeforeJoin(event.ReasonReadNeedsWall, fmt.Sprintf(textReadNeedsWall, workflow, subject), textReadNeedsWallDetail)
	case spec.setupWhere == config.SetupInside:
		return refusedBeforeJoin(event.ReasonReadNeedsSetupOutside, fmt.Sprintf(textReadNeedsSetupOutside, workflow, subject), textReadNeedsSetupOutsideDetail)
	case img.Docker:
		shown := img.Name
		if shown == "" {
			shown = img.Ref
		}
		return refusedBeforeJoin(event.ReasonReadNeedsNoNestedDocker, fmt.Sprintf(textReadNeedsNoNestedDocker, workflow, subject), fmt.Sprintf(textReadNeedsNoNestedDockerDetail, accesskey.ShownName(shown, false)))
	}
	spec.WorkspaceReadOnly = true
	for i := range spec.Mounts {
		if spec.Mounts[i].Path == root {
			spec.Mounts[i].ReadOnly = true
		}
	}
	return nil
}

// refusedBeforeJoin is the error of a run from a run code that stops before the join,
// with the reason code and the text that [reviewStop] makes of the cause and the detail.
// The reason code is the run's close reason and is not in the text.
func refusedBeforeJoin(code, cause, detail string) error {
	return &reasonError{code: code, text: reviewStop(cause, detail).Error()}
}

// writeNeedsWall is the error of a run from a run code that does not only read the
// checkout, [readsOnly], and that has no wall: without a wall the agent runs as qory's
// user on the host. The run stops before the image's fetch, the setup and the join,
// closed failed with [event.ReasonWriteNeedsWall].
func writeNeedsWall(workflow string) error {
	return refusedBeforeJoin(event.ReasonWriteNeedsWall, fmt.Sprintf(textWriteNeedsWall, workflow), textWriteNeedsWallDetail)
}

// removeSetup removes the setup container of a read run, after the setup and before the
// join, so no process in that container writes the checkout while the agent reads it.
// With no setup container, RemoveSetup returns nil, so the call has no condition. An
// error stops the run before the join with [event.ReasonReadNeedsSetupRemoved]: the
// container may still run, and no session can join the run.
func removeSetup(ctx context.Context, run openedRun) error {
	if err := run.RemoveSetup(ctx); err != nil {
		return refusedBeforeJoin(event.ReasonReadNeedsSetupRemoved, textReadNeedsSetupRemoved, fmt.Sprintf(textReadNeedsSetupRemovedDetail, accesskey.ShownError(err)))
	}
	return nil
}
