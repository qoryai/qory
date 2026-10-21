package pool

import (
	"path/filepath"
	"testing"
)

// TestAJournalEntryWithASecretIsShownAsAPhrase refuses to write an entry that is not one,
// with its dispatch id, which contains an access key secret, printed as a phrase.
func TestAJournalEntryWithASecretIsShownAsAPhrase(t *testing.T) {
	j := &journal{path: filepath.Join(t.TempDir(), JournalFile)}
	err := j.write([]Entry{{DispatchID: "dp_qak_x", RunID: "r", Step: "explore", Phase: PhaseClaimed}})
	if want := "the dispatch (a name that contains an access key secret) cannot be written to the journal"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}
