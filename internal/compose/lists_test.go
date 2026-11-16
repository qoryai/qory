package compose_test

import (
	"testing"

	"github.com/qoryai/qory/internal/compose"
)

// TestAListOfThreeNamesHasASerialComma is the lists a refusal makes of names: one, two,
// three and four names, where three or more have a comma before the conjunction.
func TestAListOfThreeNamesHasASerialComma(t *testing.T) {
	for _, c := range []struct {
		names   []string
		and, or string
	}{
		{nil, "", ""},
		{[]string{"a"}, "a", "a"},
		{[]string{"a", "b"}, "a and b", "a or b"},
		{[]string{"a", "b", "c"}, "a, b, and c", "a, b, or c"},
		{[]string{"a", "b", "c", "d"}, "a, b, c, and d", "a, b, c, or d"},
	} {
		if got := compose.AndList(c.names); got != c.and {
			t.Errorf("AndList(%v) = %q, want %q", c.names, got, c.and)
		}
		if got := compose.OrList(c.names); got != c.or {
			t.Errorf("OrList(%v) = %q, want %q", c.names, got, c.or)
		}
	}
}
