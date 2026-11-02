package preparation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/branchformat"
)

// onBranch fetches acme/shop, and acme/lib when list has it, and checks out the branch,
// from a first commit of each repository; it returns the working folder and the branch.
func onBranch(t *testing.T, list string) (string, *WorkBranch) {
	t.Helper()
	needGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	base := t.TempDir()
	for _, path := range strings.Split(list, ",") {
		remote(t, base, path, "main", nil)
	}
	dir := emptyFolder(t)
	version := run(t, filepath.Join(base, "acme", "shop.git"), "rev-parse", "main")
	b := fetchBranchAt(t, dir, list, version, nil)
	if _, err := switchTo(dir, b); err != nil {
		t.Fatal(err)
	}
	for _, r := range b.Repositories {
		bare := filepath.Join(base, filepath.FromSlash(r.Path)+".git")
		run(t, bare, "config", "http.receivepack", "true")
	}
	return dir, b
}

// pushOf is [Push] of b in dir, with the message "Changes left", which returns the lines
// it printed too.
func pushOf(dir string, b *WorkBranch) (*branchformat.Branch, string, error) {
	var report strings.Builder
	doc, err := Push(context.Background(), dir, os.Environ(), b, "Changes left", testProxy, &report)
	return doc, report.String(), err
}

// TestAfterTheRunTheCheckTakesWhatTheFetchWrote is a repository qory fetched: the check
// of a fetch stops on its remote, and the check after the run takes it.
func TestAfterTheRunTheCheckTakesWhatTheFetchWrote(t *testing.T) {
	dir, _ := onBranch(t, "acme/shop,acme/lib")
	ctx := context.Background()
	if hold, err := safeRepository(ctx, dir); err == nil || err.Error() != textHasRemote {
		hold.Release()
		t.Errorf("the check of a fetch: %v", err)
	}
	hold, err := safeAfterRun(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	hold.Release()
	if err := safeFurther(ctx, filepath.Join(dir, filepath.FromSlash(Repositories), "acme", "lib")); err != nil {
		t.Errorf("the check of a further repository: %v", err)
	}
}

// TestAFurtherRepositoryIsCheckedBeforeAnyCommit is a key in a further repository's
// configuration: the target's change is not committed, and nothing is pushed.
func TestAFurtherRepositoryIsCheckedBeforeAnyCommit(t *testing.T) {
	dir, b := onBranch(t, "acme/shop,acme/lib")
	head := run(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "left.txt"), "left\n")
	lib := filepath.Join(dir, filepath.FromSlash(b.Repositories[1].Dir))
	run(t, lib, "config", "core.sshCommand", "/bin/false")
	_, _, err := pushOf(dir, b)
	if err == nil || !strings.Contains(err.Error(), "the check of repository acme/lib failed") || !strings.Contains(err.Error(), "core.sshcommand") {
		t.Fatalf("Push: %v", err)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
}

// TestAStopHasACauseFitForAfterTheRun is each stop of the check: its line for a run that
// has not started tells the reader to start in a new tree, and the line after the agent
// exited does not.
func TestAStopHasACauseFitForAfterTheRun(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	write(t, filepath.Join(dir, ".git", "commondir"), "/elsewhere\n")
	_, _, err := pushOf(dir, b)
	if err == nil || strings.Contains(err.Error(), "start the run in a new") {
		t.Fatalf("Push: %v", err)
	}
	if !strings.Contains(err.Error(), "its .git has a commondir file") {
		t.Errorf("Push: %v", err)
	}
}

// TestACommitHasEveryPathByItsName is an agent's paths that a pathspec would read as
// magic or as a wildcard, a path with a space and a line break, a removed file, and a
// repository cloned inside the working folder, which git adds as a link to a commit.
func TestACommitHasEveryPathByItsName(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	for _, name := range []string{":(magic).txt", "a*.txt", "with space\nand a break.txt"} {
		write(t, filepath.Join(dir, name), "left\n")
	}
	write(t, filepath.Join(dir, "another.txt"), "stays out, since a*.txt is a name\n")
	if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(dir, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, inner, "init", "--quiet", "-b", "main")
	write(t, filepath.Join(inner, "file.txt"), "inner\n")
	run(t, inner, "add", "-A")
	run(t, inner, "commit", "--quiet", "-m", "inner")

	doc, _, err := pushOf(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(run(t, dir, "-c", "core.quotePath=false", "diff-tree", "-r", "-z", "--name-only", "--no-commit-id", "HEAD"), "\x00")
	got = slices.DeleteFunc(got, func(s string) bool { return s == "" })
	want := []string{":(magic).txt", "README.md", "a*.txt", "another.txt", "inner", "with space\nand a break.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("the commit has %q, want %q", got, want)
	}
	if doc == nil || doc.Repositories[0].Commit != run(t, dir, "rev-parse", "HEAD") {
		t.Errorf("the document is %+v", doc)
	}
}

// TestNoChangeAfterGitAddsCommitsNothing is a file that git status lists and that equals
// HEAD once added, as a removed CRLF does under text=auto: no commit, no push.
func TestNoChangeAfterGitAddsCommitsNothing(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	head := run(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, ".git", "info", "attributes"), "README.md text eol=lf\n")
	content := contentOf(t, filepath.Join(dir, "README.md"))
	write(t, filepath.Join(dir, "README.md"), strings.ReplaceAll(content, "\n", "\r\n"))
	doc, lines, err := pushOf(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	if doc != nil || run(t, dir, "rev-parse", "HEAD") != head {
		t.Errorf("the document is %+v, and HEAD is %s", doc, run(t, dir, "rev-parse", "HEAD"))
	}
	if strings.TrimSpace(lines) != textNoNewCommits {
		t.Errorf("the lines are %q", lines)
	}
}

// TestWithCommit replaces the entry of a repository, in any letter case on github.com,
// and adds one for a repository that has none.
func TestWithCommit(t *testing.T) {
	entry := func(system, path, commit string) branchformat.Repository {
		return branchformat.Repository{System: system, Path: path, Commit: commit}
	}
	version := []branchformat.Repository{entry("github.com", "Acme/Shop", "a"), entry("github.com", "acme/lib", "b")}
	got := withCommit(slices.Clone(version), "github.com", "acme/shop", "c")
	if want := []branchformat.Repository{entry("github.com", "Acme/Shop", "c"), entry("github.com", "acme/lib", "b")}; !slices.Equal(got, want) {
		t.Errorf("replaced: %+v, want %+v", got, want)
	}
	got = withCommit(slices.Clone(version), "git.example.com", "Acme/Shop", "c")
	if len(got) != 3 || got[2] != entry("git.example.com", "Acme/Shop", "c") {
		t.Errorf("added: %+v", got)
	}
}

// TestPushCause is the line of a failed push that names the failure: the ref's, else the
// first error, else the first line.
func TestPushCause(t *testing.T) {
	for output, want := range map[string]string{
		"To https://git.example.com/acme/shop.git\n ! [rejected]  abc -> qory/wi_1 (fetch first)\nerror: failed to push some refs\n": "! [rejected]  abc -> qory/wi_1 (fetch first)",
		"remote: nothing\nerror: RPC failed; HTTP 403 curl 22\nfatal: the remote end hung up\n":                                      "error: RPC failed; HTTP 403 curl 22",
		"the only line\n": "the only line",
	} {
		if got := pushCause(output); got != want {
			t.Errorf("pushCause(%q) = %q, want %q", output, got, want)
		}
	}
}

// TestPushFailed is the line of each failure, by what git wrote, with no password of a
// URL in it.
func TestPushFailed(t *testing.T) {
	b := &WorkBranch{Ref: testRef, system: "git.example.com"}
	for output, want := range map[string]string{
		" ! [rejected] a -> b (non-fast-forward)":           "qory run: add a new version of the artifact branch",
		" ! [remote rejected] a -> b (protected)":           "qory run: change the rule on git.example.com",
		"error: RPC failed; HTTP 403 curl 22":               "qory run: add /acme/shop.git/git-receive-pack to the path rules",
		"fatal: unable to access 'x': returned error: 403":  "qory run: add /acme/shop.git/git-receive-pack to the path rules",
		"fatal: Authentication failed for 'https://x/'":     "qory run: give the run's credential for git.example.com write access to acme/shop",
		"fatal: unable to access 'x': Could not resolve":    "qory run: check that the run's policy allows git.example.com",
		"fatal: https://u:token-123@git.example.com/ fails": "qory run: check that the run's policy allows git.example.com",
	} {
		got := pushFailed(b, "acme/shop", &gitError{line: output, exit: 1, output: output}).Error()
		if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "; the code is used") {
			t.Errorf("pushFailed(%q) = %q, want it to start with %q", output, got, want)
		}
		if strings.Contains(got, "token-123") {
			t.Errorf("pushFailed(%q) shows the password: %q", output, got)
		}
	}
	if got := pushFailed(b, "acme/shop", errors.New("git is gone")).Error(); !strings.Contains(got, "git is gone") {
		t.Errorf("pushFailed: %q", got)
	}
}

// recordSetup is [RecordSetup] of b in dir.
func recordSetup(t *testing.T, dir string, b *WorkBranch) {
	t.Helper()
	if err := RecordSetup(context.Background(), dir, os.Environ(), b); err != nil {
		t.Fatal(err)
	}
}

// commitPaths is each path of the commit at HEAD.
func commitPaths(t *testing.T, dir string) []string {
	t.Helper()
	got := strings.Fields(run(t, dir, "diff-tree", "-r", "--name-only", "--no-commit-id", "HEAD"))
	slices.Sort(got)
	return got
}

// setupLeaves is a setup that changes README.md, adds lock.txt, removes first.txt and
// adds the link latest.
func setupLeaves(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, "README.md"), "the setup\n")
	write(t, filepath.Join(dir, "lock.txt"), "the setup\n")
	if err := os.Remove(filepath.Join(dir, "first.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(dir, "latest")); err != nil {
		t.Fatal(err)
	}
}

// TestWhatTheSetupLeftIsNotCommitted is a setup that changed a file, added one, removed
// one and added a link, and an agent that changed nothing: no commit, no push, no version.
func TestWhatTheSetupLeftIsNotCommitted(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	head := run(t, dir, "rev-parse", "HEAD")
	setupLeaves(t, dir)
	recordSetup(t, dir, b)
	doc, lines, err := pushOf(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	if doc != nil || run(t, dir, "rev-parse", "HEAD") != head {
		t.Errorf("the document is %+v, and HEAD is %s", doc, run(t, dir, "rev-parse", "HEAD"))
	}
	if strings.TrimSpace(lines) != textNoNewCommits {
		t.Errorf("the lines are %q", lines)
	}
}

// TestAPathTheAgentChangedAgainIsCommitted is each kind of path the setup left, which the
// agent changed again, beside the ones it did not touch.
func TestAPathTheAgentChangedAgainIsCommitted(t *testing.T) {
	cases := map[string]struct {
		agent func(t *testing.T, dir string)
		want  []string
	}{
		"a file": {func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "README.md"), "the agent\n")
		}, []string{"README.md"}},
		"a file with the bit that lets it run": {func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, "README.md"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, []string{"README.md"}},
		"a removed file": {func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "first.txt"), "the agent\n")
		}, []string{"first.txt"}},
		"a file the setup added, removed": {func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
				t.Fatal(err)
			}
		}, []string{"README.md"}},
		"a link": {func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "latest")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("lock.txt", filepath.Join(dir, "latest")); err != nil {
				t.Fatal(err)
			}
		}, []string{"latest"}},
		"a new file": {func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "new.txt"), "the agent\n")
		}, []string{"new.txt"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir, b := onBranch(t, "acme/shop")
			setupLeaves(t, dir)
			recordSetup(t, dir, b)
			c.agent(t, dir)
			doc, _, err := pushOf(dir, b)
			if err != nil {
				t.Fatal(err)
			}
			if doc == nil {
				t.Fatal("the run pushed nothing")
			}
			if got := commitPaths(t, dir); !slices.Equal(got, c.want) {
				t.Errorf("the commit has %q, want %q", got, c.want)
			}
		})
	}
}

// TestAFileTheSetupRestoredIsNoChange is a path the setup left changed that the agent
// put back as HEAD has it: git status no longer lists it, so no commit.
func TestAFileTheSetupRestoredIsNoChange(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	content := contentOf(t, filepath.Join(dir, "README.md"))
	write(t, filepath.Join(dir, "README.md"), "the setup\n")
	recordSetup(t, dir, b)
	write(t, filepath.Join(dir, "README.md"), content)
	if doc, _, err := pushOf(dir, b); err != nil || doc != nil {
		t.Errorf("the document is %+v, the error %v", doc, err)
	}
}

// TestASetupThatAddsAFilterStopsBeforeGitStatus is a setup that added a clean filter,
// which git status would run on this machine: the check comes first and the filter does
// not run.
func TestASetupThatAddsAFilterStopsBeforeGitStatus(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	marker := filepath.Join(t.TempDir(), "filter-ran")
	run(t, dir, "config", "filter.probe.clean", "touch "+marker+"; cat")
	write(t, filepath.Join(dir, ".git", "info", "attributes"), "* filter=probe\n")
	write(t, filepath.Join(dir, "README.md"), "the setup\n")
	err := RecordSetup(context.Background(), dir, os.Environ(), b)
	if err == nil || !strings.Contains(err.Error(), "after the setup, the check of repository acme/shop failed, so the agent did not start: ") {
		t.Fatalf("the error is %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the filter ran")
	}
}

// TestARepositoryInARepositoryIsOneEntry is a repository the setup cloned in the working
// folder, which git status lists as one path: it stays out until a file in it changes.
func TestARepositoryInARepositoryIsOneEntry(t *testing.T) {
	for name, changed := range map[string]bool{"as the setup left it": false, "changed": true} {
		t.Run(name, func(t *testing.T) {
			dir, b := onBranch(t, "acme/shop")
			inner := filepath.Join(dir, "inner")
			if err := os.MkdirAll(inner, 0o755); err != nil {
				t.Fatal(err)
			}
			run(t, inner, "init", "--quiet", "-b", "main")
			write(t, filepath.Join(inner, "file.txt"), "inner\n")
			run(t, inner, "add", "-A")
			run(t, inner, "commit", "--quiet", "-m", "inner")
			recordSetup(t, dir, b)
			if changed {
				write(t, filepath.Join(inner, "file.txt"), "the agent\n")
				run(t, inner, "commit", "--quiet", "-a", "-m", "the agent")
			}
			doc, _, err := pushOf(dir, b)
			if err != nil {
				t.Fatal(err)
			}
			if (doc != nil) != changed {
				t.Errorf("the document is %+v", doc)
			}
		})
	}
}

// TestAFailedPushPrintsTheForgeAndTheCommits is a push that failed: each remote: line and
// each commit, with no login, and a count of what the limit left out.
func TestAFailedPushPrintsTheForgeAndTheCommits(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	r := b.Repositories[0]
	write(t, filepath.Join(dir, "left.txt"), "left\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "--quiet", "-m", "see https://bot:token-123@git.example.com/x")
	head := run(t, dir, "rev-parse", "HEAD")
	var output, report strings.Builder
	output.WriteString("To https://git.example.com/acme/shop.git\n")
	output.WriteString(strings.Repeat("remote: line\n", maxFailedPushLines+3))
	output.WriteString("remote:\nremote: \n ! [remote rejected] a -> b (hook declined)\n")
	printFailedPush(context.Background(), hostRunner(&testProxy), dir, r, head, output.String(), &report)
	got := report.String()
	if n := strings.Count(got, "qory run: remote: line\n"); n != maxFailedPushLines {
		t.Errorf("%d lines of the forge, want %d:\n%s", n, maxFailedPushLines, got)
	}
	for _, want := range []string{"qory run: 3 further lines left out\n", "qory run: not pushed from acme/shop: " + head[:12] + " see https://"} {
		if !strings.Contains(got, want) {
			t.Errorf("the lines have no %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "token-123") {
		t.Errorf("the lines show the password:\n%s", got)
	}
}
