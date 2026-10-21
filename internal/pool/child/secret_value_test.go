package child_test

import (
	"context"
	"os"
	"testing"

	"github.com/qoryai/qory/internal/pool/child"
)

// TestARelativeFolderWithASecretIsShownAsAPhrase refuses a relative configuration or
// state directory whose name contains an access key secret with the phrase in place of
// the directory.
func TestARelativeFolderWithASecretIsShownAsAPhrase(t *testing.T) {
	for _, c := range []struct {
		cmd  child.Command
		want string
	}{
		{child.Command{Path: os.Args[0], ConfigHome: "qak_config"}, "the runs' configuration directory (a value that contains an access key secret) is not an absolute path"},
		{child.Command{Path: os.Args[0], ConfigHome: t.TempDir(), StateHome: "qak_state"}, "the run's state directory (a value that contains an access key secret) is not an absolute path"},
	} {
		if _, err := child.Run(context.Background(), c.cmd); err == nil || err.Error() != c.want {
			t.Errorf("%v, want %q", err, c.want)
		}
	}
}
