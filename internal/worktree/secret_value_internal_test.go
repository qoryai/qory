package worktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
)

// secret is a value that contains an access key secret; it is not a real key.
const secret = "qak_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"

// valuePhrase is what a message prints in place of a value that contains secret.
const valuePhrase = "(a value that contains an access key secret)"

// TestARunErrorHidesASecretInTheCommandAndItsOutput is a worktree.run.add command with a
// secret that fails: the error prints the command and the copy of it in the output as
// Forager's phrase, and still unwraps to the command's exit error.
func TestARunErrorHidesASecretInTheCommandAndItsOutput(t *testing.T) {
	command := "./setup --token " + secret
	_, err := run("exit 3", t.TempDir(), nil, nil)
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run: %v, want an exit error", err)
	}
	e := &RunError{Command: command, Path: "/w/wt-feature", Verb: "add", Output: "ran " + command + "\nkey qrk_m rejected\n", Err: err}
	want := valuePhrase + " in /w/wt-feature: exit status 3; the worktree is kept, repair the command and add again; it printed:\nran " + valuePhrase + "\nkey (a value that contains a run key) rejected"
	if e.Error() != want {
		t.Errorf("error = %q\nwant %q", e.Error(), want)
	}
	if !errors.As(error(e), &exit) {
		t.Error("the run error lost the exit error under it")
	}
	e = &RunError{Command: "./setup", Path: "/w/wt-" + secret, Verb: "remove", Err: err}
	if got := e.Error(); strings.Contains(got, secret) || !strings.HasPrefix(got, "./setup in "+valuePhrase+": ") {
		t.Errorf("error = %q, want the path as the phrase", got)
	}
}

// hermetic points HOME, XDG and git's global config at an empty directory, so no setting
// of this machine reaches the test's git.
func hermetic(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(home))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hostgit.Forget()
	t.Cleanup(hostgit.Forget)
}

// repository is a new repository with one commit on main, and the branches given cut at
// it; it returns the main checkout.
func repository(t *testing.T, branches ...string) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(base, "repo")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q", "--initial-branch=main")
	gitIn(t, main, "-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "-q", "--allow-empty", "-m", "first")
	for _, b := range branches {
		gitIn(t, main, "branch", b)
	}
	return main
}

// gitIn runs git in dir and fails the test when it fails.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestWorktreeMessagesHideASecretInABranchABaseAndARemote adds worktrees with a secret
// in the base, the branch and the remote's path: each error, the How of the add and
// every trace line print the secret as Forager's phrase.
func TestWorktreeMessagesHideASecretInABranchABaseAndARemote(t *testing.T) {
	hermetic(t)
	main := repository(t, "b"+secret)
	var trace []string
	o := Options{Trace: func(line string) { trace = append(trace, line) }}

	o.Base = "x" + secret
	_, err := Add(main, "feature", o)
	if want := "base " + valuePhrase + " is not a branch, tag or commit of this repository"; err == nil || err.Error() != want {
		t.Errorf("add off a base that is not there: %v, want %q", err, want)
	}

	o.Base = "b" + secret
	a, err := Add(main, "f"+secret, o)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if want := "new off " + valuePhrase; a.How != want {
		t.Errorf("how = %q, want %q", a.How, want)
	}

	gitIn(t, main, "remote", "add", "origin", filepath.Join(t.TempDir(), "r"+secret, "h.git"))
	o.Base = ""
	_, err = RemoteBranch(main, "feature", o)
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), valuePhrase) {
		t.Errorf("ls-remote of a remote with a secret in its path: %v", err)
	}
	_, err = Add(main, "other", o)
	if err == nil || strings.Contains(err.Error(), secret) || !strings.HasPrefix(err.Error(), "git fetch origin: ") {
		t.Errorf("fetch of a remote with a secret in its path: %v", err)
	}
	for _, line := range trace {
		if strings.Contains(line, secret) {
			t.Errorf("trace line %q contains the secret", line)
		}
	}
}

// TestAGitErrorHidesAURLsPassword is a git error that quotes a URL with a password in its
// user part: the error prints the password as (hidden).
func TestAGitErrorHidesAURLsPassword(t *testing.T) {
	hermetic(t)
	main := repository(t)
	_, err := runner{main: main}.git(main, "branch", "-D", "https://u:token-123@git.example/h.git")
	if err == nil || strings.Contains(err.Error(), "token-123") || !strings.Contains(err.Error(), "https://u:(hidden)@git.example/h.git") {
		t.Errorf("git error %v, want the password as (hidden)", err)
	}
}
