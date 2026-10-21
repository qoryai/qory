package explore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/pool/explore"
)

// TestARelativeFolderWithASecretIsShownAsAPhrase refuses a relative state directory,
// runs' configuration directory or harness path whose name contains an access key secret
// with the phrase in place of the path.
func TestARelativeFolderWithASecretIsShownAsAPhrase(t *testing.T) {
	for _, c := range []struct {
		edit func(*explore.Settings)
		want string
	}{
		{func(s *explore.Settings) { s.State = "qak_state" }, "the state directory (a value that contains an access key secret) is not an absolute path"},
		{func(s *explore.Settings) { s.RunConfig = "qak_runs" }, "the runs' configuration directory (a value that contains an access key secret) is not an absolute path"},
		{func(s *explore.Settings) { s.HarnessRef, s.HarnessPath = "", "qak_harness" }, "the harness path (a value that contains an access key secret) is not an absolute path"},
	} {
		e := newEnv(t, script{Record: true})
		c.edit(&e.settings)
		_, err := explore.Launch(context.Background(), e.settings, explore.Run{RunID: testRunID, Key: &holder{b: []byte(testKey)}, Inputs: []byte(testInputs)})
		if err == nil || err.Error() != c.want {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "w"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := explore.RecordDir("qak_state", filepath.Join(work, "w"), testRunID)
	if want := "the run's state directory (a value that contains an access key secret) is not an absolute path"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}
