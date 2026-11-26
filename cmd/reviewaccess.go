package cmd

import "github.com/qoryai/qory/internal/stack"

// workflowReadsOnly reports whether a session of the workflow name only reads: its entry
// in workflows, the composed stack's table (compose.Result.Stack.Workflows), has
// access: read. A workflow that the table lacks, and an entry with no access, read as
// read, as [stack.Stack.AccessOf] reads them and as [accessOf] does for a run.
func workflowReadsOnly(workflows map[string]stack.Workflow, name string) bool {
	return (&stack.Stack{Workflows: workflows}).AccessOf(name) == stack.AccessRead
}

// workflowJudges reports whether a session of the workflow name judges the work: false
// only when its entry in workflows has judges: false, true when the entry is missing, or
// has judges: true or no judges.
func workflowJudges(workflows map[string]stack.Workflow, name string) bool {
	j := workflows[name].Judges
	return j == nil || *j
}

// reviewGroupOf is the review_group of the workflow name in workflows, the composed
// stack's table: the name of the group role whose members' reports a session of the
// workflow needs, "" when the entry is missing or has none.
func reviewGroupOf(workflows map[string]stack.Workflow, name string) string {
	return workflows[name].ReviewGroup
}

// startsReviewers reports whether a session of the workflow name starts reviewer agents:
// its entry in workflows names a review_group. Only such a session gets
// qory.review_session in its inputs.json, and a launch snapshot with the newest version
// of every plan type, see [snapshotPlanTypes].
func startsReviewers(workflows map[string]stack.Workflow, name string) bool {
	return reviewGroupOf(workflows, name) != ""
}

// joinsAsReview reports whether a later session of the workflow name joins a run at level
// as a review join: the run is at level write, the session's level, got, is read, and its
// entry in workflows declares access: read and a review_group. A run at write-no-push has
// no pushed commit, so no suite of a commit and no review join. Any other session whose
// level differs from level stops the run, so no later session raises the access of the
// run. A review join reads a read-only copy of the pushed commit, not the checkout, and
// pushes nothing, see [reviewJoinSpec].
func joinsAsReview(workflows map[string]stack.Workflow, name, got, level string) bool {
	return level == stack.AccessWrite && got == stack.AccessRead &&
		workflowReadsOnly(workflows, name) && startsReviewers(workflows, name)
}
