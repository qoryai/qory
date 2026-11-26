package cmd

import (
	"encoding/json"
	"strings"
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

// TestQoryInputsHaveTheWorkflowOfARunCode is the qory.workflow member as the session of a
// run code gets it, and as a run with no run code gets it, which has none.
func TestQoryInputsHaveTheWorkflowOfARunCode(t *testing.T) {
	no := false
	workflows := map[string]stack.Workflow{"implement-changes": {Judges: &no}, "plan-the-build": {}}
	for name, want := range map[string]string{
		"implement-changes": `"workflow":{"judges":false}`,
		"plan-the-build":    `"workflow":{"judges":true}`,
	} {
		inputs := newWorkflowInputs(workflows, name)
		data, err := json.Marshal(qoryInputs{Workflow: &inputs})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s: qory = %s, want %s", name, data, want)
		}
	}
	data, err := json.Marshal(qoryInputs{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"workflow"`) {
		t.Errorf("a run with no run code: qory = %s, want no workflow member", data)
	}
}
