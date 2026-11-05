package cmd_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	m := regexp.MustCompile(`the run's commits are on (qory/run-\S+) in `).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the output has no line with the run's branch:\n%s", out)
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
	lacks(t, out, "They change these qory settings")
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
	wants(t, out, "the run's commits are on "+branch+" in ", " They change these qory settings: harness.launch.claude.command. Check them before you check out the branch.")
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

// TestARunCodeWithAReadWriteMountStops is a write run from a run code with a read-write
// mount: the line ends as each line after the code was read does.
func TestARunCodeWithAReadWriteMountStops(t *testing.T) {
	p := walledPush(t)
	mount := tempDir(t)
	p.flags = append(p.flags, "--mount", mount)
	r := p.run(t, writesAndCommits(t), nil)
	if r.err == nil || cmd.ExitCode(r.err) != cmd.ExitInput {
		t.Fatalf("got %v (exit %d)\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	wants(t, r.err.Error(), "make the mount "+mount+" read-only", "; the code is used")
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

// TestThePushStepPushesFromTheQuarantine is a walled run whose agent plants a hook and a
// configuration key in the copy's .git: both stay unused, its work reaches the branch, and
// no git of qory runs in the copy.
func TestThePushStepPushesFromTheQuarantine(t *testing.T) {
	p := walledPush(t)
	canary := filepath.Join(tempDir(t), "canary")
	var copyDir string
	var before int
	calls := recordGitDirs(t)
	r := p.run(t, func(dir string) {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		copyDir = real
		log, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		before = len(log)
		writeFile(t, filepath.Join(dir, "left.txt"), "left\n")
		hook := filepath.Join(dir, ".git", "hooks", "pre-push")
		writeFile(t, hook, "#!/bin/sh\ntouch "+canary+"\n")
		if err := os.Chmod(hook, 0o755); err != nil {
			t.Fatal(err)
		}
		config := filepath.Join(dir, ".git", "config")
		data, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, config, string(data)+"[core]\n\tfsmonitor = touch "+canary+"\n")
	}, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	wants(t, r.out, "pushed qory/wi_")
	if _, err := os.Stat(canary); err == nil {
		t.Error("a program of the copy's .git ran on this machine")
	}
	if p.api.document(t, 1).Repositories == nil {
		t.Errorf("no repository in the version\n%s", r.out)
	}
	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	seenPush := false
	for line := range strings.Lines(string(log[before:])) {
		parts := strings.SplitN(strings.TrimSuffix(line, "\n"), "|", 3)
		dir, home, args := parts[0], parts[1], parts[2]
		// The test's stand-in git session runs as a process on this machine, with its HOME in
		// the run's folder. On a wall it runs in the run's container.
		if strings.Contains(home, filepath.Join("qory", "runs")) {
			continue
		}
		if dir == copyDir || strings.HasPrefix(dir, copyDir+string(filepath.Separator)) {
			t.Errorf("git %s ran in the copy %s", args, dir)
		}
		seenPush = seenPush || strings.Contains(" "+args+" ", " push ")
	}
	if !seenPush {
		t.Errorf("no git push ran:\n%s", log)
	}
}

// recordGitDirs puts a git in front of PATH that records the folder, $HOME and arguments of
// each git that qory runs, and returns the file of the records.
func recordGitDirs(t *testing.T) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	calls := filepath.Join(tempDir(t), "calls")
	writeFile(t, calls, "")
	bin := tempDir(t)
	writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nprintf '%s|%s|%s\\n' \"$(pwd -P)\" \"$HOME\" \"$*\" >> "+calls+"\nexec "+real+" \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
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
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the git session has no deadline")
		}
		if spec.Timeout != runcopy.SessionLimit {
			t.Errorf("the git session's timeout is %s, want %s", spec.Timeout, runcopy.SessionLimit)
		}
		return &session.Result{TimedOut: true}, nil
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if cmd.ExitCode(err) != 1 {
		t.Errorf("exit %d (%v), want 1", cmd.ExitCode(err), err)
	}
	wants(t, out, "read the run's files in "+filepath.Dir(copyDir)+"/work, and run no git there: the git session ran past its limit of 5m0s, so nothing came back from the run")
	if _, err := os.Stat(filepath.Join(copyDir, "left.txt")); err != nil {
		t.Errorf("the copy did not stay: %v", err)
	}
	wantDisarmed(t, copyDir)
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(copyDir)) })
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

// lacksCopyPath checks that out has no path in the folder of the runs' copies.
func lacksCopyPath(t *testing.T, out string) {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, filepath.Join(base, "qory", "runs"))
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
	wants(t, out, "read the run's files in "+copyDir+", and run no git there: the git session exited with 7, so nothing came back from the run")
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
	wants(t, out, "read the run's files in "+copyDir+", and run no git there: qory run got a second signal during the git session, so nothing came back from the run")
	lacks(t, out, "ran past its limit", "failed git's checks")
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
}

// TestAFirstSignalLeavesTheGitSessionItsTime is one signal during the git session of a local
// run: the session keeps its limit of five minutes, and the run's work comes back.
func TestAFirstSignalLeavesTheGitSessionItsTime(t *testing.T) {
	root := copyCheckout(t)
	agentInCopy(t, func(spec session.SessionSpec) {
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		term(t)
		if err := ctx.Err(); err != nil {
			t.Errorf("one signal ended the git session: %v", err)
		}
		if at, ok := ctx.Deadline(); !ok || time.Until(at) < 4*time.Minute {
			t.Errorf("the git session has until %s", at)
		}
		return cmd.GitSessionOnHost(ctx, spec)
	}))
	out, _ := run(t, "run", "claude", "--", "-p", "hi")
	branch := runBranchOf(t, out)
	gitOutput(t, root, "cat-file", "-e", branch+":left.txt")
}

// TestThePushPathLineAsksForTheWorkItemAgain is a run with the push step whose git session
// passes its limit: the person may have no access to this machine, so the line has no path of
// the copy, and the run code is used.
func TestThePushPathLineAsksForTheWorkItemAgain(t *testing.T) {
	p := walledPush(t)
	t.Cleanup(cmd.SetGitSession(func(context.Context, session.SessionSpec) (*session.Result, error) {
		return &session.Result{TimedOut: true}, nil
	}))
	r := p.run(t, writesAndCommits(t), nil)
	wants(t, r.out, "take the work item in again in Qory Apiary: the git session ran past its limit of 5m0s, so nothing came back from the run; the code is used")
	lacksCopyPath(t, r.out)
	lacks(t, r.out, "run no git")
}

// TestASignalCutsTheGitSessionOfThePushStep is a signal during the git session of the push
// step, which has less than the session's five minutes: the line has that cause.
func TestASignalCutsTheGitSessionOfThePushStep(t *testing.T) {
	p := walledPush(t)
	defer cmd.SetPushTimes(200*time.Millisecond, 200*time.Millisecond)()
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, _ session.SessionSpec) (*session.Result, error) {
		term(t)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	r := p.run(t, writesAndCommits(t), nil)
	wants(t, r.out, "take the work item in again in Qory Apiary: qory run got SIGTERM, and the step reached its limit of 200ms during the git session, so nothing came back from the run; the code is used")
	lacksCopyPath(t, r.out)
	lacks(t, r.out, "ran past its limit")
}

// TestAChangedSettingShowsWhenThePushFails is a run whose commits change a qory setting and
// whose push fails: the line with the setting prints before the push.
func TestAChangedSettingShowsWhenThePushFails(t *testing.T) {
	p := walledPush(t)
	writeFile(t, filepath.Join(p.bare, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(p.bare, "hooks", "pre-receive"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := p.run(t, func(dir string) {
		writeFile(t, filepath.Join(dir, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  launch:\n    claude:\n      command: /opt/planted\n")
	}, nil)
	if p.branchHead(t) != "" {
		t.Errorf("the forge has the branch\n%s", r.out)
	}
	wants(t, r.out, "change these qory settings: harness.launch.claude.command")
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

// TestAPlainFolderConflictKeepsTheCopy is a plain folder whose file the person changes during
// the run, and the run too: the person's stays, and the copy stays with the run's, disarmed.
func TestAPlainFolderConflictKeepsTheCopy(t *testing.T) {
	root := emptyDir(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, "both.txt"), "first\n")
	docker, _ := fakeDocker(t)
	walledBy(t, docker)
	t.Setenv("QORY_RUN_KEY", "")
	var copyDir string
	agentInCopy(t, func(spec session.SessionSpec) {
		copyDir = spec.Dir
		writeFile(t, filepath.Join(spec.Dir, ".git", "config"), "[core]\n\tbare = false\n")
		writeFile(t, filepath.Join(spec.Dir, "both.txt"), "the run\n")
		writeFile(t, filepath.Join(root, "both.txt"), "the person\n")
	})
	out, err := run(t, "run", "claude", "--home", filepath.Join(tempDir(t), "home"), "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "you and the run both changed these files, so yours stay: both.txt", "read the run's files in "+copyDir+", run no git there, then remove the folder")
	if data, err := os.ReadFile(filepath.Join(root, "both.txt")); err != nil || string(data) != "the person\n" {
		t.Errorf("both.txt has %q (%v)", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(copyDir, "both.txt")); err != nil || string(data) != "the run\n" {
		t.Errorf("the copy's both.txt has %q (%v)", data, err)
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

// TestAReplacedFileStaysReplacedInTheCopy is a checkout where a compose replaced a tracked
// .claude/settings.json with a link: the copy's compose replaces it too, with no flag, and the
// run's branch has the repository's file.
func TestAReplacedFileStaysReplacedInTheCopy(t *testing.T) {
	root := copyCheckout(t)
	settings := filepath.Join(".claude", "settings.json")
	writeFile(t, filepath.Join(root, settings), "{\"from\": \"the repository\"}\n")
	runGit(t, root, "add", settings)
	runGit(t, root, "commit", "--quiet", "-m", "settings")
	if out, err := run(t, "hc", "--runtime", "claude", "--force"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rep := readReport(t, root); !slices.Contains(rep.Replaced, ".claude/settings.json") {
		t.Fatalf("the report has %v as replaced", rep.Replaced)
	}
	agentInCopy(t, func(spec session.SessionSpec) {
		info, err := os.Lstat(filepath.Join(spec.Dir, settings))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the copy's settings: %v, %v", info, err)
		}
		writeFile(t, filepath.Join(spec.Dir, "left.txt"), "left\n")
	})
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	branch := runBranchOf(t, out)
	if got := gitOutput(t, root, "show", branch+":.claude/settings.json"); !strings.Contains(got, "the repository") {
		t.Errorf("the run's branch has the settings %q", got)
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

// TestAControlPlaneRunHasNoArtifactsCopy is a run from a run code in a repository with a
// .qory/artifacts folder: the copy gets none, and the run's stays out of the checkout.
func TestAControlPlaneRunHasNoArtifactsCopy(t *testing.T) {
	var root string
	joined := false
	r := runFromCodeWith(t, func(string) []string {
		var err error
		if root, err = os.Getwd(); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "file.txt"), "first\n")
		runGit(t, root, "add", "file.txt")
		runGit(t, root, "commit", "--quiet", "-m", "first")
		writeFile(t, filepath.Join(root, ".qory", "artifacts", "person.txt"), "the person\n")
		docker, _ := fakeDocker(t)
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
			"wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  image: "+readImage+"\n")
		t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
			joined = true
			if _, err := os.Lstat(filepath.Join(spec.Dir, ".qory", "artifacts", "person.txt")); err == nil {
				t.Error("the copy has the person's artifacts")
			}
			writeFile(t, filepath.Join(spec.Dir, ".qory", "artifacts", "run.txt"), "the run\n")
			return &session.Result{}, nil
		}))
		return nil
	}, []string{"--home", filepath.Join(tempDir(t), "home")}, declares("write"), nil)
	if !joined {
		t.Fatalf("no session joined: %v (exit %d)\n%s", r.err, cmd.ExitCode(r.err), r.out)
	}
	if _, err := os.Lstat(filepath.Join(root, ".qory", "artifacts", "run.txt")); err == nil {
		t.Errorf("the run's artifact came back\n%s", r.out)
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

// TestARunCodeWithACheckoutInTheHomeFolderRuns is a run from a run code whose checkout is the
// home folder, which holds the run key's home and report on macOS: the run's agent gets the
// copy, so the code is not used up by a stop.
func TestARunCodeWithACheckoutInTheHomeFolderRuns(t *testing.T) {
	p := walledPush(t)
	t.Setenv("HOME", p.h.dir)
	var mounts []wall.Mount
	var dir string
	p.mountsOf = func(spec session.SessionSpec) { mounts, dir = spec.Mounts, spec.Dir }
	r := p.run(t, func(d string) { writeFile(t, filepath.Join(d, "left.txt"), "left\n") }, nil)
	r.wantsClose(t, event.StateSucceeded, 0)
	if dir == p.h.dir {
		t.Errorf("the session starts in the home folder %s", dir)
	}
	real, err := filepath.EvalSymlinks(p.h.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mounts {
		if m.Path == real || m.Path == p.h.dir {
			t.Errorf("the session has the mount %+v of the home folder", m)
		}
	}
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
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil {
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

// TestAWriteRunHasTwoSessionsInItsRecord is a walled write run through Forager's join, with a
// wall that runs its containers on this machine: the agent's commit reaches the run's branch,
// and the run's record has the agent's session and the git session.
func TestAWriteRunHasTwoSessionsInItsRecord(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	script := filepath.Join(t.TempDir(), "agent")
	writeFile(t, script, "#!/bin/sh\necho left > left.txt\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	composedForFake(t, root, script)
	runGit(t, root, "add", "qory.yaml", "modules")
	runGit(t, root, "commit", "--quiet", "-m", "first")
	writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"), "apiVersion: qory.dev/v1alpha1\n"+passingWall(t))
	t.Setenv("QORY_RUN_KEY", "")
	t.Cleanup(cmd.GitSessionThroughJoin())
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	gitOutput(t, root, "cat-file", "-e", runBranchOf(t, out)+":left.txt")
	started := 0
	for _, kind := range typesOf(sessionRecord(t, root)) {
		if kind == "session.started" {
			started++
		}
	}
	if started != 2 {
		t.Errorf("the record has %d started sessions, want 2\n%s", started, out)
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

// TestARunClosedDuringItsSessionKeepsTheCopy is a run that the gateway closed while its agent
// ran: no git session starts, the copy stays, and the line has its path.
func TestARunClosedDuringItsSessionKeepsTheCopy(t *testing.T) {
	copyCheckout(t)
	var dir string
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		dir = spec.Dir
		return &session.Result{RunClosed: true}, nil
	}))
	t.Cleanup(cmd.SetGitSession(func(context.Context, session.SessionSpec) (*session.Result, error) {
		t.Error("a git session started")
		return &session.Result{}, nil
	}))
	out, _ := run(t, "run", "claude", "--", "-p", "hi")
	wants(t, out, "read the run's files in "+dir+", and run no git there: the run ended before the git session started")
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the copy did not stay: %v", err)
	}
}

// TestARunCodeWithNoRepositoriesWorksInAFolderCopy is a run code whose run lists no
// repositories, in a folder where git init ran and in a plain folder. The run works in a copy
// with no .git, and its file comes back. The person's .git stays as it is, with no commit.
func TestARunCodeWithNoRepositoriesWorksInAFolderCopy(t *testing.T) {
	for name, keepGit := range map[string]bool{"git init": true, "plain": false} {
		t.Run(name, func(t *testing.T) {
			var root string
			joined := false
			r := runFromCodeWith(t, func(string) []string {
				var err error
				if root, err = os.Getwd(); err != nil {
					t.Fatal(err)
				}
				if !keepGit {
					if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
						t.Fatal(err)
					}
				}
				docker, _ := fakeDocker(t)
				writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "forager.yaml"),
					"wall:\n  adapter: docker\n  command: "+docker+"\n  helper: "+staticELF(t)+"\n  user: \"1000:1000\"\n  image: "+readImage+"\n")
				t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
					joined = true
					if spec.Dir == root {
						t.Errorf("the session starts in the folder %s, want a copy", root)
					}
					if _, err := os.Lstat(filepath.Join(spec.Dir, ".git")); err == nil {
						t.Error("the copy has a .git")
					}
					writeFile(t, filepath.Join(spec.Dir, "added.txt"), "the run\n")
					return &session.Result{}, nil
				}))
				return nil
			}, []string{"--home", filepath.Join(tempDir(t), "home")}, declares("write"), nil)
			if !joined {
				t.Fatalf("no session joined: %v (exit %d)\n%s", r.err, cmd.ExitCode(r.err), r.out)
			}
			data, err := os.ReadFile(filepath.Join(root, "added.txt"))
			if err != nil || string(data) != "the run\n" {
				t.Errorf("the run's file has %q (%v)\n%s", data, err, r.out)
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
// folder, with the cache folder in it: no copy has .ssh.
func TestAPlainFolderAtTheHomeFolderStops(t *testing.T) {
	root := emptyDir(t)
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
	writeFile(t, filepath.Join(root, ".ssh", "id_test"), "dummy\n")
	var seen string
	agentInCopy(t, func(spec session.SessionSpec) {
		seen = spec.Dir
		if _, err := os.Lstat(filepath.Join(spec.Dir, ".ssh")); err == nil {
			t.Error("the copy has .ssh")
		}
	})
	if out, err := run(t, "run", "claude", "--", "-p", "hi"); err != nil || seen == "" {
		t.Fatalf("the agent ran in %q: %v\n%s", seen, err, out)
	}
}
