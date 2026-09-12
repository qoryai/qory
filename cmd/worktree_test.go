package cmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// runSplit runs the command with stdin and returns stdout and stderr apart.
func runSplit(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut strings.Builder
	root := cmd.Root()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	err := root.Execute()
	return out.String(), errOut.String(), err
}

// gitOut runs git in dir and returns its trimmed output.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// worktreeRepo is a checkout on main with the two-modules stack committed, a qory.yaml
// that links .env and .env.local, copies config/local.json and runs one command on add
// and one on remove, an .env and a config/local.json outside git, and one commit.
func worktreeRepo(t *testing.T) string {
	t.Helper()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	configure(t, root, nil, []string{"worktree:", "  link: [.env, .env.local]", "  copy: [config/local.json]", "  run:", `    add: ['printf "%s %s" "$QORY_BRANCH" "$(basename "$QORY_MAIN")" > ran.txt']`, `    remove: ['touch "$QORY_MAIN/removed-$QORY_BRANCH"']`})
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "stack")
	writeFile(t, filepath.Join(root, ".env"), "SECRET=1\n")
	writeFile(t, filepath.Join(root, "config", "local.json"), "{}\n")
	return root
}

// TestWorktreeAddPreparesAndComposes is a repository with a stack and a worktree section:
// add cuts the branch off main, sets its upstream to a remote branch of its own name,
// links and copies what the section names, reports the one missing in the main checkout,
// runs the add command with the environment set, and composes the harness into the
// worktree.
func TestWorktreeAddPreparesAndComposes(t *testing.T) {
	root := worktreeRepo(t)
	out, err := run(t, "wa", "feature")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	wantsRow(t, out, "path", "../wt-feature")
	wantsRow(t, out, "branch", "feature  (new off main)")
	wantsRow(t, out, "pushes to", "origin/feature")
	wantsRow(t, out, "linked", ".env  (from the main checkout)")
	wantsRow(t, out, "copied", "config/local.json  (from the main checkout)")
	wantsRow(t, out, "missing", ".env.local  (not in the main checkout; nothing linked)")
	wants(t, out, "composed 8 entries from 2 modules")
	if got := gitOut(t, wt, "branch", "--show-current"); got != "feature" {
		t.Errorf("branch %q", got)
	}
	if got := gitOut(t, wt, "config", "branch.feature.merge"); got != "refs/heads/feature" {
		t.Errorf("upstream branch %q", got)
	}
	if got := gitOut(t, wt, "config", "branch.feature.remote"); got != "origin" {
		t.Errorf("upstream remote %q", got)
	}
	if target, err := os.Readlink(filepath.Join(wt, ".env")); err != nil || target != filepath.Join("..", filepath.Base(root), ".env") {
		t.Errorf(".env links to %q, %v", target, err)
	}
	if data, _ := os.ReadFile(filepath.Join(wt, "config", "local.json")); string(data) != "{}\n" {
		t.Errorf("config/local.json: %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(wt, "ran.txt")); string(data) != "feature "+filepath.Base(root) {
		t.Errorf("ran.txt: %q", data)
	}
	if _, err := os.Stat(filepath.Join(wt, ".claude", "settings.json")); err != nil {
		t.Errorf("the harness is not composed into the worktree: %v", err)
	}
}

// TestWorktreeAddNeedsACommit is a repository with no commit yet: add says so instead
// of passing git's message on.
func TestWorktreeAddNeedsACommit(t *testing.T) {
	newCheckout(t)
	out, _, err := runSplit(t, "", "worktree", "add", "feature")
	if err == nil {
		t.Fatalf("add in a repository with no commit succeeded:\n%s", out)
	}
	wants(t, err.Error(), "main has no commit yet; a worktree branch starts from a commit, so commit once and add again")
}

// TestWorktreeAddReusesAndRefuses is add run twice for one branch, once from inside the
// worktree, a path standing on another branch, a base that does not exist on a new
// branch and on one that exists, and a name that is not a branch.
func TestWorktreeAddReusesAndRefuses(t *testing.T) {
	root := worktreeRepo(t)
	if _, err := run(t, "wa", "feature", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "wa", "feature", "--no-compose")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "path", "../wt-feature")
	wantsRow(t, out, "branch", "feature  (already there)")
	wants(t, out, ".env  (already in the worktree)", "config/local.json  (already in the worktree)")
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	t.Chdir(wt)
	out, err = run(t, "wa", "feature", "--no-compose")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "path", "../wt-feature  (you are in it)")
	t.Chdir(root)
	_, err = run(t, "wa", "feature", "--base", "nowhere")
	if err == nil || !strings.Contains(err.Error(), "base nowhere is not a branch, tag or commit") {
		t.Fatalf("bad base on an existing branch: %v", err)
	}
	runGit(t, wt, "switch", "-q", "-c", "other")
	_, err = run(t, "wa", "feature")
	if err == nil || !strings.Contains(err.Error(), "exists on branch other, not feature") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("another branch: %v", err)
	}
	_, err = run(t, "wa", "second", "--base", "nowhere")
	if err == nil || !strings.Contains(err.Error(), "base nowhere is not a branch, tag or commit") {
		t.Fatalf("bad base: %v", err)
	}
	_, err = run(t, "wa", "bad name")
	if err == nil || !strings.Contains(err.Error(), `"bad name" is not a branch name`) {
		t.Fatalf("bad name: %v", err)
	}
	_, err = run(t, "wa")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("no argument: %v", err)
	}
}

// runNoTTY runs the command with a regular file for stdin, the way a script runs it: no
// question can be asked.
func runNoTTY(t *testing.T, args ...string) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out strings.Builder
	root := cmd.Root()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(f)
	err = root.Execute()
	return out.String(), err
}

// tagCommit makes one more commit on main in root, touching file, and tags it.
func tagCommit(t *testing.T, root, file, tag string) {
	t.Helper()
	writeFile(t, filepath.Join(root, file), tag+"\n")
	runGit(t, root, "add", file)
	runGit(t, root, "commit", "-q", "-m", tag)
	runGit(t, root, "tag", tag)
}

// TestWorktreeAddMovesAnExistingBranchOntoABase is --base on a branch that exists: the
// base a new branch was cut from is recorded; a branch with no commits of its own is
// moved onto the new base after a question, which Enter answers yes; one with commits is
// kept on a no, rebased with --rebase, and left alone when already on the base; without
// a terminal the question is an error naming --rebase; uncommitted changes refuse the
// move; and a conflict aborts the rebase and keeps the branch as it was.
func TestWorktreeAddMovesAnExistingBranchOntoABase(t *testing.T) {
	root := worktreeRepo(t)
	if _, err := run(t, "wa", "feature", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	if got := gitOut(t, root, "config", "branch.feature.qory-base"); got != "main" {
		t.Errorf("recorded base %q, want main", got)
	}
	tagCommit(t, root, "release.txt", "v0.4.0")
	stdout, _, err := runSplit(t, "\n", "wa", "feature", "--no-compose", "--base", "v0.4.0")
	if err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	wants(t, stdout, "feature sits on main with no commits of its own. Move it onto v0.4.0? [Y/n]")
	wantsRow(t, stdout, "branch", "feature  (moved onto v0.4.0; was on main)")
	if gitOut(t, wt, "rev-parse", "HEAD") != gitOut(t, root, "rev-parse", "v0.4.0") {
		t.Errorf("the branch did not move")
	}
	if got := gitOut(t, root, "config", "branch.feature.qory-base"); got != "v0.4.0" {
		t.Errorf("recorded base %q, want v0.4.0", got)
	}
	writeFile(t, filepath.Join(wt, "work.txt"), "w\n")
	runGit(t, wt, "add", "work.txt")
	runGit(t, wt, "commit", "-q", "-m", "work")
	tagCommit(t, root, "release.txt", "v0.5.0")
	stdout, _, err = runSplit(t, "n\n", "wa", "feature", "--no-compose", "--base", "v0.5.0")
	if err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	wants(t, stdout, "feature holds 1 commit off v0.4.0. Rebase it onto v0.5.0? [y/N]")
	wantsRow(t, stdout, "branch", "feature  (already there; kept on v0.4.0, not moved onto v0.5.0)")
	_, err = runNoTTY(t, "wa", "feature", "--no-compose", "--base", "v0.5.0")
	if err == nil || !strings.Contains(err.Error(), "feature exists off v0.4.0, so --base v0.5.0 needs an answer: add --rebase to move it onto v0.5.0, or leave --base out to keep it") {
		t.Fatalf("no terminal: %v", err)
	}
	writeFile(t, filepath.Join(wt, "work.txt"), "edited\n")
	_, err = run(t, "wa", "feature", "--no-compose", "--base", "v0.5.0", "--rebase")
	if err == nil || !strings.Contains(err.Error(), "has uncommitted changes, so feature stays where it is") {
		t.Fatalf("dirty: %v", err)
	}
	runGit(t, wt, "checkout", "--", "work.txt")
	out, err := run(t, "wa", "feature", "--no-compose", "--base", "v0.5.0", "--rebase")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "branch", "feature  (rebased onto v0.5.0; 1 commit, was on v0.4.0)")
	if gitOut(t, wt, "rev-parse", "HEAD~1") != gitOut(t, root, "rev-parse", "v0.5.0") {
		t.Errorf("the commit is not on v0.5.0")
	}
	if got := gitOut(t, wt, "log", "-1", "--format=%s"); got != "work" {
		t.Errorf("HEAD is %q, want the rebased commit", got)
	}
	out, err = run(t, "wa", "feature", "--no-compose", "--base", "v0.5.0")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "branch", "feature  (already there; on v0.5.0 already)")
	// main edits work.txt too, so the rebase onto its tip conflicts.
	writeFile(t, filepath.Join(root, "work.txt"), "main\n")
	runGit(t, root, "add", "work.txt")
	runGit(t, root, "commit", "-q", "-m", "clash")
	runGit(t, root, "tag", "v0.6.0")
	before := gitOut(t, wt, "rev-parse", "HEAD")
	_, err = run(t, "wa", "feature", "--no-compose", "--base", "v0.6.0", "--rebase")
	if err == nil || !strings.Contains(err.Error(), "rebasing feature onto v0.6.0 stops at a conflict, so it stays on v0.5.0; run git rebase --onto v0.6.0 v0.5.0 in") {
		t.Fatalf("conflict: %v", err)
	}
	if gitOut(t, wt, "rev-parse", "HEAD") != before {
		t.Errorf("the branch moved despite the conflict")
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "worktrees", "wt-feature", "rebase-merge")); err == nil {
		t.Errorf("a rebase is still in progress")
	}
}

// TestWorktreeAddMovesALocalBranchWithoutAWorktree is --base on a branch that exists
// with no worktree: the worktree is made, then the branch moved.
func TestWorktreeAddMovesALocalBranchWithoutAWorktree(t *testing.T) {
	root := worktreeRepo(t)
	runGit(t, root, "branch", "feature")
	tagCommit(t, root, "release.txt", "v0.4.0")
	out, err := run(t, "wa", "feature", "--no-compose", "--base", "v0.4.0", "--rebase")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "branch", "feature  (moved onto v0.4.0; was on main)")
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	if gitOut(t, wt, "rev-parse", "HEAD") != gitOut(t, root, "rev-parse", "v0.4.0") {
		t.Errorf("the branch did not move")
	}
}

// TestWorktreeAddStopsAtAFailingCommand keeps the worktree and names the command.
func TestWorktreeAddStopsAtAFailingCommand(t *testing.T) {
	root := worktreeRepo(t)
	// The stack stays; the worktree section is replaced by one whose second command fails.
	file := filepath.Join(root, "qory.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	writeFile(t, file, doc[:strings.Index(doc, "worktree:\n")]+"worktree:\n  run:\n    add: ['true', 'exit 3', 'touch never']\n")
	_, err = run(t, "wa", "feature")
	if err == nil || !strings.Contains(err.Error(), "exit 3 in ") || !strings.Contains(err.Error(), "the worktree is kept, repair the command and add again") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("err = %v", err)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the worktree is gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "never")); err == nil {
		t.Errorf("the command after the failing one ran")
	}
}

// TestWorktreeRemoveGuardsAndRuns is remove on a worktree with a commit of its own
// without a terminal, refused with the flags named; with an uncommitted change, refused;
// with --force and --keep-branch it runs the remove command and goes, keeping the branch;
// a branch with nothing of its own goes quietly; --delete-branch takes one with commits;
// and the main checkout is never a target.
func TestWorktreeRemoveGuardsAndRuns(t *testing.T) {
	root := worktreeRepo(t)
	if _, err := run(t, "wa", "feature", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	writeFile(t, filepath.Join(wt, "new.txt"), "x\n")
	runGit(t, wt, "add", "new.txt")
	runGit(t, wt, "commit", "-q", "-m", "new")
	_, err := runNoTTY(t, "wr", "feature")
	if err == nil || !strings.Contains(err.Error(), "branch feature holds 1 commit no remote branch, the main checkout or its base holds; push them, or remove with --keep-branch or --delete-branch") {
		t.Fatalf("own commit: %v", err)
	}
	writeFile(t, filepath.Join(wt, "new.txt"), "y\n")
	_, err = run(t, "wr", "feature")
	if err == nil || !strings.Contains(err.Error(), "has uncommitted changes; commit or stash them, or remove with --force") {
		t.Fatalf("dirty: %v", err)
	}
	out, err := run(t, "wr", "feature", "--force", "--keep-branch")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "ran", `touch "$QORY_MAIN/removed-$QORY_BRANCH"`)
	wantsRow(t, out, "removed", "../wt-feature")
	wantsRow(t, out, "branch", "feature  (kept with 1 commit nothing else holds; git branch -D feature deletes it)")
	wantsRow(t, out, "main", root)
	if _, err := os.Stat(filepath.Join(root, "removed-feature")); err != nil {
		t.Errorf("the remove command did not run: %v", err)
	}
	gone(t, filepath.Dir(root), "wt-feature")
	if gitOut(t, root, "branch", "--list", "feature") == "" {
		t.Errorf("the branch went with the worktree")
	}
	if _, err := run(t, "wa", "second", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "wr", filepath.Join(filepath.Dir(root), "wt-second"))
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "branch", "second  (deleted; every commit of it is on the remote, in the main checkout or on its base)")
	if gitOut(t, root, "branch", "--list", "second") != "" {
		t.Errorf("the branch stayed")
	}
	if _, err := run(t, "wa", "feature", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	out, err = runNoTTY(t, "wr", "feature", "--delete-branch")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "branch", "feature  (deleted with 1 commit nothing else holds; git reflog finds it for 30 days)")
	if gitOut(t, root, "branch", "--list", "feature") != "" {
		t.Errorf("the branch stayed")
	}
	_, err = run(t, "wr", root)
	if err == nil || !strings.Contains(err.Error(), "is the main checkout, not a worktree") {
		t.Fatalf("main: %v", err)
	}
	_, err = run(t, "wr", "nothing")
	if err == nil || !strings.Contains(err.Error(), "no worktree is at nothing, on a branch of that name or added as it") {
		t.Fatalf("unknown: %v", err)
	}
}

// TestWorktreeRemoveAsksAboutOwnCommits is remove on a branch with a commit of its own
// at a terminal: Enter stops and leaves the worktree, k keeps the branch, d deletes it
// anyway, and p pushes it to the remote first, after which add tracks the remote branch.
func TestWorktreeRemoveAsksAboutOwnCommits(t *testing.T) {
	root := worktreeRepo(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, root, "init", "--quiet", "--bare", bare)
	runGit(t, root, "remote", "set-url", "origin", bare)
	runGit(t, root, "push", "--quiet", "origin", "main")
	own := func(branch string) string {
		if _, err := run(t, "wa", branch, "--no-compose"); err != nil {
			t.Fatal(err)
		}
		wt := filepath.Join(filepath.Dir(root), "wt-"+branch)
		writeFile(t, filepath.Join(wt, branch+".txt"), "x\n")
		runGit(t, wt, "add", branch+".txt")
		runGit(t, wt, "commit", "-q", "-m", "work on "+branch)
		return wt
	}
	wt := own("feature")
	question := `branch feature holds 1 commit no remote branch, the main checkout or its base holds (last: "work on feature").`
	stdout, _, err := runSplit(t, "\n", "wr", "feature")
	if err == nil || err.Error() != "nothing removed" {
		t.Fatalf("Enter: %v", err)
	}
	wants(t, stdout, question, "[p]ush it and delete, [k]eep it, [d]elete it anyway (git reflog finds the commits for 30 days), or [a]bort? [p/k/d/A]")
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("the worktree went: %v", err)
	}
	stdout, _, err = runSplit(t, "k\n", "wr", "feature")
	if err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	wantsRow(t, stdout, "branch", "feature  (kept with 1 commit nothing else holds; git branch -D feature deletes it)")
	runGit(t, root, "branch", "-D", "feature")
	own("feature")
	stdout, _, err = runSplit(t, "d\n", "wr", "feature")
	if err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	wantsRow(t, stdout, "branch", "feature  (deleted with 1 commit nothing else holds; git reflog finds it for 30 days)")
	if gitOut(t, root, "branch", "--list", "feature") != "" {
		t.Errorf("the branch stayed")
	}
	own("feature")
	stdout, _, err = runSplit(t, "p\n", "wr", "feature")
	if err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	wantsRow(t, stdout, "branch", "feature  (pushed to origin/feature, then deleted)")
	if gitOut(t, root, "ls-remote", "--heads", "origin", "feature") == "" {
		t.Errorf("origin lacks the branch")
	}
	out, err := run(t, "wa", "feature", "--no-compose")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "branch", "feature  (remote)")
	out, err = run(t, "wr", "feature")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "branch", "feature  (deleted; every commit of it is on the remote, in the main checkout or on its base)")
}

// TestWorktreeBranchSettingKeepsTheBranch is worktree.branch: keep in the user's file,
// which --delete-branch overrides, and a value that is neither delete nor keep.
func TestWorktreeBranchSettingKeepsTheBranch(t *testing.T) {
	root := worktreeRepo(t)
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	writeFile(t, user, "apiVersion: qory.ai/v1alpha1\nworktree: {branch: keep}\n")
	if _, err := run(t, "wa", "feature", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "wr", "feature")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "branch", "feature  (kept; git branch -d feature deletes it)")
	out, err = run(t, "config")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "worktree.branch")
	wants(t, out, "keep")
	if _, err := run(t, "wa", "feature", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "wr", "feature", "--delete-branch")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "branch", "feature  (deleted; every commit of it is on the remote, in the main checkout or on its base)")
	writeFile(t, user, "apiVersion: qory.ai/v1alpha1\nworktree: {branch: drop}\n")
	_, err = run(t, "wa", "feature", "--no-compose")
	if err == nil || !strings.Contains(err.Error(), `worktree.branch "drop" is not delete or keep`) {
		t.Fatalf("bad value: %v", err)
	}
	_ = root
}

// TestWorktreeRemoveDefaultsToTheOneYouStandIn is remove without an argument from inside
// a worktree, and the shell hook's --path output naming the main checkout alone.
func TestWorktreeRemoveDefaultsToTheOneYouStandIn(t *testing.T) {
	root := worktreeRepo(t)
	stdout, stderr, err := runSplit(t, "", "wa", "feature", "--no-compose", "--path")
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	if strings.TrimSpace(stdout) != wt {
		t.Errorf("stdout %q, want the path alone", stdout)
	}
	wants(t, stderr, "pushes to")
	t.Chdir(wt)
	stdout, stderr, err = runSplit(t, "", "wr", "--path")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != root {
		t.Errorf("stdout %q, want the main checkout alone", stdout)
	}
	wants(t, stderr, "removed")
	t.Chdir(root)
	gone(t, filepath.Dir(root), "wt-feature")
}

// TestWorktreeListNamesEveryWorktree lists the main checkout and the worktrees with
// their branches, and marks the composed ones.
func TestWorktreeListNamesEveryWorktree(t *testing.T) {
	root := worktreeRepo(t)
	if _, err := run(t, "wa", "feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "wa", "second", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "wl")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "main", "../wt-feature", "feature", "composed", "../wt-second", "second")
	_ = root
}

// TestWorktreeLayoutFromTheConfiguration is worktree.dir and worktree.name in the user's
// file: the worktree goes to that directory under that name, with {repo} and a slash in
// the branch made a dash.
func TestWorktreeLayoutFromTheConfiguration(t *testing.T) {
	root := worktreeRepo(t)
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	writeFile(t, user, "apiVersion: qory.ai/v1alpha1\nworktree: {dir: worktrees, name: \"{repo}-{branch}\"}\n")
	out, err := run(t, "wa", "issue/42", "--no-compose")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "worktrees", filepath.Base(root)+"-issue-42")
	wantsRow(t, out, "path", "worktrees/"+filepath.Base(root)+"-issue-42")
	if got := gitOut(t, want, "branch", "--show-current"); got != "issue/42" {
		t.Errorf("branch %q", got)
	}
}

// bareOrigin turns the checkout's origin into a bare repository beside it holding main,
// and returns the bare repository and a second clone of it, with which a test plays the
// colleague who pushes from elsewhere.
func bareOrigin(t *testing.T, root string) (bare, dev string) {
	t.Helper()
	bare = filepath.Join(t.TempDir(), "origin.git")
	runGit(t, root, "init", "--quiet", "--bare", bare)
	runGit(t, root, "remote", "set-url", "origin", bare)
	runGit(t, root, "push", "--quiet", "origin", "main")
	dev = filepath.Join(t.TempDir(), "dev")
	runGit(t, root, "clone", "--quiet", bare, dev)
	runGit(t, dev, "config", "user.name", "Dev")
	runGit(t, dev, "config", "user.email", "dev@example.com")
	return bare, dev
}

// commitOn switches the clone to branch, cutting it when it is new, commits one change
// there and returns the commit.
func commitOn(t *testing.T, dev, branch, change string) string {
	t.Helper()
	if out, _ := exec.Command("git", "-C", dev, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output(); len(out) == 0 {
		runGit(t, dev, "switch", "-q", "-c", branch)
	} else {
		runGit(t, dev, "switch", "-q", branch)
	}
	writeFile(t, filepath.Join(dev, branch+".txt"), change+"\n")
	runGit(t, dev, "add", branch+".txt")
	runGit(t, dev, "commit", "-q", "-m", change+" on "+branch)
	return gitOut(t, dev, "rev-parse", "HEAD")
}

// TestWorktreeAddAttachesToRemoteBranch is --branch: a branch pushed from elsewhere is
// fetched and tracked without a fetch of the whole remote, one the remote lacks is
// refused, a name beside the flag names the worktree, a worktree already on the branch
// is reused wherever it is, and a local branch that was kept is fast-forwarded, or left
// where it is when it is ahead or has diverged.
func TestWorktreeAddAttachesToRemoteBranch(t *testing.T) {
	root := worktreeRepo(t)
	_, dev := bareOrigin(t, root)
	tip := commitOn(t, dev, "feature", "first")
	runGit(t, dev, "push", "--quiet", "origin", "feature")
	_, err := run(t, "wa", "--branch", "nowhere", "--no-compose")
	if err == nil || err.Error() != "origin has no branch nowhere; qory worktree add nowhere cuts a new one" || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("a branch the remote lacks: %v", err)
	}
	out, err := run(t, "wa", "--branch", "feature", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	wants(t, out, "worktree feature")
	wantsRow(t, out, "path", "../wt-feature")
	wantsRow(t, out, "branch", "feature  (fetched from origin)")
	wantsRow(t, out, "pushes to", "origin/feature")
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != tip {
		t.Errorf("HEAD %s, want the remote's %s", got, tip)
	}
	if got := gitOut(t, wt, "config", "branch.feature.merge"); got != "refs/heads/feature" {
		t.Errorf("upstream branch %q", got)
	}
	out, err = run(t, "wa", "review", "--branch", "feature", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "worktree review")
	wantsRow(t, out, "path", "../wt-feature")
	wantsRow(t, out, "branch", "feature  (already there)")
	if _, err := run(t, "wr", "feature", "--keep-branch"); err != nil {
		t.Fatal(err)
	}
	tip = commitOn(t, dev, "feature", "second")
	runGit(t, dev, "push", "--quiet", "origin", "feature")
	out, err = run(t, "wa", "short", "--branch", "feature", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wt = filepath.Join(filepath.Dir(root), "wt-short")
	wantsRow(t, out, "path", "../wt-short")
	wantsRow(t, out, "branch", "feature  (fetched from origin; local branch fast-forwarded to origin/feature)")
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != tip {
		t.Errorf("HEAD %s, want the remote's %s", got, tip)
	}
	if _, err := run(t, "wr", "short", "--keep-branch"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "wa", "--branch", "feature", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "branch", "feature  (fetched from origin; local branch at origin/feature)")
	wt = filepath.Join(filepath.Dir(root), "wt-feature")
	writeFile(t, filepath.Join(wt, "mine.txt"), "x\n")
	runGit(t, wt, "add", "mine.txt")
	runGit(t, wt, "commit", "-q", "-m", "mine")
	if _, err := run(t, "wr", "feature", "--keep-branch"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "wa", "--branch", "feature", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "branch", "feature  (fetched from origin; local branch ahead of origin/feature)")
	if _, err := run(t, "wr", "feature", "--keep-branch"); err != nil {
		t.Fatal(err)
	}
	commitOn(t, dev, "feature", "third")
	runGit(t, dev, "push", "--quiet", "origin", "feature")
	out, err = run(t, "wa", "--branch", "feature", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "branch", "feature  (fetched from origin; local branch diverged from origin/feature; git pull merges)")
	_, err = run(t, "wa", "--branch", "main", "--no-compose")
	if err == nil || !strings.Contains(err.Error(), "main is checked out in the main checkout, "+root+"; a worktree is for another branch") {
		t.Fatalf("the main checkout's branch: %v", err)
	}
	_, err = run(t, "wa")
	if err == nil || err.Error() != "qory wa takes a branch name, or --branch or --pr to say which branch of the remote to attach to" || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("no branch: %v", err)
	}
	runGit(t, root, "remote", "remove", "origin")
	_, err = run(t, "wa", "--branch", "feature", "--no-compose")
	if err == nil || err.Error() != "the repository has no remote to attach to" {
		t.Fatalf("no remote: %v", err)
	}
}

// TestWorktreeAddAttachesToPullRequest is --pr: a pull request whose head is a branch of
// the remote checks that branch out and pushes to it; one whose head no branch holds, a
// fork's, is checked out as pr-<n> and pulls from the pull request's ref, and its remove
// is quiet since the commits are on the remote; the refs GitLab publishes are found too;
// a head at the tip of two branches is refused; a number the remote publishes nothing
// for says what was looked for; worktree.pr names the host's own ref; and the flags that
// do not go together.
func TestWorktreeAddAttachesToPullRequest(t *testing.T) {
	root := worktreeRepo(t)
	bare, dev := bareOrigin(t, root)
	topic := commitOn(t, dev, "topic", "first")
	runGit(t, dev, "push", "--quiet", "origin", "topic")
	runGit(t, bare, "update-ref", "refs/pull/7/head", topic)
	out, err := run(t, "wa", "--pr", "7", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-topic")
	wants(t, out, "worktree topic")
	wantsRow(t, out, "path", "../wt-topic")
	wantsRow(t, out, "branch", "topic  (pull request #7, fetched from origin)")
	wantsRow(t, out, "pushes to", "origin/topic")
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != topic {
		t.Errorf("HEAD %s, want the pull request's %s", got, topic)
	}
	if got := gitOut(t, wt, "config", "branch.topic.merge"); got != "refs/heads/topic" {
		t.Errorf("upstream branch %q", got)
	}
	fork := commitOn(t, dev, "fork-work", "first")
	runGit(t, dev, "push", "--quiet", "origin", "HEAD:refs/pull/8/head")
	out, err = run(t, "wa", "review", "--pr", "8", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wt = filepath.Join(filepath.Dir(root), "wt-review")
	wantsRow(t, out, "path", "../wt-review")
	wantsRow(t, out, "branch", "pr-8  (pull request #8, fetched from origin)")
	wantsRow(t, out, "pulls from", "origin refs/pull/8/head  (no branch of the remote holds it, a fork's or a deleted one; a push from here goes nowhere)")
	lacks(t, out, "pushes to")
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != fork {
		t.Errorf("HEAD %s, want the pull request's %s", got, fork)
	}
	if got := gitOut(t, wt, "config", "branch.pr-8.merge"); got != "refs/pull/8/head" {
		t.Errorf("upstream branch %q", got)
	}
	out, err = run(t, "wr", "review")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "branch", "pr-8  (deleted; every commit of it is on the remote, in the main checkout or on its base)")
	runGit(t, bare, "update-ref", "refs/merge-requests/9/head", topic)
	out, err = run(t, "wa", "--pr", "9", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "path", "../wt-topic")
	wantsRow(t, out, "branch", "topic  (already there)")
	_, err = run(t, "wa", "--pr", "10", "--no-compose")
	if err == nil || !strings.HasPrefix(err.Error(), "pull request 10 not found on origin; looked for refs/pull/10/head, refs/merge-requests/10/head, refs/pull-requests/10/from. worktree.pr in qory.yaml names the ref your host uses") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("no such pull request: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "wt-pr-10")); err == nil {
		t.Errorf("a pull request that is not found made a worktree")
	}
	if out := gitOut(t, root, "branch", "--list", "pr-10"); out != "" {
		t.Errorf("a pull request that is not found made a branch: %s", out)
	}
	runGit(t, dev, "push", "--quiet", "origin", "topic:refs/heads/topic-copy")
	_, err = run(t, "wa", "--pr", "7", "--no-compose")
	if err == nil || err.Error() != "pull request 7 is at the tip of 2 branches of origin, topic, topic-copy; --branch says which to attach to" {
		t.Fatalf("two branches: %v", err)
	}
	configure(t, root, nil, []string{"  pr: refs/changes/{n}/head"})
	runGit(t, bare, "update-ref", "refs/changes/11/head", fork)
	_, err = run(t, "wa", "--pr", "7", "--no-compose")
	if err == nil || !strings.Contains(err.Error(), "looked for refs/changes/7/head.") {
		t.Fatalf("worktree.pr alone is looked for: %v", err)
	}
	out, err = run(t, "wa", "--pr", "11", "--no-compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantsRow(t, out, "branch", "pr-11  (pull request #11, fetched from origin)")
	wantsRow(t, out, "pulls from", "origin refs/changes/11/head  (no branch of the remote holds it, a fork's or a deleted one; a push from here goes nowhere)")
	out, err = run(t, "config")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "worktree.pr", "refs/changes/{n}/head")
	_, err = run(t, "wa", "--branch", "topic", "--pr", "7")
	if err == nil || !strings.Contains(err.Error(), "none of the others can be") {
		t.Fatalf("both flags: %v", err)
	}
	_, err = run(t, "wa", "--pr", "0")
	if err == nil || err.Error() != "--pr takes a pull request number, got 0" {
		t.Fatalf("pr 0: %v", err)
	}
}
