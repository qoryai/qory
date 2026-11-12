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
	"github.com/qoryai/qory/internal/preparation"
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

// leftBehind reports whether the git session of repos recorded work of the run that the bundles
// do not have, besides the files of [Left.Large]: n.nested or n.left.
func leftBehind(d Dir, repos []Repository) bool {
	return d.recorded(repos, ".nested") || d.recorded(repos, ".left")
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
	if _, _, err := qs[0].FetchInto(ctx, src, branch, "run-1"); err != nil {
		t.Fatal(err)
	}
	return qs[0]
}

// prefix is where the run's other refs come back in every test: [Dir.RefPrefix] of the run id run-1.
const prefix = "refs/qory/run-run-1"

// runRefs runs the git session for repos with out, wants no work left behind, fetches what came
// back into the person's repository at src, as qory run does, and returns the quarantine and the
// refs under [prefix] in src, each without the prefix and with its object.
func runRefs(t *testing.T, d Dir, src string, repos []Repository, out map[string]LeftOut) (*Quarantine, map[string]string) {
	t.Helper()
	printed := sessionOutput(t, d, repos, out)
	if leftBehind(d, repos) {
		t.Errorf("the session recorded work left behind:\n%s", printed)
	}
	qs, err := d.Quarantine(context.Background(), branch, repos)
	if err != nil {
		t.Fatalf("Quarantine: %v\n%s", err, printed)
	}
	if qs[0] == nil {
		t.Fatalf("no quarantine for the person's repository\n%s", printed)
	}
	_, got, err := qs[0].FetchInto(context.Background(), src, branch, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := prefix; qs[0].hasRefs() && got != want {
		t.Errorf("the refs came back under %q, want %q", got, want)
	}
	return qs[0], refsUnder(t, src, prefix)
}

// refsUnder is each ref under prefix in the repository at dir, without the prefix and its slash,
// with its object.
func refsUnder(t *testing.T, dir, prefix string) map[string]string {
	t.Helper()
	refs := map[string]string{}
	for line := range strings.Lines(testGit(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", prefix+"/")) {
		name, id, _ := strings.Cut(strings.TrimSpace(line), " ")
		refs[strings.TrimPrefix(name, prefix+"/")] = id
	}
	return refs
}

// wantRefs fails the test unless refs has exactly the names of want.
func wantRefs(t *testing.T, refs map[string]string, want ...string) {
	t.Helper()
	var got []string
	for name := range refs {
		got = append(got, name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the run's other refs are %v, want %v", got, want)
	}
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

// forgedSignature makes a commit on HEAD of the repository at work with a gpgsig header that no key
// made, moves HEAD's branch to it, and sets log.showSignature and gpg.program to program there: a
// git log of HEAD's reflog with that configuration runs program to check the signature.
func forgedSignature(t *testing.T, work, program string) {
	t.Helper()
	head := testGit(t, work, "rev-parse", "HEAD")
	tree := testGit(t, work, "rev-parse", "HEAD^{tree}")
	commit := "tree " + tree + "\nparent " + head + "\n" +
		"author Example <example@example.com> 1700000000 +0000\n" +
		"committer Example <example@example.com> 1700000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEzBAABCAAdFiEEforged\n -----END PGP SIGNATURE-----\n" +
		"\nforged\n"
	id := gitIn(t, work, commit, "hash-object", "-t", "commit", "-w", "--stdin")
	testGit(t, work, "update-ref", "-m", "forged", "HEAD", id)
	testGit(t, work, "config", "log.showSignature", "true")
	testGit(t, work, "config", "gpg.program", program)
}

// TestASignatureCheckPlantedInTheCopyRunsNothing is an agent that sets log.showSignature and
// gpg.program in the copy's configuration, and puts a commit with a forged signature in HEAD's
// reflog, in the repository and in a submodule: the session reads the reflogs, and the program
// never runs.
func TestASignatureCheckPlantedInTheCopyRunsNothing(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	d, repos := cloneRun(t, src)
	program, ran := marker(t)
	forgedSignature(t, filepath.Join(d.Work(), filepath.FromSlash(sub)), program)
	forgedSignature(t, d.Work(), program)
	printed := sessionOutput(t, d, repos, nil)
	if b, err := os.ReadFile(ran); err == nil {
		t.Fatalf("the planted program ran:\n%s\n%s", b, printed)
	}
	// The control: git log of HEAD's reflog in the copy runs the planted program, so the check
	// above has teeth.
	_ = gitCmd(d.Work(), "log", "-g", "--format=%H %gs", "HEAD").Run()
	if _, err := os.Stat(ran); err != nil {
		t.Errorf("git log -g in the copy did not run the planted program, so the test shows nothing: %v", err)
	}
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
		if _, _, err := qs[1].FetchInto(ctx, further, branch, "run-1"); err != nil {
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
	want := "rel='.' base=" + base + " icase=false links=2 setup=1 since=0 generated='" + generatedNames() + "'\nset -- '--' 'x' 'dir' other\n"
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

// editedBundle writes the bundle of the person's repository for the git session, from the copy of d,
// with refs/heads/<branch> at commit, past base, and the run's refs under prefix in names, each at
// commit too; then each name of the header that edits has is replaced by its value.
func editedBundle(t *testing.T, d Dir, commit, base string, names []string, edits map[string]string) {
	t.Helper()
	testGit(t, d.Work(), "update-ref", "refs/heads/"+branch, commit)
	args := []string{"bundle", "create", "--quiet", filepath.Join(d.Bundle(), "0.bundle"), "refs/heads/" + branch}
	for _, name := range names {
		testGit(t, d.Work(), "update-ref", name, commit)
		args = append(args, name)
	}
	if err := os.Mkdir(d.Bundle(), 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, d.Work(), append(args, "^"+base)...)
	path := filepath.Join(d.Bundle(), "0.bundle")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	end := bytes.Index(data, []byte("\n\n")) + 1
	header := string(data[:end])
	for from, to := range edits {
		if !strings.Contains(header, " "+from+"\n") {
			t.Fatalf("the header has no %s:\n%s", from, header)
		}
		header = strings.Replace(header, " "+from+"\n", " "+to+"\n", 1)
	}
	if err := os.WriteFile(path, append([]byte(header), data[end:]...), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestABundleWithARefOutsideThePrefixFails is a bundle whose header has a ref other than the run's
// branch and the run's refs under its prefix, a ref under the prefix with a trick in its name, or
// two refs that differ only in letter case: the quarantine fails, and nothing reaches the person's
// repository.
func TestABundleWithARefOutsideThePrefixFails(t *testing.T) {
	for name, c := range map[string]struct {
		names []string
		edits map[string]string
		want  string
	}{
		"a branch":         {[]string{"refs/heads/main"}, nil, "the bundle has the ref refs/heads/main"},
		"HEAD":             {[]string{prefix + "/HEAD"}, map[string]string{prefix + "/HEAD": "HEAD"}, "the bundle has the ref HEAD"},
		"another run":      {[]string{"refs/qory/run-2/heads/x"}, nil, "the bundle has the ref refs/qory/run-2/heads/x"},
		"another form":     {[]string{prefix + "/other"}, nil, "the bundle has the ref " + prefix + "/other"},
		"a dot dot":        {[]string{prefix + "/heads/aa"}, map[string]string{prefix + "/heads/aa": prefix + "/heads/../../heads/main"}, "the bundle has the ref"},
		"a lock":           {[]string{prefix + "/heads/aa"}, map[string]string{prefix + "/heads/aa": prefix + "/heads/x.lock"}, "the bundle has the ref"},
		"a control":        {[]string{prefix + "/heads/aa"}, map[string]string{prefix + "/heads/aa": prefix + "/heads/a\tb"}, "the bundle has the ref"},
		"a lost name":      {[]string{prefix + "/lost/01"}, nil, "the bundle has the ref " + prefix + "/lost/01"},
		"two letter cases": {[]string{prefix + "/heads/aa", prefix + "/heads/bb"}, map[string]string{prefix + "/heads/bb": prefix + "/heads/AA"}, "the bundle has the ref " + prefix + "/heads/AA twice"},
		"the branch twice": {[]string{prefix + "/heads/aa"}, map[string]string{prefix + "/heads/aa": "refs/heads/" + branch}, "the bundle has the ref refs/heads/" + branch + " twice"},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			bothChecks(t, func(t *testing.T) {
				src, base := person(t)
				before := testGit(t, src, "for-each-ref")
				d, repos := cloneRun(t, src)
				write(t, d.Work(), "x.txt", "x\n")
				commit := commitAll(t, d.Work(), "x")
				editedBundle(t, d, commit, base, c.names, c.edits)
				_, err := d.Quarantine(context.Background(), branch, repos)
				hasPrefix(t, name, err, "the run's bundle for . failed git's checks")
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Errorf("the error %v does not have %q", err, c.want)
				}
				if after := testGit(t, src, "for-each-ref"); after != before {
					t.Errorf("your repository's refs were\n%s\nand are\n%s", before, after)
				}
				noRunCommit(t, src, commit)
			})
		})
	}
}

// TestRunRefTakesTheFormsOfTheRunsRefs is the check of each ref name under the run's prefix.
func TestRunRefTakesTheFormsOfTheRunsRefs(t *testing.T) {
	for name, want := range map[string]bool{
		prefix + "/HEAD": true, prefix + "/uncommitted": true, prefix + "/stash": true,
		prefix + "/lost/1": true, prefix + "/lost/12": true, prefix + "/heads/a/b": true,
		prefix + "/tags/v1.0": true, prefix + "/heads/qory/run-1": true,
		prefix + "/lost/0": false, prefix + "/lost/": false, prefix + "/lost/x": false,
		prefix + "/lost/1234567890": false, prefix + "/heads/": false, prefix + "/heads/a//b": false,
		prefix + "/heads/.a": false, prefix + "/heads/a.": false, prefix + "/heads/a.lock": false,
		prefix + "/heads/a..b": false, prefix + "/heads/a@{1}": false, prefix + "/heads/@": false,
		prefix + "/heads/a b": false, prefix + "/heads/a~1": false, prefix + "/heads/a^": false,
		prefix + "/heads/a:b": false, prefix + "/heads/a?": false, prefix + "/heads/a*": false,
		prefix + "/heads/a[": false, prefix + "/heads/a\\b": false, prefix + "/heads/a\x7f": false,
		prefix + "/heads/a/": false, prefix + "/notes/a": false, prefix + "/HEADS/a": false,
		prefix: false, prefix + "-2/HEAD": false, "refs/heads/main": false, "HEAD": false,
	} {
		if got := runRef(prefix, name); got != want {
			t.Errorf("runRef(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestTheRunsRefsTakeAFreePrefix is a person's repository that has refs under the run's prefix, as
// after an earlier run with the same id: the run's refs come back under the next free prefix, and the
// earlier refs stay. A ref above every prefix, refs/qory, leaves none free, and the fetch stops.
func TestTheRunsRefsTakeAFreePrefix(t *testing.T) {
	for name, c := range map[string]struct{ ref, want string }{
		"a ref under it":        {prefix + "/heads/old", prefix + "-2"},
		"a ref in another case": {"refs/QORY/run-run-1/heads/old", prefix + "-2"},
		"a ref above it":        {"refs/qory", ""},
		"the prefix as a ref":   {prefix, prefix + "-2"},
		"a ref beside it":       {prefix + "-x/heads/old", prefix},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, base := person(t)
			testGit(t, src, "update-ref", c.ref, base)
			d, repos := cloneRun(t, src)
			write(t, d.Work(), "x.txt", "x\n")
			testGit(t, d.Work(), "stash", "--quiet", "--include-untracked")
			runGit(t, d, repos, nil)
			qs, err := d.Quarantine(context.Background(), branch, repos)
			if err != nil {
				t.Fatal(err)
			}
			_, got, err := qs[0].FetchInto(context.Background(), src, branch, "run-1")
			if c.want == "" {
				hasPrefix(t, name, err, "delete the refs under "+prefix+" and "+prefix+"-<number> in ")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("the refs came back under %s, want %s", got, c.want)
			}
			// On a disk that ignores case, for-each-ref lists a ref by the case of a folder that exists.
			testGit(t, src, "rev-parse", "--verify", "--quiet", c.want+"/stash")
			if at := testGit(t, src, "rev-parse", c.ref); at != base {
				t.Errorf("%s moved to %s", c.ref, at)
			}
		})
	}
}

// TestFetchIntoMovesNoBranchOfThePersons is a run's branch whose name the person's repository has:
// the checked-out main, and side on another line. Neither moves, the commits land on the next free
// name, a short name with no valid ref stops, and a second fetch lands on the name after that.
func TestFetchIntoMovesNoBranchOfThePersons(t *testing.T) {
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
	for _, c := range []struct{ name, want string }{{"main", "qory/run-run-1"}, {"side", "qory/run-run-1-2"}} {
		landed, prefix, err := q.FetchInto(ctx, src, c.name, "run-1")
		if err != nil {
			t.Fatal(err)
		}
		if landed != c.want || prefix != "" {
			t.Errorf("%s: the commits landed on %q with the prefix %q, want %q and none", c.name, landed, prefix, c.want)
		}
		testGit(t, src, "cat-file", "-e", c.want+":new.txt")
	}
	if got := testGit(t, src, "rev-parse", "refs/heads/main"); got != base {
		t.Errorf("main moved to %s", got)
	}
	if got := testGit(t, src, "rev-parse", "refs/heads/side"); got != side {
		t.Errorf("side moved to %s", got)
	}
	if got := testGit(t, src, "for-each-ref", "refs/qory/"); got != "" {
		t.Errorf("refs stay under refs/qory:\n%s", got)
	}
	_, _, err = q.FetchInto(ctx, src, "bad..name", "run-1")
	hasPrefix(t, "a name that is no ref", err, "give a full ref name")
	if _, err := d.Quarantine(ctx, branch, repos); err != nil {
		t.Fatal(err)
	}
}

// TestFetchIntoMakesNoBranchThatAppearedAfterTheFetch is a branch that the person makes at the
// base commit with the name of the run's branch, between the pick of the name and the fetch, as
// the fetch's lock wait leaves time for: the run's commits descend from it, and still it stays
// where the person made it, and the commits land on the next free name.
func TestFetchIntoMakesNoBranchThatAppearedAfterTheFetch(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, base := person(t)
	q := fetchQuarantine(t, src)
	testGit(t, src, "branch", branch, base)
	landed, prefix, err := q.FetchInto(context.Background(), src, branch, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if landed != "qory/run-run-1" || prefix != "" {
		t.Errorf("the commits landed on %q with the prefix %q", landed, prefix)
	}
	if got := testGit(t, src, "rev-parse", "refs/heads/"+branch); got != base {
		t.Errorf("%s moved to %s", branch, got)
	}
	testGit(t, src, "cat-file", "-e", landed+":new.txt")
}

// TestFetchIntoMakesNoBranchAWorkingTreeHasUnborn is a working tree of the person's repository on
// the run's branch with no commit yet, which no ref lists: qory makes no branch of that name, since
// the working tree would then be at the run's commit, and the commits stay under the prefix.
func TestFetchIntoMakesNoBranchAWorkingTreeHasUnborn(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	q := fetchQuarantine(t, src)
	tree := filepath.Join(t.TempDir(), "tree")
	testGit(t, src, "worktree", "add", "--quiet", "--detach", tree)
	testGit(t, tree, "checkout", "--quiet", "--orphan", branch)
	before := testGit(t, tree, "status", "--porcelain")
	landed, got, err := q.FetchInto(context.Background(), src, branch, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if landed != "" || got != prefix {
		t.Errorf("the commits landed on %q with the prefix %q, want none and %q", landed, got, prefix)
	}
	if err := gitCmd(src, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run(); err == nil {
		t.Errorf("your repository has the branch %s", branch)
	}
	testGit(t, src, "cat-file", "-e", prefix+"/branch:new.txt")
	if after := testGit(t, tree, "status", "--porcelain"); after != before {
		t.Errorf("the working tree's status was\n%s\nand is\n%s", before, after)
	}
}

// fetchQuarantine is the quarantine of a run that added one file to the person's repository at
// src.
func fetchQuarantine(t *testing.T, src string) *Quarantine {
	t.Helper()
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "new.txt", "new\n")
	runGit(t, d, repos, nil)
	qs, err := d.Quarantine(context.Background(), branch, repos)
	if err != nil {
		t.Fatal(err)
	}
	return qs[0]
}

// holdLock writes the lock file of the branch qory/run-1 in the repository at src, as a git
// process that writes the branch does.
func holdLock(t *testing.T, src string) string {
	t.Helper()
	lock := filepath.Join(src, ".git", "refs", "heads", "qory", "run-1.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return lock
}

// TestFetchIntoWaitsForALock is a lock on the run's branch that another git process holds for
// 2 seconds: the fetch runs again and lands the commits.
func TestFetchIntoWaitsForALock(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	q := fetchQuarantine(t, src)
	lock := holdLock(t, src)
	released := time.AfterFunc(2*time.Second, func() { _ = os.Remove(lock) })
	t.Cleanup(func() { released.Stop() })
	start := time.Now()
	if _, _, err := q.FetchInto(context.Background(), src, branch, "run-1"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 2*time.Second {
		t.Errorf("the fetch took %s, less than the lock's 2s", took)
	}
	testGit(t, src, "cat-file", "-e", branch+":new.txt")
}

// TestFetchIntoStopsAfterTheLockWaits is a lock on the run's branch that stays: git makes the
// branch once and once more per wait, then the commits stay under the run's prefix. The waits are
// 5 and last 30 s.
func TestFetchIntoStopsAfterTheLockWaits(t *testing.T) {
	var total time.Duration
	for _, w := range lockWaits {
		total += w
	}
	if len(lockWaits) != 5 || total != 30*time.Second {
		t.Errorf("the waits are %v, want 5 that last 30s", lockWaits)
	}
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	q := fetchQuarantine(t, src)
	holdLock(t, src)
	old := lockWaits
	lockWaits = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	t.Cleanup(func() { lockWaits = old })
	start := time.Now()
	landed, prefix, err := q.FetchInto(context.Background(), src, branch, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if landed != "" || prefix != "refs/qory/run-run-1" {
		t.Errorf("the commits landed on %q with the prefix %q", landed, prefix)
	}
	testGit(t, src, "cat-file", "-e", prefix+"/branch:new.txt")
	if took := time.Since(start); took < 50*time.Millisecond {
		t.Errorf("the fetch took %s, less than its waits", took)
	}
}

// TestAFailedWorktreeListMakesNoBranch is a git worktree list that fails after the fetch: a
// working tree may have the branch checked out, so qory makes no branch, and the commits stay under
// the run's prefix.
func TestAFailedWorktreeListMakesNoBranch(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	q := fetchQuarantine(t, src)
	old := worktreeList
	worktreeList = func(context.Context, string) (string, error) { return "", errors.New("git worktree: it failed") }
	t.Cleanup(func() { worktreeList = old })
	landed, prefix, err := q.FetchInto(context.Background(), src, branch, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if landed != "" || prefix != "refs/qory/run-run-1" {
		t.Errorf("the commits landed on %q with the prefix %q", landed, prefix)
	}
	testGit(t, src, "cat-file", "-e", prefix+"/branch:new.txt")
	if got := testGit(t, src, "for-each-ref", "refs/heads/qory/"); got != "" {
		t.Errorf("qory made a branch:\n%s", got)
	}
}

// joined is the errors that err joins, or err alone.
func joined(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

// TestAFailedFetchIntoASubmoduleLeavesYourRepositoryAsItWas is a run with a commit in the
// repository and a change in a submodule, and a lock that stays on the ref the submodule's change
// comes back to: the fetch into the submodule fails first, the fetch into the repository does not
// run, and the error has a fix for each. With the lock on the repository's ref instead, the
// submodule's refs come back, and the error lists them.
func TestAFailedFetchIntoASubmoduleLeavesYourRepositoryAsItWas(t *testing.T) {
	old := lockWaits
	lockWaits = []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { lockWaits = old })
	for _, inSubmodule := range []bool{true, false} {
		t.Run(fmt.Sprintf("in the submodule %t", inSubmodule), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			ctx := context.Background()
			src, sub := withSubmodule(t)
			gitDir := testGit(t, filepath.Join(src, sub), "rev-parse", "--path-format=absolute", "--git-dir")
			d, repos := cloneRun(t, src)
			write(t, d.Work(), filepath.Join(sub, "lib.txt"), "changed\n")
			write(t, d.Work(), "new.txt", "new\n")
			runGit(t, d, repos, nil)
			qs, err := d.Quarantine(ctx, branch, repos)
			if err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(src, ".git", filepath.FromSlash(prefix), "branch.lock")
			if inSubmodule {
				lock = filepath.Join(gitDir, filepath.FromSlash(prefix), "uncommitted.lock")
			}
			write(t, filepath.Dir(lock), filepath.Base(lock), "")
			_, _, err = qs[0].FetchInto(ctx, src, branch, "run-1")
			errs := joined(err)
			if len(errs) != 2 {
				t.Fatalf("the error joins %d errors, want 2: %v", len(errs), err)
			}
			if inSubmodule {
				hasPrefix(t, "the submodule's fetch", errs[0], "fetch "+prefix+"/* from "+shown(qs[0].Submodules[0].Path)+" by hand:")
				hasPrefix(t, "the repository's fetch", errs[1], "fetch refs/heads/"+branch+" from "+shown(qs[0].Path)+" into "+shown(src)+" by hand:")
				if got := testGit(t, src, "for-each-ref", "refs/qory/", "refs/heads/qory/"); got != "" {
					t.Errorf("your repository has refs of the run:\n%s", got)
				}
				return
			}
			hasPrefix(t, "the repository's fetch", errs[0], "fetch refs/heads/"+branch+" from "+shown(qs[0].Path)+" by hand:")
			hasPrefix(t, "the submodule's refs", errs[1], "list the run's refs with git for-each-ref "+prefix+" in the git folder of each submodule whose fetch worked:")
			wantRefs(t, refsUnder(t, gitDir, prefix), "uncommitted")
		})
	}
}

// TestFetchIntoStopsWaitingAtTheEndOfItsContext is a lock that stays and a context that ends
// during a wait: the commits stay under the run's prefix, and no wait follows.
func TestFetchIntoStopsWaitingAtTheEndOfItsContext(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	q := fetchQuarantine(t, src)
	holdLock(t, src)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	landed, _, err := q.FetchInto(ctx, src, branch, "run-1")
	if err == nil && landed != "" {
		t.Fatalf("the commits landed on %s past a held lock", landed)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the fetch took %s after its context ended", took)
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
		{"conflicts", r.Conflicts, []string{"both.txt"}},
		{"not deleted", r.NotDeleted, []string{"edited-gone.txt"}},
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
		"a.txt":             "changed\n",
		"both.txt":          "the person's edit\n",
		"both.txt-from-run": "the run's edit\n",
		"edited-gone.txt":   "the person's edit\n",
		"gone.txt":          "",
		"qory.yaml":         "env:\n  A: \"1\"\n",
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

// TestFreeBranchSkipsTakenNames is a person's repository, and one under .qory/repositories,
// with branches that have or block the run's branch name: FreeBranch picks the next free name,
// and the clone works on it.
func TestFreeBranchSkipsTakenNames(t *testing.T) {
	for _, c := range []struct {
		name            string
		person, further []string
		want            string
	}{
		{"no branch", nil, nil, "qory/run-1"},
		{"the name", []string{"qory/run-1"}, nil, "qory/run-1-2"},
		{"the name in a further repository", nil, []string{"qory/run-1"}, "qory/run-1-2"},
		{"a name below", []string{"qory/run-1/x", "qory/run-1-2"}, nil, "qory/run-1-3"},
		{"another case", []string{"QORY/run-1"}, nil, "qory/run-1-2"},
		{"a branch qory", []string{"qory"}, nil, "qory-run-1"},
		{"a branch qory in a further repository", nil, []string{"qory"}, "qory-run-1"},
		{"a branch qory and the other name", []string{"qory", "qory-run-1"}, nil, "qory-run-1-2"},
		{"other branches below qory", []string{"qory/run-10", "qory/other"}, nil, "qory/run-1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			src, base := person(t)
			further := filepath.Join(src, ".qory", "repositories", "other")
			if err := os.MkdirAll(further, 0o755); err != nil {
				t.Fatal(err)
			}
			testGit(t, further, "init", "--quiet", "-b", "main")
			write(t, further, "a.txt", "a\n")
			furtherBase := commitAll(t, further, "first")
			for _, b := range c.person {
				testGit(t, src, "branch", b, base)
			}
			for _, b := range c.further {
				testGit(t, further, "branch", b, furtherBase)
			}
			got, err := FreeBranch(context.Background(), src, "1")
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("FreeBranch = %s, want %s", got, c.want)
			}
			if _, err := newDir(t).Clone(context.Background(), src, got); err != nil {
				t.Errorf("the clone on %s: %v", got, err)
			}
		})
	}
}

// TestFreeBranchAgainKeepsAFreeName is the run's branch name at the fetch: a name that is
// still free stays, and a name taken by now gives way to the next free one.
func TestFreeBranchAgainKeepsAFreeName(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	src, base := person(t)
	testGit(t, src, "branch", "qory/run-1", base)
	if got, err := FreeBranchAgain(ctx, src, "qory/run-1-2", "1"); err != nil || got != "qory/run-1-2" {
		t.Errorf("a free name: %s, %v", got, err)
	}
	testGit(t, src, "branch", "qory/run-1-2", base)
	if got, err := FreeBranchAgain(ctx, src, "qory/run-1-2", "1"); err != nil || got != "qory/run-1-3" {
		t.Errorf("a name taken by now: %s, %v", got, err)
	}
	_, err := FreeBranchAgain(ctx, t.TempDir(), "qory/run-1", "1")
	hasPrefix(t, "no repository", err, "check that ")
}

// TestFreeBranchStopsWhenEveryNameIsTaken is a repository with every name FreeBranch tries:
// the line starts with the branches to delete.
func TestFreeBranchStopsWhenEveryNameIsTaken(t *testing.T) {
	branches := []string{"qory/run-1"}
	for n := 2; n <= branchTries; n++ {
		branches = append(branches, "qory/run-1-"+strconv.Itoa(n))
	}
	_, err := freeName(branches, "1", "/repo")
	hasPrefix(t, "every name taken", err, "delete the branches qory/run-1 and qory/run-1-<number> in /repo")
	if got, err := freeName(branches[:branchTries-1], "1", "/repo"); err != nil || got != "qory/run-1-100" {
		t.Errorf("one name left: %s, %v", got, err)
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

// TestCopyBackListsWhatItCannotReadOrWrite is a run's file and folder that qory cannot read,
// and a new file in a folder of the person's that qory cannot write: each is listed as failed,
// the rest comes back, and nothing in the folder that failed is deleted.
func TestCopyBackListsWhatItCannotReadOrWrite(t *testing.T) {
	isolate(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads and writes every file")
	}
	dest := t.TempDir()
	write(t, dest, "locked/kept.txt", "the person\n")
	write(t, dest, "closed/old.txt", "the person\n")
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	work := d.Work()
	write(t, work, "a.txt", "the run\n")
	write(t, work, "secret.txt", "the run\n")
	write(t, work, "z.txt", "the run\n")
	write(t, work, "closed/new.txt", "the run\n")
	for p, mode := range map[string]os.FileMode{
		filepath.Join(work, "secret.txt"): 0,
		filepath.Join(work, "locked"):     0,
		filepath.Join(dest, "closed"):     0o555,
	} {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dest, "closed"), 0o755) })
	r, err := d.CopyBack(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"closed/new.txt", "locked", "secret.txt"}; !slices.Equal(r.Failed, want) {
		t.Errorf("failed %q, want %q", r.Failed, want)
	}
	if want := []string{"a.txt", "z.txt"}; !slices.Equal(r.Copied, want) {
		t.Errorf("copied %q, want %q", r.Copied, want)
	}
	if len(r.Deleted) != 0 || read(t, dest, "locked/kept.txt") != "the person\n" {
		t.Errorf("the copy back deleted %q", r.Deleted)
	}
	if read(t, dest, "z.txt") != "the run\n" {
		t.Error("the file after the failed ones did not come back")
	}
}

// TestCopyBackListsADeletionThatFails is a run that deletes a file in a folder of the person's
// that qory cannot write, and another one elsewhere: the first is listed as failed and stays, and
// the second is deleted.
func TestCopyBackListsADeletionThatFails(t *testing.T) {
	isolate(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes every folder")
	}
	dest := t.TempDir()
	write(t, dest, "closed/old.txt", "the person\n")
	write(t, dest, "z.txt", "the person\n")
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"closed/old.txt", "z.txt"} {
		if err := os.Remove(filepath.Join(d.Work(), filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dest, "closed"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dest, "closed"), 0o755) })
	r, err := d.CopyBack(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"closed/old.txt"}; !slices.Equal(r.Failed, want) {
		t.Errorf("failed %q, want %q", r.Failed, want)
	}
	if want := []string{"z.txt"}; !slices.Equal(r.Deleted, want) {
		t.Errorf("deleted %q, want %q", r.Deleted, want)
	}
	if read(t, dest, "closed/old.txt") != "the person\n" {
		t.Error("closed/old.txt is gone")
	}
}

// TestCopyBackCountsNoGeneratedFolder is a run that writes more into node_modules and the other
// folders that a build writes than the budget allows: all of it comes back, and a file outside
// them still counts.
func TestCopyBackCountsNoGeneratedFolder(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	var want []string
	for name := range generatedFolders {
		for i := range 3 {
			rel := name + "/sub/f" + strconv.Itoa(i)
			write(t, d.Work(), rel, "generated\n")
			want = append(want, rel)
		}
	}
	r, err := d.copyBackWith(context.Background(), src, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(want)
	if slices.Sort(r.Copied); !slices.Equal(r.Copied, want) {
		t.Errorf("copied %q, want %q", r.Copied, want)
	}
	write(t, d.Work(), "a.txt", "counted\n")
	write(t, d.Work(), "b.txt", "counted\n")
	write(t, d.Work(), "c.txt", "counted\n")
	if _, err := d.copyBackWith(context.Background(), src, 2, 1<<20); err == nil {
		t.Error("three files passed a budget of two")
	}
}

// TestCopyBackCountsAFileNamedAsAGeneratedFolder is a run that writes regular files named dist and
// build: they are no folders of a build, so they count toward the budget.
func TestCopyBackCountsAFileNamedAsAGeneratedFolder(t *testing.T) {
	isolate(t)
	src := t.TempDir()
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	write(t, d.Work(), "dist", "counted\n")
	write(t, d.Work(), "sub/build", "counted\n")
	write(t, d.Work(), "x.txt", "counted\n")
	if _, err := d.copyBackWith(context.Background(), src, 2, 1<<20); err == nil {
		t.Error("three files passed a budget of two")
	}
}

// TestCopyBackLeavesOutGeneratedFoldersPastTheirLimit is a run that writes more files into the
// folders of a build than their limit allows: each such folder stays out, with all below it, and
// is listed, the rest comes back, and nothing below them is deleted.
func TestCopyBackLeavesOutGeneratedFoldersPastTheirLimit(t *testing.T) {
	isolate(t)
	files, bytes := MaxGeneratedFiles, MaxGeneratedBytes
	t.Cleanup(func() { MaxGeneratedFiles, MaxGeneratedBytes = files, bytes })
	for _, c := range []struct {
		name  string
		files int
		bytes int64
	}{{"files", 2, 1 << 20}, {"bytes", 100, 20}} {
		t.Run(c.name, func(t *testing.T) {
			MaxGeneratedFiles, MaxGeneratedBytes = c.files, c.bytes
			src := t.TempDir()
			write(t, src, "node_modules/old.js", "old\n")
			d := newDir(t)
			if err := d.CopyFolder(context.Background(), src); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(d.Work(), "node_modules", "old.js")); err != nil {
				t.Fatal(err)
			}
			write(t, d.Work(), "node_modules/a/b.js", "generated\n")
			write(t, d.Work(), "pkg/dist/c.js", "generated\n")
			write(t, d.Work(), "pkg/dist/build/d.js", "generated\n")
			write(t, d.Work(), "dist", "a file\n")
			r, err := d.CopyBack(context.Background(), src)
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"node_modules", "pkg/dist"}; !slices.Equal(r.Generated, want) {
				t.Errorf("left out %q, want %q", r.Generated, want)
			}
			if want := []string{"dist"}; !slices.Equal(r.Copied, want) {
				t.Errorf("copied %q, want %q", r.Copied, want)
			}
			if len(r.Deleted) != 0 || read(t, src, "node_modules/old.js") != "old\n" {
				t.Errorf("the copy back deleted %q", r.Deleted)
			}
			if _, err := os.Lstat(filepath.Join(src, "pkg", "dist")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("pkg/dist came back: %v", err)
			}
		})
	}
}

// TestCopyBackCountsGeneratedFolders is a run that makes empty folders in node_modules: each folder
// counts toward the limit of files in the folders of a build, so past it node_modules stays out and
// no folder comes back. At the limit, they come back.
func TestCopyBackCountsGeneratedFolders(t *testing.T) {
	isolate(t)
	files := MaxGeneratedFiles
	t.Cleanup(func() { MaxGeneratedFiles = files })
	for _, c := range []struct {
		limit int
		out   bool
	}{{5, true}, {6, false}} {
		t.Run(strconv.Itoa(c.limit), func(t *testing.T) {
			MaxGeneratedFiles = c.limit
			src := t.TempDir()
			d := newDir(t)
			if err := d.CopyFolder(context.Background(), src); err != nil {
				t.Fatal(err)
			}
			// node_modules and five folders in it: six entries.
			for i := range 5 {
				if err := os.MkdirAll(filepath.Join(d.Work(), "node_modules", "d"+strconv.Itoa(i)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			r, err := d.CopyBack(context.Background(), src)
			if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Lstat(filepath.Join(src, "node_modules", "d4"))
			if c.out {
				if want := []string{"node_modules"}; !slices.Equal(r.Generated, want) {
					t.Errorf("left out %q, want %q", r.Generated, want)
				}
				if !errors.Is(statErr, os.ErrNotExist) {
					t.Errorf("a folder in node_modules came back: %v", statErr)
				}
				return
			}
			if len(r.Generated) != 0 || statErr != nil {
				t.Errorf("left out %q, and node_modules/d4: %v", r.Generated, statErr)
			}
		})
	}
}

// TestCopyBackTakesALargeNodeModules is a run that writes 200,000 files into node_modules, twice
// the budget of files: the copy back brings all of them.
func TestCopyBackTakesALargeNodeModules(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 200,000 files")
	}
	isolate(t)
	src := t.TempDir()
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	const folders, files = 200, 1000
	for i := range folders {
		dir := filepath.Join(d.Work(), "node_modules", "p"+strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for j := range files {
			if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(j)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	r, err := d.CopyBack(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Copied) != folders*files || len(r.Failed) != 0 {
		t.Errorf("copied %d files, failed %q", len(r.Copied), r.Failed)
	}
}

// TestCopyBackWritesTheRunsVersionBeside is a file that the person and the run both changed, one
// the person deleted and the run changed, and one both added: the person's file stays, and the
// run's version goes beside it with -from-run, or with a number after that when the person or the
// run has that name.
func TestCopyBackWritesTheRunsVersionBeside(t *testing.T) {
	isolate(t)
	dest := t.TempDir()
	for _, rel := range []string{"both.txt", "again.txt", "taken.txt", "deleted.txt", "tool.sh"} {
		write(t, dest, rel, "first\n")
	}
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	work := d.Work()
	for _, rel := range []string{"both.txt", "again.txt", "taken.txt", "deleted.txt", "tool.sh", "added.txt"} {
		write(t, work, rel, "the run\n")
		if rel != "deleted.txt" {
			write(t, dest, rel, "the person\n")
		}
	}
	if err := os.Chmod(filepath.Join(work, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dest, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, dest, "again.txt-from-run", "an earlier run\n")
	write(t, work, "taken.txt-from-run", "the run's other file\n")
	r, err := d.CopyBack(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"added.txt", "again.txt", "both.txt", "deleted.txt", "taken.txt", "tool.sh"}; !slices.Equal(r.Conflicts, want) {
		t.Errorf("conflicts %q, want %q", r.Conflicts, want)
	}
	if want := []string{"taken.txt-from-run"}; !slices.Equal(r.Copied, want) {
		t.Errorf("copied %q, want %q", r.Copied, want)
	}
	for rel, want := range map[string]string{
		"both.txt":             "the person\n",
		"both.txt-from-run":    "the run\n",
		"again.txt":            "the person\n",
		"again.txt-from-run":   "an earlier run\n",
		"again.txt-from-run-1": "the run\n",
		"taken.txt-from-run":   "the run's other file\n",
		"taken.txt-from-run-1": "the run\n",
		"deleted.txt":          "",
		"deleted.txt-from-run": "the run\n",
		"added.txt":            "the person\n",
		"added.txt-from-run":   "the run\n",
	} {
		if got := read(t, dest, rel); got != want {
			t.Errorf("%s is %q, want %q", rel, got, want)
		}
	}
	if info, err := os.Stat(filepath.Join(dest, "tool.sh-from-run")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("tool.sh-from-run has no execute bit: %v, %v", info, err)
	}
}

// TestCopyBackListsADeletionOfAChangedFile is a file that the run deleted and the person changed:
// it stays, is listed as not deleted, and no file goes beside it.
func TestCopyBackListsADeletionOfAChangedFile(t *testing.T) {
	isolate(t)
	dest := t.TempDir()
	write(t, dest, "kept.txt", "first\n")
	d := newDir(t)
	if err := d.CopyFolder(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(d.Work(), "kept.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, dest, "kept.txt", "the person\n")
	r, err := d.CopyBack(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.NotDeleted, []string{"kept.txt"}) || len(r.Conflicts)+len(r.Deleted) != 0 {
		t.Errorf("not deleted %q, conflicts %q, deleted %q", r.NotDeleted, r.Conflicts, r.Deleted)
	}
	if read(t, dest, "kept.txt") != "the person\n" {
		t.Error("the person's file changed")
	}
	if _, err := os.Lstat(filepath.Join(dest, "kept.txt-from-run")); err == nil {
		t.Error("a file went beside the one the run deleted")
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
	if _, err := d.cloneSubmodules(context.Background(), src, d.Work(), base); err != nil {
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
	if _, _, err := qs[0].FetchInto(context.Background(), src, branch, "run-1"); err != nil {
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
	if !slices.Contains(got.Conflicts, store+"mine/both.txt") || read(t, src, store+"mine/both.txt-from-run") != "the run\n" {
		t.Errorf("the conflicts are %v, and the run's version is not beside the person's", got.Conflicts)
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
	if leftBehind(d, repos) {
		t.Error("the session recorded work left behind, with nothing left behind")
	}
}

// TestAChangeOnAScratchBranchArrivesUnderRefsQory is the same with a file left in the working
// tree, which the branch does not start from: the branch comes back as it is, and the file comes
// back in a commit on HEAD, under the run's prefix.
func TestAChangeOnAScratchBranchArrivesUnderRefsQory(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, base := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	testGit(t, d.Work(), "checkout", "--quiet", "-b", "scratch", base)
	write(t, d.Work(), "scratch.txt", "x\n")
	q, refs := runRefs(t, d, src, repos, nil)
	got := files(t, src, "refs/heads/"+branch)
	if !slices.Contains(got, "kept.txt") || slices.Contains(got, "scratch.txt") {
		t.Errorf("the branch at %s has %v", q.Head, got)
	}
	// The branch scratch is at the base commit, which the person has.
	wantRefs(t, refs, "uncommitted", "heads/scratch")
	if refs["heads/scratch"] != base {
		t.Errorf("scratch came back at %s, want %s", refs["heads/scratch"], base)
	}
	if got := files(t, src, prefix+"/uncommitted"); !slices.Contains(got, "scratch.txt") || slices.Contains(got, "kept.txt") {
		t.Errorf("uncommitted has %v", got)
	}
	if got := testGit(t, src, "rev-parse", prefix+"/uncommitted^"); got != base {
		t.Errorf("uncommitted has the parent %s, want HEAD %s", got, base)
	}
}

// TestACommitOnAnotherBranchArrivesUnderRefsQory is an agent that committed on a branch and then
// checked out the run's branch: the branch comes back under the run's prefix, and the run's
// branch, with no commit, does not.
func TestACommitOnAnotherBranchArrivesUnderRefsQory(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "checkout", "--quiet", "-b", "feature")
	write(t, d.Work(), "feature.txt", "x\n")
	tip := commitAll(t, d.Work(), "on feature")
	testGit(t, d.Work(), "checkout", "--quiet", branch)
	q, refs := runRefs(t, d, src, repos, nil)
	if q.Head != "" {
		t.Errorf("the run's branch came back at %s", q.Head)
	}
	wantRefs(t, refs, "heads/feature")
	if refs["heads/feature"] != tip {
		t.Errorf("heads/feature is at %s, want %s", refs["heads/feature"], tip)
	}
	if out := testGit(t, src, "for-each-ref", "refs/heads/qory/"); out != "" {
		t.Errorf("a run's branch came back: %s", out)
	}
}

// TestABranchPastHeadArrivesUnderRefsQory is commits on two branches from the base commit: HEAD's
// come back on the run's branch, and the run's branch in the copy under the run's prefix.
func TestABranchPastHeadArrivesUnderRefsQory(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, base := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "one.txt", "x\n")
	one := commitAll(t, d.Work(), "on the branch")
	testGit(t, d.Work(), "checkout", "--quiet", "-b", "side", base)
	write(t, d.Work(), "two.txt", "x\n")
	commitAll(t, d.Work(), "on side")
	_, refs := runRefs(t, d, src, repos, nil)
	if got := files(t, src, "refs/heads/"+branch); !slices.Contains(got, "two.txt") {
		t.Errorf("the branch has %v", got)
	}
	wantRefs(t, refs, "heads/side", "heads/"+branch)
	if refs["heads/"+branch] != one {
		t.Errorf("heads/%s is at %s, want %s", branch, refs["heads/"+branch], one)
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
	if leftBehind(d, repos) {
		t.Error("the session recorded work left behind, with nothing left behind")
	}
}

// TestAFolderGitCannotReadComesBack is new work in a folder of mode 000 and in a file of mode 000,
// which the session's user owns: the session gives itself read access, and the work comes back.
func TestAFolderGitCannotReadComesBack(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "ok.txt", "ok\n")
	write(t, d.Work(), "locked/new.txt", "x\n")
	write(t, d.Work(), "unread.txt", "y\n")
	for _, p := range []string{"locked/new.txt", "unread.txt", "locked"} {
		locked := filepath.Join(d.Work(), filepath.FromSlash(p))
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	}
	q, _ := runRefs(t, d, src, repos, nil)
	got := files(t, q.Path, q.Head)
	for _, want := range []string{"ok.txt", "locked/new.txt", "unread.txt"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s did not come back: %v", want, got)
		}
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
	if leftBehind(d, repos) {
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
	if !leftBehind(d, repos) {
		t.Error("the session did not record the embedded repository")
	}
	if left := d.Left(repos); left.Nested || !left.Behind {
		t.Errorf("Left = %+v, want work git could not add", left)
	}
}

// noBundleWith runs the git session for repos with out, and wants nothing to come back and
// nothing left behind.
func noBundleWith(t *testing.T, d Dir, repos []Repository, out map[string]LeftOut) {
	t.Helper()
	printed := sessionOutput(t, d, repos, out)
	if _, err := d.Quarantine(context.Background(), branch, repos); !errors.Is(err, ErrNoBundle) {
		t.Errorf("Quarantine = %v, want no bundle\n%s", err, printed)
	}
	if leftBehind(d, repos) {
		t.Errorf("the session recorded work left behind\n%s", printed)
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
	if got := leftBehind(d, repos); got != left {
		t.Errorf("left behind = %v, want %v\n%s", got, left, printed)
	}
	return printed
}

// TestAHeadBeforeTheBaseCommitCommitsNothing is an agent that checked out a commit older than the
// base commit: the working tree lacks the base commit's files, and the session commits no deletion
// of them. With nothing else in the copy nothing comes back, and a change there comes back in a
// commit on HEAD, under the run's prefix.
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
			if !changed {
				noBundle(t, d, repos, false)
				return
			}
			write(t, d.Work(), "README.md", "changed\n")
			_, refs := runRefs(t, d, src, repos, nil)
			wantRefs(t, refs, "uncommitted")
			if got := files(t, src, prefix+"/uncommitted"); slices.Contains(got, "newer.txt") {
				t.Errorf("uncommitted has %v", got)
			}
			if got := testGit(t, src, "show", prefix+"/uncommitted:README.md"); got != "changed" {
				t.Errorf("uncommitted has README.md %q", got)
			}
		})
	}
}

// TestAResetBelowTheBaseCommitCommitsNothing is an agent that runs reset --hard HEAD~1 on the run's
// branch and leaves a new file: HEAD and the branch are one commit, older than the base commit, so
// a commit of that working tree on the branch would delete the base commit's file. The branch stays
// at the base commit, and the new file comes back in a commit on HEAD, under the run's prefix.
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
	q, refs := runRefs(t, d, src, repos, nil)
	if q.Head != "" {
		t.Errorf("the run's branch came back at %s", q.Head)
	}
	wantRefs(t, refs, "uncommitted")
	got := files(t, src, prefix+"/uncommitted")
	if !slices.Contains(got, "left.txt") || slices.Contains(got, "newer.txt") {
		t.Errorf("uncommitted has %v", got)
	}
}

// TestAnOrphanHeadCommitsNothing is an agent on a branch with no commit, past the run's branch:
// the session commits no deletion of every tracked file on the branch, the branch comes back as
// it is, and the new file comes back in a commit with no parent, under the run's prefix.
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
	q, refs := runRefs(t, d, src, repos, nil)
	if q.Head != tip {
		t.Errorf("the bundle has %s, want the agent's commit %s", q.Head, tip)
	}
	wantRefs(t, refs, "uncommitted")
	if got := files(t, src, prefix+"/uncommitted"); !slices.Equal(got, []string{"new.txt"}) {
		t.Errorf("uncommitted has %v", got)
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

// TestWorkOutsideTheBranchArrivesUnderRefsQory is each way an agent can leave work that the run's
// branch does not have, with nothing past the base commit: the work comes back under the run's
// prefix, and no copy stays.
func TestWorkOutsideTheBranchArrivesUnderRefsQory(t *testing.T) {
	for name, c := range map[string]struct {
		agent func(t *testing.T, work, base string)
		refs  []string
	}{
		"stash": {func(t *testing.T, work, _ string) {
			write(t, work, "README.md", "stashed\n")
			testGit(t, work, "stash", "--quiet")
		}, []string{"stash"}},
		"tag then reset": {func(t *testing.T, work, base string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			testGit(t, work, "tag", "keep")
			testGit(t, work, "reset", "--quiet", "--hard", base)
		}, []string{"tags/keep"}},
		"annotated tag then reset": {func(t *testing.T, work, base string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			testGit(t, work, "tag", "-a", "-m", "keep", "keep")
			testGit(t, work, "reset", "--quiet", "--hard", base)
		}, []string{"tags/keep"}},
		"reset": {func(t *testing.T, work, base string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			testGit(t, work, "reset", "--quiet", "--hard", base)
		}, []string{"lost/1"}},
		"detached commit": {func(t *testing.T, work, _ string) {
			testGit(t, work, "checkout", "--quiet", "--detach")
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			testGit(t, work, "checkout", "--quiet", branch)
		}, []string{"lost/1"}},
		"two resets": {func(t *testing.T, work, base string) {
			write(t, work, "x.txt", "x\n")
			commitAll(t, work, "x")
			write(t, work, "y.txt", "y\n")
			commitAll(t, work, "y")
			testGit(t, work, "reset", "--quiet", "--hard", "HEAD~1")
			testGit(t, work, "reset", "--quiet", "--hard", base)
		}, []string{"lost/1"}},
		"notes": {func(t *testing.T, work, _ string) {
			testGit(t, work, "notes", "add", "-m", "a note")
		}, []string{"lost/1"}},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, base := person(t)
			d, repos := cloneRun(t, src)
			c.agent(t, d.Work(), base)
			q, refs := runRefs(t, d, src, repos, nil)
			if q.Head != "" {
				t.Errorf("the run's branch came back at %s", q.Head)
			}
			wantRefs(t, refs, c.refs...)
			for name := range refs {
				if got := testGit(t, src, "rev-parse", prefix+"/"+name+"^{commit}"); got == base {
					t.Errorf("%s is at the base commit", name)
				}
			}
		})
	}
}

// TestTwoStashesBesideTheBranchArriveUnderRefsQory is an agent with a commit on the branch and two
// stashes: the branch comes back, the newer stash as stash, and the older one as lost/1.
func TestTwoStashesBesideTheBranchArriveUnderRefsQory(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	write(t, d.Work(), "README.md", "older\n")
	testGit(t, d.Work(), "stash", "--quiet")
	older := testGit(t, d.Work(), "rev-parse", "refs/stash")
	write(t, d.Work(), "README.md", "newer\n")
	testGit(t, d.Work(), "stash", "--quiet")
	newer := testGit(t, d.Work(), "rev-parse", "refs/stash")
	q, refs := runRefs(t, d, src, repos, nil)
	if !slices.Contains(files(t, src, q.Head), "kept.txt") {
		t.Error("kept.txt did not come back")
	}
	wantRefs(t, refs, "stash", "lost/1")
	if refs["stash"] != newer || refs["lost/1"] != older {
		t.Errorf("stash is %s and lost/1 %s, want %s and %s", refs["stash"], refs["lost/1"], newer, older)
	}
}

// TestAStashThatAddsQoryFailsTheQuarantine is a stash whose untracked files have .qory at the top,
// which git stash apply would write: nothing comes back.
func TestAStashThatAddsQoryFailsTheQuarantine(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), ".qory/qory.yaml", "env:\n  A: \"2\"\n")
	testGit(t, d.Work(), "stash", "--quiet", "--include-untracked")
	printed := sessionOutput(t, d, repos, nil)
	_, err := d.Quarantine(context.Background(), branch, repos)
	hasPrefix(t, "a stash with .qory", err, "the run's commit ")
	if err == nil || !strings.Contains(err.Error(), "adds .qory at the top") {
		t.Errorf("the error is %v\n%s", err, printed)
	}
	noRunCommit(t, src, testGit(t, d.Work(), "rev-parse", "refs/stash"))
}

// TestADetachedCommitBesideTheBranchArrivesUnderRefsQory is an agent with a commit on the branch and
// one made with HEAD detached: the branch comes back, and the other commit as lost/1.
func TestADetachedCommitBesideTheBranchArrivesUnderRefsQory(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	testGit(t, d.Work(), "checkout", "--quiet", "--detach")
	write(t, d.Work(), "x.txt", "x\n")
	detached := commitAll(t, d.Work(), "x")
	testGit(t, d.Work(), "checkout", "--quiet", branch)
	_, refs := runRefs(t, d, src, repos, nil)
	wantRefs(t, refs, "lost/1")
	if refs["lost/1"] != detached {
		t.Errorf("lost/1 is at %s, want %s", refs["lost/1"], detached)
	}
}

// TestAnUnrelatedHeadArrivesUnderRefsQory is a detached commit that does not descend from the base
// commit, with the branch past that commit: the branch comes back, and the commit as HEAD. The
// working tree is at that commit, so the session finds no change there.
func TestAnUnrelatedHeadArrivesUnderRefsQory(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	write(t, d.Work(), "kept.txt", "kept\n")
	commitAll(t, d.Work(), "on the branch")
	tip := testGit(t, d.Work(), "rev-parse", "HEAD")
	testGit(t, d.Work(), "checkout", "--quiet", "--orphan", "y")
	write(t, d.Work(), "y.txt", "y\n")
	y := commitAll(t, d.Work(), "y")
	testGit(t, d.Work(), "checkout", "--quiet", "--detach")
	testGit(t, d.Work(), "branch", "-q", "-D", "y")
	q, refs := runRefs(t, d, src, repos, nil)
	if q.Head != tip {
		t.Errorf("the bundle has %s, want %s", q.Head, tip)
	}
	wantRefs(t, refs, "HEAD")
	if refs["HEAD"] != y {
		t.Errorf("HEAD is at %s, want %s", refs["HEAD"], y)
	}
}

// TestASubmodulePastItsGitlinkArrivesUnderRefsQory is a commit or a change in a submodule: it
// comes back under the run's prefix in the person's git folder of the submodule, as HEAD or as
// uncommitted, and no ref outside the prefix there moves.
func TestASubmodulePastItsGitlinkArrivesUnderRefsQory(t *testing.T) {
	for name, commit := range map[string]bool{"commit": true, "change": false} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			gitDir := testGit(t, filepath.Join(src, sub), "rev-parse", "--path-format=absolute", "--git-dir")
			before := testGit(t, gitDir, "for-each-ref")
			head := testGit(t, gitDir, "rev-parse", "HEAD")
			d, repos := cloneRun(t, src)
			inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
			write(t, inner, "lib.txt", "changed\n")
			want := "uncommitted"
			if commit {
				commitAll(t, inner, "in the submodule")
				want = "HEAD"
			}
			q, refs := runRefs(t, d, src, repos, nil)
			wantRefs(t, refs)
			if q.Head != "" || len(q.Submodules) != 1 {
				t.Fatalf("the quarantine has the head %q and %d submodules", q.Head, len(q.Submodules))
			}
			got := refsUnder(t, gitDir, prefix)
			wantRefs(t, got, want)
			if text := testGit(t, gitDir, "show", prefix+"/"+want+":lib.txt"); text != "changed" {
				t.Errorf("lib.txt in %s is %q", want, text)
			}
			var after []string
			for line := range strings.Lines(testGit(t, gitDir, "for-each-ref")) {
				if !strings.Contains(line, "\t"+prefix+"/") {
					after = append(after, strings.TrimSuffix(line, "\n"))
				}
			}
			if strings.Join(after, "\n") != before {
				t.Errorf("the submodule's refs were\n%s\nand are\n%s", before, after)
			}
			if got := testGit(t, gitDir, "rev-parse", "HEAD"); got != head {
				t.Errorf("the submodule's HEAD moved to %s", got)
			}
		})
	}
}

// TestAFilterPlantedInASubmoduleRunsNothing is an agent that writes a clean filter into the
// configuration of a submodule and an attribute that applies it, and changes a file there: the
// session reads the submodule's changes with a configuration it made, so the filter never runs,
// and the change comes back.
func TestAFilterPlantedInASubmoduleRunsNothing(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	gitDir := testGit(t, filepath.Join(src, sub), "rev-parse", "--path-format=absolute", "--git-dir")
	d, repos := cloneRun(t, src)
	inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
	marker := filepath.Join(t.TempDir(), "ran")
	testGit(t, inner, "config", "filter.planted.clean", "touch "+marker+"; cat")
	write(t, inner, ".gitattributes", "* filter=planted\n")
	// The same size and a later time: a status of the submodule's index hashes the file, through
	// the filter.
	write(t, inner, "lib.txt", "LIB\n")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(inner, "lib.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	_, refs := runRefs(t, d, src, repos, nil)
	wantRefs(t, refs)
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the planted filter ran: %v", err)
	}
	wantRefs(t, refsUnder(t, gitDir, prefix), "uncommitted")
	if text := testGit(t, gitDir, "show", prefix+"/uncommitted:lib.txt"); text != "LIB" {
		t.Errorf("lib.txt is %q", text)
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
			if left := d.Left(repos); !left.Nested || left.Behind {
				t.Errorf("Left = %+v, want a nested repository alone", left)
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
			if leftBehind(d, repos) {
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
			if leftBehind(d, repos) {
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
			_, refs := runRefs(t, d, src, repos, nil)
			wantRefs(t, refs, "uncommitted")
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
	if strings.Contains(printed, "that the run made") || leftBehind(d, repos) {
		t.Errorf("left behind = %v:\n%s", leftBehind(d, repos), printed)
	}
	qs, err := d.Quarantine(context.Background(), branch, repos)
	if err != nil {
		t.Fatalf("Quarantine: %v\n%s", err, printed)
	}
	if !slices.Contains(files(t, qs[0].Path, qs[0].Head), "x.txt") {
		t.Errorf("the commit did not come back:\n%s", printed)
	}
}

// TestAChangeInANestedSubmoduleIsLeftBehind is an agent that updates a nested submodule and changes
// a file there without a commit: the session prints a line for the submodule around it, and the copy
// stays. A planted fsmonitor in the nested submodule's configuration never runs.
func TestAChangeInANestedSubmoduleIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src := nestedSubmodule(t)
	d, repos := cloneRun(t, src)
	updateSubmodules(t, d.Work())
	inner := filepath.Join(d.Work(), "libs", "m", "inner")
	program, ran := marker(t)
	testGit(t, inner, "config", "core.fsmonitor", program)
	write(t, inner, "lib.txt", "changed\n")
	printed := sessionOutput(t, d, repos, nil)
	if want := "qory: the submodule libs/m in the copy has a git repository with commits or changes that the commit leaves out"; !strings.Contains(printed, want) {
		t.Errorf("the session printed no line for libs/m:\n%s", printed)
	}
	if !leftBehind(d, repos) {
		t.Errorf("the session recorded no work left behind:\n%s", printed)
	}
	if b, err := os.ReadFile(ran); err == nil {
		t.Errorf("the planted fsmonitor ran:\n%s", b)
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
	if leftBehind(d, repos) {
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
	submodules, partial, err := d.SubmoduleSetup(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]LeftOut{r.Path: {Setup: states, Submodules: submodules, SubmodulesPartial: partial}}
	recordRepositories(t, d, []Repository{r}, out)
	return out[r.Path]
}

// recordRepositories runs the record session for repos on this machine with sh, in place of its
// container, sets the states in out from it, and removes its folder, so a further call can make it.
func recordRepositories(t *testing.T, d Dir, repos []Repository, out map[string]LeftOut) {
	t.Helper()
	s, needed, err := d.RecordSession(t.Context(), repos, out)
	if err != nil || !needed {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	printed, err := boundedSession(t, s)
	if err != nil {
		t.Fatalf("record session: %v\n%s", err, printed)
	}
	d.Recorded(repos, out, true)
	if err := os.RemoveAll(d.SetupRecord()); err != nil {
		t.Fatal(err)
	}
}

// gitlinks commits in the repository at dir a gitlink at each of paths, at commit.
func gitlinks(t *testing.T, dir, commit string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		testGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+commit+","+p)
	}
	testGit(t, dir, "commit", "--quiet", "-m", "gitlinks")
}

// TestAGitlinkThatIsALinkIsLeftBehind is a submodule with gitlinks that are links, or go through
// one: to the submodule's folder, which would repeat its status without end, or to a repository
// outside the copy at the gitlink's commit with no change. The status does not follow them, the
// session ends within 30 seconds, and the copy stays.
func TestAGitlinkThatIsALinkIsLeftBehind(t *testing.T) {
	for name, c := range map[string]struct {
		nested  bool
		outside bool
		paths   []string
		links   map[string]string
	}{
		"two links to the folder":         {paths: []string{"a", "b"}, links: map[string]string{"a": ".", "b": "."}},
		"three links to the folder":       {paths: []string{"a", "b", "c"}, links: map[string]string{"a": ".", "b": ".", "c": "."}},
		"two links in a nested submodule": {nested: true, paths: []string{"a", "b"}, links: map[string]string{"a": ".", "b": "."}},
		"a link to a repository":          {outside: true, paths: []string{"a"}, links: map[string]string{"a": "a"}},
		"a gitlink through a link":        {outside: true, paths: []string{"x/a"}, links: map[string]string{"x": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			var src, s string
			if c.nested {
				src = nestedSubmodule(t)
			} else {
				src, _ = withSubmodule(t)
			}
			d, repos := cloneRun(t, src)
			if c.nested {
				updateSubmodules(t, d.Work())
				s = filepath.Join(d.Work(), "libs", "m", "inner")
			} else {
				s = filepath.Join(d.Work(), "libs", "s")
			}
			commit := testGit(t, s, "rev-parse", "HEAD")
			outside := t.TempDir()
			if c.outside {
				repo := filepath.Join(outside, "a")
				write(t, repo, "f.txt", "f\n")
				testGit(t, repo, "init", "--quiet", "-b", "main")
				commit = commitAll(t, repo, "outside")
			}
			gitlinks(t, s, commit, c.paths...)
			for l, target := range c.links {
				if c.outside {
					target = filepath.Join(outside, target)
				}
				link(t, s, l, target)
			}
			start := time.Now()
			printed := sessionOutput(t, d, repos, nil)
			if took := time.Since(start); took > 30*time.Second {
				t.Errorf("the session took %s", took)
			}
			if !leftBehind(d, repos) {
				t.Errorf("the session recorded no work left behind:\n%s", printed)
			}
		})
	}
}

// nest makes a chain of levels repositories below the folder dir, each in the folder d of the one
// above it, with one commit that has the file f.txt and the gitlink d of the next, and commits a
// gitlink at d in the repository at dir.
func nest(t *testing.T, dir string, levels int) {
	t.Helper()
	paths := make([]string, levels)
	p := dir
	for i := range paths {
		p = filepath.Join(p, "d")
		paths[i] = p
	}
	for i := levels - 1; i >= 0; i-- {
		write(t, paths[i], "f.txt", "f\n")
		testGit(t, paths[i], "init", "--quiet", "-b", "main")
		testGit(t, paths[i], "add", "f.txt")
		if i+1 < levels {
			gitlinks(t, paths[i], testGit(t, paths[i+1], "rev-parse", "HEAD"), "d")
		} else {
			testGit(t, paths[i], "commit", "--quiet", "-m", "level")
		}
	}
	gitlinks(t, dir, testGit(t, paths[0], "rev-parse", "HEAD"), "d")
}

// TestASubmoduleMoreThanEightDeepIsLeftBehind is an agent that commits a submodule the clone did
// not copy, the first of a chain of repositories, with no change: at 8 submodules deep the session
// checks each, and at 9 the copy stays.
func TestASubmoduleMoreThanEightDeepIsLeftBehind(t *testing.T) {
	for _, deep := range []int{8, 9} {
		t.Run(strconv.Itoa(deep), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			d, repos := cloneRun(t, src)
			nest(t, d.Work(), deep)
			printed := sessionOutput(t, d, repos, nil)
			if got := leftBehind(d, repos); got != (deep > 8) {
				t.Errorf("left behind = %v:\n%s", got, printed)
			}
		})
	}
}

// TestAFileInAnUnpopulatedNestedSubmoduleIsLeftBehind is a nested submodule that no update
// populated, an empty folder after the clone: a file the agent writes there, with a dot at the
// start of its name or not, keeps the copy, and the empty folder keeps nothing.
func TestAFileInAnUnpopulatedNestedSubmoduleIsLeftBehind(t *testing.T) {
	for _, file := range []string{"new.txt", ".new", ""} {
		t.Run(fmt.Sprintf("a file %q", file), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			d, repos := cloneRun(t, nestedSubmodule(t))
			inner := filepath.Join(d.Work(), "libs", "m", "inner")
			if entries, err := os.ReadDir(inner); err != nil || len(entries) > 0 {
				t.Fatalf("the clone left %d entries in libs/m/inner: %v", len(entries), err)
			}
			if file != "" {
				write(t, inner, file, "agent work\n")
			}
			printed := sessionOutput(t, d, repos, nil)
			if got := leftBehind(d, repos); got != (file != "") {
				t.Errorf("left behind = %v:\n%s", got, printed)
			}
		})
	}
}

// TestAQuotedGitlinkInASubmoduleIsLeftBehind is a gitlink with a double quote in its path, which
// git ls-files quotes, in a submodule, at an empty folder: the session cannot check it, and the
// copy stays.
func TestAQuotedGitlinkInASubmoduleIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	d, repos := cloneRun(t, src)
	s := filepath.Join(d.Work(), filepath.FromSlash(sub))
	gitlinks(t, s, testGit(t, s, "rev-parse", "HEAD"), `q"x`)
	if err := os.Mkdir(filepath.Join(s, `q"x`), 0o755); err != nil {
		t.Fatal(err)
	}
	printed := sessionOutput(t, d, repos, nil)
	if !leftBehind(d, repos) {
		t.Errorf("the session recorded no work left behind:\n%s", printed)
	}
}

// TestAFailedStatusInANestedSubmoduleIsLeftBehind is a nested submodule at its gitlink's commit,
// whose tree is missing from its objects: its status fails, and the copy stays.
func TestAFailedStatusInANestedSubmoduleIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	d, repos := cloneRun(t, nestedSubmodule(t))
	updateSubmodules(t, d.Work())
	m := filepath.Join(d.Work(), "libs", "m")
	inner := filepath.Join(m, "inner")
	write(t, inner, "lib.txt", "changed\n")
	head := commitAll(t, inner, "inner")
	tree := testGit(t, inner, "rev-parse", "HEAD^{tree}")
	objects := testGit(t, inner, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err := os.Remove(filepath.Join(objects, tree[:2], tree[2:])); err != nil {
		t.Fatal(err)
	}
	gitlinks(t, m, head, "inner")
	printed := sessionOutput(t, d, repos, nil)
	if !leftBehind(d, repos) {
		t.Errorf("the session recorded no work left behind:\n%s", printed)
	}
}

// TestNestedSubmodulesInASubmoduleComeBack is a submodule that the clone copied, with a chain of
// repositories in it, as levels submodules in all, and a new file at its top: the file comes back
// in the submodule's uncommitted, without the files of the chain. A change at the end of the chain
// keeps the copy, and so does a chain more than 8 submodules deep.
func TestNestedSubmodulesInASubmoduleComeBack(t *testing.T) {
	for _, c := range []struct {
		levels int
		change bool
	}{{3, false}, {4, false}, {8, false}, {3, true}, {4, true}, {9, false}} {
		t.Run(fmt.Sprintf("%d levels, a change %t", c.levels, c.change), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			d, repos := cloneRun(t, src)
			s := filepath.Join(d.Work(), filepath.FromSlash(sub))
			nest(t, s, c.levels-1)
			write(t, s, "new.txt", "new\n")
			if c.change {
				write(t, s, strings.Repeat("d/", c.levels-1)+"f.txt", "changed\n")
			}
			printed := sessionOutput(t, d, repos, nil)
			if got, want := leftBehind(d, repos), c.change || c.levels > 8; got != want {
				t.Errorf("left behind = %v:\n%s", got, printed)
			}
			qs, err := d.Quarantine(context.Background(), branch, repos)
			if err != nil {
				t.Fatalf("Quarantine: %v\n%s", err, printed)
			}
			q := qs[0].Submodules[0]
			ref := prefix + "/uncommitted"
			if !slices.Contains(q.Refs, ref) {
				t.Fatalf("the submodule's refs are %v:\n%s", q.Refs, printed)
			}
			got := files(t, q.Path, ref)
			if !slices.Contains(got, "new.txt") || !slices.Contains(got, "d") || slices.Contains(got, "d/f.txt") {
				t.Errorf("the submodule's uncommitted has %v", got)
			}
		})
	}
}

// TestARepositoryInARepositoryTheRunMadeInASubmoduleIsLeftBehind is an agent that commits a/f.txt
// in a submodule the clone copied, runs git init in a and in a/b, and writes a/b/new.txt: the
// session prints the line of a change in a nested repository, and the copy stays.
func TestARepositoryInARepositoryTheRunMadeInASubmoduleIsLeftBehind(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	d, repos := cloneRun(t, src)
	s := filepath.Join(d.Work(), filepath.FromSlash(sub))
	write(t, s, "a/f.txt", "f\n")
	commitAll(t, s, "a")
	testGit(t, filepath.Join(s, "a"), "init", "--quiet")
	write(t, s, "a/b/new.txt", "new\n")
	testGit(t, filepath.Join(s, "a", "b"), "init", "--quiet")
	printed := sessionOutput(t, d, repos, nil)
	if want := "qory: the submodule libs/s in the copy has a git repository with commits or changes that the commit leaves out"; !strings.Contains(printed, want) {
		t.Errorf("the session printed:\n%s", printed)
	}
	if !d.Left(repos).Behind {
		t.Errorf("the session recorded no change left behind:\n%s", printed)
	}
}

// TestARepositoryTheRunMadeInASubmoduleFolderIsLeftBehind is an agent that commits a/f.txt in a
// submodule the clone copied, runs git init in a, and writes a/new.txt, or changes a/f.txt under an
// ignore rule that covers a: the session prints a line for the repository, and the copy stays.
// Without the git init the file comes back, and nothing stays.
func TestARepositoryTheRunMadeInASubmoduleFolderIsLeftBehind(t *testing.T) {
	for _, c := range []struct {
		name          string
		init, ignored bool
	}{{"init", true, false}, {"ignored", true, true}, {"no-init", false, false}} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			d, repos := cloneRun(t, src)
			s := filepath.Join(d.Work(), filepath.FromSlash(sub))
			write(t, s, "a/f.txt", "f\n")
			commitAll(t, s, "a")
			if c.ignored {
				write(t, s, ".gitignore", "a/\n")
				commitAll(t, s, "ignore")
			}
			if c.init {
				testGit(t, filepath.Join(s, "a"), "init", "--quiet")
			}
			if c.ignored {
				write(t, s, "a/f.txt", "changed\n")
			} else {
				write(t, s, "a/new.txt", "new\n")
			}
			printed := sessionOutput(t, d, repos, nil)
			line := "qory: the submodule libs/s in the copy has a git repository at a that the run made, and the commit leaves it out"
			if strings.Contains(printed, line) != c.init {
				t.Errorf("the session printed:\n%s", printed)
			}
			if left := d.Left(repos); left.Nested != c.init || left.Behind {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestEveryFolderWithAGitInASubmoduleIsLeftBehind is an agent that makes a folder with a .git in a
// submodule the clone copied, in each of the ways below, and writes new.txt in it: the session
// exits 0, prints the line of a git repository, and the copy stays. An empty .git folder or a .git
// file that git cannot read makes no repository, so git lists new.txt in that folder, and the check
// under the others finds it. A folder named :x is no pathspec magic to the session.
func TestEveryFolderWithAGitInASubmoduleIsLeftBehind(t *testing.T) {
	type step func(t *testing.T, s string)
	initAt := func(rel string) step {
		return func(t *testing.T, s string) {
			if err := os.MkdirAll(filepath.Join(s, rel), 0o755); err != nil {
				t.Fatal(err)
			}
			testGit(t, filepath.Join(s, rel), "init", "--quiet")
		}
	}
	file := func(rel string) step {
		return func(t *testing.T, s string) { write(t, s, rel, "new\n") }
	}
	tracked := func(dir string) step {
		return func(t *testing.T, s string) {
			write(t, s, filepath.Join(dir, "f.txt"), "f\n")
			commitAll(t, s, "tracked")
		}
	}
	emptyGit := func(rel string) step {
		return func(t *testing.T, s string) {
			if err := os.MkdirAll(filepath.Join(s, rel, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	gitFile := func(rel, text string) step {
		return func(t *testing.T, s string) { write(t, s, filepath.Join(rel, ".git"), text) }
	}
	outside := func(rel string) step {
		return func(t *testing.T, s string) {
			repo := t.TempDir()
			testGit(t, repo, "init", "--quiet")
			write(t, s, filepath.Join(rel, ".git"), "gitdir: "+filepath.Join(repo, ".git")+"\n")
		}
	}
	nested := func(t *testing.T, s string) { nest(t, s, 1) }
	for _, c := range []struct {
		name  string
		steps []step
		made  string
	}{
		{"a chain in a tracked folder", []step{tracked("a"), initAt("a"), initAt("a/b"), file("a/b/new.txt")}, ""},
		{"a chain in a new folder", []step{initAt("u"), initAt("u/v"), file("u/v/new.txt")}, ""},
		{"a chain in a new folder in a tracked folder", []step{tracked("a"), initAt("a/u"), initAt("a/u/v"), file("a/u/v/new.txt")}, ""},
		{"a tracked folder two deep", []step{tracked("d1/d2"), initAt("d1/d2"), file("d1/d2/new.txt")}, "d1/d2"},
		{"a tracked folder with a space", []step{tracked("a b"), initAt("a b"), file("a b/new.txt")}, "a b"},
		{"a new folder with a space", []step{initAt("u v"), file("u v/new.txt")}, ""},
		{"a tracked folder with a line break", []step{tracked("a\nb"), initAt("a\nb"), file("a\nb/new.txt")}, ""},
		{"a new folder with a line break", []step{initAt("u\nv"), file("u\nv/new.txt")}, ""},
		{"a tracked folder named *", []step{tracked("*"), initAt("*"), file("*/new.txt")}, "*"},
		{"a new folder named a* beside ab", []step{tracked("ab"), initAt("a*"), file("a*/new.txt")}, ""},
		{"a tracked folder named -a", []step{tracked("-a"), initAt("-a"), file("-a/new.txt")}, "-a"},
		{"a tracked folder named :x", []step{tracked(":x"), initAt(":x"), file(":x/new.txt")}, ":x"},
		{"a tracked folder named :/x", []step{tracked(":/x"), initAt(":/x"), file(":/x/new.txt")}, ":/x"},
		{"an empty .git in a tracked folder", []step{tracked("a"), emptyGit("a"), file("a/new.txt")}, "a"},
		{"an empty .git in a new folder", []step{emptyGit("u"), file("u/new.txt")}, ""},
		{"a .git file to a missing folder", []step{gitFile("u", "gitdir: /nonexistent/x\n"), file("u/new.txt")}, ""},
		{"a .git file of junk", []step{gitFile("u", "junk\n"), file("u/new.txt")}, ""},
		{"a .git file to a repository outside", []step{outside("u"), file("u/new.txt")}, ""},
		{"a tracked folder in a nested submodule", []step{nested, func(t *testing.T, s string) {
			write(t, s, "d/x/f.txt", "f\n")
			commitAll(t, filepath.Join(s, "d"), "x")
		}, initAt("d/x"), file("d/x/new.txt")}, ""},
		{"a new folder in a nested submodule", []step{nested, initAt("d/u"), file("d/u/new.txt")}, ""},
		{"an empty .git in a nested submodule", []step{nested, emptyGit("d/u"), file("d/u/new.txt")}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			d, repos := cloneRun(t, src)
			s := filepath.Join(d.Work(), filepath.FromSlash(sub))
			for _, st := range c.steps {
				st(t, s)
			}
			printed := sessionOutput(t, d, repos, nil)
			left := d.Left(repos)
			if !left.Any() || !strings.Contains(printed, " has a git repository ") {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
			if c.made == "" {
				return
			}
			line := "qory: the submodule libs/s in the copy has a git repository at " + c.made + " that the run made, and the commit leaves it out"
			if !left.Nested || !strings.Contains(printed, line) {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestTheCheckUnderTheOthersInTheCopy is an agent that writes a file in an empty gitlink folder
// that git lists no path in, makes a repository with a commit at :x/y below the gitlink x, or adds
// the submodule :x at its commit with no change. The file and the repository keep the copy, and
// the submodule keeps nothing.
func TestTheCheckUnderTheOthersInTheCopy(t *testing.T) {
	for _, c := range []struct {
		name         string
		run          func(t *testing.T, w, head string)
		nested, left bool
		line         string
	}{
		{"a file in an empty gitlink folder", func(t *testing.T, w, head string) {
			gitlinks(t, w, head, "x")
			write(t, w, "x/new.txt", "new\n")
		}, false, true, ""},
		{"a repository below a gitlink", func(t *testing.T, w, head string) {
			gitlinks(t, w, head, "x")
			y := filepath.Join(w, ":x", "y")
			write(t, y, "new.txt", "new\n")
			testGit(t, y, "init", "--quiet")
			commitAll(t, y, "y")
		}, true, false, "qory: the copy has a git repository at :x/y that the run made, and the commit leaves it out"},
		{"a submodule named :x", func(t *testing.T, w, head string) {
			testGit(t, w, "clone", "--quiet", w, filepath.Join(w, ":x"))
			gitlinks(t, w, head, ":x")
		}, false, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, base := person(t)
			d, repos := cloneRun(t, src)
			c.run(t, d.Work(), base)
			printed := sessionOutput(t, d, repos, nil)
			if left := d.Left(repos); left.Nested != c.nested || left.Behind != c.left {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
			if c.line != "" && !strings.Contains(printed, c.line) {
				t.Errorf("the session printed:\n%s", printed)
			}
		})
	}
}

// TestTheCheckUnderTheOthersFindsNothingWhenAllComesBack is an agent that adds, changes and
// deletes files, writes a file that the person's .gitignore ignores, leaves a setup's file as the
// setup left it, commits in a submodule and writes a file there, and adds a gitlink at an empty
// folder: everything comes back, and nothing stays.
func TestTheCheckUnderTheOthersFindsNothingWhenAllComesBack(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	write(t, src, ".gitignore", "ignored/\n")
	commitAll(t, src, "ignore rule")
	d, repos := cloneRun(t, src)
	w := d.Work()
	write(t, w, "setup.txt", "setup\n")
	states, err := d.SetupStates(context.Background(), repos[0], []string{"setup.txt"})
	if err != nil {
		t.Fatal(err)
	}
	gitlinks(t, w, testGit(t, w, "rev-parse", "HEAD"), "empty")
	if err := os.Mkdir(filepath.Join(w, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, w, "new.txt", "new\n")
	write(t, w, "README.md", "changed\n")
	if err := os.Remove(filepath.Join(w, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	write(t, w, "ignored/x.txt", "x\n")
	s := filepath.Join(w, filepath.FromSlash(sub))
	write(t, s, "lib.txt", "changed\n")
	commitAll(t, s, "lib")
	write(t, s, "new.txt", "new\n")
	q, _ := runRefs(t, d, src, repos, map[string]LeftOut{"": {Setup: states}})
	got := files(t, q.Path, q.Head)
	if !slices.Contains(got, "new.txt") || slices.Contains(got, "CLAUDE.md") || slices.Contains(got, "setup.txt") || slices.Contains(got, "ignored/x.txt") {
		t.Errorf("the commit has %v", got)
	}
}

// TestARuleOfTheRunIgnoresNothing is a path that only an ignore rule the run wrote or changed
// covers: the commit leaves it out, and the copy stays, with the line of a repository for a
// repository the run made. A path that the person's .gitignore or .git/info/exclude ignores stays
// out with no line.
func TestARuleOfTheRunIgnoresNothing(t *testing.T) {
	for _, c := range []struct {
		name         string
		run          func(t *testing.T, w string)
		nested, left bool
	}{
		{"a .gitignore of the agent", func(t *testing.T, w string) {
			write(t, w, "foo/.gitignore", "*\n")
			write(t, w, "foo/work.go", "package foo\n")
		}, false, true},
		{"a rule the agent adds to the person's .gitignore", func(t *testing.T, w string) {
			write(t, w, ".gitignore", "build/\n*.log\nwork.go\n")
			write(t, w, "work.go", "package main\n")
		}, false, true},
		{"a rule the agent commits", func(t *testing.T, w string) {
			write(t, w, "foo/.gitignore", "*.go\n")
			commitAll(t, w, "rule")
			write(t, w, "foo/work.go", "package foo\n")
		}, false, true},
		{"a repository that a rule of the agent ignores", func(t *testing.T, w string) {
			write(t, w, "foo/.gitignore", "*\n")
			r := filepath.Join(w, "foo", "repo")
			write(t, r, "a.txt", "a\n")
			testGit(t, r, "init", "--quiet")
		}, true, true},
		{"a file next to a repository the run made", func(t *testing.T, w string) {
			write(t, w, ".gitignore", "build/\n*.log\nlib.go\n")
			write(t, w, "lib.go", "package main\n")
			r := filepath.Join(w, "lib")
			write(t, r, "a.txt", "a\n")
			testGit(t, r, "init", "--quiet")
		}, true, true},
		{"files the person's rules ignore", func(t *testing.T, w string) {
			write(t, w, "build/out.bin", "out\n")
			write(t, w, "sub/run.log", "log\n")
			write(t, w, "personal.txt", "mine\n")
			r := filepath.Join(w, "build", "repo")
			write(t, r, "a.txt", "a\n")
			testGit(t, r, "init", "--quiet")
		}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			write(t, src, ".gitignore", "build/\n*.log\n")
			commitAll(t, src, "rules")
			write(t, src, ".git/info/exclude", "personal.txt\n")
			d, repos := cloneRun(t, src)
			c.run(t, d.Work())
			printed := sessionOutput(t, d, repos, nil)
			if left := d.Left(repos); left.Nested != c.nested || left.Behind != c.left {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestAFileMarkedSkipWorktreeComesBack is an agent that changes README.md or adds a file, and
// marks it with git update-index --skip-worktree: the file is in the working tree, so it comes back
// in the commit on HEAD, as for a sparse checkout.
func TestAFileMarkedSkipWorktreeComesBack(t *testing.T) {
	for _, file := range []string{"README.md", "new.go"} {
		t.Run(file, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			d, repos := cloneRun(t, src)
			w := d.Work()
			write(t, w, file, "the agent's\n")
			testGit(t, w, "add", file)
			testGit(t, w, "update-index", "--skip-worktree", file)
			_, refs := runRefs(t, d, src, repos, nil)
			wantRefs(t, refs, "uncommitted")
			if got := testGit(t, src, "show", prefix+"/uncommitted:"+file); got != "the agent's" {
				t.Errorf("%s is %q", file, got)
			}
		})
	}
}

// TestALinkReplacedByAFileKeepsTheCopy is a link into .qory at CLAUDE.md that the agent replaces
// with a regular file or a folder: the file may have the run's text from .qory, so the commit keeps
// CLAUDE.md as the base commit has it, and the copy stays. A link that stays a link, or is gone,
// keeps nothing behind, and nor does a file with what the base commit has.
func TestALinkReplacedByAFileKeepsTheCopy(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(t *testing.T, w string)
		left bool
	}{
		{"a regular file", func(t *testing.T, w string) {
			write(t, w, "CLAUDE.md", "the agent's\n")
		}, true},
		{"a folder", func(t *testing.T, w string) {
			write(t, w, "CLAUDE.md/notes.md", "the agent's\n")
		}, true},
		{"a file the agent commits", func(t *testing.T, w string) {
			write(t, w, "CLAUDE.md", "the agent's\n")
			commitAll(t, w, "claude")
		}, true},
		{"the base commit's file", func(t *testing.T, w string) {
			write(t, w, "CLAUDE.md", "base\n")
		}, false},
		{"a link", func(t *testing.T, w string) {
			link(t, w, "CLAUDE.md", ".qory/CLAUDE.md")
			write(t, w, ".qory/CLAUDE.md", "changed through the link\n")
		}, false},
		{"nothing", func(t *testing.T, w string) {}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, base := person(t)
			d, repos := cloneRun(t, src)
			w := d.Work()
			write(t, w, ".qory/CLAUDE.md", "the run's text\n")
			if err := os.Remove(filepath.Join(w, "CLAUDE.md")); err != nil {
				t.Fatal(err)
			}
			c.run(t, w)
			write(t, w, "new.txt", "new\n")
			out := map[string]LeftOut{"": {Links: []string{"CLAUDE.md"}}}
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind != c.left || left.Nested {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
			q, err := d.Quarantine(context.Background(), branch, repos)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := q[0].FetchInto(context.Background(), src, branch, "run-1"); err != nil {
				t.Fatal(err)
			}
			if got, want := testGit(t, src, "show", q[0].Head+":CLAUDE.md"), testGit(t, src, "show", base+":CLAUDE.md"); got != want {
				t.Errorf("CLAUDE.md is %q at the run's commit, want %q", got, want)
			}
		})
	}
}

// TestAChangeInARepositoryOfTheSetupKeepsTheCopy is a setup that makes repositories at tool and
// a/b: the commit leaves both out, a file the agent writes in one keeps the copy, and a file at
// toolx or a/bc comes back.
func TestAChangeInARepositoryOfTheSetupKeepsTheCopy(t *testing.T) {
	for _, c := range []struct {
		name string
		file string
		left bool
	}{
		{"nothing", "", false},
		{"a file in tool", "tool/new.go", true},
		{"a changed file in tool", "tool/a.go", true},
		{"a file in a/b", "a/b/new.go", true},
		{"a file at toolx", "toolx/new.go", false},
		{"a file at a/bc", "a/bc", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			d, repos := cloneRun(t, src)
			w := d.Work()
			for _, r := range []string{"tool", "a/b"} {
				at := filepath.Join(w, filepath.FromSlash(r))
				write(t, at, "a.go", "package a\n")
				testGit(t, at, "init", "--quiet")
				commitAll(t, at, "setup")
			}
			out := setupRecord(t, d, repos[0])
			if len(out.Setup) != 2 || out.Setup[0].Path != "a/b" || out.Setup[1].Path != "tool" || !strings.HasPrefix(out.Setup[0].State, "repo:") || !strings.HasPrefix(out.Setup[1].State, "repo:") {
				t.Fatalf("the setup's paths are %v, want a/b and tool as repositories", out.Setup)
			}
			if c.file == "" {
				noBundleWith(t, d, repos, map[string]LeftOut{"": out})
				return
			}
			write(t, w, c.file, "the agent's\n")
			printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
			if left := d.Left(repos); left.Behind != c.left || left.Nested {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
			if c.left {
				return
			}
			q, err := d.Quarantine(context.Background(), branch, repos)
			if err != nil {
				t.Fatal(err)
			}
			if got := files(t, q[0].Path, q[0].Head); !slices.Contains(got, c.file) {
				t.Errorf("the commit has %v", got)
			}
		})
	}
}

// setupRepository makes a repository with a commit at the folder rel of the copy at w, as a setup
// does.
func setupRepository(t *testing.T, w, rel string) string {
	t.Helper()
	at := filepath.Join(w, filepath.FromSlash(rel))
	write(t, at, "a.go", "package a\n")
	testGit(t, at, "init", "--quiet")
	commitAll(t, at, "setup")
	return at
}

// TestACommitInARepositoryOfTheSetupKeepsTheCopy is an agent that commits, tags, commits and resets
// back, or writes ignore rules in a repository that the setup made: each keeps the copy. A repository whose state the
// setup could not record, here one with a .git file, keeps the copy with no change.
func TestACommitInARepositoryOfTheSetupKeepsTheCopy(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(t *testing.T, tool string)
	}{
		{"a commit", func(t *testing.T, tool string) {
			write(t, tool, "new.go", "package a\n")
			commitAll(t, tool, "agent")
		}},
		{"a tag", func(t *testing.T, tool string) {
			testGit(t, tool, "tag", "v1")
		}},
		{"a commit and a reset", func(t *testing.T, tool string) {
			write(t, tool, "new.go", "package a\n")
			commitAll(t, tool, "agent")
			testGit(t, tool, "reset", "--quiet", "--hard", "HEAD~1")
		}},
		{"a branch", func(t *testing.T, tool string) {
			testGit(t, tool, "branch", "side")
		}},
		{"an ignore rule", func(t *testing.T, tool string) {
			write(t, tool, "x/.gitignore", "*\n")
			write(t, tool, "x/new.go", "package x\n")
		}},
		{"a state the setup did not record", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			d, repos := cloneRun(t, src)
			tool := filepath.Join(d.Work(), "tool")
			if c.run == nil {
				write(t, tool, "a.go", "package a\n")
				testGit(t, tool, "init", "--quiet", "--separate-git-dir", filepath.Join(t.TempDir(), "git"))
				commitAll(t, tool, "setup")
			} else {
				setupRepository(t, d.Work(), "tool")
			}
			out := setupRecord(t, d, repos[0])
			if want := c.run == nil; len(out.Setup) != 1 || (out.Setup[0].State == "other") != want {
				t.Fatalf("the setup's paths are %v", out.Setup)
			}
			if c.run != nil {
				c.run(t, tool)
			}
			printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
			if left := d.Left(repos); !left.Behind || left.Nested {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// setupWith clones a run of a new person's repository, runs mk on its copy as a setup, and returns
// the record of that setup.
func setupWith(t *testing.T, mk func(t *testing.T, w string)) (Dir, []Repository, map[string]LeftOut) {
	t.Helper()
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	mk(t, d.Work())
	return d, repos, map[string]LeftOut{"": setupRecord(t, d, repos[0])}
}

// toolWithSubmodule makes a repository at tool with a submodule at lib, as a setup that runs git
// clone --recursive does.
func toolWithSubmodule(t *testing.T, w string) {
	up := t.TempDir()
	write(t, up, "l.go", "package l\n")
	testGit(t, up, "init", "--quiet")
	commitAll(t, up, "lib")
	tool := setupRepository(t, w, "tool")
	testGit(t, tool, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", up, "lib")
	commitAll(t, tool, "submodule")
}

// TestASubmoduleOfARepositoryOfTheSetup is a repository that the setup made with a submodule: with
// no change, the session keeps no copy. A commit the agent leaves on a branch of the submodule, also
// after git submodule deinit, a change or a stash there, or a repository the agent makes in the
// repository keeps the copy.
func TestASubmoduleOfARepositoryOfTheSetup(t *testing.T) {
	for _, c := range []struct {
		name string
		run  func(t *testing.T, tool string)
	}{
		{"no change", nil},
		{"a branch", func(t *testing.T, tool string) {
			lib := filepath.Join(tool, "lib")
			testGit(t, lib, "checkout", "--quiet", "-b", "side")
			write(t, lib, "new.go", "package l\n")
			commitAll(t, lib, "agent")
			testGit(t, lib, "checkout", "--quiet", "-")
		}},
		{"a branch and a deinit", func(t *testing.T, tool string) {
			lib := filepath.Join(tool, "lib")
			testGit(t, lib, "checkout", "--quiet", "-b", "side")
			write(t, lib, "new.go", "package l\n")
			commitAll(t, lib, "agent")
			testGit(t, lib, "checkout", "--quiet", "-")
			testGit(t, tool, "submodule", "deinit", "--quiet", "-f", "lib")
		}},
		{"a change", func(t *testing.T, tool string) {
			write(t, tool, "lib/l.go", "package l // agent\n")
		}},
		{"a stash", func(t *testing.T, tool string) {
			lib := filepath.Join(tool, "lib")
			write(t, lib, "l.go", "package l // agent\n")
			testGit(t, lib, "stash", "--quiet")
		}},
		{"a repository", func(t *testing.T, tool string) {
			setupRepository(t, tool, "x")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, repos, out := setupWith(t, toolWithSubmodule)
			if c.run != nil {
				c.run(t, filepath.Join(d.Work(), "tool"))
			}
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); (left.Behind || left.Nested) != (c.run != nil) {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// toolWithClone makes a repository at tool, as a setup does, with a further clone at third/b inside
// it, in a folder that tool's .gitignore ignores, with ignored set, or that no rule covers.
func toolWithClone(ignored bool) func(t *testing.T, w string) {
	return func(t *testing.T, w string) {
		up := t.TempDir()
		write(t, up, "b.go", "package b\n")
		testGit(t, up, "init", "--quiet")
		commitAll(t, up, "b")
		tool := setupRepository(t, w, "tool")
		if ignored {
			write(t, tool, ".gitignore", "third/\n")
			commitAll(t, tool, "ignore")
		}
		testGit(t, tool, "clone", "--quiet", up, "third/b")
	}
}

// TestACloneInARepositoryOfTheSetup is a repository that the setup made, with a clone that the
// setup made inside it, in a folder that no rule covers or that its .gitignore ignores: the setup's
// paths have both repositories, and with no change the session keeps no copy. A change or a new
// file the agent writes in the clone, or a repository the agent makes in that folder, also one whose
// git folder is no .git, keeps the copy.
func TestACloneInARepositoryOfTheSetup(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		for _, c := range []struct {
			name string
			run  func(t *testing.T, tool string)
		}{
			{"no change", nil},
			{"a change", func(t *testing.T, tool string) {
				write(t, tool, "third/b/b.go", "package b // agent\n")
			}},
			{"a new file", func(t *testing.T, tool string) {
				write(t, tool, "third/b/new.go", "package b\n")
			}},
			{"a repository", func(t *testing.T, tool string) {
				setupRepository(t, tool, "third/c")
			}},
			{"a repository with its git folder beside it", func(t *testing.T, tool string) {
				up := t.TempDir()
				write(t, up, "c.go", "package c\n")
				testGit(t, up, "init", "--quiet")
				commitAll(t, up, "c")
				testGit(t, tool, "clone", "--quiet", "--separate-git-dir", filepath.Join(tool, "third", "c-git"), up, "third/c")
			}},
		} {
			t.Run(fmt.Sprintf("%s, ignored %t", c.name, ignored), func(t *testing.T) {
				d, repos, out := setupWith(t, toolWithClone(ignored))
				setup := out[""].Setup
				if len(setup) != 2 || setup[0].Path != "tool" || setup[1].Path != "tool/third/b" ||
					!strings.HasPrefix(setup[0].State, "repo:") || !strings.HasPrefix(setup[1].State, "repo:") {
					t.Fatalf("the setup's paths are %v", setup)
				}
				if c.run != nil {
					c.run(t, filepath.Join(d.Work(), "tool"))
				}
				printed := sessionOutput(t, d, repos, out)
				if left := d.Left(repos); (left.Behind || left.Nested) != (c.run != nil) {
					t.Errorf("Left = %+v:\n%s", left, printed)
				}
			})
		}
	}
}

// TestASubmoduleWithNoCommitInARepositoryOfTheSetup is a repository that the setup made, with a
// submodule that it added and did not commit, whose git folder is below the repository's
// .git/modules: with no change, the session keeps no copy.
func TestASubmoduleWithNoCommitInARepositoryOfTheSetup(t *testing.T) {
	d, repos, out := setupWith(t, func(t *testing.T, w string) {
		up := t.TempDir()
		write(t, up, "l.go", "package l\n")
		testGit(t, up, "init", "--quiet")
		commitAll(t, up, "lib")
		tool := setupRepository(t, w, "tool")
		testGit(t, tool, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", up, "lib")
	})
	if setup := out[""].Setup; len(setup) != 1 || !strings.HasPrefix(setup[0].State, "repo:") {
		t.Fatalf("the setup's paths are %v", setup)
	}
	printed := sessionOutput(t, d, repos, out)
	if left := d.Left(repos); left.Behind || left.Nested {
		t.Errorf("Left = %+v:\n%s", left, printed)
	}
}

// TestASubmoduleWithASeparateGitFolderInARepositoryOfTheSetup is a repository that the setup made,
// with a gitlink in its HEAD at a clone whose git folder is elsewhere in the repository's .git: with
// no change, the session keeps no copy, and a change there keeps it.
func TestASubmoduleWithASeparateGitFolderInARepositoryOfTheSetup(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprintf("change %t", change), func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				up := t.TempDir()
				write(t, up, "l.go", "package l\n")
				testGit(t, up, "init", "--quiet")
				commitAll(t, up, "lib")
				tool := setupRepository(t, w, "tool")
				testGit(t, tool, "clone", "--quiet", "--separate-git-dir", filepath.Join(tool, ".git", "lib.git"), up, "lib")
				gitlinks(t, tool, strings.TrimSpace(testGit(t, up, "rev-parse", "HEAD")), "lib")
			})
			if setup := out[""].Setup; len(setup) != 1 || !strings.HasPrefix(setup[0].State, "repo:") {
				t.Fatalf("the setup's paths are %v", setup)
			}
			if change {
				write(t, d.Work(), "tool/lib/l.go", "package l // agent\n")
			}
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); (left.Behind || left.Nested) != change {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestALinkAtTheModulesOfARepositoryOfTheSetup is an agent that replaces .git/modules of a
// repository that the setup made, with no submodules, with a link to third, a folder that the
// repository ignores. A repository the agent makes at third/c, with its git folder at third/gd,
// is no submodule: the session writes its nested line. The link alone keeps the copy too.
func TestALinkAtTheModulesOfARepositoryOfTheSetup(t *testing.T) {
	for _, repo := range []bool{true, false} {
		t.Run(fmt.Sprintf("repository %t", repo), func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				tool := setupRepository(t, w, "tool")
				write(t, tool, ".gitignore", "third/\n")
				commitAll(t, tool, "ignore")
			})
			tool := filepath.Join(d.Work(), "tool")
			if err := os.MkdirAll(filepath.Join(tool, "third"), 0o755); err != nil {
				t.Fatal(err)
			}
			if repo {
				c := filepath.Join(tool, "third", "c")
				if err := os.Mkdir(c, 0o755); err != nil {
					t.Fatal(err)
				}
				testGit(t, c, "init", "--quiet", "--separate-git-dir", filepath.Join(tool, "third", "gd"))
				write(t, c, "agent.go", "package agent\n")
				commitAll(t, c, "agent")
			}
			if err := os.Symlink("../third", filepath.Join(tool, ".git", "modules")); err != nil {
				t.Fatal(err)
			}
			printed := sessionOutput(t, d, repos, out)
			left := d.Left(repos)
			if !left.Behind || left.Nested != repo {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestAFileThatARepositoryOfTheSetupIgnores is a file the agent writes in a repository that the
// setup made, at a path that the repository's .gitignore covers: the copy stays, and n.ignored lists
// the file. In a folder named build, which builds write, the file stays out with no line.
func TestAFileThatARepositoryOfTheSetupIgnores(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"build/x.go", nil},
		{"third/x.go", []string{"tool/third/x.go"}},
	} {
		t.Run(c.file, func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				tool := setupRepository(t, w, "tool")
				write(t, tool, ".gitignore", "build/\nthird/\n")
				commitAll(t, tool, "ignore")
				write(t, tool, "third/old.go", "package x\n")
			})
			write(t, filepath.Join(d.Work(), "tool"), c.file, "package x\n")
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind != (c.want != nil) || left.Nested || !slices.Equal(left.Ignored, c.want) {
				t.Errorf("Left = %+v, want %v:\n%s", left, c.want, printed)
			}
			if printed != "" {
				t.Errorf("the session printed %q", printed)
			}
		})
	}
}

// TestAChangeUnderARuleOfTheSetupKeepsTheCopy is a setup that writes a folder with a .gitignore of
// *, or a .gitignore with a rule for a folder, and files there: a file that the agent adds or
// changes there keeps the copy, also with its time set back as touch -d sets it, and n.ignored lists
// it. A file the agent deletes there is no work, and keeps no copy.
func TestAChangeUnderARuleOfTheSetupKeepsTheCopy(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		agent func(t *testing.T, w string)
		want  []string
	}{
		{"a new file", func(t *testing.T, w string) {
			write(t, w, "cache/new.txt", "agent\n")
		}, []string{"cache/new.txt"}},
		{"a changed file", func(t *testing.T, w string) {
			write(t, w, "cache/a.txt", "agent\n")
		}, []string{"cache/a.txt"}},
		{"a changed file with its old time", func(t *testing.T, w string) {
			write(t, w, "cache/a.txt", "agent\n")
			if err := os.Chtimes(filepath.Join(w, "cache", "a.txt"), old, old); err != nil {
				t.Fatal(err)
			}
		}, []string{"cache/a.txt"}},
		{"a new file under a rule for a folder", func(t *testing.T, w string) {
			write(t, w, "gen/deep/new.txt", "agent\n")
		}, []string{"gen/deep/new.txt"}},
		{"a deleted file", func(t *testing.T, w string) {
			if err := os.Remove(filepath.Join(w, "cache", "a.txt")); err != nil {
				t.Fatal(err)
			}
		}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				write(t, w, "cache/.gitignore", "*\n")
				write(t, w, "cache/a.txt", "setup\n")
				write(t, w, ".gitignore", "gen/\n")
				write(t, w, "gen/out.txt", "setup\n")
			})
			if out[""].SetupEnd <= 0 {
				t.Fatalf("the setup's end is %d", out[""].SetupEnd)
			}
			c.agent(t, d.Work())
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind != (c.want != nil) || left.Nested || !slices.Equal(left.Ignored, c.want) {
				t.Errorf("Left = %+v, want %v:\n%s", left, c.want, printed)
			}
		})
	}
}

// TestACommitInARepositoryUnderARuleOfTheSetupKeepsTheCopy is a bare repository, or one with a work
// tree, that the setup made in a folder that its rules ignore: a commit that the agent makes there
// keeps the copy, and n.ignored lists the files it wrote, such as the ref or the committed file.
func TestACommitInARepositoryUnderARuleOfTheSetupKeepsTheCopy(t *testing.T) {
	for _, bare := range []bool{true, false} {
		t.Run(fmt.Sprintf("bare %t", bare), func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				write(t, w, "cache/.gitignore", "*\n")
				if bare {
					testGit(t, w, "init", "--quiet", "--bare", "cache/b.git")
				} else {
					setupRepository(t, w, "cache/b")
				}
			})
			if bare {
				// What git push writes there: an object and the ref.
				git := filepath.Join(d.Work(), "cache", "b.git")
				blob := gitIn(t, git, "agent\n", "hash-object", "-w", "--stdin")
				tree := gitIn(t, git, "100644 blob "+blob+"\tx.txt\n", "mktree")
				commit := testGit(t, git, "commit-tree", "-m", "agent", tree)
				testGit(t, git, "update-ref", "refs/heads/main", commit)
			} else {
				b := filepath.Join(d.Work(), "cache", "b")
				write(t, b, "new.go", "package a\n")
				commitAll(t, b, "agent")
			}
			printed := sessionOutput(t, d, repos, out)
			left := d.Left(repos)
			want := "cache/b/new.go"
			if bare {
				want = "cache/b.git/refs/heads/main"
			}
			if !left.Behind || !slices.Contains(left.Ignored, want) {
				t.Errorf("Left = %+v, want %s in Ignored:\n%s", left, want, printed)
			}
		})
	}
}

// TestASetupThatWritesItsCachesKeepsNoCopy is a setup that installs into .venv and node_modules and
// writes a cache under its rules, and an agent that changes a file of the person's and writes in
// node_modules and .venv: no copy stays, and the file comes back. A record whose end is unknown keeps
// the copy for the cache, with no list.
func TestASetupThatWritesItsCachesKeepsNoCopy(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(fmt.Sprintf("end known %t", known), func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				write(t, w, ".venv/.gitignore", "*\n")
				write(t, w, ".venv/bin/python", "x\n")
				write(t, w, ".gitignore", "node_modules/\n.cache/\n")
				write(t, w, "node_modules/p/index.js", "setup\n")
				write(t, w, ".cache/pip/x", "setup\n")
			})
			if !known {
				o := out[""]
				o.SetupEnd = -1
				out[""] = o
			}
			write(t, d.Work(), "README.md", "agent\n")
			write(t, d.Work(), "node_modules/p/index.js", "agent\n")
			write(t, d.Work(), "node_modules/q/new.js", "agent\n")
			write(t, d.Work(), ".venv/lib/new.py", "agent\n")
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind == known || left.Nested || len(left.Ignored) > 0 {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestAChangeUnderARuleOfTheSetupInASubmodule is a setup that writes a .gitignore in a submodule
// that the clone copied, and a file under it: a file that the agent writes there keeps the copy, and
// n.ignored lists it.
func TestAChangeUnderARuleOfTheSetupInASubmodule(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	d, repos := cloneRun(t, src)
	s := filepath.Join(d.Work(), filepath.FromSlash(sub))
	write(t, s, ".gitignore", "gen/\n")
	write(t, s, "gen/out.txt", "setup\n")
	out := map[string]LeftOut{"": setupRecord(t, d, repos[0])}
	write(t, s, "gen/new.txt", "agent\n")
	printed := sessionOutput(t, d, repos, out)
	if left := d.Left(repos); !left.Behind || !slices.Equal(left.Ignored, []string{sub + "/gen/new.txt"}) {
		t.Errorf("Left = %+v:\n%s", left, printed)
	}
}

// setupWithIgnore is [setupWith] on a person's commit with a .gitignore of rules.
func setupWithIgnore(t *testing.T, rules string, mk func(t *testing.T, w string)) (Dir, []Repository, map[string]LeftOut) {
	t.Helper()
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	write(t, src, ".gitignore", rules)
	commitAll(t, src, "ignore")
	d, repos := cloneRun(t, src)
	mk(t, d.Work())
	return d, repos, map[string]LeftOut{"": setupRecord(t, d, repos[0])}
}

// TestAClosedFolderUnderARuleOfTheSetupKeepsTheCopy is a setup that ignores a folder with a rule
// for folders only, and an agent that writes there and takes the read or search right off the
// folder: the copy stays, and n.ignored lists the folder.
func TestAClosedFolderUnderARuleOfTheSetupKeepsTheCopy(t *testing.T) {
	for _, mode := range []os.FileMode{0o000, 0o300, 0o100} {
		t.Run(mode.String(), func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				write(t, w, ".gitignore", "cache/\n")
				write(t, w, "cache/a.txt", "setup\n")
			})
			cache := filepath.Join(d.Work(), "cache")
			write(t, cache, "new.txt", "agent\n")
			if err := os.Chmod(cache, mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(cache, 0o755) })
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); !left.Behind || !slices.Equal(left.Ignored, []string{"cache/"}) {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestANameWithALineBreakKeepsTheCopy is a file under the setup's rules whose name has a line
// break, and whose first line is a file that the person's rules ignore: the copy stays. With no
// line break, n.ignored lists the file.
func TestANameWithALineBreakKeepsTheCopy(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"x.tmp\nb.log", nil},
		{"b.log", []string{"b.log"}},
	} {
		t.Run(strconv.Quote(c.file), func(t *testing.T) {
			d, repos, out := setupWithIgnore(t, "*.tmp\n", func(t *testing.T, w string) {
				write(t, w, ".gitignore", "*.tmp\n*.log\n")
				write(t, w, "x.log", "setup\n")
			})
			write(t, d.Work(), "x.tmp", "scratch\n")
			write(t, d.Work(), c.file, "agent\n")
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); !left.Behind || !slices.Equal(left.Ignored, c.want) {
				t.Errorf("Left = %+v, want %v:\n%s", left, c.want, printed)
			}
		})
	}
}

// TestGitStatusInARepositoryOfTheSetupKeepsNoCopy is a repository that the setup cloned in a
// folder that its rules ignore, and an agent that runs git status there, which can write the
// index, or changes only a file of the person's: no copy stays.
func TestGitStatusInARepositoryOfTheSetupKeepsNoCopy(t *testing.T) {
	for _, c := range []struct {
		name  string
		agent func(t *testing.T, b string)
	}{
		{"no git", func(*testing.T, string) {}},
		{"git status", func(t *testing.T, b string) {
			testGit(t, b, "status", "--porcelain")
		}},
		{"a refreshed index", func(t *testing.T, b string) {
			testGit(t, b, "update-index", "--really-refresh")
			testGit(t, b, "status", "--porcelain")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				write(t, w, ".gitignore", "cache/\n")
				write(t, w, "cache/a.txt", "setup\n")
				setupRepository(t, w, "cache/b")
			})
			c.agent(t, filepath.Join(d.Work(), "cache", "b"))
			write(t, d.Work(), "README.md", "agent\n")
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind || left.Nested {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestADeepGitignoreOfTheSetupKeepsTheCopy is a setup that writes a .gitignore of * in a folder
// deeper than the walk after the setup goes, at the top or in a submodule, and an agent that
// writes a file there: the record runs, and the copy stays. At the top, the folder too deep keeps
// the copy whole; in the submodule, no record lists the .gitignore, so its rule counts as the
// agent's, and the line lists no file.
func TestADeepGitignoreOfTheSetupKeepsTheCopy(t *testing.T) {
	deep := strings.Repeat("d/", maxOpenFolders+6) + "x"
	for _, inSub := range []bool{false, true} {
		t.Run(fmt.Sprintf("submodule %t", inSub), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			d, repos := cloneRun(t, src)
			at := d.Work()
			if inSub {
				at = filepath.Join(d.Work(), filepath.FromSlash(sub))
			}
			write(t, at, deep+"/.gitignore", "*\n")
			write(t, at, deep+"/a.txt", "setup\n")
			out := map[string]LeftOut{"": setupRecord(t, d, repos[0])}
			if out[""].SetupEnd <= 0 {
				t.Errorf("the setup's end is %d", out[""].SetupEnd)
			}
			write(t, at, deep+"/new.txt", "agent\n")
			printed := sessionOutput(t, d, repos, out)
			left := d.Left(repos)
			if !left.Behind || len(left.Ignored) > 0 {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestAFileNamedGitIndexUnderARuleOfTheSetupKeepsTheCopy is a file that the agent writes at
// .git/index in a folder that the setup's rules ignore, with no repository there, or in a
// repository there with a change staged: the copy stays, and n.ignored lists the file.
func TestAFileNamedGitIndexUnderARuleOfTheSetupKeepsTheCopy(t *testing.T) {
	for _, c := range []struct {
		name  string
		agent func(t *testing.T, w string) string
	}{
		{"no repository", func(t *testing.T, w string) string {
			write(t, w, "cache/notes/.git/index", "agent\n")
			return "cache/notes/.git/index"
		}},
		{"a staged change", func(t *testing.T, w string) string {
			b := filepath.Join(w, "cache", "b")
			write(t, b, "staged.go", "package a\n")
			testGit(t, b, "add", "staged.go")
			return "cache/b/.git/index"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				write(t, w, ".gitignore", "cache/\n")
				write(t, w, "cache/a.txt", "setup\n")
				setupRepository(t, w, "cache/b")
			})
			want := c.agent(t, d.Work())
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); !left.Behind || !slices.Contains(left.Ignored, want) {
				t.Errorf("Left = %+v, want %s in Ignored:\n%s", left, want, printed)
			}
		})
	}
}

// TestALineBreakUnderARuleOfTheAgentKeepsTheCopy is a run with no rules of the setup, an agent
// that adds a rule to the .gitignore, and a file under it whose name has a line break, and each of
// whose lines the person's rules cover: the copy stays.
func TestALineBreakUnderARuleOfTheAgentKeepsTheCopy(t *testing.T) {
	d, repos, out := setupWithIgnore(t, "*.tmp\nz\n", func(t *testing.T, w string) {
		write(t, w, "setup.txt", "setup\n")
	})
	if out[""].SetupEnd != 0 {
		t.Fatalf("the setup's end is %d", out[""].SetupEnd)
	}
	write(t, d.Work(), ".gitignore", "*.tmp\nz\n*z\n")
	write(t, d.Work(), "a.tmp", "scratch\n")
	write(t, d.Work(), "a.tmp\nz", "agent\n")
	printed := sessionOutput(t, d, repos, out)
	if left := d.Left(repos); !left.Behind {
		t.Errorf("Left = %+v:\n%s", left, printed)
	}
}

// TestMoreLineBreaksAndClosedFoldersKeepTheCopy are names with a line break under the setup's
// rules, one whose second line reads as a time and a path, and a folder; and a folder that the
// agent closes under a rule that it wrote, with no rules of the setup: each keeps the copy.
func TestMoreLineBreaksAndClosedFoldersKeepTheCopy(t *testing.T) {
	for _, c := range []struct {
		name   string
		person string
		setup  func(t *testing.T, w string)
		agent  func(t *testing.T, w string)
	}{
		{"a time in a name", "*.tmp\n", func(t *testing.T, w string) {
			write(t, w, ".gitignore", "*.tmp\n*.log\n")
			write(t, w, "x.log", "setup\n")
		}, func(t *testing.T, w string) {
			write(t, w, "x.tmp", "scratch\n")
			write(t, w, "y.tmp", "scratch\n")
			write(t, w, "x.tmp\n1999999999 ./y.tmp\nb.log", "agent\n")
		}},
		{"a folder with a line break", "", func(t *testing.T, w string) {
			write(t, w, ".gitignore", "cache/\n")
			write(t, w, "cache/a.txt", "setup\n")
		}, func(t *testing.T, w string) {
			write(t, w, "cache/a\nb/work.txt", "agent\n")
		}},
		{"a closed folder under a rule of the agent", "", func(t *testing.T, w string) {
			write(t, w, "setup.txt", "setup\n")
		}, func(t *testing.T, w string) {
			write(t, w, "cache/.gitignore", "*\n")
			write(t, w, "cache/new.txt", "agent\n")
			cache := filepath.Join(w, "cache")
			if err := os.Chmod(cache, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(cache, 0o755) })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, repos, out := setupWithIgnore(t, c.person, c.setup)
			c.agent(t, d.Work())
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); !left.Behind {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestALineBreakThatSplitsIntoKeptPathsKeepsTheCopy is a file that git cannot add, at a path with a
// line break whose lines are each a path that the setup left and the commit keeps out: the copy
// stays. A part GIT~1 makes a path that git refuses. A line-end file with a line break in its name,
// which the commit keeps out whole, keeps no copy: see TestALineEndRewriteStaysOut.
func TestALineBreakThatSplitsIntoKeptPathsKeepsTheCopy(t *testing.T) {
	d, repos, out := setupWith(t, func(t *testing.T, w string) {
		write(t, w, "GIT~1/a", "setup\n")
		write(t, w, "cfg", "setup\n")
	})
	write(t, d.Work(), "GIT~1/a\ncfg", "agent\n")
	printed := sessionOutput(t, d, repos, out)
	if left := d.Left(repos); !left.Behind {
		t.Errorf("Left = %+v:\n%s", left, printed)
	}
}

// TestAClosedFolderWithALineBreakKeepsTheCopy is a folder with work in it that the session cannot
// open, as one that another user owns, whose name has a line break and whose lines your rules each
// ignore: the copy stays. A chmod that fails stands in for the other owner.
func TestAClosedFolderWithALineBreakKeepsTheCopy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a folder with no read bit")
	}
	d, repos, out := setupWithIgnore(t, "*.log\n.env\n", func(t *testing.T, w string) {
		write(t, w, "setup.txt", "setup\n")
	})
	closed := filepath.Join(d.Work(), "x.log\n.env")
	write(t, closed, "work.txt", "agent\n")
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "chmod"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := sessionPath
	sessionPath = stub + ":" + old
	t.Cleanup(func() { sessionPath = old })
	printed := sessionOutput(t, d, repos, out)
	if left := d.Left(repos); !left.Behind {
		t.Errorf("Left = %+v:\n%s", left, printed)
	}
}

// TestASetupWithNoRulesRecordsNoEnd is a setup that writes no .gitignore and makes no
// repository: no record runs, and the setup's end is 0, so the git session lists no change.
func TestASetupWithNoRulesRecordsNoEnd(t *testing.T) {
	d, repos, out := setupWith(t, func(t *testing.T, w string) {
		write(t, w, "made/x.txt", "setup\n")
	})
	if out[""].SetupEnd != 0 {
		t.Errorf("the setup's end is %d", out[""].SetupEnd)
	}
	write(t, d.Work(), "made/x.txt", "agent\n")
	printed := sessionOutput(t, d, repos, out)
	if left := d.Left(repos); left.Behind || left.Nested {
		t.Errorf("Left = %+v:\n%s", left, printed)
	}
}

// TestACloneOfTheSetupWithNoChange is a repository that the setup cloned, with names of each kind
// in its .gitignore files and in its reflogs, and a link in its refs: with no change, the session
// keeps no copy. A tab or a byte below it at the end of a name sorts the same in the setup's record
// and in the session.
func TestACloneOfTheSetupWithNoChange(t *testing.T) {
	for _, c := range []struct {
		name string
		up   func(t *testing.T, up string)
		dep  func(t *testing.T, dep string)
	}{
		{"plain", nil, nil},
		{"names", func(t *testing.T, up string) {
			write(t, up, "x y/\tt\\b/.gitignore", "*.o\n")
			write(t, up, "-a/.gitignore", "*.o\n")
			write(t, up, "B/.gitignore", "*.o\n")
			write(t, up, "a/.gitignore", "*.o\n")
			write(t, up, "a.b/.gitignore", "*.o\n")
			write(t, up, "a-b/.gitignore", "*.o\n")
			write(t, up, "dir/.gitignore/in/.gitignore", "x\n")
			link(t, up, "sub/.gitignore", "../a/.gitignore")
		}, nil},
		{"a link in refs", nil, func(t *testing.T, dep string) {
			link(t, dep, ".git/refs/tags/l", "../heads/main")
		}},
		{"a control byte in logs", nil, func(t *testing.T, dep string) {
			write(t, dep, ".git/logs/refs/a\x01", "x\n")
			write(t, dep, ".git/logs/refs/a", "x\n")
		}},
		{"a tab in logs", nil, func(t *testing.T, dep string) {
			write(t, dep, ".git/logs/refs/a\t", "x\n")
			write(t, dep, ".git/logs/refs/a", "x\n")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				up := t.TempDir()
				write(t, up, "a.go", "package a\n")
				if c.up != nil {
					c.up(t, up)
				}
				testGit(t, up, "init", "--quiet")
				commitAll(t, up, "one")
				testGit(t, up, "tag", "v1")
				testGit(t, up, "tag", "-a", "-m", "t", "v2")
				testGit(t, up, "branch", "other")
				testGit(t, w, "clone", "--quiet", up, "dep")
				if c.dep != nil {
					c.dep(t, filepath.Join(w, "dep"))
				}
			})
			if len(out[""].Setup) != 1 || !strings.HasPrefix(out[""].Setup[0].State, "repo:") {
				t.Fatalf("the setup's paths are %v", out[""].Setup)
			}
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind || left.Nested {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestAFolderOfTheSetupThatIgnoresItself is a setup that writes folders with a .gitignore that
// ignores the folder, as python -m venv and pytest do: with no change, the session keeps no copy.
func TestAFolderOfTheSetupThatIgnoresItself(t *testing.T) {
	d, repos, out := setupWith(t, func(t *testing.T, w string) {
		write(t, w, ".venv/.gitignore", "# created by venv\n*\n")
		write(t, w, ".venv/bin/python", "x\n")
		write(t, w, ".pytest_cache/.gitignore", "# Created by pytest automatically.\n*\n")
		write(t, w, ".pytest_cache/v/cache/nodeids", "[]\n")
	})
	printed := sessionOutput(t, d, repos, out)
	if left := d.Left(repos); left.Behind || left.Nested {
		t.Errorf("Left = %+v:\n%s", left, printed)
	}
}

// TestAFilterInARepositoryOfTheSetup is a repository that the setup made with a clean and a smudge
// filter, which the session's git does not run, so git status there lists the filtered file: with
// no change, the session keeps no copy, and a change of that file keeps it.
func TestAFilterInARepositoryOfTheSetup(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprint("change ", change), func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				tool := filepath.Join(w, "tool")
				write(t, tool, ".gitattributes", "*.dat filter=up\n")
				write(t, tool, "a.dat", "abc\n")
				testGit(t, tool, "init", "--quiet")
				testGit(t, tool, "config", "filter.up.clean", "tr a-z A-Z")
				testGit(t, tool, "config", "filter.up.smudge", "tr A-Z a-z")
				commitAll(t, tool, "setup")
				if s := testGit(t, tool, "status", "--porcelain"); s != "" {
					t.Fatalf("git status lists %q", s)
				}
			})
			if change {
				write(t, d.Work(), "tool/a.dat", "abcd\n")
			}
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind != change || left.Nested {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestARecordThatDidNotRunKeepsTheCopy is a repository that the setup made, whose record session
// did not run or failed: the repository is other, the setup's end is unknown, and the copy stays
// with no change. A git session takes no path that still has [StateRepo].
func TestARecordThatDidNotRunKeepsTheCopy(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	out := map[string]LeftOut{"": {}}
	setupRepository(t, d.Work(), "tool")
	paths, err := d.SetupChanges(t.Context(), repos[0])
	if err != nil {
		t.Fatal(err)
	}
	states, err := d.SetupStates(t.Context(), repos[0], paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].State != StateRepo {
		t.Fatalf("the setup's paths are %v", states)
	}
	out[""] = LeftOut{Setup: states}
	if _, err := d.GitSession(t.Context(), branch, "", repos, out); err == nil {
		t.Error("a git session took a repository with no record")
	}
	if _, needed, err := d.RecordSession(t.Context(), repos, out); err != nil || !needed {
		t.Fatalf("a record of a repository of the setup: %t, %v", needed, err)
	}
	d.Recorded(repos, out, false)
	if got := out[""].Setup; len(got) != 1 || got[0].State != "other" || out[""].SetupEnd != -1 {
		t.Fatalf("the setup's paths are %v, its end %d", got, out[""].SetupEnd)
	}
	printed := sessionOutput(t, d, repos, out)
	if left := d.Left(repos); !left.Behind || left.Nested {
		t.Errorf("Left = %+v:\n%s", left, printed)
	}
}

// TestAReftableRepositoryOfTheSetup is a repository that the setup made with its refs in a
// reftable: with no change, the session keeps no copy, and a branch the agent makes there keeps
// it. It needs git 2.45 or later.
func TestAReftableRepositoryOfTheSetup(t *testing.T) {
	isolate(t)
	if out, err := exec.Command("git", "init", "--quiet", "--ref-format=reftable", t.TempDir()).CombinedOutput(); err != nil {
		t.Skipf("this git has no reftable: %v: %s", err, out)
	}
	for _, branchToo := range []bool{false, true} {
		t.Run(fmt.Sprint("branch ", branchToo), func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				tool := filepath.Join(w, "tool")
				write(t, tool, "a.go", "package a\n")
				testGit(t, tool, "init", "--quiet", "--ref-format=reftable")
				commitAll(t, tool, "setup")
			})
			if len(out[""].Setup) != 1 || !strings.HasPrefix(out[""].Setup[0].State, "repo:") {
				t.Fatalf("the setup's paths are %v", out[""].Setup)
			}
			if branchToo {
				testGit(t, filepath.Join(d.Work(), "tool"), "branch", "side")
			}
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind != branchToo || left.Nested {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestASubmoduleTheSetupLeftChanged is a repository that the setup made with a submodule, in which
// the setup left a changed file: with no change, the session keeps no copy. A further change of
// that file, or one that sets it back, keeps the copy.
func TestASubmoduleTheSetupLeftChanged(t *testing.T) {
	for _, c := range []struct {
		name, text string
	}{
		{"no change", ""},
		{"a further change", "package l // agent\n"},
		{"the file set back", "package l\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				toolWithSubmodule(t, w)
				write(t, w, "tool/lib/l.go", "package l // setup\n")
			})
			if c.text != "" {
				write(t, d.Work(), "tool/lib/l.go", c.text)
			}
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind != (c.text != "") || left.Nested {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestACommitInASubmoduleWithItsGitFolder is a repository that the setup made with a submodule
// whose .git is a folder in the submodule's working tree, not in .git/modules: a commit that the
// agent leaves on a branch of the submodule, and then leaves, keeps the copy.
func TestACommitInASubmoduleWithItsGitFolder(t *testing.T) {
	d, repos, out := setupWith(t, func(t *testing.T, w string) {
		up := t.TempDir()
		write(t, up, "l.go", "package l\n")
		testGit(t, up, "init", "--quiet")
		commitAll(t, up, "lib")
		tool := setupRepository(t, w, "tool")
		testGit(t, tool, "clone", "--quiet", up, "lib")
		testGit(t, tool, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", up, "lib")
		commitAll(t, tool, "submodule")
		if info, err := os.Lstat(filepath.Join(tool, "lib", ".git")); err != nil || !info.IsDir() {
			t.Fatalf("lib/.git is no folder: %v", err)
		}
	})
	lib := filepath.Join(d.Work(), "tool", "lib")
	testGit(t, lib, "checkout", "--quiet", "-b", "side")
	write(t, lib, "new.go", "package l\n")
	commitAll(t, lib, "agent")
	testGit(t, lib, "checkout", "--quiet", "-")
	printed := sessionOutput(t, d, repos, out)
	if left := d.Left(repos); !left.Behind {
		t.Errorf("Left = %+v:\n%s", left, printed)
	}
}

// TestADecomposedNameInARepositoryOfTheSetup is a repository that the setup made with names in
// Unicode's decomposed form, as macOS writes them: a file that git status lists and a folder with a
// .gitignore. With no change, the session keeps no copy, and a change of that file keeps it.
func TestADecomposedNameInARepositoryOfTheSetup(t *testing.T) {
	const name = "cafe\u0301.txt"
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprint("change ", change), func(t *testing.T) {
			d, repos, out := setupWith(t, func(t *testing.T, w string) {
				tool := setupRepository(t, w, "tool")
				write(t, tool, "e\u0301/.gitignore", "*.o\n")
				commitAll(t, tool, "folder")
				write(t, tool, name, "setup\n")
			})
			if change {
				write(t, d.Work(), "tool/"+name, "agent\n")
			}
			printed := sessionOutput(t, d, repos, out)
			if left := d.Left(repos); left.Behind != change || left.Nested {
				t.Errorf("Left = %+v:\n%s", left, printed)
			}
		})
	}
}

// TestARuleOfTheRunInASubmoduleTheCloneDidNotCopy is a nested submodule that the setup updated, and
// an agent that writes a file there with a .gitignore that covers it: the commit leaves the file out,
// and the copy stays.
func TestARuleOfTheRunInASubmoduleTheCloneDidNotCopy(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src := nestedSubmodule(t)
	d, repos := cloneRun(t, src)
	updateSubmodules(t, d.Work())
	out := setupRecord(t, d, repos[0])
	inner := filepath.Join(d.Work(), "libs", "m", "inner")
	write(t, inner, "x/.gitignore", "*\n")
	write(t, inner, "x/new.go", "package x\n")
	printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out})
	if !leftBehind(d, repos) {
		t.Errorf("the session recorded no work left behind:\n%s", printed)
	}
}

// TestARuleOfTheSetupIsAsThePersons is a setup that changes or writes a .gitignore and then a file
// that it covers, in the repository or in a submodule, where a file with a time before the clone's
// is in no record of the setup: the file stays out with no line, and the agent's work comes back.
func TestARuleOfTheSetupIsAsThePersons(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, w, sub string)
	}{
		{"a changed .gitignore", func(t *testing.T, w, sub string) {
			write(t, w, ".gitignore", "build/\ngen/\n")
			write(t, w, "gen/out.txt", "generated\n")
		}},
		{"a new .gitignore", func(t *testing.T, w, sub string) {
			write(t, w, "pkg/.gitignore", "*.o\n")
			write(t, w, "pkg/a.o", "object\n")
		}},
		{"a .gitignore in a submodule", func(t *testing.T, w, sub string) {
			s := filepath.Join(w, filepath.FromSlash(sub))
			write(t, s, ".gitignore", "gen/\n")
			write(t, s, "gen/out.txt", "generated\n")
		}},
		{"a file of an old time in a submodule", func(t *testing.T, w, sub string) {
			s := filepath.Join(w, filepath.FromSlash(sub))
			write(t, s, ".gitignore", "gen/\n")
			write(t, s, "gen/out.txt", "unpacked\n")
			old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := os.Chtimes(filepath.Join(s, "gen", "out.txt"), old, old); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, sub := withSubmodule(t)
			write(t, src, ".gitignore", "build/\n")
			commitAll(t, src, "rules")
			d, repos := cloneRun(t, src)
			c.setup(t, d.Work(), sub)
			out := setupRecord(t, d, repos[0])
			write(t, d.Work(), "new.txt", "new\n")
			q, _ := runRefs(t, d, src, repos, map[string]LeftOut{"": out})
			if got := files(t, q.Path, q.Head); !slices.Contains(got, "new.txt") || slices.Contains(got, "gen/out.txt") || slices.Contains(got, "pkg/a.o") {
				t.Errorf("the commit has %v", got)
			}
		})
	}
}

// TestAFolderFindFailsToListKeepsTheCopy is a nested submodule that no update populated, with a
// file the agent wrote there or with nothing, and a find that fails when it lists that folder: the
// folder counts as not empty, and the copy stays.
func TestAFolderFindFailsToListKeepsTheCopy(t *testing.T) {
	for _, file := range []string{"new.txt", ""} {
		t.Run(fmt.Sprintf("a file %q", file), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			bin := t.TempDir()
			for _, name := range []string{"git", "cat", "chmod", "comm", "cp", "find", "grep", "mkdir", "mv", "readlink", "rm", "sh", "sort", "tr"} {
				real, err := exec.LookPath(name)
				if err != nil {
					t.Skipf("no %s here: %v", name, err)
				}
				text := "#!/bin/sh\nexec '" + real + "' \"$@\"\n"
				if name == "find" {
					text = "#!/bin/sh\nif [ \"$2\" = '!' ] && [ \"$3\" = -name ] && [ \"$4\" = . ]; then exit 1; fi\n" + text[len("#!/bin/sh\n"):]
				}
				write(t, bin, name, text)
				if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			old := sessionPath
			sessionPath = bin
			t.Cleanup(func() { sessionPath = old })
			d, repos := cloneRun(t, nestedSubmodule(t))
			if file != "" {
				write(t, filepath.Join(d.Work(), "libs", "m", "inner"), file, "agent work\n")
			}
			printed := sessionOutput(t, d, repos, nil)
			if !d.Left(repos).Behind {
				t.Errorf("the session recorded no work left behind:\n%s", printed)
			}
		})
	}
}

// TestAnUnreadableFileComesBack is a file of mode 000 that the agent left, which the session's user
// owns, beside a commit, a change or nothing else: the session gives itself read access, and the
// file comes back with the rest.
func TestAnUnreadableFileComesBack(t *testing.T) {
	for name, c := range map[string]struct{ commit, change bool }{
		"a commit":      {commit: true},
		"a change":      {change: true},
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
			q, _ := runRefs(t, d, src, repos, nil)
			got := files(t, q.Path, q.Head)
			if c.commit || c.change {
				if !slices.Contains(got, "kept.txt") {
					t.Errorf("the commit has %v", got)
				}
			}
			if !slices.Contains(got, "locked.txt") {
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
			if leftBehind(d, repos) {
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

// TestASparseCheckoutCommitsNoDeletion is a sparse checkout in the copy, with or without a change:
// the run's branch gets no commit, and a change comes back on HEAD under the run's prefix, with
// each path the checkout took out as HEAD has it.
func TestASparseCheckoutCommitsNoDeletion(t *testing.T) {
	for name, agent := range map[string]func(t *testing.T, work string){
		"sparse-checkout": func(t *testing.T, work string) {
			testGit(t, work, "sparse-checkout", "set", "nothing-here")
		},
		"skip-worktree": func(t *testing.T, work string) {
			testGit(t, work, "update-index", "--skip-worktree", "sub/a.txt")
			if err := os.Remove(filepath.Join(work, "sub", "a.txt")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		for _, changed := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				isolate(t)
				sessionGit(t)
				src, base := person(t)
				write(t, src, "sub/a.txt", "a\n")
				write(t, src, "sub/b.txt", "b\n")
				base = commitAll(t, src, "a folder")
				d, repos := cloneRun(t, src)
				agent(t, d.Work())
				if !changed {
					noBundle(t, d, repos, false)
					return
				}
				write(t, d.Work(), "README.md", "changed\n")
				q, refs := runRefs(t, d, src, repos, nil)
				if q.Head != "" {
					t.Errorf("the run's branch came back at %s", q.Head)
				}
				wantRefs(t, refs, "uncommitted")
				if got := files(t, src, prefix+"/uncommitted"); !slices.Equal(got, files(t, src, base)) {
					t.Errorf("uncommitted has %v", got)
				}
				if got := testGit(t, src, "show", prefix+"/uncommitted:README.md"); got != "changed" {
					t.Errorf("README.md is %q", got)
				}
			})
		}
	}
}

// TestASetupFileInASubmoduleLeavesNothingBehind is a setup that wrote a file in a submodule: the
// file is the setup's and stays out, and a change the agent makes after that comes back.
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
			if !agentChanges {
				noBundleWith(t, d, repos, map[string]LeftOut{"": out})
				return
			}
			write(t, inner, "gen.o", "the agent's\n")
			q, _ := runRefs(t, d, src, repos, map[string]LeftOut{"": out})
			if len(q.Submodules) != 1 {
				t.Fatalf("%d submodules came back", len(q.Submodules))
			}
			gitDir := testGit(t, filepath.Join(src, sub), "rev-parse", "--path-format=absolute", "--git-dir")
			if got := testGit(t, gitDir, "show", prefix+"/uncommitted:gen.o"); got != "the agent's" {
				t.Errorf("gen.o is %q", got)
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
	if leftBehind(d, repos) {
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
		if got := strings.TrimPrefix(ShownError(err).Error(), "git status: "); got != want {
			t.Errorf("run printed %q for %q, want %q", got, raw, want)
		}
		if strings.Contains(ge.line, "ZZSECRETZZ") {
			t.Errorf("run printed %q for %q, with the secret", ge.line, raw)
		}
	}
}

// TestALineOfGitInitKeepsItsPath is a real error of git init in a long folder of the copy, as the
// quarantine runs it: the line has the whole path, for a caller to take it out before the cut, and
// [ShownError] cuts the line as before.
func TestALineOfGitInitKeepsItsPath(t *testing.T) {
	isolate(t)
	long := filepath.Join(t.TempDir(), strings.Repeat("a-long-cache-folder/", 12))
	t.Setenv("HOME", long)
	t.Setenv("XDG_CACHE_HOME", long)
	d := newDir(t)
	file := filepath.Join(d.Root(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(file, "0.git")
	_, err := git(t.Context(), d.Root(), "init", "--quiet", "--bare", "--template=", "--", q)
	var ge *gitError
	if !errors.As(err, &ge) || !ge.uncut || !strings.Contains(ge.line, q) {
		t.Fatalf("the error of git init is %v, want git's line with %s", err, q)
	}
	shownErr := ShownError(fmt.Errorf(textQuarantine, shown(q), err)).Error()
	if want := fmt.Errorf(textQuarantine, shown(q), errors.New("git init: "+shown(ge.line))).Error(); shownErr != want || !strings.HasSuffix(shownErr, "...") {
		t.Errorf("the line is %q, want %q", shownErr, want)
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
			if printed := sessionOutput(t, d, repos, map[string]LeftOut{"": out}); leftBehind(d, repos) {
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
	if leftBehind(d, repos) {
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
			if left := d.Left(repos); !left.Behind || left.Nested {
				t.Errorf("the session did not record the undo as work git could not add: %+v\n%s", left, printed)
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
	if leftBehind(d, repos) {
		t.Errorf("the session recorded work left behind:\n%s", printed)
	}
}

// TestALockedSetupFileInASubmoduleComesBack is a setup that leaves a file of mode 000 or a folder
// of mode 0444 in a submodule, which the record of the setup cannot read: the session gives itself
// read access, and the file comes back in the submodule's uncommitted.
func TestALockedSetupFileInASubmoduleComesBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 000, so the record of the setup has it, and it stays out")
	}
	for name, c := range map[string]struct {
		path, file string
		mode       os.FileMode
	}{
		"a file of mode 000":    {"gen.o", "gen.o", 0},
		"a folder of mode 0444": {"gen", "gen/f.o", 0o444},
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
			q, _ := runRefs(t, d, src, repos, map[string]LeftOut{"": out})
			if len(q.Submodules) != 1 {
				t.Fatalf("%d submodules came back", len(q.Submodules))
			}
			gitDir := testGit(t, filepath.Join(src, sub), "rev-parse", "--path-format=absolute", "--git-dir")
			if got := testGit(t, gitDir, "show", prefix+"/uncommitted:"+c.file); got != "generated" {
				t.Errorf("%s is %q", c.file, got)
			}
		})
	}
}

// TestANameThatReadsAsTwoStatusLinesComesBack is an agent's file in a submodule whose name has a
// line break, and whose pieces read as two status lines of files the setup left: no file counts as
// the setup's, and the file comes back in the submodule's uncommitted.
func TestANameThatReadsAsTwoStatusLinesComesBack(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	d, repos := cloneRun(t, src)
	inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
	write(t, inner, "foo.o", "generated\n")
	write(t, inner, "gen.o", "generated\n")
	out := setupRecord(t, d, repos[0])
	write(t, inner, "foo.o\n?? gen.o", "the agent's\n")
	q, _ := runRefs(t, d, src, repos, map[string]LeftOut{"": out})
	if len(q.Submodules) != 1 {
		t.Fatalf("%d submodules came back", len(q.Submodules))
	}
	gitDir := testGit(t, filepath.Join(src, sub), "rev-parse", "--path-format=absolute", "--git-dir")
	if got := testGit(t, gitDir, "show", prefix+"/uncommitted:foo.o\n?? gen.o"); got != "the agent's" {
		t.Errorf("the file is %q", got)
	}
}

// TestASeedInTheCopyLeavesOutWhatTheBranchRemoved is a work item's branch that deleted src/old.py,
// and a next run that starts from a prebuilt image's copy of the repository: the run's fetch leaves
// both commits at depth 1, so no branch or tag reaches the target version, and the file stays out
// of the run's copy. The ref leaves the git session nothing to report.
func TestASeedInTheCopyLeavesOutWhatTheBranchRemoved(t *testing.T) {
	isolate(t)
	sessionGit(t)
	up := t.TempDir()
	testGit(t, up, "init", "--quiet", "-b", "main")
	write(t, up, "src/old.py", "old\n")
	write(t, up, "keep.py", "keep\n")
	target := commitAll(t, up, "first")
	testGit(t, up, "checkout", "--quiet", "-b", "item")
	testGit(t, up, "rm", "--quiet", "src/old.py")
	commitAll(t, up, "remove")

	work := t.TempDir()
	testGit(t, work, "init", "--quiet", "-b", "unused")
	testGit(t, work, "fetch", "--quiet", "--depth", "1", "file://"+up, "main")
	testGit(t, work, "checkout", "--quiet", "--detach", "FETCH_HEAD")
	testGit(t, work, "update-ref", preparation.TargetRef, "HEAD")
	testGit(t, work, "fetch", "--quiet", "--depth", "1", "file://"+up, "item")
	testGit(t, work, "checkout", "--quiet", "-b", branch, "FETCH_HEAD")

	d, repos := cloneRun(t, work)
	if got := testGit(t, d.Work(), "rev-parse", "--verify", preparation.TargetRef+"^{commit}"); got != target {
		t.Errorf("the copy has the target version at %q, want %q", got, target)
	}
	runGit(t, d, repos, nil)
	if leftBehind(d, repos) {
		t.Error("the git session reports work left behind")
	}

	seed := t.TempDir()
	testGit(t, seed, "clone", "--quiet", "--branch", "main", "file://"+up, ".")
	testGit(t, seed, "remote", "set-url", "origin", "https://github.com/acme/shop")
	write(t, seed, "lib/dependency.py", "installed\n")
	env := map[string]string{preparation.EnvRepositories: "acme/shop", preparation.EnvSystem: "github.com"}
	if _, err := preparation.Prepare(context.Background(), d.Work(), seed, func(k string) string { return env[k] }, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(d.Work(), "src", "old.py")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the seed brought back src/old.py: %v", err)
	}
	if got := read(t, d.Work(), "lib/dependency.py"); got != "installed\n" {
		t.Errorf("lib/dependency.py is %q", got)
	}
}

// TestACloneWithNoTargetVersionHasNoRef is a local run's repository.
func TestACloneWithNoTargetVersionHasNoRef(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	if out, err := gitCmd(d.Work(), "rev-parse", "--verify", "--quiet", preparation.TargetRef).Output(); err == nil {
		t.Errorf("the copy has the ref at %s", out)
	}
}

// TestAnAddAllInTheCopyStagesNoFurtherRepository is an agent's git add -A in a run with a further
// repository: no gitlink goes into the index.
func TestAnAddAllInTheCopyStagesNoFurtherRepository(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	further := filepath.Join(src, ".qory", "repositories", "example", "further")
	write(t, further, "f.txt", "further\n")
	testGit(t, further, "init", "--quiet", "-b", "main")
	commitAll(t, further, "further")
	d, _ := cloneRun(t, src)
	testGit(t, d.Work(), "add", "-A")
	if staged := testGit(t, d.Work(), "ls-files", "--stage"); strings.Contains(staged, "160000") || strings.Contains(staged, ".qory") {
		t.Errorf("the index has:\n%s", staged)
	}
}

// TestArtifactsBackWritesNoQoryWithNoArtifacts is a copy with a .qory that has no artifacts: the
// person's folder gets no .qory.
func TestArtifactsBackWritesNoQoryWithNoArtifacts(t *testing.T) {
	isolate(t)
	src, _ := person(t)
	d, _ := cloneRun(t, src)
	write(t, d.Work(), ".qory/other.txt", "the run\n")
	if _, err := d.CopyArtifactsBack(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(src, ".qory")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the person's folder has .qory: %v", err)
	}
	write(t, d.Work(), ".qory/artifacts/note/main/1/a.txt", "note\n")
	if _, err := d.CopyArtifactsBack(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if got := read(t, src, ".qory/artifacts/note/main/1/a.txt"); got != "note\n" {
		t.Errorf("the artifact came back as %q", got)
	}
}

// TestTheGitSessionRunsItsScriptFromAFile is the session on each shell: its arguments have the
// file's path and no script text, its work comes back, and a file planted over the script after
// the session changes nothing the host reads.
func TestTheGitSessionRunsItsScriptFromAFile(t *testing.T) {
	for _, shell := range []string{"/bin/sh", "/bin/dash"} {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			if _, err := os.Stat(shell); err != nil {
				t.Skipf("no %s here", shell)
			}
			isolate(t)
			sessionGit(t)
			src, _ := person(t)
			d, repos := cloneRun(t, src)
			write(t, d.Work(), "left.txt", "left\n")
			s, err := d.GitSession(context.Background(), branch, "", repos, nil)
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(d.Bundle(), SessionScriptName)
			if s.Args[0] != script {
				t.Errorf("the session's first argument is %q, want %q", s.Args[0], script)
			}
			if joined := strings.Join(s.Args, " "); strings.Contains(joined, "set -eu") || len(joined) > 2000 {
				t.Errorf("the session's arguments have the script, %d characters", len(joined))
			}
			if got := read(t, d.Bundle(), SessionScriptName); got != sessionScript {
				t.Error("the file does not have the script's text")
			}
			s.Command = shell
			runSession(t, s)
			write(t, d.Bundle(), SessionScriptName, "exit 9\n")
			if leftBehind(d, repos) {
				t.Error("the host reports work left behind")
			}
			qs, err := d.Quarantine(context.Background(), branch, repos)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := qs[0].FetchInto(context.Background(), src, branch, "run-1"); err != nil {
				t.Fatal(err)
			}
			testGit(t, src, "cat-file", "-e", branch+":left.txt")
		})
	}
}

// sparseFile makes the file rel in dir with size bytes and no data written, so a test of the size
// cap writes nothing to the disk.
func sparseFile(t *testing.T, dir, rel string, size int64) {
	t.Helper()
	write(t, dir, rel, "")
	if err := os.Truncate(filepath.Join(dir, filepath.FromSlash(rel)), size); err != nil {
		t.Fatal(err)
	}
}

// TestAFileOverTheSizeCapStaysInTheCopy is an agent that commits two files of more than MaxFile
// bytes, changes one of them, and leaves a new one, an ignored one, one of MaxFile bytes and a
// small change: the run's commit leaves out the changed file and the new one, and n.large lists
// both. The commit of the agent keeps its files, and nothing else stays behind.
func TestAFileOverTheSizeCapStaysInTheCopy(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	write(t, src, ".gitignore", "ignored.bin\n")
	commitAll(t, src, "ignore")
	d, repos := cloneRun(t, src)
	sparseFile(t, d.Work(), "committed.bin", MaxFile+1)
	sparseFile(t, d.Work(), "changed.bin", MaxFile+1)
	agentCommit := commitAll(t, d.Work(), "two large files")
	sparseFile(t, d.Work(), "changed.bin", MaxFile+2)
	sparseFile(t, d.Work(), "big/new.bin", MaxFile+1)
	sparseFile(t, d.Work(), "ignored.bin", MaxFile+1)
	sparseFile(t, d.Work(), "exact.bin", MaxFile)
	write(t, d.Work(), "small.txt", "small\n")
	q := bringBack(t, d, src, repos, nil)
	got := files(t, src, q.Head)
	for _, want := range []string{"committed.bin", "changed.bin", "exact.bin", "small.txt"} {
		if !slices.Contains(got, want) {
			t.Errorf("the run's commit lacks %s: %v", want, got)
		}
	}
	for _, out := range []string{"big/new.bin", "ignored.bin"} {
		if slices.Contains(got, out) {
			t.Errorf("the run's commit has %s", out)
		}
	}
	if size := testGit(t, src, "cat-file", "-s", q.Head+":changed.bin"); size != strconv.Itoa(MaxFile+1) {
		t.Errorf("changed.bin in the run's commit has %s bytes, want the agent's commit", size)
	}
	if parent := testGit(t, src, "rev-parse", q.Head+"^"); parent != agentCommit {
		t.Errorf("the run's commit follows %s, want the agent's commit %s", parent, agentCommit)
	}
	left := d.Left(repos)
	if want := []string{"big/new.bin", "changed.bin"}; !slices.Equal(left.Large, want) || left.Behind {
		t.Errorf("Left = %+v, want large %v and nothing behind", left, want)
	}
}

// TestALargeFileAloneBringsNoCommit is a run whose only change is a file over the cap: no bundle,
// and the file is listed.
func TestALargeFileAloneBringsNoCommit(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	sparseFile(t, d.Work(), "data.bin", MaxFile+1)
	printed := sessionOutput(t, d, repos, nil)
	if _, err := d.Quarantine(context.Background(), branch, repos); !errors.Is(err, ErrNoBundle) {
		t.Errorf("Quarantine = %v, want no bundle\n%s", err, printed)
	}
	if left := d.Left(repos); !slices.Equal(left.Large, []string{"data.bin"}) || left.Behind || !left.Any() {
		t.Errorf("Left = %+v\n%s", left, printed)
	}
}

// TestALargeFileInASubmoduleStaysInTheCopy is a file over the cap in a submodule the clone copied:
// it stays out of the submodule's uncommitted, and n.large lists it by its path in the copy.
func TestALargeFileInASubmoduleStaysInTheCopy(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	d, repos := cloneRun(t, src)
	inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
	sparseFile(t, inner, "data.bin", MaxFile+1)
	write(t, inner, "small.txt", "small\n")
	q, _ := runRefs(t, d, src, repos, nil)
	if len(q.Submodules) != 1 {
		t.Fatalf("%d submodules came back", len(q.Submodules))
	}
	gitDir := testGit(t, filepath.Join(src, sub), "rev-parse", "--path-format=absolute", "--git-dir")
	got := files(t, gitDir, prefix+"/uncommitted")
	if !slices.Contains(got, "small.txt") || slices.Contains(got, "data.bin") {
		t.Errorf("the submodule's uncommitted has %v", got)
	}
	if left := d.Left(repos); !slices.Equal(left.Large, []string{sub + "/data.bin"}) || left.Behind {
		t.Errorf("Left = %+v", left)
	}
}

// TestLeftReadsTheListsAsText is a list that is no regular file, which counts as work left behind,
// and lists past 1 MiB, which end with "..." and keep no line the cut ended.
func TestLeftReadsTheListsAsText(t *testing.T) {
	d := newDir(t)
	repos := []Repository{{}, {}}
	if err := os.Mkdir(d.Bundle(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(d.Bundle(), "0.large"), 0o700); err != nil {
		t.Fatal(err)
	}
	if left := d.Left(repos); !left.Behind || len(left.Large) > 0 {
		t.Errorf("a folder at 0.large: Left = %+v", left)
	}
	if err := os.Remove(filepath.Join(d.Bundle(), "0.large")); err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("a", 1000) + "\n"
	write(t, d.Bundle(), "0.large", strings.Repeat(line, 1000))
	write(t, d.Bundle(), "1.large", strings.Repeat(line, 100))
	left := d.Left(repos)
	if left.Behind || len(left.Large) != 1047+1 || left.Large[len(left.Large)-1] != "..." {
		t.Errorf("past 1 MiB: %d files, behind %v, last %.10q", len(left.Large), left.Behind, left.Large[len(left.Large)-1])
	}
	for _, f := range left.Large[:len(left.Large)-1] {
		if len(f) != 1000 {
			t.Fatalf("a listed file has %d bytes", len(f))
		}
	}
}

// TestARefAtTheBaseCommitComesBack is a tag and a branch at the base commit and an annotated tag
// of it, with no other work: a bundle leaves out a ref whose commit it excludes, and each comes
// back under the prefix all the same, at the person's commit or with its tag object.
func TestARefAtTheBaseCommitComesBack(t *testing.T) {
	bothChecks(t, func(t *testing.T) {
		sessionGit(t)
		src, base := person(t)
		d, repos := cloneRun(t, src)
		testGit(t, d.Work(), "tag", "v-base")
		testGit(t, d.Work(), "branch", "scratch")
		testGit(t, d.Work(), "tag", "-a", "-m", "annotated", "v-annotated")
		q, refs := runRefs(t, d, src, repos, nil)
		if q.Head != "" {
			t.Errorf("the run's branch came back at %s", q.Head)
		}
		wantRefs(t, refs, "tags/v-base", "heads/scratch", "tags/v-annotated")
		if refs["tags/v-base"] != base || refs["heads/scratch"] != base {
			t.Errorf("the refs are %v, want them at %s", refs, base)
		}
		if kind := testGit(t, src, "cat-file", "-t", prefix+"/tags/v-annotated"); kind != "tag" {
			t.Errorf("the annotated tag came back as a %s", kind)
		}
		if at := testGit(t, src, "rev-parse", prefix+"/tags/v-annotated^{commit}"); at != base {
			t.Errorf("the annotated tag is at %s, want %s", at, base)
		}
	})
}

// TestARefAtTheBaseCommitComesBackBesideTheBranch is a tag of the base commit and a commit on the
// run's branch: both come back, in one fetch.
func TestARefAtTheBaseCommitComesBackBesideTheBranch(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, base := person(t)
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "tag", "v-base")
	write(t, d.Work(), "x.txt", "x\n")
	q, refs := runRefs(t, d, src, repos, nil)
	if q.Head == "" || !slices.Contains(files(t, src, "refs/heads/"+branch), "x.txt") {
		t.Errorf("the run's branch did not come back")
	}
	wantRefs(t, refs, "tags/v-base")
	if refs["tags/v-base"] != base {
		t.Errorf("the tag is at %s, want %s", refs["tags/v-base"], base)
	}
}

// TestAListOfKnownRefsIsChecked is a list of refs at the person's commits that the git session
// could have changed: an id the repository lacks, the id of a blob, a ref of another form, HEAD,
// two names in two letter cases, and a line that is no ref: each fails the quarantine, and no ref
// reaches the person's repository.
func TestAListOfKnownRefsIsChecked(t *testing.T) {
	for name, c := range map[string]struct{ line, want string }{
		"a missing id":     {strings.Repeat("1", 40) + " " + prefix + "/tags/x", "is no commit in"},
		"a blob":           {"BLOB " + prefix + "/tags/x", "is no commit in"},
		"HEAD":             {"BASE " + prefix + "/HEAD", "which is not a commit id and a branch or tag under"},
		"stash":            {"BASE " + prefix + "/stash", "which is not a commit id and a branch or tag under"},
		"outside":          {"BASE refs/heads/main", "which is not a commit id and a branch or tag under"},
		"another run":      {"BASE refs/qory/run-2/tags/x", "which is not a commit id and a branch or tag under"},
		"a short id":       {"1234 " + prefix + "/tags/x", "which is not a commit id and a branch or tag under"},
		"two letter cases": {"BASE " + prefix + "/tags/V-Base", "twice"},
	} {
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			isolate(t)
			sessionGit(t)
			src, base := person(t)
			blob := testGit(t, src, "rev-parse", base+":README.md")
			before := testGit(t, src, "for-each-ref")
			d, repos := cloneRun(t, src)
			testGit(t, d.Work(), "tag", "v-base")
			sessionOutput(t, d, repos, nil)
			line := strings.NewReplacer("BASE", base, "BLOB", blob).Replace(c.line)
			known := filepath.Join(d.Bundle(), "0.known")
			data, err := os.ReadFile(known)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(known, append(data, line+"\n"...), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = d.Quarantine(context.Background(), branch, repos)
			hasPrefix(t, name, err, "the run's bundle for . failed git's checks")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("the error %v does not have %q; the list:\n%s", err, c.want, read(t, d.Bundle(), "0.known"))
			}
			if after := testGit(t, src, "for-each-ref"); after != before {
				t.Errorf("your repository's refs were\n%s\nand are\n%s", before, after)
			}
		})
	}
}

// TestAKnownRefFetchesOnlyWhereItsCommitIs is a quarantine with a tag of the base commit, fetched
// into a repository that lacks that commit: the fetch stops before it writes any ref.
func TestAKnownRefFetchesOnlyWhereItsCommitIs(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, _ := person(t)
	d, repos := cloneRun(t, src)
	testGit(t, d.Work(), "tag", "v-base")
	runGit(t, d, repos, nil)
	qs, err := d.Quarantine(context.Background(), branch, repos)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	testGit(t, other, "init", "--quiet", "-b", "main")
	write(t, other, "other.txt", "other\n")
	commitAll(t, other, "other")
	before := testGit(t, other, "for-each-ref")
	_, _, err = qs[0].FetchInto(context.Background(), other, branch, "run-1")
	hasPrefix(t, "a fetch", err, "fetch "+prefix+"/* from ")
	if err == nil || !strings.Contains(err.Error(), "is no commit in") {
		t.Errorf("the error is %v", err)
	}
	if after := testGit(t, other, "for-each-ref"); after != before {
		t.Errorf("the refs were\n%s\nand are\n%s", before, after)
	}
}

// TestATagInASubmoduleAtItsCommitComesBack is a tag at the commit that the clone checked out in a
// submodule: it comes back under the prefix in the person's git folder of the submodule.
func TestATagInASubmoduleAtItsCommitComesBack(t *testing.T) {
	isolate(t)
	sessionGit(t)
	src, sub := withSubmodule(t)
	d, repos := cloneRun(t, src)
	inner := filepath.Join(d.Work(), filepath.FromSlash(sub))
	at := testGit(t, inner, "rev-parse", "HEAD")
	testGit(t, inner, "tag", "v-sub")
	q, refs := runRefs(t, d, src, repos, nil)
	wantRefs(t, refs)
	if len(q.Submodules) != 1 {
		t.Fatalf("%d submodules came back", len(q.Submodules))
	}
	gitDir := testGit(t, filepath.Join(src, sub), "rev-parse", "--path-format=absolute", "--git-dir")
	wantRefs(t, refsUnder(t, gitDir, prefix), "tags/v-sub")
	if got := testGit(t, gitDir, "rev-parse", prefix+"/tags/v-sub"); got != at {
		t.Errorf("the tag is at %s, want %s", got, at)
	}
}
