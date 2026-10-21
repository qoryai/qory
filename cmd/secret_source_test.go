package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// secretSourceCheckout is [newCheckout] with the rest of the environment a test of a
// module source sets: the data directory, git's search and no Claude directory.
func secretSourceCheckout(t *testing.T) string {
	t.Helper()
	root := newCheckout(t)
	isolateTheRest(t, root)
	return root
}

// isolateTheRest sets the data directory, git's search above the checkout at root and
// no Claude directory, the part of the environment [newCheckout] does not set.
func isolateTheRest(t *testing.T, root string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", filepath.Join(os.Getenv("HOME"), ".local", "share"))
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
}

// printsNoSecret fails the test when the error or the output contains an access key secret.
func printsNoSecret(t *testing.T, out string, err error) {
	t.Helper()
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "qak_") || strings.Contains(strings.ToLower(out), "qak_") {
		t.Errorf("the refusal contains the secret: %v\n%s", err, out)
	}
}

// TestASourceFailureWithASecretKeepsItsExitStatus is a module listed by a name that
// contains an access key secret, whose modules directory qory cannot read, and a git
// source whose URL contains one and cannot be fetched: compose exits 1, as for any
// source that fails, and prints no secret.
func TestASourceFailureWithASecretKeepsItsExitStatus(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory of mode 000")
		}
		root := secretSourceCheckout(t)
		writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: any\nmodules:\n  - name: qak_m\n")
		modules := filepath.Join(root, "modules")
		writeManifest(t, filepath.Join(modules, "qak_m"), "qak_m")
		if err := os.Chmod(modules, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(modules, 0o755) })
		out, err := run(t, "hc")
		if err == nil || cmd.ExitCode(err) != 1 {
			t.Errorf("err = %v, exit %d, want exit 1", err, cmd.ExitCode(err))
		}
		printsNoSecret(t, out, err)
	})
	t.Run("fetch", func(t *testing.T) {
		root := secretSourceCheckout(t)
		writeOwnStack(t, root, "apiVersion: qory.dev/v1alpha1\ntarget:\n  runtime: any\nmodules:\n  - name: core\n    source: {git: file:///nonexistent/qak_repo.git, ref: main}\n")
		out, err := run(t, "hc")
		if err == nil || cmd.ExitCode(err) != 1 {
			t.Errorf("err = %v, exit %d, want exit 1", err, cmd.ExitCode(err))
		}
		printsNoSecret(t, out, err)
	})
}

// TestAGoneBranchWithASecretInItsURLIsAnInputError is a module at main of a remote
// whose path contains an access key secret, and whose main branch is deleted: compose
// exits 2 with the gone message alone, as for any branch that is gone, and prints no
// secret.
func TestAGoneBranchWithASecretInItsURLIsAnInputError(t *testing.T) {
	root := secretSourceCheckout(t)
	remote := filepath.Join(tempDir(t), "qak_repo")
	runGit(t, filepath.Dir(remote), "init", "--quiet", "--initial-branch=main", remote)
	runGit(t, remote, "config", "user.name", "Tester")
	runGit(t, remote, "config", "user.email", "tester@example.com")
	writeManifest(t, remote, "core")
	runGit(t, remote, "add", "-A")
	runGit(t, remote, "commit", "-q", "-m", "first")
	writeOwnStack(t, root, onMain("file://"+remote))
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	runGit(t, remote, "checkout", "-q", "-b", "other")
	runGit(t, remote, "branch", "-q", "-D", "main")
	out, err := run(t, "hc")
	wantsGone(t, err)
	printsNoSecret(t, out, err)
}

// TestAGoneBaseBranchWithASecretInItsURLIsAnInputError is a stack that extends a base at
// main of a remote whose path contains an access key secret, and whose main branch is
// deleted: compose exits 2 with the gone message alone, and prints no secret.
func TestAGoneBaseBranchWithASecretInItsURLIsAnInputError(t *testing.T) {
	url := baseRepo(t)
	remote := filepath.Join(filepath.Dir(strings.TrimPrefix(url, "file://")), "qak_base")
	if err := os.Rename(strings.TrimPrefix(url, "file://"), remote); err != nil {
		t.Fatal(err)
	}
	root := consumerCheckout(t, "file://"+remote)
	isolateTheRest(t, root)
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	runGit(t, remote, "checkout", "-q", "-b", "other")
	runGit(t, remote, "branch", "-q", "-D", "main")
	out, err := run(t, "hc")
	wantsGone(t, err)
	printsNoSecret(t, out, err)
}

// wantsGone fails the test unless err is the gone message of main alone, with no module
// or extends before it, the URL with the secret printed as Forager's value phrase, and
// exits 2.
func wantsGone(t *testing.T, err error) {
	t.Helper()
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput || !strings.HasPrefix(err.Error(), "(a value that contains an access key secret) has no branch or tag main; ") {
		t.Errorf("err = %v, exit %d, want the gone message alone, exit %d", err, cmd.ExitCode(err), cmd.ExitInput)
	}
}
