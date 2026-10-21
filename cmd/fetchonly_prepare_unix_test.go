//go:build unix

package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestRunPrepareFetchOnly is qory run prepare --fetch-only, the launch of a fetch-only
// run, in a run whose image has a copy of the repository and whose repository has start
// commands: it fetches the head of the default branch into the working folder, starts
// from no copy and runs no command, and prints one line.
func TestRunPrepareFetchOnly(t *testing.T) {
	dir, base := prepareRemote(t, map[string]string{
		".devcontainer/devcontainer.json": `{"onCreateCommand": "touch onCreate", "postCreateCommand": "touch created", "postStartCommand": "touch started"}`,
	})
	marker := filepath.Join(tempDir(t), "ran")
	imageCopy(t, marker)
	out, err := run(t, "run", "prepare", "--fetch-only")
	if err != nil {
		t.Fatalf("qory run prepare --fetch-only: %v\n%s", err, out)
	}
	if got, want := prepareTimes.ReplaceAllString(out, " in Xs"), "qory run prepare: fetched acme/shop in Xs\n"; got != want {
		t.Errorf("printed %q, want %q", got, want)
	}
	if got, want := gitOut(t, dir, "rev-parse", "HEAD"), gitOut(t, filepath.Join(base, "acme", "shop.git"), "rev-parse", "main"); got != want {
		t.Errorf("HEAD is %s, want %s", got, want)
	}
	for _, name := range []string{"onCreate", "created", "started", "notes.txt", "deps"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s is in the working folder: %v", name, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("a hook of the copy ran: %v", err)
	}
}

// TestRunPrepareFetchOnlyNeedsARepository is qory run prepare --fetch-only with no
// repository named: one line, an exit that is not 0, and nothing fetched.
func TestRunPrepareFetchOnlyNeedsARepository(t *testing.T) {
	dir, _ := prepareRemote(t, nil)
	t.Setenv("QORY_RUN_REPOSITORIES", "")
	out, err := run(t, "run", "prepare", "--fetch-only")
	if want := "qory run prepare: --fetch-only fetches the run's repository, and QORY_RUN_REPOSITORIES names none\n"; out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if !errors.Is(err, cmd.ErrReported) || cmd.ExitCode(err) == 0 {
		t.Errorf("%v exits %d", err, cmd.ExitCode(err))
	}
	if got := gitOut(t, dir, "rev-list", "--all"); got != "" {
		t.Errorf("commits were fetched: %q", got)
	}
}
