package suite_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/suite"
)

// repo makes a repository with three commits and returns its folder and the commit ids. HEAD is
// the second commit, and the third is on no branch.
func repo(t *testing.T) (dir, first, second, third string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(home))
	dir = filepath.Join(home, "src")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	write(t, filepath.Join(dir, "a.txt"), "one\n")
	git("add", ".")
	git("commit", "--quiet", "-m", "one")
	first = git("rev-parse", "HEAD")
	write(t, filepath.Join(dir, "a.txt"), "two\n")
	write(t, filepath.Join(dir, "b/c.txt"), "c\n")
	git("add", ".")
	git("commit", "--quiet", "-m", "two")
	second = git("rev-parse", "HEAD")
	// A third commit that no branch has: the checkout is back on the second.
	git("checkout", "--quiet", "--detach")
	write(t, filepath.Join(dir, "a.txt"), "three\n")
	git("commit", "--quiet", "-am", "three")
	third = git("rev-parse", "HEAD")
	git("checkout", "--quiet", second)
	return dir, first, second, third
}

func TestCloneWritesTheFilesOfTheCommitAndLaterWritesToTheSourceChangeNothing(t *testing.T) {
	src, first, second, _ := repo(t)
	dest := filepath.Join(t.TempDir(), "clone")
	if err := suite.Clone(context.Background(), src, first, dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dest, "a.txt"))
	if string(got) != "one\n" {
		t.Errorf("a.txt = %q, want the first commit's", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "b")); err == nil {
		t.Error("the clone has a file of the second commit")
	}
	write(t, filepath.Join(src, "a.txt"), "agent wrote this\n")
	if err := os.RemoveAll(filepath.Join(src, ".git")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(got) != "one\n" {
		t.Errorf("a.txt = %q after the source changed", got)
	}
	again := filepath.Join(t.TempDir(), "again")
	if err := suite.Clone(context.Background(), src, second, again); err == nil {
		t.Error("a clone of a source with no repository succeeded")
	}
}

func TestCloneTakesTheCommitRequestedAndNotTheOneHeadHas(t *testing.T) {
	src, _, _, third := repo(t)
	dest := filepath.Join(t.TempDir(), "clone")
	if err := suite.Clone(context.Background(), src, third, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(got) != "three\n" {
		t.Errorf("a.txt = %q, want the third commit's", got)
	}
}

func TestCloneRunsNoHookOfTheSource(t *testing.T) {
	src, _, second, _ := repo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hook := "#!/bin/sh\ntouch " + marker + "\n"
	for _, name := range []string{"post-checkout", "post-merge", "reference-transaction", "pre-auto-gc"} {
		write(t, filepath.Join(src, ".git", "hooks", name), hook)
		if err := os.Chmod(filepath.Join(src, ".git", "hooks", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := "[core]\n\thooksPath = " + filepath.Join(src, ".git", "hooks") + "\n\tfsmonitor = " + marker + "\n[uploadpack]\n\tpackObjectsHook = " + marker + "\n"
	f, err := os.OpenFile(filepath.Join(src, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(cfg); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := suite.Clone(context.Background(), src, second, filepath.Join(t.TempDir(), "clone")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a hook or a program of the source's configuration ran")
	}
}

func TestCloneRefusesWhatIsNoCommitOfTheSourceAndLeavesNoFolder(t *testing.T) {
	src, _, second, _ := repo(t)
	// Git reads the upper-case form of a commit id as the commit: qory takes the lower-case form
	// alone, so that one commit has one suite.
	for _, commit := range []string{"HEAD", "main", "abc", strings.Repeat("0", 40), strings.ToUpper(second), "--upload-pack=x"} {
		dest := filepath.Join(t.TempDir(), "clone")
		if err := suite.Clone(context.Background(), src, commit, dest); err == nil {
			t.Errorf("Clone of %q succeeded", commit)
		}
		if _, err := os.Stat(dest); err == nil {
			t.Errorf("Clone of %q left its folder", commit)
		}
	}
}

func TestValidCommit(t *testing.T) {
	for commit, want := range map[string]bool{
		strings.Repeat("a", 40): true, strings.Repeat("0", 64): true, strings.Repeat("a", 39): false,
		strings.Repeat("A", 40): false, "HEAD": false, "": false, strings.Repeat("g", 40): false,
	} {
		if got := suite.ValidCommit(commit); got != want {
			t.Errorf("ValidCommit(%q) = %v, want %v", commit, got, want)
		}
	}
}
