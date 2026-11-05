package cmd_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

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

// firstCommit makes an empty commit in the repository at dir: a walled run that may write and
// fetches no repository works in a copy of the commit at HEAD.
func firstCommit(t *testing.T, dir string) {
	t.Helper()
	gitOutput(t, dir, "-c", "user.name=Tester", "-c", "user.email=tester@example.com", "commit", "--quiet", "--allow-empty", "-m", "first")
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
	calls := recordGitDirs(t)
	r := p.run(t, func(dir string) {
		copyDir = dir
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
	for line := range strings.Lines(string(log)) {
		dir, args, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "|")
		if dir == copyDir || strings.HasPrefix(dir, copyDir+string(filepath.Separator)) {
			t.Errorf("git %s ran in the copy %s", args, dir)
		}
		seenPush = seenPush || strings.Contains(" "+args+" ", " push ")
	}
	if !seenPush {
		t.Errorf("no git push ran:\n%s", log)
	}
}

// recordGitDirs puts a git in front of PATH that records the folder of each git that qory
// runs, and returns the file of the records.
func recordGitDirs(t *testing.T) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	calls := filepath.Join(tempDir(t), "calls")
	writeFile(t, calls, "")
	bin := tempDir(t)
	writeFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nprintf '%s|%s\\n' \"$(pwd -P)\" \"$*\" >> "+calls+"\nexec "+real+" \"$@\"\n")
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
	wants(t, out, "look at the run's copy in "+copyDir+": the git session ran past its limit of 5m0s, so nothing came back from the run")
	if _, err := os.Stat(filepath.Join(copyDir, "left.txt")); err != nil {
		t.Errorf("the copy did not stay: %v", err)
	}
	if got := gitOutput(t, root, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("the run's branch came back: %s", got)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(copyDir)) })
}

// TestACheckoutThatContainsTheStateFolderRuns is a walled run whose checkout contains qory's
// state folder, which Forager stops when the checkout is bound: the copy is bound in its place.
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
// under the state folder: qory wa, the compose, run and launch print nothing about it.
func TestAnOldPlacesFileStopsNothing(t *testing.T) {
	root := copyCheckout(t)
	old := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "places", "0123456789ab")
	writeFile(t, old, "{\"path\": \""+root+"\"}\n")
	agentInCopy(t, func(session.SessionSpec) {})
	for _, args := range [][]string{
		{"wa", "feature", "--no-compose"},
		{"harness", "compose", "--runtime", "claude", "--no-links"},
		{"run", "claude", "--", "-p", "hi"},
		{"launch", "claude", "--", "-p", "hi"},
	} {
		out, err := run(t, args...)
		lacks(t, out, "places", old)
		if err != nil {
			lacks(t, err.Error(), "places", old)
		}
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
	wants(t, out, "look at the run's copy in "+dir+": the run ended before the git session started")
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the copy did not stay: %v", err)
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
