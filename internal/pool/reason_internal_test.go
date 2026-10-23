package pool

import (
	"testing"
	"unicode/utf8"
)

// TestEveryReasonFitsTheRelease checks that every reason the pool releases a dispatch
// for is 1 to 200 characters, as release.schema.json requires.
func TestEveryReasonFitsTheRelease(t *testing.T) {
	reasons := []string{
		ReasonNotStarted, ReasonRestarted, ReasonStoppedBeforeStart, ReasonLeasePassed, ReasonMachine,
	}
	for _, r := range reasons {
		if n := utf8.RuneCountInString(r); n < 1 || n > 200 {
			t.Errorf("%q is %d characters, want 1 to 200", r, n)
		}
	}
}
