package cmd

import (
	"slices"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/stack"
)

// TestExpectedReviewers checks the order of the group, a member with no paths and a
// member whose paths match no changed file.
func TestExpectedReviewers(t *testing.T) {
	members := []compose.Entry{
		{Name: "python-reviewer", Paths: []string{"**/*.py", "pyproject.toml"}},
		{Name: "security-reviewer"},
		{Name: "docs-reviewer", Paths: []string{"docs/**"}},
		{Name: "go-reviewer", Paths: []string{"**/*.go"}},
	}
	tests := []struct {
		name    string
		changed []string
		want    []string
	}{
		{"group order, not match order", []string{"cmd/a.go", "src/b.py"},
			[]string{"python-reviewer", "security-reviewer", "go-reviewer"}},
		{"no paths means included", []string{"README.md"}, []string{"security-reviewer"}},
		{"no match means left out", []string{"docs/a.md"},
			[]string{"security-reviewer", "docs-reviewer"}},
		{"second glob of a member", []string{"pyproject.toml"},
			[]string{"python-reviewer", "security-reviewer"}},
		{"no changed file", nil, []string{"security-reviewer"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := expectedReviewers(members, tc.changed); !slices.Equal(got, tc.want) {
				t.Errorf("expectedReviewers = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestExpectedReviewersForUsesTheReviewGroup picks the members of the group that the
// workflow's review_group names, from two groups, and none for a workflow with no group
// or a group the result lacks.
func TestExpectedReviewersForUsesTheReviewGroup(t *testing.T) {
	res := &compose.Result{
		Stack: &stack.Stack{Workflows: map[string]stack.Workflow{
			"review-changes":  {ReviewGroup: "reviewers"},
			"verify-a-ticket": {ReviewGroup: "verify-reviewers"},
			"plain":           {},
			"lost":            {ReviewGroup: "gone"},
		}},
		Groups: map[string][]string{
			"agents/reviewers":        {"go-reviewer", "docs-reviewer", "security-reviewer"},
			"agents/verify-reviewers": {"verify-reviewer", "docs-reviewer"},
		},
		Entries: []compose.Entry{
			{Kind: "agents", Name: "go-reviewer", Paths: []string{"**/*.go"}},
			{Kind: "agents", Name: "docs-reviewer", Paths: []string{"docs/**"}},
			{Kind: "agents", Name: "security-reviewer"},
			{Kind: "agents", Name: "verify-reviewer"},
			{Kind: "skills", Name: "security-reviewer", Paths: []string{"never/**"}},
		},
	}
	tests := []struct {
		workflow string
		changed  []string
		want     []string
	}{
		{"review-changes", []string{"cmd/a.go"}, []string{"go-reviewer", "security-reviewer"}},
		{"review-changes", []string{"docs/a.md"}, []string{"docs-reviewer", "security-reviewer"}},
		{"verify-a-ticket", []string{"cmd/a.go"}, []string{"verify-reviewer"}},
		{"verify-a-ticket", []string{"docs/a.md"}, []string{"verify-reviewer", "docs-reviewer"}},
		{"plain", []string{"cmd/a.go"}, nil},
		{"lost", []string{"cmd/a.go"}, nil},
		{"missing", []string{"cmd/a.go"}, nil},
	}
	for _, tc := range tests {
		if got := expectedReviewersFor(res, tc.workflow, tc.changed); !slices.Equal(got, tc.want) {
			t.Errorf("%s %v: %v, want %v", tc.workflow, tc.changed, got, tc.want)
		}
	}
	if got := expectedReviewersFor(nil, "review-changes", nil); got != nil {
		t.Errorf("a nil result: %v, want none", got)
	}
}
