package cmd

import "github.com/qoryai/qory/internal/stack"

// workflowReadsOnly reports whether a session of the workflow name only reads: its entry
// in workflows, the composed stack's table (compose.Result.Stack.Workflows), has
// access: read. A workflow that the table lacks, and an entry with no access, read as
// read, as [stack.Stack.AccessOf] reads them and as [accessOf] does for a run.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func workflowReadsOnly(workflows map[string]stack.Workflow, name string) bool {
	return (&stack.Stack{Workflows: workflows}).AccessOf(name) == stack.AccessRead
}

// workflowJudges reports whether a session of the workflow name judges the work: false
// only when its entry in workflows has judges: false, true when the entry is missing, or
// has judges: true or no judges.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func workflowJudges(workflows map[string]stack.Workflow, name string) bool {
	j := workflows[name].Judges
	return j == nil || *j
}

// reviewGroupOf is the review_group of the workflow name in workflows, the composed
// stack's table: the name of the group role whose members' reports a session of the
// workflow needs, "" when the entry is missing or has none.
//
// shortcut: not called yet; wired in when cmd/run.go, cmd/rundocs.go and
// cmd/pushstep.go can change (after Q-01, Q-36 and Q-73).
func reviewGroupOf(workflows map[string]stack.Workflow, name string) string {
	return workflows[name].ReviewGroup
}
