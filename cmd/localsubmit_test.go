package cmd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	writeFile(t, filepath.Join(root, "modules/forge/bin/open-draft"), "#!/bin/sh\necho \"$QORY_SUBMIT_PATH $QORY_SUBMIT_BRANCH $QORY_SUBMIT_DRAFT\" >> "+started+"\nexit 0\n")
	composedForFake(t, root, fakeRuntime(t))
	runGit(t, root, "add", "README.md", "qory.yaml", "modules")
	runGit(t, root, "commit", "--quiet", "-m", "first")
	bare = filepath.Join(tempDir(t), "origin.git")
	runGit(t, root, "init", "--quiet", "--bare", "--initial-branch=main", bare)
	runGit(t, root, "config", "url."+bare+".insteadOf", "https://git.example.com/acme/app.git")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	return root, bare, started
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
	t.Cleanup(cmd.RegisterPushCheck(func(_ context.Context, dir, branch string) error {
		if got := pushedBranches(t, bare); got != "" {
			t.Errorf("the bare repository has %q before the check ends", got)
		}
		checked = append(checked, dir+" "+branch)
		return nil
	}))
	agentInCopy(t, handsOffText(t))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	if !strings.HasPrefix(branch, "qory/run-") {
		t.Errorf("the branch is %s, want qory/run-<run id>", branch)
	}
	if len(checked) != 1 || !strings.HasSuffix(checked[0], " "+branch) {
		t.Errorf("the checks ran on %v, want once on %s", checked, branch)
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
	if got := strings.TrimSpace(readText(t, started)); got != "acme/app "+branch+" open" {
		t.Errorf("the program got %q, want the path, %s and the draft open", got, branch)
	}
}

// TestALocalRunWithARefusingCheckPushesNothing is a local run with a hand-off text and a push
// check that refuses: nothing is pushed, no program starts, and the output says why.
func TestALocalRunWithARefusingCheckPushesNothing(t *testing.T) {
	_, bare, started := localProgramCheckout(t)
	t.Cleanup(cmd.RegisterPushCheck(func(context.Context, string, string) error { return errors.New("a key is in a commit") }))
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
	wants(t, out, "A push check refused the push, so qory pushed nothing and opened no draft.")
	wants(t, out, "a key is in a commit")
}

// TestALocalRunWithNoCheckPushesNothing is a local run with a hand-off text and no push check
// registered: the step fails closed. The output has the one line that says so, of at most 100
// characters, and nothing is pushed or started.
func TestALocalRunWithNoCheckPushesNothing(t *testing.T) {
	_, bare, started := localProgramCheckout(t)
	agentInCopy(t, handsOffText(t))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	const line = "qory run: The push checks are not available yet, so qory pushed nothing and opened no draft."
	if len(line) > 100 {
		t.Fatalf("the line has %d characters", len(line))
	}
	if n := strings.Count(out, line); n != 1 {
		t.Errorf("the output has the line %d times, want 1:\n%s", n, out)
	}
	if got := pushedBranches(t, bare); got != "" {
		t.Errorf("the bare repository has %q, want none", got)
	}
	if n := timesStarted(t, started); n != 0 {
		t.Errorf("the program started %d times, want 0", n)
	}
}

// passingCheck registers a push check that passes and counts its calls.
func passingCheck(t *testing.T) *int {
	t.Helper()
	calls := new(int)
	t.Cleanup(cmd.RegisterPushCheck(func(context.Context, string, string) error { *calls++; return nil }))
	return calls
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
	if got := strings.TrimSpace(readText(t, started)); got != "acme/app "+branch+" none" {
		t.Errorf("the program got %q, want the path, %s and the draft none", got, branch)
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
	if got := strings.TrimSpace(readText(t, started)); got != "acme/app "+branch+" none" {
		t.Errorf("the program got %q, want the path, %s and the draft none", got, branch)
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
	runGit(t, further, "config", "url."+otherBare+".insteadOf", "https://git.example.com/acme/other.git")
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
	if len(got) != 2 || got[0] != "acme/app "+branch+" open" || got[1] != "acme/other "+branch+" open" {
		t.Errorf("the program started as %q, want once for each repository", got)
	}
}
