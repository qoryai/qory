package hostgit_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/places"
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

// tooOldLine is the line for git 2.34.1.
const tooOldLine = "Install git 2.36 or later. qory reads git's worktree list in a form that git 2.34.1 does not print."

// TestAGitOlderThan236Stops runs git through hostgit with a git that prints each
// version: one older than 2.36 stops with the line that names it, as git printed it, and
// runs no git; 2.36 and later, in the forms git prints on macOS and Windows, and output
// qory cannot read, let git run.
func TestAGitOlderThan236Stops(t *testing.T) {
	for printed, version := range map[string]string{
		"git version 2.34.1":                 "2.34.1",
		"git version 2.35.9 (Apple Git-150)": "2.35.9",
		"git version 2.34.1.windows.1":       "2.34.1.windows.1",
		"git version 1.9.5":                  "1.9.5",
	} {
		t.Run(printed, func(t *testing.T) {
			hermetic(t)
			log := fakeGit(t, printed, false, true)
			_, err := hostgit.Command(context.Background(), t.TempDir(), "status")
			want := "Install git 2.36 or later. qory reads git's worktree list in a form that git " + version + " does not print."
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
			if got := runs(t, log); !slices.Equal(got, []string{"version"}) {
				t.Errorf("git ran %q, want only git version", got)
			}
		})
	}
	for _, printed := range []string{
		"git version 2.36.0",
		"git version 2.47.3",
		"git version 2.39.5 (Apple Git-154)",
		"git version 2.36.0.windows.1",
		"git version 3.0.0",
		"not a version",
		"git version two.forty",
		"",
	} {
		t.Run(printed, func(t *testing.T) {
			hermetic(t)
			fakeGit(t, printed, false, false)
			dir := t.TempDir()
			git(t, dir, "init", "-q")
			c, err := hostgit.Command(context.Background(), dir, "status", "--porcelain")
			if err != nil {
				t.Fatalf("got %v, want git to run", err)
			}
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git status: %v\n%s", err, out)
			}
		})
	}
}

// TestEveryEntryStopsOnAGitOlderThan236 calls each hostgit entry that runs git, each
// with a different fake git that prints 2.34.1, so that no earlier call found the version:
// each one stops with the line, and no git but git version runs.
func TestEveryEntryStopsOnAGitOlderThan236(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	other := newRepo(t)
	repo, ok := hostgit.Resolve(context.Background(), dir)
	if !ok {
		t.Fatal("no repository")
	}
	recs, err := places.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"Command": func() error { _, err := hostgit.Command(ctx, dir, "status"); return err },
		"Hold":    func() error { _, err := hostgit.Hold(ctx, dir); return err },
		"Record":  func() error { return hostgit.Record(ctx, other, places.KindCheckout, "run-2") },
		"Fingerprint": func() error {
			_, err := hostgit.Fingerprint(ctx, repo)
			return err
		},
		"Changes": func() error { _, err := hostgit.Changes(ctx, recs, repo); return err },
	} {
		t.Run(name, func(t *testing.T) {
			log := fakeGit(t, "git version 2.34.1", false, true)
			if err := call(); err == nil || err.Error() != tooOldLine {
				t.Errorf("got %v, want %q", err, tooOldLine)
			}
			if got := runs(t, log); !slices.Equal(got, []string{"version"}) {
				t.Errorf("git ran %q, want only git version", got)
			}
		})
	}
}

// TestResolveRunsNoGitReadyFoundTooOld finds git 2.34.1 with Ready, then resolves a
// repository: git is not started again, and Resolve finds none.
func TestResolveRunsNoGitReadyFoundTooOld(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	log := fakeGit(t, "git version 2.34.1", false, true)
	ctx := context.Background()
	if err := hostgit.Ready(ctx); err == nil || err.Error() != tooOldLine {
		t.Fatalf("got %v, want %q", err, tooOldLine)
	}
	if _, ok := hostgit.Resolve(ctx, dir); ok {
		t.Error("Resolve found a repository")
	}
	if got := runs(t, log); !slices.Equal(got, []string{"version"}) {
		t.Errorf("git ran %q, want only git version", got)
	}
}

// TestAGitVersionCutShortIsReadAgain ends the context while git version runs: the next
// Ready runs git version again and finds 2.34.1, and Command stops.
func TestAGitVersionCutShortIsReadAgain(t *testing.T) {
	hermetic(t)
	fakeGit(t, "git version 2.34.1", true, true)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = hostgit.Ready(ctx)
	if err := hostgit.Ready(context.Background()); err == nil || err.Error() != tooOldLine {
		t.Errorf("Ready: got %v, want %q", err, tooOldLine)
	}
	if _, err := hostgit.Command(context.Background(), t.TempDir(), "status"); err == nil || err.Error() != tooOldLine {
		t.Errorf("Command: got %v, want %q", err, tooOldLine)
	}
}
