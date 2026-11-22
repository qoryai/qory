package cmd_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/primitives"
)

// outputsAPI serves a run API whose work item's stage has outputs, the JSON list of the
// snapshot's outputs, and counts the reads of the work item.
// shortcut: the snapshot follows the run API contract at integrations effc22aa, not a
// response of Qory Apiary; run the test against Qory Apiary once its card A-78 lands.
func outputsAPI(t *testing.T, outputs string) (string, *atomic.Int32) {
	t.Helper()
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runs/r1/work-item" {
			http.NotFound(w, r)
			return
		}
		reads.Add(1)
		fmt.Fprintf(w, `{"version":1,"work_item":{"id":%q,"number":1},"run":{"id":"r1","number":57},"stage":{"id":"st_a"},"inputs":[],"outputs":%s}`, workItem, outputs)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1/runs/r1", &reads
}

// TestADispatchedRunWithNoRepositoryWritesTheStagesOutputs is a run from a run code whose
// run configuration lists no repository, of a work item whose stage has the output reply,
// required, which the module hello's process.yaml defines with a $ref to the primitives:
// the run reads the work item once, and the member qory of inputs.json lists reply with
// required true and its schema with the primitive written out.
func TestADispatchedRunWithNoRepositoryWritesTheStagesOutputs(t *testing.T) {
	api, reads := outputsAPI(t, `[{"type":"reply","required":true,"version":null,"check":null}]`)
	codeRunAPI = api
	t.Cleanup(func() { codeRunAPI = "" })
	process := "artifact_types:\n  - name: reply\n    base_view: structured\n    size_limit: 65536\n    max_files: 1\n    json_schema:\n      type: object\n      properties:\n        title: {$ref: \"" + primitives.URL + "#/$defs/title\"}\n"
	r := runFromCodeLinks(t, behindAWall(t), nil, nil, nil, map[string]string{"examples/hello/modules/hello/process.yaml": process}, nil)
	if !errors.Is(r.err, cmd.ErrJoinedNowhere) || !r.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("the run read the work item %d times, want 1", n)
	}
	member := qoryMember(t, r.inputs)
	wants(t, member, `"outputs":[{"type":"reply","required":true,"schema":{`, `"allOf":[`)
	lacks(t, member, primitives.URL)
}
