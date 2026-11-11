package cmd_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
	"github.com/qoryai/forager/wall"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/runcopy"
)

// copyCheckout is a checkout with the two-modules fixture in its first commit, composed for
// the fake runtime, with a wall whose docker stands in for the engine.
func copyCheckout(t *testing.T) string {
	t.Helper()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, "README.md"), "first\n")
	writeFile(t, filepath.Join(root, "gone.txt"), "first\n")
	composedForFake(t, root, fakeRuntime(t))
	runGit(t, root, "add", "README.md", "gone.txt", "qory.yaml", "modules")
	runGit(t, root, "commit", "--quiet", "-m", "first")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	return root
}

// agentInCopy makes the join of the agent's session run agent in the session's folder, with
// the session's spec, and end as a session that succeeded does.
func agentInCopy(t *testing.T, agent func(spec session.SessionSpec)) {
	t.Helper()
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		agent(spec)
		return &session.Result{}, nil
	}))
}

// copyWork is the folder of the last run's copy in lines, the fake docker's log.
func copyWork(t *testing.T, lines string) string {
	t.Helper()
	all := regexp.MustCompile(`src=([^,]*/qory/runs/[^/,]+/work),`).FindAllStringSubmatch(lines, -1)
	if all == nil {
		t.Fatalf("no bind of a copy in:\n%s", lines)
	}
	return all[len(all)-1][1]
}

// gitOutput is the output of git with args in dir.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// runBranchOf is the branch that the result line of out contains.
func runBranchOf(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`the run's commits are on (qory[/-]run-\S+) in `).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the output has no line with the run's branch:\n%s", out)
	}
	return m[1]
}

// runRefsOf is the prefix of the run's other refs in the line of out that lists them, as it says it
// twice: in the text and in the command that lists them.
func runRefsOf(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`refs are under (refs/qory/run-[^ :]+)(?: in [^:]+)?: list them with git for-each-ref (refs/qory/run-\S+)\.`).FindStringSubmatch(out)
	if m == nil || m[1] != m[2] {
		t.Fatalf("the output has no line with the run's other refs:\n%s", out)
	}
	return m[1]
}

// TestAWalledWriteRunBindsACopy is a walled write run: its session starts in a copy under
// qory's cache folder, no mount is the person's checkout or lies in it, and the copy is gone
// once its commits came back.
func TestAWalledWriteRunBindsACopy(t *testing.T) {
	root := copyCheckout(t)
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	var agentDir string
	var specs []session.SessionSpec
	agentInCopy(t, func(spec session.SessionSpec) {
		agentDir = spec.Dir
		specs = append(specs, spec)
		if _, err := os.Stat(filepath.Join(spec.Dir, ".git")); err != nil {
			t.Errorf("the copy has no .git: %v", err)
		}
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(specs) != 1 {
		t.Fatalf("%d agent sessions, want 1", len(specs))
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(agentDir, filepath.Join(cache, "qory", "runs")+string(filepath.Separator)) {
		t.Errorf("the session starts in %s, want a folder under qory/runs in %s", agentDir, cache)
	}
	for _, m := range specs[0].Mounts {
		resolved := m.Path
		if r, err := filepath.EvalSymlinks(m.Path); err == nil {
			resolved = r
		}
		if resolved == real || strings.HasPrefix(resolved, real+string(filepath.Separator)) {
			t.Errorf("the session has the mount %+v of the person's checkout %s", m, real)
		}
	}
	if !slices.ContainsFunc(specs[0].Mounts, func(m wall.Mount) bool { return m.Path == agentDir && !m.ReadOnly }) {
		t.Errorf("the session's mounts %+v have no read-write mount of the copy", specs[0].Mounts)
	}
	if !slices.Contains(specs[0].Written, filepath.Join(cache, "qory", "runs")) {
		t.Errorf("Written is %v, want qory/runs in %s", specs[0].Written, cache)
	}
	if _, err := os.Stat(agentDir); err == nil {
		t.Errorf("the copy %s stays after the run", agentDir)
	}
}

// TestAWalledWriteRunFromARunCodeBindsTheCheckout is a walled write run from a run code, the way a
// pool starts a run: its session starts in the checkout with no copy, and the push step commits
// and pushes the agent's work from the checkout.
func TestAWalledWriteRunFromARunCodeBindsTheCheckout(t *testing.T) {
	p := walledPush(t)
	var dir string
	var mounts []wall.Mount
	p.mountsOf = func(spec session.SessionSpec) { dir, mounts = spec.Dir, spec.Mounts }
	r := p.run(t, writesAndCommits(t), nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	real, err := filepath.EvalSymlinks(p.h.dir)
	if err != nil {
		t.Fatal(err)
	}
	if dir != p.h.dir && dir != real {
		t.Errorf("the session starts in %s, want the checkout %s", dir, p.h.dir)
	}
	runs := filepath.Join("qory", "runs")
	for _, m := range mounts {
		if strings.Contains(m.Path, runs) {
			t.Errorf("the session has the mount %+v of a copy", m)
		}
	}
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(filepath.Join(base, runs)); err == nil && len(entries) > 0 {
		t.Errorf("the run made a copy: %v", entries)
	}
	head := p.branchHead(t)
	if head == "" {
		t.Fatalf("the forge has no branch\n%s", r.out)
	}
	if got := gitOut(t, p.h.dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("the checkout is at %s, the forge's branch at %s", got, head)
	}
	tree := gitOutput(t, p.bare, "ls-tree", "-r", "--name-only", head)
	wants(t, tree, "committed.txt", "left.txt")
}

// TestTheRunsCommitsLandOnARunBranch is an agent that commits one change and leaves another:
// both are on qory/run-<run id>, and the person's checked-out branch and files stay.
func TestTheRunsCommitsLandOnARunBranch(t *testing.T) {
	root := copyCheckout(t)
	head := gitOutput(t, root, "rev-parse", "HEAD")
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "committed.txt"), "committed\n")
		gitOutput(t, spec.Dir, "add", "committed.txt")
		gitOutput(t, spec.Dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "the agent's commit")
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	if got := gitOutput(t, root, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s, want %s", got, head)
	}
	if got := gitOutput(t, root, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("the checked-out branch is %s, want main", got)
	}
	for _, name := range []string{"committed.txt", "left.txt"} {
		gitOutput(t, root, "cat-file", "-e", branch+":"+name)
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s is in the person's checkout", name)
		}
	}
	lacks(t, out, "They change these")
}

// commitsInCopy makes the agent of the next run commit name in its copy.
func commitsInCopy(t *testing.T, name string) {
	t.Helper()
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, name), name+"\n")
		gitOutput(t, spec.Dir, "add", name)
		gitOutput(t, spec.Dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "the agent's commit")
	})
}

// copyRunID is the run id of the tests of the run's branch name.
const copyRunID = "0193a4b2-7c1d-4e8f-9a0b-1c2d3e4f5a6b"

// TestTheRunsBranchGetsAFreeName is a person's repository where a branch has or blocks the
// run's branch name: the commits land on the next free name, and the person's branches stay.
func TestTheRunsBranchGetsAFreeName(t *testing.T) {
	for _, c := range []struct {
		name     string
		branches []string
		want     string
	}{
		{"a branch with the name", []string{"qory/run-" + copyRunID}, "qory/run-" + copyRunID + "-2"},
		{"a branch below the name", []string{"qory/run-" + copyRunID + "/x", "qory/run-" + copyRunID + "-2"}, "qory/run-" + copyRunID + "-3"},
		{"a branch qory", []string{"qory"}, "qory-run-" + copyRunID},
		{"a branch qory and the other name", []string{"qory", "qory-run-" + copyRunID}, "qory-run-" + copyRunID + "-2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := copyCheckout(t)
			head := gitOutput(t, root, "rev-parse", "HEAD")
			for _, b := range c.branches {
				gitOutput(t, root, "branch", b, head)
			}
			commitsInCopy(t, "committed.txt")
			out, err := run(t, "run", "--run-id", copyRunID, "claude", "--", "-p", "hi")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if got := runBranchOf(t, out); got != c.want {
				t.Errorf("the commits are on %s, want %s:\n%s", got, c.want, out)
			}
			gitOutput(t, root, "cat-file", "-e", c.want+":committed.txt")
			for _, b := range c.branches {
				if got := gitOutput(t, root, "rev-parse", "refs/heads/"+b); got != head {
					t.Errorf("%s moved to %s", b, got)
				}
			}
		})
	}
}

// TestAReusedRunIDGetsTheNextName is two runs with the same --run-id, with the first run's
// record removed between them, as a run in another worktree of the repository has its record
// elsewhere: the second run's commits land on qory/run-<run id>-2, and the first run's branch
// stays.
func TestAReusedRunIDGetsTheNextName(t *testing.T) {
	root := copyCheckout(t)
	commitsInCopy(t, "first.txt")
	out, err := run(t, "run", "--run-id", copyRunID, "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	first := runBranchOf(t, out)
	if first != "qory/run-"+copyRunID {
		t.Errorf("the first run's commits are on %s", first)
	}
	if err := os.RemoveAll(filepath.Join(runsDir(t, root), copyRunID)); err != nil {
		t.Fatal(err)
	}
	commitsInCopy(t, "second.txt")
	out, err = run(t, "run", "--run-id", copyRunID, "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := runBranchOf(t, out); got != first+"-2" {
		t.Errorf("the second run's commits are on %s, want %s-2", got, first)
	}
	gitOutput(t, root, "cat-file", "-e", first+":first.txt")
	gitOutput(t, root, "cat-file", "-e", first+"-2:second.txt")
}

// TestANameTakenDuringTheRunGetsTheNextName is a branch with the run's branch name that the
// person makes while the agent runs: the commits land on the next free name.
func TestANameTakenDuringTheRunGetsTheNextName(t *testing.T) {
	root := copyCheckout(t)
	head := gitOutput(t, root, "rev-parse", "HEAD")
	name := "qory/run-" + copyRunID
	agentInCopy(t, func(spec session.SessionSpec) {
		gitOutput(t, root, "branch", name, head)
		writeFile(t, filepath.Join(spec.Dir, "committed.txt"), "committed\n")
	})
	out, err := run(t, "run", "--run-id", copyRunID, "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := runBranchOf(t, out); got != name+"-2" {
		t.Errorf("the commits are on %s, want %s-2", got, name)
	}
	if got := gitOutput(t, root, "rev-parse", "refs/heads/"+name); got != head {
		t.Errorf("%s moved to %s", name, got)
	}
}

// TestARunWithNoCommitSaysSo is an agent that leaves nothing: no branch, and a line that
// needs no action.
func TestARunWithNoCommitSaysSo(t *testing.T) {
	root := copyCheckout(t)
	agentInCopy(t, func(session.SessionSpec) {})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "qory run: "+runcopy.ErrNoBundle.Error())
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
}

// TestTheResultLineListsAChangedSetting is an agent that changes a qory setting that qory
// acts on: the result line has its key.
func TestTheResultLineListsAChangedSetting(t *testing.T) {
	root := copyCheckout(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		file := filepath.Join(spec.Dir, "qory.yaml")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "harness:\n") {
			t.Fatalf("qory.yaml has no harness section:\n%s", data)
		}
		writeFile(t, file, strings.Replace(string(data), "harness:\n", "harness:\n  launch:\n    claude:\n      command: /opt/planted\n", 1))
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	wants(t, out, "the run's commits are on "+branch+" in ", " They change these qory settings, links or repository folders: harness.launch.claude.command. Check them before you check out the branch.")
	gitOutput(t, root, "cat-file", "-e", branch+":qory.yaml")
}

// TestAReadWriteMountStopsAWriteRun is a read-write mount, from --mount and from wall.mounts,
// in a run that may write: the run stops with exit 2 and the line starts with the fix. A
// read-only mount passes.
func TestAReadWriteMountStopsAWriteRun(t *testing.T) {
	copyCheckout(t)
	mount := tempDir(t)
	want := "make the mount " + mount + " read-only, with :ro on --mount or in wall.mounts: "
	joins := 0
	agentInCopy(t, func(session.SessionSpec) { joins++ })
	_, err := run(t, "run", "claude", "--mount", mount, "--", "-p", "hi")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("--mount: %v (exit %d), want the line %q", err, cmd.ExitCode(err), want)
	}
	if joins != 0 {
		t.Errorf("%d sessions joined the run, want none", joins)
	}
	if out, err := run(t, "run", "claude", "--mount", mount+":ro", "--", "-p", "hi"); err != nil {
		t.Errorf("a read-only mount: %v\n%s", err, out)
	}
	file := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, string(data)+"  mounts: ["+mount+"]\n")
	_, err = run(t, "run", "claude", "--", "-p", "hi")
	if err == nil || cmd.ExitCode(err) != cmd.ExitInput || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("wall.mounts: %v (exit %d), want the line %q", err, cmd.ExitCode(err), want)
	}
}

// TestAPlainFolderGetsItsFilesBack is a walled run in a folder that is not a repository: the
// files the run changed, added and deleted come back, a link stays out, and the line lists it.
func TestAPlainFolderGetsItsFilesBack(t *testing.T) {
	root := emptyDir(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, "changed.txt"), "first\n")
	writeFile(t, filepath.Join(root, "gone.txt"), "first\n")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	agentInCopy(t, func(spec session.SessionSpec) {
		if spec.Dir == root {
			t.Errorf("the session starts in the folder %s, want a copy", root)
		}
		writeFile(t, filepath.Join(spec.Dir, "changed.txt"), "the run\n")
		writeFile(t, filepath.Join(spec.Dir, "added.txt"), "the run\n")
		if err := os.Remove(filepath.Join(spec.Dir, "gone.txt")); err != nil {
			t.Error(err)
		}
		if err := os.Symlink("/etc/passwd", filepath.Join(spec.Dir, "link")); err != nil {
			t.Error(err)
		}
	})
	out, err := run(t, "run", "claude", "--home", filepath.Join(tempDir(t), "home"), "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for name, want := range map[string]string{"changed.txt": "the run\n", "added.txt": "the run\n"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Errorf("%s has %q (%v), want %q", name, data, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); err == nil {
		t.Error("gone.txt stays in the folder")
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); err == nil {
		t.Error("the run's link is in the folder")
	}
	wants(t, out, "left out of the copy back to your folder: ", "link")
}

// TestTheTimeLimitOfTheGitSessionKeepsTheCopy is a git session that passes its limit: the
// session has that deadline, nothing comes back, the copy stays and the line has its path.
func TestTheTimeLimitOfTheGitSessionKeepsTheCopy(t *testing.T) {
	root := copyCheckout(t)
	var copyDir string
	agentInCopy(t, func(spec session.SessionSpec) {
		copyDir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		if at, ok := ctx.Deadline(); !ok || time.Until(at) > runcopy.SessionBase {
			t.Errorf("the git session has until %s, %v, want at most %s", at, ok, runcopy.SessionBase)
		}
		return &session.Result{TimedOut: true}, nil
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1", cmd.ExitCode(err), err)
	}
	wants(t, out, "read the run's files in "+filepath.Dir(copyDir)+"/work, and run no git there: the git session ran past its limit of 5m0s, so the run's commits did not come back")
	if _, err := os.Stat(filepath.Join(copyDir, "left.txt")); err != nil {
		t.Errorf("the copy did not stay: %v", err)
	}
	wantDisarmed(t, copyDir)
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(copyDir)) })
}

// TestTheTimeLimitOfTheGitSessionGrowsWithTheCopy is a copy with 2 GiB in a sparse file: the git
// session has a minute more, and the line of its limit has that limit.
func TestTheTimeLimitOfTheGitSessionGrowsWithTheCopy(t *testing.T) {
	copyCheckout(t)
	var copyDir string
	agentInCopy(t, func(spec session.SessionSpec) {
		copyDir = spec.Dir
		if err := os.Truncate(filepath.Join(spec.Dir, "README.md"), 2<<30); err != nil {
			t.Fatal(err)
		}
	})
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		if at, ok := ctx.Deadline(); !ok || time.Until(at) <= runcopy.SessionBase || time.Until(at) > runcopy.SessionBase+time.Minute {
			t.Errorf("the git session has until %s, %v, want a minute past %s", at, ok, runcopy.SessionBase)
		}
		return &session.Result{TimedOut: true}, nil
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1", cmd.ExitCode(err), err)
	}
	wants(t, out, "and run no git there: the git session ran past its limit of 6m0s, so the run's commits did not come back")
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(copyDir)) })
}

// TestNoHostVariableReachesTheGitContainer is a run with a token and a model key in qory's
// environment: the docker run of the git container passes only the variables of the git
// session's script, each with its value, and neither of the two.
func TestNoHostVariableReachesTheGitContainer(t *testing.T) {
	root := copyCheckout(t)
	t.Setenv("GH_TOKEN", "fake-gh-token")
	t.Setenv("ANTHROPIC_API_KEY", "fake-model-key")
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	var args []string
	t.Cleanup(cmd.SeeGitContainer(func(a []string) { args = a }))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
	var names []string
	for i, a := range args {
		if strings.Contains(a, "fake-") {
			t.Errorf("a host variable reaches the git container: %s", a)
		}
		if a == "--env" {
			name, _, ok := strings.Cut(args[i+1], "=")
			if !ok {
				t.Errorf("--env %s takes its value from the docker command's environment", args[i+1])
			}
			names = append(names, name)
		}
	}
	if got, want := strings.Join(names, " "), "PATH HOME GIT_CONFIG_NOSYSTEM GIT_CONFIG_GLOBAL"; got != want {
		t.Errorf("the git container gets %s, want %s", got, want)
	}
}

// wantDisarmed checks that the copy at work has no .git, and that git status finds no
// repository there.
func wantDisarmed(t *testing.T, work string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(work, ".git")); err == nil {
		t.Errorf("the copy %s that stays has a .git", work)
	}
	info, err := os.Lstat(filepath.Join(work, ".git-from-run"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the copy's .git-from-run: %v, %v", info, err)
	}
	c := exec.Command("git", "status")
	c.Dir = work
	c.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(work), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := c.CombinedOutput(); err == nil {
		t.Errorf("git status passed in the copy:\n%s", out)
	}
}

// TestAGitSessionThatFailsBringsNothingBack is a git session that writes its bundle and exits
// with 7: qory uses no bundle of it, and the copy stays.
func TestAGitSessionThatFailsBringsNothingBack(t *testing.T) {
	root := copyCheckout(t)
	var copyDir string
	agentInCopy(t, func(spec session.SessionSpec) {
		copyDir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		res, err := cmd.GitSessionOnHost(ctx, spec)
		if err != nil || res.ExitCode != 0 {
			t.Errorf("the git session: %+v, %v", res, err)
		}
		return &session.Result{ExitCode: 7}, nil
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1", cmd.ExitCode(err), err)
	}
	wants(t, out, "read the run's files in "+copyDir+", and run no git there: the git session exited with 7, so the run's commits did not come back")
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
	wantDisarmed(t, copyDir)
}

// TestAnAgentThatDeletesTheRunsBranchKeepsTheCopy is an agent that deletes the branch the run
// works on: nothing comes back, and the line has the copy's path.
func TestAnAgentThatDeletesTheRunsBranchKeepsTheCopy(t *testing.T) {
	root := copyCheckout(t)
	var copyDir string
	agentInCopy(t, func(spec session.SessionSpec) {
		copyDir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		branch := gitOutput(t, spec.Dir, "symbolic-ref", "--short", "HEAD")
		gitOutput(t, spec.Dir, "update-ref", "-d", "refs/heads/"+branch)
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out, "read the run's files in "+copyDir+", and run no git there: ")
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
	wantDisarmed(t, copyDir)
}

// TestASecondSignalCutsTheGitSession is a second signal during the git session: the session
// ends, the line has that cause, and the copy stays.
func TestASecondSignalCutsTheGitSession(t *testing.T) {
	root := copyCheckout(t)
	var copyDir string
	agentInCopy(t, func(spec session.SessionSpec) {
		copyDir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, _ session.SessionSpec) (*session.Result, error) {
		term(t)
		term(t)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	out, _ := run(t, "run", "claude", "--", "-p", "hi")
	wants(t, out, "read the run's files in "+copyDir+", and run no git there: qory run got a second signal during the git session, so the run's commits did not come back")
	lacks(t, out, "ran past its limit", "failed git's checks")
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
}

// TestAFirstSignalLeavesTheGitSessionItsTime is one signal during the git session of a local
// run: the session keeps its limit of five minutes, and the run's work comes back.
func TestAFirstSignalLeavesTheGitSessionItsTime(t *testing.T) {
	root := copyCheckout(t)
	// The time of a run from a run code: the session outlives it here.
	t.Cleanup(cmd.SetPushTimes(time.Second, time.Second))
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		term(t)
		time.Sleep(2500 * time.Millisecond)
		if err := ctx.Err(); err != nil {
			t.Errorf("one signal ended the git session: %v", err)
		}
		if at, ok := ctx.Deadline(); !ok || time.Until(at) < 4*time.Minute {
			t.Errorf("the git session has until %s", at)
		}
		return cmd.GitSessionOnHost(ctx, spec)
	}))
	out, _ := run(t, "run", "claude", "--", "-p", "hi")
	wants(t, out, "qory run: got SIGTERM: bringing the run's work back, for at most 30m0s. Send a second signal to stop that.")
	branch := runBranchOf(t, out)
	gitOutput(t, root, "cat-file", "-e", branch+":left.txt")
}

// TestAReturnWithNoSignalPrintsNoWait is a run with no signal: nothing waits on a signal.
func TestAReturnWithNoSignalPrintsNoWait(t *testing.T) {
	copyCheckout(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	cmd.RunGitSessionsOnHost()
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lacks(t, out, "Send a second signal")
}

// TestTheRemovalOfTheSetupHasALimit is a removal of the setup container that does not end: its
// context has a deadline, so the local return goes on to its line.
func TestTheRemovalOfTheSetupHasALimit(t *testing.T) {
	copyCheckout(t)
	agentInCopy(t, func(session.SessionSpec) {})
	removals := 0
	_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{RemoveSetup: func(ctx context.Context) error {
		removals++
		if at, ok := ctx.Deadline(); !ok || time.Until(at) > 3*time.Minute {
			t.Errorf("the removal has until %s, %v", at, ok)
		}
		return nil
	}})
	defer restore()
	cmd.RunGitSessionsOnHost()
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if removals == 0 {
		t.Error("the run removed no setup")
	}
}

// TestACheckPastItsLimitHasItsLine is a check of the run's bundle cut by its limit: the line has the
// limit, and it is no failed check.
func TestACheckPastItsLimitHasItsLine(t *testing.T) {
	copyCheckout(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	cmd.RunGitSessionsOnHost()
	t.Cleanup(cmd.SetCopyStepLimit(time.Nanosecond))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil {
		t.Fatalf("the run passed:\n%s", out)
	}
	wants(t, out, "and run no git there: the check of the run's commits ran past its limit of 1ns, so those commits did not come back")
}

// TestCommitsOnAnotherBranchArriveUnderRefsQory is an agent that committed on a branch and checked
// out the run's branch: the branch comes back under the run's prefix, the line says where and how to
// list it, and the copy goes.
func TestCommitsOnAnotherBranchArriveUnderRefsQory(t *testing.T) {
	root := copyCheckout(t)
	var work string
	agentInCopy(t, func(spec session.SessionSpec) {
		work = spec.Dir
		branch := strings.TrimSpace(gitOutput(t, spec.Dir, "branch", "--show-current"))
		gitOutput(t, spec.Dir, "checkout", "--quiet", "-b", "feature")
		writeFile(t, filepath.Join(spec.Dir, "feature.txt"), "x\n")
		gitOutput(t, spec.Dir, "add", "feature.txt")
		gitOutput(t, spec.Dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "-m", "on feature")
		gitOutput(t, spec.Dir, "checkout", "--quiet", branch)
	})
	cmd.RunGitSessionsOnHost()
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lacks(t, out, "no action needed", "did not come back", "the run's commits are on")
	prefix := runRefsOf(t, out)
	wants(t, out, "qory run: the run's refs are under "+prefix+" in ")
	if got := gitOutput(t, root, "ls-tree", "-r", "--name-only", prefix+"/heads/feature"); !strings.Contains(got, "feature.txt") {
		t.Errorf("heads/feature has:\n%s", got)
	}
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("a run's branch came back: %s", got)
	}
	if _, err := os.Lstat(work); err == nil {
		t.Errorf("the copy %s stays", work)
	}
}

// TestAFailedCloseKeepsTheCopy is a run whose close fails: a container of the run may be left,
// so qory reads nothing of the copy, and the copy stays.
func TestAFailedCloseKeepsTheCopy(t *testing.T) {
	root := copyCheckout(t)
	var copyDir string
	agentInCopy(t, func(spec session.SessionSpec) {
		copyDir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{CloseErr: errors.New("the close failed")})
	defer restore()
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out, "the close failed", "read the run's files in "+copyDir+", run no git there, then remove the folder")
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
	wantDisarmed(t, copyDir)
}

// TestSeveralRepositoriesLandOnTheRunsBranch is a checkout with a further repository under
// .qory/repositories: the agent changes both, and each gets the run's branch.
func TestSeveralRepositoriesLandOnTheRunsBranch(t *testing.T) {
	root := copyCheckout(t)
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
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		writeFile(t, filepath.Join(spec.Dir, ".qory", "repositories", "other", "there.txt"), "there\n")
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	gitOutput(t, root, "cat-file", "-e", branch+":left.txt")
	gitOutput(t, further, "cat-file", "-e", branch+":there.txt")
	if strings.Count(out, "the run's commits are on "+branch) != 2 {
		t.Errorf("want a line for each repository:\n%s", out)
	}
}

// TestAPlainFolderConflictPutsTheRunsVersionBeside is a plain folder whose files the person changes during
// the run, and the run too: the person's stay, the run's version of the changed file goes beside
// it, the run's deletion is listed, and the copy is removed.
func TestAPlainFolderConflictPutsTheRunsVersionBeside(t *testing.T) {
	root := emptyDir(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, "both.txt"), "first\n")
	writeFile(t, filepath.Join(root, "gone.txt"), "first\n")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	var copyDir string
	agentInCopy(t, func(spec session.SessionSpec) {
		copyDir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, "both.txt"), "the run\n")
		if err := os.Remove(filepath.Join(spec.Dir, "gone.txt")); err != nil {
			t.Error(err)
		}
		writeFile(t, filepath.Join(root, "both.txt"), "the person\n")
		writeFile(t, filepath.Join(root, "gone.txt"), "the person\n")
	})
	out, err := run(t, "run", "claude", "--home", filepath.Join(tempDir(t), "home"), "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out,
		"you and the run both changed these files: yours stay, and the run's version of each is beside it with -from-run: both.txt.",
		"you and the run both changed these files, so yours stay: gone.txt.")
	for name, want := range map[string]string{"both.txt": "the person\n", "both.txt-from-run": "the run\n", "gone.txt": "the person\n"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(data) != want {
			t.Errorf("%s has %q (%v), want %q", name, data, err, want)
		}
	}
	if strings.Contains(out, "read the run's files in") {
		t.Errorf("the copy stays:\n%s", out)
	}
	if _, err := os.Lstat(copyDir); err == nil {
		t.Errorf("the copy %s stays", copyDir)
	}
}

// TestAPlainFolderFailedFileKeepsTheCopy is a plain folder where the person closes a folder for
// writes during the run, and the run adds a file there: the line lists that file, the rest comes
// back, and the copy stays with the file, disarmed.
func TestAPlainFolderFailedFileKeepsTheCopy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes every folder")
	}
	root := emptyDir(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, "closed", "old.txt"), "first\n")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	var copyDir string
	agentInCopy(t, func(spec session.SessionSpec) {
		copyDir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, ".git", "config"), "[core]\n\tbare = false\n")
		writeFile(t, filepath.Join(spec.Dir, "closed", "new.txt"), "the run\n")
		writeFile(t, filepath.Join(spec.Dir, "other.txt"), "the run\n")
		if err := os.Chmod(filepath.Join(root, "closed"), 0o555); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "closed"), 0o755) })
	out, err := run(t, "run", "claude", "--home", filepath.Join(tempDir(t), "home"), "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "left out of the copy back, as qory could not read or write them: closed/new.txt.", "read the run's files in "+copyDir+", run no git there, then remove the folder")
	if data, err := os.ReadFile(filepath.Join(root, "other.txt")); err != nil || string(data) != "the run\n" {
		t.Errorf("other.txt has %q (%v)", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(copyDir, "closed", "new.txt")); err != nil || string(data) != "the run\n" {
		t.Errorf("the copy's closed/new.txt has %q (%v)", data, err)
	}
	wantDisarmed(t, copyDir)
}

// TestARunInARunsCopyStops is qory run and a compose in a folder of a run's copy, which a run
// could have written: each stops with the fix first.
func TestARunInARunsCopyStops(t *testing.T) {
	copyCheckout(t)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "qory", "runs", "run-1", "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "--quiet", "--initial-branch=main")
	t.Chdir(dir)
	for _, args := range [][]string{{"run", "claude", "--", "-p", "hi"}, {"harness", "compose", "--runtime", "claude"}} {
		out, err := run(t, args...)
		if cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%v: exit %d (%v)\n%s", args, cmd.ExitCode(err), err, out)
		}
		wants(t, out+fmt.Sprint(err), "run qory in your checkout: ")
	}
}

// TestABranchPinnedModuleKeepsItsPinInTheCopy is a module at main, composed once, whose remote
// is gone: the compose in the copy starts from the person's report, so it keeps the commit.
func TestABranchPinnedModuleKeepsItsPinInTheCopy(t *testing.T) {
	root := copyCheckout(t)
	remote, url := branchRemote(t)
	writeOwnStack(t, root, onMain(url))
	runGit(t, root, "add", "qory.yaml")
	runGit(t, root, "commit", "--quiet", "-m", "a module at main")
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pin := readReport(t, root).Modules[0].Pin
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal(err)
	}
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "could not reach the remote; kept "+pin)
}

// TestARefusalOfTheCopyCallsItTheWorkspace is a read run with a setup, whose setup Forager
// stops for the copy's folder: the line calls that folder the workspace, never a mount.
func TestARefusalOfTheCopyCallsItTheWorkspace(t *testing.T) {
	listsRepositories(t)
	var work string
	codeSetup = func(spec session.SetupSpec) error {
		work = spec.Dir
		return &session.Refusal{Code: "mount_contains_forager_files", Names: []string{work, filepath.Join(work, "records")}}
	}
	t.Cleanup(func() { codeSetup = nil })
	r, _ := readRun(t, nil, readImage, "")
	if work == "" {
		t.Fatalf("no setup ran: %v\n%s", r.err, r.out)
	}
	wants(t, r.out+fmt.Sprint(r.err), "the workspace "+work)
	lacks(t, r.out+fmt.Sprint(r.err), "the mount "+work)
}

// runKeyInCopy runs qory run behind the fake wall in the checkout at root with a run key, opened
// nowhere, with agent as the join of the agent's session, and returns its output and error.
func runKeyInCopy(t *testing.T, root string, agent func(spec session.SessionSpec)) (string, error) {
	t.Helper()
	t.Chdir(root)
	key := mintRunKey(t, keyRunID, newKeyedGateway(t).url())
	_, restore := cmd.OpenNowhere()
	t.Cleanup(restore)
	docker, _ := fakeDocker(t)
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
		"wall:\n  adapter: docker\n  image: example.com/agent:1\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n")
	agentInCopy(t, agent)
	t.Setenv("QORY_RUN_KEY", key)
	return run(t, "run", "--home", filepath.Join(tempDir(t), "home"))
}

// TestAControlPlaneRunHasNoArtifactsCopy is a walled run with a run key in a repository with a
// .qory/artifacts folder: the copy gets none, and the run's stays out of the checkout.
func TestAControlPlaneRunHasNoArtifactsCopy(t *testing.T) {
	root := runKeyCheckout(t)
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "--quiet", "-m", "first")
	writeFile(t, filepath.Join(root, ".qory", "artifacts", "person.txt"), "the person\n")
	joined := false
	out, err := runKeyInCopy(t, root, func(spec session.SessionSpec) {
		joined = true
		if spec.Dir == root {
			t.Errorf("the session starts in the checkout %s, want a copy", root)
		}
		if _, err := os.Lstat(filepath.Join(spec.Dir, ".qory", "artifacts", "person.txt")); err == nil {
			t.Error("the copy has the person's artifacts")
		}
		writeFile(t, filepath.Join(spec.Dir, ".qory", "artifacts", "run.txt"), "the run\n")
	})
	if !joined {
		t.Fatalf("no session joined: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	if _, err := os.Lstat(filepath.Join(root, ".qory", "artifacts", "run.txt")); err == nil {
		t.Errorf("the run's artifact came back\n%s", out)
	}
}

// TestACheckoutThatContainsTheStateFolderRuns is a walled run whose checkout contains qory's
// state folder: the copy leaves that folder out, and the run's work comes back. The test join
// runs no check of Forager's, so this test does not show that Forager accepts the run.
func TestACheckoutThatContainsTheStateFolderRuns(t *testing.T) {
	root := copyCheckout(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, ".state"))
	gitInfoExclude(t, root, ".state")
	agentInCopy(t, func(spec session.SessionSpec) {
		if _, err := os.Stat(filepath.Join(spec.Dir, ".state")); err == nil {
			t.Error("the copy has the state folder")
		}
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
}

// gitInfoExclude keeps name out of git status in the checkout at root.
func gitInfoExclude(t *testing.T, root, name string) {
	t.Helper()
	writeFile(t, filepath.Join(root, ".git", "info", "exclude"), name+"\n")
}

// TestACheckoutAtTheHomeFolderWithTheStateOutsideRuns is a walled run with XDG_STATE_HOME
// outside HOME and the checkout at HOME: HOME is not bound.
func TestACheckoutAtTheHomeFolderWithTheStateOutsideRuns(t *testing.T) {
	root := copyCheckout(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", tempDir(t))
	t.Setenv("XDG_CACHE_HOME", tempDir(t))
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	agentInCopy(t, func(spec session.SessionSpec) {
		for _, m := range spec.Mounts {
			if !m.ReadOnly && (m.Path == root || m.Path == real) {
				t.Errorf("the session has the read-write mount %s of the home folder", m.Path)
			}
		}
		if spec.Dir == root || spec.Dir == real {
			t.Errorf("the session starts in the home folder %s", spec.Dir)
		}
	})
	if out, err := run(t, "run", "claude", "--home", filepath.Join(tempDir(t), "home"), "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestAnOldPlacesFileStopsNothing is a home with a file of the former record of checkouts
// under the state folder: qory wa, the compose and run print nothing about it.
func TestAnOldPlacesFileStopsNothing(t *testing.T) {
	root := copyCheckout(t)
	old := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "places", "0123456789ab")
	writeFile(t, old, "{\"path\": \""+root+"\"}\n")
	agentInCopy(t, func(session.SessionSpec) {})
	for _, args := range [][]string{
		{"wa", "feature", "--no-compose", "--offline"},
		{"harness", "compose", "--runtime", "claude", "--no-links"},
		{"run", "claude", "--", "-p", "hi"},
	} {
		out, err := run(t, args...)
		if err != nil {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
		lacks(t, out, "places", old)
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("the file is gone: %v", err)
	}
}

// stepsAndSessions is the steps of the record rec and the starts and ends of its sessions, in
// order: "join started", "session 1 started", "copy started: copy work out", "copy exited
// failed 128".
func stepsAndSessions(rec []runEvent) []string {
	kinds := map[float64]string{}
	var lines []string
	for _, ev := range rec {
		switch ev.Type {
		case event.StepStarted:
			kind, _ := ev.Data["kind"].(string)
			kinds[ev.Data["step"].(float64)] = kind
			if kind == event.StepCopy {
				lines = append(lines, fmt.Sprintf("copy started: %v", ev.Data["name"]))
			} else {
				lines = append(lines, kind+" started")
			}
		case event.StepExited:
			kind := kinds[ev.Data["step"].(float64)]
			if kind == event.StepCopy {
				lines = append(lines, fmt.Sprintf("copy exited %v %v", ev.Data["state"], ev.Data["exit_code"]))
			} else {
				lines = append(lines, kind+" exited")
			}
		case "dev.qory.session.started":
			bare := ""
			if ev.Data["bare"] == true {
				bare = " bare"
			}
			lines = append(lines, fmt.Sprintf("session %v started%s", ev.Data["session"], bare))
		case "dev.qory.session.exited":
			lines = append(lines, fmt.Sprintf("session %v exited", ev.Data["session"]))
		}
	}
	return lines
}

// fromCopyStep is the part of lines from the copy step's start.
func fromCopyStep(t *testing.T, lines []string) []string {
	t.Helper()
	i := slices.Index(lines, "copy started: copy work out")
	if i < 0 {
		t.Fatalf("the record has no copy step:\n%s", strings.Join(lines, "\n"))
	}
	return lines[i:]
}

// TestTheRecordHasTheCopyStepWithNoJoin is a walled write run through Forager's join, with a
// wall that runs its containers on this machine: the record has one session, the agent's, and
// the copy step after it, with no step of a join inside, which exits 0 once the git session ended.
func TestTheRecordHasTheCopyStepWithNoJoin(t *testing.T) {
	root := walledThroughJoin(t, "#!/bin/sh\necho left > left.txt\n")
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
	all := stepsAndSessions(sessionRecord(t, root))
	equalLines(t, fromCopyStep(t, all), []string{
		"copy started: copy work out",
		"copy exited succeeded 0",
	})
	if i, j := slices.Index(all, "session 1 exited"), slices.Index(all, "copy started: copy work out"); i < 0 || i > j {
		t.Errorf("the agent's session is not before the copy step:\n%s", strings.Join(all, "\n"))
	}
	if slices.Contains(all, "session 2 started") || slices.Contains(all, "session 2 started bare") {
		t.Errorf("the record has a second session:\n%s", strings.Join(all, "\n"))
	}
}

// TestAFailedGitSessionExitsItsCopyStepFailed is the git session through Forager's join on a
// copy where the agent deleted the tree of its commit: git read-tree in the session exits 128,
// and the copy step exits failed with that code.
func TestAFailedGitSessionExitsItsCopyStepFailed(t *testing.T) {
	root := walledThroughJoin(t, `#!/bin/sh
echo left > left.txt
git add left.txt
git -c user.name=agent -c user.email=agent@example.invalid commit -q -m agent
tree=$(git rev-parse 'HEAD^{tree}')
rm -f ".git/objects/$(echo "$tree" | cut -c1-2)/$(echo "$tree" | cut -c3-)"
`)
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out, "the git session exited with 128, so the run's commits did not come back")
	lines := fromCopyStep(t, stepsAndSessions(sessionRecord(t, root)))
	if got := lines[len(lines)-1]; got != "copy exited failed 128" {
		t.Errorf("the copy step ends with %q:\n%s", got, strings.Join(lines, "\n"))
	}
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
}

// walledThroughJoin is a checkout whose walled write run goes through Forager's join, with the
// agent script and a wall that runs its containers on this machine.
func walledThroughJoin(t *testing.T, agent string) string {
	t.Helper()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := filepath.Join(t.TempDir(), "agent")
	writeFile(t, script, agent)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	runGit(t, root, "add", "qory.yaml", "modules")
	runGit(t, root, "commit", "--quiet", "-m", "first")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\n"+passingWall(t))
	t.Setenv("QORY_RUN_KEY", "")
	return root
}

// copyStepRun is a walled write run with a fake run: agent, nil for one that leaves a file,
// stands in for the agent, and git for the git session. It returns the run's output and error,
// the checkout, and the copy step's events and the session's join and end, in order. Those
// two show as "joined" and "ended".
func copyStepRun(t *testing.T, values cmd.Nowhere, agent func(spec session.SessionSpec), git func(context.Context, session.SessionSpec) (*session.Result, error)) (string, error, string, []string) {
	t.Helper()
	root := copyCheckout(t)
	if agent == nil {
		agent = func(spec session.SessionSpec) {
			writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		}
	}
	agentInCopy(t, agent)
	var trace []string
	copyStep := 0
	values.OnStep = func(e cmd.StepEvent) {
		if e.Type == "step_started" && e.Kind == event.StepCopy {
			copyStep = e.Step
		}
		if e.Step == copyStep && copyStep != 0 {
			trace = append(trace, shownSteps([]cmd.StepEvent{e})...)
		}
	}
	_, _, restore := cmd.OpenNowhereWith(values)
	defer restore()
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		trace = append(trace, "joined")
		defer func() { trace = append(trace, "ended") }()
		return git(ctx, spec)
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	return out, err, root, trace
}

// withoutNumbers is lines without the step's number at the start of each.
func withoutNumbers(lines []string) []string {
	shown := make([]string, len(lines))
	for i, line := range lines {
		_, rest, _ := strings.Cut(line, " ")
		if rest == "" {
			rest = line
		}
		shown[i] = rest
	}
	return shown
}

// TestTheCopyStepEndsWithTheGitSession is each failure of the git session: the step starts before
// the join and exits failed once after the session ended, with the session's exit code, or -1
// when it has none, and its last line is the stop.
func TestTheCopyStepEndsWithTheGitSession(t *testing.T) {
	cases := []struct {
		name string
		res  *session.Result
		err  error
		exit string
		line string
	}{
		{"git fails", &session.Result{ExitCode: 128}, nil, "exited failed 128", "the git session exited with 128, so the run's commits did not come back"},
		{"the time limit", &session.Result{TimedOut: true}, nil, "exited failed -1", "the git session ran past its limit of 5m0s, so the run's commits did not come back"},
		{"the time limit with an exit code", &session.Result{TimedOut: true, ExitCode: 143}, nil, "exited failed -1", "the git session ran past its limit of 5m0s, so the run's commits did not come back"},
		{"no start", nil, errors.New("the engine is gone"), "exited failed -1", "the git session did not start, so the run's commits did not come back: the engine is gone"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err, root, trace := copyStepRun(t, cmd.Nowhere{}, nil, func(context.Context, session.SessionSpec) (*session.Result, error) {
				return c.res, c.err
			})
			if cmd.ExitCode(err) != 1 {
				t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
			}
			wants(t, out, c.line)
			got := withoutNumbers(trace)
			if len(got) != 5 {
				t.Fatalf("the copy step's events:\n%s", strings.Join(trace, "\n"))
			}
			equalLines(t, []string{got[0], got[1], got[2], got[4]}, []string{"started copy: copy work out", "joined", "ended", c.exit})
			if !strings.HasPrefix(got[3], "log: qory run: read the run's files in ") || !strings.HasSuffix(got[3], ": "+c.line+"\n") {
				t.Errorf("the step's last line is %q, want the stop %q", got[3], c.line)
			}
			if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
				t.Errorf("the run's branch came back: %s", got)
			}
		})
	}
}

// TestTheCopyStepOfASessionThatSucceeds is a git session that exits 0: the step starts before
// the join, exits succeeded with 0 after the session ended, once, with no line, and the run's
// commits come back.
func TestTheCopyStepOfASessionThatSucceeds(t *testing.T) {
	out, err, root, trace := copyStepRun(t, cmd.Nowhere{}, nil, cmd.GitSessionOnHost)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	equalLines(t, withoutNumbers(trace), []string{"started copy: copy work out", "joined", "ended", "exited succeeded 0"})
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
}

// TestACutGitSessionExitsItsCopyStepCancelled is a second signal during the git session: the step
// exits cancelled with -1, and its last line has the cut.
func TestACutGitSessionExitsItsCopyStepCancelled(t *testing.T) {
	out, _, _, trace := copyStepRun(t, cmd.Nowhere{}, nil, func(ctx context.Context, _ session.SessionSpec) (*session.Result, error) {
		term(t)
		term(t)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	line := "qory run got a second signal during the git session, so the run's commits did not come back"
	wants(t, out, line)
	got := withoutNumbers(trace)
	if len(got) != 5 || got[4] != "exited cancelled -1" || !strings.HasSuffix(got[3], ": "+line+"\n") {
		t.Errorf("the copy step's events:\n%s", strings.Join(trace, "\n"))
	}
}

// TestACutGitSessionWithAnExitCodeExitsCancelled is a second signal during the git session, whose
// join returns a result with the exit code after the stop: the step exits cancelled with -1, not
// with that code, whether it is 143 or 0.
func TestACutGitSessionWithAnExitCodeExitsCancelled(t *testing.T) {
	for _, code := range []int{143, 0} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			out, _, _, trace := copyStepRun(t, cmd.Nowhere{}, nil, func(ctx context.Context, _ session.SessionSpec) (*session.Result, error) {
				term(t)
				term(t)
				<-ctx.Done()
				return &session.Result{Cancelled: true, ExitCode: code}, nil
			})
			line := "qory run got a second signal during the git session, so the run's commits did not come back"
			wants(t, out, line)
			got := withoutNumbers(trace)
			if len(got) != 5 || got[4] != "exited cancelled -1" || !strings.HasSuffix(got[3], ": "+line+"\n") {
				t.Errorf("the copy step's events:\n%s", strings.Join(trace, "\n"))
			}
		})
	}
}

// TestACopyStepWithNoScriptExitsFailed is a git session whose script qory cannot write, since
// the agent made the folder of its bundle: no session joins, and the step exits failed with -1.
func TestACopyStepWithNoScriptExitsFailed(t *testing.T) {
	out, err, _, trace := copyStepRun(t, cmd.Nowhere{}, func(spec session.SessionSpec) {
		if err := os.Mkdir(filepath.Join(filepath.Dir(spec.Dir), "bundle"), 0o700); err != nil {
			t.Error(err)
		}
	}, func(context.Context, session.SessionSpec) (*session.Result, error) {
		t.Error("a session joined")
		return &session.Result{}, nil
	})
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out, "the git session did not start, so the run's commits did not come back: ")
	got := withoutNumbers(trace)
	if len(got) != 3 || got[0] != "started copy: copy work out" || !strings.Contains(got[1], "the git session did not start") || got[2] != "exited failed -1" {
		t.Errorf("the copy step's events:\n%s", strings.Join(trace, "\n"))
	}
}

// TestACopyStepThatDoesNotStartStopsTheReturn is a StartStep of kind copy that returns an error:
// no session joins, and the run's commits do not come back.
func TestACopyStepThatDoesNotStartStopsTheReturn(t *testing.T) {
	out, err, root, trace := copyStepRun(t, cmd.Nowhere{StepErr: func(kind string) error {
		if kind == event.StepCopy {
			return errors.New("the step did not start")
		}
		return nil
	}}, nil, func(context.Context, session.SessionSpec) (*session.Result, error) {
		t.Error("a session joined")
		return &session.Result{}, nil
	})
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out, "the git session did not start, so the run's commits did not come back: the step did not start")
	if len(trace) != 0 {
		t.Errorf("the copy step's events:\n%s", strings.Join(trace, "\n"))
	}
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
}

// TestACopyStepThatDoesNotEndStopsTheReturn is a Step.Exit of the copy step that returns an error
// after a session that succeeded: the line has the error, and the run's commits do not come
// back, as for any step that does not end.
func TestACopyStepThatDoesNotEndStopsTheReturn(t *testing.T) {
	out, err, root, _ := copyStepRun(t, cmd.Nowhere{StepExitErr: func(kind string) error {
		if kind == event.StepCopy {
			return errors.New("the step did not end")
		}
		return nil
	}}, nil, cmd.GitSessionOnHost)
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out, "and run no git there: the step did not end")
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
}

// TestAFailedExitKeepsTheStopOfTheGitSession is a git session that exits 128 and a Step.Exit of the
// copy step that returns an error: the line is the session's stop, and the error of the exit
// does not replace it.
func TestAFailedExitKeepsTheStopOfTheGitSession(t *testing.T) {
	out, err, _, _ := copyStepRun(t, cmd.Nowhere{StepExitErr: func(kind string) error {
		if kind == event.StepCopy {
			return errors.New("the step did not end")
		}
		return nil
	}}, nil, func(context.Context, session.SessionSpec) (*session.Result, error) {
		return &session.Result{ExitCode: 128}, nil
	})
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, out, "and run no git there: the git session exited with 128, so the run's commits did not come back")
	lacks(t, out, "the step did not end")
}

// TestTheCopyStepsLogHasNoPathOfTheCopy is a failed git session on a run with no push: the line in
// the step's log has the run's copy, and the line on the terminal has the path of the copy.
// It covers a path in the line's fix and a path in its cause.
func TestTheCopyStepsLogHasNoPathOfTheCopy(t *testing.T) {
	var work string
	cases := []struct {
		name  string
		agent func(session.SessionSpec)
		log   string
	}{
		{"in the fix", func(spec session.SessionSpec) { work = spec.Dir }, "the run's copy, and run no git there: the git session exited with 128, so the run's commits did not come back\n"},
		{"in the cause", func(spec session.SessionSpec) {
			work = spec.Dir
			if err := os.Mkdir(filepath.Join(filepath.Dir(spec.Dir), "bundle"), 0o700); err != nil {
				t.Error(err)
			}
		}, "the run's copy/bundle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _, _, trace := copyStepRun(t, cmd.Nowhere{}, c.agent, func(context.Context, session.SessionSpec) (*session.Result, error) {
				return &session.Result{ExitCode: 128}, nil
			})
			wants(t, out, "qory run: read the run's files in "+work+", and run no git there: ")
			line := ""
			for _, e := range trace {
				if _, rest, ok := strings.Cut(e, " log: "); ok {
					line = rest
				}
			}
			if !strings.HasPrefix(line, "qory run: read the run's files in the run's copy, and run no git there: ") || !strings.Contains(line, c.log) {
				t.Errorf("the step's log line is %q", line)
			}
			if strings.Contains(line, filepath.Join("qory", "runs")) || strings.Contains(line, filepath.Dir(work)) {
				t.Errorf("the step's log line has a path of the copy: %q", line)
			}
		})
	}
}

// TestARunsEndWithAFailedCopyStep is each end of a walled write run with no push, through qory
// run's close on a fake run: a copy step that does not succeed sets the end of a run whose agent
// succeeded, and of no other.
func TestARunsEndWithAFailedCopyStep(t *testing.T) {
	exits := func(code int) func(context.Context, session.SessionSpec) (*session.Result, error) {
		return func(context.Context, session.SessionSpec) (*session.Result, error) {
			return &session.Result{ExitCode: code}, nil
		}
	}
	agentOK := &session.Result{State: event.StateSucceeded}
	interrupted := session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: -1}
	cases := []struct {
		name   string
		agent  *session.Result
		values cmd.Nowhere
		git    func(context.Context, session.SessionSpec) (*session.Result, error)
		want   session.Outcome
		status int
	}{
		{name: "no failure", agent: agentOK, git: cmd.GitSessionOnHost, want: session.Outcome{State: event.StateSucceeded}},
		{name: "the step exits 128", agent: agentOK, git: exits(128), want: session.Outcome{State: event.StateFailed, ExitCode: 128}, status: 1},
		{name: "the session exits 0 at its limit", agent: agentOK, git: func(context.Context, session.SessionSpec) (*session.Result, error) {
			return &session.Result{TimedOut: true}, nil
		}, want: session.Outcome{State: event.StateFailed, ExitCode: -1}, status: 1},
		{name: "the session does not start", agent: agentOK, git: func(context.Context, session.SessionSpec) (*session.Result, error) {
			return nil, errors.New("the engine is gone")
		}, want: session.Outcome{State: event.StateFailed, ExitCode: -1}, status: 1},
		{name: "the step does not start", agent: agentOK, values: cmd.Nowhere{StepErr: func(kind string) error {
			if kind == event.StepCopy {
				return errors.New("the step did not start")
			}
			return nil
		}}, git: exits(0), want: session.Outcome{State: event.StateFailed, ExitCode: -1}, status: 1},
		{name: "the step does not end", agent: agentOK, values: cmd.Nowhere{StepExitErr: func(kind string) error {
			if kind == event.StepCopy {
				return errors.New("the step did not end")
			}
			return nil
		}}, git: cmd.GitSessionOnHost, want: session.Outcome{State: event.StateFailed, ExitCode: -1}, status: 1},
		{name: "the removal of the setup container fails", agent: agentOK, values: cmd.Nowhere{RemoveSetup: func(context.Context) error {
			return errors.New("the engine failed")
		}}, git: exits(128), want: session.Outcome{State: event.StateFailed, ExitCode: -1}, status: 1},
		{name: "a container of the run is kept", agent: agentOK, values: cmd.Nowhere{ContainerKept: true}, git: exits(128), want: session.Outcome{State: event.StateSucceeded}, status: 1},
		{name: "a signal cuts the step", agent: agentOK, git: func(ctx context.Context, _ session.SessionSpec) (*session.Result, error) {
			term(t)
			term(t)
			<-ctx.Done()
			return nil, ctx.Err()
		}, want: interrupted, status: 1},
		{name: "a signal cuts the removal of the setup container", agent: agentOK, values: cmd.Nowhere{RemoveSetup: func(ctx context.Context) error {
			term(t)
			term(t)
			<-ctx.Done()
			return ctx.Err()
		}}, git: exits(0), want: interrupted, status: 1},
		{name: "a signal, and the step exits 128 within its time", agent: agentOK, git: func(context.Context, session.SessionSpec) (*session.Result, error) {
			term(t)
			return &session.Result{ExitCode: 128}, nil
		}, want: session.Outcome{State: event.StateFailed, ExitCode: 128}, status: 1},
		{name: "the agent failed", agent: &session.Result{State: event.StateFailed, ExitCode: 3}, git: exits(128), want: session.Outcome{State: event.StateFailed, ExitCode: 3}, status: 3},
		{name: "the agent timed out", agent: &session.Result{State: event.StateCancelled, TimedOut: true, ExitCode: 124}, git: exits(128), want: session.Outcome{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: 124}, status: 124},
		{name: "the agent was cancelled", agent: &session.Result{State: event.StateCancelled, Cancelled: true, ExitCode: 130}, git: exits(128), want: session.Outcome{State: event.StateCancelled, Reason: event.ReasonInterrupted, ExitCode: 130}, status: 1},
		{name: "the gateway closed the run", agent: &session.Result{RunClosed: true, State: event.StateSucceeded}, git: exits(128), want: session.Outcome{State: event.StateFailed, ExitCode: -1}, status: 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			copyCheckout(t)
			t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
				if spec.OnVariables != nil {
					spec.OnVariables(session.Applied{})
				}
				writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
				return c.agent, nil
			}))
			closes, _, restore := cmd.OpenNowhereWith(c.values)
			defer restore()
			t.Cleanup(cmd.SetGitSession(c.git))
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if got := closes(); len(got) != 1 || got[0] != c.want {
				t.Errorf("the run closed with %+v, want %+v\n%s", got, c.want, out)
			}
			if got := cmd.ExitCode(err); got != c.status {
				t.Errorf("exit %d (%v), want %d\n%s", got, err, c.status, out)
			}
		})
	}
}

// TestTheRecordOfARunEndsAsItsCopyStep is a walled write run through Forager's join, whose record
// is the real one: dev.qory.run.exited for a copy step that exits 128, one that a signal cuts,
// and one that succeeds, and qory run's exit status.
func TestTheRecordOfARunEndsAsItsCopyStep(t *testing.T) {
	broken := `#!/bin/sh
echo left > left.txt
git add left.txt
git -c user.name=agent -c user.email=agent@example.invalid commit -q -m agent
tree=$(git rev-parse 'HEAD^{tree}')
rm -f ".git/objects/$(echo "$tree" | cut -c1-2)/$(echo "$tree" | cut -c3-)"
`
	cases := []struct {
		name, agent string
		git         func(context.Context, session.SessionSpec) (*session.Result, error)
		want        string
		status      int
	}{
		{name: "the step exits 128", agent: broken, want: "failed//128", status: 1},
		{name: "a signal cuts the step", agent: "#!/bin/sh\necho left > left.txt\n", git: func(ctx context.Context, _ session.SessionSpec) (*session.Result, error) {
			term(t)
			term(t)
			<-ctx.Done()
			return nil, ctx.Err()
		}, want: "cancelled/interrupted/-1", status: 1},
		{name: "no failure", agent: "#!/bin/sh\necho left > left.txt\n", want: "succeeded//0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := walledThroughJoin(t, c.agent)
			if c.git != nil {
				t.Cleanup(cmd.SetGitSession(c.git))
			}
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if got := cmd.ExitCode(err); got != c.status {
				t.Errorf("exit %d (%v), want %d\n%s", got, err, c.status, out)
			}
			if got := endsOf(t, root); len(got) != 1 || got[0] != c.want {
				t.Errorf("the run ended %v, want %s\n%s", got, c.want, out)
			}
		})
	}
}

// TestAWorkingFolderInTheCheckoutStartsInTheCopy is a run from a folder of the commit at HEAD,
// which starts in that folder of the copy, and from a folder the commit does not have, which
// stops with the fix first.
func TestAWorkingFolderInTheCheckoutStartsInTheCopy(t *testing.T) {
	root := copyCheckout(t)
	writeFile(t, filepath.Join(root, "sub", "file.txt"), "first\n")
	runGit(t, root, "add", "sub")
	runGit(t, root, "commit", "--quiet", "-m", "sub")
	var dir string
	agentInCopy(t, func(spec session.SessionSpec) { dir = spec.Dir })
	t.Chdir(filepath.Join(root, "sub"))
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if filepath.Base(dir) != "sub" || filepath.Base(filepath.Dir(dir)) != "work" {
		t.Errorf("the session starts in %s, want the folder sub of the copy", dir)
	}
	missing := filepath.Join(root, "missing")
	if err := os.Mkdir(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(missing)
	_, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil || !strings.HasPrefix(err.Error(), "commit missing, or run qory in a folder that the commit at HEAD has") {
		t.Errorf("a folder the commit lacks: %v", err)
	}
}

// TestASessionThatFailedStillBringsItsCommitsBack is an agent that exits 3 after it wrote a
// file: the file is on the run's branch, and the run exits 3.
func TestASessionThatFailedStillBringsItsCommitsBack(t *testing.T) {
	root := copyCheckout(t)
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		return &session.Result{State: event.StateFailed, ExitCode: 3}, nil
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 3 {
		t.Fatalf("%v (exit %d), want exit 3\n%s", err, cmd.ExitCode(err), out)
	}
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
}

// TestARunClosedDuringItsSessionBringsTheWorkBack is a run that the gateway closed while its agent
// ran: the git session runs after the close, with no copy step, and the agent's file is on the
// run's branch.
func TestARunClosedDuringItsSessionBringsTheWorkBack(t *testing.T) {
	root := copyCheckout(t)
	var dir string
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		dir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		return &session.Result{RunClosed: true}, nil
	}))
	var steps []string
	closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{OnStep: func(e cmd.StepEvent) {
		if e.Type == "step_started" {
			steps = append(steps, e.Kind)
		}
	}})
	defer restore()
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		if len(closes()) != 1 {
			t.Error("the git session ran before the close")
		}
		return cmd.GitSessionOnHost(ctx, spec)
	}))
	out, _ := run(t, "run", "claude", "--", "-p", "hi")
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
	if slices.Contains(steps, event.StepCopy) {
		t.Errorf("the run has a copy step: %v", steps)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy stayed: %v", err)
	}
}

// TestARunKeyWithNoRepositoriesWorksInAFolderCopy is a walled run with a run key that lists no
// repositories, in a folder where git init ran and in a plain folder. The run works in a copy
// with no .git, and its file comes back. The person's .git stays as it is, with no commit.
func TestARunKeyWithNoRepositoriesWorksInAFolderCopy(t *testing.T) {
	for name, keepGit := range map[string]bool{"git init": true, "plain": false} {
		t.Run(name, func(t *testing.T) {
			root := checkoutWithNoCommit(t)
			copyFixture(t, "two-modules", root)
			composedForFake(t, root, fakeRuntime(t))
			t.Setenv("QORY_TEST_EXIT", "0")
			if !keepGit {
				if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
					t.Fatal(err)
				}
			}
			joined := false
			out, err := runKeyInCopy(t, root, func(spec session.SessionSpec) {
				joined = true
				if spec.Dir == root {
					t.Errorf("the session starts in the folder %s, want a copy", root)
				}
				if _, err := os.Lstat(filepath.Join(spec.Dir, ".git")); err == nil {
					t.Error("the copy has a .git")
				}
				writeFile(t, filepath.Join(spec.Dir, "added.txt"), "the run\n")
			})
			if !joined {
				t.Fatalf("no session joined: %v (exit %d)\n%s", err, cmd.ExitCode(err), out)
			}
			data, err := os.ReadFile(filepath.Join(root, "added.txt"))
			if err != nil || string(data) != "the run\n" {
				t.Errorf("the run's file has %q (%v)\n%s", data, err, out)
			}
			if keepGit {
				if got := gitOutput(t, root, "rev-list", "-n", "1", "--all"); got != "" {
					t.Errorf("the person's .git has the commit %s", got)
				}
			}
		})
	}
}

// TestALocalRunInARepositoryWithNoCommitStops is a local run where git init ran and no commit
// exists: it stops with the fix first, before any session joins.
func TestALocalRunInARepositoryWithNoCommitStops(t *testing.T) {
	root := checkoutWithNoCommit(t)
	copyFixture(t, "two-modules", root)
	composedForFake(t, root, fakeRuntime(t))
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	agentInCopy(t, func(session.SessionSpec) { t.Error("a session joined") })
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 1 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	wants(t, out, "qory run: commit in ", "or start the run in a repository with a commit at HEAD")
}

// TestALocalRunSeesTheArtifactsAndTheWorkItem is a local run in a checkout with a version and
// a work item. In the copy, qory artifact pull uses the local store with the earlier version,
// qory artifact push makes version 2, and qory work-item show reads the file. Version 2 comes
// back with no conflict.
func TestALocalRunSeesTheArtifactsAndTheWorkItem(t *testing.T) {
	root := copyCheckout(t)
	t.Setenv("QORY_RUN_API", "")
	first := filepath.Join(root, "note.md")
	writeFile(t, first, "the person\n")
	if out, err := run(t, "artifact", "push", "note", first); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".qory", "work-item.json"), "{\"title\":\"Local\"}\n")
	store := filepath.Join(".qory", "artifacts", "note", "main")
	agentInCopy(t, func(spec session.SessionSpec) {
		back, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(spec.Dir); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(back); err != nil {
				t.Fatal(err)
			}
		}()
		out, err := run(t, "artifact", "pull")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		wants(t, out, "Nothing pulled")
		data, err := os.ReadFile(filepath.Join(spec.Dir, store, "v1", "note.md"))
		if err != nil || string(data) != "the person\n" {
			t.Errorf("the run's version 1 has %q (%v)", data, err)
		}
		second := filepath.Join(tempDir(t), "note.md")
		writeFile(t, second, "the run\n")
		out, err = run(t, "artifact", "push", "note", second)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		data, err = os.ReadFile(filepath.Join(spec.Dir, store, "v2", "note.md"))
		if err != nil || string(data) != "the run\n" {
			t.Errorf("the run's version 2 has %q (%v)\n%s", data, err, out)
		}
		out, err = run(t, "work-item", "show")
		if err != nil || out != "{\"title\":\"Local\"}\n" {
			t.Errorf("qory work-item show printed %q (%v)", out, err)
		}
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for version, want := range map[string]string{"v1": "the person\n", "v2": "the run\n"} {
		data, err := os.ReadFile(filepath.Join(root, store, version, "note.md"))
		if err != nil || string(data) != want {
			t.Errorf("%s has %q (%v), want %q\n%s", version, data, err, want, out)
		}
	}
	if strings.Contains(out, "both changed") || strings.Contains(out, "left out of") {
		t.Errorf("the run lists a conflict or a left-out file:\n%s", out)
	}
}

// TestARunsArtifactsComeBackWithNoCommit is a run with no commit whose agent kept a version in
// .qory/artifacts and left a link there: the version is in the person's .qory/artifacts, the
// link is not, and the line lists it. A git session past its limit brings the version too.
func TestARunsArtifactsComeBackWithNoCommit(t *testing.T) {
	for name, limit := range map[string]bool{"no commit": false, "past the limit": true} {
		t.Run(name, func(t *testing.T) {
			root := copyCheckout(t)
			secret := filepath.Join(tempDir(t), "secret.txt")
			writeFile(t, secret, "secret\n")
			agentInCopy(t, func(spec session.SessionSpec) {
				store := filepath.Join(spec.Dir, ".qory", "artifacts")
				writeFile(t, filepath.Join(store, ".gitignore"), "*\n")
				writeFile(t, filepath.Join(store, "note", "main", "1", "note.md"), "the run\n")
				if err := os.Symlink(secret, filepath.Join(store, "note", "main", "1", "secret.txt")); err != nil {
					t.Fatal(err)
				}
			})
			if limit {
				t.Cleanup(cmd.SetGitSession(func(context.Context, session.SessionSpec) (*session.Result, error) {
					return &session.Result{TimedOut: true}, nil
				}))
			}
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if limit && cmd.ExitCode(err) != 1 || !limit && err != nil {
				t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
			}
			version := filepath.Join(root, ".qory", "artifacts", "note", "main", "1")
			data, err := os.ReadFile(filepath.Join(version, "note.md"))
			if err != nil || string(data) != "the run\n" {
				t.Errorf("the version has %q (%v)\n%s", data, err, out)
			}
			if _, err := os.Lstat(filepath.Join(version, "secret.txt")); err == nil {
				t.Error("the run's link came back")
			}
			wants(t, out, "left out of the copy back to your folder: ", "secret.txt")
			if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
				t.Errorf("a run's branch came back: %s", got)
			}
		})
	}
}

// aHomeFolder makes the working folder a plain folder with .ssh/id_test at home, with the state
// directory outside it unless state is set, and returns what the agent read, if it ran.
func aHomeFolder(t *testing.T, root, home, state string) *string {
	t.Helper()
	copyFixture(t, "two-modules", root)
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	t.Setenv("HOME", home)
	if state == "" {
		state = tempDir(t)
	}
	t.Setenv("XDG_STATE_HOME", state)
	writeFile(t, filepath.Join(home, ".ssh", "id_test"), "dummy\n")
	seen := new(string)
	agentInCopy(t, func(spec session.SessionSpec) {
		*seen = spec.Dir
	})
	return seen
}

// wantHomeStop is a run that stops before any copy, with the line that starts with its fix.
func wantHomeStop(t *testing.T, seen *string, relation string) {
	t.Helper()
	out, err := run(t, "run", "claude", "--home", filepath.Join(tempDir(t), "home"), "--", "-p", "hi")
	if err == nil || *seen != "" {
		t.Fatalf("the run went on in %q: %v\n%s", *seen, err, out)
	}
	if code := cmd.ExitCode(err); code != 2 {
		t.Errorf("exit %d, want 2: %v", code, err)
	}
	wants(t, err.Error(), "start the run from a folder that does not contain your home folder or qory's state directory: the folder ", relation)
}

// TestAPlainFolderAtTheHomeFolderStops is a run that may write in a plain folder at the home
// folder, with the cache folder in it on macOS and, by XDG_CACHE_HOME, on Linux: no copy has
// .ssh.
func TestAPlainFolderAtTheHomeFolderStops(t *testing.T) {
	root := emptyDir(t)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	seen := aHomeFolder(t, root, root, "")
	wantHomeStop(t, seen, "is your home folder")
}

// TestAPlainFolderAtTheHomeFolderStopsWithTheCacheOutside is the same with the cache folder a link
// out of the home folder, where the check of the copy's folder finds no overlap.
func TestAPlainFolderAtTheHomeFolderStopsWithTheCacheOutside(t *testing.T) {
	root := emptyDir(t)
	if err := os.MkdirAll(filepath.Join(root, "Library"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tempDir(t), filepath.Join(root, "Library", "Caches")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tempDir(t), filepath.Join(root, ".cache")); err != nil {
		t.Fatal(err)
	}
	seen := aHomeFolder(t, root, root, "")
	wantHomeStop(t, seen, "is your home folder")
}

// TestAPlainFolderThatContainsTheHomeFolderStops is a plain folder above the home folder.
func TestAPlainFolderThatContainsTheHomeFolderStops(t *testing.T) {
	root := emptyDir(t)
	seen := aHomeFolder(t, root, filepath.Join(root, "users", "me"), "")
	wantHomeStop(t, seen, "contains your home folder")
}

// TestAPlainFolderThatContainsTheStateDirectoryStops is a plain folder with qory's state in it.
func TestAPlainFolderThatContainsTheStateDirectoryStops(t *testing.T) {
	root := emptyDir(t)
	seen := aHomeFolder(t, root, tempDir(t), filepath.Join(root, "state"))
	wantHomeStop(t, seen, "contains qory's state directory")
}

// TestAHomeFolderThatIsARepositoryRuns is a run that may write at a home folder with a commit:
// the copy has the commit's files, so .ssh, which git does not track, is not in it.
func TestAHomeFolderThatIsARepositoryRuns(t *testing.T) {
	root := copyCheckout(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", tempDir(t))
	// With no --home, the home is under the cache folder, which is in the home folder on macOS
	// and, by this variable, on Linux.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, ".cache"))
	writeFile(t, filepath.Join(root, ".ssh", "id_test"), "dummy\n")
	var seen string
	agentInCopy(t, func(spec session.SessionSpec) {
		seen = spec.Dir
		if _, err := os.Lstat(filepath.Join(spec.Dir, ".ssh")); err == nil {
			t.Error("the copy has .ssh")
		}
	})
	if out, err := run(t, "run", "claude", "--home", filepath.Join(tempDir(t), "home"), "--", "-p", "hi"); err != nil || seen == "" {
		t.Fatalf("the agent ran in %q: %v\n%s", seen, err, out)
	}
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 2 {
		t.Errorf("exit %d (%v), want 2\n%s", cmd.ExitCode(err), err, out)
	}
	wants(t, fmt.Sprint(err), "set --home to a folder outside ")
}

// TestACommitWithAFileAtQoryRuns is a commit at HEAD with a file at .qory: the home is outside the
// copy, so the file is in the agent's way no more than any other file of the commit.
func TestACommitWithAFileAtQoryRuns(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	runGit(t, root, "add", "qory.yaml", "modules")
	writeFile(t, filepath.Join(root, ".qory"), "a file\n")
	runGit(t, root, "add", ".qory")
	runGit(t, root, "commit", "--quiet", "-m", "first")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	ran := false
	agentInCopy(t, func(spec session.SessionSpec) {
		ran = true
		if got, err := os.ReadFile(filepath.Join(spec.Dir, ".qory")); err != nil || string(got) != "a file\n" {
			t.Errorf("the copy's .qory: %q, %v", got, err)
		}
	})
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil || !ran {
		t.Fatalf("the agent ran: %v: %v\n%s", ran, err, out)
	}
}

// TestCommitsThatMakeTheTopARepositorySayWhere is a commit that adds what makes the top folder a
// git folder: the line has words for that place.
func TestCommitsThatMakeTheTopARepositorySayWhere(t *testing.T) {
	copyCheckout(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "HEAD"), "ref: refs/heads/main\n")
		writeFile(t, filepath.Join(spec.Dir, "objects", "x"), "x\n")
		writeFile(t, filepath.Join(spec.Dir, "refs", "x"), "x\n")
	})
	cmd.RunGitSessionsOnHost()
	out, _ := run(t, "run", "claude", "--", "-p", "hi")
	wants(t, out, "They change these qory settings, links or repository folders: the repository's top folder.")
}

// homeWithALinkedCache is a home folder at dir, with Library/Caches a link out of it as a person's
// folder can have, so no copy lies inside the home folder. The working folder is what the test
// passes, and the checks that follow start in it.
func homeWithALinkedCache(t *testing.T, home string) *string {
	t.Helper()
	seen := aHomeFolder(t, home, home, "")
	if err := os.MkdirAll(filepath.Join(home, "Library"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(home, "Library", "Caches"))
	if err := os.Symlink(tempDir(t), filepath.Join(home, "Library", "Caches")); err != nil {
		t.Fatal(err)
	}
	return seen
}

// TestAHomeFolderWrittenAnotherWayStops is a working folder that is the home folder in another
// Unicode form or through the firmlink of the data volume: the file system has one folder, so the
// run stops as it does at the home folder written as HOME has it.
func TestAHomeFolderWrittenAnotherWayStops(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the two Unicode forms and the firmlink are one folder only on macOS")
	}
	const nfc, nfd = "Café", "Café"
	top, err := filepath.EvalSymlinks(tempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, paths := range map[string][2]string{
		"NFC home": {filepath.Join(top, nfc), filepath.Join(top, nfd)},
		"NFD home": {filepath.Join(top, nfd), filepath.Join(top, nfc)},
		"firmlink": {filepath.Join(top, "person"), "/System/Volumes/Data" + filepath.Join(top, "person")},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			home, working := paths[0], paths[1]
			defer func() { _ = os.RemoveAll(home) }()
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			seen := homeWithALinkedCache(t, home)
			if _, err := os.Stat(working); err != nil {
				t.Skipf("no such path here: %v", err)
			}
			t.Chdir(working)
			wantHomeStop(t, seen, "your home folder")
		})
	}
}

// TestTwoSignalsBeforeTheSessionHaveThatCause is two signals before the git session starts: the
// line has them as the cause, and no line asks for a second signal after both came. A line about
// git, which the ended context stopped, would send the person to check git.
func TestTwoSignalsBeforeTheSessionHaveThatCause(t *testing.T) {
	copyCheckout(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		term(t)
		term(t)
	})
	cmd.RunGitSessionsOnHost()
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil {
		t.Fatalf("the run passed:\n%s", out)
	}
	wants(t, out, "qory run got a second signal during the git session, so the run's commits did not come back")
	lacks(t, out, "the git session did not start", "check-ref-format", "Send a second signal")
}

// TestAClosedRunWithASignalBringsTheWorkBack is a signal to a run the gateway closed during its
// session: the work comes back after the close, and the line says so first.
func TestAClosedRunWithASignalBringsTheWorkBack(t *testing.T) {
	root := copyCheckout(t)
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		term(t)
		return &session.Result{RunClosed: true}, nil
	}))
	out, _ := run(t, "run", "claude", "--", "-p", "hi")
	wants(t, out, "qory run: got SIGTERM: bringing the run's work back, for at most 30m0s. Send a second signal to stop that.")
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
}

// TestALocalReturnCutAfterASignalHasItsBudget is a signal during the git session of a local run,
// which passes the time of the return: the line has that time, not the time of a push step.
func TestALocalReturnCutAfterASignalHasItsBudget(t *testing.T) {
	copyCheckout(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	t.Cleanup(cmd.SetReturnAfterSignal(200 * time.Millisecond))
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, _ session.SessionSpec) (*session.Result, error) {
		term(t)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil {
		t.Fatalf("the run passed:\n%s", out)
	}
	wants(t, out, "qory run got SIGTERM, and the step reached its limit of 200ms during the git session")
	lacks(t, out, "limit of 15s")
}

// commitIn commits every change in dir with an identity for the commit.
func commitIn(t *testing.T, dir, message string) {
	t.Helper()
	gitOutput(t, dir, "add", "-A")
	gitOutput(t, dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "--allow-empty", "-m", message)
}

// workUnderRefsQory is each way an agent leaves work that the run's branch does not have, with
// nothing past the base commit, in the copy at dir, with the ref under the run's prefix it comes
// back as.
var workUnderRefsQory = map[string]struct {
	agent func(t *testing.T, dir string)
	ref   string
}{
	"stash": {func(t *testing.T, dir string) {
		writeFile(t, filepath.Join(dir, "README.md"), "stashed\n")
		gitOutput(t, dir, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "stash", "--quiet")
	}, "stash"},
	"tag then reset": {func(t *testing.T, dir string) {
		writeFile(t, filepath.Join(dir, "x.txt"), "x\n")
		commitIn(t, dir, "x")
		gitOutput(t, dir, "tag", "keep")
		gitOutput(t, dir, "reset", "--quiet", "--hard", "HEAD~1")
	}, "tags/keep"},
	"detached commit": {func(t *testing.T, dir string) {
		on := strings.TrimSpace(gitOutput(t, dir, "branch", "--show-current"))
		gitOutput(t, dir, "checkout", "--quiet", "--detach")
		writeFile(t, filepath.Join(dir, "x.txt"), "x\n")
		commitIn(t, dir, "x")
		gitOutput(t, dir, "checkout", "--quiet", on)
	}, "lost/1"},
	"orphan": {func(t *testing.T, dir string) {
		gitOutput(t, dir, "checkout", "--quiet", "--orphan", "o")
		gitOutput(t, dir, "rm", "-rf", "--quiet", ".")
		writeFile(t, filepath.Join(dir, "new.txt"), "new\n")
	}, "uncommitted"},
}

// TestWorkOffTheBranchArrivesUnderRefsQory is each way of [workUnderRefsQory] in a local run: the
// work comes back under the run's prefix, the run exits 0, and the copy goes.
func TestWorkOffTheBranchArrivesUnderRefsQory(t *testing.T) {
	for name, c := range workUnderRefsQory {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			root := copyCheckout(t)
			var work string
			agentInCopy(t, func(spec session.SessionSpec) {
				work = spec.Dir
				c.agent(t, spec.Dir)
			})
			cmd.RunGitSessionsOnHost()
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			lacks(t, out, "no action needed", "did not come back")
			prefix := runRefsOf(t, out)
			if got := gitOutput(t, root, "for-each-ref", "--format=%(refname)", prefix+"/"); strings.TrimSpace(got) != prefix+"/"+c.ref {
				t.Errorf("the run's other refs are %q, want %s", got, prefix+"/"+c.ref)
			}
			if _, err := os.Lstat(work); err == nil {
				t.Errorf("the copy %s stays", work)
			}
		})
	}
}

// TestARepositoryTheRunMadeKeepsTheCopyOfALocalRun is a repository the run made in the copy: the
// line says so, the run exits 1, and the copy stays.
func TestARepositoryTheRunMadeKeepsTheCopyOfALocalRun(t *testing.T) {
	root := copyCheckout(t)
	var work string
	agentInCopy(t, func(spec session.SessionSpec) {
		work = spec.Dir
		inner := filepath.Join(spec.Dir, "inner")
		writeFile(t, filepath.Join(inner, "x.txt"), "x\n")
		gitOutput(t, inner, "init", "--quiet")
		commitIn(t, inner, "x")
	})
	cmd.RunGitSessionsOnHost()
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d, want 1:\n%s", cmd.ExitCode(err), out)
	}
	lacks(t, out, "no action needed")
	wants(t, out,
		"qory run: part of the run's work did not come back: the copy has a nested repository the run made.",
		"read the run's files in "+work+", run no git there, then remove the folder")
	if _, err := os.Lstat(work); err != nil {
		t.Errorf("the copy is gone: %v", err)
	}
	wantDisarmed(t, work)
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/", "refs/qory/"); got != "" {
		t.Errorf("a run's ref came back: %s", got)
	}
}

// TestAHeadBeforeTheBaseCommitLosesNoFile is an agent that checked out a commit older than the
// base commit and left no other work: no commit that deletes the base commit's files comes back.
func TestAHeadBeforeTheBaseCommitLosesNoFile(t *testing.T) {
	root := copyCheckout(t)
	writeFile(t, filepath.Join(root, "second.txt"), "second\n")
	runGit(t, root, "add", "second.txt")
	runGit(t, root, "commit", "--quiet", "-m", "second")
	agentInCopy(t, func(spec session.SessionSpec) {
		gitOutput(t, spec.Dir, "checkout", "--quiet", "--detach", "HEAD~1")
	})
	cmd.RunGitSessionsOnHost()
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lacks(t, out, "part of the run's work did not come back")
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("a run's branch came back: %s", got)
	}
}

// TestAnOrphanHeadBringsBackNoDeletion is a local run whose agent made a commit on the run's
// branch, then checked out a branch with no commit: the run's branch comes back as the agent left
// it, with no commit that deletes its files, the new file comes back as uncommitted under the run's
// prefix, and one line says both.
func TestAnOrphanHeadBringsBackNoDeletion(t *testing.T) {
	root := copyCheckout(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "committed.txt"), "committed\n")
		commitIn(t, spec.Dir, "Add a file")
		gitOutput(t, spec.Dir, "checkout", "--quiet", "--orphan", "o")
		gitOutput(t, spec.Dir, "rm", "-rf", "--quiet", ".")
		writeFile(t, filepath.Join(spec.Dir, "new.txt"), "new\n")
	})
	cmd.RunGitSessionsOnHost()
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lacks(t, out, "did not come back")
	branch := runBranchOf(t, out)
	prefix := runRefsOf(t, out)
	wants(t, out, ". The run's other refs are under "+prefix+": list them with git for-each-ref "+prefix+".")
	tree := gitOutput(t, root, "ls-tree", "-r", "--name-only", branch)
	wants(t, tree, "committed.txt", "README.md")
	lacks(t, tree, "new.txt")
	if got := strings.TrimSpace(gitOutput(t, root, "ls-tree", "-r", "--name-only", prefix+"/uncommitted")); got != "new.txt" {
		t.Errorf("uncommitted has %q", got)
	}
	if got := gitOutput(t, root, "log", "-1", "--format=%s", branch); !strings.Contains(got, "Add a file") {
		t.Errorf("the branch ends with %q", got)
	}
}

// nestedSubmoduleRepos makes a repository with a submodule, and returns the path of the outer one.
func nestedSubmoduleRepos(t *testing.T) string {
	t.Helper()
	inner := tempDir(t)
	runGit(t, inner, "init", "--quiet", "--initial-branch=main")
	writeFile(t, filepath.Join(inner, "lib.txt"), "lib\n")
	commitIn(t, inner, "inner")
	middle := tempDir(t)
	runGit(t, middle, "init", "--quiet", "--initial-branch=main")
	writeFile(t, filepath.Join(middle, "m.txt"), "m\n")
	runGit(t, middle, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", inner, "inner")
	commitIn(t, middle, "middle")
	return middle
}

// TestANestedSubmoduleComesBackThroughARun is an agent that updates a nested submodule, as a person
// does, and commits: the session ends well, and the commit lands on the run's branch.
func TestANestedSubmoduleComesBackThroughARun(t *testing.T) {
	root := copyCheckout(t)
	runGit(t, root, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", nestedSubmoduleRepos(t), "libs/m")
	runGit(t, root, "commit", "--quiet", "-m", "a submodule")
	agentInCopy(t, func(spec session.SessionSpec) {
		gitOutput(t, spec.Dir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive", "--quiet")
		writeFile(t, filepath.Join(spec.Dir, "x.txt"), "x\n")
		commitIn(t, spec.Dir, "x")
	})
	cmd.RunGitSessionsOnHost()
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lacks(t, out, "that the run made", "did not come back")
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got == "" {
		t.Errorf("the run's branch did not come back:\n%s", out)
	}
}

// TestASparseCheckoutBringsBackNoDeletion is a local run whose agent took files out of the working
// tree and changed nothing: nothing comes back, and no commit deletes them.
func TestASparseCheckoutBringsBackNoDeletion(t *testing.T) {
	for name, agent := range map[string]func(t *testing.T, dir string){
		"sparse-checkout": func(t *testing.T, dir string) {
			gitOutput(t, dir, "sparse-checkout", "set", "nothing-here")
		},
		"skip-worktree": func(t *testing.T, dir string) {
			gitOutput(t, dir, "update-index", "--skip-worktree", "README.md")
			if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := copyCheckout(t)
			agentInCopy(t, func(spec session.SessionSpec) { agent(t, spec.Dir) })
			cmd.RunGitSessionsOnHost()
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			lacks(t, out, "did not come back")
			wants(t, out, "no action needed: the run made no commit past its base commit")
			for _, ref := range strings.Fields(gitOutput(t, root, "for-each-ref", "--format=%(refname)", "refs/heads/qory/", "refs/qory/")) {
				if tree := gitOutput(t, root, "ls-tree", "-r", "--name-only", ref); !strings.Contains(tree, "README.md") {
					t.Errorf("%s came back without README.md:\n%s", ref, tree)
				}
			}
		})
	}
}

// noQoryFolder checks that the person's folder at root has no .qory.
func noQoryFolder(t *testing.T, root string) {
	t.Helper()
	if info, err := os.Lstat(filepath.Join(root, ".qory")); err == nil {
		t.Errorf("the run left %s, a folder: %v", filepath.Join(root, ".qory"), info.IsDir())
	}
}

// committedFixture is a checkout with the two-modules fixture committed and nothing composed,
// with a wall whose docker stands in for the engine, and the log of that docker.
func committedFixture(t *testing.T) (string, string) {
	t.Helper()
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	runGit(t, root, "add", "qory.yaml", "modules")
	runGit(t, root, "commit", "--quiet", "-m", "first")
	docker, log := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	return root, log
}

// TestAWalledRunComposesUnderTheCache is a walled run of Claude Code with no --home and no run key,
// through Forager's join and the fake engine: the agent's container starts, with the home under
// qory/homes read-only, and the person's folder gets no .qory.
func TestAWalledRunComposesUnderTheCache(t *testing.T) {
	root, log := committedFixture(t)
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 4 {
		t.Fatalf("exit %d (%v), want the agent's 4\n%s", cmd.ExitCode(err), err, out)
	}
	lacks(t, out, "Move the plugin")
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !containerRun.Match(data) {
		t.Fatalf("no container started:\n%s", data)
	}
	home := regexp.QuoteMeta(runHomesFolder(t, root)) + `/[^/,]+`
	if want := regexp.MustCompile(`--mount type=bind,src=` + home + `,dst=` + home + `,readonly`); !want.Match(data) {
		t.Errorf("no read-only mount of the run's home:\n%s", data)
	}
	noQoryFolder(t, root)
}

// TestAWalledRunWithAHomeInsideTheFolderStops is the stop of a run with a run key, on a walled run
// with no run key, before the run opens: nothing comes into the person's folder.
func TestAWalledRunWithAHomeInsideTheFolderStops(t *testing.T) {
	root, log := committedFixture(t)
	home := filepath.Join(root, ".qory", "harness")
	out, err := run(t, "run", "claude", "--home", home)
	want := "set --home to a folder outside " + root + ": " + home + " is inside " + root + ", and a walled run or a run with a run key writes no harness into the folder it runs in"
	if err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
		t.Errorf("%v (exit %d), want %q\n%s", err, cmd.ExitCode(err), want, out)
	}
	startedNothing(t, root, log)
	noQoryFolder(t, root)
}

// TestAWalledRunInAPlainFolderStartsWithNoHome is a folder that is not a repository: the default
// home is outside it, so the run starts, and its folder gets no .qory.
func TestAWalledRunInAPlainFolderStartsWithNoHome(t *testing.T) {
	root := emptyDir(t)
	copyFixture(t, "two-modules", root)
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	ran := false
	agentInCopy(t, func(session.SessionSpec) { ran = true })
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil || !ran {
		t.Fatalf("the agent ran: %v: %v\n%s", ran, err, out)
	}
	noQoryFolder(t, root)
}

// TestAJoinErrorBringsTheWorkBack is a join that passed its checks and returned an error, as a wait
// that failed does: the agent may have run, so the git session runs after the close, the agent's
// file is on the run's branch, the copy goes, and qory run fails with the join's error. The
// person's folder gets no .qory.
func TestAJoinErrorBringsTheWorkBack(t *testing.T) {
	root, _ := committedFixture(t)
	var copyDir string
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		spec.OnVariables(session.Applied{})
		copyDir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		return nil, errors.New("the wait for the agent failed")
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil {
		t.Fatalf("the run passed:\n%s", out)
	}
	wants(t, out, "the wait for the agent failed")
	lacks(t, out, "read the run's files in ")
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
	if _, err := os.Stat(copyDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy stayed: %v", err)
	}
	noQoryFolder(t, root)
}

// TestASessionAfterALostLinkBringsTheWorkBack is a run whose link to the gateway is lost during the
// agent's session: the join returns an error, and no host step starts. The git session needs no
// link, so the agent's file is on the run's branch, and the run has no copy step.
func TestASessionAfterALostLinkBringsTheWorkBack(t *testing.T) {
	root := copyCheckout(t)
	lost := errors.New("the link to the gateway is lost")
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		spec.OnVariables(session.Applied{})
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
		return nil, lost
	}))
	var steps []string
	_, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		StepErr: func(kind string) error {
			steps = append(steps, kind)
			if kind == event.StepCopy {
				return lost
			}
			return nil
		},
		HostProxyErr: func() error { return lost },
	})
	defer restore()
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil {
		t.Fatalf("the run passed:\n%s", out)
	}
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
	if slices.Contains(steps, event.StepCopy) {
		t.Errorf("the run started a copy step: %v", steps)
	}
}

// agentSucceedsInCopy is [agentInCopy] with a result whose state is succeeded, so the run closes
// succeeded.
func agentSucceedsInCopy(t *testing.T, agent func(spec session.SessionSpec)) {
	t.Helper()
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		spec.OnVariables(session.Applied{})
		agent(spec)
		return &session.Result{State: event.StateSucceeded}, nil
	}))
}

// TestAKeptContainerMovesTheGitSessionPastTheClose is a run in which a container could not be
// removed after the agent's session: no git session runs before the close. After it, the git
// session runs and the work comes back only when the close shows no container left.
func TestAKeptContainerMovesTheGitSessionPastTheClose(t *testing.T) {
	for _, wallKept := range []bool{false, true} {
		t.Run(fmt.Sprintf("WallKept %t", wallKept), func(t *testing.T) {
			root := copyCheckout(t)
			agentSucceedsInCopy(t, func(spec session.SessionSpec) {
				writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
			})
			var steps []string
			closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{ContainerKept: true, WallKept: wallKept, OnStep: func(e cmd.StepEvent) {
				if e.Type == "step_started" {
					steps = append(steps, e.Kind)
				}
			}})
			defer restore()
			sessions := 0
			t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
				sessions++
				if len(closes()) != 1 {
					t.Error("the git session ran before the close")
				}
				return cmd.GitSessionOnHost(ctx, spec)
			}))
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if slices.Contains(steps, event.StepCopy) {
				t.Errorf("the run has a copy step: %v", steps)
			}
			if got := closes(); len(got) != 1 || got[0].State != event.StateSucceeded {
				t.Errorf("the run closed with %+v, want succeeded", got)
			}
			if !wallKept {
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
				return
			}
			if sessions != 0 {
				t.Errorf("%d git sessions ran with a container kept at the close", sessions)
			}
			if cmd.ExitCode(err) != 1 {
				t.Errorf("exit %d (%v), want 1\n%s", cmd.ExitCode(err), err, out)
			}
			wants(t, out, "and run no git there: a container of the run could not be removed and may still run and write the copy, so nothing came back from the run")
			if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
				t.Errorf("the run's branch came back: %s", got)
			}
		})
	}
}

// TestAFailedRemovalOfTheSetupMovesTheGitSessionPastTheClose is a removal of the setup container
// that fails after the agent's session: the run closes failed with -1, as after a copy step that
// did not start, and the git session runs after the close, which shows no container left here,
// so the work comes back.
func TestAFailedRemovalOfTheSetupMovesTheGitSessionPastTheClose(t *testing.T) {
	root := copyCheckout(t)
	agentSucceedsInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{RemoveSetup: func(context.Context) error {
		return errors.New("the engine failed")
	}})
	defer restore()
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		if len(closes()) != 1 {
			t.Error("the git session ran before the close")
		}
		return cmd.GitSessionOnHost(ctx, spec)
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := closes(), (session.Outcome{State: event.StateFailed, ExitCode: -1}); len(got) != 1 || got[0] != want {
		t.Errorf("the run closed with %+v, want %+v", got, want)
	}
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
}

// TestAJoinStoppedAtItsChecksKeepsNoCopy is a join that its checks stopped: no agent ran, so no line tells the
// person to read a copy, and the copy goes.
func TestAJoinStoppedAtItsChecksKeepsNoCopy(t *testing.T) {
	root, _ := committedFixture(t)
	var copyDir string
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		copyDir = spec.Dir
		return nil, errors.New("the join stopped at its checks")
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err == nil {
		t.Fatalf("the run passed:\n%s", out)
	}
	lacks(t, out, "read the run's files in ")
	if _, err := os.Stat(copyDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy stayed: %v", err)
	}
	noQoryFolder(t, root)
}

// TestAnAgentsAddAllLeavesTheQoryFolderOut is an agent's git add -A in the copy of a local run: the
// artifacts and the work item that came in stay out of its commit.
func TestAnAgentsAddAllLeavesTheQoryFolderOut(t *testing.T) {
	root, _ := committedFixture(t)
	writeFile(t, filepath.Join(root, ".qory", "work-item.json"), "{\"title\":\"Local\"}\n")
	writeFile(t, filepath.Join(root, ".qory", "artifacts", "note", "main", "1", "a.txt"), "note\n")
	agentInCopy(t, func(spec session.SessionSpec) {
		if _, err := os.Stat(filepath.Join(spec.Dir, ".qory", "work-item.json")); err != nil {
			t.Errorf("the work item did not come in: %v", err)
		}
		if _, err := os.Stat(filepath.Join(spec.Dir, ".qory", "artifacts", "note", "main", "1", "a.txt")); err != nil {
			t.Errorf("the artifact did not come in: %v", err)
		}
		gitOutput(t, spec.Dir, "add", "-A")
		lacks(t, gitOutput(t, spec.Dir, "ls-files"), ".qory")
	})
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// TestTheGitSessionsLineHasNoScript is the session's arguments, which Forager prints on one line and
// writes in dev.qory.session.started: the script stays in its file.
func TestTheGitSessionsLineHasNoScript(t *testing.T) {
	copyCheckout(t)
	agentInCopy(t, func(session.SessionSpec) {})
	var args []string
	t.Cleanup(cmd.SetGitSession(func(_ context.Context, spec session.SessionSpec) (*session.Result, error) {
		args = spec.Args
		return &session.Result{}, nil
	}))
	_, _ = run(t, "run", "claude", "--", "-p", "hi")
	if len(args) == 0 || filepath.Base(args[0]) != runcopy.SessionScriptName {
		t.Fatalf("the session's arguments are %q", args)
	}
	joined := strings.Join(args, " ")
	lacks(t, joined, "set -eu", "copygit")
	if len(joined) > 2000 {
		t.Errorf("the session's arguments have %d characters", len(joined))
	}
}

// TestAPlainFolderWithQoryPrintsNoLeftOut is a plain folder with artifacts in .qory: the artifact
// the run wrote comes back, so no line has .qory, and a file the copy back does leave out has its
// line.
func TestAPlainFolderWithQoryPrintsNoLeftOut(t *testing.T) {
	root := emptyDir(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, ".qory", "artifacts", "note", "main", "1", "a.txt"), "note\n")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, ".qory", "artifacts", "note", "main", "2", "a.txt"), "run\n")
		if err := os.Symlink("README.md", filepath.Join(spec.Dir, "link")); err != nil {
			t.Fatal(err)
		}
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(root, ".qory", "artifacts", "note", "main", "2", "a.txt")); err != nil || string(data) != "run\n" {
		t.Errorf("the artifact: %q, %v", data, err)
	}
	wants(t, out, "qory run: left out of the copy back to your folder: link.\n")
	if strings.Contains(out, ".qory.") {
		t.Errorf("a line has .qory:\n%s", out)
	}
}

// TestAPlainFolderRunThatWritesInQoryPrintsTheLine is an agent that writes what the copy back does
// not bring to the person's folder, at .qory: the line prints once, since the copy goes with it.
func TestAPlainFolderRunThatWritesInQoryPrintsTheLine(t *testing.T) {
	for name, agent := range map[string]func(t *testing.T, work string){
		"a file": func(t *testing.T, work string) {
			writeFile(t, filepath.Join(work, ".qory"), "a file\n")
		},
		"a file in the folder": func(t *testing.T, work string) {
			writeFile(t, filepath.Join(work, ".qory", "notes.md"), "notes\n")
		},
		"a link": func(t *testing.T, work string) {
			if err := os.Symlink("README.md", filepath.Join(work, ".qory")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := emptyDir(t)
			copyFixture(t, "two-modules", root)
			docker, _ := fakeDocker(t)
			walledBy(t, docker)
			t.Setenv("QORY_RUN_KEY", "")
			agentInCopy(t, func(spec session.SessionSpec) { agent(t, spec.Dir) })
			out, err := run(t, "run", "claude", "--", "-p", "hi")
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if line := "qory run: left out of the copy back to your folder: .qory.\n"; strings.Count(out, line) != 1 {
				t.Errorf("want %q once:\n%s", line, out)
			}
			if _, err := os.Lstat(filepath.Join(root, ".qory")); err == nil {
				t.Error("the person's folder got a .qory")
			}
		})
	}
}

// TestAQoryLinkPrintsTheLine is a person's .qory that links to a folder with artifacts: the run's copy
// has no artifact, and the line prints.
func TestAQoryLinkPrintsTheLine(t *testing.T) {
	root, _ := committedFixture(t)
	elsewhere := tempDir(t)
	writeFile(t, filepath.Join(elsewhere, "artifacts", "note", "main", "1", "a.txt"), "note\n")
	if err := os.Symlink(elsewhere, filepath.Join(root, ".qory")); err != nil {
		t.Fatal(err)
	}
	agentInCopy(t, func(spec session.SessionSpec) {
		if _, err := os.Lstat(filepath.Join(spec.Dir, ".qory", "artifacts")); err == nil {
			t.Error("the run's copy has the artifacts")
		}
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "qory run: left out of the run's copy: .qory.\n")
}

// TestACommitWithAFileNamedQoryPrintsNoLeftOut is a commit with a file at .qory: nothing can be left
// out there, on any run, so no line has it.
func TestACommitWithAFileNamedQoryPrintsNoLeftOut(t *testing.T) {
	root, _ := committedFixture(t)
	writeFile(t, filepath.Join(root, ".qory"), "a file\n")
	runGit(t, root, "add", ".qory")
	runGit(t, root, "commit", "--quiet", "-m", "a file")
	agentInCopy(t, func(session.SessionSpec) {})
	out, err := run(t, "run", "claude", "--home", tempDir(t), "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "left out of") {
		t.Errorf("a line of what was left out:\n%s", out)
	}
}

// TestAFolderOfArtifactsBehindAFileIsReported is a run that wrote .qory/artifacts while the person
// has a file at .qory: the artifact stays out of the person's folder, and the line has .qory.
func TestAFolderOfArtifactsBehindAFileIsReported(t *testing.T) {
	root, _ := committedFixture(t)
	writeFile(t, filepath.Join(root, ".qory"), "a file\n")
	runGit(t, root, "add", ".qory")
	runGit(t, root, "commit", "--quiet", "-m", "a file")
	agentInCopy(t, func(spec session.SessionSpec) {
		if err := os.Remove(filepath.Join(spec.Dir, ".qory")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(spec.Dir, ".qory", "artifacts", "note", "main", "1", "a.txt"), "run\n")
	})
	out, _ := run(t, "run", "claude", "--home", tempDir(t), "--", "-p", "hi")
	wants(t, out, "left out of the copy back to your folder: .qory.")
}
