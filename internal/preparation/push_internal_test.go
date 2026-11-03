package preparation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/branchformat"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/proxygit"
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
	return pushWith(dir, b, true)
}

// pushWith is [pushOf] with agentStopped set.
func pushWith(dir string, b *WorkBranch, agentStopped bool) (*branchformat.Branch, string, error) {
	var report strings.Builder
	doc, err := Push(context.Background(), dir, os.Environ(), b, "Changes left", testProxy, agentStopped, &report)
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
	if err := safeFurther(ctx, dir, filepath.Join(dir, filepath.FromSlash(Repositories), "acme", "lib"), true); err != nil {
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
// repository cloned inside the working folder, which stays out.
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
	want := []string{":(magic).txt", "README.md", "a*.txt", "another.txt", "with space\nand a break.txt"}
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

// laterFilter writes into the repository at dir what a process in the setup container can
// write after the check: a clean filter, a fake program in a temporary folder that creates
// the file marker, and attributes that apply it. It also moves the time of a tracked file
// with its size unchanged, so git status reads that file.
func laterFilter(t *testing.T, dir, marker string) {
	t.Helper()
	program := filepath.Join(t.TempDir(), "filter")
	write(t, program, "#!/bin/sh\n: > '"+marker+"'\ncat\n")
	if err := os.Chmod(program, 0o755); err != nil {
		t.Fatal(err)
	}
	config, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer config.Close()
	if _, err := fmt.Fprintf(config, "[filter \"probe\"]\n\tclean = %s\n", program); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, ".git", "info", "attributes"), "* filter=probe\n")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "README.md"), later, later); err != nil {
		t.Fatal(err)
	}
}

// TestAFilterWrittenAfterTheCheckRunsNoProgram is a process that adds a filter between the
// check and git status, so each run of the test has the order that a concurrent write
// reaches only at times. git status reads the copy, so the filter does not run, and the
// record is as it is with no filter.
func TestAFilterWrittenAfterTheCheckRunsNoProgram(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	marker := filepath.Join(t.TempDir(), "filter-ran")
	write(t, filepath.Join(dir, "lock.txt"), "the setup\n")
	ran := false
	status := func(env ...string) gitRunner {
		return func(ctx context.Context, at string, args ...string) (string, error) {
			laterFilter(t, dir, marker)
			ran = true
			return stepGit(noRemote, "", env...)(ctx, at, args...)
		}
	}
	if err := recordWith(context.Background(), dir, os.Environ(), b, status); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("git status did not run")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the filter ran")
	}
	if got := slices.Sorted(maps.Keys(b.setupChanges["."])); !slices.Equal(got, []string{"lock.txt"}) {
		t.Errorf("the record has %q, want lock.txt", got)
	}
}

// TestThePrivateGitDirHasNoKeyOfALaterWrite is the folder git status reads: a later write
// to .git changes nothing in it, and git status with it has no filter to run.
func TestThePrivateGitDirHasNoKeyOfALaterWrite(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	r := b.Repositories[0]
	write(t, filepath.Join(dir, ".git", "info", "exclude"), "ignored.txt\n")
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	if err := privateGitDir(hostgit.WithBase(context.Background(), proxygit.Env(os.Environ(), nil)), dir, dir, private, r.Commit); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "filter-ran")
	laterFilter(t, dir, marker)
	config, err := os.ReadFile(filepath.Join(private, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "filter") {
		t.Errorf("the private config has a key of the later write:\n%s", config)
	}
	if _, err := os.Lstat(filepath.Join(private, "info", "attributes")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the private folder has attributes: %v", err)
	}
	for name, want := range map[string]string{"HEAD": r.Commit + "\n", "index": string(index), "info/exclude": "ignored.txt\n"} {
		if got, err := os.ReadFile(filepath.Join(private, filepath.FromSlash(name))); err != nil || string(got) != want {
			t.Errorf("%s of the private folder differs from what was read: %v", name, err)
		}
	}
	env := []string{"GIT_DIR=" + private, "GIT_WORK_TREE=" + dir, "GIT_OBJECT_DIRECTORY=" + filepath.Join(dir, ".git", "objects")}
	if _, err := changedPaths(hostgit.WithBase(context.Background(), proxygit.Env(os.Environ(), nil)), stepGit(noRemote, "", env...), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the filter ran")
	}
}

// TestThePrivateIndexHasTheTimeOfTheIndex is an index with a known past time: the copy has
// that time, so git status of the copy reads a file of the same size as the index entry as
// git status of the repository does.
func TestThePrivateIndexHasTheTimeOfTheIndex(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, ".git", "index"), past, past); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	ctx := hostgit.WithBase(context.Background(), proxygit.Env(os.Environ(), nil))
	if err := privateGitDir(ctx, dir, dir, private, b.Repositories[0].Commit); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(private, "index"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(past) {
		t.Errorf("the copy of the index has the time %s, want %s", info.ModTime(), past)
	}
}

// TestTheCheckReadsThePrivateCopy is the git the check runs, seen through a program first on
// the PATH that writes its arguments to a file and runs git: git config --file reads the
// copy that git status reads, and never the live .git/config, which a process can change
// between the two reads.
func TestTheCheckReadsThePrivateCopy(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, private := t.TempDir(), t.TempDir()
	calls := filepath.Join(bin, "calls")
	write(t, filepath.Join(bin, "git"), "#!/bin/sh\necho \"$*\" >> '"+calls+"'\nexec '"+git+"' \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	ctx := hostgit.WithBase(context.Background(), proxygit.Env(os.Environ(), nil))
	if err := privateGitDir(ctx, dir, dir, private, b.Repositories[0].Commit); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "--file "+filepath.Join(private, "config")) {
		t.Errorf("no check of the private copy in:\n%s", got)
	}
	if strings.Contains(string(got), filepath.Join(dir, ".git", "config")) {
		t.Errorf("a check of the live file in:\n%s", got)
	}
}

// TestTheRecordListsWhatGitStatusListsInTheRepository is a plain setup that changes, adds,
// removes, stages and ignores: the private folder gives the same paths as git status in
// the repository itself.
func TestTheRecordListsWhatGitStatusListsInTheRepository(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	setupLeaves(t, dir)
	write(t, filepath.Join(dir, ".git", "info", "exclude"), "ignored.txt\n")
	write(t, filepath.Join(dir, "ignored.txt"), "the setup\n")
	write(t, filepath.Join(dir, "staged.txt"), "the setup\n")
	run(t, dir, "add", "staged.txt")
	want, err := changedPaths(hostgit.WithBase(context.Background(), proxygit.Env(os.Environ(), nil)), stepGit(noRemote, ""), dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(want)
	recordSetup(t, dir, b)
	got := slices.Sorted(maps.Keys(b.setupChanges["."]))
	if len(got) == 0 || !slices.Equal(got, want) {
		t.Errorf("the record has %q, git status lists %q", got, want)
	}
}

// TestARepositoryInARepositoryStaysOut is a repository the setup or the agent left in the
// working folder, with and without a commit: it stays out of the commit. A line shows it
// unless it is as the setup left it, and a change beside it is pushed.
func TestARepositoryInARepositoryStaysOut(t *testing.T) {
	for name, c := range map[string]struct{ commits, setup, changed, line bool }{
		"the agent's, with a commit":  {commits: true, line: true},
		"the agent's, with no commit": {line: true},
		"the setup's, as it left it":  {commits: true, setup: true},
		"the setup's, changed":        {commits: true, setup: true, changed: true, line: true},
	} {
		t.Run(name, func(t *testing.T) {
			dir, b := onBranch(t, "acme/shop")
			inner := filepath.Join(dir, "a", "inner")
			if err := os.MkdirAll(inner, 0o755); err != nil {
				t.Fatal(err)
			}
			run(t, inner, "init", "--quiet", "-b", "main")
			write(t, filepath.Join(inner, "file.txt"), "inner\n")
			if c.commits {
				run(t, inner, "add", "-A")
				run(t, inner, "commit", "--quiet", "-m", "inner")
			}
			if c.setup {
				recordSetup(t, dir, b)
			}
			if c.changed {
				write(t, filepath.Join(inner, "file.txt"), "the agent\n")
				run(t, inner, "commit", "--quiet", "-a", "-m", "the agent")
			}
			write(t, filepath.Join(dir, "left.txt"), "left\n")
			doc, lines, err := pushOf(dir, b)
			if err != nil {
				t.Fatal(err)
			}
			if doc == nil {
				t.Fatal("the change beside the repository was not pushed")
			}
			if got := commitPaths(t, dir); !slices.Equal(got, []string{"left.txt"}) {
				t.Errorf("the commit has %q", got)
			}
			want := "a/inner of acme/shop to push them: that folder is a repository"
			if got := strings.Contains(lines, want); got != c.line {
				t.Errorf("the line is there: %v, want %v\n%s", got, c.line, lines)
			}
		})
	}
}

// TestAGitlinkWithARepositoryPrintsTheLine is a gitlink in the commit, with a repository in
// its folder: git status does not list it, and the line shows it.
func TestAGitlinkWithARepositoryPrintsTheLine(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, sub, "init", "--quiet", "-b", "main")
	write(t, filepath.Join(sub, "f"), "f\n")
	run(t, sub, "add", "-A")
	run(t, sub, "commit", "--quiet", "-m", "sub")
	run(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+run(t, sub, "rev-parse", "HEAD")+",sub")
	run(t, dir, "commit", "--quiet", "-m", "a gitlink")
	_, lines, err := pushOf(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := "in sub of acme/shop"; !strings.Contains(lines, "sub of acme/shop to push them") {
		t.Errorf("the lines are %q, want %q", lines, want)
	}
}

// TestAStagedFileTheAgentRemovedStaysOut is a path staged and then removed, and an ignored
// file force-added: neither is a path to add once the index is back at HEAD.
func TestAStagedFileTheAgentRemovedStaysOut(t *testing.T) {
	for name, agent := range map[string]func(t *testing.T, dir string){
		"staged then removed": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "tmp.txt"), "tmp\n")
			run(t, dir, "add", "tmp.txt")
			if err := os.Remove(filepath.Join(dir, "tmp.txt")); err != nil {
				t.Fatal(err)
			}
		},
		"an ignored file force-added": func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, ".git", "info", "exclude"), "*.log\n")
			write(t, filepath.Join(dir, "a.log"), "log\n")
			run(t, dir, "add", "-f", "a.log")
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, b := onBranch(t, "acme/shop")
			agent(t, dir)
			write(t, filepath.Join(dir, "work.txt"), "work\n")
			if _, _, err := pushOf(dir, b); err != nil {
				t.Fatal(err)
			}
			if got := commitPaths(t, dir); !slices.Equal(got, []string{"work.txt"}) {
				t.Errorf("the commit has %q", got)
			}
		})
	}
}

// TestACommitOfTheAgentWithQoryIsUndone is an agent that committed a file in .qory: the
// run's commit puts .qory back, and the agent's other file stays in the branch.
func TestACommitOfTheAgentWithQoryIsUndone(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	before := run(t, dir, "ls-tree", "HEAD", "--", ".qory")
	write(t, filepath.Join(dir, ".qory", "mine.txt"), "mine\n")
	write(t, filepath.Join(dir, "kept.txt"), "kept\n")
	run(t, dir, "add", "-f", ".qory/mine.txt", "kept.txt")
	run(t, dir, "commit", "--quiet", "-m", "the agent")
	if _, _, err := pushOf(dir, b); err != nil {
		t.Fatal(err)
	}
	if got := run(t, dir, "ls-tree", "HEAD", "--", ".qory"); got != before {
		t.Errorf(".qory on the branch is %q, want %q", got, before)
	}
	if got := run(t, dir, "show", "HEAD:kept.txt"); got != "kept" {
		t.Errorf("kept.txt is %q", got)
	}
	if got := run(t, dir, "log", "-1", "--format=%s"); got != "Changes left" {
		t.Errorf("the last commit is %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".qory", "mine.txt")); err != nil {
		t.Errorf("the file on disk is gone: %v", err)
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

// inEachRepository runs body for the target and for a further repository of a run of two:
// folder is the repository the case is about, and left a file there that the agent left.
func inEachRepository(t *testing.T, body func(t *testing.T, dir, folder string, b *WorkBranch, path string)) {
	t.Helper()
	for name, c := range map[string]struct{ repository, path string }{
		"target":  {"", "acme/shop"},
		"further": {filepath.Join(filepath.FromSlash(Repositories), "acme", "lib"), "acme/lib"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, b := onBranch(t, "acme/shop,acme/lib")
			folder := filepath.Join(dir, c.repository)
			write(t, filepath.Join(folder, "left.txt"), "left\n")
			body(t, dir, folder, b, c.path)
		})
	}
}

// committedLeft reports whether the repository at folder has a commit of the run on HEAD.
func committedLeft(t *testing.T, folder string) bool {
	t.Helper()
	return run(t, folder, "log", "-1", "--format=%s") == "Changes left"
}

// TestAStaleIndexLockDoesNotLoseTheWork is an agent that was killed in a git command, whose
// stop is proven: qory removes the lock, and the commit and the push go on.
func TestAStaleIndexLockDoesNotLoseTheWork(t *testing.T) {
	inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, path string) {
		write(t, indexLock(folder), "")
		_, lines, err := pushWith(dir, b, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(indexLock(folder)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the lock is still there: %v", err)
		}
		if !committedLeft(t, folder) {
			t.Error("the commit is missing")
		}
		want := "qory run: removed .git/index.lock of " + path + ", which the agent left when it stopped"
		if !strings.Contains(lines, want) {
			t.Errorf("the lines are %q, want %q", lines, want)
		}
	})
}

// TestAnIndexLockFailsTheStepWhileTheStopIsNotProven is a lock with an agent that may run:
// it stays, and nothing is committed in any repository.
func TestAnIndexLockFailsTheStepWhileTheStopIsNotProven(t *testing.T) {
	inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, path string) {
		write(t, indexLock(folder), "")
		_, _, err := pushWith(dir, b, false)
		want := "repository " + path + " has .git/index.lock and the stop of the agent is not proven"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("the error is %v, want %q", err, want)
		}
		if !strings.HasPrefix(err.Error(), "qory run: take the work item in again in Qory Apiary: ") {
			t.Errorf("the error starts with %q", err)
		}
		if _, err := os.Lstat(indexLock(folder)); err != nil {
			t.Errorf("the lock is gone: %v", err)
		}
		if committedLeft(t, folder) {
			t.Error("a commit was made")
		}
	})
}

// TestNoLockIsNeededWhileTheStopIsNotProven is a run with no lock: the step goes on.
func TestNoLockIsNeededWhileTheStopIsNotProven(t *testing.T) {
	inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, _ string) {
		if _, _, err := pushWith(dir, b, false); err != nil {
			t.Fatal(err)
		}
		if !committedLeft(t, folder) {
			t.Error("the commit is missing")
		}
	})
}

// TestAnIndexLockThatIsNoFileStaysAndFailsTheStep is a directory at the name of the lock.
func TestAnIndexLockThatIsNoFileStaysAndFailsTheStep(t *testing.T) {
	inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, path string) {
		if err := os.Mkdir(indexLock(folder), 0o755); err != nil {
			t.Fatal(err)
		}
		_, _, err := pushWith(dir, b, true)
		want := ".git/index.lock of repository " + path + " is not a regular file"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("the error is %v, want %q", err, want)
		}
		if info, err := os.Lstat(indexLock(folder)); err != nil || !info.IsDir() {
			t.Errorf("the directory is gone: %v", err)
		}
	})
}

// TestAnIndexLockThatIsALinkStaysAndFailsTheCheck is a link at the name of the lock, which
// the safety check stops before qory looks at the lock: the link and its target stay.
func TestAnIndexLockThatIsALinkStaysAndFailsTheCheck(t *testing.T) {
	inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, _ string) {
		target := filepath.Join(t.TempDir(), "kept")
		write(t, target, "kept\n")
		if err := os.Symlink(target, indexLock(folder)); err != nil {
			t.Fatal(err)
		}
		_, _, err := pushWith(dir, b, true)
		if err == nil || !strings.Contains(err.Error(), "the check of repository") {
			t.Fatalf("the error is %v, want the safety check's", err)
		}
		if info, err := os.Lstat(indexLock(folder)); err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("the link is gone: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("the link's target is gone: %v", err)
		}
	})
}

// TestAFailedCheckLeavesTheLock is a lock in one repository and a key in another that the
// safety check stops: qory removes nothing.
func TestAFailedCheckLeavesTheLock(t *testing.T) {
	dir, b := onBranch(t, "acme/shop,acme/lib")
	write(t, indexLock(dir), "")
	further := filepath.Join(dir, filepath.FromSlash(Repositories), "acme", "lib")
	run(t, further, "config", "filter.x.clean", "false")
	if _, _, err := pushWith(dir, b, true); err == nil || !strings.Contains(err.Error(), "the check of repository") {
		t.Fatalf("the error is %v, want the safety check's", err)
	}
	if _, err := os.Lstat(indexLock(dir)); err != nil {
		t.Errorf("the lock is gone: %v", err)
	}
}

// TestALaterLockThatFailsLeavesTheEarlierLock is a lock in the target and a directory at
// the name of the lock in a further repository: qory removes neither.
func TestALaterLockThatFailsLeavesTheEarlierLock(t *testing.T) {
	dir, b := onBranch(t, "acme/shop,acme/lib")
	write(t, indexLock(dir), "")
	further := filepath.Join(dir, filepath.FromSlash(Repositories), "acme", "lib")
	if err := os.Mkdir(indexLock(further), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pushWith(dir, b, true); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Lstat(indexLock(dir)); err != nil {
		t.Errorf("the target's lock is gone: %v", err)
	}
}

// TestNoOtherLockIsRemoved is a lock of another name, in each repository: it stays, and the
// commit that needs it fails.
func TestNoOtherLockIsRemoved(t *testing.T) {
	inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, _ string) {
		other := filepath.Join(folder, ".git", "HEAD.lock")
		write(t, other, "")
		_, _, err := pushWith(dir, b, true)
		if err == nil || !strings.Contains(err.Error(), "the commit of what the agent left") {
			t.Fatalf("the error is %v, want a failed commit", err)
		}
		if _, err := os.Lstat(other); err != nil {
			t.Errorf("the other lock is gone: %v", err)
		}
	})
}

// TestAGitlinkOfAnotherRepositoryRunsNoProgram is a repository in the working folder with a
// clean filter in its configuration, which no check reads, as a staged and as a committed
// gitlink, with .gitmodules that turn the ignoring of a dirty gitlink off.
func TestAGitlinkOfAnotherRepositoryRunsNoProgram(t *testing.T) {
	for name, c := range map[string]struct{ gitmodules, committed bool }{
		"staged":                 {},
		"committed":              {committed: true},
		"staged, .gitmodules":    {gitmodules: true},
		"committed, .gitmodules": {gitmodules: true, committed: true},
	} {
		t.Run(name, func(t *testing.T) {
			dir, b := onBranch(t, "acme/shop")
			canary := filepath.Join(t.TempDir(), "canary")
			sub := filepath.Join(dir, "sub")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			run(t, sub, "init", "--quiet", "-b", "main")
			write(t, filepath.Join(sub, "f"), "f\n")
			write(t, filepath.Join(sub, ".gitattributes"), "f filter=x\n")
			run(t, sub, "add", "-A")
			run(t, sub, "commit", "--quiet", "-m", "sub")
			program := filepath.Join(sub, ".git", "program.sh")
			write(t, program, "#!/bin/sh\nenv > "+canary+"\ncat\n")
			if err := os.Chmod(program, 0o755); err != nil {
				t.Fatal(err)
			}
			run(t, sub, "config", "filter.x.clean", ".git/program.sh")
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(filepath.Join(sub, "f"), later, later); err != nil {
				t.Fatal(err)
			}
			run(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+run(t, sub, "rev-parse", "HEAD")+",sub")
			if c.gitmodules {
				write(t, filepath.Join(dir, ".gitmodules"), "[submodule \"sub\"]\n\tpath = sub\n\turl = https://git.example.com/acme/sub.git\n\tignore = none\n")
				run(t, dir, "add", ".gitmodules")
			}
			if c.committed {
				run(t, dir, "commit", "--quiet", "-m", "sub")
			}
			pushOf(dir, b)
			if _, err := os.Stat(canary); err == nil {
				t.Error("a program in the repository in the working folder ran")
			}
		})
	}
}

// TestAGitWithoutPartsStopsBeforeAnyGit is a .git that git does not take for a repository,
// in a working folder inside another repository: the check stops, and nothing is
// committed in the repository above it.
func TestAGitWithoutPartsStopsBeforeAnyGit(t *testing.T) {
	for _, part := range []string{"HEAD", "objects", "refs"} {
		inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, _ string) {
			parent := filepath.Dir(dir)
			run(t, parent, "init", "--quiet", "-b", "main")
			write(t, filepath.Join(parent, "above.txt"), "above\n")
			run(t, parent, "add", "above.txt")
			run(t, parent, "commit", "--quiet", "-m", "above")
			before := run(t, parent, "rev-parse", "HEAD")
			if err := os.RemoveAll(filepath.Join(folder, ".git", part)); err != nil {
				t.Fatal(err)
			}
			_, _, err := pushOf(dir, b)
			if err == nil || !strings.Contains(err.Error(), "its .git has no "+part) {
				t.Fatalf("the error is %v", err)
			}
			if got := run(t, parent, "rev-parse", "HEAD"); got != before {
				t.Errorf("the repository above moved to %s", got)
			}
		})
	}
}

// TestAStepGitTakesNoFolderAbove is a git of the step in a folder with no repository, in a
// folder with one: git fails, and reads nothing above the folder.
func TestAStepGitTakesNoFolderAbove(t *testing.T) {
	dir, _ := onBranch(t, "acme/shop")
	inner := filepath.Join(dir, "inner")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := stepGit(noRemote, "")(context.Background(), inner, "rev-parse", "--git-dir"); err == nil {
		t.Errorf("git found a repository at %s", got)
	}
}

// TestALinkOnTheWayToAFurtherRepositoryStops is a further repository, or a folder above it,
// replaced by a link to a repository outside the working folder: nothing there is committed.
func TestALinkOnTheWayToAFurtherRepositoryStops(t *testing.T) {
	for name, up := range map[string]int{"the repository": 0, "the folder above it": 1, ".qory/repositories": 2} {
		t.Run(name, func(t *testing.T) {
			dir, b := onBranch(t, "acme/shop,acme/lib")
			lib := filepath.Join(dir, filepath.FromSlash(b.Repositories[1].Dir))
			at := lib
			for range up {
				at = filepath.Dir(at)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.Rename(at, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, at); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(lib, "dirty.txt"), "a file outside the working folder\n")
			head := run(t, lib, "rev-parse", "HEAD")
			_, _, err := pushOf(dir, b)
			if err == nil || !strings.Contains(err.Error(), "it has the link ") {
				t.Fatalf("the error is %v", err)
			}
			if got := run(t, lib, "rev-parse", "HEAD"); got != head {
				t.Errorf("qory committed outside the working folder: %s", got)
			}
		})
	}
}

// TestAGraftStopsTheStep is a commit with no link to the start that info/grafts gives the
// start as its parent, so that the check of the fast-forward would pass.
func TestAGraftStopsTheStep(t *testing.T) {
	inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, _ string) {
		tree := run(t, folder, "write-tree")
		forged := run(t, folder, "commit-tree", tree, "-m", "unrelated")
		start := run(t, folder, "rev-parse", "HEAD")
		run(t, folder, "update-ref", "HEAD", forged)
		write(t, filepath.Join(folder, ".git", "info", "grafts"), forged+" "+start+"\n")
		_, _, err := pushOf(dir, b)
		if err == nil || !strings.Contains(err.Error(), "its .git has info/grafts") {
			t.Fatalf("the error is %v", err)
		}
		if strings.Contains(err.Error(), "start the run in") {
			t.Errorf("the line has the fix of a run that has not started: %v", err)
		}
	})
}

// TestTheIdentityOfTheAgentDoesNotStopThePush is an agent that set user.name and user.email
// to commit, and the commit of qory, which does not use them.
func TestTheIdentityOfTheAgentDoesNotStopThePush(t *testing.T) {
	inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, _ string) {
		run(t, folder, "config", "user.name", "Agent")
		run(t, folder, "config", "user.email", "agent@example.com")
		if _, _, err := pushOf(dir, b); err != nil {
			t.Fatal(err)
		}
		if got := run(t, folder, "log", "-1", "--format=%an %ae %cn %ce"); got != "qory qory@localhost qory qory@localhost" {
			t.Errorf("the commit's identity is %q", got)
		}
	})
}

// TestOtherKeysOfTheUserSectionStop is a key that looks like the identity's and is not one.
func TestOtherKeysOfTheUserSectionStop(t *testing.T) {
	for _, key := range []string{"user.signingkey", "user.x.name", "author.name"} {
		inEachRepository(t, func(t *testing.T, dir, folder string, b *WorkBranch, _ string) {
			run(t, folder, "config", key, "a value")
			if _, _, err := pushOf(dir, b); err == nil || !strings.Contains(err.Error(), "the check stops on each key") {
				t.Fatalf("%s: the error is %v", key, err)
			}
		})
	}
}

// TestASetupThatSetsTheIdentityStops is the record after the setup, which is not a check
// after the run: a key of the identity stops it.
func TestASetupThatSetsTheIdentityStops(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	run(t, dir, "config", "user.name", "The setup")
	err := RecordSetup(context.Background(), dir, os.Environ(), b)
	if err == nil || !strings.Contains(err.Error(), "the check stops on each key") {
		t.Fatalf("the error is %v", err)
	}
}

// TestACheckThatFailedHasAFixAPersonCanCarryOut is a failed check after the run whose cause
// comes from a text for a run that has not started: the line has only the fix of a used code.
func TestACheckThatFailedHasAFixAPersonCanCarryOut(t *testing.T) {
	r := WorkRepository{Path: "acme/shop"}
	for name, err := range map[string]error{
		"git":     gitFailed(errors.New("fatal: a line of git")),
		"machine": local(errors.New("a read failed")),
		"a remote file": &unsafeError{"the line", (&hostgit.RefusedError{
			Dir: "/w", Key: "remotes/x", Reason: hostgit.RefusedRemoteFile}).Cause()},
		"a pointer": &unsafeError{"the line", (&hostgit.RefusedError{Reason: hostgit.RefusedPointer}).Cause()},
	} {
		got := notSafe(r, err).Error()
		for _, fix := range []string{"run again", "Remove it", "--accept-checkout", "docker ps"} {
			if strings.Contains(got, fix) {
				t.Errorf("%s: %q has %q", name, got, fix)
			}
		}
		if !strings.HasPrefix(got, "qory run: take the work item in again in Qory Apiary: ") || !strings.HasSuffix(got, "; the code is used") {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// TestAFailedHistoryCheckIsNotANonFastForward is a start commit that the repository does
// not have, so git merge-base exits 128: only its exit 1 is a history that left the start.
func TestAFailedHistoryCheckIsNotANonFastForward(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	r := b.Repositories[0]
	r.Commit = strings.Repeat("1", 40)
	_, err := headPast(context.Background(), stepGit(noRemote, ""), dir, r)
	if err == nil || !strings.Contains(err.Error(), "the check that HEAD of acme/shop descends from commit 111111111111 failed") {
		t.Fatalf("the error is %v", err)
	}
}

// TestAPasswordInGitsOutputIsHidden is a git whose error has the password of the proxy.
func TestAPasswordInGitsOutputIsHidden(t *testing.T) {
	const password = "the-password-of-the-proxy"
	p := &proxygit.Proxy{URL: "http://qory:" + password + "@127.0.0.1:9"}
	_, err := runGit(context.Background(), p, func(ctx context.Context) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "sh", "-c", "echo 'fatal: "+password+"' >&2; exit 128"), nil
	}, "push")
	ge := (*gitError)(nil)
	if !errors.As(err, &ge) {
		t.Fatalf("the error is %v", err)
	}
	if strings.Contains(err.Error(), password) || strings.Contains(ge.output, password) {
		t.Errorf("the password is in %q and %q", err, ge.output)
	}
}

// TestACommitThatFailsInAFurtherRepositoryPushesNothing is a commit that fails in the
// further repository, after the target's commit: no repository reaches its branch.
func TestACommitThatFailsInAFurtherRepositoryPushesNothing(t *testing.T) {
	dir, b := onBranch(t, "acme/shop,acme/lib")
	further := filepath.Join(dir, filepath.FromSlash(b.Repositories[1].Dir))
	write(t, filepath.Join(dir, "left.txt"), "left\n")
	write(t, filepath.Join(further, "left.txt"), "left\n")
	write(t, filepath.Join(further, ".git", "HEAD.lock"), "")
	_, _, err := pushOf(dir, b)
	if err == nil || !strings.Contains(err.Error(), "the commit of what the agent left in acme/lib failed") {
		t.Fatalf("the error is %v", err)
	}
	if got := b.PushedPaths(); got != "" {
		t.Errorf("qory pushed %s", got)
	}
}
