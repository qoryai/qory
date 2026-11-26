package cmd

import "github.com/qoryai/qory/internal/stack"

// workflowInputs is qory.workflow, written into the inputs.json of every session of a run
// from a run code: the session's workflow and the values of its entry in the composed
// stack's workflows table that the harness reads.
type workflowInputs struct {
	// Judges is whether the session judges the work, from [workflowJudges]. A session
	// with judges false records escalate or no verdict.
	Judges bool `json:"judges"`
	// Name is the workflow of the session, whether or not the table has an entry for it.
	Name string `json:"name"`
}

// newWorkflowInputs is qory.workflow for a session of the workflow name, with
// workflows the composed stack's table (compose.Result.Stack.Workflows).
func newWorkflowInputs(workflows map[string]stack.Workflow, name string) workflowInputs {
	return workflowInputs{Judges: workflowJudges(workflows, name), Name: name}
}
