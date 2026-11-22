package cmd_test

import (
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
)

// TestARunLoweredBelowWriteRunsNoSubmitStep is a run whose documents have access write and a
// deliver, of a harness with a submit program, whose stack declares the workflow write,
// write-no-push or read: the run starts no program. Only the run at write sets the hand-off
// variable, and the others have no step of kind or name submit and store no version. The levels
// gate the run at "submitLevel == stack.AccessWrite" in the run's setup. The deliver line is not
// checked here: this harness has no process.yaml, so the run prints none at any level, and
// TestARunBelowWritePrintsNoDeliverLine checks it.
func TestARunLoweredBelowWriteRunsNoSubmitStep(t *testing.T) {
	for _, c := range []struct {
		access string
		write  bool
	}{{"write", true}, {"write-no-push", false}, {"read", false}} {
		t.Run(c.access, func(t *testing.T) {
			p, log := newProgramRepo(t, c.access)
			p.change = func(d map[string]any) {
				d["access"] = "write"
				d["deliver"] = []any{map[string]any{"type": "code-change", "send": "after_gate"}}
			}
			var variables []string
			p.mountsOf = func(spec session.SessionSpec) { variables = spec.Variables.Run }
			agent := writesAndCommits(t)
			r := p.run(t, agent, nil)
			r.wantsClose(t, event.StateSucceeded, 0)
			handoff := false
			for _, v := range variables {
				handoff = handoff || strings.HasPrefix(v, "QORY_SUBMISSION_HANDOFF=")
			}
			if handoff != c.write {
				t.Errorf("hand-off variable %v, want %v", handoff, c.write)
			}
			wantsNoProgram(t, r, log)
			if c.write {
				return
			}
			for _, e := range r.steps {
				if strings.Contains(strings.ToLower(e.Kind+" "+e.Name), "submit") {
					t.Errorf("a submit step: %+v", e)
				}
			}
			if posts, _, stored := p.api.counts(); posts != 0 || stored != 0 {
				t.Errorf("%d POST and %d versions, want none", posts, stored)
			}
		})
	}
}
