package acting_test

import (
	"testing"

	"github.com/qoryai/qory/internal/acting"
)

// TestAChangedKeyWithASecretInItsNameIsNotPrinted is the refusal of an env variable a
// walled run wrote whose name contains an access key secret: the refusal prints
// Forager's phrase in place of the key.
func TestAChangedKeyWithASecretInItsNameIsNotPrinted(t *testing.T) {
	err := &acting.ChangedError{File: "/w/repo/qory.yaml", Key: "env.qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"}
	want := "/w/repo/qory.yaml changes (a name that contains an access key secret) since a walled run began here; qory does not act on it, since the run could have written it. qory setup repo --accept-checkout accepts it"
	if err.Error() != want {
		t.Errorf("refusal = %q\nwant %q", err.Error(), want)
	}
}
