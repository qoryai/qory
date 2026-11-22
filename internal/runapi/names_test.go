package runapi_test

import (
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/runapi"
)

// TestArtifactNameStartsWithALetter is the form of an artifact type and an artifact name:
// groups of a-z and 0-9 joined by single hyphens, the first character a letter, at most 64
// bytes. A group after a hyphen may start with a digit.
func TestArtifactNameStartsWithALetter(t *testing.T) {
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"main", true},
		{"pull-request", true},
		{"t-e047707709a59e682f6b3df0e394d0bb", true},
		{"t-0a1b", true},
		{"a", true},
		{"a" + strings.Repeat("b", 63), true},
		{"a" + strings.Repeat("b", 64), false},
		{"1abc", false},
		{"0", false},
		{"a--b", false},
		{"abc-", false},
		{"-abc", false},
		{"Abc", false},
		{"a_b", false},
		{"", false},
	} {
		if got := runapi.ArtifactName(c.name); got != c.ok {
			t.Errorf("ArtifactName(%q) = %v, want %v", c.name, got, c.ok)
		}
	}
}
