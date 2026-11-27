package cmd

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/internal/render"
)

// reasonReviewNeedsNoNestedDocker is the code of a read session that cannot run beside
// an inner Docker daemon, as Forager's read_needs_no_nested_docker is for a read run.
const reasonReviewNeedsNoNestedDocker = "review_needs_no_nested_docker"

// textReviewNeedsNoNestedDocker is the error of that session, as a stop of a review join
// shows it, see [reviewStop].
var textReviewNeedsNoNestedDocker = reviewStop("The run's image runs an inner Docker daemon, so the review did not start", "A read-only review session cannot run beside it").Error()

// readSessionSpec is spec adjusted for a session whose workflow has access: read: the
// workspace is bound read-only, and the runtime's arguments limit the tools. runtime
// is the runtime's name, and nestedDocker is whether the run's image runs a Docker daemon
// (session.Image.Docker). Credentials and every other member of spec stay as they are.
// It returns an error that starts with the fix, and no spec, for an image with nested
// Docker (code [reasonReviewNeedsNoNestedDocker]) and for a runtime that has no review
// guard, see [render.ReviewGuard]: it has no argument or no settings that limit the tools
// of a read session. For a session that does not read only, call nothing: the spec is
// unchanged.
func readSessionSpec(spec session.SessionSpec, runtime string, nestedDocker bool) (session.SessionSpec, error) {
	// An unknown runtime is nil, which has no review guard.
	rt, _ := render.Lookup(runtime)
	return readSessionSpecOf(spec, runtime, rt, nestedDocker)
}

// readSessionSpecOf is [readSessionSpec] for rt, the runtime named runtime, nil for
// none.
func readSessionSpecOf(spec session.SessionSpec, runtime string, rt render.Runtime, nestedDocker bool) (session.SessionSpec, error) {
	if nestedDocker {
		return session.SessionSpec{}, &reasonError{code: reasonReviewNeedsNoNestedDocker, text: textReviewNeedsNoNestedDocker}
	}
	guard, ok := render.ReviewGuard(rt)
	if !ok {
		return session.SessionSpec{}, noReviewGuard(runtime)
	}
	spec.WorkspaceReadOnly = true
	spec.Args = append(slices.Clone(spec.Args), guard.ReviewArgs()...)
	return spec, nil
}

// noReviewGuard is the stop of a review session on runtime, which has no review guard.
func noReviewGuard(runtime string) error {
	return reviewStop(fmt.Sprintf("qory has no argument that limits a review session's tools on %s, so the review did not start", runtime), "")
}

// noVerifyGuard is the stop of a Verify session on runtime, a [render.SettingsInliner]
// with no [render.VerifyGuard], see [verifyFoldersOf].
func noVerifyGuard(runtime string) error {
	return reviewStop(fmt.Sprintf("Run this workflow on another runtime: qory has no Verify settings for %s, so the review did not start", runtime), "")
}

// reviewWorkspace is the folders that a review join reads and writes in place of the
// run's checkout: work, the empty folder that is the session's working folder, commit,
// the commit folder of the fresh copy, and reports, the writable mount of the session's
// reports folder, which only that join gets. The join binds work and commit read-only,
// each at its path on the machine, as it binds reports, so the paths are the same in the
// agent's container.
type reviewWorkspace struct {
	work, commit string
	reports      wall.Mount
}

// readOnlyJoin is spec, a copy, for a review join: the workspace and the checkout's mount
// are read-only and the tools are limited, as [readSessionSpec] and [readOnlyCheckout]
// make a read run's. It is the part of [reviewJoinSpec] that needs no folder, so a run
// whose runtime or image cannot join a review session stops before the join makes any.
// The arguments are those of [reviewJoinSpec].
func readOnlyJoin(spec runSpec, root, workflow, runtime string, img session.Image) (runSpec, error) {
	read, err := readSessionSpec(spec.SessionSpec, runtime, img.Docker)
	if err != nil {
		return runSpec{}, err
	}
	spec.SessionSpec = read
	spec.Mounts = slices.Clone(spec.Mounts)
	if err := readOnlyCheckout(&spec, root, workflow, img, startedReview); err != nil {
		return runSpec{}, err
	}
	return spec, nil
}

// reviewJoinSpec is the spec of the join of a review session in a run at level write,
// from spec, the spec of the run's sessions, which it leaves as it is: the next session
// of the run joins with spec, writable, at the checkout. The workspace is bound
// read-only with [readSessionSpec]. The join reads the fresh copy of the pushed commit,
// ws, in place of the checkout at root: its working folder is the empty
// folder ws.work, and the copy is a second read-only mount at ws.commit, whose path
// qory.review_session.commit_folder has. The mount of root is dropped, and the join gets
// the writable mount of ws's reports folder and no other writable mount that the run did
// not have. A mount that contains or lies inside the checkout stops the join. The places
// that the run may write stay in the join's Written, and the working folder and the
// reports folder are added. The join's inputs.json has no qory.outbox, since the join
// binds no checkout that has an outbox. runtime is the runtime's name, workflow the
// review session's workflow, and img the run's image. A run with no wall, one whose
// setup runs inside the agent's container, and one whose image runs nested Docker stop
// here with the stops of a read run, which end with "so the review did not start", see
// [readOnlyCheckout]. It makes no folder, so [planReviewJoin] (the first caller),
// prepareLater in cmd/run.go (the second) and the last check before the join get the
// same spec.
func reviewJoinSpec(spec runSpec, root, workflow, runtime string, img session.Image, ws reviewWorkspace) (session.SessionSpec, error) {
	spec, err := readOnlyJoin(spec, root, workflow, runtime, img)
	if err != nil {
		return session.SessionSpec{}, err
	}
	if !cleanAbs(ws.commit) || !cleanAbs(ws.reports.Path) || ws.reports.ReadOnly || ws.reports.Target != "" {
		return session.SessionSpec{}, reviewStop("The review has no copy of the commit or no writable reports folder, so it did not start",
			fmt.Sprintf("The copy is %s and the reports folder is %s", strconv.Quote(ws.commit), strconv.Quote(ws.reports.Path)))
	}
	if !cleanAbs(ws.work) {
		return session.SessionSpec{}, reviewStop("The review's working folder is not the empty folder qory made, so the review did not start",
			fmt.Sprintf("The working folder is %s", strconv.Quote(ws.work)))
	}
	// The join does not bind the checkout, so a mount of root is dropped, and any other
	// mount inside root would show the checkout's files in the agent's container.
	var kept []wall.Mount
	for _, m := range spec.Mounts {
		switch how := session.Overlap(root, m.Path); {
		case m.Path == root || how == "is":
		case how == "contains" || how == "lies inside":
			return session.SessionSpec{}, reviewStop("A mount contains the checkout or lies inside it, so the review did not start",
				fmt.Sprintf("The mount %s would show the checkout %s", strconv.Quote(m.Path), strconv.Quote(root)))
		default:
			kept = append(kept, m)
		}
	}
	spec.Mounts = append(kept, wall.Mount{Path: ws.work, ReadOnly: true}, wall.Mount{Path: ws.commit, ReadOnly: true}, ws.reports)
	spec.Dir = ws.work
	spec.Written = slices.Clone(spec.Written)
	for _, p := range writtenPlaces(ws.work, spec.Mounts) {
		if !slices.Contains(spec.Written, p) {
			spec.Written = append(spec.Written, p)
		}
	}
	return spec.SessionSpec, nil
}
