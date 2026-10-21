package prebuild_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/pool/prebuild"
	"github.com/qoryai/qory/internal/provisioner"
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

// TestALaunchErrorWithASecretInAPathIsLoggedAsAPhrase logs the error of a prebuild whose
// state directory, a file here, is under a folder whose name contains an access key
// secret: the line has the phrase in place of that part of the path, and the rest of the
// error, in place of a line left out whole.
func TestALaunchErrorWithASecretInAPathIsLoggedAsAPhrase(t *testing.T) {
	e := newEnv(t)
	root := filepath.Dir(e.settings.State)
	e.settings.State = filepath.Join(root, "qak_state")
	if err := os.WriteFile(e.settings.State, []byte("not a folder\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := func(context.Context, provisioner.Image) error { return nil }
	prebuild.Launcher(e.settings)(context.Background(), testRunID, testTarget, &holder{b: []byte(testKey)}, []byte(testInputs), report)
	lines := e.lines()
	shown := filepath.Join(root, "(a name that contains an access key secret)")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], testRunID+": ") || !strings.Contains(lines[0], shown+": not a directory") || accesskey.ContainsSecret(lines[0]) {
		t.Errorf("logged %q, want the error with %s", lines, shown)
	}
}
