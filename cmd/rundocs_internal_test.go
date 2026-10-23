package cmd

import (
	"testing"
	"time"
)

// TestARunCodesTimeLimitLineNamesWhereTheLimitIsSet is the line of a run from a run code
// whose time limit came before the runtime started: its fix is a longer limit in Qory
// Apiary or in the workflow, never --timeout, which such a run does not take.
func TestARunCodesTimeLimitLineNamesWhereTheLimitIsSet(t *testing.T) {
	r := &codeRun{workflow: "example", stack: "https://code.example.com/acme/stacks#4b8e2f1a9c7d3e5b0a6f8c2d4e1b9a7c5f3d0e2b:stacks/example"}
	want := "set a longer time_limit_minutes in Qory Apiary, or a longer workflows.example.time_limit in the stack https://code.example.com/acme/stacks#4b8e2f1a9c7d3e5b0a6f8c2d4e1b9a7c5f3d0e2b:stacks/example: the run was cancelled at its time limit of 45m0s, before claude started"
	if got := r.timedOutLine(45*time.Minute, "claude"); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
