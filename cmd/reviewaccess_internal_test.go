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

// TestJoinsAsReview is the table of later sessions: a read session with a review group
// joins a run at write, and nothing else that differs from the run's level does. A run at
// write-no-push pushes no commit, so no suite of a commit exists for a review to read.
func TestJoinsAsReview(t *testing.T) {
	table := map[string]stack.Workflow{
		"review": {Access: stack.AccessRead, ReviewGroup: "reviewers"},
		"plain":  {Access: stack.AccessRead},
		"fix":    {Access: stack.AccessWrite, ReviewGroup: "reviewers"},
	}
	for _, c := range []struct {
		name, got, level string
		want             bool
	}{
		{"review", stack.AccessRead, stack.AccessWrite, true},
		{"review", stack.AccessRead, stack.AccessWriteNoPush, false},
		{"review", stack.AccessRead, stack.AccessRead, false}, // equal: not a review join
		{"plain", stack.AccessRead, stack.AccessWrite, false},
		{"fix", stack.AccessWrite, stack.AccessRead, false},
		{"fix", stack.AccessWrite, stack.AccessWriteNoPush, false},
		{"review", stack.AccessWrite, stack.AccessRead, false},
		{"missing", stack.AccessRead, stack.AccessWrite, false},
	} {
		if got := joinsAsReview(table, c.name, c.got, c.level); got != c.want {
			t.Errorf("joinsAsReview(%q, %s, %s) = %t, want %t", c.name, c.got, c.level, got, c.want)
		}
	}
}

// A session is a Verify session only when the run is at write-no-push, the entry of its
// workflow declares write-no-push too, and the entry has a review_group.
func TestVerifiesAsReview(t *testing.T) {
	workflows := map[string]stack.Workflow{
		"verify-a-ticket":   {Access: stack.AccessWriteNoPush, ReviewGroup: "verify-reviewers"},
		"plan":              {Access: stack.AccessWriteNoPush},
		"review-changes":    {Access: stack.AccessRead, ReviewGroup: "reviewers"},
		"implement-changes": {Access: stack.AccessWrite, ReviewGroup: "reviewers"},
	}
	for _, c := range []struct {
		name, workflow, level string
		want                  bool
	}{
		{"a Verify session", "verify-a-ticket", stack.AccessWriteNoPush, true},
		{"no review group", "plan", stack.AccessWriteNoPush, false},
		{"a read entry in a run at write-no-push", "review-changes", stack.AccessWriteNoPush, false},
		{"a write entry in a run at write-no-push", "implement-changes", stack.AccessWriteNoPush, false},
		{"a write-no-push entry in a run at write", "verify-a-ticket", stack.AccessWrite, false},
		{"a write-no-push entry in a run at read", "verify-a-ticket", stack.AccessRead, false},
		{"a workflow the table lacks", "missing", stack.AccessWriteNoPush, false},
	} {
		if got := verifiesAsReview(workflows, c.workflow, c.level); got != c.want {
			t.Errorf("%s: verifiesAsReview is %t, want %t", c.name, got, c.want)
		}
	}
	if verifiesAsReview(nil, "verify-a-ticket", stack.AccessWriteNoPush) {
		t.Error("a nil table is a Verify session")
	}
}
