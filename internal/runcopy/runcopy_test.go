package runcopy

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
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
	}
	for _, kv := range set {
		testGit(t, work, "config", kv[0], kv[1])
	}
	write(t, work, ".gitattributes", "* filter=mark\n")
}

// runSession runs the git session on this machine with sh, in place of the wall.
func runSession(t *testing.T, s GitSession) {
	t.Helper()
	c := exec.Command(s.Command, s.Args...)
	c.Dir = s.Dir
	c.Env = s.Env
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git session: %v\n%s", err, out)
	}
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
	// The copy comes from the bare source, which no run binds, and borrows no object.
	if got := testGit(t, work, "config", "remote.origin.url"); got != d.source(0) {
		t.Errorf("the copy was cloned from %s, want %s", got, d.source(0))
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
	if err := d.CopyFolder(src); err != nil {
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
	err := d.CopyFolder(src)
	hasPrefix(t, "second CopyFolder", err, "remove ")
}

func TestCopyFolderOfAFolderThatContainsTheRun(t *testing.T) {
	isolate(t)
	d := newDir(t)
	err := d.CopyFolder(os.Getenv("HOME"))
	hasPrefix(t, "CopyFolder of HOME", err, "start the run in a folder that does not contain ")
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
	hasPrefix(t, "no record", err, "look at the git session's output")
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
	s, err := d.GitSession(ctx, "main", "", main, map[string]LeftOut{"": {Links: []string{"--", "x"}, Setup: []SetupPath{{Path: "dir/", State: "other"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", base, "2", "--", "x", "1", "dir", "other"}
	if got := s.Args[len(s.Args)-len(want):]; !slices.Equal(got, want) {
		t.Errorf("arguments end in %q, want %q", got, want)
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
		hasPrefix(t, filepath.Base(p), err, "look at ")
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
		hasPrefix(t, "corrupt commit", err, "look at the run's copy")
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
				hasPrefix(t, name, err, "look at the run's copy")
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
	if err := d.CopyFolder(dest); err != nil {
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

	r, err := d.CopyBack(dest)
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
		r, err := d.copyBackWith(dest, c.files, c.bytes)
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
