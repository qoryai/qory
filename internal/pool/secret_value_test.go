package pool_test

import (
	"context"
	"errors"
	"testing"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/pool/explore"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// TestALaunchErrorWithASecretInAPathIsShownAsAPhrase logs the error of a launch that
// could not start with the part of a path in it that contains an access key secret
// printed as a phrase, and the rest of the error as it is.
func TestALaunchErrorWithASecretInAPathIsShownAsAPhrase(t *testing.T) {
	h := newHarness(t, 1, 1)
	h.setLaunch(func(ctx context.Context, r explore.Run) (explore.Result, error) {
		return explore.Result{Ending: explore.EndingOf(false, child.Result{})}, errors.New("mkdir /var/qak_state/pool: not a directory")
	})
	h.srv.Offer(provisionertest.Run{RunID: runA})
	h.start()
	h.settled(1)
	h.stop()
	m := h.log.with("the run could not be started")
	if want := "mkdir /var/(a name that contains an access key secret)/pool: not a directory"; len(m) != 1 || m[0]["error"] != want {
		t.Errorf("the launch's lines are %v, want the error %q", m, want)
	}
	if accesskey.ContainsSecret(h.log.text()) {
		t.Errorf("a secret was logged:\n%s", h.log.text())
	}
}

// TestARelativePoolFolderWithASecretIsShownAsAPhrase refuses a relative pool's folder
// whose name contains an access key secret with the phrase in place of the folder.
func TestARelativePoolFolderWithASecretIsShownAsAPhrase(t *testing.T) {
	h := newHarness(t, 1, 1)
	h.pool.Settings.State = "qak_pool"
	err := h.pool.Run(context.Background())
	if want := "the pool's folder (a value that contains an access key secret) is not an absolute path"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}
