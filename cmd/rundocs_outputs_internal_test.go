package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/report"
	"github.com/qoryai/qory/internal/runapi"
)

// stageRun is a run from a run code whose work item snapshot has the stage outputs.
func stageRun(outputs ...runapi.Output) *codeRun {
	return &codeRun{snapshot: &runapi.Snapshot{Outputs: outputs}}
}

// TestTheInputsListTheStagesOutputsInOrderWithTheirSchemas is inputs.json's member qory
// for a stage with two outputs, the first required and defined in the composed
// process.yaml with a $ref to the primitives, the second not required and not defined
// there: outputs lists both in the stage's order, each with required, the first with its
// schema with the primitive written out, the second with the schema null. A stage with no
// outputs, and a run with no snapshot, write "outputs": [].
func TestTheInputsListTheStagesOutputsInOrderWithTheirSchemas(t *testing.T) {
	home := t.TempDir()
	module := filepath.Join(home, "modules", "factory")
	if err := os.MkdirAll(module, 0o755); err != nil {
		t.Fatal(err)
	}
	process := "artifact_types:\n  - name: reply\n    base_view: structured\n    json_schema:\n      type: object\n      properties:\n        title: {$ref: \"" + primitives.URL + "#/$defs/title\"}\n"
	if err := os.WriteFile(filepath.Join(module, "process.yaml"), []byte(process), 0o600); err != nil {
		t.Fatal(err)
	}
	types, found := harnessTypes(report.Report{Home: home, Modules: []report.Module{{Name: "factory"}}})
	if !found {
		t.Fatal("no process.yaml found")
	}
	r := stageRun(runapi.Output{Type: "reply", Required: true}, runapi.Output{Type: "screenshot"})
	data, err := withQoryMember([]byte(`{}`), qoryInputs{Outputs: r.outputs(types)})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Qory struct {
			Outputs []struct {
				Type     string         `json:"type,case:strict"`
				Required bool           `json:"required,case:strict"`
				Schema   map[string]any `json:"schema,case:strict"`
			} `json:"outputs,case:strict"`
		} `json:"qory,case:strict"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	outs := got.Qory.Outputs
	if len(outs) != 2 || outs[0].Type != "reply" || !outs[0].Required || outs[1].Type != "screenshot" || outs[1].Required {
		t.Fatalf("outputs are %+v\n%s", outs, data)
	}
	if outs[0].Schema == nil || strings.Contains(string(data), primitives.URL) || !strings.Contains(string(data), `"allOf"`) {
		t.Errorf("the reply schema does not have the primitive written out:\n%s", data)
	}
	if outs[1].Schema != nil || !strings.Contains(string(data), `"type":"screenshot","required":false,"schema":null`) {
		t.Errorf("the screenshot schema is not null:\n%s", data)
	}
	for name, r := range map[string]*codeRun{"no outputs": stageRun(), "no snapshot": {}, "no run code": nil} {
		data, err := withQoryMember([]byte(`{}`), qoryInputs{Outputs: r.outputs(types)})
		if err != nil || !strings.Contains(string(data), `"outputs":[]`) {
			t.Errorf("%s: %s, %v, want \"outputs\":[]", name, data, err)
		}
	}
}
