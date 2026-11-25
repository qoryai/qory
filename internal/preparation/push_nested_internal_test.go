package preparation

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/proxygit"
)

// nestedRepo makes a repository at sub of dir: one commit, a filter of f.txt by its
// .gitattributes, and a clean filter in its configuration that makes the file marker. It
// returns the commit.
func nestedRepo(t *testing.T, dir, sub, marker string) string {
	t.Helper()
	d := filepath.Join(dir, sub)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, d, "init", "--quiet", "-b", "main")
	write(t, filepath.Join(d, "f.txt"), "one\n")
	write(t, filepath.Join(d, ".gitattributes"), "*.txt filter=x\n")
	run(t, d, "add", "-A")
	run(t, d, "commit", "--quiet", "-m", "n")
	run(t, d, "config", "filter.x.clean", "touch "+marker+"; cat")
	return run(t, d, "rev-parse", "HEAD")
}

// touchNested changes f.txt of the repository at sub of dir, with another time and the same
// size, so git status there runs the filter.
func touchNested(t *testing.T, dir, sub string) {
	t.Helper()
	f := filepath.Join(dir, sub, "f.txt")
	write(t, f, "two\n")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(f, later, later); err != nil {
		t.Fatal(err)
	}
}

// gitIn runs git in dir with in on its stdin, and returns its output.
func gitIn(t *testing.T, dir, in string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Stdin = dir, strings.NewReader(in)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitlinksOf is each gitlink of the commit HEAD at dir, as git ls-tree prints it.
func gitlinksOf(t *testing.T, dir string) []string {
	t.Helper()
	var links []string
	for entry := range strings.SplitSeq(run(t, dir, "ls-tree", "-r", "-z", "HEAD"), "\x00") {
		if strings.HasPrefix(entry, gitlinkMode+" ") {
			links = append(links, entry)
		}
	}
	return links
}

// nestedLayout is a place where the agent left a repository in the checkout: make makes it and
// returns the folders of the repositories in the checkout.
type nestedLayout struct {
	name string
	make func(t *testing.T, dir string, b *WorkBranch, marker string) []string
	// noPush is a layout whose commit the remote does not take.
	noPush bool
	// told are the folders that a line is for, nil for the ones that make returns.
	told []string
	// has are the entries of the commit below sub or d, as git ls-tree prints their names.
	has string
}

// linkLayout commits the repository at sub as a gitlink, and then runs after.
func linkLayout(sub string, after func(t *testing.T, dir string)) func(*testing.T, string, *WorkBranch, string) []string {
	return func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		nestedRepo(t, dir, sub, marker)
		run(t, dir, "add", "-A", "-f")
		run(t, dir, "commit", "--quiet", "-m", "link")
		if after != nil {
			after(t, dir)
		}
		return []string{sub}
	}
}

// rename moves from to to, both below dir.
func rename(t *testing.T, dir, from, to string) {
	t.Helper()
	if err := os.Rename(filepath.Join(dir, from), filepath.Join(dir, to)); err != nil {
		t.Fatal(err)
	}
}

var nestedLayouts = []nestedLayout{
	{name: "a gitlink in HEAD", make: linkLayout("sub", func(t *testing.T, dir string) {
		write(t, filepath.Join(dir, ".gitmodules"), "[submodule \"sub\"]\n\tpath = sub\n\turl = https://git.example.com/acme/sub.git\n\tignore = none\n")
	})},
	{name: "a gitlink in the index", make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		nestedRepo(t, dir, "sub", marker)
		run(t, dir, "add", "-A")
		return []string{"sub"}
	}},
	{name: "untracked", make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		nestedRepo(t, dir, "sub", marker)
		return []string{"sub"}
	}},
	{name: "a gitlink at another commit", make: func(t *testing.T, dir string, b *WorkBranch, marker string) []string {
		nestedRepo(t, dir, "sub", marker)
		run(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+b.Repositories[0].Commit+",sub")
		run(t, dir, "commit", "--quiet", "-m", "link")
		return []string{"sub"}
	}},
	{name: "a .git file", make: linkLayout("sub", func(t *testing.T, dir string) {
		rename(t, dir, "sub/.git", "away")
		write(t, filepath.Join(dir, "sub", ".git"), "gitdir: ../away\n")
	})},
	{name: "an untracked .git file", make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		nestedRepo(t, dir, "sub", marker)
		rename(t, dir, "sub/.git", "away")
		write(t, filepath.Join(dir, "sub", ".git"), "gitdir: ../away\n")
		return []string{"sub"}
	}},
	{name: "in a nested one", told: []string{"sub"}, make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		nestedRepo(t, dir, "sub", marker)
		nestedRepo(t, dir, "sub/deep", marker)
		run(t, filepath.Join(dir, "sub"), "add", "deep")
		run(t, filepath.Join(dir, "sub"), "commit", "--quiet", "-m", "deep")
		run(t, dir, "add", "-A")
		run(t, dir, "commit", "--quiet", "-m", "link")
		return []string{"sub", "sub/deep"}
	}},
	{name: "a line feed and odd bytes", make: linkLayout("s\n:(top)*\té", nil)},
	// git lists the files there one by one, since HEAD has one of them, and they stay out.
	{name: "a tracked folder that gains a .git", has: "sub/f.txt", make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "sub", "f.txt"), "one\n")
		run(t, dir, "add", "-A")
		run(t, dir, "commit", "--quiet", "-m", "files")
		nestedRepo(t, dir, "sub", marker)
		return []string{"sub"}
	}},
	{name: "a file that is now a repository", has: "sub", make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		write(t, filepath.Join(dir, "sub"), "file\n")
		run(t, dir, "add", "-A")
		run(t, dir, "commit", "--quiet", "-m", "file")
		if err := os.Remove(filepath.Join(dir, "sub")); err != nil {
			t.Fatal(err)
		}
		nestedRepo(t, dir, "sub", marker)
		return []string{"sub"}
	}},
	{name: "a file that is now a folder with one", has: "d/new.txt", make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		write(t, filepath.Join(dir, "d"), "d\n")
		run(t, dir, "add", "-A")
		run(t, dir, "commit", "--quiet", "-m", "file")
		if err := os.Remove(filepath.Join(dir, "d")); err != nil {
			t.Fatal(err)
		}
		nestedRepo(t, dir, "d/sub", marker)
		write(t, filepath.Join(dir, "d", "new.txt"), "new\n")
		return []string{"d/sub"}
	}},
	{name: "a HEAD with a file d and a gitlink d/sub", noPush: true, make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		id := nestedRepo(t, dir, "d/sub", marker)
		blob := gitIn(t, dir, "x\n", "hash-object", "-w", "--stdin")
		sub := gitIn(t, dir, "160000 commit "+id+"\tsub\n", "mktree", "--missing")
		tree := gitIn(t, dir, run(t, dir, "ls-tree", "HEAD")+"\n100644 blob "+blob+"\td\n040000 tree "+sub+"\td\n", "mktree", "--missing")
		run(t, dir, "update-ref", "HEAD", run(t, dir, "commit-tree", tree, "-p", "HEAD", "-m", "both"))
		return []string{"d/sub"}
	}},
	// The line has the folder in the letter case of the disk.
	{name: "a file d and a gitlink D/sub", told: []string{}, make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		write(t, filepath.Join(dir, "d"), "d\n")
		run(t, dir, "add", "-A")
		run(t, dir, "commit", "--quiet", "-m", "file")
		if err := os.Remove(filepath.Join(dir, "d")); err != nil {
			t.Fatal(err)
		}
		id := nestedRepo(t, dir, "d/sub", marker)
		run(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+id+",D/sub")
		run(t, dir, "commit", "--quiet", "-m", "link")
		return []string{"d/sub"}
	}},
	{name: "a gitlink behind a symlink", told: []string{"other/sub"}, make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		linkLayout("real/sub", nil)(t, dir, nil, marker)
		rename(t, dir, "real", "other")
		if err := os.Symlink("other", filepath.Join(dir, "real")); err != nil {
			t.Fatal(err)
		}
		return []string{"other/sub"}
	}},
	{name: "an ignored gitlink", make: linkLayout("sub", func(t *testing.T, dir string) {
		write(t, filepath.Join(dir, ".gitignore"), "sub\nsub/\n")
	})},
	{name: "a gitlink that is a symlink to a repository", told: []string{"real"}, make: func(t *testing.T, dir string, _ *WorkBranch, marker string) []string {
		linkLayout("sub", nil)(t, dir, nil, marker)
		rename(t, dir, "sub", "real")
		if err := os.Symlink("real", filepath.Join(dir, "sub")); err != nil {
			t.Fatal(err)
		}
		return []string{"real"}
	}},
	{name: "a gitlink below .qory", make: linkLayout(".qory/sub", nil)},
}

// TestPushRunsNoProgramOfANestedRepository: no filter of a repository in the checkout runs in a
// push, or in a second one after it, the commit that qory makes has no gitlink that the agent's
// HEAD did not have, and the line for each repository is printed once for a push.
func TestPushRunsNoProgramOfANestedRepository(t *testing.T) {
	for _, l := range nestedLayouts {
		t.Run(l.name, func(t *testing.T) {
			dir, b := onBranch(t, "acme/shop")
			marker := filepath.Join(t.TempDir(), "ran")
			subs := l.make(t, dir, b, marker)
			for _, sub := range subs {
				touchNested(t, dir, sub)
			}
			// The set-up runs git in the nested repositories, and an add in the checkout.
			if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			before := gitlinksOf(t, dir)
			for n := range 2 {
				write(t, filepath.Join(dir, "left.txt"), strings.Repeat("left\n", n+1))
				_, lines, err := pushOf(dir, b)
				if err != nil && !l.noPush {
					t.Errorf("push %d: %v", n+1, err)
				}
				if _, err := os.Lstat(marker); err == nil {
					t.Fatalf("push %d ran the filter", n+1)
				}
				if got := run(t, dir, "show", "HEAD:left.txt"); len(got) != 5*(n+1)-1 {
					t.Errorf("push %d: the commit has left.txt with %q", n+1, got)
				}
				told := l.told
				if told == nil {
					told = subs
				}
				for _, sub := range told {
					if line := fmt.Sprintf(textNestedRepository, shownText(sub), "acme/shop"); strings.Count(lines, line) != 1 {
						t.Errorf("push %d printed no line for %q: %q", n+1, sub, lines)
					}
				}
				if l.has != "" {
					if got := run(t, dir, "ls-tree", "-r", "--name-only", "HEAD", "sub", "d"); got != l.has {
						t.Errorf("push %d: the commit has %q, want %q", n+1, got, l.has)
					}
				}
				for _, link := range gitlinksOf(t, dir) {
					if !slices.Contains(before, link) {
						t.Errorf("push %d: the commit has the new gitlink %q", n+1, link)
					}
				}
				for line := range strings.Lines(lines) {
					if strings.Count(lines, line) != 1 {
						t.Errorf("push %d printed %q twice", n+1, line)
					}
				}
			}
		})
	}
}

// TestPushLeavesOutAFileThatTheAddFindsARepository: a file that becomes a repository between
// git status and the add stays what HEAD has in the push step's commit, with its line.
func TestPushLeavesOutAFileThatTheAddFindsARepository(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	write(t, filepath.Join(dir, "sub"), "one\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "--quiet", "-m", "file")
	write(t, filepath.Join(dir, "sub"), "two\n")
	write(t, filepath.Join(dir, "left.txt"), "left\n")
	base, marker := t.TempDir(), filepath.Join(t.TempDir(), "ran")
	nestedRepo(t, base, "sub", marker)
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin := t.TempDir()
	// Once, before the first git add: the push step adds sub, which HEAD has, with git add -u.
	swapped := filepath.Join(bin, "swapped")
	write(t, filepath.Join(bin, "git"), "#!/bin/sh\ncase \" $* \" in *' add -u '*|*' add -A '*) [ -e '"+swapped+"' ] || { : > '"+swapped+"'; rm '"+dir+"/sub'; cp -R '"+base+"/sub' '"+dir+"/sub'; } ;; esac\nexec '"+real+"' \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, lines, err := pushOf(dir, b)
	if err != nil {
		t.Error(err)
	}
	if line := fmt.Sprintf(textNestedRepository, "sub", "acme/shop"); strings.Count(lines, line) != 1 {
		t.Errorf("no line for the repository: %q", lines)
	}
	if got := run(t, dir, "show", "HEAD:sub") + run(t, dir, "show", "HEAD:left.txt"); got != "oneleft" {
		t.Errorf("the commit has %q", got)
	}
	if _, err := os.Lstat(marker); err == nil {
		t.Error("the filter ran")
	}
}

// TestCommitLeftOfAGitlinkAlone: the push step commits a change of gitlinks alone, here one below
// .qory that a commit of the agent changed, and runs no program of the repository in its folder,
// also with ignore = none in .gitmodules, for which git status and git commit can start git there.
func TestCommitLeftOfAGitlinkAlone(t *testing.T) {
	dir, b := onBranch(t, "acme/shop")
	marker := filepath.Join(t.TempDir(), "ran")
	id, start := nestedRepo(t, dir, ".qory/sub", marker), strings.Repeat("12", 20)
	write(t, filepath.Join(dir, ".gitmodules"), "[submodule \"s\"]\n\tpath = .qory/sub\n\turl = ./s\n\tignore = none\n")
	run(t, dir, "add", ".gitmodules")
	run(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+start+",.qory/sub")
	run(t, dir, "commit", "--quiet", "-m", "start")
	r := b.Repositories[0]
	r.Commit = run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "update-index", "--cacheinfo", "160000,"+id+",.qory/sub")
	run(t, dir, "commit", "--quiet", "--allow-empty", "-m", "agent")
	touchNested(t, dir, ".qory/sub")
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	ctx := hostgit.WithBase(t.Context(), proxygit.Env(os.Environ(), nil))
	if err := commitLeft(ctx, dir, r, nil, "left", io.Discard); err != nil {
		t.Error(err)
	}
	if _, err := os.Lstat(marker); err == nil {
		t.Error("the filter ran")
	}
	if got := run(t, dir, "rev-parse", "HEAD:.qory/sub"); got != start {
		t.Errorf("the commit has %s, want %s", got, start)
	}
	// Control: git status runs the filter with ignore = none.
	run(t, dir, "status", "--ignore-submodules=none")
	if _, err := os.Lstat(marker); err != nil {
		t.Error("the filter never runs, so the test shows nothing")
	}
}

// pushTwice pushes dir twice, with the change that left makes before each push, and wants the
// paths of HEAD below the paths at, with their modes, and no line of the push step but lines.
func pushTwice(t *testing.T, dir string, b *WorkBranch, left func(), want, lines string, at ...string) {
	t.Helper()
	for n := range 2 {
		left()
		write(t, filepath.Join(dir, "left.txt"), strings.Repeat("left\n", n+1))
		p, got, err := pushOf(dir, b)
		if err != nil || p == nil || len(p.Repositories) != 1 {
			t.Fatalf("push %d: %+v, %v", n+1, p, err)
		}
		if head := run(t, dir, "rev-parse", "HEAD"); p.Repositories[0].Commit != head {
			t.Errorf("push %d pushed %s, want %s", n+1, p.Repositories[0].Commit, head)
		}
		var tree []string
		for line := range strings.Lines(run(t, dir, append([]string{"ls-tree", "-r", "HEAD"}, at...)...)) {
			mode, _, _ := strings.Cut(line, " ")
			_, path, _ := strings.Cut(strings.TrimSpace(line), "\t")
			tree = append(tree, mode+" "+path)
		}
		if got := strings.Join(tree, "\n"); got != want {
			t.Errorf("push %d: the commit has %q, want %q", n+1, got, want)
		}
		if before, _, _ := strings.Cut(got, "qory run: pushed "); before != lines {
			t.Errorf("push %d printed %q, want %q", n+1, got, lines)
		}
		if size := run(t, dir, "cat-file", "-s", "HEAD:left.txt"); size != fmt.Sprint(5*(n+1)) {
			t.Errorf("push %d: left.txt has %s bytes", n+1, size)
		}
	}
}

// withFiles is a checkout on the run's branch with the files, in a commit that a push before
// this one sent.
func withFiles(t *testing.T, files ...string) (string, *WorkBranch) {
	t.Helper()
	dir, b := onBranch(t, "acme/shop")
	for _, path := range files {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, path), path+"\n")
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "--quiet", "-m", "files")
	if _, _, err := pushOf(dir, b); err != nil {
		t.Fatal(err)
	}
	return dir, b
}

// replace removes path of dir, and puts there what put makes.
func replace(t *testing.T, dir, path string, put func(at string) error) {
	t.Helper()
	at := filepath.Join(dir, path)
	if err := os.RemoveAll(at); err != nil {
		t.Fatal(err)
	}
	if err := put(at); err != nil {
		t.Fatal(err)
	}
}

// TestPushCommitsAFolderThatIsNowAFile: the push step commits the file that took the place of a
// tracked folder, made by an editor or by git mv, left by the agent or in a commit of it.
func TestPushCommitsAFolderThatIsNowAFile(t *testing.T) {
	for name, left := range map[string]func(t *testing.T, dir string){
		"an editor": func(t *testing.T, dir string) {
			replace(t, dir, "d", func(at string) error { return os.WriteFile(at, []byte("d\n"), 0o644) })
		},
		"git mv": func(t *testing.T, dir string) {
			run(t, dir, "rm", "-r", "--quiet", "d")
			run(t, dir, "mv", "e/x", "d")
		},
		"a commit": func(t *testing.T, dir string) {
			run(t, dir, "rm", "-r", "--quiet", "d")
			run(t, dir, "mv", "e/x", "d")
			run(t, dir, "commit", "--quiet", "-m", "moved")
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, b := withFiles(t, "d/x", "d/deep/y", "e/x", "e/z")
			done := false
			want := "100644 d\n100644 e/z"
			if name == "an editor" {
				want = "100644 d\n100644 e/x\n100644 e/z"
			}
			pushTwice(t, dir, b, func() {
				if !done {
					left(t, dir)
				}
				done = true
			}, want, "", "d", "e")
		})
	}
}

// TestPushCommitsAFolderThatIsNowALink: the push step commits the link that took the place of a
// tracked folder, and no file behind it, also when git ignores the link, which stays out then. A
// link into .qory stays out, and so does the change of the paths behind it.
func TestPushCommitsAFolderThatIsNowALink(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "file"), "outside\n")
	// A repository outside the checkout is none of it.
	run(t, outside, "init", "--quiet", "x")
	for _, c := range []struct{ name, target, want string }{
		{"outside the checkout", outside, "120000 d\n100644 e/x"},
		{"in the checkout", "e", "120000 d\n100644 e/x"},
		{"to nothing", "missing", "120000 d\n100644 e/x"},
		{"ignored", "e", "100644 e/x"},
		{"into .qory", ".qory/somewhere", "100644 d/deep/y\n100644 d/x\n100644 e/x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b := withFiles(t, "d/x", "d/deep/y", "e/x")
			if c.name == "ignored" {
				write(t, filepath.Join(dir, ".git", "info", "exclude"), "/d\n")
			}
			pushTwice(t, dir, b, func() {
				replace(t, dir, "d", func(at string) error { return os.Symlink(c.target, at) })
			}, c.want, "", "d", "e")
		})
	}
}

// TestPushCommitsAFileThatIsNowAFolderWithARepository: the push step commits that a tracked file
// is gone, and the other files of the folder in its place, and leaves out a repository in the
// folder, which has no commit, with one line.
func TestPushCommitsAFileThatIsNowAFolderWithARepository(t *testing.T) {
	for _, c := range []struct{ name, file, want string }{
		{"with another file", "y", "100644 f/y"},
		{"alone", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b := withFiles(t, "f", "g")
			replace(t, dir, "f", func(at string) error { return os.MkdirAll(filepath.Join(at, "sub"), 0o755) })
			run(t, dir, "init", "--quiet", "f/sub")
			write(t, filepath.Join(dir, "f", "sub", "in"), "in\n")
			if c.file != "" {
				write(t, filepath.Join(dir, "f", c.file), "y\n")
			}
			pushTwice(t, dir, b, func() {}, c.want, fmt.Sprintf(textNestedRepository+"\n", "f/sub", "acme/shop"), "f")
		})
	}
}

// TestPushLeavesOutAFolderWithAGitEntry: the push step leaves out each file of a folder with a .git
// entry that git does not read as a repository, prints one line for the folder, and keeps what
// HEAD has at the folder's path.
func TestPushLeavesOutAFolderWithAGitEntry(t *testing.T) {
	gitFile := func(text func(at string) string) func(at string) error {
		return func(at string) error { return os.WriteFile(filepath.Join(at, ".git"), []byte(text(at)), 0o644) }
	}
	noGitdir := gitFile(func(string) string { return "no gitdir\n" })
	for _, c := range []struct {
		name, want string
		entry      func(at string) error
	}{
		{"a .git file with no gitdir line", "100644 sub", noGitdir},
		{"a gitdir to a missing path", "100644 sub", gitFile(func(at string) string { return "gitdir: " + filepath.Join(filepath.Dir(at), "missing") + "\n" })},
		{"an empty .git folder", "100644 sub", func(at string) error { return os.Mkdir(filepath.Join(at, ".git"), 0o755) }},
		{"a new folder with a .git file with no gitdir line", "", noGitdir},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := []string{"g"}
			if c.want != "" {
				files = append(files, "sub")
			}
			dir, b := withFiles(t, files...)
			replace(t, dir, "sub", func(at string) error {
				if err := os.Mkdir(at, 0o755); err != nil {
					return err
				}
				if err := c.entry(at); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(at, "f.txt"), []byte("f\n"), 0o644)
			})
			pushTwice(t, dir, b, func() {}, c.want, fmt.Sprintf(textNestedRepository+"\n", "sub", "acme/shop"), "sub")
		})
	}
}

// TestPushPrintsTheTopmostFolderWithAGitEntry: the push step prints one line for the topmost
// folder with a .git entry above a path, also for a gitlink or a repository below that folder,
// and reads no .git entry behind a link on the way.
func TestPushPrintsTheTopmostFolderWithAGitEntry(t *testing.T) {
	mkdir := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, want      string
		files, at, told []string
		left            func(t *testing.T, dir string)
	}{
		{name: "a gitlink below it", want: "160000 sub/inner", files: []string{"g"}, at: []string{"sub"}, told: []string{"sub"}, left: func(t *testing.T, dir string) {
			nestedRepo(t, dir, "sub/inner", filepath.Join(t.TempDir(), "ran"))
			run(t, dir, "add", "sub/inner")
			run(t, dir, "commit", "--quiet", "-m", "link")
			mkdir(t, filepath.Join(dir, "sub", ".git"))
			write(t, filepath.Join(dir, "sub", "n.txt"), "n\n")
		}},
		{name: "a repository two folders below it", files: []string{"g"}, at: []string{"a"}, told: []string{"a"}, left: func(t *testing.T, dir string) {
			mkdir(t, filepath.Join(dir, "a", ".git"))
			mkdir(t, filepath.Join(dir, "a", "b", ".git"))
			nestedRepo(t, dir, "a/b/c", filepath.Join(t.TempDir(), "ran"))
			write(t, filepath.Join(dir, "a", "x.txt"), "x\n")
		}},
		// git lists l/f.txt as deleted, and the link l, which leads to a .git entry, as new.
		{name: "a link on the way to it", files: []string{"l/f.txt"}, at: []string{"l", "real"}, told: []string{"l", "real"}, left: func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "real", ".git"), "no gitdir\n")
			write(t, filepath.Join(dir, "real", "f.txt"), "f\n")
			replace(t, dir, "l", func(at string) error { return os.Symlink("real", at) })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b := withFiles(t, c.files...)
			c.left(t, dir)
			var lines strings.Builder
			for _, folder := range c.told {
				fmt.Fprintf(&lines, textNestedRepository+"\n", folder, "acme/shop")
			}
			pushTwice(t, dir, b, func() {}, c.want, lines.String(), c.at...)
		})
	}
}

// TestPushCommitsAFolderThatTheSetupChanged: a path of the folder that the setup changed counts as
// the agent's change when the agent put a file or a link in the folder's place, and stays out when
// the setup did.
func TestPushCommitsAFolderThatTheSetupChanged(t *testing.T) {
	file := func(at string) error { return os.WriteFile(at, []byte("d\n"), 0o644) }
	link := func(at string) error { return os.Symlink("e", at) }
	for _, c := range []struct {
		name         string
		setup, agent func(at string) error
		want         string
	}{
		{"the agent, a file", nil, file, "100644 d\n100644 e/x"},
		{"the agent, a link", nil, link, "120000 d\n100644 e/x"},
		{"the setup, a file", file, nil, "100644 d/x\n100644 e/x"},
		{"the setup, a link", link, nil, "100644 d/x\n100644 e/x"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b := withFiles(t, "d/x", "e/x")
			b.Repositories[0].Commit = run(t, dir, "rev-parse", "HEAD")
			if c.setup != nil {
				replace(t, dir, "d", c.setup)
			} else {
				write(t, filepath.Join(dir, "d", "x"), "the setup\n")
			}
			recordSetup(t, dir, b)
			pushTwice(t, dir, b, func() {
				if c.agent != nil {
					replace(t, dir, "d", c.agent)
				}
			}, c.want, "", "d", "e")
		})
	}
}

// TestResetPathsWithAFileNamedAsTheRevision: the push step takes a new gitlink out of the index
// again in a repository with a file HEAD, which git reset would not tell from the revision.
func TestResetPathsWithAFileNamedAsTheRevision(t *testing.T) {
	dir, _ := withFiles(t, "HEAD", "sub")
	if err := resetPaths(hostgit.WithBase(t.Context(), proxygit.Env(os.Environ(), nil)), dir, "HEAD", []string{"sub"}); err != nil {
		t.Error(err)
	}
}

// TestPushCommitsAChangeOfGitlinksAlone: the push step commits a change of gitlinks alone, here
// the one that puts back the gitlink below .qory that a commit of the agent changed. ignore = none
// in .gitmodules makes git diff show the change. git 2.37 and 2.39 commit it only with
// --allow-empty.
func TestPushCommitsAChangeOfGitlinksAlone(t *testing.T) {
	dir, b, base := onBranchIn(t, "acme/shop")
	start, other := strings.Repeat("12", 20), strings.Repeat("34", 20)
	write(t, filepath.Join(dir, ".gitmodules"), "[submodule \"s\"]\n\tpath = .qory/sub\n\turl = ./s\n\tignore = none\n")
	run(t, dir, "add", ".gitmodules")
	run(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+start+",.qory/sub")
	run(t, dir, "commit", "--quiet", "-m", "start")
	// The forge has the start commit, as after an earlier run's push.
	run(t, dir, "push", "--quiet", filepath.Join(base, "acme", "shop.git"), "HEAD:refs/heads/"+strings.TrimPrefix(b.Ref, "refs/heads/"))
	b.Repositories[0].Commit = run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "update-index", "--cacheinfo", "160000,"+other+",.qory/sub")
	run(t, dir, "commit", "--quiet", "-m", "agent")
	agent := run(t, dir, "rev-parse", "HEAD")
	p, lines, err := pushOf(dir, b)
	if err != nil || p == nil || len(p.Repositories) != 1 {
		t.Fatalf("pushed %+v, printed %q, error %v", p, lines, err)
	}
	if head := run(t, dir, "rev-parse", "HEAD"); p.Repositories[0].Commit != head || run(t, dir, "rev-parse", "HEAD^") != agent {
		t.Errorf("pushed %s, want a commit on %s at %s", p.Repositories[0].Commit, agent, head)
	}
	if got := run(t, dir, "rev-parse", "HEAD:.qory/sub"); got != start {
		t.Errorf("the commit has %s, want %s", got, start)
	}
}

// TestPushCommitsANewFileWithAGitlinkOutsideItsPaths: a gitlink at a path that no path of the add
// reaches stays as HEAD has it, and a new file in a folder goes into the commit. An exclusion of
// the gitlink would leave the file out.
func TestPushCommitsANewFileWithAGitlinkOutsideItsPaths(t *testing.T) {
	dir, b := withFiles(t, "deep/er/f")
	head := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",x")
	run(t, dir, "commit", "--quiet", "-m", "gitlink")
	write(t, filepath.Join(dir, "deep", "er", "new"), "new\n")
	p, lines, err := pushOf(dir, b)
	if err != nil || p == nil || len(p.Repositories) != 1 {
		t.Fatalf("pushed %+v, printed %q, error %v", p, lines, err)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); p.Repositories[0].Commit != got {
		t.Errorf("pushed %s, want %s", p.Repositories[0].Commit, got)
	}
	if got := run(t, dir, "ls-tree", "-r", "--name-only", "HEAD", "deep"); got != "deep/er/f\ndeep/er/new" {
		t.Errorf("the commit has %q below deep", got)
	}
	if got := run(t, dir, "rev-parse", "HEAD:x"); got != head {
		t.Errorf("the gitlink x is at %s, want %s", got, head)
	}
}

// TestIsReplacedOfAnUntrackedFolder: a folder that git status lists with a slash took the place of
// no tracked file, and a link above a path did.
func TestIsReplacedOfAnUntrackedFolder(t *testing.T) {
	dir, _ := withFiles(t, "f/x")
	if err := os.Symlink("f", filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{"f/": false, "l/x": true, "f/x": false, "l/x/y": true, "missing/y": false} {
		if got, stays, err := isReplaced(dir, path, nil); err != nil || got != want || stays {
			t.Errorf("%s: %t, %t, %v", path, got, stays, err)
		}
	}
}

// TestPushCommitsATrackedFileBelowAnIgnoredFolder: the push step commits the change and the
// deletion of a tracked file below a folder that .gitignore lists, also with a gitlink beside it,
// whose repository runs no git. A new file in that folder stays out, and the push goes on.
func TestPushCommitsATrackedFileBelowAnIgnoredFolder(t *testing.T) {
	changed := func(t *testing.T, dir string) { write(t, filepath.Join(dir, "vendor", "tracked.txt"), "changed\n") }
	for _, c := range []struct {
		name, want string
		gitlink    bool
		left       func(t *testing.T, dir string)
	}{
		{"changed", "100644 vendor/other.txt\n100644 vendor/tracked.txt", false, changed},
		{"deleted", "100644 vendor/other.txt", false, func(t *testing.T, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, "vendor", "tracked.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"a new file", "100644 vendor/other.txt\n100644 vendor/tracked.txt", false, func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, "vendor", "new.txt"), "new\n")
		}},
		{"changed, with a gitlink beside it", "100644 vendor/other.txt\n160000 vendor/sub\n100644 vendor/tracked.txt", true, changed},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, b := withFiles(t, "vendor/tracked.txt", "vendor/other.txt")
			write(t, filepath.Join(dir, ".gitignore"), "vendor/\n")
			marker := filepath.Join(t.TempDir(), "ran")
			lines := ""
			if c.gitlink {
				nestedRepo(t, dir, "vendor/sub", marker)
				lines = fmt.Sprintf(textNestedRepository+"\n", "vendor/sub", "acme/shop")
			}
			run(t, dir, "add", "-A", "-f")
			run(t, dir, "commit", "--quiet", "-m", "ignore")
			if c.gitlink {
				touchNested(t, dir, "vendor/sub")
			}
			pushTwice(t, dir, b, func() { c.left(t, dir) }, c.want, lines, "vendor")
			if strings.Contains(c.want, "tracked.txt") {
				want := "vendor/tracked.txt"
				if strings.HasPrefix(c.name, "changed") {
					want = "changed"
				}
				if got := run(t, dir, "show", "HEAD:vendor/tracked.txt"); got != want {
					t.Errorf("vendor/tracked.txt has %q, want %q", got, want)
				}
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("a git ran in vendor/sub")
			}
		})
	}
}
