package hookrun_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hookrun"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/preparation"
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

// TestProgramWritesTheCommittedFile: the program is the file as the commit has it, also after
// the working folder and a later commit changed it, in a file every user may read and run.
func TestProgramWritesTheCommittedFile(t *testing.T) {
	dir, commit := repo(t, map[string]string{"scripts/check.sh": "#!/bin/sh\necho original\n"})
	write(t, dir, "scripts/check.sh", "#!/bin/sh\necho edited\n")
	git(t, dir, "commit", "--quiet", "-am", "edit")
	write(t, dir, "scripts/check.sh", "#!/bin/sh\necho left\n")
	dest := filepath.Join(t.TempDir(), "0")
	if err := hookrun.Program(context.Background(), dir, commit, "scripts/check.sh", dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "#!/bin/sh\necho original\n" {
		t.Errorf("the program is %q", got)
	}
	if info, _ := os.Stat(dest); info.Mode().Perm() != 0o555 {
		t.Errorf("the program has the mode %v, want 0555", info.Mode().Perm())
	}
}

// TestProgramRefusesALinkAFolderAndAMissingEntry: an entry that is a link, a path through a
// linked folder, a folder, and no entry are each a [*hookrun.Refused], and no file is written.
func TestProgramRefusesALinkAFolderAndAMissingEntry(t *testing.T) {
	dir, _ := repo(t, map[string]string{"real/check.sh": "#!/bin/sh\n"})
	if err := os.Symlink("../real/check.sh", filepath.Join(dir, "check.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "tools")); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "links")
	commit := git(t, dir, "rev-parse", "HEAD")
	for path, want := range map[string]string{
		"check.sh":          "check.sh is a link in git at ",
		"tools/check.sh":    "tools/check.sh passes through the link tools in git at ",
		"real":              "real is a folder in git at ",
		"real/missing.sh":   "real/missing.sh has no entry in git at ",
		"../outside.sh":     "../outside.sh is not a path from the checkout's root",
		"real//check.sh":    "real//check.sh is not a path from the checkout's root",
		"real/check.sh/sub": "real/check.sh/sub has no entry",
	} {
		dest := filepath.Join(t.TempDir(), "0")
		err := hookrun.Program(context.Background(), dir, commit, path, dest)
		var refused *hookrun.Refused
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want a refusal with %q", path, err, want)
		}
		if _, err := os.Lstat(dest); err == nil {
			t.Errorf("%s: the program was written", path)
		}
	}
}

// TestProgramRefusesAnObjectThatDoesNotMatchItsID: the loose object of the program's blob in
// .git/objects replaced with other bytes, as a process that writes .git can replace it, is
// refused, so the hook never runs those bytes.
func TestProgramRefusesAnObjectThatDoesNotMatchItsID(t *testing.T) {
	dir, commit := repo(t, map[string]string{"check.sh": "#!/bin/sh\necho original\n"})
	blob := git(t, dir, "rev-parse", "HEAD:check.sh")
	// The planted object has the blob's id and other content, and replaces the only copy, so
	// every version of git reads it.
	content := []byte("#!/bin/sh\necho planted\n")
	var raw bytes.Buffer
	zw := zlib.NewWriter(&raw)
	fmt.Fprintf(zw, "blob %d\x00%s", len(content), content)
	zw.Close()
	path := filepath.Join(dir, ".git", "objects", blob[:2], blob[2:])
	if err := os.Remove(path); err != nil {
		t.Fatalf("the blob is not a loose object: %v", err)
	}
	if err := os.WriteFile(path, raw.Bytes(), 0o444); err != nil {
		t.Fatal(err)
	}
	if sum := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(content))), content...)); hex.EncodeToString(sum[:]) == blob {
		t.Fatal("the planted bytes have the blob's id")
	}
	dest := filepath.Join(t.TempDir(), "0")
	err := hookrun.Program(context.Background(), dir, commit, "check.sh", dest)
	if err == nil || !strings.Contains(err.Error(), "the blob "+blob[:12]+" in .git does not match its id") {
		t.Fatalf("error %v, want the object refused", err)
	}
	if _, err := os.Lstat(dest); err == nil {
		t.Error("the program was written")
	}
}

// forgeTree writes body as the loose object of the tree id in the checkout at dir, in place of
// the tree that has that id.
func forgeTree(t *testing.T, dir, id string, body []byte) {
	t.Helper()
	var raw bytes.Buffer
	zw := zlib.NewWriter(&raw)
	fmt.Fprintf(zw, "tree %d\x00%s", len(body), body)
	zw.Close()
	path := filepath.Join(dir, ".git", "objects", id[:2], id[2:])
	if err := os.Remove(path); err != nil {
		t.Fatalf("the tree is not a loose object: %v", err)
	}
	if err := os.WriteFile(path, raw.Bytes(), 0o444); err != nil {
		t.Fatal(err)
	}
}

// TestASnapshotOfAForgedFolderTreeSeesTheHooksChange: the run's setup wrote over the tree object
// of vendor with a tree of one submodule entry, lib, and removed the files of vendor. The
// snapshot has that state, and its tree id is computed from its entries, not taken from the
// forged object, so after a hook that writes back vendor's files of HEAD the check returns a
// difference, and the restore removes them. With the tree ids that read-tree keeps, the check
// returned no difference. Both in a new repository and in a clone whose pack the setup unpacked.
func TestASnapshotOfAForgedFolderTreeSeesTheHooksChange(t *testing.T) {
	files := map[string]string{"a.txt": "a\n", ".gitignore": "node_modules/\n", "vendor/v1.py": "assert False\n", "vendor/v2.py": "x\n"}
	for _, clone := range []bool{false, true} {
		t.Run(fmt.Sprintf("clone %v", clone), func(t *testing.T) {
			dir, head := repo(t, files)
			if clone {
				src := dir
				dir = filepath.Join(t.TempDir(), "c")
				git(t, filepath.Dir(dir), "clone", "--quiet", "file://"+src, dir)
				packs, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
				if err != nil || len(packs) != 1 {
					t.Fatalf("packs %v, %v", packs, err)
				}
				pack, err := os.ReadFile(packs[0])
				if err != nil {
					t.Fatal(err)
				}
				for _, path := range []string{packs[0], strings.TrimSuffix(packs[0], ".pack") + ".idx"} {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command("git", "unpack-objects", "-q")
				cmd.Dir, cmd.Stdin = dir, bytes.NewReader(pack)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git unpack-objects: %v %s", err, out)
				}
			}
			vendor := git(t, dir, "rev-parse", "HEAD:vendor")
			id, err := hex.DecodeString(head)
			if err != nil {
				t.Fatal(err)
			}
			forgeTree(t, dir, vendor, append([]byte("160000 lib\x00"), id...))
			if err := os.RemoveAll(filepath.Join(dir, "vendor")); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "vendor", "lib"), 0o755); err != nil {
				t.Fatal(err)
			}
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if snap.Tree == git(t, dir, "rev-parse", "HEAD^{tree}") {
				t.Errorf("the snapshot's tree is HEAD's, %s", snap.Tree)
			}
			// The hook's change: vendor's files of HEAD in place of lib.
			if err := os.Remove(filepath.Join(dir, "vendor", "lib")); err != nil {
				t.Fatal(err)
			}
			write(t, dir, "vendor/v1.py", files["vendor/v1.py"])
			write(t, dir, "vendor/v2.py", files["vendor/v2.py"])
			if same, err := snap.Same(context.Background()); err != nil || same {
				t.Errorf("the check finds the checkout the same: %v, %v", same, err)
			}
			if err := snap.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"vendor/v1.py", "vendor/v2.py"} {
				if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path))); err == nil {
					t.Errorf("%s is still there after the restore", path)
				}
			}
		})
	}
}

// caseFolds reports whether the file system of dir finds a file by a name that differs in case.
func caseFolds(t *testing.T, dir string) bool {
	t.Helper()
	write(t, dir, "fold-probe", "x")
	defer os.Remove(filepath.Join(dir, "fold-probe"))
	_, err := os.Lstat(filepath.Join(dir, "FOLD-PROBE"))
	return err == nil
}

// TestTheCheckoutsCoreKeysChangeNoRestore: with a .git/config that the run's setup wrote with
// core.filemode false, core.symlinks false or core.ignorecase true, which the copy of the
// configuration keeps, the restore undoes a mode that the hook changed, a link that it replaced
// with a file of the link's text, and a file whose name differs from a tracked one in case alone.
func TestTheCheckoutsCoreKeysChangeNoRestore(t *testing.T) {
	for _, c := range []struct {
		key  string
		hook func(t *testing.T, dir string)
		want func(t *testing.T, dir string)
	}{
		{"core.filemode=false", func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, dir string) {
			if info, err := os.Lstat(filepath.Join(dir, "a.txt")); err != nil || info.Mode().Perm()&0o111 != 0 {
				t.Errorf("a.txt after the restore: %v, %v", info.Mode(), err)
			}
		}},
		{"core.symlinks=false", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			write(t, dir, "link", "a.txt")
		}, func(t *testing.T, dir string) {
			if target, err := os.Readlink(filepath.Join(dir, "link")); err != nil || target != "a.txt" {
				t.Errorf("link after the restore: %q, %v", target, err)
			}
		}},
		{"core.ignorecase=true", func(t *testing.T, dir string) {
			write(t, dir, "A.TXT", "the hook's\n")
		}, func(t *testing.T, dir string) {
			if _, err := os.Lstat(filepath.Join(dir, "A.TXT")); err == nil {
				t.Error("A.TXT is still there after the restore")
			}
		}},
	} {
		t.Run(c.key, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
			if c.key == "core.ignorecase=true" && caseFolds(t, dir) {
				t.Skip("the file system of the test's folder ignores case")
			}
			if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			git(t, dir, "add", "-A")
			git(t, dir, "commit", "--quiet", "-m", "a link")
			key, value, _ := strings.Cut(c.key, "=")
			git(t, dir, "config", key, value)
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c.hook(t, dir)
			if err := snap.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			c.want(t, dir)
		})
	}
}

// TestAHooksFileInASubmodulesFolderFailsTheRestore: qory leaves the folder of a submodule
// entry, sub, out of the add and compares it. A hook that writes files there fails the restore,
// also when the setup filled the folder first; a hook that leaves the folder as the setup left it,
// empty or filled, passes, and the folder keeps the setup's files.
func TestAHooksFileInASubmodulesFolderFailsTheRestore(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup map[string]string
		hook  map[string]string
		fails bool
	}{
		{"a file in an empty folder", nil, map[string]string{"sub/x": "x\n", "sub/d/z": "z\n"}, true},
		{"a changed file in a filled folder", map[string]string{"sub/lib.py": "lib\n"}, map[string]string{"sub/lib.py": "planted\n"}, true},
		{"an empty folder left as it was", nil, map[string]string{"a.txt": "the hook's\n"}, false},
		{"a filled folder left as it was", map[string]string{"sub/lib.py": "lib\n"}, map[string]string{"a.txt": "the hook's\n"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, head := repo(t, map[string]string{"a.txt": "a\n"})
			git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
			git(t, dir, "commit", "--quiet", "-m", "a submodule")
			if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			for path, content := range c.setup {
				write(t, dir, path, content)
			}
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for path, content := range c.hook {
				write(t, dir, path, content)
			}
			err = snap.Restore(context.Background())
			if c.fails != (err != nil) || err != nil && err.Error() != "a hook changed the folder sub of a submodule entry" {
				t.Fatalf("the restore returned %v", err)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(got) != "a\n" {
				t.Errorf("a.txt is %q after the restore", got)
			}
			for path, content := range c.setup {
				if got, err := os.ReadFile(filepath.Join(dir, path)); !c.fails && (err != nil || string(got) != content) {
					t.Errorf("%s is %q, %v after the restore", path, got, err)
				}
			}
		})
	}
}

// filterRepo makes a repository in the folder sub of the checkout at dir, with the file f
// committed and a filter for f that creates the file ran in a folder outside the checkout when a
// git reads f there. It returns the path of ran and the repository's commit.
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
func notRan(t *testing.T, ran string) {
	t.Helper()
	if _, err := os.Lstat(ran); err == nil {
		t.Error("the filter of the repository in the checkout ran")
	}
}

// TestNoGitRunsInTheFolderOfASubmoduleEntry: no git reads the folder of a submodule entry, for the
// snapshot and for a restore. With a repository there before the snapshot, a hook that changes
// nothing passes; with one from the hook, the restore fails.
func TestNoGitRunsInTheFolderOfASubmoduleEntry(t *testing.T) {
	for _, byHook := range []bool{false, true} {
		t.Run(fmt.Sprintf("by the hook %v", byHook), func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
			at := dir
			if byHook {
				at = t.TempDir()
			}
			ran, commit := filterRepo(t, at, "sub")
			// git add starts git status in a submodule's folder when its HEAD is the entry's commit.
			git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+commit+",sub")
			git(t, dir, "commit", "--quiet", "-m", "a submodule")
			if byHook {
				if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if byHook {
				if err := os.Remove(filepath.Join(dir, "sub")); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(at, "sub"), filepath.Join(dir, "sub")); err != nil {
					t.Fatal(err)
				}
			}
			notRan(t, ran)
			if err := snap.Restore(context.Background()); byHook != (err != nil) {
				t.Errorf("the restore returned %v", err)
			}
			notRan(t, ran)
		})
	}
}

// TestASnapshotKeepsNoSubmoduleEntryWithoutAFolder: a submodule entry of HEAD whose folder is not
// there, or is a file, before the hooks, is not in the snapshot: a hook that changes nothing
// passes, the checkout stays as it was, and a folder that the hook makes there is removed.
func TestASnapshotKeepsNoSubmoduleEntryWithoutAFolder(t *testing.T) {
	for _, file := range []bool{false, true} {
		t.Run(fmt.Sprintf("a file %v", file), func(t *testing.T) {
			dir, head := repo(t, map[string]string{"a.txt": "a\n"})
			git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",lib/sub")
			git(t, dir, "commit", "--quiet", "-m", "a submodule")
			if file {
				write(t, dir, "lib", "the agent's\n")
			}
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := snap.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !file {
				write(t, dir, "lib/sub/x", "the hook's\n")
				if err := snap.Restore(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			got, err := os.ReadFile(filepath.Join(dir, "lib"))
			if file && string(got) != "the agent's\n" || !file && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("lib is %q, %v after the restore", got, err)
			}
		})
	}
}

// TestASnapshotKeepsNoSubmoduleEntryBehindALink: a submodule entry of HEAD below a folder that is a
// link before the hooks is not in the snapshot: a hook that changes nothing passes, and the link
// stays.
func TestASnapshotKeepsNoSubmoduleEntryBehindALink(t *testing.T) {
	dir, head := repo(t, map[string]string{"a.txt": "a\n"})
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",d/sub")
	git(t, dir, "commit", "--quiet", "-m", "a submodule")
	write(t, dir, "e/sub/x", "x\n")
	if err := os.Symlink("e", filepath.Join(dir, "d")); err != nil {
		t.Fatal(err)
	}
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "ls-tree", "-r", "--name-only", snap.Tree); got != "a.txt\nd\ne/sub/x" {
		t.Errorf("the snapshot has %q", got)
	}
	if err := snap.Restore(context.Background()); err != nil {
		t.Error(err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "d")); target != "e" {
		t.Errorf("d is %q, %v", target, err)
	}
}

// TestRestoreReadsNoExcludesFileOfTheUser: a file that a hook added is removed, also when the
// ignore file of the user that runs qory matches it.
func TestRestoreReadsNoExcludesFileOfTheUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	write(t, home, ".config/git/ignore", "added.txt\n")
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "added.txt", "added\n")
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "added.txt")); err == nil {
		t.Error("added.txt stays")
	}
}

// TestAnErrorOfGitHasTheErrorLine: an error of git has the line of git's error, not a warning that
// git prints before it, whatever the language of the environment.
func TestAnErrorOfGitHasTheErrorLine(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	t.Cleanup(hookrun.SetAfterWalk(func(dir string) {
		write(t, dir, ".gitattributes", "* not\x1ba-name!\n")
		write(t, dir, "b.txt", "b\n")
		git(t, dir, "init", "--quiet", "nested")
	}))
	_, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if want := "git add in " + dir + " failed: error: 'nested/' does not have a commit checked out"; err == nil || err.Error() != want {
		t.Errorf("the error %q, want %q", err, want)
	}
}

// TestAnErrorOfGitIsOnePrintableLine: with no error line, an error of git has git's last line on
// stderr that is not empty, else its exit status. A character that does not print is a ?.
func TestAnErrorOfGitIsOnePrintableLine(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ script, want string }{
		{`printf 'warning: one\nwarning: two\n\n' >&2; exit 3`, "warning: two"},
		{`printf 'warning: one\nerror: a\033[2Kb\nlast\n' >&2; exit 3`, "error: a?[2Kb"},
		{`printf 'a\033]0;title\007b\n' >&2; exit 3`, "a?]0;title?b"},
		{`exit 3`, "exit status 3"},
	} {
		err := hookrun.RunScript(context.Background(), dir, c.script, "add")
		if want := "git add in " + dir + " failed: " + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: the error %q, want %q", c.script, err, want)
		}
	}
}

// TestAnErrorOfAGitThatRanPastTheContextHasTheEndOfTheRun: when the context ends while git
// runs, the error has the error of the run, not the last line that git printed on stderr.
func TestAnErrorOfAGitThatRanPastTheContextHasTheEndOfTheRun(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer cancel()
		for range 1000 {
			if _, err := os.Lstat(marker); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	script := "printf 'warning: one\\n' >&2; : > '" + marker + "'; exec sleep 30"
	err := hookrun.RunScript(ctx, dir, script, "add")
	if want := "git add in " + dir + " failed: signal: killed"; err == nil || err.Error() != want {
		t.Errorf("the error %q, want %q", err, want)
	}
}

// TestAFileAtASubmodulesPathIsRestored: a file or a link at the path of a submodule entry of HEAD,
// from before the hooks, is restored when a hook puts a folder in its place.
func TestAFileAtASubmodulesPathIsRestored(t *testing.T) {
	for _, link := range []bool{false, true} {
		t.Run(fmt.Sprintf("a link %v", link), func(t *testing.T) {
			dir, head := repo(t, map[string]string{"a.txt": "a\n"})
			git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
			git(t, dir, "commit", "--quiet", "-m", "a submodule")
			path, mode := filepath.Join(dir, "sub"), fs.FileMode(0)
			if link {
				mode = fs.ModeSymlink
				if err := os.Symlink("a.txt", path); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, dir, "sub", "sub\n")
			}
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := snap.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(path); err != nil || info.Mode().Type() != mode {
				t.Errorf("sub is %v, %v", info, err)
			}
		})
	}
}

// TestRestoreRemovesNestedRuleFiles: six folders in each other, each with a .gitignore that
// matches every file, that a hook made are removed: more than one restore's removals of rule
// files.
func TestRestoreRemovesNestedRuleFiles(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for n := range 6 {
		write(t, dir, strings.Repeat("n/", n+1)+".gitignore", "*\n")
	}
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "n")); err == nil {
		t.Error("n stays")
	}
}

// TestRestoreComparesTheBytesOfAFileWithAnEncoding: a file that a .gitattributes of the checkout
// has an encoding for is restored when a hook wrote the same text in another byte order.
func TestRestoreComparesTheBytesOfAFileWithAnEncoding(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	write(t, dir, ".gitattributes", "*.u working-tree-encoding=UTF-16\n")
	before, after := "\xfe\xff\x00a\x00\n", "\xff\xfea\x00\n\x00"
	write(t, dir, "x.u", before)
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "x.u", after)
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "x.u")); string(got) != before {
		t.Errorf("x.u is %q, want %q", got, before)
	}
}

// TestASecondSnapshotInAFolderHasNoEntryOfTheFirst: the snapshot before the hooks at a session's
// end is in the folder of the one before the hooks at its start; a submodule entry that a commit
// removed in between is not in it.
func TestASecondSnapshotInAFolderHasNoEntryOfTheFirst(t *testing.T) {
	dir, head := repo(t, map[string]string{"a.txt": "a\n"})
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
	git(t, dir, "commit", "--quiet", "-m", "a submodule")
	write(t, dir, "sub/x", "x\n")
	folder := t.TempDir()
	if _, err := hookrun.Take(context.Background(), dir, folder); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "rm", "--quiet", "--cached", "sub")
	git(t, dir, "commit", "--quiet", "-m", "no submodule")
	snap, err := hookrun.Take(context.Background(), dir, folder)
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "ls-tree", "-r", "--name-only", snap.Tree); got != "a.txt\nsub/x" {
		t.Errorf("the snapshot has %q", got)
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
	var ran []string
	for _, sub := range subs {
		if sub != "missing" {
			r, _ := filterRepo(t, dir, sub)
			ran = append(ran, r)
		}
		fmt.Fprintf(&entries, "160000 %s\t%s\x00", head, sub)
	}
	cmd := exec.Command("git", "update-index", "-z", "--index-info")
	cmd.Dir, cmd.Stdin = dir, strings.NewReader(entries.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git update-index: %v\n%s", err, out)
	}
	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "sub/a.txt", "changed\n")
	write(t, dir, "deep/er/new.txt", "new\n")
	write(t, dir, "new.txt", "new\n")
	if err := hookrun.AddAll(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	for _, r := range ran {
		notRan(t, r)
	}
	staged := git(t, dir, "-c", "core.quotePath=false", "ls-files", "-z", "--stage")
	for _, sub := range subs {
		if !strings.Contains(staged, "160000 "+head+" 0\t"+sub+"\x00") {
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

// TestAddAllFailsOnANewSubmoduleEntry: a repository that an add makes a submodule entry of the
// index is an error with its path, and the next add with that index leaves the entry out: the
// filter does not run.
func TestAddAllFailsOnANewSubmoduleEntry(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	ran, commit := filterRepo(t, dir, "nested")
	err := hookrun.AddAll(context.Background(), dir)
	if err == nil || err.Error() != "the checkout has a repository in nested" {
		t.Errorf("the error %v", err)
	}
	if got := git(t, dir, "ls-files", "--stage", "nested"); got != "160000 "+commit+" 0\tnested" {
		t.Errorf("the index has %q", got)
	}
	if err := hookrun.AddAll(context.Background(), dir); err != nil {
		t.Errorf("the second add returned %v", err)
	}
	notRan(t, ran)
}

// TestNoGitRunsInASubmodulesFolderInAnyLayout: no git reads the folder of a submodule entry of HEAD
// for the snapshot or a restore, with a .git file, with a .gitmodules, and with a HEAD that has a
// file and a folder of the same name, where the folder has the entry, in either order.
func TestNoGitRunsInASubmodulesFolderInAnyLayout(t *testing.T) {
	for _, c := range []struct {
		name, at string
		make     func(t *testing.T, dir string)
		fails    bool
	}{
		{"a .git file", "sub", func(t *testing.T, dir string) {
			outside := filepath.Join(t.TempDir(), "git")
			if err := os.Rename(filepath.Join(dir, "sub", ".git"), outside); err != nil {
				t.Fatal(err)
			}
			write(t, dir, "sub/.git", "gitdir: "+outside+"\n")
		}, false},
		{"a .gitmodules", "sub", func(t *testing.T, dir string) {
			write(t, dir, ".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = ./sub\n\tignore = none\n")
		}, false},
		{"a file and a folder d", "d/sub", func(t *testing.T, dir string) { fileAndFolder(t, dir, false) }, false},
		{"a folder d and a file", "d/sub", func(t *testing.T, dir string) { fileAndFolder(t, dir, true) }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
			ran, commit := filterRepo(t, dir, c.at)
			git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+commit+","+c.at)
			git(t, dir, "commit", "--quiet", "-m", "a submodule")
			c.make(t, dir)
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if c.fails {
				if err == nil || err.Error() != "the checkout has a repository in "+c.at {
					t.Errorf("the snapshot returned %v", err)
				}
				notRan(t, ran)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			notRan(t, ran)
			if err := snap.Restore(context.Background()); err != nil {
				t.Error(err)
			}
			notRan(t, ran)
		})
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

// TestAnEmptySubmoduleFolderThatAHookReplacedIsRestored: with the empty folder of a submodule
// entry, of a mode that a restore does not set, a hook that removes the folder, or puts a file or a
// link in its place or in the place of its parent, passes: the restore puts the folder back.
func TestAnEmptySubmoduleFolderThatAHookReplacedIsRestored(t *testing.T) {
	for _, c := range []struct {
		name, at string
		put      func(path string) error
	}{
		{"removed", "lib/sub", func(string) error { return nil }},
		{"a file", "lib/sub", func(path string) error { return os.WriteFile(path, []byte("x"), 0o644) }},
		{"a link", "lib/sub", func(path string) error { return os.Symlink(os.TempDir(), path) }},
		{"a file for the parent", "lib", func(path string) error { return os.WriteFile(path, []byte("x"), 0o644) }},
		{"a link for the parent", "lib", func(path string) error { return os.Symlink(os.TempDir(), path) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, head := repo(t, map[string]string{"a.txt": "a\n"})
			git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",lib/sub")
			git(t, dir, "commit", "--quiet", "-m", "a submodule")
			sub := filepath.Join(dir, "lib", "sub")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(sub, 0o775); err != nil {
				t.Fatal(err)
			}
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, filepath.FromSlash(c.at))
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := c.put(path); err != nil {
				t.Fatal(err)
			}
			if err := snap.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if entries, err := os.ReadDir(sub); err != nil || len(entries) != 0 {
				t.Errorf("lib/sub has %v, %v after the restore", entries, err)
			}
			if info, err := os.Lstat(sub); err != nil || !info.IsDir() {
				t.Errorf("lib/sub is %v, %v after the restore", info, err)
			}
		})
	}
}

// errContext is a context whose Err is err.
type errContext struct {
	context.Context
	err func() error
}

func (c errContext) Err() error { return c.err() }

// endsAfter is a context that ended once Err was called n times.
func endsAfter(n int) context.Context {
	return errContext{context.Background(), func() error {
		if n--; n < 0 {
			return context.Canceled
		}
		return nil
	}}
}

// TestALargeFileInASubmodulesFolderEndsWithTheContext: a read of a file of 64 MiB in the folder of
// a submodule entry, 2048 reads, ends with the context of the snapshot and of the restore.
func TestALargeFileInASubmodulesFolderEndsWithTheContext(t *testing.T) {
	for _, byHook := range []bool{false, true} {
		t.Run(fmt.Sprintf("by the hook %v", byHook), func(t *testing.T) {
			dir, head := repo(t, map[string]string{"a.txt": "a\n"})
			git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
			git(t, dir, "commit", "--quiet", "-m", "a submodule")
			write(t, dir, "sub/x", "x\n")
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(filepath.Join(dir, "sub", "x"), 64<<20); err != nil {
				t.Fatal(err)
			}
			if byHook {
				err = snap.Restore(endsAfter(1000))
			} else {
				_, err = hookrun.Take(endsAfter(1000), dir, t.TempDir())
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("the error %v", err)
			}
		})
	}
}

// TestTheWalkOfASubmodulesFolderEndsWithTheContext: the walk of folders with no file ends with the
// context.
func TestTheWalkOfASubmodulesFolderEndsWithTheContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub", "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	for n := range 3 {
		if _, err := hookrun.FolderState(endsAfter(n), dir, "sub"); !errors.Is(err, context.Canceled) {
			t.Errorf("after %d: the error %v", n, err)
		}
	}
}

// TestAnErrorOfAFolderStateHasThePathFromTheCheckout: an error of the walk of a folder has the
// folder's path from the checkout, not from the root of this machine. It keeps its cause.
func TestAnErrorOfAFolderStateHasThePathFromTheCheckout(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	_, err := hookrun.FolderState(context.Background(), dir, "sub/missing")
	if want := "lstat sub/missing: no such file or directory"; err == nil || err.Error() != want {
		t.Errorf("the error %v, want %q", err, want)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the error %v is not fs.ErrNotExist", err)
	}
}

// TestAFileThatChangesDuringTheReadIsAnError: a file that is not the one that the walk found, has
// another size, or gets shorter during the read is an error with its path in the checkout.
func TestAFileThatChangesDuringTheReadIsAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sub/x", "content\n")
	write(t, dir, "sub/y", "another\n")
	info := func(name string) fs.FileInfo {
		info, err := os.Lstat(filepath.Join(dir, "sub", name))
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	path := filepath.Join(dir, "sub", "x")
	before := info("x")
	if err := hookrun.AddFile(context.Background(), dir, path, before); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("sub", "x") + " changed while qory read it"
	if err := hookrun.AddFile(context.Background(), dir, path, info("y")); err == nil || err.Error() != want {
		t.Errorf("another file: %v", err)
	}
	shorter := errContext{context.Background(), func() error { return os.Truncate(path, 3) }}
	if err := hookrun.AddFile(shorter, dir, path, before); err == nil || err.Error() != want {
		t.Errorf("a shorter file: %v", err)
	}
	write(t, dir, "sub/x", "more content\n")
	if err := hookrun.AddFile(context.Background(), dir, path, before); err == nil || err.Error() != want {
		t.Errorf("another size: %v", err)
	}
}

// TestAFolderThatGoesAwayDuringTheWalkIsNoError: a folder that a process removes once the walk
// listed it fails neither the snapshot nor the restore.
func TestAFolderThatGoesAwayDuringTheWalkIsNoError(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	gone := filepath.Join(dir, "n")
	removes := func() context.Context {
		write(t, dir, "n/x", "x\n")
		calls := 0
		return errContext{context.Background(), func() error {
			calls++
			if _, err := os.Lstat(gone); err == nil && calls > 1 {
				return os.RemoveAll(gone)
			}
			return nil
		}}
	}
	snap, err := hookrun.Take(removes(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.Restore(removes()); err != nil {
		t.Error(err)
	}
}

// TestRestoreUndoesEveryChangeThatGitSees: after a snapshot of a checkout with the agent's
// changes left, a change of a tracked file, a removed file, a new file, a new folder, a file in
// place of a folder, and a new file that a new .gitignore hides are undone, the agent's changes
// stay, and a file under an ignored path stays as it was left.
func TestRestoreUndoesEveryChangeThatGitSees(t *testing.T) {
	dir, _ := repo(t, map[string]string{
		"src/main.go": "package main\n", "src/util/u.go": "package util\n", "README.md": "readme\n",
		".gitignore": "node_modules/\n",
	})
	// What the agent left: a change and a new file, not committed, and an ignored folder.
	write(t, dir, "README.md", "agent's readme\n")
	write(t, dir, "notes.txt", "agent's notes\n")
	write(t, dir, "node_modules/a/index.js", "setup\n")
	before := git(t, dir, "status", "--porcelain", "--untracked-files=all")
	folder := t.TempDir()
	snap, err := hookrun.Take(context.Background(), dir, folder)
	if err != nil {
		t.Fatal(err)
	}
	// The hook's changes.
	write(t, dir, "src/main.go", "package main // changed\n")
	write(t, dir, "README.md", "hook's readme\n")
	if err := os.Remove(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "src", "util")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "src/util", "a file in place of the folder\n")
	write(t, dir, "added.go", "package added\n")
	write(t, dir, "gen/deep/x.go", "package x\n")
	write(t, dir, "hidden/.gitignore", "added.go\n")
	write(t, dir, "hidden/added.go", "package added\n")
	write(t, dir, "node_modules/a/index.js", "hook\n")
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := git(t, dir, "status", "--porcelain", "--untracked-files=all"); after != before {
		t.Errorf("git status after the restore:\n%s\nwant:\n%s", after, before)
	}
	for path, want := range map[string]string{
		"src/main.go": "package main\n", "src/util/u.go": "package util\n", "README.md": "agent's readme\n",
		"notes.txt": "agent's notes\n", "node_modules/a/index.js": "hook\n",
	} {
		if got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path))); err != nil || string(got) != want {
			t.Errorf("%s is %q (%v), want %q", path, got, err, want)
		}
	}
	for _, path := range []string{"added.go", "gen", "hidden"} {
		if _, err := os.Lstat(filepath.Join(dir, path)); err == nil {
			t.Errorf("%s is still there", path)
		}
	}
}

// nextSecond waits until a second of the clock starts.
func nextSecond() { time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second))) }

// TestRestoreSeesAChangeOfTheSameSizeAndTime: a restore compares the bytes of a file whose size
// and time are the snapshot's, so a hook's change of it is undone.
func TestRestoreSeesAChangeOfTheSameSizeAndTime(t *testing.T) {
	for _, c := range []struct {
		name string
		at   func() time.Time
	}{
		{"in the second of the last change", nil},
		{"with an old time set back", func() time.Time { return time.Now().Add(-time.Hour) }},
		{"with the time 0 set back", func() time.Time { return time.Unix(0, 0) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"a.txt": "one\n"})
			path := filepath.Join(dir, "a.txt")
			nextSecond()
			change := func(content string, at time.Time) {
				write(t, dir, "a.txt", content)
				if c.at != nil {
					if err := os.Chtimes(path, at, at); err != nil {
						t.Fatal(err)
					}
				}
			}
			var at time.Time
			if c.at != nil {
				at = c.at()
			}
			change("two\n", at)
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			change("bad\n", at)
			nextSecond()
			if err := snap.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(path); string(got) != "two\n" {
				t.Errorf("a.txt is %q after the restore", got)
			}
		})
	}
}

// TestARestoreLeavesAnUnchangedFilesTime: a restore does not write a file that the hook left as
// it was, so its time stays.
func TestARestoreLeavesAnUnchangedFilesTime(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	path, at := filepath.Join(dir, "a.txt"), time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "b.txt", "the hook's\n")
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || info.ModTime().Unix() != at.Unix() {
		t.Errorf("the time of a.txt is %v, %v, want %v", info.ModTime(), err, at)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "b.txt")); string(got) != "b\n" {
		t.Errorf("b.txt is %q after the restore", got)
	}
}

// TestRestoreCoversFilesThatNewRulesHide: a restore applies the snapshot's .gitattributes and
// .gitignore, not the ones that a hook added or changed: each file has the snapshot's bytes, what
// the hook added is removed, and a file that the snapshot's rules match stays.
func TestRestoreCoversFilesThatNewRulesHide(t *testing.T) {
	for _, c := range []struct {
		name string
		hook map[string]string
	}{
		{"attributes", map[string]string{"z/.gitattributes": "* eol=crlf\n", "z/b.sh": "b\r\n"}},
		{"attributes and unchanged bytes", map[string]string{"z/.gitattributes": "* eol=crlf\n", "z/b.sh": "x\n"}},
		{"changed attributes", map[string]string{".gitattributes": "* eol=crlf\n", "z/b.sh": "b\r\n"}},
		{"hidden attributes", map[string]string{"z/.gitattributes": "* eol=crlf\n", "z/.gitignore": ".gitattributes\n", "z/b.sh": "b\r\n"}},
		{"rules that hide themselves", map[string]string{"z/.gitattributes": "* eol=crlf\n", "z/.gitignore": "*\n", "z/b.sh": "b\r\n", "z/added.sh": "added\n"}},
		{"rules at the top that hide themselves", map[string]string{".gitignore": "*\n", "added.sh": "added\n", "n/added.sh": "added\n"}},
		{"rules in hidden folders", map[string]string{"n/.gitignore": "*\n", "n/m/.gitignore": "*\n", "n/m/o/.gitignore": "*\n!.env\n", "n/m/o/added.sh": "added\n"}},
		{"rules that show a hidden file", map[string]string{"z/.gitignore": "!.env\n!.cache\n*\n", ".gitignore": "x\n", "z/.env": "kept\n"}},
		{"changed rules that hid themselves", map[string]string{".cache/.gitignore": "!*\n"}},
		{"rules in another case", map[string]string{"z/.GITIGNORE": "*\n", "z/added.sh": "added\n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{".gitattributes": "* text=auto\n", ".gitignore": ".env\n", "z/b.sh": "b\n", "crlf.txt": "c\r\n"})
			// What the setup left: a hidden file, a folder that hides itself, and bytes that the
			// attributes would change.
			kept := map[string]string{".env": "kept\n", "z/.env": "kept\n", ".cache/.gitignore": "*\n", ".cache/v/x": "kept\n", "crlf.txt": "c\r\nd\n", "z/b.sh": "b\n", ".gitignore": ".env\n"}
			for path, content := range kept {
				write(t, dir, path, content)
			}
			before := files(t, dir)
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for path, content := range c.hook {
				write(t, dir, path, content)
			}
			write(t, dir, "crlf.txt", "c\nd\n")
			if err := snap.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if after := files(t, dir); !maps.Equal(before, after) {
				t.Errorf("the checkout after the restore:\n%q\nwant:\n%q", after, before)
			}
		})
	}
}

// files is the content of each file of the checkout at dir, by its path, its .git left out.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	all := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(dir, path)
		switch {
		case err != nil:
			return err
		case rel == ".git":
			return filepath.SkipDir
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			all[filepath.ToSlash(rel)] = string(data)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// TestRestoreWritesNothingThroughALink: a hook that puts a link to a folder outside the checkout
// in place of a tracked folder leaves the outside folder as it was after the restore, and the
// tracked folder comes back.
func TestRestoreWritesNothingThroughALink(t *testing.T) {
	dir, _ := repo(t, map[string]string{"src/util/u.go": "package util\n", "src/main.go": "package main\n"})
	outside := t.TempDir()
	write(t, outside, "keep.txt", "outside\n")
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "src", "util")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "src", "util")); err != nil {
		t.Fatal(err)
	}
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Errorf("the outside folder has %v", entries)
	}
	if info, err := os.Lstat(filepath.Join(dir, "src", "util")); err != nil || !info.IsDir() {
		t.Errorf("src/util is %v, %v, want a folder", info, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "src", "util", "u.go")); string(got) != "package util\n" {
		t.Errorf("src/util/u.go is %q", got)
	}
}

// TestARepositoryInTheCheckoutFailsTheSnapshotAndTheRestore: with a repository, or another entry
// .git, in a folder below the top that is neither a submodule entry of HEAD nor ignored, as the
// agent leaves it, the snapshot is an error with the folder's path; with one that a hook made,
// the restore is. No git reads its folder.
func TestARepositoryInTheCheckoutFailsTheSnapshotAndTheRestore(t *testing.T) {
	for _, c := range []struct {
		name, at string
		make     func(t *testing.T, dir string) (ran string)
	}{
		{"a new folder", "nested", func(t *testing.T, dir string) string {
			ran, _ := filterRepo(t, dir, "nested")
			return ran
		}},
		{"a folder with tracked files", "src", func(t *testing.T, dir string) string {
			ran, _ := filterRepo(t, dir, "src")
			return ran
		}},
		{"a deep folder", "src/a/b", func(t *testing.T, dir string) string {
			ran, _ := filterRepo(t, dir, "src/a/b")
			return ran
		}},
		{"a folder where git ignores every file", "logs", func(t *testing.T, dir string) string {
			write(t, dir, "logs/.git/config", "[core]\n")
			write(t, dir, "logs/a.log", "log\n")
			return ""
		}},
		{"a file", "src", func(t *testing.T, dir string) string {
			write(t, dir, "src/.git", "gitdir: /nowhere\n")
			return ""
		}},
		{"a link", "src", func(t *testing.T, dir string) string {
			if err := os.Symlink("/nowhere", filepath.Join(dir, "src", ".git")); err != nil {
				t.Fatal(err)
			}
			return ""
		}},
		{"another case", "src", func(t *testing.T, dir string) string {
			write(t, dir, "src/.Git/config", "[core]\n")
			return ""
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, byHook := range []bool{false, true} {
				dir, _ := repo(t, map[string]string{"src/f": "f\n", ".gitignore": "*.log\n"})
				var ran string
				if !byHook {
					ran = c.make(t, dir)
				}
				snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
				if byHook {
					if err != nil {
						t.Fatal(err)
					}
					ran = c.make(t, dir)
					err = snap.Restore(context.Background())
				}
				if _, statErr := os.Lstat(filepath.Join(dir, "src", ".git")); byHook && c.name == "another case" && errors.Is(statErr, fs.ErrNotExist) {
					// git clean sees the name, and removed it.
					if err != nil {
						t.Error(err)
					}
					continue
				}
				if err == nil || err.Error() != "the checkout has a repository in "+c.at {
					t.Errorf("by the hook %v: the error %v, want the folder %s", byHook, err, c.at)
				}
				if ran != "" {
					notRan(t, ran)
				}
			}
		})
	}
}

// TestARepositoryMadeAfterTheWalkFails: a process that makes a repository once the walk found
// none, in a new folder, a folder with tracked files, or in place of a tracked file, fails the
// snapshot; one that does so during a check fails the restore, or the restore undoes it. No git
// reads the repository's folder.
func TestARepositoryMadeAfterTheWalkFails(t *testing.T) {
	for _, at := range []string{"nested", "src", "src/f"} {
		for _, inRestore := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s in the restore %v", at, inRestore), func(t *testing.T) {
				dir, _ := repo(t, map[string]string{"src/f": "f\n"})
				path, ran, once := filepath.Join(dir, filepath.FromSlash(at)), "", false
				writeLater := func() {
					t.Cleanup(hookrun.SetAfterWalk(func(dir string) {
						if once {
							return
						}
						once = true
						if at == "src/f" {
							if err := os.Remove(path); err != nil {
								t.Fatal(err)
							}
						}
						ran, _ = filterRepo(t, dir, at)
					}))
				}
				if !inRestore {
					writeLater()
				}
				snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
				if inRestore {
					if err != nil {
						t.Fatal(err)
					}
					writeLater()
					err = snap.Restore(context.Background())
				}
				if !once {
					t.Fatal("no repository was made")
				}
				_, gitErr := os.Lstat(filepath.Join(path, ".git"))
				// git lists no file .git, so a folder of tracked files with one made after the walk is as
				// the snapshot has it, and the next snapshot or restore finds it.
				left := at == "src" && err == nil
				if left {
					err = snap.Restore(context.Background())
				}
				if err == nil && gitErr == nil || err != nil && err.Error() != "the checkout has a repository in "+at {
					t.Errorf("the error %v, .git is there: %v", err, gitErr == nil)
				}
				notRan(t, ran)
			})
		}
	}
}

// TestATrackedFileThatAHookReplacedWithARepositoryIsRestored: a repository in place of a tracked
// file, or of a tracked folder, that a hook made is removed or fails the restore; one from before
// the hooks fails the snapshot. No git reads its folder.
func TestATrackedFileThatAHookReplacedWithARepositoryIsRestored(t *testing.T) {
	for _, c := range []struct {
		at    string
		fails bool
	}{{"src/f", false}, {"src", true}} {
		t.Run(c.at, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"src/f": "f\n", "a.txt": "a\n"})
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(c.at))); err != nil {
				t.Fatal(err)
			}
			ran, _ := filterRepo(t, dir, c.at)
			if _, err := hookrun.Take(context.Background(), dir, t.TempDir()); err == nil || err.Error() != "the checkout has a repository in "+c.at {
				t.Errorf("the snapshot returned %v", err)
			}
			if err := snap.Restore(context.Background()); c.fails != (err != nil) {
				t.Errorf("the restore returned %v", err)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "src", "f")); !c.fails && string(got) != "f\n" {
				t.Errorf("src/f is %q after the restore", got)
			}
			notRan(t, ran)
		})
	}
}

// TestARepositoryInAnIgnoredFolderPassesTheRestore: a read target under .qory, which the checkout's
// info/exclude hides, and repositories in folders that a .gitignore hides, pass the snapshot and
// a restore, and stay as they were.
func TestARepositoryInAnIgnoredFolderPassesTheRestore(t *testing.T) {
	dir, _ := repo(t, map[string]string{"src/f": "f\n", ".gitignore": "node_modules/\n/keep/deps/\n", "keep/k": "k\n"})
	write(t, dir, ".git/info/exclude", preparation.ExcludeLine+"\n")
	write(t, dir, ".qory/outbox/reply.json", "{}\n")
	write(t, dir, ".cache/.gitignore", "*\n")
	var all []string
	for _, sub := range []string{preparation.Repositories + "/acme/lib", "node_modules/x", "src/node_modules/y", "keep/deps/z", ".cache/r"} {
		ran, _ := filterRepo(t, dir, sub)
		all = append(all, ran)
	}
	before := files(t, dir)
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "src/f", "the hook's\n")
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := files(t, dir); !maps.Equal(before, after) {
		t.Errorf("the checkout after the restore:\n%q\nwant:\n%q", after, before)
	}
	for _, ran := range all {
		notRan(t, ran)
	}
}

// filterLater sets a clean and a smudge filter for every file of the checkout at dir, in its
// .git/config and its .gitattributes, once the next private git directory is made: as a process
// of the run's setup can write them while the hooks run. Each filter command creates the file
// ran in a folder outside the checkout, which the function returns.
func filterLater(t *testing.T, dir string) string {
	t.Helper()
	ran := filepath.Join(t.TempDir(), "ran")
	once := false
	t.Cleanup(hookrun.SetAfterCheck(func(string) {
		if once {
			return
		}
		once = true
		git(t, dir, "config", "filter.x.clean", "touch "+ran+"; cat")
		git(t, dir, "config", "filter.x.smudge", "touch "+ran+"; cat")
		write(t, dir, ".gitattributes", "* filter=x\n")
	}))
	return ran
}

// TestAFilterWrittenAfterTheCheckDoesNotRun: a clean and a smudge filter that .git/config and
// .gitattributes get after the check of .git, before git runs, do not run on this machine:
// neither for the snapshot, nor for a restore, nor for a program's read. The restore still
// writes back the hook's change.
func TestAFilterWrittenAfterTheCheckDoesNotRun(t *testing.T) {
	t.Run("take", func(t *testing.T) {
		dir, _ := repo(t, map[string]string{"README.md": "readme\n"})
		ran := filterLater(t, dir)
		if _, err := hookrun.Take(context.Background(), dir, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(ran); err == nil {
			t.Error("a filter ran for the snapshot")
		}
	})
	t.Run("restore", func(t *testing.T) {
		dir, _ := repo(t, map[string]string{"README.md": "readme\n"})
		snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, "README.md", "changed\n")
		ran := filterLater(t, dir)
		if err := snap.Restore(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(ran); err == nil {
			t.Error("a filter ran for the restore")
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(got) != "readme\n" {
			t.Errorf("README.md is %q", got)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".gitattributes")); err == nil {
			t.Error(".gitattributes stayed")
		}
		// The next round checks .git again and stops on the filter.
		if err := snap.Restore(context.Background()); err == nil {
			t.Error("a restore passed with a filter in .git/config")
		}
		if _, err := os.Lstat(ran); err == nil {
			t.Error("a filter ran for the second restore")
		}
	})
	t.Run("program", func(t *testing.T) {
		dir, commit := repo(t, map[string]string{"check.sh": "#!/bin/sh\n"})
		ran := filterLater(t, dir)
		if err := hookrun.Program(context.Background(), dir, commit, "check.sh", filepath.Join(t.TempDir(), "0")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(ran); err == nil {
			t.Error("a filter ran for the program's read")
		}
	})
}

// TestTakeReadsHEADFromPackedRefs: with the branch in packed-refs alone, the snapshot has HEAD's
// tree; with HEAD a link, it fails.
func TestTakeReadsHEADFromPackedRefs(t *testing.T) {
	dir, _ := repo(t, map[string]string{"README.md": "readme\n"})
	git(t, dir, "pack-refs", "--all", "--prune")
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if want := git(t, dir, "rev-parse", "HEAD^{tree}"); snap.Tree != want {
		t.Errorf("the snapshot's tree is %s, want HEAD's %s", snap.Tree, want)
	}
	head := filepath.Join(dir, ".git", "HEAD")
	if err := os.Rename(head, head+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("HEAD.real", head); err != nil {
		t.Fatal(err)
	}
	if _, err := hookrun.Take(context.Background(), dir, t.TempDir()); err == nil {
		t.Error("the snapshot passed with HEAD a link")
	}
}

// TestTakeStopsOnAFilterInTheCheckoutsConfig: a filter in .git/config, which git would run as a
// program on this machine for git add, stops the snapshot before any git adds a file.
func TestTakeStopsOnAFilterInTheCheckoutsConfig(t *testing.T) {
	dir, _ := repo(t, map[string]string{"a.dat": "abc\n", ".gitattributes": "*.dat filter=up\n"})
	marker := filepath.Join(t.TempDir(), "ran")
	git(t, dir, "config", "filter.up.clean", "touch "+marker+"; cat")
	write(t, dir, "a.dat", "abcd\n")
	if _, err := hookrun.Take(context.Background(), dir, t.TempDir()); err == nil {
		t.Error("the snapshot was taken")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the filter ran")
	}
}

// TestAnAttributesFileInAnIgnoredFolderFailsTheRestore: a .gitattributes that git ignores, in a
// folder above a tracked file, fails the restore when a hook adds, changes or removes it, and
// passes when the hook leaves it as it was. One in an ignored folder with no tracked file passes.
func TestAnAttributesFileInAnIgnoredFolderFailsTheRestore(t *testing.T) {
	for _, c := range []struct {
		name, before, after string
		at                  string
		fails               bool
	}{
		{"added", "", "* eol=crlf\n", "ig/d/.gitattributes", true},
		{"added above", "", "* eol=crlf\n", "ig/.gitattributes", true},
		{"changed", "* eol=lf\n", "* eol=crlf\n", "ig/d/.gitattributes", true},
		{"removed", "* eol=lf\n", "", "ig/d/.gitattributes", true},
		{"unchanged", "* eol=lf\n", "* eol=lf\n", "ig/d/.gitattributes", false},
		{"no tracked file", "", "* eol=crlf\n", "ig/e/.gitattributes", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"a.txt": "a\n", ".gitignore": "ig/\n"})
			write(t, dir, "ig/d/f", "f\n")
			git(t, dir, "add", "--force", "ig/d/f")
			git(t, dir, "commit", "--quiet", "-m", "a file in an ignored folder")
			state := func(content string) {
				if content != "" {
					write(t, dir, c.at, content)
				} else if err := os.Remove(filepath.Join(dir, filepath.FromSlash(c.at))); err != nil && !errors.Is(err, fs.ErrNotExist) {
					t.Fatal(err)
				}
			}
			state(c.before)
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			state(c.after)
			err = snap.Restore(context.Background())
			if want := "a hook changed " + c.at + " in an ignored folder with tracked files"; c.fails && (err == nil || err.Error() != want) || !c.fails && err != nil {
				t.Errorf("the error %v", err)
			}
		})
	}
}

// TestANewFileInAnIgnoredFolderWithTrackedFilesStays: an ignored folder has a tracked file and a
// submodule entry with its folder. A hook's new file below that folder stays as the hook left it.
// The restore writes the tracked file back.
func TestANewFileInAnIgnoredFolderWithTrackedFilesStays(t *testing.T) {
	dir, head := repo(t, map[string]string{"a.txt": "a\n", ".gitignore": "ig/\n"})
	write(t, dir, "ig/d/f", "f\n")
	git(t, dir, "add", "--force", "ig/d/f")
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",ig/sub")
	git(t, dir, "commit", "--quiet", "-m", "a file and a submodule in an ignored folder")
	if err := os.Mkdir(filepath.Join(dir, "ig", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "ig/d/new.txt", "hook\n")
	write(t, dir, "ig/d/f", "changed\n")
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"ig/d/new.txt": "hook\n", "ig/d/f": "f\n"} {
		if got := string(read(t, filepath.Join(dir, filepath.FromSlash(path)))); got != want {
			t.Errorf("%s is %q, want %q", path, got, want)
		}
	}
}

// TestAPipeOrSocketThatAHookMadeFailsTheRestore: a pipe or a socket that a hook made in a folder
// that git does not ignore fails the restore with its path. One from before the hooks passes, and
// so does one in an ignored folder; one in place of a tracked file is undone.
func TestAPipeOrSocketThatAHookMadeFailsTheRestore(t *testing.T) {
	pipe := func(t *testing.T, path string) {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	socket := func(t *testing.T, path string) {
		// A socket's path has at most 103 bytes on macOS, which a test's folder can pass.
		short := t.TempDir()
		if len(short) > 100 {
			var err error
			if short, err = os.MkdirTemp("", "q"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(short) })
		}
		l, err := net.Listen("unix", filepath.Join(short, "s"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		if err := os.Rename(filepath.Join(short, "s"), path); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, at string
		make     func(t *testing.T, path string)
		before   bool
		fails    bool
	}{
		{"a pipe", "d/new", pipe, false, true},
		{"a pipe at the top", "new", pipe, false, true},
		{"a socket", "d/new", socket, false, true},
		{"a pipe from before", "d/new", pipe, true, false},
		{"a pipe in an ignored folder", "ig/new", pipe, false, false},
		{"a pipe in place of a file", "d/f", pipe, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"d/f": "f\n", ".gitignore": "ig/\n"})
			if err := os.Mkdir(filepath.Join(dir, "ig"), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, filepath.FromSlash(c.at))
			if c.before {
				c.make(t, path)
			}
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if !c.before {
				os.Remove(path)
				c.make(t, path)
			}
			err = snap.Restore(context.Background())
			if want := "a hook made the pipe, socket or device " + c.at; c.fails && (err == nil || err.Error() != want) || !c.fails && err != nil {
				t.Errorf("the error %v", err)
			}
			if got, _ := os.ReadFile(filepath.Join(dir, "d", "f")); string(got) != "f\n" {
				t.Errorf("d/f is %q", got)
			}
		})
	}
}

// TestRestorePutsBackTheModes: a restore puts back the mode of a file and of a folder above one,
// with the setuid, setgid and sticky bits, that a hook changed, also of a file that the hook
// wrote again.
func TestRestorePutsBackTheModes(t *testing.T) {
	dir, _ := repo(t, map[string]string{"d/f": "f\n", "d/g": "g\n", "d/h": "h\n"})
	chmod := func(name string, mode fs.FileMode) {
		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]fs.FileMode{"d": 0o750, "d/f": 0o700, "d/g": 0o740, "d/h": 0o755}
	for name, mode := range want {
		chmod(name, mode)
	}
	// A file of a group that the user is not in loses the setgid bit on macOS, with no error.
	if err := os.Lchown(filepath.Join(dir, "d", "h"), -1, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chmod("d", 0o777)
	chmod("d/f", 0o777|fs.ModeSetuid)
	write(t, dir, "d/g", "changed\n")
	chmod("d/h", 0o755|fs.ModeSetgid)
	if info, err := os.Lstat(filepath.Join(dir, "d", "h")); err != nil || info.Mode()&fs.ModeSetgid == 0 {
		t.Fatalf("d/h has no setgid bit before the restore: %v", err)
	}
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, mode := range want {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() &^ fs.ModeDir; got != mode {
			t.Errorf("%s has the mode %v, want %v", name, got, mode)
		}
	}
}

// TestRestoreRemovesAFileInAFolderThatAHookMadeReadOnly: a restore puts back the mode of a
// folder before it removes the file that a hook left there. The hook sets the folder to 0555 or 0.
func TestRestoreRemovesAFileInAFolderThatAHookMadeReadOnly(t *testing.T) {
	for _, mode := range []fs.FileMode{0o555, 0} {
		t.Run(fmt.Sprintf("%04o", uint32(mode)), func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"d/f": "f\n"})
			d := filepath.Join(dir, "d")
			t.Cleanup(func() { os.Chmod(d, 0o755) })
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			write(t, dir, "d/new", "new\n")
			if err := os.Chmod(d, mode); err != nil {
				t.Fatal(err)
			}
			if err := snap.Restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(d, "new")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("d/new is still there: %v", err)
			}
			if info, err := os.Lstat(d); err != nil || info.Mode().Perm() != 0o755 {
				t.Errorf("d has the mode %v, %v", info.Mode(), err)
			}
		})
	}
}

// TestRestoreWritesInAFolderThatIsReadOnlyInTheSnapshot: the folder d is read-only in the
// snapshot, and a hook made it writable. A restore writes and removes files in d, then puts back
// its mode.
func TestRestoreWritesInAFolderThatIsReadOnlyInTheSnapshot(t *testing.T) {
	dir, _ := repo(t, map[string]string{"d/f": "f\n"})
	d := filepath.Join(dir, "d")
	t.Cleanup(func() { os.Chmod(d, 0o755) })
	if err := os.Chmod(d, 0o555); err != nil {
		t.Fatal(err)
	}
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "d/new", "new\n")
	write(t, dir, "d/f", "changed\n")
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(d, "new")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("d/new is still there: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(d, "f")); err != nil || string(got) != "f\n" {
		t.Errorf("d/f has %q, %v, want %q", got, err, "f\n")
	}
	if info, err := os.Lstat(d); err != nil || info.Mode().Perm() != 0o555 {
		t.Errorf("d has the mode %v, %v, want 0555", info.Mode(), err)
	}
}

// TestRestoreGoesOnWhenItCannotMakeAFolderWritable: the folder d is read-only in the snapshot.
// Each chmod fails, as for a folder of another user. In the first two cases the hook does not
// change d: a permission error lets the restore go on, and another error fails it. When the hook
// made d writable, the last pass must set its mode, so a permission error there fails the restore.
func TestRestoreGoesOnWhenItCannotMakeAFolderWritable(t *testing.T) {
	for _, c := range []struct {
		name     string
		err      error
		writable bool
		fails    bool
	}{
		{"permission", syscall.EPERM, false, false},
		{"other", syscall.EIO, false, true},
		{"last pass", syscall.EPERM, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"a.txt": "a\n", "d/f": "f\n"})
			d := filepath.Join(dir, "d")
			t.Cleanup(func() { os.Chmod(d, 0o755) })
			if err := os.Chmod(d, 0o555); err != nil {
				t.Fatal(err)
			}
			snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			write(t, dir, "a.txt", "changed\n")
			if c.writable {
				if err := os.Chmod(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			defer hookrun.SetChmod(func(_ *os.Root, name string, _ fs.FileMode) error {
				return &fs.PathError{Op: "chmod", Path: name, Err: c.err}
			})()
			err = snap.Restore(context.Background())
			if c.fails != (err != nil) || c.fails && !errors.Is(err, c.err) {
				t.Fatalf("the error %v", err)
			}
			if c.fails {
				return
			}
			if got, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(got) != "a\n" {
				t.Errorf("a.txt has %q, %v, want %q", got, err, "a\n")
			}
		})
	}
}

// TestRestorePutsBackAFolderBeforeTheFoldersBelowIt: a hook set d and d/e to mode 0. A restore
// sets d first, so it can set d/e and remove the file that the hook left there.
func TestRestorePutsBackAFolderBeforeTheFoldersBelowIt(t *testing.T) {
	dir, _ := repo(t, map[string]string{"d/e/f": "f\n"})
	d, e := filepath.Join(dir, "d"), filepath.Join(dir, "d", "e")
	t.Cleanup(func() { os.Chmod(d, 0o755); os.Chmod(e, 0o755) })
	snap, err := hookrun.Take(context.Background(), dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "d/e/new", "new\n")
	for _, at := range []string{e, d} {
		if err := os.Chmod(at, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := snap.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(e, "new")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("d/e/new is still there: %v", err)
	}
}

// TestAFolderThatQoryCannotReadIsAnErrorWithItsPath: a folder that qory cannot read fails the
// snapshot with the folder's path from the checkout.
func TestAFolderThatQoryCannotReadIsAnErrorWithItsPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	for _, c := range []struct {
		name string
		mode fs.FileMode
		want string
	}{
		{"no access", 0, "lstat u/.git: permission denied"},
		{"no read", 0o300, "open u: permission denied"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
			write(t, dir, "u/x", "x\n")
			u := filepath.Join(dir, "u")
			t.Cleanup(func() { os.Chmod(u, 0o755) })
			if err := os.Chmod(u, c.mode); err != nil {
				t.Fatal(err)
			}
			_, err := hookrun.Take(context.Background(), dir, t.TempDir())
			if err == nil || err.Error() != c.want {
				t.Errorf("the error %v, want %q", err, c.want)
			}
		})
	}
}

// TestTakeStopsOnStatKeysInTheCheckoutsConfig: the keys of .git/config for the comparison of a
// file's size and times stop the snapshot.
func TestTakeStopsOnStatKeysInTheCheckoutsConfig(t *testing.T) {
	for _, key := range []string{"core.trustctime", "core.checkStat", "core.ignoreStat", "core.untrackedCache"} {
		dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
		git(t, dir, "config", key, "false")
		if _, err := hookrun.Take(context.Background(), dir, t.TempDir()); err == nil {
			t.Errorf("the snapshot was taken with %s", key)
		}
	}
}

// TestNoGitOfAHookFetchesAMissingObject: a git of the hooks fetches no object that the repository
// does not have, whatever the environment of this process sets.
func TestNoGitOfAHookFetchesAMissingObject(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_NO_LAZY_FETCH", "0")
	dir, _ := repo(t, map[string]string{"a.txt": "a\n"})
	ran := filepath.Join(t.TempDir(), "ran")
	git(t, dir, "config", "extensions.partialClone", "x")
	git(t, dir, "config", "remote.x.url", "file://"+dir)
	git(t, dir, "config", "remote.x.promisor", "true")
	git(t, dir, "config", "remote.x.uploadpack", "touch "+ran+"; false")
	cmd, err := hookrun.GitCommand(context.Background(), dir, "cat-file", "-p", strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("git found the object: %s", out)
	}
	if _, err := os.Lstat(ran); err == nil {
		t.Error("git ran the remote's program")
	}
	last := map[string]string{}
	for _, kv := range cmd.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok {
			last[name] = kv
		}
	}
	if !hostgit.NoProtocol(last["GIT_ALLOW_PROTOCOL"]) {
		t.Errorf("git runs with %q, not the protocol of hostgit", last["GIT_ALLOW_PROTOCOL"])
	}
	if last["GIT_NO_LAZY_FETCH"] != "GIT_NO_LAZY_FETCH=1" {
		t.Errorf("git runs with %q, want GIT_NO_LAZY_FETCH=1", last["GIT_NO_LAZY_FETCH"])
	}
}

// TestHeadReadsNoConfiguration: Head is the commit of HEAD, also with a configuration in .git that
// no git reads without an error, and an error for a repository with no commit.
func TestHeadReadsNoConfiguration(t *testing.T) {
	dir, head := repo(t, nil)
	write(t, dir, ".git/config", "[not a configuration\n")
	if got, err := hookrun.Head(dir); got != head || err != nil {
		t.Errorf("Head is %q, %v, want %s", got, err, head)
	}
	empty := t.TempDir()
	git(t, empty, "init", "--quiet")
	if got, err := hookrun.Head(empty); err == nil {
		t.Errorf("Head of a repository with no commit is %q", got)
	}
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
