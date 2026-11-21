package cmd

import (
	"encoding/json"
	"testing"

	"github.com/qoryai/qory/internal/stack"
)

func TestNewWorkflowInputsJudges(t *testing.T) {
	no, yes := false, true
	workflows := map[string]stack.Workflow{
		"implement-changes": {Judges: &no},
		"review-changes":    {Judges: &yes},
		"plan-the-build":    {},
	}
	for name, want := range map[string]string{
		"implement-changes": `{"judges":false}`,
		"review-changes":    `{"judges":true}`,
		"plan-the-build":    `{"judges":true}`,
		"not-in-the-table":  `{"judges":true}`,
	} {
		data, err := json.Marshal(newWorkflowInputs(workflows, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Errorf("%s: qory.workflow = %s, want %s", name, data, want)
		}
	}
}
