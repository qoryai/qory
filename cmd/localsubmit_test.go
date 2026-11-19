package cmd_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// localProgramCheckout is a checkout of a harness with a module that declares a submit program,
// with a remote origin that is a bare repository next to it, for a local run: its commits land
// on a branch of the checkout, and a push goes to the bare repository. It returns the checkout,
// the bare repository and the number of times the program started, which a file records: the
// program is a script that appends a line to the file.
func localProgramCheckout(t *testing.T) (root, bare, started string) {
	t.Helper()
	root = newCheckout(t)
	copyFixture(t, "two-modules", root)
	started = filepath.Join(tempDir(t), "started")
	writeFile(t, filepath.Join(root, "README.md"), "first\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), strings.Replace(readText(t, filepath.Join(root, "qory.yaml")),
		"    - name: nextjs\n", "    - name: forge\n      source: {path: modules/forge}\n    - name: nextjs\n", 1))
	for name, content := range programModule() {
		writeFile(t, filepath.Join(root, name), content)
	}
	// The program writes its path, branch and draft, and how many of the repositories listed in the
	// file started.bares already have the branch: every push comes before the first program.
	writeFile(t, filepath.Join(root, "modules/forge/bin/open-draft"), `#!/bin/sh
n=0
for b in $(cat `+started+`.bares); do
  git --git-dir="$b" rev-parse --verify -q "refs/heads/$QORY_SUBMIT_BRANCH" >/dev/null && n=$((n+1))
done
echo "$QORY_SUBMIT_PATH $QORY_SUBMIT_BRANCH $QORY_SUBMIT_DRAFT $n" >> `+started+`
if [ -f `+started+`.out ]; then sed "s|@PATH@|$QORY_SUBMIT_PATH|g" `+started+`.out; fi
exit $(cat `+started+`.exit 2>/dev/null || echo 0)
`)
	composedForFake(t, root, fakeRuntime(t))
	runGit(t, root, "add", "README.md", "qory.yaml", "modules")
	runGit(t, root, "commit", "--quiet", "-m", "first")
	bare = filepath.Join(tempDir(t), "origin.git")
	runGit(t, root, "init", "--quiet", "--bare", "--initial-branch=main", bare)
	writeFile(t, started+".bares", bare+"\n")
	routePush(t, "https://git.example.com/acme/app.git", bare)
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	return root, bare, started
}

// pushRoutes is where the test run's pushes go: the address that qory pushes to, and the bare
// repository on disk that stands in for it.
var pushRoutes = map[string]string{}

// routePush makes a push to address go to the bare repository bare, for the rest of the test.
func routePush(t *testing.T, address, bare string) {
	t.Helper()
	if len(pushRoutes) == 0 {
		t.Cleanup(cmd.SetLocalPushAddress(func(a string) string {
			if b, ok := pushRoutes[a]; ok {
				return b
			}
			return a
		}))
		t.Cleanup(func() { clear(pushRoutes) })
	}
	pushRoutes[address] = bare
}

// bothChecks registers the push checks that a local run needs, each calling check. It returns
// the number of calls.
func bothChecks(t *testing.T, check func(ctx context.Context, dir, branch string) error) *int {
	t.Helper()
	calls := new(int)
	count := func(ctx context.Context, dir, branch string) error { *calls++; return check(ctx, dir, branch) }
	names := cmd.PushChecksRequired()
	if len(names) != 2 {
		t.Fatalf("the push checks required are %q, want 2", names)
	}
	t.Cleanup(cmd.RegisterPushCheck(names[0], count))
	t.Cleanup(cmd.RegisterPushCheck(names[1], func(context.Context, string, string) error { return nil }))
	return calls
}

// makeProgramRunnable makes the submit program in the run's composed home executable: a home
// that qory makes for a run has the files of the modules with the mode 0644.
func makeProgramRunnable(t *testing.T) {
	t.Helper()
	found := false
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(filepath.Join(cache, "qory"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(path) == "open-draft" {
			found = true
			return os.Chmod(path, 0o755)
		}
		return err
	})
	if err != nil || !found {
		t.Fatalf("the run's home has no submit program: %v", err)
	}
}

// handsOffText is an agent that commits a file and writes a hand-off with a title.
func handsOffText(t *testing.T) func(spec session.SessionSpec) {
	return func(spec session.SessionSpec) {
		makeProgramRunnable(t)
		writeFile(t, filepath.Join(spec.Dir, "committed.txt"), "committed\n")
		gitOutput(t, spec.Dir, "add", "committed.txt")
		gitOutput(t, spec.Dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "Add a file")
		writeFile(t, filepath.Join(spec.Dir, ".qory", "handoff", "submission.json"), `{"text":{"title":"Add a file","description":"A file."}}`)
	}
}

// pushedBranches is the branches of the bare repository, one per line.
func pushedBranches(t *testing.T, bare string) string {
	t.Helper()
	return gitOutput(t, bare, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
}

// timesStarted is how many times the submit program started, from the file it appends to.
func timesStarted(t *testing.T, started string) int {
	t.Helper()
	data, err := os.ReadFile(started)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// TestALocalWriteRunWithAHandoffPushesThroughTheChecksThenRunsTheProgram is a local run with a
// hand-off text and a push check that passes: qory pushes qory/run-<run id> once, after the check
// ran on it, and the submit program starts once, on that branch.
func TestALocalWriteRunWithAHandoffPushesThroughTheChecksThenRunsTheProgram(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	var checked []string
	bothChecks(t, func(_ context.Context, dir, head string) error {
		if got := pushedBranches(t, bare); got != "" {
			t.Errorf("the bare repository has %q before the check ends", got)
		}
		checked = append(checked, dir+" "+head)
		return nil
	})
	agentInCopy(t, handsOffText(t))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	if !strings.HasPrefix(branch, "qory/run-") {
		t.Errorf("the branch is %s, want qory/run-<run id>", branch)
	}
	if head := gitOutput(t, root, "rev-parse", branch); len(checked) != 1 || !strings.HasSuffix(checked[0], " "+head) {
		t.Errorf("the checks ran on %v, want once on the head %s of %s", checked, head, branch)
	}
	if got := pushedBranches(t, bare); got != branch {
		t.Errorf("the bare repository has %q, want %s", got, branch)
	}
	if got := gitOutput(t, bare, "rev-parse", branch); got != gitOutput(t, root, "rev-parse", branch) {
		t.Errorf("the pushed head is %s, want the local one", got)
	}
	if n := timesStarted(t, started); n != 1 {
		t.Errorf("the program started %d times, want 1\n%s", n, out)
	}
	if got := strings.TrimSpace(readText(t, started)); got != "acme/app "+branch+" open 1" {
		t.Errorf("the program got %q, want the path, %s, the draft open and 1 pushed repository", got, branch)
	}
}

// TestALocalRunWithARefusingCheckPushesNothing is a local run with a hand-off text and a push
// check that refuses: nothing is pushed, no program starts, and the output says why.
func TestALocalRunWithARefusingCheckPushesNothing(t *testing.T) {
	_, bare, started := localProgramCheckout(t)
	bothChecks(t, func(context.Context, string, string) error { return errors.New("a key is in a commit") })
	agentInCopy(t, handsOffText(t))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := pushedBranches(t, bare); got != "" {
		t.Errorf("the bare repository has %q, want none", got)
	}
	if n := timesStarted(t, started); n != 0 {
		t.Errorf("the program started %d times, want 0", n)
	}
	wants(t, out, "qory run: Fix the cause below, then run again: a push check refused the push.\n  qory pushed nothing and opened no draft.")
	wants(t, out, "a key is in a commit")
}

// TestALocalRunWithNoCheckPushesNothing is a local run with no push check
// registered and a hand-off file that is not usable: the step fails closed. The output has the
// line that says so, of at most 100 characters, and one line after it, and nothing else, and
// nothing is pushed or started.
func TestALocalRunWithNoCheckPushesNothing(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		handsOffText(t)(spec)
		// A hand-off file that is not usable would print its warning line if the step read it.
		writeFile(t, filepath.Join(spec.Dir, ".qory", "handoff", "submission.json"), "{")
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	const line = "qory run: Push each branch by hand: the key and scope push checks of qory run are not registered."
	if len(line) > 100 {
		t.Fatalf("the line has %d characters", len(line))
	}
	if n := strings.Count(out, line); n != 1 {
		t.Errorf("the output has the line %d times, want 1:\n%s", n, out)
	}
	wants(t, out, line+"\n  qory pushed nothing and opened no draft; open each draft by hand if you want one.\n  Branch: "+runBranchOf(t, out)+" in "+root+".\n")
	lacks(t, out, "hand-off")
	if got := pushedBranches(t, bare); got != "" {
		t.Errorf("the bare repository has %q, want none", got)
	}
	if n := timesStarted(t, started); n != 0 {
		t.Errorf("the program started %d times, want 0", n)
	}
}

// passingCheck registers the push checks, which pass, and counts the calls of the first.
func passingCheck(t *testing.T) *int {
	t.Helper()
	return bothChecks(t, func(context.Context, string, string) error { return nil })
}

// TestALocalRunWithNoHandoffPushesAndRunsTheProgramWithNoDraft is a local run whose agent wrote
// no hand-off: qory pushes the branch once, through the check, and starts the program once, with
// the draft none and no title.
func TestALocalRunWithNoHandoffPushesAndRunsTheProgramWithNoDraft(t *testing.T) {
	_, bare, started := localProgramCheckout(t)
	calls := passingCheck(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		makeProgramRunnable(t)
		writeFile(t, filepath.Join(spec.Dir, "committed.txt"), "committed\n")
		gitOutput(t, spec.Dir, "add", "committed.txt")
		gitOutput(t, spec.Dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "Add a file")
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	if got := pushedBranches(t, bare); got != branch || *calls != 1 {
		t.Errorf("pushed %q after %d checks, want %s after 1", got, *calls, branch)
	}
	if got := strings.TrimSpace(readText(t, started)); got != "acme/app "+branch+" none 1" {
		t.Errorf("the program got %q, want the path, %s, the draft none and 1 pushed repository", got, branch)
	}
}

// TestALocalRunThatDidNotPassPushesAndRunsTheProgramWithNoDraft is a local run whose session
// ended with exit 7 after it wrote a hand-off text: as a run from a run code does, qory pushes the
// branch and starts the program once, with the draft none.
func TestALocalRunThatDidNotPassPushesAndRunsTheProgramWithNoDraft(t *testing.T) {
	_, bare, started := localProgramCheckout(t)
	calls := passingCheck(t)
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		handsOffText(t)(spec)
		return &session.Result{ExitCode: 7}, nil
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 7 {
		t.Fatalf("the run ended with %v, want exit 7\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	if got := pushedBranches(t, bare); got != branch || *calls != 1 {
		t.Errorf("pushed %q after %d checks, want %s after 1", got, *calls, branch)
	}
	if got := strings.TrimSpace(readText(t, started)); got != "acme/app "+branch+" none 1" {
		t.Errorf("the program got %q, want the path, %s, the draft none and 1 pushed repository", got, branch)
	}
}

// TestALocalRunPushesAndSubmitsEachRepository is a local run with a further repository under
// .qory/repositories: each repository is checked and pushed once, and the program starts once for
// each, with that repository's path and branch.
func TestALocalRunPushesAndSubmitsEachRepository(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	further := filepath.Join(root, ".qory", "repositories", "other")
	if err := os.MkdirAll(further, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, further, "init", "--quiet", "--initial-branch=main")
	runGit(t, further, "config", "user.name", "Tester")
	runGit(t, further, "config", "user.email", "tester@example.com")
	writeFile(t, filepath.Join(further, "README.md"), "first\n")
	runGit(t, further, "add", "README.md")
	runGit(t, further, "commit", "--quiet", "-m", "first")
	otherBare := filepath.Join(tempDir(t), "other.git")
	runGit(t, further, "init", "--quiet", "--bare", "--initial-branch=main", otherBare)
	runGit(t, further, "remote", "add", "origin", "https://git.example.com/acme/other.git")
	routePush(t, "https://git.example.com/acme/other.git", otherBare)
	writeFile(t, started+".bares", bare+"\n"+otherBare+"\n")
	calls := passingCheck(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		handsOffText(t)(spec)
		writeFile(t, filepath.Join(spec.Dir, ".qory", "repositories", "other", "there.txt"), "there\n")
		in := filepath.Join(spec.Dir, ".qory", "repositories", "other")
		gitOutput(t, in, "add", "there.txt")
		gitOutput(t, in, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "Add there")
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	if a, b := pushedBranches(t, bare), pushedBranches(t, otherBare); a != branch || b != branch || *calls != 2 {
		t.Errorf("pushed %q and %q after %d checks, want %s twice after 2", a, b, *calls, branch)
	}
	got := strings.Split(strings.TrimSpace(readText(t, started)), "\n")
	if len(got) != 2 || got[0] != "acme/app "+branch+" open 2" || got[1] != "acme/other "+branch+" open 2" {
		t.Errorf("the program started as %q, want once for each repository, after both pushes", got)
	}
}

// TestALocalRunWithNoCheckNamesEachBranchWithItsRepository is a run of two repositories with
// commits, where a branch named qory appears in the second while the run goes on, so the run's
// commits land on a branch with another name there: the text has one Branch line for each repository, with the branch of that repository
// and its folder.
func TestALocalRunWithNoCheckNamesEachBranchWithItsRepository(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	further := filepath.Join(root, ".qory", "repositories", "other")
	if err := os.MkdirAll(further, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, further, "init", "--quiet", "--initial-branch=main")
	runGit(t, further, "config", "user.name", "Tester")
	runGit(t, further, "config", "user.email", "tester@example.com")
	writeFile(t, filepath.Join(further, "README.md"), "first\n")
	runGit(t, further, "add", "README.md")
	runGit(t, further, "commit", "--quiet", "-m", "first")
	runGit(t, further, "remote", "add", "origin", "https://git.example.com/acme/other.git")
	out := runLocal(t, func(spec session.SessionSpec) {
		handsOffText(t)(spec)
		// A branch named qory blocks the run's branch name in the second repository, after qory
		// chose the name at the start.
		runGit(t, further, "branch", "qory")
		writeFile(t, filepath.Join(spec.Dir, ".qory", "repositories", "other", "there.txt"), "there\n")
		in := filepath.Join(spec.Dir, ".qory", "repositories", "other")
		gitOutput(t, in, "add", "there.txt")
		gitOutput(t, in, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "Add there")
	})
	var lines []string
	for _, m := range regexp.MustCompile(`the run's commits are on (\S+) in (\S+)\.`).FindAllStringSubmatch(out, -1) {
		lines = append(lines, "  Branch: "+m[1]+" in "+m[2]+".")
	}
	if len(lines) != 2 || strings.HasPrefix(lines[0], "  Branch: qory/run-") == strings.HasPrefix(lines[1], "  Branch: qory/run-") {
		t.Fatalf("the output has the commit lines %q, want two with different branch names:\n%s", lines, out)
	}
	wants(t, out, "qory run: Push each branch by hand: the key and scope push checks of qory run are not registered.\n"+
		"  qory pushed nothing and opened no draft; open each draft by hand if you want one.\n"+strings.Join(lines, "\n")+"\n")
	if got := pushedBranches(t, bare); got != "" || timesStarted(t, started) != 0 {
		t.Errorf("pushed %q and started %d programs, want none", got, timesStarted(t, started))
	}
}

// TestALocalRunNeedsEveryPushCheck is a local run with a hand-off text, with no push check, with
// the check key only, and with the check scope only: each ends with the one line that says the
// checks are not available, and nothing is pushed or started. With both, qory pushes.
func TestALocalRunNeedsEveryPushCheck(t *testing.T) {
	names := cmd.PushChecksRequired()
	if len(names) != 2 || names[0] != "key" || names[1] != "scope" {
		t.Fatalf("the required checks are %v, want key and scope", names)
	}
	pass := func(context.Context, string, string) error { return nil }
	const line = "qory run: Push each branch by hand: the key and scope push checks of qory run are not registered."
	for _, c := range []struct {
		name  string
		given []string
		ok    bool
	}{{"none", nil, false}, {"key only", names[:1], false}, {"scope only", names[1:], false}, {"both", names, true}} {
		t.Run(c.name, func(t *testing.T) {
			_, bare, started := localProgramCheckout(t)
			for _, name := range c.given {
				t.Cleanup(cmd.RegisterPushCheck(name, pass))
			}
			agentInCopy(t, handsOffText(t))
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			pushed, started1 := pushedBranches(t, bare) != "", timesStarted(t, started)
			if c.ok {
				if strings.Contains(out, line) || !pushed || started1 != 1 {
					t.Errorf("pushed %v, started %d, want a push and 1 program:\n%s", pushed, started1, out)
				}
				return
			}
			if n := strings.Count(out, line); n != 1 || pushed || started1 != 0 {
				t.Errorf("the line is there %d times, pushed %v, started %d, want 1, false, 0:\n%s", n, pushed, started1, out)
			}
		})
	}
}

// TestACheckThatMovesTheBranchCannotChangeThePushedCommit is a push check that moves the branch to a
// new commit after it read the head: the push sends the commit that the checks saw, not the
// commit the branch name has by then.
func TestACheckThatMovesTheBranchCannotChangeThePushedCommit(t *testing.T) {
	_, bare, _ := localProgramCheckout(t)
	var checked string
	bothChecks(t, func(_ context.Context, dir, head string) error {
		checked = head
		branch := strings.TrimSpace(gitOutput(t, dir, "for-each-ref", "--points-at", head, "--format=%(refname:short)", "refs/heads/qory/"))
		moved := gitOutput(t, dir, "-c", "user.name=a", "-c", "user.email=a@example.com", "commit-tree", "-p", checked, "-m", "later", checked+"^{tree}")
		gitOutput(t, dir, "update-ref", "refs/heads/"+branch, moved)
		return nil
	})
	agentInCopy(t, handsOffText(t))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	if checked == "" {
		t.Fatalf("no check ran:\n%s", out)
	}
	if got := gitOutput(t, bare, "rev-parse", branch); got != checked {
		t.Errorf("the pushed commit is %s, want the checked %s", got, checked)
	}
}

// TestALocalRunPushesToThePushAddress is a remote origin whose push address differs from its fetch
// address: the program gets the system and path of the push address, and the push goes to it.
func TestALocalRunPushesToThePushAddress(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	pushBare := filepath.Join(tempDir(t), "pushed.git")
	runGit(t, root, "init", "--quiet", "--bare", "--initial-branch=main", pushBare)
	runGit(t, root, "config", "remote.origin.pushurl", "https://git.example.com/acme/pushed.git")
	routePush(t, "https://git.example.com/acme/pushed.git", pushBare)
	writeFile(t, started+".bares", pushBare+"\n")
	passingCheck(t)
	agentInCopy(t, handsOffText(t))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	if got := pushedBranches(t, pushBare); got != branch {
		t.Errorf("the push address has %q, want %s", got, branch)
	}
	if got := pushedBranches(t, bare); got != "" {
		t.Errorf("the fetch address has %q, want none", got)
	}
	if got := strings.TrimSpace(readText(t, started)); got != "acme/pushed "+branch+" open 1" {
		t.Errorf("the program got %q, want the path of the push address", got)
	}
}

// TestALocalRunWithTwoPushAddressesPushesNothing is a remote origin with two push addresses: qory
// pushes nothing, runs no program, and says which address count it found.
func TestALocalRunWithTwoPushAddressesPushesNothing(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	runGit(t, root, "config", "--add", "remote.origin.pushurl", "https://git.example.com/acme/app.git")
	runGit(t, root, "config", "--add", "remote.origin.pushurl", "https://git.example.com/acme/second.git")
	passingCheck(t)
	agentInCopy(t, handsOffText(t))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, pushURLsText(2))
	wants(t, out, pushURLsHow)
	if got := pushedBranches(t, bare); got != "" || timesStarted(t, started) != 0 {
		t.Errorf("pushed %q and started %d programs, want none", got, timesStarted(t, started))
	}
}

// pushURLsText is the text of a remote origin with n push addresses: the fix, the cause and the
// branch, then how to list the configured addresses and the two commands that leave one.
func pushURLsText(n int) string {
	return fmt.Sprintf("qory run: Remove the extra push addresses of remote origin, so it has one.\n"+
		"  The remote origin has %d push addresses, so qory pushed nothing and opened no draft.\n  Branch: qory/run-", n)
}

// pushURLsHow is the lines of the text of a remote origin with several push addresses that follow
// the branch.
const pushURLsHow = ".\n  List the configured addresses and the file of each with git config --show-origin --get-all remote.origin.pushurl, or with git config --show-origin --get-all remote.origin.url when that prints none.\n" +
	"  Do not use git remote get-url --push --all: it shows the addresses after the insteadOf rewrites of git.\n" +
	"  For each file that --show-origin shows (the path after file:), run git config --file <file> --unset-all remote.origin.pushurl in the top folder of the repository. Put a path with spaces in quotes, and edit a file by hand when git prints its path in double quotes. Exit status 5 means that file has no pushurl value. The system file usually needs administrator rights.\n" +
	"  If --show-origin shows command line: instead of a file, run unset GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS (fish: set -e GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS) in the shell that runs qory, then run qory again.\n" +
	"  Then run git remote set-url --push origin <address> with the address to keep.\n"

// TestALocalRunWithTwoUrlValuesPushesNothing is a remote origin with two url values and no pushurl:
// git has two push addresses then, and qory pushes nothing and gives the same text as for two
// pushurl values, whose two commands leave one push address in both cases.
func TestALocalRunWithTwoUrlValuesPushesNothing(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	runGit(t, root, "remote", "set-url", "--add", "origin", "https://git.example.com/acme/second.git")
	passingCheck(t)
	agentInCopy(t, handsOffText(t))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, pushURLsText(2))
	wants(t, out, pushURLsHow)
	if got := pushedBranches(t, bare); got != "" || timesStarted(t, started) != 0 {
		t.Errorf("pushed %q and started %d programs, want none", got, timesStarted(t, started))
	}
}

// draftLines are the result lines of a program that opened a draft on the repository @PATH@ of
// the forge git.example.com.
const draftLines = "draft_number 7\ndraft_url https://git.example.com/@PATH@/pull/7\ndraft_title Add a file\ndraft_state draft\ndraft_action opened\n"

// runLocal runs a local run with the agent and returns the output, failing the test on an error.
func runLocal(t *testing.T, agent func(session.SessionSpec)) string {
	t.Helper()
	agentInCopy(t, agent)
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out
}

// lastLines is the last n lines of out, without trailing blank lines.
func lastLines(out string, n int) []string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return lines[max(0, len(lines)-n):]
}

// TestALocalRunPrintsTheAddressOfItsDraftAsItsLastLine is a program that returns the five draft
// lines: the output ends with `Draft: <address>`, once, after the line with the record.
func TestALocalRunPrintsTheAddressOfItsDraftAsItsLastLine(t *testing.T) {
	_, _, started := localProgramCheckout(t)
	writeFile(t, started+".out", draftLines)
	passingCheck(t)
	out := runLocal(t, handsOffText(t))
	const want = "Draft: https://git.example.com/acme/app/pull/7"
	if got := lastLines(out, 1); got[0] != want || strings.Count(out, "Draft: ") != 1 {
		t.Errorf("the last line is %q with %d draft lines, want %q once:\n%s", got, strings.Count(out, "Draft: "), want, out)
	}
	if strings.Index(out, "the record is in") > strings.Index(out, want) {
		t.Errorf("the draft line is before the record line:\n%s", out)
	}
}

// TestALocalRunPrintsNoDraftLineForNoDraft is a program that returns draft lines for a run that
// has no hand-off text, so the draft is none and qory leaves the lines out, and a program that
// fails, and one whose address has a control character, which the contract's rule refuses:
// none of them prints a draft line.
func TestALocalRunPrintsNoDraftLineForNoDraft(t *testing.T) {
	noText := func(spec session.SessionSpec) {
		makeProgramRunnable(t)
		writeFile(t, filepath.Join(spec.Dir, "committed.txt"), "committed\n")
		gitOutput(t, spec.Dir, "add", "committed.txt")
		gitOutput(t, spec.Dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "Add a file")
	}
	for _, c := range []struct {
		name  string
		lines string
		exit  string
		agent func(session.SessionSpec)
	}{
		{"draft none", draftLines, "0", noText},
		{"a program that fails", "", "1", handsOffText(t)},
		{"an address with a control character", strings.Replace(draftLines, "/pull/7", "/pull/7\x01", 1), "0", handsOffText(t)},
		{"an address on another host", strings.Replace(draftLines, "git.example.com", "evil.example.net", 1), "0", handsOffText(t)},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, started := localProgramCheckout(t)
			writeFile(t, started+".out", c.lines)
			writeFile(t, started+".exit", c.exit+"\n")
			passingCheck(t)
			out := runLocal(t, c.agent)
			if strings.Contains(out, "Draft: ") {
				t.Errorf("the output has a draft line:\n%s", out)
			}
		})
	}
}

// TestALocalRunPrintsTheAddressOfADraftThatTheProgramReportedBeforeItFailed is a program that
// prints the five draft lines and exits with 1: the contract records the draft with that exit, a
// draft exists on the forge, so its address is printed.
func TestALocalRunPrintsTheAddressOfADraftThatTheProgramReportedBeforeItFailed(t *testing.T) {
	_, _, started := localProgramCheckout(t)
	writeFile(t, started+".out", draftLines)
	writeFile(t, started+".exit", "1\n")
	passingCheck(t)
	out := runLocal(t, handsOffText(t))
	if got := lastLines(out, 1); got[0] != "Draft: https://git.example.com/acme/app/pull/7" {
		t.Errorf("the last line is %q, want the draft's address:\n%s", got, out)
	}
}

// TestALocalRunPrintsADraftLineForEachRepositoryInOrder is a local run of two repositories whose
// program returns a draft for each: the output ends with their two lines, the first repository's
// first.
func TestALocalRunPrintsADraftLineForEachRepositoryInOrder(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	further := filepath.Join(root, ".qory", "repositories", "other")
	if err := os.MkdirAll(further, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, further, "init", "--quiet", "--initial-branch=main")
	runGit(t, further, "config", "user.name", "Tester")
	runGit(t, further, "config", "user.email", "tester@example.com")
	writeFile(t, filepath.Join(further, "README.md"), "first\n")
	runGit(t, further, "add", "README.md")
	runGit(t, further, "commit", "--quiet", "-m", "first")
	otherBare := filepath.Join(tempDir(t), "other.git")
	runGit(t, further, "init", "--quiet", "--bare", "--initial-branch=main", otherBare)
	runGit(t, further, "remote", "add", "origin", "https://git.example.com/acme/other.git")
	routePush(t, "https://git.example.com/acme/other.git", otherBare)
	writeFile(t, started+".bares", bare+"\n"+otherBare+"\n")
	writeFile(t, started+".out", draftLines)
	passingCheck(t)
	out := runLocal(t, func(spec session.SessionSpec) {
		handsOffText(t)(spec)
		writeFile(t, filepath.Join(spec.Dir, ".qory", "repositories", "other", "there.txt"), "there\n")
		in := filepath.Join(spec.Dir, ".qory", "repositories", "other")
		gitOutput(t, in, "add", "there.txt")
		gitOutput(t, in, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "Add there")
	})
	want := []string{"Draft: https://git.example.com/acme/app/pull/7", "Draft: https://git.example.com/acme/other/pull/7"}
	if got := lastLines(out, 2); !slices.Equal(got, want) {
		t.Errorf("the last lines are %q, want %q:\n%s", got, want, out)
	}
}

// TestALocalRunWithAnAddressThatIsNotAForgePushesNothing is a remote origin that is a file address:
// qory pushes nothing, runs no program, and the line has no part of the address.
func TestALocalRunWithAnAddressThatIsNotAForgePushesNothing(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	runGit(t, root, "config", "remote.origin.pushurl", "file://"+bare+"/needle")
	passingCheck(t)
	out := runLocal(t, handsOffText(t))
	wants(t, out, "qory run: Set remote origin to a forge repository address, then run again; qory pushed nothing.\n  qory opened no draft.")
	lacks(t, out, "needle")
	if got := pushedBranches(t, bare); got != "" || timesStarted(t, started) != 0 {
		t.Errorf("pushed %q and started %d programs, want none", got, timesStarted(t, started))
	}
}

// TestALocalRunWithNoRemoteOriginPushesNothingAndSaysSo is a repository that lost its remote origin
// after the run began: reading the push address fails before any push, so the text says qory
// pushed nothing and opened no draft, and says to fix the cause and run again.
func TestALocalRunWithNoRemoteOriginPushesNothingAndSaysSo(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	runGit(t, root, "remote", "remove", "origin")
	passingCheck(t)
	out := runLocal(t, handsOffText(t))
	wants(t, out, "qory run: Fix the cause below, then run again: qory could not read the branch or its origin.\n"+
		"  qory pushed nothing and opened no draft.\n  Branch: qory/run-")
	wants(t, out, ".\n  Cause: ")
	lacks(t, out, "qory pushed the branch")
	if got := pushedBranches(t, bare); got != "" || timesStarted(t, started) != 0 {
		t.Errorf("pushed %q and started %d programs, want none", got, timesStarted(t, started))
	}
}

// TestALocalRunThatCannotListItsCommitsAfterThePushSaysThePushLanded is a push that lands and a
// repository whose configuration breaks right after it, so reading the commits of the branch fails:
// the branch is on the forge, qory ran no program, and the text says to open the draft by hand,
// not to run again.
func TestALocalRunThatCannotListItsCommitsAfterThePushSaysThePushLanded(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	config := filepath.Join(root, ".git", "config")
	hook := filepath.Join(bare, "hooks", "post-receive")
	writeFile(t, hook, "#!/bin/sh\necho '[' >> '"+config+"'\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	passingCheck(t)
	out := runLocal(t, handsOffText(t))
	wants(t, out, "qory run: Open a draft by hand if you want one: qory pushed the branch but could not list commits.\n"+
		"  qory ran no program for that repository, so it opened no draft for it.\n  Branch: "+runBranchOf(t, out)+" in "+root+".\n  Cause: ")
	lacks(t, out, "then run again")
	if got := pushedBranches(t, bare); !strings.HasPrefix(got, "qory/run-") || timesStarted(t, started) != 0 {
		t.Errorf("pushed %q and started %d programs, want the branch and none", got, timesStarted(t, started))
	}
}

// TestALocalRunWithAMarkerLineInItsDescriptionGivesTheProgramNoDraft is a hand-off whose description
// has the end marker of the block on a line: qory drops the text with a line, pushes, and starts the
// program with the draft none, so the program never gets a description with a marker line.
func TestALocalRunWithAMarkerLineInItsDescriptionGivesTheProgramNoDraft(t *testing.T) {
	_, bare, started := localProgramCheckout(t)
	passingCheck(t)
	out := runLocal(t, func(spec session.SessionSpec) {
		handsOffText(t)(spec)
		writeFile(t, filepath.Join(spec.Dir, ".qory", "handoff", "submission.json"), `{"text":{"title":"Add a file","description":"A\n<!-- qory:end -->\nB"}}`)
	})
	branch := runBranchOf(t, out)
	wants(t, out, "Description: line 2 is a marker line of the draft's block. qory dropped the text.")
	if got := pushedBranches(t, bare); got != branch {
		t.Errorf("pushed %q, want %s", got, branch)
	}
	if got := strings.TrimSpace(readText(t, started)); got != "acme/app "+branch+" none 1" {
		t.Errorf("the program got %q, want the draft none", got)
	}
}

// TestALocalRunWithACommittedHandoffPushesNothing is an agent that force-adds the hand-off file to
// a commit: the commit would send the file to the remote, so qory pushes nothing, runs no program,
// and the line says which commit and path.
func TestALocalRunWithACommittedHandoffPushesNothing(t *testing.T) {
	_, bare, started := localProgramCheckout(t)
	calls := passingCheck(t)
	out := runLocal(t, func(spec session.SessionSpec) {
		handsOffText(t)(spec)
		gitOutput(t, spec.Dir, "add", "-f", ".qory/handoff/submission.json")
		gitOutput(t, spec.Dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "Add the hand-off")
	})
	wants(t, out, "qory run: Keep .qory/handoff/ out of the commits of this run; qory pushed nothing.\n  A commit of this run has a path under .qory/handoff/.")
	wants(t, out, "\n  Repository git.example.com/acme/app.\n  Commit ")
	wants(t, out, "\n  Path .qory/handoff/submission.json.")
	if got := pushedBranches(t, bare); got != "" || timesStarted(t, started) != 0 || *calls != 0 {
		t.Errorf("pushed %q, started %d programs and ran %d checks, want none", got, timesStarted(t, started), *calls)
	}
}

// TestAGitAddAllOfTheAgentLeavesTheHandoffOut is an agent that stages everything with git add -A
// and commits with git commit -a after it wrote the hand-off: the file is in no commit, so the run
// pushes and the program starts with the draft open.
func TestAGitAddAllOfTheAgentLeavesTheHandoffOut(t *testing.T) {
	_, bare, started := localProgramCheckout(t)
	passingCheck(t)
	var excluded string
	out := runLocal(t, func(spec session.SessionSpec) {
		makeProgramRunnable(t)
		writeFile(t, filepath.Join(spec.Dir, ".qory", "handoff", "submission.json"), `{"text":{"title":"Add a file","description":"A file."}}`)
		// The copy's .qory is ignored as a whole already; the line for the hand-off folder is the
		// one that qory adds for this file.
		path := gitOutput(t, spec.Dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
		excluded = readText(t, strings.TrimSpace(path))
		writeFile(t, filepath.Join(spec.Dir, "committed.txt"), "committed\n")
		gitOutput(t, spec.Dir, "add", "-A")
		gitOutput(t, spec.Dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-a", "-m", "Add a file")
	})
	branch := runBranchOf(t, out)
	if !slices.Contains(strings.Split(excluded, "\n"), "/.qory/handoff/") {
		t.Errorf("the copy's info/exclude is %q, want the line /.qory/handoff/", excluded)
	}
	if files := gitOutput(t, bare, "ls-tree", "-r", "--name-only", branch); strings.Contains(files, ".qory") || !strings.Contains(files, "committed.txt") {
		t.Errorf("the pushed branch has the files %q, want committed.txt and nothing under .qory", files)
	}
	if got := strings.TrimSpace(readText(t, started)); got != "acme/app "+branch+" open 1" {
		t.Errorf("the program got %q, want the draft open\n%s", got, out)
	}
}

// TestALocalRunWhoseSecondRepositoryIsRefusedPushesNothingAtAll is a run of two repositories with
// commits, where the check passes for the first and refuses the second: no repository is pushed,
// and no program starts, because every check comes before the first push.
func TestALocalRunWhoseSecondRepositoryIsRefusedPushesNothingAtAll(t *testing.T) {
	root, bare, started := localProgramCheckout(t)
	further := filepath.Join(root, ".qory", "repositories", "other")
	if err := os.MkdirAll(further, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, further, "init", "--quiet", "--initial-branch=main")
	runGit(t, further, "config", "user.name", "Tester")
	runGit(t, further, "config", "user.email", "tester@example.com")
	writeFile(t, filepath.Join(further, "README.md"), "first\n")
	runGit(t, further, "add", "README.md")
	runGit(t, further, "commit", "--quiet", "-m", "first")
	otherBare := filepath.Join(tempDir(t), "other.git")
	runGit(t, further, "init", "--quiet", "--bare", "--initial-branch=main", otherBare)
	runGit(t, further, "remote", "add", "origin", "https://git.example.com/acme/other.git")
	routePush(t, "https://git.example.com/acme/other.git", otherBare)
	writeFile(t, started+".bares", bare+"\n"+otherBare+"\n")
	calls := bothChecks(t, func(context.Context, string, string) error { return nil })
	names := cmd.PushChecksRequired()
	count := 0
	t.Cleanup(cmd.RegisterPushCheck(names[0], func(context.Context, string, string) error {
		count++
		if count == 2 {
			return errors.New("a key is in a commit of the second repository")
		}
		return nil
	}))
	_ = calls
	out := runLocal(t, func(spec session.SessionSpec) {
		handsOffText(t)(spec)
		writeFile(t, filepath.Join(spec.Dir, ".qory", "repositories", "other", "there.txt"), "there\n")
		in := filepath.Join(spec.Dir, ".qory", "repositories", "other")
		gitOutput(t, in, "add", "there.txt")
		gitOutput(t, in, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "Add there")
	})
	wants(t, out, "a key is in a commit of the second repository")
	if a, b := pushedBranches(t, bare), pushedBranches(t, otherBare); a != "" || b != "" || count != 2 || timesStarted(t, started) != 0 {
		t.Errorf("pushed %q and %q after %d checks and %d programs, want nothing after 2 checks", a, b, count, timesStarted(t, started))
	}
}

// rewriteTarget is a bare repository at <dir>/acme/app that a git rewrite of an address would
// reach, and the <dir>/ that a rule maps to it.
func rewriteTarget(t *testing.T) (bare, base string) {
	t.Helper()
	base = tempDir(t) + "/rewrite/"
	bare = filepath.Join(base, "acme", "app")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, bare, "init", "--quiet", "--bare", "--initial-branch=main")
	return bare, base
}

// failFast makes a git push to an address where nothing listens fail at once: no proxy, whatever
// the person's environment has, and a stop after 2 seconds of no data.
func failFast(t *testing.T, root string) {
	t.Helper()
	runGit(t, root, "config", "http.proxy", "")
	t.Setenv("GIT_HTTP_LOW_SPEED_LIMIT", "1")
	t.Setenv("GIT_HTTP_LOW_SPEED_TIME", "2")
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, "")
	}
}

// TestAPushIsNotRewrittenAgainByInsteadOf is a remote origin written as gh:acme/app, with the rule
// gh: to http://127.0.0.1:1/ that the checks and the program see, and a second rule from that
// address to a repository on disk. git applies a rule once to the address of a remote, so the
// push goes to http://127.0.0.1:1/acme/app, where nothing listens, and fails; it never goes
// to the repository on disk, as a push to the address as an argument would, which git rewrites again.
func TestAPushIsNotRewrittenAgainByInsteadOf(t *testing.T) {
	root, _, started := localProgramCheckout(t)
	elsewhere, base := rewriteTarget(t)
	failFast(t, root)
	runGit(t, root, "remote", "set-url", "origin", "gh:acme/app")
	runGit(t, root, "config", "url.http://127.0.0.1:1/.insteadOf", "gh:")
	runGit(t, root, "config", "url."+base+".insteadOf", "http://127.0.0.1:1/")
	passingCheck(t)
	out := runLocal(t, handsOffText(t))
	wants(t, out, "qory run: Fix the cause below, then run again: the push failed, so qory opened no draft.")
	if got := gitOutput(t, elsewhere, "for-each-ref", "refs/heads/"); got != "" {
		t.Errorf("the push reached the repository of the second rewrite: %q", got)
	}
	if n := timesStarted(t, started); n != 0 {
		t.Errorf("the program started %d times, want 0", n)
	}
}

// TestAPushIsNotRewrittenAgainByPushInsteadOf is the same for the push address of a remote: the
// pushurl http://127.0.0.1:2/acme/app with a pushInsteadOf rule to a repository on disk. The checks
// see the address, and the push goes to it, not to the repository on disk.
func TestAPushIsNotRewrittenAgainByPushInsteadOf(t *testing.T) {
	root, _, started := localProgramCheckout(t)
	elsewhere, base := rewriteTarget(t)
	failFast(t, root)
	runGit(t, root, "remote", "set-url", "--push", "origin", "http://127.0.0.1:2/acme/app")
	runGit(t, root, "config", "url."+base+".pushInsteadOf", "http://127.0.0.1:2/")
	var seen []string
	bothChecks(t, func(_ context.Context, dir, head string) error {
		seen = append(seen, gitOutput(t, dir, "remote", "get-url", "--push", "origin"))
		return nil
	})
	out := runLocal(t, handsOffText(t))
	wants(t, out, "qory run: Fix the cause below, then run again: the push failed, so qory opened no draft.")
	if len(seen) != 1 || strings.TrimSpace(seen[0]) != "http://127.0.0.1:2/acme/app" {
		t.Errorf("the push address is %q, want http://127.0.0.1:2/acme/app", seen)
	}
	if got := gitOutput(t, elsewhere, "for-each-ref", "refs/heads/"); got != "" {
		t.Errorf("the push reached the repository of the pushInsteadOf rule: %q", got)
	}
	if n := timesStarted(t, started); n != 0 {
		t.Errorf("the program started %d times, want 0", n)
	}
}

// TestARunWithARunKeyAndNoRunCodeNeverPushesLocally is a run with a run key and no run code of a
// harness whose module declares a submit program, with both push checks registered: qory pushes
// nothing, starts no program, and prints no line of the local submit step, because that step is
// for a run with neither a run code nor a run key.
func TestARunWithARunKeyAndNoRunCodeNeverPushesLocally(t *testing.T) {
	_, bare, started := localProgramCheckout(t)
	calls := passingCheck(t)
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	_, restore := cmd.OpenNowhere()
	defer restore()
	agentInCopy(t, handsOffText(t))
	out, _ := run(t, "run", "claude", "--run-key-fd", descriptor(t, key), "--", "-p", "hi")
	if got := pushedBranches(t, bare); got != "" || timesStarted(t, started) != 0 || *calls != 0 {
		t.Errorf("pushed %q, started %d programs and ran %d checks, want none\n%s", got, timesStarted(t, started), *calls, out)
	}
	for _, line := range []string{"Draft: ", "qory pushed nothing", "the push failed, so qory opened no draft", "a push check refused the push"} {
		if strings.Contains(out, line) {
			t.Errorf("the output has %q:\n%s", line, out)
		}
	}
}
