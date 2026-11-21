package cmd

import (
	"testing"

	"github.com/qoryai/qory/internal/stack"
)

func TestWorkflowReadsOnly(t *testing.T) {
	table := map[string]stack.Workflow{
		"look":  {Access: stack.AccessRead},
		"fix":   {Access: stack.AccessWrite},
		"stage": {Access: stack.AccessWriteNoPush},
		"bare":  {},
	}
	for name, want := range map[string]bool{
		"look":    true,
		"fix":     false,
		"stage":   false,
		"bare":    true, // an entry with no access reads as read
		"missing": true, // a workflow the table lacks reads as read
	} {
		if got := workflowReadsOnly(table, name); got != want {
			t.Errorf("workflowReadsOnly(%q) = %t, want %t", name, got, want)
		}
	}
	if !workflowReadsOnly(nil, "any") {
		t.Error("a nil table reads as read")
	}
}

func TestWorkflowJudges(t *testing.T) {
	yes, no := true, false
	table := map[string]stack.Workflow{"a": {Judges: &no}, "b": {Judges: &yes}, "c": {}}
	for name, want := range map[string]bool{"a": false, "b": true, "c": true, "missing": true} {
		if got := workflowJudges(table, name); got != want {
			t.Errorf("workflowJudges(%q) = %t, want %t", name, got, want)
		}
	}
}
