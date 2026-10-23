package pool_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/pool/child"
	"github.com/qoryai/qory/internal/provisioner/provisionertest"
)

// TestAStartErrorWithASecretInAPathIsShownAsAPhrase logs the error of a qory run that
// could not start, whose program is in a folder whose name contains an access key
// secret, with the phrase in place of that part of the path, and nothing released.
func TestAStartErrorWithASecretInAPathIsShownAsAPhrase(t *testing.T) {
	h := newHarness(t, 1)
	h.pool.Settings.Qory = filepath.Join(h.root, "qak_bin", "qory")
	h.srv.Offer(provisionertest.Run{})
	h.start()
	h.settled(1)
	h.stop()
	m := h.log.with("the run could not be started")
	want := filepath.Join(h.root, "(a name that contains an access key secret)", "qory")
	if len(m) != 1 {
		t.Fatalf("the start's lines are %v", m)
	}
	if e, _ := m[0]["error"].(string); child.ContainsSecret(e) || !strings.Contains(e, want) {
		t.Errorf("the start's error is %q, want one with %s", e, want)
	}
	h.notReleased()
}

// TestARelativePoolFolderWithASecretIsShownAsAPhrase refuses a relative pool's folder
// whose name contains an access key secret with the phrase in place of the folder.
func TestARelativePoolFolderWithASecretIsShownAsAPhrase(t *testing.T) {
	h := newHarness(t, 1)
	h.pool.Settings.State = "qak_pool"
	err := h.pool.Run(context.Background())
	if want := "the pool's folder (a value that contains an access key secret) is not an absolute path"; err == nil || err.Error() != want {
		t.Errorf("%v, want %q", err, want)
	}
}
