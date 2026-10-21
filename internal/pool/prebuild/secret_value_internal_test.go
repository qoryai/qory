package prebuild

import (
	"errors"
	"slices"
	"testing"
)

// TestAnErrorWithASecretIsLoggedAsAPhrase logs what failed and the error after the run
// id, with the part of a path in the error that contains an access key secret printed
// as a phrase: the line of a run directory or a local tag that could not be removed.
func TestAnErrorWithASecretIsLoggedAsAPhrase(t *testing.T) {
	var got []string
	s := Settings{Log: func(line string) { got = append(got, line) }}
	s.logError("r1", "the run directory could not be removed: ", errors.New("unlinkat /state/qak_x/pool: permission denied"))
	want := []string{"r1: the run directory could not be removed: unlinkat /state/(a name that contains an access key secret)/pool: permission denied"}
	if !slices.Equal(got, want) {
		t.Errorf("logged %q, want %q", got, want)
	}
}
