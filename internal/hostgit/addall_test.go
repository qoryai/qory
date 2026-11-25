package hostgit_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
)

// addAll is [hostgit.AddAll] in the repository at dir, with a plain git of [hostgit.Unchecked].
func addAll(dir string, paths ...string) ([]string, error) {
	return hostgit.AddAll(context.Background(), maker, dir, nil, paths)
}

// maker is the [hostgit.Maker] of [addAll].
func maker(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
	cmd, err := hostgit.Unchecked(ctx, dir, os.Environ(), args...)
	if err == nil {
		cmd.Env = append(cmd.Env, env...)
	}
	return cmd, err
}

// write writes content to the file path below dir, with its folders.
func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repo is a repository with the files, committed, and returns its folder and the commit.
func repo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	hermetic(t)
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(name, "t@example.com")
	}
	dir := t.TempDir()
	git(t, dir, "init", "--quiet")
	for path, content := range files {
		write(t, dir, path, content)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "--allow-empty", "-m", "first")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

// filterRepo makes a repository in the folder sub of the checkout at dir, with the file f
// committed, a changed time of f, and a configuration and a .gitattributes that run a clean
// filter for f: a git status in sub runs it. The filter creates the file ran in a folder outside
// the checkout, which the function returns, with the repository's commit.
func filterRepo(t *testing.T, dir, sub string) (ran, commit string) {
	t.Helper()
	ran = filepath.Join(t.TempDir(), "ran")
	full := filepath.Join(dir, filepath.FromSlash(sub))
	write(t, full, "f", "f\n")
	git(t, full, "init", "--quiet")
	git(t, full, "add", "-A")
	git(t, full, "commit", "--quiet", "-m", "nested")
	commit = git(t, full, "rev-parse", "HEAD")
	git(t, full, "config", "filter.x.clean", "touch "+ran+"; cat")
	write(t, full, ".gitattributes", "* filter=x\n")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(full, "f"), later, later); err != nil {
		t.Fatal(err)
	}
	return ran, commit
}

// notRan fails the test when the filter of [filterRepo] ran.
func notRan(t *testing.T, marker string) {
	t.Helper()
	if ran(marker) {
		t.Error("the filter of the repository in the checkout ran")
	}
}

// indexInfo adds entries, as git update-index --index-info -z reads them, to the index at dir.
func indexInfo(t *testing.T, dir, entries string) {
	t.Helper()
	cmd := exec.Command("git", "update-index", "-z", "--index-info")
	cmd.Dir, cmd.Stdin = dir, strings.NewReader(entries)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-index: %v\n%s", err, out)
	}
}

// TestAddAllLeavesOutTheSubmoduleEntriesAlone: with submodule entries whose paths have a tab, a
// line feed, a colon at the start, or pattern characters, that git ignores, and that are not
// there, the add runs no git in them, adds every other path from the top and below, and keeps
// the entries.
func TestAddAllLeavesOutTheSubmoduleEntriesAlone(t *testing.T) {
	dir, head := repo(t, map[string]string{"a.txt": "a\n", "sub/a.txt": "a\n", ".gitignore": "/ignored/\n"})
	subs := []string{"t\tab", "line\nfeed", ":(top)colon", "*", "deep/er/[a-z]", "ignored", "missing"}
	var entries strings.Builder
	var markers []string
	ids := map[string]string{"missing": head}
	for _, sub := range subs {
		if sub != "missing" {
			r, commit := filterRepo(t, dir, sub)
			markers, ids[sub] = append(markers, r), commit
		}
		fmt.Fprintf(&entries, "160000 %s\t%s\x00", ids[sub], sub)
	}
	indexInfo(t, dir, entries.String())
	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "sub/a.txt", "changed\n")
	write(t, dir, "deep/er/new.txt", "new\n")
	write(t, dir, "new.txt", "new\n")
	added, err := addAll(dir)
	if err != nil || len(added) != 0 {
		t.Fatalf("new entries %q, error %v", added, err)
	}
	for _, r := range markers {
		notRan(t, r)
	}
	staged := git(t, dir, "-c", "core.quotePath=false", "ls-files", "-z", "--stage")
	for _, sub := range subs {
		if !strings.Contains(staged, "160000 "+ids[sub]+" 0\t"+sub+"\x00") {
			t.Errorf("the index has no submodule entry %q", sub)
		}
	}
	if left := git(t, dir, "diff-files", "--name-only", "--ignore-submodules=all"); left != "" {
		t.Errorf("not added: %s", left)
	}
	if left := git(t, dir, "ls-files", "--others", "--exclude-standard"); left != "" {
		t.Errorf("not added: %s", left)
	}
}

// TestAddAllReturnsANewSubmoduleEntry: each repository that the add makes a submodule entry of
// the index is returned once, with a .git folder, with a .git file, and in place of a tracked
// file, and the next add with that index leaves the entries out: no filter runs.
func TestAddAllReturnsANewSubmoduleEntry(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.txt": "a\n", "tracked": "t\n"})
	if err := os.Remove(filepath.Join(dir, "tracked")); err != nil {
		t.Fatal(err)
	}
	want := []string{"deep/t\tab", "evil", "tracked", "zfile"}
	var markers []string
	for _, sub := range want {
		r, _ := filterRepo(t, dir, sub)
		markers = append(markers, r)
	}
	outside := filepath.Join(t.TempDir(), "git")
	if err := os.Rename(filepath.Join(dir, "zfile", ".git"), outside); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "zfile/.git", "gitdir: "+outside+"\n")
	if added, err := addAll(dir); err != nil || !slices.Equal(added, want) {
		t.Errorf("new entries %q, error %v, want %q", added, err, want)
	}
	if added, err := addAll(dir); err != nil || len(added) != 0 {
		t.Errorf("the second add: new entries %q, error %v", added, err)
	}
	for _, r := range markers {
		notRan(t, r)
	}
}

// TestAddAllRunsNoGitInASubmodulesFolderInAnyLayout: the filter of the repository in the folder
// of a submodule entry of HEAD does not run, with a .git file in place of the folder, with a
// .gitmodules that sets ignore, and with a HEAD that has a file and a folder of the same name,
// where the folder has the entry, in either order, for an add of everything and of one path.
func TestAddAllRunsNoGitInASubmodulesFolderInAnyLayout(t *testing.T) {
	for _, c := range []struct {
		name, at, path string
		make           func(t *testing.T, dir string)
	}{
		{"a .git folder", "sub", "a.txt", func(*testing.T, string) {}},
		{"a .git file", "sub", "a.txt", func(t *testing.T, dir string) {
			outside := filepath.Join(t.TempDir(), "git")
			if err := os.Rename(filepath.Join(dir, "sub", ".git"), outside); err != nil {
				t.Fatal(err)
			}
			write(t, dir, "sub/.git", "gitdir: "+outside+"\n")
		}},
		{"a .gitmodules", "sub", ".gitmodules", func(t *testing.T, dir string) {
			write(t, dir, ".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = ./sub\n\tignore = none\n")
		}},
		{"a file and a folder d", "d/sub", "d", func(t *testing.T, dir string) { fileAndFolder(t, dir, false) }},
		{"a folder d and a file", "d/sub", "d", func(t *testing.T, dir string) { fileAndFolder(t, dir, true) }},
	} {
		for _, paths := range [][]string{nil, {c.path}} {
			t.Run(fmt.Sprintf("%s %q", c.name, paths), func(t *testing.T) {
				dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
				marker, commit := filterRepo(t, dir, c.at)
				git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+commit+","+c.at)
				git(t, dir, "commit", "--quiet", "-m", "a submodule")
				c.make(t, dir)
				git(t, dir, "-c", "diff.ignoreSubmodules=all", "read-tree", "HEAD")
				if _, err := addAll(dir, paths...); err != nil {
					t.Error(err)
				}
				notRan(t, marker)
			})
		}
	}
}

// fileAndFolder commits a tree in the repository at dir, whose HEAD has the submodule entry d/sub,
// that has a file d and HEAD's folder d, in that order, or with the folder first.
func fileAndFolder(t *testing.T, dir string, folderFirst bool) {
	t.Helper()
	raw := func(id string) []byte {
		data, err := hex.DecodeString(id)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	file := append([]byte("100644 d\x00"), raw(git(t, dir, "rev-parse", "HEAD:a.txt"))...)
	folder := append([]byte("40000 d\x00"), raw(git(t, dir, "rev-parse", "HEAD:d"))...)
	body := slices.Concat(file, folder)
	if folderFirst {
		body = slices.Concat(folder, file)
	}
	cmd := exec.Command("git", "hash-object", "-t", "tree", "-w", "--literally", "--stdin")
	cmd.Dir, cmd.Stdin = dir, bytes.NewReader(body)
	tree, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "update-ref", "HEAD", git(t, dir, "commit-tree", "-m", "d twice", strings.TrimSpace(string(tree))))
}

// TestAddAllLeavesOutManySubmoduleEntries: with more submodule entries than a command line takes,
// the add leaves each out, the first and the last with a filter too, and adds the rest.
func TestAddAllLeavesOutManySubmoduleEntries(t *testing.T) {
	dir, head := repo(t, map[string]string{"a.txt": "a\n"})
	long := strings.Repeat("n", 230)
	first, firstID := filterRepo(t, dir, long+"/0")
	last, lastID := filterRepo(t, dir, long+"/8999")
	var entries strings.Builder
	for i := range 9000 {
		id := map[int]string{0: firstID, 8999: lastID}[i]
		if id == "" {
			id = head
		}
		fmt.Fprintf(&entries, "160000 %s\t%s/%d\x00", id, long, i)
	}
	indexInfo(t, dir, entries.String())
	write(t, dir, "new.txt", "new\n")
	if added, err := addAll(dir); err != nil || len(added) != 0 {
		t.Errorf("new entries %d, error %v", len(added), err)
	}
	notRan(t, first)
	notRan(t, last)
	if got := git(t, dir, "ls-files", "new.txt"); got != "new.txt" {
		t.Error("the add added no new.txt")
	}
}

// TestAddAllLeavesOutASubmoduleEntryBehindALink: with a link above the path of a submodule entry,
// whose names have pattern characters, the add passes, adds the link and removes the entry. It
// runs no git in the entry's folder also when that takes the link's place before git add, which
// then makes a new entry. An add of another path keeps the entry.
func TestAddAllLeavesOutASubmoduleEntryBehindALink(t *testing.T) {
	for _, c := range []struct {
		back  bool
		paths []string
	}{{false, nil}, {true, nil}, {false, []string{"re*l", "other/s[u]\\b"}}, {true, []string{"re*l", "a.txt"}}, {false, []string{"a.txt"}}} {
		back, paths := c.back, c.paths
		dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
		marker, commit := filterRepo(t, dir, "re*l/s[u]\\b")
		indexInfo(t, dir, "160000 "+commit+"\tre*l/s[u]\\b\x00")
		real := filepath.Join(dir, "re*l")
		if err := os.Rename(real, filepath.Join(dir, "other")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("other", real); err != nil {
			t.Fatal(err)
		}
		swapped := false
		command := func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
			// Once, before the first git add: AddAll runs two for a tracked path and a new one.
			if back && args[0] == "add" && !swapped {
				swapped = true
				if err := os.Remove(real); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(dir, "other"), real); err != nil {
					t.Fatal(err)
				}
			}
			return maker(ctx, dir, env, args...)
		}
		added, err := hostgit.AddAll(context.Background(), command, dir, nil, paths)
		if err != nil {
			t.Errorf("a folder in the link's place %v: %v", back, err)
		}
		notRan(t, marker)
		staged := git(t, dir, "-c", "core.quotePath=false", "ls-files", "-z", "--stage", "re*l", "other")
		if slices.Equal(paths, []string{"a.txt"}) {
			if len(added) != 0 || !strings.Contains(staged, "160000 "+commit+" 0\tre*l/s[u]\\b") {
				t.Errorf("an add of another path: new entries %q, and the index has %q", added, staged)
			}
			continue
		}
		if back && !slices.Equal(added, []string{"re*l/s[u]\\b"}) {
			t.Errorf("new entries %q, and the index has %q", added, staged)
		}
		if !back && (!slices.Equal(added, []string{"other/s[u]\\b"}) || strings.Contains(staged, "\tre*l/") || !strings.Contains(staged, "120000 ")) {
			t.Errorf("new entries %q, and the index has %q", added, staged)
		}
	}
}

// TestAddAllOfPathsAddsThemAlone: paths with pattern characters, with a tab, and at a colon are
// added as they are written, and no other path; a path of a submodule entry alone adds nothing;
// and an add in a repository with nothing but the submodule entry passes.
func TestAddAllOfPathsAddsThemAlone(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	marker, commit := filterRepo(t, dir, "sub")
	indexInfo(t, dir, "160000 "+commit+"\tsub\x00")
	for _, name := range []string{"*", "t\tab", ":(top)x", "x", "y"} {
		write(t, dir, name, "new\n")
	}
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if added, err := addAll(dir, "sub"); err != nil || len(added) != 0 {
		t.Errorf("new entries %q, error %v", added, err)
	}
	if got := git(t, dir, "ls-files", "-z"); got != "a.txt\x00sub\x00" {
		t.Errorf("after an add of sub alone, the index has %q", got)
	}
	if added, err := addAll(dir, "*", "t\tab", ":(top)x", "a.txt", "sub"); err != nil || len(added) != 0 {
		t.Errorf("new entries %q, error %v", added, err)
	}
	if got := git(t, dir, "ls-files", "-z"); got != "*\x00:(top)x\x00sub\x00t\tab\x00" {
		t.Errorf("the index has %q", got)
	}
	notRan(t, marker)

	empty, _ := repo(t, nil)
	marker, commit = filterRepo(t, empty, "sub")
	indexInfo(t, empty, "160000 "+commit+"\tsub\x00")
	if added, err := addAll(empty); err != nil || len(added) != 0 {
		t.Errorf("new entries %q, error %v", added, err)
	}
	notRan(t, marker)
}

// TestAddAllFailsAndAddsNothingWithoutItsExclusions: a git that fails to list the index or that
// reads each pathspec as a plain path is an error, and no filter runs.
func TestAddAllFailsAndAddsNothingWithoutItsExclusions(t *testing.T) {
	for _, c := range []struct {
		name    string
		command hostgit.Maker
		paths   []string
	}{
		{"no list", func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
			if args[0] == "ls-files" {
				args = []string{"ls-files", "--no-such-option"}
			}
			return maker(ctx, dir, env, args...)
		}, nil},
		{"no git", func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
			if args[0] == "ls-files" {
				return nil, errors.New("no git")
			}
			return maker(ctx, dir, env, args...)
		}, nil},
		{"literal pathspecs", func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
			return maker(ctx, dir, append(env, "GIT_LITERAL_PATHSPECS=1"), args...)
		}, nil},
		{"literal pathspecs of a path", func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
			return maker(ctx, dir, append(env, "GIT_LITERAL_PATHSPECS=1"), args...)
		}, []string{"new.txt"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
			marker, commit := filterRepo(t, dir, "sub")
			indexInfo(t, dir, "160000 "+commit+"\tsub\x00")
			write(t, dir, "new.txt", "new\n")
			if added, err := hostgit.AddAll(context.Background(), c.command, dir, nil, c.paths); err == nil {
				t.Errorf("the add passed with new entries %q", added)
			}
			notRan(t, marker)
			if got := git(t, dir, "ls-files", "new.txt"); got != "" {
				t.Errorf("the add added %s", got)
			}
		})
	}
}

// TestAddAllsGitsHaveNoProtocol: each git of the add has the variables of a git with no protocol,
// also with a command that drops git's variables or allows a protocol after them, and the add
// uses the index that env names.
func TestAddAllsGitsHaveNoProtocol(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	write(t, dir, "new.txt", "new\n")
	index := filepath.Join(t.TempDir(), "index")
	var made []*exec.Cmd
	command := func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
		cmd, err := maker(ctx, dir, env, args...)
		if err != nil {
			return nil, err
		}
		cmd.Env = slices.DeleteFunc(cmd.Env, func(kv string) bool {
			return strings.HasPrefix(kv, "GIT_") && !slices.Contains(env, kv)
		})
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ALLOW_PROTOCOL=file", "GIT_NO_LAZY_FETCH=0")
		made = append(made, cmd)
		return cmd, nil
	}
	if added, err := hostgit.AddAll(context.Background(), command, dir, []string{"GIT_INDEX_FILE=" + index}, nil); err != nil || len(added) != 0 {
		t.Errorf("new entries %q, error %v", added, err)
	}
	if len(made) != 3 {
		t.Errorf("%d gits, want 3", len(made))
	}
	for _, cmd := range made {
		last := map[string]string{}
		for _, kv := range cmd.Env {
			name, _, _ := strings.Cut(kv, "=")
			last[name] = kv
		}
		if !hostgit.NoProtocol(last["GIT_ALLOW_PROTOCOL"]) || last["GIT_NO_LAZY_FETCH"] != "GIT_NO_LAZY_FETCH=1" {
			t.Errorf("git %s has a protocol", cmd.Args[1:])
		}
	}
	if got := git(t, dir, "ls-files"); got != "a.txt" {
		t.Errorf("the repository's index has %q", got)
	}
	t.Setenv("GIT_INDEX_FILE", index)
	if got := git(t, dir, "ls-files"); got != "a.txt\nnew.txt" {
		t.Errorf("the index of the add has %q", got)
	}
}

// TestAddAllLosesNoFileToAnExclusion: with submodule entries of each length beside a folder and
// above it, the add of paths that are all in the folder adds each of them, and so does the add of
// the whole repository.
func TestAddAllLosesNoFileToAnExclusion(t *testing.T) {
	var subs []string
	for n := 1; n <= 12; n++ {
		subs = append(subs, strings.Repeat("x", n), "deep/"+strings.Repeat("y", n), "deep/er/"+strings.Repeat("z", n)+"/sub")
	}
	for _, c := range []struct{ paths, want []string }{
		{[]string{"deep/er/new", "deep/er/other"}, nil},
		{[]string{"deep/er/new", "deep/er/zz"}, []string{"deep/er/new", "deep/er/zz/file"}},
		{[]string{"deep/er/new"}, nil},
		{[]string{"deep/er/"}, []string{"deep/er/f", "deep/er/new", "deep/er/other", "deep/er/zz/file"}},
		{nil, []string{"deep/er/f", "deep/er/new", "deep/er/other", "deep/er/zz/file", "new"}},
	} {
		dir, head := repo(t, map[string]string{"deep/er/f": "f\n"})
		var entries strings.Builder
		for _, sub := range subs {
			entries.WriteString("160000 " + head + "\t" + sub + "\x00")
		}
		indexInfo(t, dir, entries.String())
		for _, name := range []string{"deep/er/new", "deep/er/other", "deep/er/zz/file", "new"} {
			write(t, dir, name, "new\n")
		}
		write(t, dir, "deep/er/f", "changed\n")
		if added, err := addAll(dir, c.paths...); err != nil || len(added) != 0 {
			t.Errorf("%q: new entries %q, error %v", c.paths, added, err)
		}
		want := c.want
		if want == nil {
			want = c.paths
		}
		if got := strings.Fields(git(t, dir, "diff", "--cached", "--name-only", "--ignore-submodules=all", "HEAD")); !slices.Equal(got, want) {
			t.Errorf("%q: the add has %q, want %q", c.paths, got, want)
		}
		if got := strings.Count(git(t, dir, "ls-files", "--stage"), "160000 "+head); got != len(subs) {
			t.Errorf("%q: %d submodule entries, want %d", c.paths, got, len(subs))
		}
	}
}

// TestAddAllLeavesOutASubmoduleEntryBelowAnIgnoredFolder: a submodule entry below a folder that
// git ignores stops no add of the whole repository, whose tracked files below that folder are
// added.
func TestAddAllLeavesOutASubmoduleEntryBelowAnIgnoredFolder(t *testing.T) {
	for _, sub := range []string{"vendor/lib", "vendor/deep/l*b", "lib"} {
		dir, _ := repo(t, map[string]string{"a": "a\n", "vendor/t": "t\n"})
		write(t, dir, ".gitignore", "vendor/\nlib\n")
		marker, commit := filterRepo(t, dir, sub)
		indexInfo(t, dir, "160000 "+commit+"\t"+sub+"\x00")
		for _, name := range []string{"a", "new", "vendor/t", "vendor/new"} {
			write(t, dir, name, "new\n")
		}
		if added, err := addAll(dir); err != nil || len(added) != 0 {
			t.Errorf("%s: new entries %q, error %v", sub, added, err)
		}
		notRan(t, marker)
		if got := git(t, dir, "diff", "--cached", "--name-only", "--ignore-submodules=all", "HEAD"); got != ".gitignore\na\nnew\nvendor/t" {
			t.Errorf("%s: the add has %q", sub, got)
		}
		if got := git(t, dir, "ls-files", "--stage"); strings.Count(got, "160000 "+commit) != 1 {
			t.Errorf("%s: the index has %q", sub, got)
		}
	}
}

// TestAddAllLeavesOutASubmoduleEntryByItsNameAlone: the exclusion of a submodule entry with a
// byte that means something in a pathspec leaves out that entry and no file whose name the
// bytes would match as a pattern, at the top and in a folder, in an add of the whole repository
// and of the folder. git add with the name as a literal exclusion makes the same index.
func TestAddAllLeavesOutASubmoduleEntryByItsNameAlone(t *testing.T) {
	for _, c := range []struct{ sub, file string }{
		{"-dash", "dash"},
		{":(top)colon", "colon"},
		{":!colon", "colon"},
		{"!bang", "bang"},
		{"^caret", "caret"},
		{"#hash", "hash"},
		{`back\slash`, "backslash"},
		{`\lead`, "lead"},
		{`tail\`, "tail"},
		{`two\\`, `two\`},
		{"st*r", "star"},
		{"*", "any"},
		{"**", "any"},
		{"qu?st", "quest"},
		{"?", "q"},
		{"br[a]ck", "brack"},
		{"[b]", "b"},
		{"[", "open"},
		{"a[!b]", "ac"},
		{" sp ace ", "sp ace"},
		{"line\nfeed", "line"},
		{"\nlead", "lead"},
		{"t\tab", "tab"},
		{"é", "e"},
		{"{a,b}", "a"},
	} {
		t.Run(c.sub, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"deep/a": "a\n"})
			var markers []string
			for _, at := range []string{"", "deep/"} {
				marker, commit := filterRepo(t, dir, at+c.sub)
				markers = append(markers, marker)
				indexInfo(t, dir, "160000 "+commit+"\t"+at+c.sub+"\x00")
				write(t, dir, at+c.file, "new\n")
				write(t, dir, at+c.sub+"2", "new\n")
			}
			write(t, dir, "deep/a", "changed\n")
			for _, paths := range [][]string{nil, {"deep/"}} {
				index := []string{"GIT_INDEX_FILE=" + filepath.Join(t.TempDir(), "index")}
				data, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
				if err != nil {
					t.Fatal(err)
				}
				var list []string
				for _, literal := range []bool{true, false} {
					file := strings.TrimPrefix(index[0], "GIT_INDEX_FILE=")
					if err := os.WriteFile(file, data, 0o644); err != nil {
						t.Fatal(err)
					}
					if literal {
						specs := ":(top)\x00"
						if paths != nil {
							specs = ":(top,literal)deep\x00"
						} else {
							specs += ":(exclude,top,literal)" + c.sub + "\x00"
						}
						cmd := exec.Command("git", "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")
						cmd.Dir, cmd.Env = dir, append(os.Environ(), index...)
						cmd.Stdin = strings.NewReader(specs + ":(exclude,top,literal)deep/" + c.sub + "\x00")
						if out, err := cmd.CombinedOutput(); err != nil {
							t.Fatalf("git add: %v\n%s", err, out)
						}
					} else if added, err := hostgit.AddAll(context.Background(), maker, dir, index, paths); err != nil || len(added) != 0 {
						t.Errorf("%q: new entries %q, error %v", paths, added, err)
					}
					cmd := exec.Command("git", "ls-files", "-z", "--stage")
					cmd.Dir, cmd.Env = dir, append(os.Environ(), index...)
					out, err := cmd.Output()
					if err != nil {
						t.Fatal(err)
					}
					list = append(list, string(out))
				}
				if list[0] != list[1] {
					t.Errorf("%q: the index has %q, want %q", paths, list[1], list[0])
				}
				for _, name := range []string{c.file, c.sub + "2"} {
					if !strings.Contains(list[1], "\tdeep/"+name+"\x00") {
						t.Errorf("%q: the index has no %q: %q", paths, name, list[1])
					}
				}
			}
			for _, marker := range markers {
				notRan(t, marker)
			}
		})
	}
}

// TestAddAllStagesATrackedFileBelowAnIgnoredFolder: the change and the deletion of tracked files
// below a folder that .gitignore lists go to the index, alone, with a new file, and in an add of
// the whole repository. A new file below that folder stays out.
func TestAddAllStagesATrackedFileBelowAnIgnoredFolder(t *testing.T) {
	for _, c := range []struct {
		name  string
		paths []string
		want  string
	}{
		{"tracked paths alone", []string{"vendor/changed.txt", "vendor/deleted.txt"}, "M\tvendor/changed.txt\nD\tvendor/deleted.txt"},
		{"with a new file", []string{"vendor/changed.txt", "new.txt", "vendor/deleted.txt"}, "A\tnew.txt\nM\tvendor/changed.txt\nD\tvendor/deleted.txt"},
		{"the whole repository", nil, "A\tnew.txt\nM\tvendor/changed.txt\nD\tvendor/deleted.txt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{".gitignore": "vendor/\n"})
			write(t, dir, "vendor/changed.txt", "first\n")
			write(t, dir, "vendor/deleted.txt", "first\n")
			git(t, dir, "add", "-f", "vendor")
			git(t, dir, "commit", "--quiet", "-m", "vendor")
			write(t, dir, "vendor/changed.txt", "changed\n")
			if err := os.Remove(filepath.Join(dir, "vendor", "deleted.txt")); err != nil {
				t.Fatal(err)
			}
			write(t, dir, "vendor/new.txt", "new\n")
			write(t, dir, "new.txt", "new\n")
			if _, err := addAll(dir, c.paths...); err != nil {
				t.Fatal(err)
			}
			if got := git(t, dir, "diff", "--cached", "--name-status"); got != c.want {
				t.Errorf("the index has %q, want %q", got, c.want)
			}
		})
	}
}

// TestAddAllStopsOnTheErrorOfEachAdd: an add that fails, here on a locked index, stops AddAll with
// its error, for a tracked path, a new one, and both.
func TestAddAllStopsOnTheErrorOfEachAdd(t *testing.T) {
	for name, paths := range map[string][]string{
		"tracked": {"f.txt"},
		"new":     {"new.txt"},
		"both":    {"f.txt", "new.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"f.txt": "f\n"})
			write(t, dir, "f.txt", "changed\n")
			write(t, dir, "new.txt", "new\n")
			write(t, dir, ".git/index.lock", "")
			_, err := addAll(dir, paths...)
			var ae *hostgit.AddError
			if !errors.As(err, &ae) || ae.Subcommand != "add" {
				t.Errorf("AddAll: %v, want the error of git add", err)
			}
		})
	}
}
