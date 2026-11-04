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

// testGit runs git with args in dir, as a person runs it, and returns its output.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Example", "GIT_AUTHOR_EMAIL=example@example.com",
		"GIT_COMMITTER_NAME=Example", "GIT_COMMITTER_EMAIL=example@example.com")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
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
	testGit(t, dir, "add", "-A")
	testGit(t, dir, "commit", "--quiet", "-m", "first")
	return dir, testGit(t, dir, "rev-parse", "HEAD")
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
	if err := os.Mkdir(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"pre-commit", "commit-msg", "prepare-commit-msg", "post-commit", "post-checkout", "post-merge", "pre-push", "reference-transaction", "post-index-change", "pre-auto-gc", "post-rewrite", "fetch-hook"} {
		if err := os.Symlink(program, filepath.Join(hooks, h)); err != nil {
			t.Fatal(err)
		}
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

func TestNewStaysOutOfKeptFolders(t *testing.T) {
	isolate(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{cache, filepath.Join(cache, "qory", "runs", "run-1", "state")} {
		if _, err := New("run-1", keep); err == nil || !strings.HasPrefix(err.Error(), "set XDG_STATE_HOME") {
			t.Errorf("keep out %s: error %v, want one that starts with its fix", keep, err)
		}
	}
	for _, id := range []string{"", ".", "..", "a/b", "-x", "a b"} {
		if _, err := New(id); err == nil {
			t.Errorf("run id %q: no error", id)
		}
	}
	d, err := New("run-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cache, "qory", "runs", "run-1"); d.Root() != want {
		t.Errorf("root %s, want %s", d.Root(), want)
	}
	if _, err := New("run-1"); err == nil || !strings.HasPrefix(err.Error(), "remove ") {
		t.Errorf("second New: error %v, want one that starts with its fix", err)
	}
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
	write(t, src, ".qory/repositories/example/further/f.txt", "further\n")
	further := filepath.Join(src, ".qory", "repositories", "example", "further")
	testGit(t, further, "init", "--quiet", "-b", "main")
	testGit(t, further, "add", "-A")
	testGit(t, further, "commit", "--quiet", "-m", "further")
	furtherBase := testGit(t, further, "rev-parse", "HEAD")
	testGit(t, further, "checkout", "--quiet", "--detach")

	d := newDir(t)
	repos, err := d.Clone(context.Background(), src, "qory/run-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Repository{{Base: base}, {Path: ".qory/repositories/example/further", Base: furtherBase}}
	if !slices.Equal(repos, want) {
		t.Errorf("repositories %v, want %v", repos, want)
	}
	work := d.Work()
	if got := testGit(t, work, "rev-parse", "HEAD"); got != base {
		t.Errorf("copy at %s, want %s", got, base)
	}
	if got := testGit(t, work, "symbolic-ref", "HEAD"); got != "refs/heads/qory/run-1" {
		t.Errorf("copy on %s, want refs/heads/qory/run-1", got)
	}
	if _, err := os.Lstat(filepath.Join(work, "untracked.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy has the untracked file: %v", err)
	}
	if got := testGit(t, filepath.Join(work, ".qory", "repositories", "example", "further"), "rev-parse", "HEAD"); got != furtherBase {
		t.Errorf("further copy at %s, want %s", got, furtherBase)
	}
	theirs := inodes(t, filepath.Join(src, ".git", "objects"))
	for key, path := range inodes(t, filepath.Join(work, ".git", "objects")) {
		if other, ok := theirs[key]; ok {
			t.Errorf("%s shares its inode with %s", path, other)
		}
	}
}

func TestCloneOfAHeadOnNoBranch(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	write(t, src, "second.txt", "second\n")
	testGit(t, src, "add", "-A")
	testGit(t, src, "commit", "--quiet", "-m", "second")
	head := testGit(t, src, "rev-parse", "HEAD")
	testGit(t, src, "checkout", "--quiet", "--detach")
	testGit(t, src, "reset", "--quiet", "--hard", "HEAD~1")
	write(t, src, "third.txt", "third\n")
	testGit(t, src, "add", "-A")
	testGit(t, src, "commit", "--quiet", "-m", "third, on no branch")
	detached := testGit(t, src, "rev-parse", "HEAD")
	if detached == head {
		t.Fatal("the test made no commit on no branch")
	}
	d := newDir(t)
	repos, err := d.Clone(context.Background(), src, "qory/run-1")
	if err != nil {
		t.Fatal(err)
	}
	if repos[0].Base != detached || testGit(t, d.Work(), "rev-parse", "HEAD") != detached {
		t.Errorf("copy at %v, want %s", repos, detached)
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
	if err := os.Symlink("/etc/hosts", filepath.Join(src, "hosts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(src, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := newDir(t)
	if err := d.CopyFolder(src); err != nil {
		t.Fatal(err)
	}
	work := d.Work()
	if b, err := os.ReadFile(filepath.Join(work, "a.txt")); err != nil || string(b) != "a\n" {
		t.Errorf("a.txt: %q, %v", b, err)
	}
	if info, err := os.Stat(filepath.Join(work, "sub", "run.sh")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("sub/run.sh lost its execute bit: %v, %v", info, err)
	}
	for _, link := range []string{"hosts", "elsewhere"} {
		if info, err := os.Lstat(filepath.Join(work, link)); err != nil || info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s is no link in the copy: %v", link, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(work, "pipe")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the copy has the pipe: %v", err)
	}
}

// agentWork makes, in the copy at work, what an agent leaves: a commit that changes an
// acting key and a file qory sets back, and uncommitted work, a file under .qory, a
// repository in the repository and a link qory wrote.
func agentWork(t *testing.T, work string) {
	t.Helper()
	write(t, work, "qory.yaml", "env:\n  A: \"1\"\n  TOKEN: example\n")
	write(t, work, "CLAUDE.md", "changed by the agent\n")
	testGit(t, work, "add", "-A")
	testGit(t, work, "commit", "--quiet", "-m", "agent's commit")
	write(t, work, "new.txt", "uncommitted\n")
	write(t, work, "README.md", "hello again\n")
	write(t, work, ".qory/harness.txt", "composed\n")
	write(t, work, "nested/inner/file.txt", "inside\n")
	testGit(t, filepath.Join(work, "nested", "inner"), "init", "--quiet")
	if err := os.Symlink(".qory/harness.txt", filepath.Join(work, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
}

func TestGitSessionBringsTheWorkBackAndRunsNothingPlanted(t *testing.T) {
	isolate(t)
	sessionGit(t)
	ctx := context.Background()
	src, base := person(t)
	d := newDir(t)
	if _, err := d.Clone(ctx, src, "qory/run-1"); err != nil {
		t.Fatal(err)
	}
	agentWork(t, d.Work())
	program, ran := marker(t)
	plant(t, d.Work(), program)

	s, err := d.GitSession(ctx, "qory/run-1", base, LeftOut{Links: []string{"AGENTS.md", "CLAUDE.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.SessionMounts(); len(got) != 2 || got[0].Path != d.Work() || got[1].Path != d.Bundle() || got[0].ReadOnly || got[1].ReadOnly {
		t.Errorf("mounts %v, want the copy and the bundle folder, read-write", got)
	}
	runSession(t, s)
	q, err := d.Quarantine(ctx, src, "qory/run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.FetchInto(ctx, src, "refs/heads/qory/run-1"); err != nil {
		t.Fatal(err)
	}
	changes, err := q.ActingChanges(ctx, src, base, q.Head)
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
	head := testGit(t, src, "rev-parse", "refs/heads/qory/run-1")
	if head != q.Head {
		t.Errorf("your repository has the run's branch at %s, want %s", head, q.Head)
	}
	if got := testGit(t, src, "log", "--format=%an <%ae> %s", "-1", head); got != commitName+" <"+commitEmail+"> "+commitMessage {
		t.Errorf("last commit %q", got)
	}
	if got := testGit(t, src, "show", head+":new.txt"); got != "uncommitted" {
		t.Errorf("new.txt is %q at the run's commit", got)
	}
	if got := testGit(t, src, "show", head+":README.md"); got != "hello again" {
		t.Errorf("README.md is %q at the run's commit", got)
	}
	if got := testGit(t, src, "show", head+":CLAUDE.md"); got != "base" {
		t.Errorf("CLAUDE.md is %q at the run's commit, want the base commit's", got)
	}
	if got := testGit(t, src, "show", head+":.gitattributes"); got != "* filter=mark" {
		t.Errorf(".gitattributes is %q at the run's commit", got)
	}
	files := testGit(t, src, "ls-tree", "-r", "--name-only", head)
	for _, out := range []string{".qory/harness.txt", "nested/inner/file.txt", "nested/inner", "AGENTS.md"} {
		if slices.Contains(strings.Split(files, "\n"), out) {
			t.Errorf("the run's commit has %s:\n%s", out, files)
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
	c := exec.Command("git", "status", "--porcelain")
	c.Dir = d.Work()
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	_ = c.Run()
	if _, err := os.Stat(ran); err != nil {
		t.Errorf("git status in the copy did not run the planted program, so the test shows nothing: %v", err)
	}
	if err := d.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(d.Root()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the run's folder is still there: %v", err)
	}
}

func TestGitSessionWithNoChangeWritesNoBundle(t *testing.T) {
	isolate(t)
	sessionGit(t)
	ctx := context.Background()
	src, base := person(t)
	d := newDir(t)
	if _, err := d.Clone(ctx, src, "qory/run-1"); err != nil {
		t.Fatal(err)
	}
	s, err := d.GitSession(ctx, "qory/run-1", base, LeftOut{})
	if err != nil {
		t.Fatal(err)
	}
	runSession(t, s)
	if _, err := d.Quarantine(ctx, src, "qory/run-1"); !errors.Is(err, ErrNoBundle) {
		t.Errorf("error %v, want ErrNoBundle", err)
	}
}

func TestGitSessionChecksItsArguments(t *testing.T) {
	isolate(t)
	d := newDir(t)
	ctx := context.Background()
	base := strings.Repeat("a", 40)
	for _, c := range []struct {
		branch, base string
		out          LeftOut
	}{
		{"-x", base, LeftOut{}},
		{"a..b", base, LeftOut{}},
		{"main", "HEAD", LeftOut{}},
		{"main", base, LeftOut{Links: []string{"../x"}}},
		{"main", base, LeftOut{Setup: []string{"/etc/x"}}},
		{"main", base, LeftOut{Setup: []string{"a//b"}}},
	} {
		if _, err := d.GitSession(ctx, c.branch, c.base, c.out); err == nil {
			t.Errorf("%v: no error", c)
		}
	}
}

func TestOpenBundleTakesARegularFileAlone(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	write(t, dir, "regular", "bundle")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
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
	for _, p := range []string{link, pipe, large, dir} {
		if f, err := OpenBundle(p); err == nil || !strings.HasPrefix(err.Error(), "look at ") {
			if f != nil {
				f.Close()
			}
			t.Errorf("%s: error %v, want one that starts with its fix", filepath.Base(p), err)
		}
	}
	if _, err := OpenBundle(filepath.Join(dir, "missing")); !errors.Is(err, ErrNoBundle) {
		t.Errorf("missing: error %v, want ErrNoBundle", err)
	}
	f, err = OpenBundle(regular)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestCorruptObjectFailsTheQuarantineFetch(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	src, base := person(t)
	d := newDir(t)
	if _, err := d.Clone(ctx, src, "qory/run-1"); err != nil {
		t.Fatal(err)
	}
	work := d.Work()
	// A commit with no author line: git's object check stops it.
	tree := testGit(t, work, "rev-parse", "HEAD^{tree}")
	c := exec.Command("git", "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	c.Dir = work
	c.Stdin = strings.NewReader("tree " + tree + "\nparent " + base + "\n\nno author\n")
	out, err := c.Output()
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.TrimSpace(string(out))
	testGit(t, work, "update-ref", "refs/heads/qory/run-1", bad)
	if err := os.Mkdir(d.Bundle(), 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, work, "bundle", "create", "--quiet", filepath.Join(d.Bundle(), bundleFile), "refs/heads/qory/run-1", "^"+base)

	if _, err := d.Quarantine(ctx, src, "qory/run-1"); err == nil || !strings.HasPrefix(err.Error(), "look at the run's copy") {
		t.Fatalf("error %v, want the failed check", err)
	}
	c = exec.Command("git", "cat-file", "-e", bad)
	c.Dir = src
	if c.Run() == nil {
		t.Error("the corrupt commit reached your repository")
	}
	if got := testGit(t, src, "for-each-ref", "--format=%(refname)"); got != "refs/heads/main" {
		t.Errorf("your repository has refs %q", got)
	}
}

func TestFetchIntoACheckedOutBranchFails(t *testing.T) {
	isolate(t)
	sessionGit(t)
	ctx := context.Background()
	src, base := person(t)
	d := newDir(t)
	if _, err := d.Clone(ctx, src, "qory/run-1"); err != nil {
		t.Fatal(err)
	}
	write(t, d.Work(), "new.txt", "new\n")
	s, err := d.GitSession(ctx, "qory/run-1", base, LeftOut{})
	if err != nil {
		t.Fatal(err)
	}
	runSession(t, s)
	q, err := d.Quarantine(ctx, src, "qory/run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := q.FetchInto(ctx, src, "refs/heads/main"); err == nil {
		t.Fatal("the fetch into the checked-out branch main succeeded")
	}
	if got := testGit(t, src, "rev-parse", "refs/heads/main"); got != base {
		t.Errorf("main moved to %s", got)
	}
	if err := q.FetchInto(ctx, src, "main"); err == nil {
		t.Error("a ref name that is not full passed")
	}
}

func TestActingChangesFollowAStackInTheRepository(t *testing.T) {
	isolate(t)
	sessionGit(t)
	ctx := context.Background()
	src, _ := person(t)
	write(t, src, "qory.yaml", "harness:\n  extends:\n    path: stacks/base\n")
	write(t, src, "stacks/base/qory-stack.yaml", "modules:\n  - source:\n      git: https://example.com/a.git\n")
	testGit(t, src, "add", "-A")
	testGit(t, src, "commit", "--quiet", "-m", "stack")
	d := newDir(t)
	repos, err := d.Clone(ctx, src, "qory/run-1")
	if err != nil {
		t.Fatal(err)
	}
	base := repos[0].Base
	write(t, d.Work(), "stacks/base/qory-stack.yaml", "modules:\n  - source:\n      git: https://example.com/b.git\n")
	write(t, d.Work(), "qory-stack.yaml", "- not a mapping\n")
	s, err := d.GitSession(ctx, "qory/run-1", base, LeftOut{})
	if err != nil {
		t.Fatal(err)
	}
	runSession(t, s)
	q, err := d.Quarantine(ctx, src, "qory/run-1")
	if err != nil {
		t.Fatal(err)
	}
	changes, err := q.ActingChanges(ctx, src, base, q.Head)
	if err != nil {
		t.Fatal(err)
	}
	want := []ActingChange{{File: "qory-stack.yaml"}, {File: "stacks/base/qory-stack.yaml", Key: "modules[0].source.git"}}
	if !slices.Equal(changes, want) {
		t.Errorf("acting changes %v, want %v", changes, want)
	}
}

func TestCopyBackLeavesOutLinksGitAndQory(t *testing.T) {
	isolate(t)
	dest := t.TempDir()
	write(t, dest, "a.txt", "a\n")
	write(t, dest, "same.txt", "same\n")
	if err := os.Chmod(filepath.Join(dest, "a.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(dest, "out")); err != nil {
		t.Fatal(err)
	}
	write(t, dest, "gone.txt", "stays\n")
	d := newDir(t)
	if err := d.CopyFolder(dest); err != nil {
		t.Fatal(err)
	}
	work := d.Work()
	write(t, work, "a.txt", "changed\n")
	write(t, work, "b.txt", "new\n")
	write(t, work, "sub/c.txt", "new in a folder\n")
	write(t, work, ".git/config", "[core]\n\tfsmonitor = /bin/false\n")
	write(t, work, "sub/.Git/HEAD", "ref: refs/heads/main\n")
	write(t, work, "sub/.g\u200cit/HEAD", "ref: refs/heads/main\n")
	write(t, work, ".qory/qory.yaml", "env: {}\n")
	if err := os.Remove(filepath.Join(work, "out")); err != nil {
		t.Fatal(err)
	}
	write(t, work, "out/planted.txt", "planted\n")
	if err := os.Symlink("/etc/hosts", filepath.Join(work, "hosts")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "gone.txt")); err != nil {
		t.Fatal(err)
	}

	r, err := d.CopyBack(dest)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.txt", "b.txt", "sub/c.txt"}; !slices.Equal(r.Copied, want) {
		t.Errorf("copied %v, want %v", r.Copied, want)
	}
	if want := []string{".git", ".qory", "hosts", "out", "sub/.Git", "sub/.g\u200cit"}; !slices.Equal(r.Skipped, want) {
		t.Errorf("skipped %q, want %q", r.Skipped, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "a.txt")); string(b) != "changed\n" {
		t.Errorf("a.txt is %q", b)
	}
	if info, err := os.Stat(filepath.Join(dest, "a.txt")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("a.txt changed its mode: %v, %v", info, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "gone.txt")); string(b) != "stays\n" {
		t.Errorf("gone.txt is %q; a copy-back deletes nothing", b)
	}
	for _, p := range []string{".git", ".qory", "hosts", "sub/.Git", "sub/.g\u200cit"} {
		if _, err := os.Lstat(filepath.Join(dest, p)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s reached your folder: %v", p, err)
		}
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("the copy-back wrote through your link: %v", entries)
	}
}

func TestRemoveDeletesAFolderWithNoWriteBit(t *testing.T) {
	isolate(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes every folder")
	}
	d := newDir(t)
	write(t, d.Work(), "locked/file.txt", "x\n")
	if err := os.Chmod(filepath.Join(d.Work(), "locked"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(d.Root()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the run's folder is still there: %v", err)
	}
}
