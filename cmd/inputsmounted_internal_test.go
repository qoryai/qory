package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/integrations/contracts"
)

// TestAMountAtOrInsideTheInputsStopsTheSession is a walled session whose mount is the
// inputs folder or lies inside it: a run from a run code stops
// with the cause and machine_setup_invalid, a run with a run key and no run code with the
// fix and no end code. A mount that contains the folder, or lies beside it, passes.
func TestAMountAtOrInsideTheInputsStopsTheSession(t *testing.T) {
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(top, "qory-inputs-1", "in")
	if err := os.MkdirAll(in, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, mount := range []string{in, filepath.Join(in, "start-artifacts")} {
		given := []givenMount{{path: filepath.Join(top, "other"), from: "--mount"}, {path: mount, from: "--mount"}}
		err := (&codeRun{}).inputsMounted(in, given)
		var reason *reasonError
		want := "A wall mount is at or inside the read-only inputs folder, so the session did not start.\n  Mount " + mount + ".\n  Inputs folder " + in + "."
		if !errors.As(err, &reason) || reason.code != string(contracts.EndCodeMachineSetupInvalid) || err.Error() != want {
			t.Errorf("a run code's mount %s: %v, want %q", mount, err, want)
		}
		err = (*codeRun)(nil).inputsMounted(in, given)
		want = "Remove the mount at or inside the read-only inputs folder from wall.mounts or --mount.\n  Mount " + mount + ".\n  Inputs folder " + in + "."
		if errors.As(err, &reason) || err == nil || err.Error() != want {
			t.Errorf("a run key's mount %s: %v, want %q", mount, err, want)
		}
	}
	for _, mount := range []string{top, filepath.Join(top, "qory-inputs-1"), filepath.Join(top, "other")} {
		if err := (&codeRun{}).inputsMounted(in, []givenMount{{path: mount, from: "--mount"}}); err != nil {
			t.Errorf("the mount %s: %v", mount, err)
		}
	}
}
