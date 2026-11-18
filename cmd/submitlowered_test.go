package cmd_test

import (
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
)

// TestARunLoweredBelowWriteRunsNoSubmitStep is a run whose documents have access write and a
// submit, of a harness with a submit program, whose stack declares the workflow
// write-no-push or read: the run sets no hand-off variable, prints no submit line, starts no
// container for the program, has no step of kind or name submit and stores no version. The
// same documents at write run the program, so the others differ by the stack's level alone.
// The levels gate the run at "submitLevel == stack.AccessWrite" in the run's setup; the call
// of limitSubmit before it only empties the settings that nothing else reads.
func TestARunLoweredBelowWriteRunsNoSubmitStep(t *testing.T) {
	for _, c := range []struct {
		access string
		write  bool
	}{{"write", true}, {"write-no-push", false}, {"read", false}} {
		t.Run(c.access, func(t *testing.T) {
			p, log, _ := newProgramRepo(t, c.access, "fill", 0)
			p.change = func(d map[string]any) {
				d["access"] = "write"
				d["submit"] = map[string]any{"type": "code-change", "send": "after_gate"}
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
			ran := strings.Contains(checkLog(t, log), "start")
			line := strings.Contains(r.out, "qory run: submit:")
			if handoff != c.write || ran != c.write {
				t.Errorf("hand-off variable %v and program run %v, want %v for both", handoff, ran, c.write)
			}
			if c.write {
				return
			}
			if line {
				t.Errorf("the run prints a submit line:\n%s", r.out)
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
