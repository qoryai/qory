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

// TestWorktreeAddReusesAndRefuses is add run twice for one branch, a path standing on
// another branch, a base that does not exist, and a name that is not a branch.
func TestWorktreeAddReusesAndRefuses(t *testing.T) {
	root := worktreeRepo(t)
	if _, err := run(t, "wa", "feature", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "wa", "feature", "--no-compose")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "branch", "feature  (reused)")
	wants(t, out, ".env  (already in the worktree)", "config/local.json  (already in the worktree)")
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
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

// TestWorktreeRemoveGuardsAndRuns is remove on a worktree with an unpushed commit, then
// with an uncommitted change, both refused; with --force it runs the remove command and
// goes, keeping the branch; --delete-branch takes the branch too; and the main checkout
// is never a target.
func TestWorktreeRemoveGuardsAndRuns(t *testing.T) {
	root := worktreeRepo(t)
	if _, err := run(t, "wa", "feature", "--no-compose"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(filepath.Dir(root), "wt-feature")
	writeFile(t, filepath.Join(wt, "new.txt"), "x\n")
	runGit(t, wt, "add", "new.txt")
	runGit(t, wt, "commit", "-q", "-m", "new")
	_, err := run(t, "wr", "feature")
	if err == nil || !strings.Contains(err.Error(), "branch feature has 1 commit no remote branch and not the main checkout holds; push them, or remove with --force") {
		t.Fatalf("unpushed: %v", err)
	}
	writeFile(t, filepath.Join(wt, "new.txt"), "y\n")
	_, err = run(t, "wr", "feature")
	if err == nil || !strings.Contains(err.Error(), "has uncommitted changes; commit or stash them, or remove with --force") {
		t.Fatalf("dirty: %v", err)
	}
	out, err := run(t, "wr", "feature", "--force")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "ran", `touch "$QORY_MAIN/removed-$QORY_BRANCH"`)
	wantsRow(t, out, "removed", "../wt-feature")
	wantsRow(t, out, "branch", "feature  (kept; git branch -d feature deletes it)")
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
	out, err = run(t, "wr", filepath.Join(filepath.Dir(root), "wt-second"), "--delete-branch")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "branch", "second  (deleted)")
	if gitOut(t, root, "branch", "--list", "second") != "" {
		t.Errorf("the branch stayed")
	}
	_, err = run(t, "wr", root)
	if err == nil || !strings.Contains(err.Error(), "is the main checkout, not a worktree") {
		t.Fatalf("main: %v", err)
	}
	_, err = run(t, "wr", "nothing")
	if err == nil || !strings.Contains(err.Error(), "no worktree is at nothing or on a branch of that name") {
		t.Fatalf("unknown: %v", err)
	}
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
