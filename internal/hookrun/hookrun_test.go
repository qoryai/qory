package hookrun_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/qoryai/qory/internal/hookrun"
)

// git runs git in dir with a configuration of the test's alone, and returns its output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// write writes content to the file path below dir, with its folders.
func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// read is the file at path.
func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// repo is a repository with the files, committed, and returns its folder and the commit.
func repo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--quiet")
	for path, content := range files {
		write(t, dir, path, content)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "--allow-empty", "-m", "first")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

// TestHeadFailsOnACommondirAndARefNameThatGitDoesNotAccept: Head is the commit of HEAD, and an
// error with a .git/commondir file, which git follows to another repository, or with HEAD at a
// ref name that git does not accept.
func TestHeadFailsOnACommondirAndARefNameThatGitDoesNotAccept(t *testing.T) {
	dir, commit := repo(t, map[string]string{"a.txt": "a\n"})
	if head, err := hookrun.Head(dir); err != nil || head != commit {
		t.Fatalf("Head = %q, %v, want %s", head, err, commit)
	}
	branch := strings.TrimPrefix(git(t, dir, "symbolic-ref", "HEAD"), "refs/heads/")
	// A path that ends with a slash or has two is the branch's file; each other ref has a file
	// with the commit, so only the name makes Head fail.
	for _, ref := range []string{
		"refs/heads/" + branch + "/", "refs/heads//" + branch, "refs/heads/." + branch,
		"refs/heads/" + branch + ".lock", "refs/heads/" + branch + ".", "refs/heads/a b",
		"refs/heads/a@{1}", "refs/heads/a~1", "refs/heads/a:b", "refs/heads/a\\b",
		"refs/heads/a..b", "refs/heads/a^b", "refs/heads/a?b", "refs/heads/a*b", "refs/heads/a[b",
		"refs/heads/a\tb", "refs/heads/a\x7fb",
	} {
		if exec.Command("git", "check-ref-format", ref).Run() == nil {
			t.Fatalf("git check-ref-format accepts %q", ref)
		}
		if !strings.Contains(ref, branch+"/") {
			write(t, filepath.Join(dir, ".git"), ref, commit+"\n")
		}
		if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: "+ref+"\n"),
			0o644); err != nil {
			t.Fatal(err)
		}
		if head, err := hookrun.Head(dir); err == nil {
			t.Errorf("HEAD at %q: Head = %q, want an error", ref, head)
		}
	}
	git(t, dir, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	other := t.TempDir()
	git(t, other, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(dir, ".git", "commondir"),
		[]byte(filepath.Join(other, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if head, err := hookrun.Head(dir); err == nil {
		t.Errorf("a .git with commondir: Head = %q, want an error", head)
	}
}

// TestHeadFailsOnRefsThatGitCanReadOtherwise: with packed-refs in the form that git writes,
// with a peeled tag, with a detached HEAD, and with a ref name that ends with U+00A0, Head is
// the commit that git reads. A HEAD, a loose ref or packed-refs of another form or order, an id
// of sha256, a reftable folder, no real refs or objects folder, and refStorage, objectFormat or
// an unknown format in the config are errors, since git can read another commit from them. An
// error to read commondir is that error.
func TestHeadFailsOnRefsThatGitCanReadOtherwise(t *testing.T) {
	setup := func(t *testing.T) (dir, first, commit, branch string) {
		dir, first = repo(t, map[string]string{"a.txt": "a\n"})
		git(t, dir, "tag", "-a", "-m", "v1", "v1")
		write(t, dir, "a.txt", "b\n")
		git(t, dir, "commit", "--quiet", "-am", "second")
		git(t, dir, "pack-refs", "--all", "--prune")
		commit = git(t, dir, "rev-parse", "HEAD")
		branch = git(t, dir, "symbolic-ref", "HEAD")
		if head, err := hookrun.Head(dir); err != nil || head != commit {
			t.Fatalf("packed refs: Head = %q, %v, want %s", head, err, commit)
		}
		return dir, first, commit, branch
	}
	header := "# pack-refs with: peeled fully-peeled sorted \n"
	long := func(id string) string { return (id + id)[:64] }
	for _, c := range []struct {
		name string
		file string
		data func(first, commit, branch string) string
	}{
		{"a ref twice in packed-refs", "packed-refs", func(first, commit, branch string) string {
			return header + commit + " " + branch + "\n" + first + " " + branch + "\n"
		}},
		{"a carriage return in packed-refs", "packed-refs", func(_, commit, branch string) string {
			return strings.TrimSuffix(header, "\n") + "\r\n" + commit + " " + branch + "\n"
		}},
		{"a ref name that git does not accept", "packed-refs", func(_, commit, branch string) string {
			return commit + " refs/heads/a b\n" + commit + " " + branch + "\n"
		}},
		{"no line feed at the end of packed-refs", "packed-refs", func(_, commit, branch string) string {
			return commit + " " + branch
		}},
		{"a peeled line first", "packed-refs", func(first, commit, branch string) string {
			return "^" + first + "\n" + commit + " " + branch + "\n"
		}},
		{"two peeled lines", "packed-refs", func(first, commit, branch string) string {
			return commit + " " + branch + "\n^" + first + "\n^" + first + "\n"
		}},
		{"a peeled line that is not an id", "packed-refs", func(_, commit, branch string) string {
			return commit + " " + branch + "\n^x\n"
		}},
		{"an id with no ref", "packed-refs", func(_, commit, branch string) string {
			return commit + "\n" + commit + " " + branch + "\n"
		}},
		{"another line", "packed-refs", func(_, commit, branch string) string {
			return "garbage line\n" + commit + " " + branch + "\n"
		}},
		{"a header after the first line", "packed-refs", func(_, commit, branch string) string {
			return commit + " " + branch + "\n" + header
		}},
		{"a carriage return in HEAD", "HEAD", func(_, _, branch string) string {
			return "ref: " + branch + "\r\n"
		}},
		{"a space before ref: in HEAD", "HEAD", func(_, _, branch string) string {
			return " ref: " + branch + "\n"
		}},
		{"no space after ref: in HEAD", "HEAD", func(_, _, branch string) string {
			return "ref:" + branch + "\n"
		}},
		{"two lines in HEAD", "HEAD", func(_, _, branch string) string {
			return "ref: " + branch + "\n\n"
		}},
		{"a commit id with no line feed in HEAD", "HEAD", func(_, commit, _ string) string {
			return commit
		}},
		{"refs out of order", "packed-refs", func(first, commit, branch string) string {
			return header + commit + " " + branch + "\n" + first + " refs/heads/HEAD\n" +
				first + " refs/heads/zz\n"
		}},
		{"a 64-digit id in packed-refs", "packed-refs", func(_, commit, branch string) string {
			return header + long(commit) + " " + branch + "\n"
		}},
		{"a 64-digit peeled id", "packed-refs", func(_, commit, branch string) string {
			return header + commit + " " + branch + "\n^" + long(commit) + "\n"
		}},
		{"a 64-digit id in HEAD", "HEAD", func(_, commit, _ string) string {
			return long(commit) + "\n"
		}},
		{"refStorage in the config", "config", func(_, _, _ string) string {
			return "[core]\n\trepositoryformatversion = 1\n[extensions]\n\trefStorage = files\n"
		}},
		{"REFSTORAGE in the config", "config", func(_, _, _ string) string {
			return "[extensions]\n\tREFSTORAGE = files\n"
		}},
		{"refstorage in the config", "config", func(_, _, _ string) string {
			return "[extensions]\n\trefstorage = files\n"
		}},
		{"ObjectFormat in the config", "config", func(_, _, _ string) string {
			return "[extensions]\n\tObjectFormat = sha1\n"
		}},
		{"objectformat after a section's header", "config", func(_, _, _ string) string {
			return "[extensions]objectformat=sha256\n"
		}},
		{"objectformat with a carriage return and spaces before it", "config", func(_, _, _ string) string {
			return "[extensions]\r\n \r\t objectFormat = sha256\r\n"
		}},
		{"objectformat after a line of a key with no value", "config", func(_, _, _ string) string {
			return "[extensions]\n\tnoop\n\tobjectformat = sha256\n"
		}},
		{"objectformat after a format", "config", func(_, _, _ string) string {
			return "[core]\nrepositoryformatversion = 1\n[extensions]\nnoop = \"#\"\nobjectformat = sha256\n"
		}},
		{"refStorage after a header with a ] in its name", "config", func(_, _, _ string) string {
			return "[remote \"a]b\"] url = x\n[extensions] refStorage = reftable\n"
		}},
		{"refStorage with no value", "config", func(_, _, _ string) string {
			return "[extensions]\n\trefStorage"
		}},
		{"compatObjectFormat in the config", "config", func(_, _, _ string) string {
			return "[extensions]\n\tcompatObjectFormat = sha256\n"
		}},
		{"a format with no value", "config", func(_, _, _ string) string {
			return "[core]\n\trepositoryformatversion\n"
		}},
		{"format 2 on the line after format 0", "config", func(_, _, _ string) string {
			return "[core]\nrepositoryformatversion = 0\nrepositoryformatversion = 2\n"
		}},
		{"format 2 in the config", "config", func(_, _, _ string) string {
			return "[core]\n\trepositoryformatversion = 2\n"
		}},
		{"format 10 in the config", "config", func(_, _, _ string) string {
			return "[core]\n\trepositoryformatversion = 10\n"
		}},
		{"RepositoryFormatVersion 2 after 0 in the config", "config", func(_, _, _ string) string {
			return "[core]\n\trepositoryformatversion = 0\n[core] RepositoryFormatVersion=2\n"
		}},
		{"a format on the next line of the config", "config", func(_, _, _ string) string {
			return "[core]\n\trepositoryformatversion = \\\n2\n"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, first, commit, branch := setup(t)
			if err := os.WriteFile(filepath.Join(dir, ".git", c.file),
				[]byte(c.data(first, commit, branch)), 0o644); err != nil {
				t.Fatal(err)
			}
			if head, err := hookrun.Head(dir); err == nil {
				t.Errorf("Head = %q, want an error", head)
			}
		})
	}
	t.Run("a ref name that ends with U+00A0", func(t *testing.T) {
		for _, file := range []string{"HEAD", "refs/heads/via"} {
			dir, first, commit, branch := setup(t)
			git(t, dir, "update-ref", branch+"\u00a0", first)
			git(t, dir, "symbolic-ref", "HEAD", "refs/heads/via")
			write(t, filepath.Join(dir, ".git"), file, "ref: "+branch+"\u00a0\n")
			if got := git(t, dir, "rev-parse", "HEAD"); got != first {
				t.Fatalf("git reads %s through %s, want %s", got, file, first)
			}
			if head, err := hookrun.Head(dir); err != nil || head != first {
				t.Errorf("%s: Head = %q, %v, and git reads %s, not %s", file, head, err, first, commit)
			}
		}
	})
	t.Run("a loose ref of another form", func(t *testing.T) {
		for _, data := range []func(commit string) string{
			func(commit string) string { return commit + "\r\n" },
			func(commit string) string { return commit },
			func(commit string) string { return " " + commit + "\n" },
			func(commit string) string { return commit + "\n\n" },
		} {
			dir, _, commit, branch := setup(t)
			write(t, filepath.Join(dir, ".git"), branch, data(commit))
			if head, err := hookrun.Head(dir); err == nil {
				t.Errorf("a loose ref %q: Head = %s, want an error", data(commit), head)
			}
		}
	})
	t.Run("a 64-digit id in a loose ref", func(t *testing.T) {
		dir, _, commit, branch := setup(t)
		write(t, filepath.Join(dir, ".git"), branch, long(commit)+"\n")
		if head, err := hookrun.Head(dir); err == nil {
			t.Errorf("Head = %q, want an error", head)
		}
	})
	t.Run("a packed-refs that git searches by halves", func(t *testing.T) {
		dir, first, commit, branch := setup(t)
		write(t, filepath.Join(dir, ".git"), "packed-refs", header+commit+" refs/heads/main\n"+
			first+" refs/heads/HEAD\n"+first+" refs/heads/zz\n")
		write(t, filepath.Join(dir, ".git"), "HEAD", "ref: refs/heads/main\n")
		if got := git(t, dir, "rev-parse", "--verify", "--quiet", "HEAD"); got != first {
			t.Fatalf("git reads %s with %s packed, want %s", got, branch, first)
		}
		if head, err := hookrun.Head(dir); err == nil {
			t.Errorf("Head = %q, and git reads %s", head, first)
		}
	})
	t.Run("a sha256 repository whose config has the word in a comment alone", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "--quiet", "--object-format=sha256")
		write(t, dir, "a.txt", "a\n")
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "--quiet", "-m", "first")
		if head, err := hookrun.Head(dir); err == nil {
			t.Errorf("a sha256 repository: Head = %q, want an error", head)
		}
		// git reads sha1 with this config, and so another commit than the branch's file has.
		write(t, filepath.Join(dir, ".git"), "refs/heads/HEAD", strings.Repeat("1", 40)+"\n")
		write(t, filepath.Join(dir, ".git"), "config",
			"[core]\n\trepositoryformatversion = 0\n# objectformat = sha256\n")
		if got := git(t, dir, "rev-parse", "HEAD"); !strings.Contains(got, strings.Repeat("1", 40)) {
			t.Fatalf("git reads %s", got)
		}
		if head, err := hookrun.Head(dir); err == nil {
			t.Errorf("the word in a comment: Head = %q, want an error", head)
		}
	})
	for _, name := range []string{"refs", "objects"} {
		for _, kind := range []string{"missing", "a file", "a link", "not searchable"} {
			t.Run(name+" "+kind, func(t *testing.T) {
				dir, _, _, _ := setup(t)
				git(t, dir, "checkout", "--quiet", "--detach")
				path := filepath.Join(dir, ".git", name)
				moved := filepath.Join(t.TempDir(), name)
				var err error
				if kind == "not searchable" {
					if os.Getuid() == 0 {
						t.Skip("root searches every folder")
					}
					t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
					err = os.Chmod(path, 0o600)
				} else {
					err = os.Rename(path, moved)
				}
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "a file":
					write(t, filepath.Join(dir, ".git"), name, "x\n")
				case "a link":
					if err := os.Symlink(moved, path); err != nil {
						t.Fatal(err)
					}
				}
				if head, err := hookrun.Head(dir); err == nil {
					t.Errorf("Head = %q, want an error", head)
				}
			})
		}
	}
	t.Run("a bare repository at the top with no .git/refs", func(t *testing.T) {
		dir, _, commit, _ := setup(t)
		git(t, dir, "checkout", "--quiet", "--detach")
		bare, other := repo(t, map[string]string{"b.txt": "b\n"})
		git(t, bare, "config", "core.bare", "true")
		for _, name := range []string{"HEAD", "config", "objects", "refs"} {
			if err := os.Rename(filepath.Join(bare, ".git", name), filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, ".git", "refs")); err != nil {
			t.Fatal(err)
		}
		if got := git(t, dir, "rev-parse", "HEAD"); got != other {
			t.Fatalf("git reads %s, want the bare repository's %s", got, other)
		}
		if head, err := hookrun.Head(dir); err == nil {
			t.Errorf("Head = %q (the checkout's is %s), and git reads %s", head, commit, other)
		}
	})
	t.Run("a config with the words in no key", func(t *testing.T) {
		dir, _, commit, branch := setup(t)
		git(t, dir, "remote", "add", "objectformat", "https://h.example/octo/refstorage.git")
		git(t, dir, "config", "branch.refStorage.merge", "refs/heads/repositoryformatversion")
		git(t, dir, "config", "remote.objectformat.pushurl", "https://h.example/octo/ objectformat = x")
		git(t, dir, "config", "alias.objectformat-tools", "repositoryformatversion")
		git(t, dir, "config", "alias.refstoragex", "x")
		write(t, filepath.Join(dir, ".git"), "config", string(read(t, filepath.Join(dir, ".git", "config")))+"# objectformat = sha256\n")
		if git(t, dir, "rev-parse", branch) != commit {
			t.Fatal("git reads another commit")
		}
		head, err := hookrun.Head(dir)
		if err != nil || head != commit {
			t.Errorf("Head = %q, %v, want %s", head, err, commit)
		}
	})
	t.Run("a folder in the place of a loose ref", func(t *testing.T) {
		for _, below := range []string{".", "refs/heads/a/b/c"} {
			dir, first, commit, branch := setup(t)
			write(t, filepath.Join(dir, ".git"), "refs/heads/via", "ref: "+branch+"\n")
			if below != "." {
				write(t, filepath.Join(dir, ".git", branch), below, first+"\n")
			} else if err := os.MkdirAll(filepath.Join(dir, ".git", branch), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, at := range []string{branch, "refs/heads/via"} {
				write(t, filepath.Join(dir, ".git"), "HEAD", "ref: "+at+"\n")
				if got := git(t, dir, "rev-parse", "HEAD"); got != commit {
					t.Fatalf("git reads %s through %s, want %s", got, at, commit)
				}
				if head, err := hookrun.Head(dir); err != nil || head != commit {
					t.Errorf("%s: Head = %q, %v, want %s", at, head, err, commit)
				}
			}
			if err := os.Remove(filepath.Join(dir, ".git", "packed-refs")); err != nil {
				t.Fatal(err)
			}
			if head, err := hookrun.Head(dir); err == nil {
				t.Errorf("with no packed-refs: Head = %q, want an error", head)
			}
			if err := os.Mkdir(filepath.Join(dir, ".git", "packed-refs"), 0o755); err != nil {
				t.Fatal(err)
			}
			if head, err := hookrun.Head(dir); err == nil {
				t.Errorf("with a folder packed-refs: Head = %q, want an error", head)
			}
		}
	})
	t.Run("a HEAD that is a folder", func(t *testing.T) {
		dir, first, _, _ := setup(t)
		head := filepath.Join(dir, ".git", "HEAD")
		if err := os.Remove(head); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(head, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, ".git"), "packed-refs", first+" HEAD\n")
		if got, err := hookrun.Head(dir); err == nil {
			t.Errorf("Head = %q, want an error", got)
		}
	})
	t.Run("a link to a folder in the place of a loose ref", func(t *testing.T) {
		dir, _, _, branch := setup(t)
		if err := os.Symlink(t.TempDir(), filepath.Join(dir, ".git", branch)); err != nil {
			t.Fatal(err)
		}
		if head, err := hookrun.Head(dir); err == nil {
			t.Errorf("Head = %q, want an error", head)
		}
	})
	t.Run("a detached HEAD", func(t *testing.T) {
		dir, _, commit, _ := setup(t)
		git(t, dir, "checkout", "--quiet", "--detach")
		if head, err := hookrun.Head(dir); err != nil || head != commit {
			t.Errorf("Head = %q, %v, want %s", head, err, commit)
		}
	})
	t.Run("a reftable folder", func(t *testing.T) {
		dir, _, _, _ := setup(t)
		if err := os.Mkdir(filepath.Join(dir, ".git", "reftable"), 0o755); err != nil {
			t.Fatal(err)
		}
		if head, err := hookrun.Head(dir); err == nil {
			t.Errorf("Head = %q, want an error", head)
		}
	})
	t.Run("a .git file", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, ".git", "gitdir: elsewhere\n")
		_, err := hookrun.Head(dir)
		if err == nil || strings.Contains(err.Error(), "the .git has") ||
			!errors.Is(err, syscall.ENOTDIR) || !strings.Contains(err.Error(), "commondir") {
			t.Errorf("Head = %v, want the error to read commondir in a .git that is not a folder", err)
		}
	})
	t.Run("a config that is a folder", func(t *testing.T) {
		dir, _, _, _ := setup(t)
		config := filepath.Join(dir, ".git", "config")
		if err := os.Remove(config); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(config, 0o755); err != nil {
			t.Fatal(err)
		}
		if head, err := hookrun.Head(dir); err == nil {
			t.Errorf("Head = %q, want an error", head)
		}
	})
}
