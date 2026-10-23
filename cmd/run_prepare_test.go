package cmd_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestRunPrepareIsHiddenAndQuietWithoutRepositories is qory run prepare with no
// repositories named: hidden from the help, it does nothing, prints nothing and exits 0,
// in a folder that is no git working tree too.
func TestRunPrepareIsHiddenAndQuietWithoutRepositories(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("QORY_RUN_SYSTEM", "github.com")
	t.Setenv("QORY_RUN_REPOSITORIES", "")
	out, err := run(t, "run", "prepare")
	if err != nil || out != "" {
		t.Fatalf("qory run prepare with no repositories: %v\n%q", err, out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the folder holds %d entries", len(entries))
	}
	if c, _, _ := cmd.Root().Find([]string{"run", "prepare"}); c == nil || c.Name() != "prepare" || !c.Hidden {
		t.Error("qory run prepare is not a hidden verb")
	}
}

// TestRunPrepareFailsInOneLine is a refusal printed once, on one line, and an exit that
// is not 0, which main does not print again.
func TestRunPrepareFailsInOneLine(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("QORY_RUN_SYSTEM", "github.com")
	t.Setenv("QORY_RUN_REPOSITORIES", "acme/shop,other/lib")
	out, err := run(t, "run", "prepare")
	want := "qory run: list in QORY_RUN_REPOSITORIES only repositories of owner acme: entry 2 is of owner other, and on github.com the repositories of a run are of one owner\n"
	if out != want {
		t.Errorf("printed %q, want %q", out, want)
	}
	if !errors.Is(err, cmd.ErrReported) || cmd.ExitCode(err) == 0 {
		t.Errorf("%v exits %d and is reported %v", err, cmd.ExitCode(err), errors.Is(err, cmd.ErrReported))
	}
	if _, err := os.Stat(".git"); !os.IsNotExist(err) {
		t.Errorf("the folder has .git: %v", err)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("printed %d lines", strings.Count(out, "\n"))
	}
}
