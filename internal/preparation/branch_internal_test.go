package preparation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/branchformat"
)

const testRef = "qory/wi_1"

// branchCommit commits on top of first in a clone of base/path.git, with change applied,
// pushes the commit to the branch work, and returns it.
func branchCommit(t *testing.T, base, path, first string, change func(work string)) string {
	t.Helper()
	work := t.TempDir()
	run(t, work, "clone", "--quiet", filepath.Join(base, filepath.FromSlash(path)+".git"), ".")
	run(t, work, "checkout", "--quiet", "-b", "work", first)
	change(work)
	run(t, work, "add", "-A")
	run(t, work, "commit", "--quiet", "-m", "branch")
	run(t, work, "push", "--quiet", "origin", "work")
	return run(t, work, "rev-parse", "HEAD")
}

// versionOf is a version with the commit of each path, given as path, commit, path,
// commit, on git.example.com.
func versionOf(pairs ...string) []branchformat.Repository {
	var entries []branchformat.Repository
	for i := 0; i < len(pairs); i += 2 {
		entries = append(entries, branchformat.Repository{System: "git.example.com", Path: pairs[i], Commit: pairs[i+1]})
	}
	return entries
}

// fetchBranchAt fetches list with the target acme/shop at version and the branch with
// entries, and returns the branch.
func fetchBranchAt(t *testing.T, dir, list, version string, entries []branchformat.Repository) *WorkBranch {
	t.Helper()
	b := &WorkBranch{Ref: testRef, Version: entries}
	if _, err := fetchListAt(dir, list, Target{System: "git.example.com", Path: "acme/shop", Version: version, Branch: b}); err != nil {
		t.Fatal(err)
	}
	return b
}

// contentOf is the content of the file path.
func contentOf(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// switchTo is [Switch] of b in dir, which returns the line it printed.
func switchTo(dir string, b *WorkBranch) (string, error) {
	var report strings.Builder
	err := Switch(context.Background(), dir, os.Environ(), b, &report)
	return strings.TrimSuffix(report.String(), "\n"), err
}

// TestTheTargetStaysAtItsVersionUntilTheSwitch is a run with a branch version: the fetch
// leaves the working folder at the target version, for the compose, and the switch puts
// it on the branch at the version's commit.
func TestTheTargetStaysAtItsVersionUntilTheSwitch(t *testing.T) {
	needGit(t)
	base, dir := t.TempDir(), emptyFolder(t)
	first, _ := movedRemote(t, base, "acme/shop")
	commit := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, "qory.yaml"), "branch\n")
	})

	b := fetchBranchAt(t, dir, "acme/shop", first, versionOf("acme/shop", commit))
	if got := run(t, dir, "rev-parse", "HEAD"); got != first {
		t.Fatalf("after the fetch HEAD = %s, want %s", got, first)
	}
	if got := contentOf(t, filepath.Join(dir, "qory.yaml")); got != "first\n" {
		t.Fatalf("after the fetch qory.yaml = %q", got)
	}

	line, err := switchTo(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := "qory run: on branch " + testRef + " at " + commit[:12]; line != want {
		t.Errorf("line = %q, want %q", line, want)
	}
	if got := run(t, dir, "symbolic-ref", "--short", "HEAD"); got != testRef {
		t.Errorf("branch = %s", got)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); got != commit {
		t.Errorf("HEAD = %s, want %s", got, commit)
	}
	if got := contentOf(t, filepath.Join(dir, "qory.yaml")); got != "branch\n" {
		t.Errorf("qory.yaml = %q", got)
	}
	if want := (WorkRepository{Path: "acme/shop", Dir: ".", Commit: commit}); len(b.Repositories) != 1 || b.Repositories[0] != want {
		t.Errorf("repositories = %+v", b.Repositories)
	}
}

// TestABranchWithNoVersionStartsAtTheTargetVersion is a work item with no branch version:
// the branch is new, at the target version.
func TestABranchWithNoVersionStartsAtTheTargetVersion(t *testing.T) {
	needGit(t)
	base, dir := t.TempDir(), emptyFolder(t)
	first, _ := movedRemote(t, base, "acme/shop")

	b := fetchBranchAt(t, dir, "acme/shop", first, nil)
	if _, err := switchTo(dir, b); err != nil {
		t.Fatal(err)
	}

	if got := run(t, dir, "symbolic-ref", "--short", "HEAD"); got != testRef {
		t.Errorf("branch = %s", got)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); got != first {
		t.Errorf("HEAD = %s, want %s", got, first)
	}
}

// TestFurtherRepositoriesGetTheBranch is a run of three repositories, with a version that
// has an entry for one further repository and one that the run does not list: that one
// is left alone, each further repository is on the branch, and the entries stay.
func TestFurtherRepositoriesGetTheBranch(t *testing.T) {
	needGit(t)
	base, dir := t.TempDir(), emptyFolder(t)
	first, _ := movedRemote(t, base, "acme/shop")
	apiFirst, apiHead := movedRemote(t, base, "acme/api")
	_, docsHead := movedRemote(t, base, "acme/docs")
	apiCommit := branchCommit(t, base, "acme/api", apiFirst, func(work string) {
		write(t, filepath.Join(work, "qory.yaml"), "branch\n")
	})
	entries := versionOf("acme/api", apiCommit, "acme/gone", strings.Repeat("a", 40))

	b := fetchBranchAt(t, dir, "acme/shop,acme/api,acme/docs", first, entries)

	api := filepath.Join(dir, ".qory", "repositories", "acme", "api")
	if got := run(t, api, "rev-parse", "HEAD"); got != apiCommit {
		t.Errorf("api HEAD = %s, want %s, the head was %s", got, apiCommit, apiHead)
	}
	docs := filepath.Join(dir, ".qory", "repositories", "acme", "docs")
	if got := run(t, docs, "rev-parse", "HEAD"); got != docsHead {
		t.Errorf("docs HEAD = %s, want %s", got, docsHead)
	}
	for _, repository := range []string{api, docs} {
		if got := run(t, repository, "symbolic-ref", "--short", "HEAD"); got != testRef {
			t.Errorf("%s branch = %s", repository, got)
		}
	}

	if _, err := switchTo(dir, b); err != nil {
		t.Fatal(err)
	}
	want := []WorkRepository{
		{Path: "acme/shop", Dir: ".", Commit: first},
		{Path: "acme/api", Dir: ".qory/repositories/acme/api", Commit: apiCommit},
		{Path: "acme/docs", Dir: ".qory/repositories/acme/docs", Commit: docsHead},
	}
	if len(b.Repositories) != len(want) {
		t.Fatalf("repositories = %+v", b.Repositories)
	}
	for i := range want {
		if b.Repositories[i] != want[i] {
			t.Errorf("repository %d = %+v, want %+v", i, b.Repositories[i], want[i])
		}
	}
	if len(b.Version) != 2 {
		t.Errorf("version = %+v", b.Version)
	}
}

// TestABranchCannotChangeTheComposedHarness is a branch that adds a file under .qory: the
// switch stops before the checkout, so the file is not in the composed home.
func TestABranchCannotChangeTheComposedHarness(t *testing.T) {
	needGit(t)
	base, dir := t.TempDir(), emptyFolder(t)
	first, _ := movedRemote(t, base, "acme/shop")
	commit := branchCommit(t, base, "acme/shop", first, func(work string) {
		write(t, filepath.Join(work, ".qory", "harness", "claude", "settings.json"), "{}\n")
	})

	b := fetchBranchAt(t, dir, "acme/shop", first, versionOf("acme/shop", commit))

	_, err := switchTo(dir, b)
	if err == nil || !strings.HasPrefix(err.Error(), "qory run: make .qory in commit "+commit[:12]) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".qory", "harness")); !os.IsNotExist(err) {
		t.Errorf(".qory/harness: %v", err)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); got != first {
		t.Errorf("HEAD = %s", got)
	}
}

// TestABranchCannotReplaceALinkOfTheHarness has the compose's link at a path that the
// branch tracks, excluded or not: the switch stops, and the link stays.
func TestABranchCannotReplaceALinkOfTheHarness(t *testing.T) {
	for name, excluded := range map[string]bool{"untracked": false, "excluded": true} {
		t.Run(name, func(t *testing.T) {
			needGit(t)
			base, dir := t.TempDir(), emptyFolder(t)
			first, _ := movedRemote(t, base, "acme/shop")
			commit := branchCommit(t, base, "acme/shop", first, func(work string) {
				write(t, filepath.Join(work, "CLAUDE.md"), "from the branch\n")
			})

			b := fetchBranchAt(t, dir, "acme/shop", first, versionOf("acme/shop", commit))
			if err := os.Symlink(".qory/harness/CLAUDE.md", filepath.Join(dir, "CLAUDE.md")); err != nil {
				t.Fatal(err)
			}
			if excluded {
				write(t, filepath.Join(dir, ".git", "info", "exclude"), "/CLAUDE.md\n")
			}

			_, err := switchTo(dir, b)
			if err == nil || !strings.HasPrefix(err.Error(), "qory run: remove the harness's links from commit "+commit[:12]) {
				t.Fatalf("err = %v", err)
			}
			if target, err := os.Readlink(filepath.Join(dir, "CLAUDE.md")); err != nil || target != ".qory/harness/CLAUDE.md" {
				t.Errorf("link = %q, %v", target, err)
			}
		})
	}
}

// TestStopsOnABranchCommitTheRemoteLacks is a version whose commit is not on the remote:
// the fetch stops, as for a target version.
func TestStopsOnABranchCommitTheRemoteLacks(t *testing.T) {
	needGit(t)
	base, dir := t.TempDir(), emptyFolder(t)
	first, _ := movedRemote(t, base, "acme/shop")

	b := &WorkBranch{Ref: testRef, Version: versionOf("acme/shop", strings.Repeat("a", 40))}
	_, err := fetchListAt(dir, "acme/shop", Target{System: "git.example.com", Path: "acme/shop", Version: first, Branch: b})
	if err == nil || !strings.Contains(err.Error(), strings.Repeat("a", 12)) {
		t.Fatalf("err = %v", err)
	}
}

// TestABranchNeedsATargetVersion is a run with no version: the fetch uses no branch.
func TestABranchNeedsATargetVersion(t *testing.T) {
	needGit(t)
	base, dir := t.TempDir(), emptyFolder(t)
	movedRemote(t, base, "acme/shop")

	b := &WorkBranch{Ref: testRef}
	if _, err := fetchListAt(dir, "acme/shop", Target{Branch: b}); err != nil {
		t.Fatal(err)
	}
	if b.Repositories != nil {
		t.Errorf("repositories = %+v", b.Repositories)
	}
}
