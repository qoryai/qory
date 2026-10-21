package cmd_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// fakeGit puts a git first on the PATH that prints printed for git version and runs the
// git the PATH found before for every other command, and returns the file in which it
// logs the arguments of each run. With slow, git version waits 1 s before it prints.
// With old, git worktree list -z exits as git 2.34 does, on the unknown switch.
func fakeGit(t *testing.T, printed string, slow, old bool) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on the PATH")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "runs.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n"
	if slow {
		script += "if [ \"$1\" = version ]; then sleep 1; fi\n"
	}
	script += "if [ \"$1\" = version ]; then printf '%s\\n' '" + printed + "'; exit 0; fi\n"
	if old {
		script += "case \" $* \" in *\" worktree \"*\" -z \"*) echo \"error: unknown switch \\`z'\" >&2; exit 129;; esac\n"
	}
	script += "exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// runs is the arguments of each run of the fake git that logs to log, in order.
func runs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// TestAGitOlderThan236StopsEveryCommandThatReadsGit runs commands that read git with a
// git that prints 2.34.1 and refuses git worktree list -z, as git 2.34 does: each stops
// with the line that names the version, with exit status 1, before it runs any git but
// git version, and neither its output nor its error says the folder is outside a git
// working tree.
func TestAGitOlderThan236StopsEveryCommandThatReadsGit(t *testing.T) {
	want := "Install git 2.36 or later. qory reads git's worktree list in a form that git 2.34.1 does not print."
	for _, args := range [][]string{
		{"worktree", "list"},
		{"harness", "compose"},
		{"setup", "repo", "--accept-checkout"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newCheckout(t)
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
			t.Setenv("XDG_DATA_HOME", filepath.Join(os.Getenv("HOME"), ".local", "share"))
			if err := cmd.RecordCheckout(root); err != nil {
				t.Fatal(err)
			}
			log := fakeGit(t, "git version 2.34.1", false, true)
			out, err := run(t, args...)
			if err == nil || err.Error() != want {
				t.Errorf("got %v, exit status %d, want %q\n%s", err, cmd.ExitCode(err), want, out)
			}
			if code := cmd.ExitCode(err); code != 1 {
				t.Errorf("exit status %d, want 1", code)
			}
			shown := out
			if err != nil {
				shown += err.Error()
			}
			lacks(t, shown, "not inside a git working tree")
			if got := runs(t, log); !slices.Equal(got, []string{"version"}) {
				t.Errorf("git ran %q, want only git version", got)
			}
		})
	}
}

// TestAGitOf236OrLaterRunsTheCommands lists the worktrees with a git that prints 2.36
// or later, in the forms git prints on macOS and Windows, and with output qory cannot
// read: the command runs.
func TestAGitOf236OrLaterRunsTheCommands(t *testing.T) {
	for _, printed := range []string{
		"git version 2.36.0",
		"git version 2.39.5 (Apple Git-154)",
		"git version 2.36.0.windows.1",
		"not a version",
	} {
		t.Run(printed, func(t *testing.T) {
			root := newCheckout(t)
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
			t.Setenv("XDG_DATA_HOME", filepath.Join(os.Getenv("HOME"), ".local", "share"))
			fakeGit(t, printed, false, false)
			if out, err := run(t, "worktree", "list"); err != nil {
				t.Fatalf("got %v\n%s", err, out)
			}
		})
	}
}
