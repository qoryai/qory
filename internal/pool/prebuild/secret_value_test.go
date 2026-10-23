package prebuild_test

import (
	"testing"
)

// TestARelativeStateWithASecretIsShownAsAPhrase refuses a relative state directory whose
// name contains an access key secret with the phrase in place of the directory.
func TestARelativeStateWithASecretIsShownAsAPhrase(t *testing.T) {
	e := newEnv(t)
	e.settings.State = "qak_state"
	_, err := e.launch(t, false)
	if want := "the state directory (a value that contains an access key secret) is not an absolute path"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}
