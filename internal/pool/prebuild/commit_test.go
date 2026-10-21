package prebuild_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/qoryai/qory/internal/pool/prebuild"
)

// gitTree is a working folder whose .git holds files, each a name under .git and its
// content; a content that starts with "->" makes a link to the rest instead.
func gitTree(t *testing.T, files map[string]string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(filepath.Join(work, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(work, ".git", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(content, "->"); ok {
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return work
}

func TestCommit(t *testing.T) {
	const id = testCommit
	other := strings.Repeat("a", 40)
	for _, c := range []struct {
		name  string
		files map[string]string
	}{
		{"a loose ref", map[string]string{"HEAD": "ref: refs/heads/main\n", "refs/heads/main": id + "\n"}},
		{"a loose ref with no newline", map[string]string{"HEAD": "ref: refs/heads/main", "refs/heads/main": id}},
		{"a branch with slashes", map[string]string{"HEAD": "ref: refs/heads/feature/a-b_c.d\n", "refs/heads/feature/a-b_c.d": id + "\n"}},
		{"a packed ref", map[string]string{"HEAD": "ref: refs/heads/main\n", "packed-refs": "# pack-refs with: peeled fully-peeled sorted \n" + other + " refs/heads/develop\n" + id + " refs/heads/main\n^" + other + "\n"}},
		{"a loose ref before the packed one", map[string]string{"HEAD": "ref: refs/heads/main\n", "refs/heads/main": id + "\n", "packed-refs": other + " refs/heads/main\n"}},
		{"a detached HEAD", map[string]string{"HEAD": id + "\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := prebuild.Commit(gitTree(t, c.files))
			if err != nil || got != id {
				t.Errorf("Commit %q, %v; want %s", got, err, id)
			}
		})
	}
}

func TestCommitRefused(t *testing.T) {
	const id = testCommit
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte(id+"\n"), 0o600)
	head := "ref: refs/heads/main\n"
	neither := ".git/HEAD is neither a commit id nor a ref under refs/"
	notLine := ".git/HEAD is not one line"
	for _, c := range []struct {
		name  string
		files map[string]string
		edit  func(t *testing.T, work string)
		why   string
	}{
		{"no HEAD", map[string]string{}, nil, ".git/HEAD is not there"},
		{"HEAD a link", map[string]string{"HEAD": "->" + outside}, nil, ".git/HEAD is not a regular file"},
		{"HEAD a link inside", map[string]string{"HEAD": "->ORIG_HEAD", "ORIG_HEAD": id + "\n"}, nil, ".git/HEAD is not a regular file"},
		{"the ref a link", map[string]string{"HEAD": head, "refs/heads/main": "->" + outside}, nil, ".git/refs/heads/main is not a regular file"},
		{"the ref a link inside", map[string]string{"HEAD": head, "refs/heads/main": "->../../ORIG_HEAD", "ORIG_HEAD": id + "\n"}, nil, ".git/refs/heads/main is not a regular file"},
		{"a folder of the ref a link", map[string]string{"HEAD": head, "refs/heads": "->" + filepath.Dir(outside)}, nil, ".git/refs/heads is not a folder"},
		{"a folder of the ref a link inside", map[string]string{"HEAD": head, "refs/heads": "->../other", "other/main": id + "\n"}, nil, ".git/refs/heads is not a folder"},
		{"refs a link inside", map[string]string{"HEAD": head, "refs": "->other", "other/heads/main": id + "\n"}, nil, ".git/refs is not a folder"},
		{"packed-refs a link", map[string]string{"HEAD": head, "packed-refs": "->" + outside}, nil, ".git/packed-refs is not a regular file"},
		{"packed-refs a link inside", map[string]string{"HEAD": head, "packed-refs": "->other", "other": id + " refs/heads/main\n"}, nil, ".git/packed-refs is not a regular file"},
		{".git a link inside", nil, func(t *testing.T, work string) {
			os.Rename(filepath.Join(work, ".git"), filepath.Join(work, "real-git"))
			os.WriteFile(filepath.Join(work, "real-git", "HEAD"), []byte(id+"\n"), 0o600)
			os.Symlink("real-git", filepath.Join(work, ".git"))
		}, ".git is not a folder"},
		{".git a file", nil, func(t *testing.T, work string) {
			os.RemoveAll(filepath.Join(work, ".git"))
			os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: /elsewhere\n"), 0o600)
		}, ".git is not a folder"},
		{"the working folder a link", map[string]string{"HEAD": id + "\n"}, func(t *testing.T, work string) {
			moved := work + "-moved"
			os.Rename(work, moved)
			os.Symlink(moved, work)
		}, "the working folder is not a folder"},
		{"HEAD a folder", map[string]string{"HEAD/x": "y"}, nil, ".git/HEAD is not a regular file"},
		{"HEAD a FIFO", map[string]string{}, func(t *testing.T, work string) {
			if err := syscall.Mkfifo(filepath.Join(work, ".git", "HEAD"), 0o600); err != nil {
				t.Skip(err)
			}
		}, ".git/HEAD is not a regular file"},
		{"HEAD garbage", map[string]string{"HEAD": "nonsense\n"}, nil, neither},
		{"HEAD empty", map[string]string{"HEAD": ""}, nil, notLine},
		{"HEAD of two lines", map[string]string{"HEAD": id + "\n" + id + "\n"}, nil, notLine},
		{"HEAD with a carriage return", map[string]string{"HEAD": id + "\r\n"}, nil, notLine},
		{"HEAD with a NUL", map[string]string{"HEAD": id + "\x00"}, nil, notLine},
		{"HEAD too large", map[string]string{"HEAD": strings.Repeat("a", 5000)}, nil, ".git/HEAD is larger than 4096 bytes"},
		{"an id of upper case", map[string]string{"HEAD": strings.ToUpper(id) + "\n"}, nil, neither},
		{"a short id", map[string]string{"HEAD": id[:39] + "\n"}, nil, neither},
		{"a SHA-256 id", map[string]string{"HEAD": strings.Repeat("a", 64) + "\n"}, nil, neither},
		{"a ref outside refs", map[string]string{"HEAD": "ref: ORIG_HEAD\n", "ORIG_HEAD": id + "\n"}, nil, neither},
		{"a ref that climbs", map[string]string{"HEAD": "ref: refs/../ORIG_HEAD\n", "ORIG_HEAD": id + "\n"}, nil, neither},
		{"a ref with a dot segment", map[string]string{"HEAD": "ref: refs/heads/./main\n", "refs/heads/main": id + "\n"}, nil, neither},
		{"a ref with an empty segment", map[string]string{"HEAD": "ref: refs/heads//main\n", "refs/heads/main": id + "\n"}, nil, neither},
		{"a lock ref", map[string]string{"HEAD": "ref: refs/heads/main.lock\n", "refs/heads/main.lock": id + "\n"}, nil, neither},
		{"an absolute ref", map[string]string{"HEAD": "ref: /etc/passwd\n"}, nil, neither},
		{"a ref that names a ref", map[string]string{"HEAD": head, "refs/heads/main": "ref: refs/heads/other\n", "refs/heads/other": id + "\n"}, nil, ".git/refs/heads/main is not a commit id"},
		{"a ref of garbage", map[string]string{"HEAD": head, "refs/heads/main": "not an id\n"}, nil, ".git/refs/heads/main is not a commit id"},
		{"a ref of two lines", map[string]string{"HEAD": head, "refs/heads/main": id + "\n" + id + "\n"}, nil, ".git/refs/heads/main is not one line"},
		{"a ref in neither place", map[string]string{"HEAD": head}, nil, "refs/heads/main is in neither .git/refs/heads/main nor .git/packed-refs"},
		{"a ref packed not", map[string]string{"HEAD": head, "packed-refs": id + " refs/heads/develop\n"}, nil, "refs/heads/main is in neither .git/refs/heads/main nor .git/packed-refs"},
		{"a ref packed twice", map[string]string{"HEAD": head, "packed-refs": id + " refs/heads/main\n" + strings.Repeat("b", 40) + " refs/heads/main\n"}, nil, ".git/packed-refs names refs/heads/main twice"},
		{"a ref packed with garbage", map[string]string{"HEAD": head, "packed-refs": "zz refs/heads/main\n"}, nil, ".git/packed-refs gives refs/heads/main no commit id"},
		{"packed-refs too large", map[string]string{"HEAD": head, "packed-refs": strings.Repeat("# x\n", 300000) + id + " refs/heads/main\n"}, nil, ".git/packed-refs is larger than 1048576 bytes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			work := gitTree(t, c.files)
			if c.edit != nil {
				c.edit(t, work)
			}
			got, err := prebuild.Commit(work)
			if want := "the fetched repository names no commit: " + c.why; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Commit %q, %v; want %q", got, err, want)
			}
		})
	}
}

// TestCommitOfGit is the commit of a working tree git made, as qory run prepare leaves
// one: a branch checked out with a loose ref, then with its refs packed, then a
// detached HEAD; each read as git reads it.
func TestCommitOfGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	work := filepath.Join(t.TempDir(), "work")
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = work
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com", "GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(work, 0o700)
	git("init", "--quiet", "-b", "main")
	os.WriteFile(filepath.Join(work, "README.md"), []byte("acme/shop\n"), 0o600)
	git("add", "-A")
	git("commit", "--quiet", "-m", "first")
	for _, step := range []func(){func() {}, func() { git("pack-refs", "--all") }, func() { git("checkout", "--quiet", "--detach") }} {
		step()
		want := git("rev-parse", "HEAD")
		if got, err := prebuild.Commit(work); err != nil || got != want {
			t.Errorf("Commit %q, %v; want %s", got, err, want)
		}
	}
}
