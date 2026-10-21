package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/accesskey"

	"github.com/qoryai/qory/internal/foragerdir"
)

// poolSecretState sets XDG_STATE_HOME to a folder whose name contains an access key
// secret, after [newPoolMachine], and returns the pool's folder under it and that folder
// as a refusal prints it.
func poolSecretState(t *testing.T) (state, shown string) {
	t.Helper()
	base := tempDir(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "qak_state"))
	state = filepath.Join(base, "qak_state", "qory", "pool")
	shown = filepath.Join(base, "(a name that contains an access key secret)", "qory", "pool")
	return state, shown
}

// TestRunPoolHidesASecretInTheLockedStatePath refuses a second qory run pool by the
// pool's folder, with the part of its path that contains an access key secret printed
// as a phrase.
func TestRunPoolHidesASecretInTheLockedStatePath(t *testing.T) {
	newPoolMachine(t)
	state, shown := poolSecretState(t)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := foragerdir.LockFile(filepath.Join(state, "pool.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	started(t)
	out, err := run(t, "run", "pool")
	if want := "another qory run pool is running with " + shown; err == nil || err.Error() != want || out != "" {
		t.Errorf("%v, printed %q, want %q", err, out, want)
	}
	if err != nil && accesskey.ContainsSecret(out+err.Error()) {
		t.Errorf("a secret was printed: %v", err)
	}
}

// TestRunPoolHidesASecretInTheStatePathItCannotMake refuses a pool's folder that is a
// file, with the part of its path that contains an access key secret printed as a
// phrase.
func TestRunPoolHidesASecretInTheStatePathItCannotMake(t *testing.T) {
	newPoolMachine(t)
	state, shown := poolSecretState(t)
	writeFile(t, state, "not a folder\n")
	started(t)
	out, err := run(t, "run", "pool")
	if err == nil || !strings.Contains(err.Error(), shown) || accesskey.ContainsSecret(out+err.Error()) {
		t.Errorf("%v, printed %q, want the path as %s", err, out, shown)
	}
}
