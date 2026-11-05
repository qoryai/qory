package runcopy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/exports"
)

// branch is the run's branch in every test.
const branch = "qory/run-1"

// isolate sets HOME and the XDG folders to new folders, and keeps git from reading a
// system or global configuration file.
func isolate(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// gitCmd is git with args in dir, as a person runs it, with no system or global
// configuration.
func gitCmd(dir string, args ...string) *exec.Cmd {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.com",
		"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.com")
	return c
}

// testGit runs git with args in dir, as a person runs it, and returns its output.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmd(dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitIn runs git with args in dir, as [testGit] does, with input as its stdin.
func gitIn(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	c := gitCmd(dir, args...)
	c.Stdin = strings.NewReader(input)
	out, err := c.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// write writes text to the file rel in dir, and the folders above it.
func write(t *testing.T, dir, rel, text string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// read is what the file rel in dir has, "" when there is none.
func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	return string(b)
}

// link makes a link at rel in dir to target.
func link(t *testing.T, dir, rel, target string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// commitAll commits every change in the repository at dir.
func commitAll(t *testing.T, dir, message string) string {
	t.Helper()
	testGit(t, dir, "add", "-A")
	testGit(t, dir, "commit", "--quiet", "-m", message)
	return testGit(t, dir, "rev-parse", "HEAD")
}

// person makes the person's repository, on branch main with one commit, and returns its
// folder and the commit.
func person(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "person")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "init", "--quiet", "-b", "main")
	write(t, dir, "qory.yaml", "env:\n  A: \"1\"\n")
	write(t, dir, "README.md", "hello\n")
	write(t, dir, "CLAUDE.md", "base\n")
	return dir, commitAll(t, dir, "first")
}

// newDir makes the run's folder for run-1.
func newDir(t *testing.T) Dir {
	t.Helper()
	d, err := New("run-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Remove() })
	return d
}

// cloneRun makes the run's folder and clones the person's repository at src into it.
func cloneRun(t *testing.T, src string) (Dir, []Repository) {
	t.Helper()
	d := newDir(t)
	repos, err := d.Clone(context.Background(), src, branch)
	if err != nil {
		t.Fatal(err)
	}
	return d, repos
}

// marker writes a program that creates the file it returns when it runs, and passes its
// input through, as a filter does.
func marker(t *testing.T) (program, ran string) {
	t.Helper()
	dir := t.TempDir()
	ran = filepath.Join(dir, "ran")
	program = filepath.Join(dir, "marker.sh")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho \"$0 $*\" >> '"+ran+"'\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return program, ran
}

// plant sets keys in the copy's .git/config, and an include, that would each make git
// run program on this machine, and sets a filter in .gitattributes.
func plant(t *testing.T, work, program string) {
	t.Helper()
	hooks := filepath.Join(t.TempDir(), "hooks")
	for _, h := range []string{"pre-commit", "commit-msg", "prepare-commit-msg", "post-commit", "post-checkout", "post-merge", "pre-push", "reference-transaction", "post-index-change", "pre-auto-gc", "post-rewrite", "fetch-hook"} {
		link(t, hooks, h, program)
	}
	include := filepath.Join(t.TempDir(), "included")
	if err := os.WriteFile(include, []byte("[core]\n\tpager = "+program+"\n[diff]\n\texternal = "+program+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	set := [][2]string{
		{"core.hooksPath", hooks},
		{"core.fsmonitor", program},
		{"filter.mark.clean", program},
		{"filter.mark.smudge", program},
		{"filter.mark.process", program},
		{"filter.mark.required", "true"},
		{"core.sshCommand", program},
		{"include.path", include},
		{"uploadpack.packObjectsHook", program},
		{"core.alternateRefsCommand", program},
		{"remote.origin.url", "ext::" + program},
		{"commit.gpgSign", "true"},
		{"gpg.program", program},
		{"credential.helper", program},
		{"extensions.partialClone", "origin"},
		{"remote.origin.promisor", "true"},
		{"protocol.ext.allow", "always"},
	}
	for _, kv := range set {
		testGit(t, work, "config", kv[0], kv[1])
	}
	write(t, work, ".gitattributes", "* filter=mark\n")
}

// runSession runs the git session on this machine with sh, in place of the wall.
func runSession(t *testing.T, s GitSession) {
	t.Helper()
	if out, err := boundedSession(t, s); err != nil {
		t.Fatalf("git session: %v\n%s", err, out)
	}
}

// boundedSession runs s in a new process group, with a time limit, and kills that group at the
// end, so a regression that hangs fails the test and leaves no process behind.
func boundedSession(t *testing.T, s GitSession) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, s.Command, s.Args...)
	c.Dir, c.Env = s.Dir, s.Env
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Start()
	if err != nil {
		return "", err
	}
	defer func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }()
	err = c.Wait()
	return out.String(), err
}

// sessionGit sets the git session's PATH to the folder of this machine's git.
func sessionGit(t *testing.T) {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	old := sessionPath
	sessionPath = filepath.Dir(path) + ":/usr/bin:/bin"
	t.Cleanup(func() { sessionPath = old })
}

// runGit makes the git session for repos with out, and runs it.
func runGit(t *testing.T, d Dir, repos []Repository, out map[string]LeftOut) {
	t.Helper()
	s, err := d.GitSession(context.Background(), branch, "", repos, out)
	if err != nil {
		t.Fatal(err)
	}
	runSession(t, s)
}

// bringBack runs the git session for repos with out, checks the bundle of the person's
// repository, and fetches the run's branch into the person's repository at src.
func bringBack(t *testing.T, d Dir, src string, repos []Repository, out map[string]LeftOut) *Quarantine {
	t.Helper()
	ctx := context.Background()
	runGit(t, d, repos, out)
	qs, err := d.Quarantine(ctx, branch, repos)
	if err != nil {
		t.Fatal(err)
	}
	if qs[0] == nil {
		t.Fatal("no quarantine for the person's repository")
	}
	if err := qs[0].FetchInto(ctx, src, "refs/heads/"+branch); err != nil {
		t.Fatal(err)
	}
	return qs[0]
}

// files is the files of commit in the repository at dir.
func files(t *testing.T, dir, commit string) []string {
	t.Helper()
	return strings.Split(testGit(t, dir, "ls-tree", "-r", "--name-only", commit), "\n")
}

// bothChecks runs test once with the object checks of git fetch from a bundle, when
// this git has them, and once with git index-pack, as on a git older than 2.48.
func bothChecks(t *testing.T, test func(t *testing.T)) {
	for _, c := range []struct {
		name   string
		checks bool
	}{{"fetch", true}, {"index-pack", false}} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			old := bundleFetchChecks
			if c.checks && !old(context.Background()) {
				t.Skip("this git's fetch from a bundle checks no object")
			}
			bundleFetchChecks = func(context.Context) bool { return c.checks }
			t.Cleanup(func() { bundleFetchChecks = old })
			test(t)
		})
	}
}

// hasPrefix fails the test when err is nil or its text does not start with prefix.
func hasPrefix(t *testing.T, what string, err error, prefix string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("%s: error %v, want one that starts with %q", what, err, prefix)
	}
}

func TestNewStaysOutOfKeptFolders(t *testing.T) {
	isolate(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{cache, filepath.Join(cache, "qory", "runs", "run-1", "state")} {
		_, err := New("run-1", keep)
		hasPrefix(t, "keep out "+keep, err, "set XDG_STATE_HOME")
	}
	for _, id := range []string{"", ".", "..", "a/b", "-x", "a b"} {
		_, err := New(id)
		hasPrefix(t, "run id "+id, err, "give a run id")
	}
	d, err := New("run-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cache, "qory", "runs", "run-1"); d.Root() != want {
		t.Errorf("root %s, want %s", d.Root(), want)
	}
	_, err = New("run-1")
	hasPrefix(t, "second New", err, "remove ")
	if err := d.Remove(); err != nil {
		t.Fatal(err)
	}
}

// inodes is the device and inode of each file under dir.
func inodes(t *testing.T, dir string) map[[2]uint64]string {
	t.Helper()
	out := map[[2]uint64]string{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		out[[2]uint64{uint64(st.Dev), uint64(st.Ino)}] = path
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCloneSharesNoObjectFile(t *testing.T) {
	isolate(t)
	src, base := person(t)
	testGit(t, src, "gc", "--quiet")
	write(t, src, "untracked.txt", "not committed\n")
	further := filepath.Join(src, ".qory", "repositories", "example", "further")
	write(t, further, "f.txt", "further\n")
	testGit(t, further, "init", "--quiet", "-b", "main")
	furtherBase := commitAll(t, further, "further")
	testGit(t, further, "checkout", "--quiet", "--detach")

	d, repos := cloneRun(t, src)
	if len(repos) != 2 ||
		repos[0].Path != "" || repos[0].Base != base || repos[0].index != 0 ||
		repos[1].Path != ".qory/repositories/example/further" || repos[1].Base != furtherBase || repos[1].index != 1 {
		t.Errorf("repositories %+v", repos)
	}
	work := d.Work()
	if got := testGit(t, work, "rev-parse", "HEAD"); got != base {
		t.Errorf("copy at %s, want %s", got, base)
	}
	if got := testGit(t, work, "symbolic-ref", "HEAD"); got != "refs/heads/"+branch {
		t.Errorf("copy on %s, want refs/heads/%s", got, branch)
	}
	if _, err := os.Lstat(filepath.Join(work, "untracked.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy has the untracked file: %v", err)
	}
	if got := testGit(t, filepath.Join(work, ".qory", "repositories", "example", "further"), "rev-parse", "HEAD"); got != furtherBase {
		t.Errorf("further copy at %s, want %s", got, furtherBase)
	}
	// The copy comes from the bare source, which no run binds, and borrows no object. The
	// person has no origin, so it has none either.
	if out, err := gitCmd(work, "config", "--get", "remote.origin.url").CombinedOutput(); err == nil {
		t.Errorf("the copy has the origin %s, and the person has none", out)
	}
	if got := testGit(t, d.source(0), "rev-parse", "refs/heads/"+branch); got != base {
		t.Errorf("the bare source has the run's branch at %s, want %s", got, base)
	}
	if _, err := os.Lstat(filepath.Join(work, ".git", "objects", "info", "alternates")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy borrows objects: %v", err)
	}
	for _, dir := range []string{filepath.Join(src, ".git", "objects"), d.source(0)} {
		theirs := inodes(t, dir)
		for key, path := range inodes(t, filepath.Join(work, ".git", "objects")) {
			if other, ok := theirs[key]; ok {
				t.Errorf("%s shares its inode with %s", path, other)
			}
		}
	}
	_, err := d.Clone(context.Background(), src, branch)
	hasPrefix(t, "second Clone", err, "remove ")
}

func TestCloneOfAHeadOnNoBranch(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, "second.txt", "second\n")
	head := commitAll(t, src, "second")
	testGit(t, src, "checkout", "--quiet", "--detach")
	testGit(t, src, "reset", "--quiet", "--hard", "HEAD~1")
	write(t, src, "third.txt", "third\n")
	detached := commitAll(t, src, "third, on no branch")
	if detached == head {
		t.Fatal("the test made no commit on no branch")
	}
	d, repos := cloneRun(t, src)
	if repos[0].Base != detached || testGit(t, d.Work(), "rev-parse", "HEAD") != detached {
		t.Errorf("copy at %v, want %s", repos, detached)
	}
	if got := testGit(t, d.Work(), "symbolic-ref", "HEAD"); got != "refs/heads/"+branch {
		t.Errorf("copy on %s, want refs/heads/%s", got, branch)
	}
}

// filterProgram writes a filter that applies the sed script expr to its input, and
// appends a line to the file it returns each time it runs.
func filterProgram(t *testing.T, name, expr string) (program, ran string) {
	t.Helper()
	dir := t.TempDir()
	ran = filepath.Join(dir, "ran")
	program = filepath.Join(dir, name)
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho ran >> '"+ran+"'\nsed '"+expr+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return program, ran
}

func TestCloneUsesThePersonsFilterAndSubmodules(t *testing.T) {
	isolate(t)
	sessionGit(t)
	ctx := context.Background()
	src, _ := person(t)
	sub := filepath.Join(t.TempDir(), "sub")
	write(t, sub, "lib.txt", "lib\n")
	testGit(t, sub, "init", "--quiet", "-b", "main")
	subHead := commitAll(t, sub, "lib")
	testGit(t, src, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", sub, "libs/s")
	// What a Git LFS pointer is to the person's filter.
	write(t, src, ".gitattributes", "*.bin filter=fake\n")
	write(t, src, "big.bin", "pointer\n")
	write(t, src, "other.bin", "pointer\n")
	commitAll(t, src, "submodule and filter")

	smudge, smudged := filterProgram(t, "smudge.sh", "s/pointer/real/")
	clean, cleaned := filterProgram(t, "clean.sh", "s/real/pointer/")
	config := t.TempDir()
	write(t, config, "gitconfig", "[filter \"fake\"]\n\tsmudge = "+smudge+"\n\tclean = "+clean+"\n\trequired = true\n")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))

	d, repos := cloneRun(t, src)
	work := d.Work()
	if got := read(t, work, "big.bin"); got != "real\n" {
		t.Errorf("big.bin is %q in the copy, want what the person's filter writes", got)
	}
	if got := testGit(t, filepath.Join(work, "libs", "s"), "rev-parse", "HEAD"); got != subHead {
		t.Errorf("the submodule is at %s, want %s", got, subHead)
	}
	if got := read(t, work, "libs/s/lib.txt"); got != "lib\n" {
		t.Errorf("libs/s/lib.txt is %q", got)
	}
	for _, p := range []string{smudged, cleaned} {
		_ = os.Remove(p)
	}

	// The same filter in the copy's configuration, after the clone, runs in no later
	// step on this machine, and neither does the person's.
	program, ran := marker(t)
	testGit(t, work, "config", "filter.fake.smudge", program)
	testGit(t, work, "config", "filter.fake.clean", program)
	testGit(t, work, "config", "filter.fake.required", "true")
	write(t, work, "other.bin", "changed by the agent\n")
	write(t, filepath.Join(work, "libs", "s"), "lib.txt", "changed in the submodule\n")
	commitAll(t, filepath.Join(work, "libs", "s"), "a commit in the submodule")

	q := bringBack(t, d, src, repos, nil)
	if _, err := q.ActingChanges(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ran, smudged, cleaned} {
		if b, err := os.ReadFile(p); err == nil {
			t.Errorf("a filter ran on this machine after the clone:\n%s", b)
		}
	}
	if got := testGit(t, src, "show", q.Head+":big.bin"); got != "pointer" {
		t.Errorf("big.bin is %q at the run's commit, want the base commit's", got)
	}
	if got := testGit(t, src, "show", q.Head+":other.bin"); got != "changed by the agent" {
		t.Errorf("other.bin is %q at the run's commit", got)
	}
	if got := testGit(t, src, "rev-parse", q.Head+":libs/s"); got != subHead {
		t.Errorf("the run's commit has the submodule at %s, want %s", got, subHead)
	}
}

func TestCopyFolderKeepsLinksAsLinks(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	write(t, src, "a.txt", "a\n")
	write(t, src, "sub/run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(src, "sub", "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	link(t, src, "hosts", "/etc/hosts")
	link(t, src, "elsewhere", t.TempDir())
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	work := d.Work()
	if got := read(t, work, "a.txt"); got != "a\n" {
		t.Errorf("a.txt: %q", got)
	}
	if info, err := os.Stat(filepath.Join(work, "sub", "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("sub/run.sh lost its execute bit: %v, %v", info, err)
	}
	for _, l := range []string{"hosts", "elsewhere"} {
		if info, err := os.Lstat(filepath.Join(work, l)); err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s is no link in the copy: %v", l, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(work, "pipe")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy has the pipe: %v", err)
	}
	err := d.CopyFolder(context.Background(), src)
	hasPrefix(t, "second CopyFolder", err, "remove ")
}

func TestCopyFolderOfAFolderThatContainsTheRun(t *testing.T) {
	isolate(t)
	d := newDir(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	err = d.CopyFolder(context.Background(), cache)
	hasPrefix(t, "CopyFolder of the cache folder", err, "start the run in a folder that does not contain ")
	if _, err := os.Lstat(d.Work()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy was made: %v", err)
	}
}

// agentWork makes, in the copy at work, what an agent leaves: a commit that changes an
// acting key and a file qory sets back, and uncommitted work, files under .qory in two
// letter cases, a repository in the repository and a link qory wrote.
func agentWork(t *testing.T, work string) {
	t.Helper()
	write(t, work, "qory.yaml", "env:\n  A: \"1\"\n  TOKEN: example\n")
	write(t, work, "CLAUDE.md", "changed by the agent\n")
	commitAll(t, work, "agent's commit")
	write(t, work, "new.txt", "uncommitted\n")
	write(t, work, "README.md", "hello again\n")
	write(t, work, ".qory/harness.txt", "composed\n")
	write(t, work, ".QORY/upper.txt", "composed\n")
	write(t, work, "nested/inner/file.txt", "inside\n")
	testGit(t, filepath.Join(work, "nested", "inner"), "init", "--quiet")
	link(t, work, "AGENTS.md", ".qory/harness.txt")
}

func TestGitSessionBringsTheWorkBackAndRunsNothingPlanted(t *testing.T) {
	bothChecks(t, func(t *testing.T) {
		sessionGit(t)
		ctx := context.Background()
		src, base := person(t)
		d, repos := cloneRun(t, src)
		agentWork(t, d.Work())
		program, ran := marker(t)
		plant(t, d.Work(), program)

		if got := d.SessionMounts(); len(got) != 2 || got[0].Path != d.Work() || got[1].Path != d.Bundle() || got[0].ReadOnly || got[1].ReadOnly {
			t.Errorf("mounts %v, want the copy and the bundle folder, read-write", got)
		}
		q := bringBack(t, d, src, repos, map[string]LeftOut{"": {Links: []string{"AGENTS.md", "CLAUDE.md"}}})
		changes, err := q.ActingChanges(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(ran); err == nil {
			t.Fatalf("a planted program ran on this machine:\n%s", b)
		}
		want := []ActingChange{{File: "qory.yaml", Key: "env.TOKEN"}}
		if !slices.Equal(changes, want) {
			t.Errorf("acting changes %v, want %v", changes, want)
		}
		head := testGit(t, src, "rev-parse", "refs/heads/"+branch)
		if head != q.Head {
			t.Errorf("your repository has the run's branch at %s, want %s", head, q.Head)
		}
		if alt := read(t, q.Path, "objects/info/alternates"); alt != filepath.Join(d.source(0), "objects")+"\n" {
			t.Errorf("the quarantine borrows from %q, want the bare source", alt)
		}
		if got := testGit(t, src, "log", "--format=%an <%ae> %s", "-1", head); got != commitName+" <"+commitEmail+"> "+commitMessage {
			t.Errorf("last commit %q", got)
		}
		for file, want := range map[string]string{"new.txt": "uncommitted", "README.md": "hello again", "CLAUDE.md": "base", ".gitattributes": "* filter=mark"} {
			if got := testGit(t, src, "show", head+":"+file); got != want {
				t.Errorf("%s is %q at the run's commit, want %q", file, got, want)
			}
		}
		all := files(t, src, head)
		for _, out := range []string{".qory/harness.txt", ".QORY/upper.txt", "nested/inner/file.txt", "nested/inner", "AGENTS.md"} {
			if slices.Contains(all, out) {
				t.Errorf("the run's commit has %s:\n%v", out, all)
			}
		}
		if got := testGit(t, src, "rev-parse", "HEAD"); got != base {
			t.Errorf("your checkout moved to %s", got)
		}
		if got := testGit(t, src, "status", "--porcelain"); got != "" {
			t.Errorf("your checkout changed:\n%s", got)
		}

		// The control: git that opens the copy's .git runs the planted program, so the
		// checks above have teeth.
		_ = gitCmd(d.Work(), "status", "--porcelain").Run()
		if _, err := os.Stat(ran); err != nil {
			t.Errorf("git status in the copy did not run the planted program, so the test shows nothing: %v", err)
		}
		if err := d.Remove(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(d.Root()); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the run's folder is still there: %v", err)
		}
	})
}

func TestGitSessionBringsAFurtherRepositoryBack(t *testing.T) {
	bothChecks(t, func(t *testing.T) {
		sessionGit(t)
		ctx := context.Background()
		src, _ := person(t)
		further := filepath.Join(src, ".qory", "repositories", "example", "further")
		write(t, further, "f.txt", "further\n")
		testGit(t, further, "init", "--quiet", "-b", "main")
		furtherBase := commitAll(t, further, "further")
		d, repos := cloneRun(t, src)
		copied := filepath.Join(d.Work(), ".qory", "repositories", "example", "further")
		write(t, copied, "f.txt", "changed\n")
		commitAll(t, copied, "the agent's commit in a further repository")
		write(t, copied, "g.txt", "uncommitted\n")
		runGit(t, d, repos, nil)
		if _, err := os.Lstat(filepath.Join(d.Bundle(), "0.none")); err != nil {
			t.Errorf("no record of an empty change for the person's repository: %v", err)
		}
		qs, err := d.Quarantine(ctx, branch, repos)
		if err != nil {
			t.Fatal(err)
		}
		if len(qs) != 2 || qs[0] != nil || qs[1] == nil {
			t.Fatalf("quarantines %v, want none for the person's repository and one for the further one", qs)
		}
		if err := qs[1].FetchInto(ctx, further, "refs/heads/"+branch); err != nil {
			t.Fatal(err)
		}
		head := testGit(t, further, "rev-parse", "refs/heads/"+branch)
		if got := testGit(t, further, "rev-list", "--count", head, "^"+furtherBase); got != "2" {
			t.Errorf("%s commits past the base, want 2", got)
		}
		if got := testGit(t, further, "show", head+":g.txt"); got != "uncommitted" {
			t.Errorf("g.txt is %q", got)
		}
		if got := testGit(t, further, "rev-parse", "HEAD"); got != furtherBase {
			t.Errorf("the further repository's checkout moved to %s", got)
		}
	})
}

func TestGitSessionWithNoChangeWritesNoBundle(t *testing.T) {
	isolate(t)
	sessionGit(t)
	ctx := context.Background()
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	runGit(t, d, repos, nil)
	if _, err := d.Quarantine(ctx, branch, repos); !errors.Is(err, ErrNoBundle) {
		t.Errorf("error %v, want ErrNoBundle", err)
	}
	// A session that ended before it wrote anything brought nothing back, and says so.
	if err := os.Remove(filepath.Join(d.Bundle(), "0.none")); err != nil {
		t.Fatal(err)
	}
	_, err := d.Quarantine(ctx, branch, repos)
	hasPrefix(t, "no record", err, "the git session exited 0 and wrote no bundle")
	if errors.Is(err, ErrNoBundle) {
		t.Error("a session that wrote nothing counts as one with no change")
	}
	_, err = d.GitSession(ctx, branch, "", repos, nil)
	hasPrefix(t, "second GitSession", err, "remove ")
}

func TestGitSessionChecksItsArguments(t *testing.T) {
	isolate(t)
	d := newDir(t)
	ctx := context.Background()
	base := strings.Repeat("a", 40)
	main := []Repository{{Base: base}}
	for _, c := range []struct {
		branch string
		repos  []Repository
		out    LeftOut
		prefix string
	}{
		{"-x", main, LeftOut{}, "give a branch name"},
		{"a..b", main, LeftOut{}, "give a branch name"},
		{"main", []Repository{{Base: "HEAD"}}, LeftOut{}, "give the base commit"},
		{"main", []Repository{{Path: "../x", Base: base}}, LeftOut{}, "give each repository"},
		{"main", []Repository{{Path: "other/x", Base: base}}, LeftOut{}, "give each repository"},
		{"main", main, LeftOut{Links: []string{"../x"}}, "give each path left out"},
		{"main", main, LeftOut{Setup: []SetupPath{{Path: "/etc/x", State: "missing"}}}, "give each path left out"},
		{"main", main, LeftOut{Setup: []SetupPath{{Path: "a//b", State: "missing"}}}, "give each path left out"},
		{"main", main, LeftOut{Setup: []SetupPath{{Path: "a", State: "file:xyz"}}}, "give each setup path's state"},
		{"main", main, LeftOut{Setup: []SetupPath{{Path: "a", State: ""}}}, "give each setup path's state"},
	} {
		_, err := d.GitSession(ctx, c.branch, "", c.repos, map[string]LeftOut{c.repos[0].Path: c.out})
		hasPrefix(t, c.branch+" "+c.prefix, err, c.prefix)
	}
	// A path named -- is a path like any other: the script takes counts, not a
	// separator. A trailing / is dropped.
	_, err := d.GitSession(ctx, "main", "", main, map[string]LeftOut{"": {Links: []string{"--", "x"}, Setup: []SetupPath{{Path: "dir/", State: "other"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "rel='.' base=" + base + " icase=false links=2 setup=1\nset -- '--' 'x' 'dir' other\n"
	if got := read(t, d.Bundle(), "list-0"); got != want {
		t.Errorf("the list is %q, want %q", got, want)
	}
}

func TestGitSessionCommitsASetupPathTheAgentChanged(t *testing.T) {
	isolate(t)
	sessionGit(t)
	ctx := context.Background()
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	work := d.Work()
	// What the setup leaves.
	write(t, work, "setup/same.txt", "setup\n")
	write(t, work, "setup/edited.txt", "setup\n")
	write(t, work, "setup/tool", "#!/bin/sh\n")
	write(t, work, "setup-dir/made.txt", "setup\n")
	if err := os.Chmod(filepath.Join(work, "setup", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	link(t, work, "setup-link", "README.md")
	if err := os.Remove(filepath.Join(work, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	paths := []string{"setup/same.txt", "setup/edited.txt", "setup/tool", "setup-link", "CLAUDE.md", "setup/none", "setup-dir/"}
	states, err := d.SetupStates(ctx, repos[0], paths)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"file:", "file:", "exec:", "link:", "missing", "missing", "other"} {
		if !strings.HasPrefix(states[i].State, want) {
			t.Errorf("%s: state %s, want %s", paths[i], states[i].State, want)
		}
	}
	if states[6].Path != "setup-dir" {
		t.Errorf("path %q, want setup-dir", states[6].Path)
	}
	_, err = d.SetupStates(ctx, repos[0], []string{"../x"})
	hasPrefix(t, "SetupStates of ../x", err, "give each setup path")
	// What the agent does after.
	write(t, work, "setup/edited.txt", "edited by the agent\n")
	write(t, work, "setup/none", "written by the agent\n")
	write(t, work, "new.txt", "new\n")

	q := bringBack(t, d, src, repos, map[string]LeftOut{"": {Setup: states}})
	all := files(t, src, q.Head)
	for _, in := range []string{"setup/edited.txt", "setup/none", "new.txt", "CLAUDE.md"} {
		if !slices.Contains(all, in) {
			t.Errorf("the run's commit has no %s: %v", in, all)
		}
	}
	for _, out := range []string{"setup/same.txt", "setup/tool", "setup-link", "setup-dir/made.txt"} {
		if slices.Contains(all, out) {
			t.Errorf("the run's commit has %s, which the setup left: %v", out, all)
		}
	}
	if got := testGit(t, src, "show", q.Head+":setup/edited.txt"); got != "edited by the agent" {
		t.Errorf("setup/edited.txt is %q", got)
	}
}

// agentCommit commits a new file in the copy at work.
func agentCommit(t *testing.T, work string) {
	t.Helper()
	write(t, work, "agent.txt", "committed\n")
	commitAll(t, work, "agent")
}

func TestGitSessionReadsPackedRefs(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	agentCommit(t, d.Work())
	testGit(t, d.Work(), "pack-refs", "--all")
	if _, err := os.Lstat(filepath.Join(d.Work(), ".git", "refs", "heads", "qory", "run-1")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the branch still has a loose ref: %v", err)
	}
	q := bringBack(t, d, src, repos, nil)
	if all := files(t, src, q.Head); !slices.Contains(all, "agent.txt") {
		t.Errorf("the run's commit has no agent.txt: %v", all)
	}
}

func TestGitSessionReadsReftable(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	agentCommit(t, d.Work())
	if out, err := gitCmd(d.Work(), "refs", "migrate", "--ref-format=reftable").CombinedOutput(); err != nil {
		t.Skipf("this git does not move a repository to reftable: %v\n%s", err, out)
	}
	if got := testGit(t, d.Work(), "rev-parse", "--show-ref-format"); got != "reftable" {
		t.Fatalf("ref format %s, want reftable", got)
	}
	q := bringBack(t, d, src, repos, nil)
	if all := files(t, src, q.Head); !slices.Contains(all, "agent.txt") {
		t.Errorf("the run's commit has no agent.txt: %v", all)
	}
}

func TestOpenBundleTakesARegularFileAlone(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	write(t, dir, "regular", "bundle")
	link(t, dir, "link", regular)
	pipe := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "large")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxBundle + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, p := range []string{filepath.Join(dir, "link"), pipe, large, dir, filepath.Join(dir, "missing")} {
		f, err := OpenBundle(p)
		if f != nil {
			f.Close()
		}
		hasPrefix(t, filepath.Base(p), err, "the run's bundle ")
	}
	f, err = OpenBundle(regular)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// handBundle sets the run's branch in the copy of d to commit, and writes its bundle
// past base where the git session writes the one of the person's repository.
func handBundle(t *testing.T, d Dir, commit, base string) {
	t.Helper()
	testGit(t, d.Work(), "update-ref", "refs/heads/"+branch, commit)
	if err := os.Mkdir(d.Bundle(), 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, d.Work(), "bundle", "create", "--quiet", filepath.Join(d.Bundle(), "0.bundle"), "refs/heads/"+branch, "^"+base)
}

// noRunCommit fails the test when the person's repository at src has commit, or a ref
// other than main.
func noRunCommit(t *testing.T, src, commit string) {
	t.Helper()
	if gitCmd(src, "cat-file", "-e", commit).Run() == nil {
		t.Errorf("the commit %s reached your repository", commit)
	}
	if got := testGit(t, src, "for-each-ref", "--format=%(refname)"); got != "refs/heads/main" {
		t.Errorf("your repository has refs %q", got)
	}
}

func TestCorruptObjectFailsTheQuarantineFetch(t *testing.T) {
	bothChecks(t, func(t *testing.T) {
		ctx := context.Background()
		src, base := person(t)
		d, repos := cloneRun(t, src)
		// A commit with no author line: git's object check stops it.
		tree := testGit(t, d.Work(), "rev-parse", "HEAD^{tree}")
		bad := gitIn(t, d.Work(), "tree "+tree+"\nparent "+base+"\n\nno author\n", "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
		handBundle(t, d, bad, base)
		_, err := d.Quarantine(ctx, branch, repos)
		hasPrefix(t, "corrupt commit", err, "the run's bundle for ")
		noRunCommit(t, src, bad)
	})
}

func TestTopEntryFailsTheQuarantine(t *testing.T) {
	for _, name := range []string{".qory", ".QORY", ".Qory\u200c", ".GIT", ".git"} {
		t.Run(name, func(t *testing.T) {
			bothChecks(t, func(t *testing.T) {
				ctx := context.Background()
				src, base := person(t)
				d, repos := cloneRun(t, src)
				work := d.Work()
				blob := gitIn(t, work, "[core]\n\tfsmonitor = /bin/false\n", "hash-object", "-w", "--stdin")
				inner := gitIn(t, work, "100644 blob "+blob+"\tconfig\n", "mktree")
				top := testGit(t, work, "ls-tree", "HEAD") + "\n040000 tree " + inner + "\t" + name + "\n"
				tree := gitIn(t, work, top, "mktree")
				commit := testGit(t, work, "commit-tree", tree, "-p", base, "-m", "adds "+name)
				handBundle(t, d, commit, base)
				_, err := d.Quarantine(ctx, branch, repos)
				hasPrefix(t, name, err, "the run's ")
				noRunCommit(t, src, commit)
			})
		})
	}
}

func TestFetchIntoSaysWhatToDo(t *testing.T) {
	isolate(t)
	sessionGit(t)
	ctx := context.Background()
	src, base := person(t)
	testGit(t, src, "checkout", "--quiet", "-b", "side")
	write(t, src, "side.txt", "side\n")
	side := commitAll(t, src, "side")
	testGit(t, src, "checkout", "--quiet", "main")
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "new.txt", "new\n")
	runGit(t, d, repos, nil)
	qs, err := d.Quarantine(ctx, branch, repos)
	if err != nil {
		t.Fatal(err)
	}
	q := qs[0]

	err = q.FetchInto(ctx, src, "refs/heads/main")
	hasPrefix(t, "checked-out branch", err, "give the run's commits a ref other than refs/heads/main")
	if got := testGit(t, src, "rev-parse", "refs/heads/main"); got != base {
		t.Errorf("main moved to %s", got)
	}
	err = q.FetchInto(ctx, src, "refs/heads/side")
	hasPrefix(t, "branch on another line", err, "delete refs/heads/side in ")
	if err == nil || !strings.Contains(err.Error(), q.Path) {
		t.Errorf("the error %v does not say the quarantine's path %s", err, q.Path)
	}
	if got := testGit(t, src, "rev-parse", "refs/heads/side"); got != side {
		t.Errorf("side moved to %s", got)
	}
	if _, err := os.Lstat(q.Path); err != nil {
		t.Errorf("the quarantine is gone after a failed fetch: %v", err)
	}
	err = q.FetchInto(ctx, src, "main")
	hasPrefix(t, "short ref", err, "give a full ref name")

	// A second fetch, and a second quarantine, work.
	for range 2 {
		if err := q.FetchInto(ctx, src, "refs/heads/"+branch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Quarantine(ctx, branch, repos); err != nil {
		t.Fatal(err)
	}
}

// actingRun commits the files of person, by path, in the person's repository, clones
// it, lets change edit the copy, and returns the acting changes of the run's head.
func actingRun(t *testing.T, person map[string]string, change func(work string)) []ActingChange {
	t.Helper()
	sessionGit(t)
	src := personWith(t, person)
	d, repos := cloneRun(t, src)
	change(d.Work())
	q := bringBack(t, d, src, repos, nil)
	changes, err := q.ActingChanges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

// personWith is [person] with the files of files, by path, committed on top.
func personWith(t *testing.T, files map[string]string) string {
	t.Helper()
	src, _ := person(t)
	for rel, text := range files {
		write(t, src, rel, text)
	}
	commitAll(t, src, "qory files")
	return src
}

func TestActingChangesFollowAStackInTheRepository(t *testing.T) {
	isolate(t)
	changes := actingRun(t, map[string]string{
		"qory.yaml":                   "harness:\n  extends:\n    path: stacks/base\n",
		"stacks/base/qory-stack.yaml": "modules:\n  - source:\n      git: https://example.com/a.git\n",
	}, func(work string) {
		write(t, work, "stacks/base/qory-stack.yaml", "modules:\n  - source:\n      git: https://example.com/b.git\n")
		write(t, work, "qory-stack.yaml", "- not a mapping\n")
		write(t, work, "Harness.yaml", "env:\n  B: \"2\"\n")
	})
	want := []ActingChange{
		{File: "Harness.yaml", Key: "env.B"},
		{File: "qory-stack.yaml"},
		{File: "stacks/base/qory-stack.yaml", Key: "modules[0].source.git"},
	}
	if !slices.Equal(changes, want) {
		t.Errorf("acting changes %v, want %v", changes, want)
	}
}

func TestActingChangesResolveNothingOnThisMachine(t *testing.T) {
	isolate(t)
	// A folder on this machine with a stack file in it: a check that resolved the run's
	// link here would read it.
	elsewhere := t.TempDir()
	write(t, elsewhere, "base/qory-stack.yaml", "modules:\n  - source:\n      git: https://example.com/a.git\n")
	changes := actingRun(t, map[string]string{
		"qory.yaml":                   "harness:\n  extends:\n    path: stacks/base\n",
		"stacks/base/qory-stack.yaml": "modules:\n  - source:\n      git: https://example.com/a.git\n",
	}, func(work string) {
		write(t, work, "qory.yaml", "harness:\n  extends:\n    path: ./stacks/base\n")
		if err := os.RemoveAll(filepath.Join(work, "stacks")); err != nil {
			t.Fatal(err)
		}
		link(t, work, "stacks", elsewhere)
		link(t, work, "docs/notes", "../README.md")
	})
	want := []ActingChange{
		{File: "docs/notes"},
		{File: "qory.yaml", Key: "harness.extends.path"},
		{File: "stacks"},
		{File: "stacks/base/qory-stack.yaml"},
		{File: "stacks/base/qory-stack.yaml", Key: "modules[0].source.git"},
	}
	if !slices.Equal(changes, want) {
		t.Errorf("acting changes %v, want %v", changes, want)
	}
}

// copyBackFixture makes the person's folder and copies it into a new run's folder.
func copyBackFixture(t *testing.T) (string, Dir) {
	t.Helper()
	dest := t.TempDir()
	write(t, dest, "a.txt", "a\n")
	write(t, dest, "same.txt", "same\n")
	write(t, dest, "gone.txt", "the run deletes it\n")
	write(t, dest, "edited-gone.txt", "the run deletes it, the person edits it\n")
	write(t, dest, "both.txt", "both edit it\n")
	write(t, dest, "linked/f.txt", "under a folder the person turns into a link\n")
	write(t, dest, "half/HEAD", "ref: refs/heads/main\n")
	write(t, dest, "qory.yaml", "env:\n  A: \"1\"\n")
	if err := os.Chmod(filepath.Join(dest, "a.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	link(t, dest, "out", t.TempDir())
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	return dest, d
}

func TestCopyBackLeavesOutLinksGitAndQory(t *testing.T) {
	isolate(t)
	dest, d := copyBackFixture(t)
	elsewhere, err := os.Readlink(filepath.Join(dest, "out"))
	if err != nil {
		t.Fatal(err)
	}
	work := d.Work()
	// What the run does.
	write(t, work, "a.txt", "changed\n")
	write(t, work, "b.txt", "new\n")
	write(t, work, "sub/c.txt", "new in a folder\n")
	write(t, work, "both.txt", "the run's edit\n")
	write(t, work, ".git/config", "[core]\n\tfsmonitor = /bin/false\n")
	write(t, work, "sub/.Git/HEAD", "ref: refs/heads/main\n")
	write(t, work, "sub/.g\u200cit/HEAD", "ref: refs/heads/main\n")
	write(t, work, "fake/HEAD", "ref: refs/heads/main\n")
	write(t, work, "fake/objects/x", "x\n")
	write(t, work, "half/refs/heads/main", "x\n")
	write(t, work, ".qory/qory.yaml", "env: {}\n")
	write(t, work, "qory.yaml", "env:\n  A: \"2\"\n")
	if err := os.Remove(filepath.Join(work, "out")); err != nil {
		t.Fatal(err)
	}
	write(t, work, "out/planted.txt", "planted\n")
	link(t, work, "hosts", "/etc/hosts")
	for _, p := range []string{"gone.txt", "edited-gone.txt", "linked/f.txt"} {
		if err := os.Remove(filepath.Join(work, p)); err != nil {
			t.Fatal(err)
		}
	}
	// What the person does meanwhile.
	write(t, dest, "both.txt", "the person's edit\n")
	write(t, dest, "edited-gone.txt", "the person's edit\n")
	moved := t.TempDir()
	write(t, moved, "f.txt", "under a folder the person turns into a link\n")
	if err := os.RemoveAll(filepath.Join(dest, "linked")); err != nil {
		t.Fatal(err)
	}
	link(t, dest, "linked", moved)

	r, err := d.CopyBack(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what      string
		got, want []string
	}{
		{"copied", r.Copied, []string{"a.txt", "b.txt", "sub/c.txt"}},
		{"deleted", r.Deleted, []string{"gone.txt"}},
		{"conflicts", r.Conflicts, []string{"both.txt", "edited-gone.txt"}},
		{"skipped", r.Skipped, []string{".git", ".qory", "fake", "half", "hosts", "linked", "out", "sub/.Git", "sub/.g\u200cit"}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s %q, want %q", c.what, c.got, c.want)
		}
	}
	if want := []ActingChange{{File: "qory.yaml", Key: "env.A"}}; !slices.Equal(r.Acting, want) {
		t.Errorf("acting changes %v, want %v", r.Acting, want)
	}
	for rel, want := range map[string]string{
		"a.txt":           "changed\n",
		"both.txt":        "the person's edit\n",
		"edited-gone.txt": "the person's edit\n",
		"gone.txt":        "",
		"qory.yaml":       "env:\n  A: \"1\"\n",
	} {
		if got := read(t, dest, rel); got != want {
			t.Errorf("%s is %q, want %q", rel, got, want)
		}
	}
	if got := read(t, moved, "f.txt"); got == "" {
		t.Error("the copy-back deleted a file through your link")
	}
	if info, err := os.Stat(filepath.Join(dest, "a.txt")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("a.txt changed its mode: %v, %v", info, err)
	}
	for _, p := range []string{".git", ".qory", "hosts", "sub/.Git", "sub/.g\u200cit", "fake", "half/refs"} {
		if _, err := os.Lstat(filepath.Join(dest, p)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s reached your folder: %v", p, err)
		}
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("the copy-back wrote through your link: %v", entries)
	}
}

func TestCopyBackStopsPastItsBudget(t *testing.T) {
	isolate(t)
	for _, c := range []struct {
		files int
		bytes int64
	}{{1, 1 << 20}, {100, 5}} {
		dest, d := copyBackFixture(t)
		write(t, d.Work(), "b.txt", "new\n")
		write(t, d.Work(), "c.txt", "new\n")
		r, err := d.copyBackWith(context.Background(), dest, c.files, c.bytes)
		hasPrefix(t, "past the budget", err, "copy the rest from ")
		if len(r.Copied) >= 2 {
			t.Errorf("copied %v past a budget of %d files and %d bytes", r.Copied, c.files, c.bytes)
		}
		if err := d.Remove(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoveDeletesFoldersWithNoWriteOrReadBit(t *testing.T) {
	isolate(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes every folder")
	}
	d := newDir(t)
	write(t, d.Work(), "locked/file.txt", "x\n")
	write(t, d.Work(), "locked/closed/file.txt", "x\n")
	outside := t.TempDir()
	link(t, d.Work(), "locked/out", outside)
	if err := os.Chmod(filepath.Join(d.Work(), "locked", "closed"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(d.Work(), "locked"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(d.Root()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the run's folder is still there: %v", err)
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() == 0o700 {
		t.Errorf("Remove followed a link: %v, %v", info, err)
	}
}

func TestGitHelpers(t *testing.T) {
	for _, c := range []struct {
		out  string
		want bool
	}{
		{"git version 2.39.5 (Apple Git-154)\n", false},
		{"git version 2.48.0\n", true},
		{"git version 2.55.1", true},
		{"git version 3.0", true},
		{"version 2.50", false},
	} {
		if got := atLeast(c.out, 2, 48); got != c.want {
			t.Errorf("atLeast(%q) = %v, want %v", c.out, got, c.want)
		}
	}
	if got := subcommand([]string{"-c", "a=b", "--no-pager", "-c", "fetch=x", "fetch", "--quiet"}); got != "fetch" {
		t.Errorf("subcommand %q, want fetch", got)
	}
}

// TestSetupChangesListsWhatTheSetupChanged is a setup that adds, changes and deletes files,
// and plants a monitor in the copy's .git: the list has the three paths and no .qory, and no
// program of the copy runs.
func TestSetupChangesListsWhatTheSetupChanged(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, "kept.txt", "first\n")
	write(t, src, "changed.txt", "first\n")
	write(t, src, "gone.txt", "first\n")
	commitAll(t, src, "more")
	d, repos := cloneRun(t, src)
	program, ran := marker(t)
	testGit(t, d.Work(), "config", "core.fsmonitor", program)
	write(t, d.Work(), "changed.txt", "the setup\n")
	write(t, d.Work(), "added/new.txt", "the setup\n")
	write(t, d.Work(), ".qory/harness/entry", "the compose\n")
	if err := os.Remove(filepath.Join(d.Work(), "gone.txt")); err != nil {
		t.Fatal(err)
	}
	got, err := d.SetupChanges(context.Background(), repos[0])
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{"added/new.txt", "changed.txt", "gone.txt"}; !slices.Equal(got, want) {
		t.Errorf("the setup changed %v, want %v", got, want)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("a program the copy's .git names ran on this machine")
	}
}

// TestAFileThatPointsToARepositoryIsNoBundle is a bundle file with gitdir: <path>: git fetch
// would open that repository, so the header check stops it first and no program of that
// repository runs.
func TestAFileThatPointsToARepositoryIsNoBundle(t *testing.T) {
	for _, target := range []string{"copy", "other"} {
		t.Run(target, func(t *testing.T) {
			isolate(t)
			src, _ := person(t)
			d, repos := cloneRun(t, src)
			program, ran := marker(t)
			at := filepath.Join(d.Work(), ".git")
			if target == "other" {
				other := t.TempDir()
				testGit(t, other, "init", "--quiet")
				at = filepath.Join(other, ".git")
			}
			testGit(t, filepath.Dir(at), "config", "uploadpack.packObjectsHook", program)
			testGit(t, filepath.Dir(at), "config", "core.fsmonitor", program)
			if err := os.Mkdir(d.Bundle(), 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, d.Bundle(), "0.bundle", "gitdir: "+at+"\n")
			_, err := d.Quarantine(context.Background(), branch, repos)
			if err == nil || !strings.Contains(err.Error(), "header") {
				t.Errorf("err = %v, want the header check", err)
			}
			if data, err := os.ReadFile(ran); err == nil {
				t.Errorf("a program ran: %s", data)
			}
		})
	}
}

// TestTheGitSessionFetchesNoMissingObject is a copy set up as a partial clone from an ext::
// address with the branch at a missing object: the session fails, and the program does not run.
func TestTheGitSessionFetchesNoMissingObject(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	program, ran := marker(t)
	for _, kv := range [][2]string{
		{"extensions.partialClone", "origin"},
		{"remote.origin.promisor", "true"},
		{"remote.origin.url", "ext::" + program},
		{"protocol.ext.allow", "always"},
	} {
		testGit(t, d.Work(), "config", kv[0], kv[1])
	}
	write(t, d.Work(), ".git/refs/heads/"+branch, strings.Repeat("a", 40)+"\n")
	s, err := d.GitSession(context.Background(), branch, "", repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := boundedSession(t, s); err == nil {
		t.Errorf("the session passed:\n%s", out)
	}
	if data, err := os.ReadFile(ran); err == nil {
		t.Errorf("a program ran: %s", data)
	}
}

// TestTheGitSessionTakesManySetupPaths is 10000 setup paths, over the limit of one argument of
// sh on Linux, and paths with a quote and a line break: each stays out of the commit.
func TestTheGitSessionTakesManySetupPaths(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	const many = 10000
	for i := range many {
		write(t, d.Work(), "gen/f"+strconv.Itoa(i)+"-"+strings.Repeat("x", 40), "x\n")
	}
	odd := []string{"it's a file", "line\nbreak", "sp ace"}
	paths := []string{"gen"}
	for _, p := range odd {
		write(t, d.Work(), p, "x\n")
		paths = append(paths, p)
	}
	for i := range many {
		paths = append(paths, "gen/f"+strconv.Itoa(i)+"-"+strings.Repeat("x", 40))
	}
	states, err := d.SetupStates(context.Background(), repos[0], paths)
	if err != nil {
		t.Fatal(err)
	}
	write(t, d.Work(), "agent.txt", "the agent\n")
	q := bringBack(t, d, src, repos, map[string]LeftOut{"": {Setup: states}})
	got := strings.Split(testGit(t, src, "ls-tree", "-r", "--name-only", q.Head), "\n")
	if !slices.Contains(got, "agent.txt") {
		t.Errorf("the commit has no agent.txt: %d files", len(got))
	}
	for _, f := range got {
		if strings.HasPrefix(f, "gen/") || strings.Contains(f, "sp ace") || strings.Contains(f, "it") && strings.Contains(f, "file") {
			t.Errorf("the commit has the setup's %s", f)
			break
		}
	}
}

// TestCommitsOnAnotherBranchKeepTheirMessages is an agent that checks out another branch and
// commits twice: the bundle has both commits, and what it left uncommitted after them.
func TestCommitsOnAnotherBranchKeepTheirMessages(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "checkout", "--quiet", "-b", "feature")
	write(t, d.Work(), "one.txt", "1\n")
	commitAll(t, d.Work(), "first of the agent")
	write(t, d.Work(), "two.txt", "2\n")
	commitAll(t, d.Work(), "second of the agent")
	write(t, d.Work(), "left.txt", "left\n")
	q := bringBack(t, d, src, repos, nil)
	log := testGit(t, src, "log", "--format=%s", "-n", "3", q.Head)
	for _, want := range []string{"first of the agent", "second of the agent", commitMessage} {
		if !strings.Contains(log, want) {
			t.Errorf("the log has no %q:\n%s", want, log)
		}
	}
}

// TestAHeadFromElsewhereUsesTheBranch is an agent that checks out a commit that does not
// descend from the base commit: the bundle has the run's branch.
func TestAHeadFromElsewhereUsesTheBranch(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	testGit(t, d.Work(), "checkout", "--quiet", "--orphan", "elsewhere")
	commitAll(t, d.Work(), "unrelated")
	q := bringBack(t, d, src, repos, nil)
	if log := testGit(t, src, "log", "--format=%s", q.Head); !strings.Contains(log, "on the branch") {
		t.Errorf("the log has no commit of the branch:\n%s", log)
	}
}

// TestPersonalIgnoreRulesStayInEffect is a person's .git/info/exclude and global excludes file:
// a file they cover stays out of the commit.
func TestPersonalIgnoreRulesStayInEffect(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	write(t, src, ".git/info/exclude", "*.mine\n")
	config := t.TempDir()
	write(t, config, "global-ignore", "*.global\n")
	write(t, config, "gitconfig", "[core]\n\texcludesFile = "+filepath.Join(config, "global-ignore")+"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "a.mine", "x\n")
	write(t, d.Work(), "b.global", "x\n")
	write(t, d.Work(), "c.txt", "x\n")
	q := bringBack(t, d, src, repos, nil)
	got := files(t, src, q.Head)
	if slices.Contains(got, "a.mine") || slices.Contains(got, "b.global") || !slices.Contains(got, "c.txt") {
		t.Errorf("the commit has %v", got)
	}
}

// TestTheCopyShowsThePersonsOrigin is a person's repository with an origin with a password, a
// remote branch and a local branch: the copy has the address with no password, the remote
// branch, no ref of the local branch, and the repository's user.
func TestTheCopyShowsThePersonsOrigin(t *testing.T) {
	isolate(t)
	src, base := person(t)
	testGit(t, src, "config", "remote.origin.url", "https://me:secret@git.example.com/acme/app.git")
	testGit(t, src, "config", "user.name", "The Person")
	testGit(t, src, "config", "user.email", "person@example.com")
	testGit(t, src, "update-ref", "refs/remotes/origin/main", base)
	testGit(t, src, "update-ref", "refs/remotes/origin/release", base)
	testGit(t, src, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	testGit(t, src, "branch", "local-only", base)
	d, _ := cloneRun(t, src)
	if got := testGit(t, d.Work(), "config", "remote.origin.url"); got != "https://git.example.com/acme/app.git" {
		t.Errorf("remote.origin.url = %s", got)
	}
	refs := testGit(t, d.Work(), "for-each-ref", "--format=%(refname)", "refs/remotes/")
	if strings.Contains(refs, "local-only") || !strings.Contains(refs, "refs/remotes/origin/release") {
		t.Errorf("the remote refs are:\n%s", refs)
	}
	if got := testGit(t, d.Work(), "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/main" {
		t.Errorf("origin/HEAD = %s", got)
	}
	if got := testGit(t, d.Work(), "config", "user.name"); got != "The Person" {
		t.Errorf("user.name = %s", got)
	}
}

// TestWithoutCredentials is an address per kind.
func TestWithoutCredentials(t *testing.T) {
	for raw, want := range map[string]string{
		"https://me:secret@host.example/a.git":       "https://host.example/a.git",
		"https://token@host.example/a.git":           "https://host.example/a.git",
		"HTTPS://me:secret@host.example/a.git":       "HTTPS://host.example/a.git",
		"https://me:se%40cret@host.example/a.git":    "https://host.example/a.git",
		"https://me:se^cret@host.example/a.git":      "https://host.example/a.git",
		"https://me:se%ZZcret@host.example/a.git":    "https://host.example/a.git",
		"https://me:se cret@host.example/a.git":      "https://host.example/a.git",
		"https://me:a@b@host.example:8443/a.git":     "https://host.example:8443/a.git",
		"https://host.example/a.git?private_token=x": "https://host.example/a.git",
		"https://host.example/a.git#x":               "https://host.example/a.git",
		"ftps://token@host.example/a.git":            "ftps://host.example/a.git",
		"git://me@host.example/a.git":                "git://host.example/a.git",
		"ssh://git:secret@host.example/a.git":        "ssh://git@host.example/a.git",
		"ssh://git@host.example/a.git":               "ssh://git@host.example/a.git",
		"git+ssh://git:secret@host.example/a.git":    "git+ssh://git@host.example/a.git",
		"SSH://git:secret@[::1]:2222/a.git":          "SSH://git@[::1]:2222/a.git",
		"file:///srv/a.git":                          "file:///srv/a.git",
		"git@host.example:a.git":                     "git@host.example:a.git",
		"gh:acme/a.git":                              "gh:acme/a.git",
		"/srv/a.git":                                 "/srv/a.git",
		"/srv/a b@c.git":                             "/srv/a b@c.git",
		"../a.git":                                   "../a.git",
		// Each of these can have a password in it, so the copy gets no address.
		"https://me:se/cret@host.example/a.git": "",
		"https://me:se#cret@host.example/a.git": "",
		"https://me:se?cret@host.example/a.git": "",
		"https://me:secret/a.git":               "",
		"https://me:secret":                     "",
		"https://me:se]cret@ho st/a.git":        "",
		"https://host.example/a@b.git":          "",
		"https://host.example/a.git?x=a@b":      "",
		"me:secret@host.example:a.git":          "",
		"https://host.example/a\n.git":          "",
	} {
		if got := withoutCredentials(raw); got != want {
			t.Errorf("withoutCredentials(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestTheCopyHasNoCredentialOfTheRemote is a repository with a credential in each setting of a
// remote: no file under the run's folder has one, and the copy's origin is the address without it,
// or no address when a credential could be left in it.
func TestTheCopyHasNoCredentialOfTheRemote(t *testing.T) {
	for origin, want := range map[string]string{
		"gh:acme/a.git":                         "gh:acme/a.git",
		"https://me:se^LEAK@host.example/a.git": "https://host.example/a.git",
		"https://me:se/LEAK@host.example/a.git": "",
		"https://host.example/a.git?t=LEAK":     "https://host.example/a.git",
	} {
		// A test folder has the name of its subtest, so that name has no credential in it.
		t.Run("", func(t *testing.T) {
			isolate(t)
			src, _ := person(t)
			testGit(t, src, "remote", "add", "origin", origin)
			testGit(t, src, "config", "url.https://me:LEAK@host.example/.insteadOf", "gh:")
			testGit(t, src, "config", "remote.origin.pushurl", "https://me:LEAK@host.example/a.git")
			testGit(t, src, "config", "http.extraHeader", "Authorization: LEAK")
			testGit(t, src, "config", "lfs.url", "https://me:LEAK@host.example/a.git/info/lfs")
			testGit(t, src, "config", "credential.helper", "LEAK")
			d, _ := cloneRun(t, src)
			if out, _ := exec.Command("grep", "-rl", "LEAK", d.Root()).CombinedOutput(); len(out) > 0 {
				t.Errorf("files under the run's folder with a credential:\n%s", out)
			}
			got := ""
			if c := gitCmd(d.Work(), "config", "--get", "remote.origin.url"); true {
				out, _ := c.Output()
				got = strings.TrimSpace(string(out))
			}
			if got != want {
				t.Errorf("the copy's origin is %q, want %q", got, want)
			}
		})
	}
}

// TestTheCheckoutGetsThePersonsLFSSettings is a repository with LFS objects and an endpoint: a
// stand-in filter records the options git passed to the checkout. git-lfs itself does not run.
func TestTheCheckoutGetsThePersonsLFSSettings(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, ".gitattributes", "*.bin filter=fake\n")
	write(t, src, "big.bin", "pointer\n")
	commitAll(t, src, "a filtered file")
	write(t, src, ".git/lfs/objects/keep", "x\n")
	testGit(t, src, "config", "lfs.url", "https://lfs.example/acme/app")
	seen := filepath.Join(t.TempDir(), "seen")
	smudge := filepath.Join(t.TempDir(), "smudge.sh")
	if err := os.WriteFile(smudge, []byte("#!/bin/sh\nprintf '%s %s %s %s\\n' \"$GIT_CONFIG_KEY_0\" \"$GIT_CONFIG_VALUE_0\" \"$GIT_CONFIG_KEY_1\" \"$GIT_CONFIG_VALUE_1\" >> '"+seen+"'\nprintf 'line: %s\\n' \"$GIT_CONFIG_PARAMETERS\" >> '"+seen+"'\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := t.TempDir()
	write(t, config, "gitconfig", "[filter \"fake\"]\n\tsmudge = "+smudge+"\n\trequired = true\n")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	cloneRun(t, src)
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lfs.storage", filepath.Join(".git", "lfs"), "lfs.url", "https://lfs.example/acme/app"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the filter's options have no %s:\n%s", want, data)
		}
	}
	// git's -c options reach a filter here; a process list shows them on git's command line.
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, "line: ") && strings.Contains(line, "lfs") {
			t.Errorf("an LFS setting is on git's command line: %s", line)
		}
	}
}

// TestAFilterThatCannotRunStopsTheClone is a filter set as required whose program fails: the
// clone stops, so no run works on a checkout the filter did not write.
func TestAFilterThatCannotRunStopsTheClone(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, ".gitattributes", "*.bin filter=fake\n")
	write(t, src, "big.bin", "pointer\n")
	commitAll(t, src, "a filtered file")
	config := t.TempDir()
	write(t, config, "gitconfig", "[filter \"fake\"]\n\tsmudge = /bin/false\n\trequired = true\n")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	d := newDir(t)
	_, err := d.Clone(context.Background(), src, branch)
	if err == nil || !strings.HasPrefix(err.Error(), "check that git can check out ") {
		t.Errorf("err = %v", err)
	}
}

// TestAnIgnoredLeftOutPathStopsNothing is a .gitignore that lists .qory and a link of the
// compose: git add exits 1 for such a path, and the session goes on with the rest.
func TestAnIgnoredLeftOutPathStopsNothing(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	write(t, src, ".gitignore", ".qory\nlinked\n")
	testGit(t, src, "add", ".gitignore")
	commitAll(t, src, "ignore")
	d, repos := cloneRun(t, src)
	write(t, d.Work(), ".qory/home/x", "x\n")
	link(t, d.Work(), "linked", ".qory/home/x")
	write(t, d.Work(), "work.txt", "the run\n")
	q := bringBack(t, d, src, repos, map[string]LeftOut{"": {Links: []string{"linked"}}})
	all := files(t, src, q.Head)
	if !slices.Contains(all, "work.txt") || slices.Contains(all, "linked") || slices.Contains(all, ".qory/home/x") {
		t.Errorf("the commit has: %v", all)
	}
}

// TestAPartialCloneStops is a partial clone: the clone stops with the commands that fetch the
// missing objects, and after those commands it passes.
func TestAPartialCloneStops(t *testing.T) {
	isolate(t)
	origin, _ := person(t)
	testGit(t, origin, "config", "uploadpack.allowFilter", "true")
	src := filepath.Join(t.TempDir(), "partial")
	testGit(t, t.TempDir(), "-c", "protocol.file.allow=always", "clone", "--quiet", "--filter=blob:none", "file://"+origin, src)
	d := newDir(t)
	_, err := d.Clone(context.Background(), src, branch)
	if err == nil || !strings.Contains(err.Error(), "git fetch --refetch origin") {
		t.Fatalf("err = %v", err)
	}
	testGit(t, src, "config", "--unset", "remote.origin.partialclonefilter")
	testGit(t, src, "-c", "protocol.file.allow=always", "fetch", "--quiet", "--refetch", "origin")
	testGit(t, src, "config", "--unset", "remote.origin.promisor")
	again, err := New("run-3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Remove() })
	if _, err := again.Clone(context.Background(), src, branch); err != nil {
		t.Errorf("after the commands: %v", err)
	}
}

// TestABranchAboveTheRunsBranchStops is a person's branch named qory: the run's branch cannot
// go below it, and the line has the rename as its fix.
func TestABranchAboveTheRunsBranchStops(t *testing.T) {
	isolate(t)
	src, base := person(t)
	testGit(t, src, "branch", "qory", base)
	d := newDir(t)
	_, err := d.Clone(context.Background(), src, branch)
	if err == nil || !strings.HasPrefix(err.Error(), "rename your branch qory in ") {
		t.Errorf("err = %v", err)
	}
}

// TestAFurtherRepositoryGetsItsSubmodules is a repository under .qory/repositories with a
// submodule: the copy has the submodule's files.
func TestAFurtherRepositoryGetsItsSubmodules(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	further, sub := withSubmodule(t)
	at := filepath.Join(src, ".qory", "repositories")
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(further, filepath.Join(at, "other")); err != nil {
		t.Fatal(err)
	}
	d, repos := cloneRun(t, src)
	if len(repos) != 2 {
		t.Fatalf("repos = %+v", repos)
	}
	if got := read(t, filepath.Join(d.Work(), ".qory", "repositories", "other"), sub+"/lib.txt"); got != "lib\n" {
		t.Errorf("the submodule's file has %q", got)
	}
}

// TestAKeptCopyRunsNothingInAGitStatus is a copy with every setting and hook that would run a
// program, a planted bare repository at the top and below it, a link named .git in a folder,
// and a bundle folder with a git folder. After [Dir.Disarm], git status in each runs nothing.
func TestAKeptCopyRunsNothingInAGitStatus(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	program, ran := marker(t)
	plant(t, d.Work(), program)
	outside := t.TempDir()
	testGit(t, outside, "init", "--quiet")
	testGit(t, outside, "config", "core.fsmonitor", program)
	link(t, d.Work(), "sub/.git", filepath.Join(outside, ".git"))
	for _, f := range []string{"bare/HEAD", "bare/objects/x", "bare/refs/x"} {
		write(t, d.Work(), f, "ref: refs/heads/main\n")
	}
	write(t, d.Work(), "bare/config", "[core]\n\tfsmonitor = "+program+"\n")
	write(t, d.Work(), "HEAD", "ref: refs/heads/main\n")
	write(t, d.Work(), "objects/x", "x\n")
	write(t, d.Bundle(), "git-0/HEAD", "ref: refs/heads/main\n")
	write(t, d.Bundle(), "git-0/objects/x", "x\n")
	write(t, d.Bundle(), "git-0/config", "[core]\n\tfsmonitor = "+program+"\n")
	if err := d.Disarm(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".git", "sub/.git", "bare/HEAD", "HEAD"} {
		if _, err := os.Lstat(filepath.Join(d.Work(), filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s stays", rel)
		}
	}
	info, err := os.Lstat(filepath.Join(d.Work(), ".git-from-run"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf(".git-from-run: %v, %v", info, err)
	}
	if target, err := os.Readlink(filepath.Join(d.Work(), "sub", ".git-from-run")); err != nil || target != filepath.Join(outside, ".git") {
		t.Errorf("the link is %q (%v)", target, err)
	}
	for _, dir := range []string{d.Work(), filepath.Join(d.Work(), "sub"), filepath.Join(d.Work(), "bare"), filepath.Join(d.Bundle(), "git-0")} {
		c := gitCmd(dir, "status")
		c.Env = append(c.Env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(d.Root()))
		if out, err := c.CombinedOutput(); err == nil {
			t.Errorf("git status passed in %s:\n%s", dir, out)
		}
	}
	if data, err := os.ReadFile(ran); err == nil {
		t.Errorf("a program ran: %s", data)
	}
}

// TestACheckoutInARunsCopyStops is a repository and a plain folder under the folder of the
// copies: neither gets a copy, and the line starts with the fix.
func TestACheckoutInARunsCopyStops(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	other, err := New("run-other")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Remove() })
	_, err = other.Clone(context.Background(), d.Work(), branch)
	if err == nil || !strings.HasPrefix(err.Error(), "run qory in your checkout: ") {
		t.Errorf("Clone: %v", err)
	}
	if _, err := os.Lstat(other.Work()); err == nil {
		t.Error("Clone made a copy")
	}
	err = other.CopyFolder(context.Background(), d.Work())
	if err == nil || !strings.HasPrefix(err.Error(), "run qory in your checkout: ") {
		t.Errorf("CopyFolder: %v", err)
	}
	if err := InRunCopies(src); err != nil {
		t.Errorf("the person's repository: %v", err)
	}
}

// TestHashBlobIsWhatGitComputes compares [hashBlob] with git hash-object in both object formats.
func TestHashBlobIsWhatGitComputes(t *testing.T) {
	isolate(t)
	for _, format := range []string{"sha1", "sha256"} {
		dir := t.TempDir()
		testGit(t, dir, "init", "--quiet", "--object-format="+format)
		content := "line\n\x00binary\r\n"
		want := gitIn(t, dir, content, "hash-object", "--no-filters", "--stdin")
		got, err := hashBlob(context.Background(), format, strings.NewReader(content), int64(len(content)))
		if err != nil || got != want {
			t.Errorf("%s: %s (%v), want %s", format, got, err, want)
		}
	}
	if _, err := hashBlob(context.Background(), "sha1", strings.NewReader("x"), 2); err == nil {
		t.Error("a short file has a hash")
	}
}

// TestCopyBackChecksTheSizeBeforeTheHash is a file of the run past the budget, sparse so that a
// hash would take long: the copy back stops with no read.
func TestCopyBackChecksTheSizeBeforeTheHash(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	write(t, src, "file.txt", "first\n")
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(d.Work(), "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 40); err != nil {
		t.Skipf("this file system has no sparse file: %v", err)
	}
	f.Close()
	start := time.Now()
	_, err = d.copyBackWith(context.Background(), src, 10, 1<<20)
	if err == nil || !strings.HasPrefix(err.Error(), "copy the rest from ") {
		t.Errorf("err = %v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("the copy back took %v", time.Since(start))
	}
	if _, err := os.Lstat(filepath.Join(src, "sparse")); err == nil {
		t.Error("the file came back")
	}
}

// TestCopyBackCountsFolders is a run that adds many empty folders: they pass the budget.
func TestCopyBackCountsFolders(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if err := os.Mkdir(filepath.Join(d.Work(), "d"+strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.copyBackWith(context.Background(), src, 3, 1<<20); err == nil {
		t.Error("five folders passed a budget of three")
	}
}

// TestCopyBackStopsWithItsContext is a context that ended: the copy back stops, with what it
// copied in the line.
func TestCopyBackStopsWithItsContext(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	write(t, d.Work(), "new.txt", "the run\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.CopyBack(ctx, src)
	if err == nil || !strings.Contains(err.Error(), "cut short") {
		t.Errorf("err = %v", err)
	}
}

// TestCopyFolderStopsAtItsBudget is a folder past each budget, and a context that ended.
func TestCopyFolderStopsAtItsBudget(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	write(t, src, "a.txt", "12345\n")
	write(t, src, "b.txt", "12345\n")
	for name, c := range map[string]struct {
		entries int
		bytes   int64
	}{"entries": {1, 1 << 20}, "bytes": {10, 3}} {
		d, err := New("run-" + name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.Remove() })
		err = d.copyFolderOf(context.Background(), src, "", c.entries, c.bytes)
		if err == nil || !strings.HasPrefix(err.Error(), "start the run in a smaller folder") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	d := newDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.CopyFolder(ctx, src); err == nil {
		t.Error("a copy with an ended context passed")
	}
}

// TestALinkOfTheFolderIsNotListedAgain is a plain folder with a link at the copy: the copy back
// lists only a new link, or one the run changed.
func TestALinkOfTheFolderIsNotListedAgain(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	write(t, src, "target.txt", "x\n")
	link(t, src, "same", "target.txt")
	link(t, src, "changed", "target.txt")
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(d.Work(), "changed")); err != nil {
		t.Fatal(err)
	}
	link(t, d.Work(), "changed", "/etc/passwd")
	link(t, d.Work(), "new", "target.txt")
	res, err := d.CopyBack(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"changed", "new"}; !slices.Equal(res.Skipped, want) {
		t.Errorf("skipped = %v, want %v", res.Skipped, want)
	}
}

// TestNamesFoldAsMacOSFoldsThem is U+017F and the Kelvin sign, which lower-casing keeps and
// APFS folds to s and k.
func TestNamesFoldAsMacOSFoldsThem(t *testing.T) {
	for name, want := range map[string]string{
		"harne\u017fs.yaml":    "harness.yaml",
		"qory-\u017ftack.yaml": "qory-stack.yaml",
		"qory-stac\u212a.yaml": "qory-stack.yaml",
		"QORY.yaml":            "qory.yaml",
		"qory\u200d.yaml":      "qory.yaml",
	} {
		if got := qoryName(name); got != want {
			t.Errorf("qoryName(%q) = %q, want %q", name, got, want)
		}
	}
	for _, names := range [][]string{
		{"HEAD", "object\u017f"},
		{"HEAD", "ref\u017f"},
		{"head", "REFS"},
	} {
		if !gitDirLike(names) {
			t.Errorf("gitDirLike(%q) = false", names)
		}
	}
	if folded("\u00d3") != folded("O\u0301") {
		t.Errorf("folded does not normalize: %q, %q", folded("\u00d3"), folded("O\u0301"))
	}
}

// TestActingChangesReadsAFoldedQoryFile is a run's commit that adds a stack file whose name
// macOS folds to qory-stack.yaml: its keys are listed.
func TestActingChangesReadsAFoldedQoryFile(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "harne\u017fs.yaml", "env:\n  B: \"2\"\n")
	write(t, d.Work(), "qory-stac\u212a.yaml", "modules:\n  - name: other\n    source:\n      git: https://git.example.com/other.git\n")
	q := bringBack(t, d, src, repos, nil)
	changes, err := q.ActingChanges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) < 2 {
		t.Errorf("the changes for the two folded names are %+v", changes)
	}
}

// TestActingChangesListsAnAddedGitFolder is a run's commit that adds a bare repository: the
// result lists its folder, also when a name folds to objects.
func TestActingChangesListsAnAddedGitFolder(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	for _, f := range []string{"bare/HEAD", "bare/objects/x", "bare/refs/x", "folded/HEAD", "folded/object\u017f/x"} {
		write(t, d.Work(), f, "x\n")
	}
	q := bringBack(t, d, src, repos, nil)
	changes, err := q.ActingChanges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, c := range changes {
		files = append(files, c.File)
	}
	for _, want := range []string{"bare", "folded"} {
		if !slices.Contains(files, want) {
			t.Errorf("the changes %v do not list %s", files, want)
		}
	}
}

// withSubmodule makes the person's repository with a submodule, and returns its folder and the
// submodule's path.
func withSubmodule(t *testing.T) (string, string) {
	t.Helper()
	src, _ := person(t)
	sub := filepath.Join(t.TempDir(), "sub")
	write(t, sub, "lib.txt", "lib\n")
	testGit(t, sub, "init", "--quiet", "-b", "main")
	commitAll(t, sub, "lib")
	testGit(t, src, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", sub, "libs/s")
	commitAll(t, src, "submodule")
	return src, "libs/s"
}

// TestASubmoduleBehindALinkIsSkipped is a copy whose submodule folder's parent is a link to a
// folder outside, as a commit can make through a name the file system folds: no clone goes
// through the link.
func TestASubmoduleBehindALinkIsSkipped(t *testing.T) {
	isolate(t)
	src, sub := withSubmodule(t)
	d, _ := cloneRun(t, src)
	base := testGit(t, d.Work(), "rev-parse", "HEAD")
	top := filepath.Join(d.Work(), strings.Split(sub, "/")[0])
	if err := os.RemoveAll(top); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, top); err != nil {
		t.Fatal(err)
	}
	if err := d.cloneSubmodules(context.Background(), src, d.Work(), base); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Errorf("the folder outside has %v (%v)", entries, err)
	}
}

// TestAFurtherRepositoryBehindALinkIsSkipped is a commit with a link at .qory to a folder
// outside, and a further repository in the person's .qory: no clone goes through the link.
func TestAFurtherRepositoryBehindALinkIsSkipped(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(src, ".qory")); err != nil {
		t.Fatal(err)
	}
	testGit(t, src, "add", ".qory")
	commitAll(t, src, "a link at .qory")
	if err := os.Remove(filepath.Join(src, ".qory")); err != nil {
		t.Fatal(err)
	}
	further := filepath.Join(src, ".qory", "repositories", "other")
	if err := os.MkdirAll(further, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, further, "init", "--quiet", "-b", "main")
	write(t, further, "file.txt", "x\n")
	commitAll(t, further, "first")
	d := newDir(t)
	repos, err := d.Clone(context.Background(), src, branch)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Errorf("repos = %+v, want the person's alone", repos)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Errorf("the folder outside has %v (%v)", entries, err)
	}
}

// TestCopyFolderWithoutGitLeavesOutTheTopGit is a folder where git init ran: its .git stays out
// of the copy, and a copy back leaves it as it is.
func TestCopyFolderWithoutGitLeavesOutTheTopGit(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	testGit(t, src, "init", "--quiet")
	write(t, src, "file.txt", "first\n")
	if !NoCommits(context.Background(), src) {
		t.Error("NoCommits = false after git init")
	}
	d := newDir(t)
	if err := d.CopyFolderWithoutGit(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(d.Work(), ".git")); err == nil {
		t.Error("the copy has a .git")
	}
	write(t, d.Work(), "file.txt", "the run\n")
	head := read(t, src, ".git/HEAD")
	if _, err := d.CopyBack(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if read(t, src, "file.txt") != "the run\n" || read(t, src, ".git/HEAD") != head {
		t.Error("the copy back did not bring the file, or changed .git")
	}
}

// TestNoCommitsIsFalseWithACommit is a repository with a commit, also on a branch HEAD is not
// on, and a folder git has nothing to read in.
func TestNoCommitsIsFalseWithACommit(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	if NoCommits(context.Background(), src) {
		t.Error("NoCommits = true with a commit")
	}
	testGit(t, src, "checkout", "--quiet", "--orphan", "other")
	if NoCommits(context.Background(), src) {
		t.Error("NoCommits = true on an unborn branch beside a commit")
	}
	if NoCommits(context.Background(), t.TempDir()) {
		t.Error("NoCommits = true in a folder with no repository")
	}
}

// TestCopyArtifactsInCopiesTheArtifactsAndTheWorkItem is a checkout with versions and a work
// item: the copy has them with the same content, so a later copy back has no conflict.
func TestCopyArtifactsInCopiesTheArtifactsAndTheWorkItem(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	write(t, src, ".qory/artifacts/note/main/v1/note.md", "first\n")
	write(t, src, ".qory/artifacts/.gitignore", "*\n")
	write(t, src, ".qory/work-item.json", "{}\n")
	write(t, src, ".qory/secrets.env", "no\n")
	res, err := d.CopyArtifactsIn(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped)+len(res.Conflicts) != 0 || len(res.Copied) != 3 {
		t.Errorf("result = %+v", res)
	}
	for _, rel := range []string{".qory/artifacts/note/main/v1/note.md", ".qory/work-item.json"} {
		if got, want := read(t, d.Work(), rel), read(t, src, rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(d.Work(), ".qory", "secrets.env")); err == nil {
		t.Error("the copy has a file of .qory that is no artifact")
	}
	back, err := d.CopyArtifactsBack(context.Background(), src)
	if err != nil || len(back.Conflicts)+len(back.Copied) != 0 {
		t.Errorf("the copy back: %+v, %v", back, err)
	}
}

// TestCopyArtifactsInFollowsNoLink is a link at each place of the path: nothing it points to
// reaches the copy.
func TestCopyArtifactsInFollowsNoLink(t *testing.T) {
	outside := func(t *testing.T) string {
		dir := t.TempDir()
		write(t, dir, "secret.txt", "secret\n")
		write(t, dir, "work-item.json", "secret\n")
		write(t, dir, "artifacts/secret.txt", "secret\n")
		return dir
	}
	for name, plant := range map[string]func(t *testing.T, src, out string) string{
		".qory": func(t *testing.T, src, out string) string {
			link(t, src, ".qory", out)
			return ".qory"
		},
		"artifacts": func(t *testing.T, src, out string) string {
			write(t, src, ".qory/keep.txt", "x\n")
			link(t, src, ".qory/artifacts", filepath.Join(out, "artifacts"))
			return ".qory/artifacts"
		},
		"work item": func(t *testing.T, src, out string) string {
			write(t, src, ".qory/keep.txt", "x\n")
			link(t, src, ".qory/work-item.json", filepath.Join(out, "work-item.json"))
			return ".qory/work-item.json"
		},
		"a file": func(t *testing.T, src, out string) string {
			write(t, src, ".qory/artifacts/keep.txt", "x\n")
			link(t, src, ".qory/artifacts/secret.txt", filepath.Join(out, "secret.txt"))
			return ".qory/artifacts/secret.txt"
		},
		"a folder": func(t *testing.T, src, out string) string {
			write(t, src, ".qory/artifacts/keep.txt", "x\n")
			link(t, src, ".qory/artifacts/note", out)
			return ".qory/artifacts/note"
		},
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			src, _ := person(t)
			d, _ := cloneRun(t, src)
			want := plant(t, src, outside(t))
			res, err := d.CopyArtifactsIn(context.Background(), src)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(res.Skipped, want) {
				t.Errorf("skipped = %v, want %s", res.Skipped, want)
			}
			if _, err := os.Lstat(filepath.Join(d.Work(), filepath.FromSlash(want))); err == nil {
				t.Errorf("the copy has %s", want)
			}
			err = filepath.WalkDir(d.Work(), func(p string, e fs.DirEntry, err error) error {
				if err == nil && strings.Contains(e.Name(), "secret") {
					t.Errorf("the copy has %s", p)
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestCopyArtifactsInStopsAtItsBudget is a store past the budget: the run does not start with
// part of the store, and the error starts with the fix.
func TestCopyArtifactsInStopsAtItsBudget(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	write(t, src, ".qory/artifacts/a.txt", "x\n")
	write(t, src, ".qory/artifacts/b.txt", "x\n")
	_, err := d.artifactsInWith(context.Background(), src, 1, 1<<20)
	if err == nil || !strings.HasPrefix(err.Error(), "remove old versions from ") {
		t.Errorf("err = %v", err)
	}
}

// TestCopyArtifactsInKeepsAFileOfTheCommit is a store file that the commit has with other
// content: the commit's stays, and the file is a conflict.
func TestCopyArtifactsInKeepsAFileOfTheCommit(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, ".qory/artifacts/a.txt", "the commit\n")
	testGit(t, src, "add", "-f", ".qory/artifacts/a.txt")
	commitAll(t, src, "more")
	d, _ := cloneRun(t, src)
	write(t, src, ".qory/artifacts/a.txt", "the person\n")
	res, err := d.CopyArtifactsIn(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Conflicts, []string{".qory/artifacts/a.txt"}) {
		t.Errorf("conflicts = %v", res.Conflicts)
	}
	if got := read(t, d.Work(), ".qory/artifacts/a.txt"); got != "the commit\n" {
		t.Errorf("the copy has %q", got)
	}
}

// TestSetupChangesFollowsGitsRules is each case of what git status lists: a file and a folder
// that .gitignore ignores stay out, a pattern's negation counts, a changed execute bit and a
// tracked file under an ignored folder count, an unchanged file does not, an empty folder
// does not, and a new repository is one path.
func TestSetupChangesFollowsGitsRules(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, ".gitignore", "*.log\nignored/\nbuild/\n!keep.log\n")
	write(t, src, "ignored/tracked.txt", "first\n")
	write(t, src, "run.sh", "first\n")
	write(t, src, "same.txt", "first\n")
	testGit(t, src, "add", "-f", "ignored/tracked.txt")
	commitAll(t, src, "more")
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "a.log", "x\n")
	write(t, d.Work(), "keep.log", "x\n")
	write(t, d.Work(), "build/out.txt", "x\n")
	write(t, d.Work(), "ignored/new.txt", "x\n")
	write(t, d.Work(), "ignored/tracked.txt", "the setup\n")
	write(t, d.Work(), "sub/.gitignore", "*.tmp\n")
	write(t, d.Work(), "sub/a.tmp", "x\n")
	write(t, d.Work(), "sub/b.txt", "x\n")
	write(t, d.Work(), "inner/.git/HEAD", "ref: refs/heads/main\n")
	write(t, d.Work(), "inner/file.txt", "x\n")
	if err := os.Mkdir(filepath.Join(d.Work(), "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(d.Work(), "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := d.SetupChanges(context.Background(), repos[0])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ignored/tracked.txt", "inner/", "keep.log", "run.sh", "sub/.gitignore", "sub/b.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("the setup changed %v, want %v", got, want)
	}
}

// TestSetupChangesFollowsNoLink is a new link to a folder outside, and a tracked file and a
// tracked folder replaced by links there: each link is one path, and nothing of the folder it
// points to is listed or read, not even a file named as a tracked one.
func TestSetupChangesFollowsNoLink(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, "tracked.txt", "first\n")
	write(t, src, "folder/file.txt", "first\n")
	commitAll(t, src, "more")
	d, repos := cloneRun(t, src)
	outside := t.TempDir()
	write(t, outside, "file.txt", "first\n")
	write(t, outside, "secret.txt", "secret\n")
	link(t, d.Work(), "out", outside)
	for _, rel := range []string{"tracked.txt", "folder"} {
		if err := os.RemoveAll(filepath.Join(d.Work(), rel)); err != nil {
			t.Fatal(err)
		}
	}
	link(t, d.Work(), "tracked.txt", filepath.Join(outside, "file.txt"))
	link(t, d.Work(), "folder", outside)
	got, err := d.SetupChanges(context.Background(), repos[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"folder", "folder/file.txt", "out", "tracked.txt"}; !slices.Equal(got, want) {
		t.Errorf("the setup changed %v, want %v", got, want)
	}
}

// TestSetupChangesRunsNoGitInTheCopy records the folder and the work tree of each git it runs:
// none is the copy or lies in it.
func TestSetupChangesRunsNoGitInTheCopy(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "README.md", "the setup\n")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	calls := filepath.Join(t.TempDir(), "calls")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf '%s|%s|%s\\n' \"$(pwd -P)\" \"$GIT_WORK_TREE\" \"$*\" >> "+calls+"\nexec "+real+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := d.SetupChanges(context.Background(), repos[0]); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	work, err := filepath.EvalSymlinks(d.Work())
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for line := range strings.Lines(string(log)) {
		seen = seen || strings.Contains(line, "ls-tree")
		if strings.Contains(line, work) || strings.Contains(line, d.Work()) {
			t.Errorf("a git ran in the copy: %s", line)
		}
	}
	if !seen {
		t.Errorf("no git ls-tree ran:\n%s", log)
	}
}

// TestAShallowRepositoryComesBack is a person's repository cloned with a depth of 1: the
// quarantine reads the run's commits, though the parent of the first commit is not there.
func TestAShallowRepositoryComesBack(t *testing.T) {
	isolate(t)
	full, _ := person(t)
	write(t, full, "second.txt", "second\n")
	commitAll(t, full, "second")
	src := filepath.Join(t.TempDir(), "shallow")
	testGit(t, "", "clone", "--quiet", "--depth", "1", "file://"+full, src)
	testGit(t, src, "config", "user.name", "Tester")
	testGit(t, src, "config", "user.email", "tester@example.com")
	sessionGit(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "left.txt", "left\n")
	bringBack(t, d, src, repos, nil)
	if got := files(t, src, branch); !slices.Contains(got, "left.txt") {
		t.Errorf("the run's branch has %v, want left.txt", got)
	}
}

// TestTheGitSessionUsesTheMessageItIsGiven is the message of a run on a work branch, with a
// second line: the commit has it, and without one the commit has the default.
func TestTheGitSessionUsesTheMessageItIsGiven(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "left.txt", "left\n")
	s, err := d.GitSession(context.Background(), branch, "Changes left by run 7\n\nThe run ended failed.", repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	runSession(t, s)
	qs, err := d.Quarantine(context.Background(), branch, repos)
	if err != nil {
		t.Fatal(err)
	}
	if err := qs[0].FetchInto(context.Background(), src, "refs/heads/"+branch); err != nil {
		t.Fatal(err)
	}
	if got := testGit(t, src, "log", "-1", "--format=%B", branch); !strings.Contains(got, "Changes left by run 7\n\nThe run ended failed.") {
		t.Errorf("the commit's message is %q", got)
	}
}

// TestCopyArtifactsBackBringsOnlyRegularFiles is a run's .qory/artifacts in a clone: a version
// and the store's .gitignore come back; a link, a .git, a folder that git takes as a git
// folder and a pipe do not; the person's other file stays; and no other file of .qory comes.
func TestCopyArtifactsBackBringsOnlyRegularFiles(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	outside := filepath.Join(t.TempDir(), "outside")
	write(t, outside, "secret.txt", "secret\n")
	write(t, src, ".qory/artifacts/mine/kept.txt", "the person\n")
	write(t, src, ".qory/artifacts/mine/both.txt", "the person\n")
	d, _ := cloneRun(t, src)
	store := ".qory/artifacts/"
	write(t, d.Work(), store+".gitignore", "*\n")
	write(t, d.Work(), store+"note/main/1/note.md", "the run\n")
	write(t, d.Work(), store+"mine/both.txt", "the run\n")
	write(t, d.Work(), store+"sub/.git/config", "[core]\n\tfsmonitor = x\n")
	write(t, d.Work(), store+"bare/HEAD", "ref: refs/heads/main\n")
	write(t, d.Work(), store+"bare/refs/x", "x\n")
	write(t, d.Work(), store+"bare/objects/x", "x\n")
	write(t, d.Work(), ".qory/harness/entry", "the compose\n")
	link(t, d.Work(), store+"out", outside)
	link(t, d.Work(), store+"note/main/1/secret.txt", filepath.Join(outside, "secret.txt"))
	if err := syscall.Mkfifo(filepath.Join(d.Work(), ".qory", "artifacts", "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := d.CopyArtifactsBack(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, src, store+"note/main/1/note.md") != "the run\n" || read(t, src, store+".gitignore") != "*\n" {
		t.Errorf("the version did not come back: %+v", got)
	}
	if read(t, src, store+"mine/kept.txt") != "the person\n" || read(t, src, store+"mine/both.txt") != "the person\n" {
		t.Errorf("the person's files changed: %+v", got)
	}
	if !slices.Contains(got.Conflicts, store+"mine/both.txt") {
		t.Errorf("the conflicts are %v", got.Conflicts)
	}
	for _, gone := range []string{store + "out", store + "note/main/1/secret.txt", store + "sub/.git", store + "bare", store + "pipe", ".qory/harness"} {
		if _, err := os.Lstat(filepath.Join(src, gone)); err == nil {
			t.Errorf("%s came back", gone)
		}
	}
	for _, skipped := range []string{store + "out", store + "note/main/1/secret.txt", store + "bare", store + "pipe"} {
		if !slices.Contains(got.Skipped, skipped) {
			t.Errorf("%s is not among the left out %v", skipped, got.Skipped)
		}
	}
}

// TestCopyArtifactsBackFollowsNoLinkOfThePerson is a link of the person's at .qory and at
// .qory/artifacts: nothing is written through it. A link in the copy's path brings nothing.
func TestCopyArtifactsBackFollowsNoLinkOfThePerson(t *testing.T) {
	isolate(t)
	for _, at := range []string{".qory", ".qory/artifacts"} {
		t.Run(at, func(t *testing.T) {
			src, _ := person(t)
			outside := t.TempDir()
			link(t, src, at, outside)
			d, _ := cloneRun(t, src)
			if err := os.RemoveAll(filepath.Join(d.Work(), ".qory")); err != nil {
				t.Fatal(err)
			}
			write(t, d.Work(), ".qory/artifacts/note.md", "the run\n")
			got, err := d.CopyArtifactsBack(context.Background(), src)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(got.Skipped, at) {
				t.Errorf("the left out are %v, want %s", got.Skipped, at)
			}
			names, err := os.ReadDir(outside)
			if err != nil || len(names) != 0 {
				t.Errorf("the folder behind the link has %v (%v)", names, err)
			}
			if err := d.Remove(); err != nil {
				t.Fatal(err)
			}
		})
	}
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	outside := t.TempDir()
	write(t, outside, "note.md", "outside\n")
	if err := os.RemoveAll(filepath.Join(d.Work(), ".qory")); err != nil {
		t.Fatal(err)
	}
	link(t, d.Work(), ".qory", outside)
	got, err := d.CopyArtifactsBack(context.Background(), src)
	if err != nil || !slices.Contains(got.Skipped, ".qory") {
		t.Errorf("a link at the copy's .qory: %+v, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(src, ".qory")); err == nil {
		t.Error(".qory is in the person's repository")
	}
}

// TestCopyArtifactsBackStopsAtTheBudget is a run past the file budget and past the byte
// budget: the error starts with its fix, and nothing past the budget is written.
func TestCopyArtifactsBackStopsAtTheBudget(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	write(t, d.Work(), ".qory/artifacts/a.txt", "12345\n")
	write(t, d.Work(), ".qory/artifacts/b.txt", "12345\n")
	_, err := d.artifactsBackWith(context.Background(), src, 1, 1<<20)
	hasPrefix(t, "the file budget", err, "copy the rest from ")
	if _, serr := os.Stat(filepath.Join(src, ".qory", "artifacts", "b.txt")); serr == nil {
		t.Error("the file past the budget came back")
	}
	_, err = d.artifactsBackWith(context.Background(), t.TempDir(), 100, 1)
	hasPrefix(t, "the byte budget", err, "copy the rest from ")
}

// TestCopyArtifactsBackOfAPlainFolderTakesTheRunsChange is a version of the person's that the
// copy of a plain folder started with and the run changed: the record shows it is the run's
// change, so it comes back.
func TestCopyArtifactsBackOfAPlainFolderTakesTheRunsChange(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	write(t, src, ".qory/artifacts/note.md", "first\n")
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	write(t, d.Work(), ".qory/artifacts/note.md", "the run\n")
	got, err := d.CopyArtifactsBack(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, src, ".qory/artifacts/note.md") != "the run\n" {
		t.Errorf("the run's change did not come back: %+v", got)
	}
}

// disarmWithin is d.Disarm under a bound: a walk that does not end fails the test.
func disarmWithin(t *testing.T, d Dir, limit time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- d.Disarm(context.Background()) }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("Disarm still runs after %s", limit)
		return nil
	}
}

// TestADisarmedCopyHasNoRepositoryGitFinds is a copy with a planted program at every place git
// reads settings from. No git status in it, or below what Disarm renamed, runs the program.
func TestADisarmedCopyHasNoRepositoryGitFinds(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	w := d.Work()
	program, ran := marker(t)
	testGit(t, w, "config", "core.fsmonitor", program)
	testGit(t, w, "config", "core.worktree", w)
	planted := func(rel string) {
		write(t, w, rel+"/HEAD", "ref: refs/heads/main\n")
		write(t, w, rel+"/objects/x", "x")
		write(t, w, rel+"/refs/x", "x")
		write(t, w, rel+"/config", "[core]\n\tfsmonitor = "+program+"\n\tworktree = "+w+"\n")
	}
	for _, rel := range []string{
		".git/modules/sub", "caseupper/.GIT", "casemix/.gIt", "zw/.g\u200cit", "nfc/.git\u0301",
		"bare.git", "other/deep/bare2", "common", "noexec/inner/.git",
	} {
		planted(rel)
	}
	write(t, w, "wtgit/HEAD", "ref: refs/heads/main\n")
	write(t, w, "wtgit/commondir", "../common\n")
	write(t, w, "wt/.git", "gitdir: "+filepath.Join(w, "wtgit")+"\n")
	write(t, w, "wt2/.git", "gitdir: ../bare.git\n")
	link(t, w, "lnk/.git", filepath.Join(w, "bare.git"))
	if err := os.Chmod(filepath.Join(w, "noexec"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := disarmWithin(t, d, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(w, "noexec", "inner", ".git")); err == nil {
		t.Error("noexec/inner/.git stays")
	}
	for _, rel := range []string{
		".", "bare.git", "other/deep/bare2", "wt", "wt2", "wtgit", "common", "lnk", "caseupper", "casemix", "zw", "nfc",
		".git-from-run", ".git-from-run/objects", ".git-from-run/modules/sub", ".git-from-run/hooks", "noexec/inner",
	} {
		dir := filepath.Join(w, filepath.FromSlash(rel))
		c := gitCmd(dir, "status")
		c.Env = append(c.Env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(d.Root()))
		out, err := c.CombinedOutput()
		if err == nil {
			t.Errorf("git status passed in %s:\n%s", rel, out)
		}
		if data, err := os.ReadFile(ran); err == nil {
			t.Errorf("git status in %s ran a program: %s", rel, data)
			os.Remove(ran)
		}
	}
}

// TestDisarmRenamesAFolderInAFolderItCanOnlyRead is .git in a folder of mode 0400, where the lookup
// of a free name fails until the folder has its search bit.
func TestDisarmRenamesAFolderInAFolderItCanOnlyRead(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	write(t, d.Work(), "trap/.git/HEAD", "x")
	if err := os.Chmod(filepath.Join(d.Work(), "trap"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := disarmWithin(t, d, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(d.Work(), "trap", ".git-from-run")); err != nil {
		t.Error(err)
	}
}

// TestDisarmRenamesALongName is a name of up to 748 bytes that folds to .git: the new name fits.
func TestDisarmRenamesALongName(t *testing.T) {
	for _, zeros := range []int{83, 248} {
		isolate(t)
		src, _ := person(t)
		d, _ := cloneRun(t, src)
		name := ".g" + strings.Repeat("\u200c", zeros) + "it"
		if err := os.MkdirAll(filepath.Join(d.Work(), "long", name), 0o755); err != nil {
			t.Logf("this file system has no such name: %v", err)
			continue
		}
		if err := disarmWithin(t, d, 30*time.Second); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(d.Work(), "long", ".git-from-run")); err != nil {
			t.Error(err)
		}
	}
}

// TestDisarmGoesOnPastAFolderTooDeep is a folder of 70 levels whose name sorts before .git: the
// error is in the result, the top .git is renamed, and at most maxDisarmErrors lines return.
func TestDisarmGoesOnPastAFolderTooDeep(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	for _, top := range []string{".a", ".b", ".c", ".d", ".e", ".f", ".0"} {
		write(t, d.Work(), top+"/"+strings.Repeat("d/", 70)+"f", "x")
	}
	err := disarmWithin(t, d, time.Minute)
	if err == nil {
		t.Fatal("no error for a folder too deep")
	}
	if n := strings.Count(err.Error(), "\n") + 1; n != maxDisarmErrors {
		t.Errorf("%d lines, want %d: %v", n, maxDisarmErrors, err)
	}
	if _, err := os.Lstat(filepath.Join(d.Work(), ".git")); err == nil {
		t.Error("the top .git stays")
	}
}

// TestDisarmEndsWithItsContext is a context that ended: the walk ends with its error.
func TestDisarmEndsWithItsContext(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Disarm(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("%v, want the context's error", err)
	}
}

// TestRenameOutStopsWhenEveryNameIsTaken is a folder with every name renameOut tries.
func TestRenameOutStopsWhenEveryNameIsTaken(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	write(t, d.Work(), "full/.git/x", "x")
	write(t, d.Work(), "full/.git-from-run", "x")
	for i := 1; i <= maxRenameTries; i++ {
		write(t, d.Work(), "full/.git-from-run-"+strconv.Itoa(i), "x")
	}
	err := disarmWithin(t, d, 30*time.Second)
	if err == nil || !strings.HasPrefix(err.Error(), "remove the files or folders next to ") || !strings.Contains(err.Error(), "all 101 such names are taken") {
		t.Errorf("%v, want the line that starts with the fix and has the count", err)
	}
}

// TestAPipeAtTheExcludeFileLeavesTheCloneToEnd is a named pipe at .git/info/exclude: the clone
// reads no such file and ends.
func TestAPipeAtTheExcludeFileLeavesTheCloneToEnd(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	exclude := filepath.Join(src, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Remove(exclude)
	if err := exec.Command("mkfifo", exclude).Run(); err != nil {
		t.Skipf("no named pipe here: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		d, err := New("run-1")
		if err == nil {
			_, err = d.Clone(t.Context(), src, branch)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(30 * time.Second):
		// Open the pipe, so the goroutine ends.
		if f, err := os.OpenFile(exclude, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		t.Fatal("the clone still waits on the named pipe")
	}
}

// sessionOutput makes the git session for repos with out, runs it, and returns what it printed.
func sessionOutput(t *testing.T, d Dir, repos []Repository, out map[string]LeftOut) string {
	t.Helper()
	s, err := d.GitSession(context.Background(), branch, "", repos, out)
	if err != nil {
		t.Fatal(err)
	}
	printed, err := boundedSession(t, s)
	if err != nil {
		t.Fatalf("git session: %v\n%s", err, printed)
	}
	return printed
}

// TestTheBranchComesBackWithHeadAtTheBaseCommit is an agent that committed on the run's branch and
// then checked out a new branch at the base commit: the commits come back as they are, and the
// working tree, which has no such file, makes no commit that removes it.
func TestTheBranchComesBackWithHeadAtTheBaseCommit(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, base := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	testGit(t, d.Work(), "checkout", "--quiet", "-b", "scratch", base)
	q := bringBack(t, d, src, repos, nil)
	if got := testGit(t, src, "log", "-1", "--format=%s", q.Head); !strings.Contains(got, "on the branch") {
		t.Errorf("the head commit is %q", got)
	}
	if !slices.Contains(files(t, src, "refs/heads/"+branch), "kept.txt") {
		t.Error("kept.txt did not come back")
	}
	if d.LeftBehind(repos) {
		t.Error("the session recorded work left behind, with nothing left behind")
	}
}

// TestAChangeOnAScratchBranchIsLeftBehind is the same with a file left in the working tree, which
// the branch does not start from: the commit stays out, and the session records that.
func TestAChangeOnAScratchBranchIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, base := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	testGit(t, d.Work(), "checkout", "--quiet", "-b", "scratch", base)
	write(t, d.Work(), "scratch.txt", "x\n")
	q := bringBack(t, d, src, repos, nil)
	got := files(t, src, "refs/heads/"+branch)
	if !slices.Contains(got, "kept.txt") || slices.Contains(got, "scratch.txt") {
		t.Errorf("the branch at %s has %v", q.Head, got)
	}
	if !d.LeftBehind(repos) {
		t.Error("the session did not record the file left behind")
	}
}

// TestACommitOnAnotherBranchIsLeftBehind is an agent that committed on a branch and then checked
// out the run's branch: no bundle comes back, and the session records the commit.
func TestACommitOnAnotherBranchIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "checkout", "--quiet", "-b", "feature")
	write(t, d.Work(), "feature.txt", "x\n")
	commitAll(t, d.Work(), "on feature")
	testGit(t, d.Work(), "checkout", "--quiet", branch)
	runGit(t, d, repos, nil)
	if _, err := d.Quarantine(t.Context(), branch, repos); !errors.Is(err, ErrNoBundle) {
		t.Errorf("%v, want no bundle", err)
	}
	if !d.LeftBehind(repos) {
		t.Error("the session did not record the commit on feature")
	}
}

// TestABranchPastHeadIsLeftBehind is commits on two branches from the base commit: HEAD's come
// back, and the session records the others.
func TestABranchPastHeadIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, base := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "one.txt", "x\n")
	commitAll(t, d.Work(), "on the branch")
	testGit(t, d.Work(), "checkout", "--quiet", "-b", "side", base)
	write(t, d.Work(), "two.txt", "x\n")
	commitAll(t, d.Work(), "on side")
	bringBack(t, d, src, repos, nil)
	if got := files(t, src, "refs/heads/"+branch); !slices.Contains(got, "two.txt") {
		t.Errorf("the branch has %v", got)
	}
	if !d.LeftBehind(repos) {
		t.Error("the session did not record the commit on the run's branch")
	}
}

// TestABranchInHeadLeavesNothingBehind is a person with a branch past HEAD and an agent's branch
// that the bundle has: the session records nothing.
func TestABranchInHeadLeavesNothingBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, base := person(t)
	testGit(t, src, "branch", "elsewhere", base)
	testGit(t, src, "checkout", "--quiet", "elsewhere")
	write(t, src, "person.txt", "x\n")
	commitAll(t, src, "the person's")
	testGit(t, src, "checkout", "--quiet", "-")
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "one.txt", "x\n")
	commitAll(t, d.Work(), "on the branch")
	testGit(t, d.Work(), "checkout", "--quiet", "-b", "more")
	write(t, d.Work(), "two.txt", "x\n")
	commitAll(t, d.Work(), "on more")
	bringBack(t, d, src, repos, nil)
	if d.LeftBehind(repos) {
		t.Error("the session recorded work left behind, with nothing left behind")
	}
}

// TestAFolderGitCannotReadIsLeftBehind is new work in a folder of mode 000: git add exits 0, the
// session prints git's line, and it records the work left behind.
func TestAFolderGitCannotReadIsLeftBehind(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder of mode 000")
	}
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "ok.txt", "ok\n")
	write(t, d.Work(), "locked/new.txt", "x\n")
	locked := filepath.Join(d.Work(), "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	out := sessionOutput(t, d, repos, nil)
	if !strings.Contains(out, "qory: git add printed in the copy: ") || !strings.Contains(out, "could not open directory") {
		t.Errorf("the session printed:\n%s", out)
	}
	if !d.LeftBehind(repos) {
		t.Error("the session did not record the folder left behind")
	}
	qs, err := d.Quarantine(t.Context(), branch, repos)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(files(t, qs[0].Path, qs[0].Head), "ok.txt") {
		t.Error("ok.txt did not come back")
	}
}

// TestAnIgnoredPathLeftOutPrintsNothing is a setup path that .gitignore lists too: git add exits 1
// and prints its block, which is no work left behind.
func TestAnIgnoredPathLeftOutPrintsNothing(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	write(t, src, ".gitignore", "*.log\n")
	commitAll(t, src, "an ignore file")
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "setup.log", "x\n")
	write(t, d.Work(), "ok.txt", "ok\n")
	states, err := d.SetupStates(t.Context(), repos[0], []string{"setup.log"})
	if err != nil {
		t.Fatal(err)
	}
	out := sessionOutput(t, d, repos, map[string]LeftOut{"": {Setup: states}})
	if strings.Contains(out, "qory:") || strings.Contains(out, "ignored") {
		t.Errorf("the session printed:\n%s", out)
	}
	if d.LeftBehind(repos) {
		t.Error("the session recorded an ignored path as work left behind")
	}
}

// TestCoreAutocrlfLeavesTheCopyAsCommitted is a person with core.autocrlf=true: the copy has LF,
// and a file the agent edits goes into the commit with LF on the lines it did not change.
func TestCoreAutocrlfLeavesTheCopyAsCommitted(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	first := read(t, src, "README.md")
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, []byte("[core]\n\tautocrlf = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	d, repos := cloneRun(t, src)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	if got := read(t, d.Work(), "README.md"); got != first {
		t.Fatalf("the copy's README.md is %q, want %q", got, first)
	}
	write(t, d.Work(), "README.md", first+"agent line\n")
	runGit(t, d, repos, nil)
	qs, err := d.Quarantine(t.Context(), branch, repos)
	if err != nil {
		t.Fatal(err)
	}
	if got := testGit(t, qs[0].Path, "cat-file", "blob", qs[0].Head+":README.md"); strings.Contains(got, "\r") {
		t.Errorf("the commit's README.md has CR: %q", got)
	}
}

// TestASetupPathEndingInACarriageReturnStaysOut is a setup path that ends in CR, which git strips
// from a line it reads: the session ends with exit 0, and the unchanged file stays out.
func TestASetupPathEndingInACarriageReturnStaysOut(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "setup\r", "x\n")
	write(t, d.Work(), "ok.txt", "ok\n")
	states, err := d.SetupStates(t.Context(), repos[0], []string{"setup\r"})
	if err != nil {
		t.Fatal(err)
	}
	sessionOutput(t, d, repos, map[string]LeftOut{"": {Setup: states}})
	qs, err := d.Quarantine(t.Context(), branch, repos)
	if err != nil {
		t.Fatal(err)
	}
	got := files(t, qs[0].Path, qs[0].Head)
	if !slices.Contains(got, "ok.txt") || slices.ContainsFunc(got, func(f string) bool { return strings.Contains(f, "setup") }) {
		t.Errorf("the commit has %v", got)
	}
}

// TestASetupFileWithAnExecuteBitButTheOwnersStaysOut is a setup file of mode 0654, whose state has
// the owner's bit. A session as root, or as a user other than the owner, must read that bit.
func TestASetupFileWithAnExecuteBitButTheOwnersStaysOut(t *testing.T) {
	if strings.Contains(sessionScript, `[ -x "`) {
		t.Error("the session's script tests what the process may do, not the owner's bit")
	}
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	for name, mode := range map[string]os.FileMode{"group.sh": 0o654, "owner.sh": 0o744} {
		write(t, d.Work(), name, "x\n")
		if err := os.Chmod(filepath.Join(d.Work(), name), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(t, d.Work(), "ok.txt", "ok\n")
	states, err := d.SetupStates(t.Context(), repos[0], []string{"group.sh", "owner.sh"})
	if err != nil {
		t.Fatal(err)
	}
	sessionOutput(t, d, repos, map[string]LeftOut{"": {Setup: states}})
	qs, err := d.Quarantine(t.Context(), branch, repos)
	if err != nil {
		t.Fatal(err)
	}
	got := files(t, qs[0].Path, qs[0].Head)
	if !slices.Contains(got, "ok.txt") || slices.Contains(got, "group.sh") || slices.Contains(got, "owner.sh") {
		t.Errorf("the commit has %v", got)
	}
}

// TestACtxReaderStopsWhenItsContextEnds is a read after the context ended.
func TestACtxReaderStopsWhenItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := ctxReader{ctx, strings.NewReader("abc")}
	if _, err := r.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Errorf("%v, want the context's error", err)
	}
}

// TestACopyReadsOnlyTheHashedSize is a file that grew after its hash: the copy has what was
// hashed, and a context that ends stops the copy.
func TestACopyReadsOnlyTheHashedSize(t *testing.T) {
	got, err := io.ReadAll(boundedRead(context.Background(), strings.NewReader("hashed and more"), 6))
	if err != nil || string(got) != "hashed" {
		t.Errorf("%q, %v, want the first 6 bytes", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := io.ReadAll(boundedRead(ctx, strings.NewReader("x"), 1)); !errors.Is(err, context.Canceled) {
		t.Errorf("%v, want the context's error", err)
	}
}

// TestAFilePastTheHashLimitIsNotHashed is a tracked file over the limit, unchanged: the setup's
// list has it with no hash of the file, and its state is other.
func TestAFilePastTheHashLimitIsNotHashed(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, "big.txt", "more than four bytes\n")
	commitAll(t, src, "a file")
	d, repos := cloneRun(t, src)
	before, err := d.SetupChanges(t.Context(), repos[0])
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(before, "big.txt") {
		t.Fatalf("an unchanged file under the limit is listed: %v", before)
	}
	states, err := d.SetupStates(t.Context(), repos[0], []string{"big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if states[0].State == "other" {
		t.Fatalf("the state under the limit is %s", states[0].State)
	}
	old := maxHashBytes
	maxHashBytes = 4
	t.Cleanup(func() { maxHashBytes = old })
	after, err := d.SetupChanges(t.Context(), repos[0])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(after, "big.txt") {
		t.Errorf("a file past the limit is not listed: %v", after)
	}
	states, err = d.SetupStates(t.Context(), repos[0], []string{"big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if states[0].State != "other" {
		t.Errorf("the state past the limit is %s, want other", states[0].State)
	}
}

// TestAnEmbeddedRepositoryWithNoFindIsLeftBehind is a session whose PATH has no find: git add adds
// the repository as a link to a commit, prints its warning, and the session records that.
func TestAnEmbeddedRepositoryWithNoFindIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	bin := t.TempDir()
	for _, name := range []string{"git", "cat", "comm", "cp", "grep", "mkdir", "mv", "readlink", "rm", "sort", "tr"} {
		real, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("no %s here: %v", name, err)
		}
		write(t, bin, name, "#!/bin/sh\nexec '"+real+"' \"$@\"\n")
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := sessionPath
	sessionPath = bin
	t.Cleanup(func() { sessionPath = old })
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	inner := filepath.Join(d.Work(), "inner")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, inner, "init", "--quiet")
	write(t, inner, "x.txt", "x\n")
	commitAll(t, inner, "inner")
	out := sessionOutput(t, d, repos, nil)
	if !strings.Contains(out, "qory: git add printed in the copy: ") || !strings.Contains(out, "embedded") {
		t.Errorf("the session printed:\n%s", out)
	}
	if !d.LeftBehind(repos) {
		t.Error("the session did not record the embedded repository")
	}
}

// noBundle runs the git session and wants no commit past the base commit in the person's
// repository, and wants left as what the session recorded as left behind.
func noBundle(t *testing.T, d Dir, repos []Repository, left bool) string {
	t.Helper()
	printed := sessionOutput(t, d, repos, nil)
	if _, err := d.Quarantine(context.Background(), branch, repos); !errors.Is(err, ErrNoBundle) {
		t.Errorf("Quarantine = %v, want no bundle\n%s", err, printed)
	}
	if got := d.LeftBehind(repos); got != left {
		t.Errorf("left behind = %v, want %v\n%s", got, left, printed)
	}
	return printed
}

// TestAHeadBeforeTheBaseCommitCommitsNothing is an agent that checked out a commit older than the
// base commit: the working tree lacks the base commit's files, and the session commits no deletion
// of them. With nothing else in the copy no work stays behind, and a change there does.
func TestAHeadBeforeTheBaseCommitCommitsNothing(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run("", func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, older := person(t)
			write(t, src, "newer.txt", "newer\n")
			commitAll(t, src, "newer")
			d, repos := cloneRun(t, src)
			testGit(t, d.Work(), "checkout", "--quiet", "--detach", older)
			if changed {
				write(t, d.Work(), "README.md", "changed\n")
			}
			noBundle(t, d, repos, changed)
		})
	}
}

// TestAResetBelowTheBaseCommitCommitsNothing is an agent that runs reset --hard HEAD~1 on the run's
// branch and leaves a new file: HEAD and the branch are one commit, older than the base commit, so
// a commit of that working tree would delete the base commit's file. The session makes no commit,
// so no bundle comes back and the branch stays at the base commit, and the new file keeps the copy.
func TestAResetBelowTheBaseCommitCommitsNothing(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, older := person(t)
	write(t, src, "newer.txt", "newer\n")
	commitAll(t, src, "newer")
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "reset", "--quiet", "--hard", "HEAD~1")
	write(t, d.Work(), "left.txt", "left\n")
	for _, ref := range []string{"HEAD", branch} {
		if got := testGit(t, d.Work(), "rev-parse", ref); got != older {
			t.Fatalf("%s is at %s, want the older commit %s", ref, got, older)
		}
	}
	printed := sessionOutput(t, d, repos, nil)
	if !d.LeftBehind(repos) {
		t.Errorf("the session did not record the new file:\n%s", printed)
	}
	qs, err := d.Quarantine(context.Background(), branch, repos)
	if errors.Is(err, ErrNoBundle) {
		return
	}
	if err != nil {
		t.Fatalf("Quarantine: %v\n%s", err, printed)
	}
	got := files(t, qs[0].Path, qs[0].Head)
	t.Errorf("the session made a commit, which has %v; newer.txt present = %t", got, slices.Contains(got, "newer.txt"))
}

// TestAnOrphanHeadCommitsNothing is an agent on a branch with no commit, past the run's branch:
// the session commits no deletion of every tracked file, the branch comes back as it is, and the
// new file stays behind.
func TestAnOrphanHeadCommitsNothing(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	tip := testGit(t, d.Work(), "rev-parse", "HEAD")
	testGit(t, d.Work(), "checkout", "--quiet", "--orphan", "o")
	testGit(t, d.Work(), "rm", "-rf", "--quiet", ".")
	write(t, d.Work(), "new.txt", "new\n")
	q := bringBack(t, d, src, repos, nil)
	if q.Head != tip {
		t.Errorf("the bundle has %s, want the agent's commit %s", q.Head, tip)
	}
	if !d.LeftBehind(repos) {
		t.Error("the session did not record the new file")
	}
}

// TestAnEmptyOrphanHeadLeavesNothingBehind is an agent that emptied the working tree on a branch
// with no commit: the commit it would make is a deletion, and the copy has nothing else.
func TestAnEmptyOrphanHeadLeavesNothingBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "checkout", "--quiet", "--orphan", "o")
	testGit(t, d.Work(), "rm", "-rf", "--quiet", ".")
	noBundle(t, d, repos, false)
}

// TestWorkOutsideTheBundleIsLeftBehind is each way an agent can leave work that the bundle does
// not have, with nothing past the base commit: the session records it, so the copy stays.
func TestWorkOutsideTheBundleIsLeftBehind(t *testing.T) {
	for name, agent := range map[string]func(t *testing.T, work, base string){
		"stash": func(t *testing.T, work, _ string) {
			write(t, work, "README.md", "stashed\n")
			testGit(t, work, "stash", "--quiet")
		},
		"tag then reset": func(t *testing.T, work, base string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			testGit(t, work, "tag", "keep")
			testGit(t, work, "reset", "--quiet", "--hard", base)
		},
		"annotated tag then reset": func(t *testing.T, work, base string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			testGit(t, work, "tag", "-a", "-m", "keep", "keep")
			testGit(t, work, "reset", "--quiet", "--hard", base)
		},
		"reset": func(t *testing.T, work, base string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			testGit(t, work, "reset", "--quiet", "--hard", base)
		},
		"detached commit": func(t *testing.T, work, _ string) {
			testGit(t, work, "checkout", "--quiet", "--detach")
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			testGit(t, work, "checkout", "--quiet", branch)
		},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, base := person(t)
			d, repos := cloneRun(t, src)
			agent(t, d.Work(), base)
			noBundle(t, d, repos, true)
		})
	}
}

// TestADetachedCommitBesideTheBranchIsLeftBehind is an agent with a commit on the branch and one
// made with HEAD detached: the branch comes back, and the other commit keeps the copy.
func TestADetachedCommitBesideTheBranchIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	testGit(t, d.Work(), "checkout", "--quiet", "--detach")
	write(t, d.Work(), "x.txt", "x\n")
	commitAll(t, d.Work(), "x")
	testGit(t, d.Work(), "checkout", "--quiet", branch)
	bringBack(t, d, src, repos, nil)
	if !d.LeftBehind(repos) {
		t.Error("the session did not record the detached commit")
	}
}

// TestAnUnrelatedHeadIsLeftBehind is a detached commit that does not descend from the base commit,
// with the branch past that commit: the branch comes back, and the commit keeps the copy. The
// working tree is at that commit, so the session finds no change there.
func TestAnUnrelatedHeadIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	tip := testGit(t, d.Work(), "rev-parse", "HEAD")
	testGit(t, d.Work(), "checkout", "--quiet", "--orphan", "y")
	write(t, d.Work(), "y.txt", "y\n")
	commitAll(t, d.Work(), "y")
	testGit(t, d.Work(), "checkout", "--quiet", "--detach")
	testGit(t, d.Work(), "branch", "-q", "-D", "y")
	q := bringBack(t, d, src, repos, nil)
	if q.Head != tip {
		t.Errorf("the bundle has %s, want %s", q.Head, tip)
	}
	if !d.LeftBehind(repos) {
		t.Error("the session did not record the commit at HEAD")
	}
}

// TestASubmodulePastItsGitlinkIsLeftBehind is a commit or a change in a submodule: the session
// prints a line, and the copy stays.
func TestASubmodulePastItsGitlinkIsLeftBehind(t *testing.T) {
	for name, commit := range map[string]bool{"commit": true, "change": false} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			d, repos := cloneRun(t, src)
			inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
			write(t, inner, "lib.txt", "changed\n")
			if commit {
				commitAll(t, inner, "in the submodule")
			}
			printed := noBundle(t, d, repos, true)
			if want := "qory: the submodule libs/s in the copy has commits or changes that the commit leaves out"; !strings.Contains(printed, want) {
				t.Errorf("the session printed:\n%s", printed)
			}
		})
	}
}

// TestASubmoduleAtItsGitlinkLeavesNothingBehind is a submodule the agent did not change.
func TestASubmoduleAtItsGitlinkLeavesNothingBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := withSubmodule(t)
	d, repos := cloneRun(t, src)
	if printed := noBundle(t, d, repos, false); printed != "" {
		t.Errorf("the session printed:\n%s", printed)
	}
}

// TestARepositoryTheRunMadeIsLeftBehind is a new repository with a commit, and git init in a
// folder of the commit with a change there: the session prints a line, and the copy stays.
func TestARepositoryTheRunMadeIsLeftBehind(t *testing.T) {
	for name, agent := range map[string]func(t *testing.T, work string){
		"new": func(t *testing.T, work string) {
			write(t, filepath.Join(work, "inner"), "x.txt", "x\n")
			testGit(t, filepath.Join(work, "inner"), "init", "--quiet")
			commitAll(t, filepath.Join(work, "inner"), "inside")
		},
		"init": func(t *testing.T, work string) {
			testGit(t, filepath.Join(work, "inner"), "init", "--quiet")
			write(t, filepath.Join(work, "inner"), "tracked.txt", "changed\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			if name == "init" {
				write(t, filepath.Join(src, "inner"), "tracked.txt", "tracked\n")
				commitAll(t, src, "a folder")
			}
			d, repos := cloneRun(t, src)
			agent(t, d.Work())
			printed := noBundle(t, d, repos, true)
			if want := "qory: the copy has a git repository at inner that the run made, and the commit leaves it out"; !strings.Contains(printed, want) {
				t.Errorf("the session printed:\n%s", printed)
			}
		})
	}
}

// TestAnIgnoredRepositoryLeavesNothingBehind is a repository in a folder an ignore rule covers,
// such as a tool's cache: git add leaves such a folder out with or without a .git.
func TestAnIgnoredRepositoryLeavesNothingBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	write(t, src, ".gitignore", "cache/\n")
	commitAll(t, src, "ignore")
	d, repos := cloneRun(t, src)
	inner := filepath.Join(d.Work(), "cache", "dep")
	write(t, inner, "x.txt", "x\n")
	testGit(t, inner, "init", "--quiet")
	commitAll(t, inner, "inside")
	if printed := noBundle(t, d, repos, false); printed != "" {
		t.Errorf("the session printed:\n%s", printed)
	}
}

// TestAGoodRunLeavesNothingBehind is what a run does with no work to lose: the session records
// nothing, so the copy goes and the run exits 0.
func TestAGoodRunLeavesNothingBehind(t *testing.T) {
	for name, agent := range map[string]func(t *testing.T, work, base string){
		"amend": func(t *testing.T, work, _ string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			write(t, work, "x.txt", "y\n")
			testGit(t, work, "add", "x.txt")
			testGit(t, work, "commit", "--quiet", "--amend", "-m", "y")
		},
		"squash": func(t *testing.T, work, base string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			write(t, work, "y.txt", "y\n")
			commitAll(t, work, "y")
			testGit(t, work, "reset", "--quiet", "--soft", base)
			testGit(t, work, "commit", "--quiet", "-m", "both")
		},
		"detach and back": func(t *testing.T, work, _ string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			testGit(t, work, "checkout", "--quiet", "--detach")
			testGit(t, work, "checkout", "--quiet", branch)
		},
		"other branch of the person and back": func(t *testing.T, work, _ string) {
			testGit(t, work, "checkout", "--quiet", "other")
			testGit(t, work, "checkout", "--quiet", branch)
			write(t, work, "x.txt", "x\n")
		},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, base := person(t)
			testGit(t, src, "checkout", "--quiet", "-b", "other")
			write(t, src, "other.txt", "other\n")
			commitAll(t, src, "other")
			testGit(t, src, "tag", "from-the-person")
			testGit(t, src, "remote", "add", "origin", "https://example.com/site.git")
			testGit(t, src, "update-ref", "refs/remotes/origin/other", "HEAD")
			testGit(t, src, "checkout", "--quiet", "-")
			testGit(t, src, "branch", "-q", "-D", "other")
			d, repos := cloneRun(t, src)
			agent(t, d.Work(), base)
			bringBack(t, d, src, repos, nil)
			if d.LeftBehind(repos) {
				t.Error("the session recorded work left behind")
			}
		})
	}
}

// TestAGoodRunOnALineEndRuleLeavesNothingBehind is a line-end rule that makes git add print a
// warning: the warning is no failure of the add, so it prints no line and the copy goes.
func TestAGoodRunOnALineEndRuleLeavesNothingBehind(t *testing.T) {
	for attributes, content := range map[string]string{
		"* text=auto\n":         "one\r\ntwo\r\n",
		"*.txt text eol=crlf\n": "one\ntwo\n",
	} {
		t.Run("", func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			write(t, src, ".gitattributes", attributes)
			commitAll(t, src, "line ends")
			d, repos := cloneRun(t, src)
			testGit(t, d.Work(), "config", "core.safecrlf", "true")
			write(t, d.Work(), "x.txt", content)
			printed := sessionOutput(t, d, repos, nil)
			if strings.Contains(printed, "left this out") {
				t.Errorf("the session printed:\n%s", printed)
			}
			if d.LeftBehind(repos) {
				t.Errorf("the session recorded work left behind:\n%s", printed)
			}
			if _, err := d.Quarantine(context.Background(), branch, repos); err != nil {
				t.Errorf("Quarantine: %v\n%s", err, printed)
			}
		})
	}
}

// TestDisarmOpensATopFolderWithNoReadBit is a run that took the read bit off the copy: the walk
// still renames its .git.
func TestDisarmOpensATopFolderWithNoReadBit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a folder with no read bit")
	}
	for _, mode := range []os.FileMode{0o300, 0o100, 0o000} {
		t.Run("", func(t *testing.T) {
			isolate(t)
			src, _ := person(t)
			d, _ := cloneRun(t, src)
			if err := os.Chmod(d.Work(), mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(d.Work(), 0o700) })
			if err := d.Disarm(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(d.Work(), ".git")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Lstat of .git: %v", err)
			}
		})
	}
}

// TestACredentialInAFilterLineStaysHidden is a required filter that prints a URL with a password
// and fails at the checkout of the copy: the stop has no password.
func TestACredentialInAFilterLineStaysHidden(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, ".gitattributes", "*.dat filter=fails\n")
	write(t, src, "x.dat", "x\n")
	commitAll(t, src, "a filtered file")
	config := t.TempDir()
	write(t, config, "gitconfig", "[filter \"fails\"]\n\tsmudge = sh -c 'echo fatal: https://me:SECRETTOK@host.example/x?t=SECRETONE SECRETTWO >&2; exit 1'\n\trequired = true\n")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	_, err := newDir(t).Clone(context.Background(), src, branch)
	if err == nil {
		t.Fatal("the clone passed")
	}
	for _, secret := range []string{"SECRETTOK", "SECRETONE", "SECRETTWO"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the line has %s: %v", secret, err)
		}
	}
}

// TestAnOrphanHeadInASHA256Repository is a branch with no commit in a repository of each object
// format: the empty tree the session compares with has that format.
func TestAnOrphanHeadInASHA256Repository(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src := filepath.Join(t.TempDir(), "person")
			if err := os.Mkdir(src, 0o755); err != nil {
				t.Fatal(err)
			}
			testGit(t, src, "init", "--quiet", "-b", "main", "--object-format="+format)
			write(t, src, "README.md", "hello\n")
			commitAll(t, src, "first")
			d, repos := cloneRun(t, src)
			testGit(t, d.Work(), "checkout", "--quiet", "--orphan", "o")
			testGit(t, d.Work(), "rm", "-rf", "--quiet", ".")
			write(t, d.Work(), "new.txt", "new\n")
			noBundle(t, d, repos, true)
		})
	}
}

// TestAHeadBeforeTheBaseCommitWithNoBranchStops is HEAD at a commit older than the base commit
// with the run's branch deleted: the session stops, and its line is true for a HEAD that is a
// commit.
func TestAHeadBeforeTheBaseCommitWithNoBranchStops(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, older := person(t)
	write(t, src, "newer.txt", "newer\n")
	commitAll(t, src, "newer")
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "checkout", "--quiet", "--detach", older)
	testGit(t, d.Work(), "branch", "-q", "-D", branch)
	s, err := d.GitSession(context.Background(), branch, "", repos, nil)
	if err != nil {
		t.Fatal(err)
	}
	printed, err := boundedSession(t, s)
	if err == nil {
		t.Fatalf("the session passed:\n%s", printed)
	}
	want := "qory: neither HEAD nor " + branch + " in the copy is at or past the base commit, so the session commits nothing"
	if !strings.Contains(printed, want) {
		t.Errorf("the session printed:\n%s", printed)
	}
}

// nestedSubmodule is a person's repository with the submodule libs/m, which has the submodule
// inner. The clone of the copy has libs/m, and git leaves inner empty until an update.
func nestedSubmodule(t *testing.T) string {
	t.Helper()
	src, _ := person(t)
	inner := filepath.Join(t.TempDir(), "inner")
	write(t, inner, "lib.txt", "lib\n")
	testGit(t, inner, "init", "--quiet", "-b", "main")
	commitAll(t, inner, "inner")
	middle := filepath.Join(t.TempDir(), "middle")
	write(t, middle, "m.txt", "m\n")
	testGit(t, middle, "init", "--quiet", "-b", "main")
	testGit(t, middle, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", inner, "inner")
	commitAll(t, middle, "middle")
	testGit(t, src, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", middle, "libs/m")
	commitAll(t, src, "submodule")
	return src
}

// updateSubmodules runs what a person runs for a nested submodule, in the copy at work.
func updateSubmodules(t *testing.T, work string) {
	t.Helper()
	testGit(t, work, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive", "--quiet")
}

// TestANestedSubmoduleLetsTheSessionCommit is an agent that updates a nested submodule and commits:
// the commit comes back, and no line says that the run made a repository.
func TestANestedSubmoduleLetsTheSessionCommit(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src := nestedSubmodule(t)
	d, repos := cloneRun(t, src)
	updateSubmodules(t, d.Work())
	if _, err := os.Stat(filepath.Join(d.Work(), "libs", "m", "inner", ".git")); err != nil {
		t.Fatalf("the update made no nested repository: %v", err)
	}
	write(t, d.Work(), "x.txt", "x\n")
	commitAll(t, d.Work(), "x")
	printed := sessionOutput(t, d, repos, nil)
	if strings.Contains(printed, "that the run made") || d.LeftBehind(repos) {
		t.Errorf("left behind = %v:\n%s", d.LeftBehind(repos), printed)
	}
	qs, err := d.Quarantine(context.Background(), branch, repos)
	if err != nil {
		t.Fatalf("Quarantine: %v\n%s", err, printed)
	}
	if !slices.Contains(files(t, qs[0].Path, qs[0].Head), "x.txt") {
		t.Errorf("the commit did not come back:\n%s", printed)
	}
}

// TestASetupThatUpdatesANestedSubmoduleLeavesNothingBehind is the same update in the setup, and an
// agent that commits after it.
func TestASetupThatUpdatesANestedSubmoduleLeavesNothingBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src := nestedSubmodule(t)
	d, repos := cloneRun(t, src)
	updateSubmodules(t, d.Work())
	out := setupRecord(t, d, repos[0])
	write(t, d.Work(), "x.txt", "x\n")
	commitAll(t, d.Work(), "x")
	printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
	if d.LeftBehind(repos) {
		t.Errorf("the session recorded work left behind:\n%s", printed)
	}
}

// setupRecord is what cmd records after the setup, for the repository r.
func setupRecord(t *testing.T, d Dir, r Repository) LeftOut {
	t.Helper()
	paths, err := d.SetupChanges(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	states, err := d.SetupStates(t.Context(), r, paths)
	if err != nil {
		t.Fatal(err)
	}
	submodules, err := d.SubmoduleSetup(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	return LeftOut{Setup: states, Submodules: submodules}
}

// TestAnUnreadableFileLeavesTheRestToComeBack is a file of mode 000 beside other work: the session
// ends well, the rest comes back, and the file keeps the copy.
func TestAnUnreadableFileLeavesTheRestToComeBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	for name, c := range map[string]struct {
		commit, change bool
		bundle         bool
	}{
		"a commit":      {commit: true, bundle: true},
		"a change":      {change: true, bundle: true},
		"no other work": {},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			d, repos := cloneRun(t, src)
			if c.commit {
				write(t, d.Work(), "kept.txt", "kept\n")
				commitAll(t, d.Work(), "kept")
			}
			if c.change {
				write(t, d.Work(), "kept.txt", "kept\n")
			}
			locked := filepath.Join(d.Work(), "locked.txt")
			write(t, d.Work(), "locked.txt", "x\n")
			if err := os.Chmod(locked, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })
			printed := sessionOutput(t, d, repos, nil)
			if !d.LeftBehind(repos) {
				t.Errorf("the session did not record the file:\n%s", printed)
			}
			qs, err := d.Quarantine(context.Background(), branch, repos)
			if !c.bundle {
				if !errors.Is(err, ErrNoBundle) {
					t.Errorf("Quarantine = %v, want no bundle", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := files(t, qs[0].Path, qs[0].Head)
			if !slices.Contains(got, "kept.txt") || slices.Contains(got, "locked.txt") {
				t.Errorf("the commit has %v", got)
			}
		})
	}
}

// TestALineOfGitAddIsNoLostWork is each rule file that makes git add print a line on every run: the
// session prints the line, the run has no change, and nothing stays behind.
func TestALineOfGitAddIsNoLostWork(t *testing.T) {
	for name, before := range map[string]func(t *testing.T, src string){
		"linked gitignore": func(t *testing.T, src string) {
			write(t, src, "ignore-rules", "*.o\n")
			link(t, src, ".gitignore", "ignore-rules")
		},
		"linked gitattributes": func(t *testing.T, src string) {
			write(t, src, "attribute-rules", "*.o binary\n")
			link(t, src, ".gitattributes", "attribute-rules")
		},
		"negative attribute": func(t *testing.T, src string) {
			write(t, src, ".gitattributes", "!foo text\n")
		},
		"macro in a folder": func(t *testing.T, src string) {
			write(t, src, "sub/.gitattributes", "[attr]m text\n")
		},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			before(t, src)
			commitAll(t, src, "the rule")
			d, repos := cloneRun(t, src)
			noBundle(t, d, repos, false)
		})
	}
}

// TestALineEndRewriteStaysOut is a blob with CRLF that a line-end rule in .gitattributes would
// rewrite on the next git add: a run with no change has no commit, and a change to the file goes in.
func TestALineEndRewriteStaysOut(t *testing.T) {
	for name, c := range map[string]struct{ attributes, file string }{
		"text rule":              {"legacy.txt text\n", "legacy.txt"},
		"eol rule":               {"*.bat text eol=crlf\n", "run.bat"},
		"quote in the name":      {"*.txt text\n", "le\"gacy.txt"},
		"line break in the name": {"*.txt text\n", "le\ngacy.txt"},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := crlfBlob(t, c.file, "one\r\ntwo\r\n", c.attributes)
			d, repos := cloneRun(t, src)
			noBundle(t, d, repos, false)
		})
		t.Run(strings.ReplaceAll(name, " ", "-")+"-changed", func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := crlfBlob(t, c.file, "one\r\ntwo\r\n", c.attributes)
			d, repos := cloneRun(t, src)
			write(t, d.Work(), c.file, "one\r\nthree\r\n")
			q := bringBack(t, d, src, repos, nil)
			if d.LeftBehind(repos) {
				t.Error("the session recorded work left behind")
			}
			got := files(t, src, q.Head)
			if !slices.ContainsFunc(got, func(f string) bool { return strings.HasSuffix(strings.Trim(f, `"`), filepath.Ext(c.file)) }) {
				t.Errorf("the commit has %v", got)
			}
		})
	}
}

// TestTextAutoHasNoCommit is * text=auto on files with each line end, which no add rewrites.
func TestTextAutoHasNoCommit(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	write(t, src, "crlf.txt", "one\r\ntwo\r\n")
	write(t, src, "lf.txt", "one\ntwo\n")
	commitAll(t, src, "both")
	write(t, src, ".gitattributes", "* text=auto\n")
	testGit(t, src, "add", ".gitattributes")
	testGit(t, src, "commit", "--quiet", "-m", "the rule")
	if blob := gitIn(t, src, "", "cat-file", "blob", "HEAD:crlf.txt"); !strings.Contains(blob, "\r") {
		t.Fatalf("the blob is %q, want a CR", blob)
	}
	d, repos := cloneRun(t, src)
	noBundle(t, d, repos, false)
}

// TestASparseCheckoutCommitsNoDeletion is an agent that took files out of the working tree: no
// commit deletes them, and the copy stays.
func TestASparseCheckoutCommitsNoDeletion(t *testing.T) {
	for name, agent := range map[string]func(t *testing.T, work string){
		"sparse-checkout": func(t *testing.T, work string) {
			testGit(t, work, "sparse-checkout", "set", "nothing-here")
		},
		"skip-worktree": func(t *testing.T, work string) {
			testGit(t, work, "update-index", "--skip-worktree", "README.md")
			if err := os.Remove(filepath.Join(work, "README.md")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			write(t, src, "sub/a.txt", "a\n")
			commitAll(t, src, "a folder")
			d, repos := cloneRun(t, src)
			agent(t, d.Work())
			noBundle(t, d, repos, true)
		})
	}
}

// TestASetupFileInASubmoduleLeavesNothingBehind is a setup that wrote a file in a submodule: the
// file is the setup's, and a change the agent makes after that keeps the copy.
func TestASetupFileInASubmoduleLeavesNothingBehind(t *testing.T) {
	for _, agentChanges := range []bool{false, true} {
		t.Run("", func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			d, repos := cloneRun(t, src)
			inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
			write(t, inner, "gen.o", "generated\n")
			out := setupRecord(t, d, repos[0])
			if agentChanges {
				write(t, inner, "gen.o", "the agent's\n")
			}
			printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
			if got := d.LeftBehind(repos); got != agentChanges {
				t.Errorf("left behind = %v, want %v:\n%s", got, agentChanges, printed)
			}
		})
	}
}

// TestATagOfThePersonIsNoWorkLeftBehind is a tag off every branch that the agent checks out, then
// leaves: the commit is the person's.
func TestATagOfThePersonIsNoWorkLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, base := person(t)
	testGit(t, src, "checkout", "--quiet", "--detach")
	write(t, src, "tagged.txt", "x\n")
	commitAll(t, src, "tagged")
	testGit(t, src, "tag", "v1")
	testGit(t, src, "checkout", "--quiet", "main")
	if got := testGit(t, src, "rev-parse", "HEAD"); got != base {
		t.Fatalf("HEAD is %s, want %s", got, base)
	}
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "checkout", "--quiet", "v1")
	testGit(t, d.Work(), "checkout", "--quiet", branch)
	noBundle(t, d, repos, false)
}

// TestAnAmendedBaseCommitTakesUncommittedWork is HEAD and the branch at one commit that the base
// commit does not have: a commit undoes nothing, so what the agent left comes back.
func TestAnAmendedBaseCommitTakesUncommittedWork(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "amended.txt", "x\n")
	testGit(t, d.Work(), "add", "amended.txt")
	testGit(t, d.Work(), "commit", "--quiet", "--amend", "-m", "amended")
	write(t, d.Work(), "left.txt", "left\n")
	q := bringBack(t, d, src, repos, nil)
	if d.LeftBehind(repos) {
		t.Error("the session recorded work left behind")
	}
	got := files(t, src, q.Head)
	for _, want := range []string{"amended.txt", "left.txt", "README.md"} {
		if !slices.Contains(got, want) {
			t.Errorf("the commit has %v, want %s", got, want)
		}
	}
}

// TestARebaseOverACommitTakesUncommittedWork is a pull --rebase over a local commit, as a rebase on
// a commit with another base: HEAD and the branch are one commit past another history.
func TestARebaseOverACommitTakesUncommittedWork(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, older := person(t)
	write(t, src, "second.txt", "second\n")
	commitAll(t, src, "second")
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "checkout", "--quiet", "-B", "elsewhere", older)
	write(t, d.Work(), "theirs.txt", "theirs\n")
	commitAll(t, d.Work(), "theirs")
	testGit(t, d.Work(), "checkout", "--quiet", branch)
	testGit(t, d.Work(), "rebase", "--quiet", "elsewhere")
	testGit(t, d.Work(), "branch", "-q", "-D", "elsewhere")
	write(t, d.Work(), "left.txt", "left\n")
	q := bringBack(t, d, src, repos, nil)
	got := files(t, src, q.Head)
	for _, want := range []string{"theirs.txt", "left.txt", "second.txt"} {
		if !slices.Contains(got, want) {
			t.Errorf("the commit has %v, want %s", got, want)
		}
	}
}

// TestAnHTTPURLInAGitLineHasNoCredential is each form of a secret in a URL that git prints: the line
// has its text before the URL, and none of the secret. A line with no @, ? or # stays whole.
func TestAnHTTPURLInAGitLineHasNoCredential(t *testing.T) {
	const hidden = "(a URL and the rest of the line, hidden)"
	secrets := []string{"SEC", "RET", "123", "SECRET", "ZZS1", "ZZS2", "corp.example", "ZZSECRETZZ"}
	for raw, want := range map[string]string{
		"fatal: https://me:SEC RET@host/x failed":                           "fatal: " + hidden,
		"fatal: https://me:123 RET@host/x failed":                           "fatal: " + hidden,
		"fatal: https://SECRET@host/x failed":                               "fatal: " + hidden,
		"fatal: https://me:SEC/RET@host/x failed":                           "fatal: " + hidden,
		"fatal: https://host/x?token=SECRET failed":                         "fatal: " + hidden,
		"fatal: https://host/x?t=ZZS1 ZZS2 failed":                          "fatal: " + hidden,
		"fatal: https://host/x?t=ZZS1'ZZS2 failed":                          "fatal: " + hidden,
		`fatal: https://host/x#ZZS1"ZZS2 failed`:                            "fatal: " + hidden,
		"fatal: HTTPS://host/x?t=ZZS1 ZZS2 failed":                          "fatal: " + hidden,
		"fatal: http://host/x?t=ZZS1 failed":                                "fatal: " + hidden,
		"fatal: ftp://me:ZZS1@host/x failed":                                "fatal: " + hidden,
		"fatal: ftps://host/x?t=ZZS1 failed":                                "fatal: " + hidden,
		"fatal: https://host/x?user=me@corp.example&token=SECRET failed":    "fatal: " + hidden,
		"fatal: https://me:S@host/x?u=a@b&token=SECRET failed":              "fatal: " + hidden,
		"fatal: https://host/x#a@SECRET failed":                             "fatal: " + hidden,
		"fatal: https://host/x?token=SEC@RET failed":                        "fatal: " + hidden,
		"fatal: https://host/x and https://me:ZZS1@host/y failed":           "fatal: " + hidden,
		"fatal: https://me:ZZS1@host/x and https://host/y?t=ZZS2 failed":    "fatal: " + hidden,
		"fatal: unable to access 'https://me:ZZS1@host/x/': failed":         "fatal: unable to access '" + hidden,
		"\x1b[31mhttps://host.example/x?t=ZZSECRETZZ":                       "\x1b[31m" + hidden,
		"url_https://ZZSECRETZZ@host.example/x":                             "url_" + hidden,
		"1https://host.example/x#ZZSECRETZZ":                                "1" + hidden,
		"fatal: me@host.example has no https://host.example/x":              "fatal: me@host.example has no https://host.example/x",
		"https://host.example: failed":                                      "https://host.example: failed",
		"fatal: unable to access 'https://host/x/': Could not resolve host": "fatal: unable to access 'https://host/x/': Could not resolve host",
		"fatal: https://host/x and ftp://host/y failed":                     "fatal: https://host/x and ftp://host/y failed",
		"ssh://git@host/x: Permission denied":                               "ssh://git@host/x: Permission denied",
		"me@host.example has no URL?":                                       "me@host.example has no URL?",
	} {
		got := exports.HideURLError(errors.New(hideHTTP(raw))).Error()
		if got != want {
			t.Errorf("the line %q prints %q, want %q", raw, got, want)
		}
		if strings.Contains(want, hidden) {
			for _, secret := range secrets {
				if strings.Contains(got, secret) {
					t.Errorf("the line %q prints %q, with %s", raw, got, secret)
				}
			}
		}
	}
	long := strings.Repeat("ZZSECRETZZ", 30)
	for raw, want := range map[string]string{
		"fatal: https://me:" + long + "@host.example/x":           "fatal: " + hidden,
		"fatal: ssh://me:" + long + "@host.example/x":             "fatal: ssh://me:(hidden)@host.example/x",
		"fatal: https://host.example/x\nZZSECRETZZ@corp\n":        "fatal: " + hidden,
		"fatal: https://host.example/x\n?t=ZZSECRETZZ\n":          "fatal: " + hidden,
		"\n\nfatal: https://me:ZZSECRETZZ@host.example/x\n":       "fatal: " + hidden,
		"\x1b[31mhttps://host.example/x?t=ZZSECRETZZ":             "?[31m" + hidden,
		"url_https://ZZSECRETZZ@host.example/x":                   "url_" + hidden,
		"1https://host.example/x#ZZSECRETZZ":                      "1" + hidden,
		"fatal: https://host.example/x and ftp://a@b/ZZSECRETZZ":  "fatal: " + hidden,
		"fatal: no repository\nsee https://host/x?t=ZZSECRETZZ\n": "fatal: no repository",
	} {
		cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `printf '%s' "$1" >&2; exit 1`, "sh", raw)
		_, err := run(cmd, nil, []string{"status"})
		var ge *gitError
		if !errors.As(err, &ge) {
			t.Fatalf("the error of %q is %v", raw, err)
		}
		if ge.line != want {
			t.Errorf("run printed %q for %q, want %q", ge.line, raw, want)
		}
		if strings.Contains(ge.line, "ZZSECRETZZ") {
			t.Errorf("run printed %q for %q, with the secret", ge.line, raw)
		}
	}
}

// TestALineBreakInARepositoryPathLetsTheSessionCommit is a further repository whose folder has a
// line break: its alternates line stays one line.
func TestALineBreakInARepositoryPathLetsTheSessionCommit(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	further := filepath.Join(src, ".qory", "repositories", "a\nb", "c\"d")
	write(t, further, "f.txt", "first\n")
	testGit(t, further, "init", "--quiet", "-b", "main")
	commitAll(t, further, "first")
	d, repos := cloneRun(t, src)
	if len(repos) != 2 {
		t.Fatalf("repositories: %+v", repos)
	}
	write(t, d.Work(), filepath.Join(filepath.FromSlash(repos[1].Path), "g.txt"), "g\n")
	printed := sessionOutput(t, d, repos, nil)
	qs, err := d.Quarantine(context.Background(), branch, repos)
	if err != nil {
		t.Fatalf("%v\n%s", err, printed)
	}
	if !slices.Contains(files(t, qs[1].Path, qs[1].Head), "g.txt") {
		t.Errorf("the commit did not come back:\n%s", printed)
	}
}

// caseInsensitiveDisk skips the test on a disk with both names as two files.
func caseInsensitiveDisk(t *testing.T) {
	t.Helper()
	at := t.TempDir()
	write(t, at, "Probe", "x\n")
	if _, err := os.Stat(filepath.Join(at, "probe")); err != nil {
		t.Skip("this disk tells Probe from probe")
	}
}

// TestACaseRenameOnACaseInsensitiveDiskKeepsOneName is an uncommitted mv Foo.txt foo.txt: no commit
// has both names.
func TestACaseRenameOnACaseInsensitiveDiskKeepsOneName(t *testing.T) {
	caseInsensitiveDisk(t)
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	write(t, src, "Foo.txt", "foo\n")
	commitAll(t, src, "Foo")
	d, repos := cloneRun(t, src)
	if err := os.Rename(filepath.Join(d.Work(), "Foo.txt"), filepath.Join(d.Work(), "foo.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, d.Work(), "other.txt", "other\n")
	qs := bringBack(t, d, src, repos, nil)
	got := files(t, src, qs.Head)
	if slices.Contains(got, "Foo.txt") && slices.Contains(got, "foo.txt") {
		t.Errorf("the commit has both names: %v", got)
	}
}

// TestTwoNamesOfOneFileOnACaseInsensitiveDiskGetNoCommit is a commit with README and readme, of
// which the disk has one: a run with no change makes no commit.
func TestTwoNamesOfOneFileOnACaseInsensitiveDiskGetNoCommit(t *testing.T) {
	caseInsensitiveDisk(t)
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	id := strings.TrimSpace(gitIn(t, src, "other\n", "hash-object", "-w", "--stdin"))
	testGit(t, src, "update-index", "--add", "--cacheinfo", "100644,"+id+",readme.md")
	testGit(t, src, "commit", "--quiet", "-m", "two names")
	d, repos := cloneRun(t, src)
	noBundle(t, d, repos, false)
}

// TestManyRefsLeaveTheSessionItsTime is 20,000 tags off every branch, which the session compares
// with what the clone had.
func TestManyRefsLeaveTheSessionItsTime(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	testGit(t, src, "checkout", "--quiet", "--detach")
	write(t, src, "tagged.txt", "x\n")
	commitAll(t, src, "tagged")
	at := testGit(t, src, "rev-parse", "HEAD")
	testGit(t, src, "checkout", "--quiet", "main")
	var refs strings.Builder
	for i := range 20000 {
		fmt.Fprintf(&refs, "create refs/tags/t%d %s\n", i, at)
	}
	gitIn(t, src, refs.String(), "update-ref", "--stdin")
	d, repos := cloneRun(t, src)
	start := time.Now()
	noBundle(t, d, repos, false)
	if took := time.Since(start); took > time.Minute {
		t.Errorf("the session took %s", took)
	}
}

// TestASetupFileWithAnyNameInASubmoduleLeavesNothingBehind is a setup's file in a submodule with a
// name that git status quotes: the name reads as the setup wrote it.
func TestASetupFileWithAnyNameInASubmoduleLeavesNothingBehind(t *testing.T) {
	for _, name := range []string{"gen file.o", "gén.o", `ge"n.o`, "ge\tn.o", `ge\n.o`, "gen.o\r"} {
		t.Run(strings.ToValidUTF8(strconv.Quote(name), ""), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			d, repos := cloneRun(t, src)
			write(t, filepath.Join(d.Work(), filepath.FromSlash(sub)), name, "generated\n")
			out := setupRecord(t, d, repos[0])
			if printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out}); d.LeftBehind(repos) {
				t.Errorf("the session recorded work left behind:\n%s", printed)
			}
		})
	}
}

// TestManySetupFilesInASubmoduleLeaveTheSessionItsTime is 2,000 files a setup left in a submodule.
// A process per file takes about 18 s here, and past 26,000 files the session's whole limit.
func TestManySetupFilesInASubmoduleLeaveTheSessionItsTime(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	d, repos := cloneRun(t, src)
	inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
	for i := range 2000 {
		write(t, inner, fmt.Sprintf("gen/f%d.o", i), "generated\n")
	}
	out := setupRecord(t, d, repos[0])
	start := time.Now()
	printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
	took := time.Since(start)
	t.Logf("the session took %s", took)
	if took > manyFilesLimit {
		t.Errorf("the session took %s, past %s", took, manyFilesLimit)
	}
	if d.LeftBehind(repos) {
		t.Errorf("the session recorded work left behind:\n%s", printed)
	}
}

// manyFilesLimit is what a session with 2,000 setup files in a submodule gets. A process per file
// passes it on a machine under test load too.
const manyFilesLimit = 10 * time.Second

// crlfUnderARule is a person's repository whose legacy.txt is a blob with CR under a line-end rule,
// checked that the blob and the checkout have the CR, with the base commit.
func crlfUnderARule(t *testing.T) (src, base string) {
	t.Helper()
	src, _ = person(t)
	return crlfBlob(t, "legacy.txt", checkoutBytes, "legacy.txt text\n")
}

// crlfBlob is a person's repository with file as a blob with content, which has a CR, and the line-end
// rule attributes committed after it, with the base commit. Only .gitattributes goes into the second
// commit: a git add -A can read the file again under the rule, and write the blob with no CR.
func crlfBlob(t *testing.T, file, content, attributes string) (src, base string) {
	t.Helper()
	src, _ = person(t)
	write(t, src, file, content)
	commitAll(t, src, "CRLF")
	write(t, src, ".gitattributes", attributes)
	testGit(t, src, "add", ".gitattributes")
	testGit(t, src, "commit", "--quiet", "-m", "the rule")
	base = testGit(t, src, "rev-parse", "HEAD")
	if blob := gitIn(t, src, "", "cat-file", "blob", base+":"+file); !strings.Contains(blob, "\r") {
		t.Fatalf("the blob is %q, want a CR", blob)
	}
	return src, base
}

// checkoutBytes is what the checkout of the fixture of [crlfUnderARule] writes.
const checkoutBytes = "one\r\ntwo\r\n"

// TestAnUndoOfACommitOnAFileKeptOutIsLeftBehind is an agent that edits and commits a file the
// session keeps out, then writes the bytes it found back with no commit: the state matches, so no
// commit has the undo, and the copy stays. Once for a file on the clone's list, and once for a
// setup's file.
func TestAnUndoOfACommitOnAFileKeptOutIsLeftBehind(t *testing.T) {
	for _, setup := range []bool{false, true} {
		t.Run(fmt.Sprintf("setup=%t", setup), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := crlfUnderARule(t)
			d, repos := cloneRun(t, src)
			found := checkoutBytes
			out := LeftOut{}
			if setup {
				found = "setup\r\n"
				write(t, d.Work(), "legacy.txt", found)
				out = setupRecord(t, d, repos[0])
			}
			if got := read(t, d.Work(), "legacy.txt"); got != found {
				t.Fatalf("the copy has %q, want the CR", got)
			}
			write(t, d.Work(), "legacy.txt", "edited\n")
			commitAll(t, d.Work(), "edit")
			write(t, d.Work(), "legacy.txt", found)
			printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
			if !d.LeftBehind(repos) {
				t.Errorf("the session did not record the undo:\n%s", printed)
			}
		})
	}
}

// TestACommitOfASetupFileAsItIsLeavesNothingBehind is an agent that runs git add -A and commits a
// file the setup left: the commit has the bytes the copy has, so nothing is lost.
func TestACommitOfASetupFileAsItIsLeavesNothingBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "gen.txt", "generated\n")
	out := setupRecord(t, d, repos[0])
	write(t, d.Work(), "agent.txt", "agent\n")
	commitAll(t, d.Work(), "everything")
	printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
	if d.LeftBehind(repos) {
		t.Errorf("the session recorded work left behind:\n%s", printed)
	}
}

// TestALockedSetupFileInASubmoduleKeepsTheCopy is a setup that leaves a file of mode 000 or a folder
// of mode 0444 in a submodule: the run goes on, and the session keeps the copy.
func TestALockedSetupFileInASubmoduleKeepsTheCopy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any mode")
	}
	for name, c := range map[string]struct {
		path string
		mode os.FileMode
	}{
		"a file of mode 000":    {"gen.o", 0},
		"a folder of mode 0444": {"gen", 0o444},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			d, repos := cloneRun(t, src)
			inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
			write(t, inner, "gen/f.o", "generated\n")
			write(t, inner, "gen.o", "generated\n")
			locked := filepath.Join(inner, c.path)
			if err := os.Chmod(locked, c.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
			out := setupRecord(t, d, repos[0])
			printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
			if !d.LeftBehind(repos) {
				t.Errorf("the session did not keep the copy:\n%s", printed)
			}
		})
	}
}

// TestANameThatReadsAsTwoStatusLinesKeepsTheCopy is an agent's file in a submodule whose name has a
// line break, and whose pieces read as two status lines of files the setup left: the file does not
// come back, so the copy stays.
func TestANameThatReadsAsTwoStatusLinesKeepsTheCopy(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	d, repos := cloneRun(t, src)
	inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
	write(t, inner, "foo.o", "generated\n")
	write(t, inner, "gen.o", "generated\n")
	out := setupRecord(t, d, repos[0])
	write(t, inner, "foo.o\n?? gen.o", "the agent's\n")
	printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
	if !d.LeftBehind(repos) {
		t.Errorf("the session did not keep the copy:\n%s", printed)
	}
}
