package explore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/pool/explore"
)

// TestARelativeStateWithASecretIsShownAsAPhrase returns an error for a relative state
// directory whose name contains an access key secret, with the phrase in place of the
// path.
func TestARelativeStateWithASecretIsShownAsAPhrase(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "w"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := explore.RecordDir("qak_state", filepath.Join(work, "w"), testRunID)
	if want := "the run's state directory (a value that contains an access key secret) is not an absolute path"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}
