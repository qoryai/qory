package cmd

import (
	"slices"
	"testing"

	"github.com/qoryai/qory/internal/compose"
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
