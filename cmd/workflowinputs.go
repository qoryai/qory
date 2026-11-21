package cmd

import "github.com/qoryai/qory/internal/stack"

// workflowInputs is qory.workflow, written into the inputs.json of every session (vault
// 0308): the values of the session's entry in the composed stack's workflows table that
// the harness reads.
type workflowInputs struct {
	// Judges is whether the session judges the work, from [workflowJudges]. A session
	// with judges false records escalate or no verdict.
	Judges bool `json:"judges"`
}

// newWorkflowInputs is qory.workflow for a session of the workflow name, with
// workflows the composed stack's table (compose.Result.Stack.Workflows).
//
// shortcut: not called yet; added to qoryInputs in cmd/rundocs.go when that file can
// change (after Q-01, Q-36 and Q-73).
func newWorkflowInputs(workflows map[string]stack.Workflow, name string) workflowInputs {
	return workflowInputs{Judges: workflowJudges(workflows, name)}
}
