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

func TestReviewGroupOf(t *testing.T) {
	table := map[string]stack.Workflow{
		"review-changes":  {ReviewGroup: "reviewers"},
		"verify-a-ticket": {Access: stack.AccessWriteNoPush, ReviewGroup: "verify-reviewers"},
		"bare":            {},
	}
	for name, want := range map[string]string{
		"review-changes": "reviewers", "verify-a-ticket": "verify-reviewers", "bare": "", "missing": "",
	} {
		if got := reviewGroupOf(table, name); got != want {
			t.Errorf("reviewGroupOf(%q) = %q, want %q", name, got, want)
		}
	}
	if got := reviewGroupOf(nil, "any"); got != "" {
		t.Errorf("a nil table: %q, want none", got)
	}
}

// Only a session whose stack entry names a review_group gets qory.review_session.
func TestStartsReviewers(t *testing.T) {
	no := false
	workflows := map[string]stack.Workflow{
		"review-changes":    {Access: "read", ReviewGroup: "reviewers"},
		"verify-a-ticket":   {Access: "write-no-push", ReviewGroup: "verify-reviewers"},
		"implement-changes": {Access: "write", Judges: &no},
	}
	for name, want := range map[string]bool{
		"review-changes":    true,
		"verify-a-ticket":   true,
		"implement-changes": false,
		"not-in-the-table":  false,
	} {
		if got := startsReviewers(workflows, name); got != want {
			t.Errorf("startsReviewers(%q) = %v, want %v", name, got, want)
		}
	}
}
