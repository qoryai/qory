package hostgit_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
)

// The steps git 2.55 takes to find a repository (setup.c: setup_git_directory_gently_1,
// read_gitfile_gently, is_git_directory, get_common_dir_noenv, ensure_valid_ownership),
// what each step reads, the sum the kept check watches of it, and the test that proves
// a change there makes the kept check run the full check again. A step marked "Sum:
// none" lists why the check watches nothing of it; for those steps, a match of every sum
// does not show that git takes the same git directory.
//
//  1. GIT_DIR, GIT_COMMON_DIR and GIT_OBJECT_DIRECTORY name the git directory, the common
//     directory and the objects without a search. Sum: none; [hostgit.Env] drops them.
//     Test: TestEnvDropsWhatAddsConfigurationOrChoosesARepository.
//  2. GIT_CEILING_DIRECTORIES, GIT_DISCOVERY_ACROSS_FILESYSTEM and the device of each
//     folder end the search below the root. Sum: none; they come from qory's
//     environment and the host's mounts, and they end git's search no further up than
//     the check's walk, which goes to the root. Test: none.
//  3. git starts in the real path of dir, as getcwd returns it. Sum: dir, as "dir" and
//     its real path. Test: TestAFolderTheCheckStartsInThatBecomesALinkKeepsNoMatch.
//  4. At each folder, git stats its .git, following links. Nothing there, or a link that
//     leads nowhere: the next step. A folder: step 5 tests it. A file: git reads
//     "gitdir: " and a path, relative to the folder, from the file as it reads a
//     commondir in step 5, and step 5 tests that path, then takes its real path. Any
//     other stat error, a pipe, a file over 1 MiB or one git cannot read or parse, and
//     a path that is no git directory: git exits with an error. Sum: each .git nearer
//     than the one that leads to the git directory, with the prefix passed: "-" while
//     nothing is there, no match otherwise; the one that leads to it by its bytes, or
//     "dir" and its real path. Tests:
//     TestAGitFolderGitLooksPastKeepsNoMatch,
//     TestAGitFolderGitLooksPastToABareRepositoryKeepsNoMatch, TestAMovedGitIsRefused.
//  5. is_git_directory: HEAD by lstat, a link whose text begins with refs/, or a file
//     whose first 255 bytes are "ref: refs/..." or an object id; commondir, if stat
//     finds it, read whole, with the line ends at its end stripped and the rest taken up
//     to the first NUL byte, appended to the folder, as a real path; objects and refs in
//     that common directory, by access with X_OK. Sum: [usable], with the prefix
//     isgitdir:, of the git directory git uses, which reads commondir the same way and
//     matches nothing for a commondir over 1 MiB, and the git directory and the common
//     directory as "dir" and their real paths. Tests:
//     TestAGitDirectoryGitStopsUsingKeepsNoMatch,
//     TestALinkOnTheWayToTheCommonDirectoryKeepsNoMatch,
//     TestACommondirReadAsGitReadsItKeepsNoMatch.
//  6. ensure_valid_ownership: the owners of the .git file, the work tree and the git
//     directory, by lstat, and safe.directory in the global and system files. Sum: the
//     owner of each folder git tests, in [usable]. A .git file or a work tree another
//     user comes to own makes git exit with an error, unless safe.directory names it.
//     When git exits, every .git that is there has the prefix passed and matches no
//     sum. The global and system files are not watched. Test:
//     TestAnOwnerGitTrustsAgainKeepsNoMatch, as root only.
//  7. With no .git git takes, is_git_directory tests the folder itself as a bare
//     repository, as in step 5, and safe.bareRepository in the global and system files
//     may refuse it. Sum: [usable], with the prefix isgitdir:, of each folder from the
//     real path of dir up to the folder whose .git leads to the git directory or that is
//     the git directory, without that folder, and up to the root when git takes none.
//     Tests: TestAFolderGitTakesForABareRepositoryKeepsNoMatch,
//     TestACommondirReadAsGitReadsItKeepsNoMatch.
//  8. git goes up to the parent folder by the text of the path, until the root. Sum: the
//     parents of the real path of dir, which the sum of step 3 keeps. Test:
//     TestAFolderTheCheckStartsInThatBecomesALinkKeepsNoMatch.

// isolated is [hermetic]: HOME, the four XDG directories and the ceiling of git's
// search in temporary directories for the test alone.
func isolated(t *testing.T) {
	t.Helper()
	hermetic(t)
}

// revParse runs git rev-parse --absolute-git-dir through hostgit in dir and returns the
// refusal, nil when the check let git run. git's own exit does not fail the test: a
// bare repository has no work tree for git status, and a repository git does not
// trust makes it exit.
func revParse(t *testing.T, dir string) error {
	t.Helper()
	c, err := hostgit.Command(context.Background(), dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	_ = c.Run()
	return nil
}

// keep checks dir 3 times, each let through, so that the check keeps what it read.
func keep(t *testing.T, dir string) {
	t.Helper()
	for i := 0; i < 3; i++ {
		if err := revParse(t, dir); err != nil {
			t.Fatalf("check %d as recorded: %v", i+1, err)
		}
	}
}

// bareAt makes dir a bare repository by hand, as a run could: a HEAD, an objects and a
// refs folder, and a config that sets core.sshCommand.
func bareAt(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"HEAD": "ref: refs/heads/main\n", "config": "[core]\n\tsshCommand = touch ran\n"}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// gitDirFrom is the git directory git uses from dir, without hostgit.
func gitDirFrom(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "rev-parse", "--absolute-git-dir")
}

// TestAFolderGitTakesForABareRepositoryKeepsNoMatch checks a folder of a recorded place
// 3 times, so that the check keeps what it read. A run then makes a folder on git's way
// up a bare repository whose config sets core.sshCommand: git takes it, the check from
// the folder reads its config, and refuses the key.
func TestAFolderGitTakesForABareRepositoryKeepsNoMatch(t *testing.T) {
	t.Run("one level", func(t *testing.T) {
		isolated(t)
		dir := newRepo(t)
		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		record(t, dir)
		keep(t, sub)
		bareAt(t, sub)
		if got := gitDirFrom(t, sub); got != sub {
			t.Fatalf("git uses %s, want %s", got, sub)
		}
		refusedKey(t, revParse(t, sub), sub, "core.sshcommand")
	})
	t.Run("two levels", func(t *testing.T) {
		isolated(t)
		dir := newRepo(t)
		a := filepath.Join(dir, "a")
		b := filepath.Join(a, "b")
		if err := os.MkdirAll(b, 0o755); err != nil {
			t.Fatal(err)
		}
		record(t, dir)
		keep(t, b)
		bareAt(t, a)
		if got := gitDirFrom(t, b); got != a {
			t.Fatalf("git uses %s, want %s", got, a)
		}
		refusedKey(t, revParse(t, b), b, "core.sshcommand")
	})
	t.Run("in a bare repository", func(t *testing.T) {
		isolated(t)
		base, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		bare := filepath.Join(base, "b.git")
		git(t, base, "init", "-q", "--bare", bare)
		x := filepath.Join(bare, "info", "x")
		if err := os.MkdirAll(x, 0o755); err != nil {
			t.Fatal(err)
		}
		record(t, bare)
		keep(t, x)
		bareAt(t, x)
		if got := gitDirFrom(t, x); got != x {
			t.Fatalf("git uses %s, want %s", got, x)
		}
		refusedKey(t, revParse(t, x), x, "core.sshcommand")
	})
}

// TestAFolderTheCheckStartsInThatBecomesALinkKeepsNoMatch checks sub of a recorded
// checkout 3 times, so that the check keeps what it read. A run then puts a link at sub
// to a folder inside a bare repository whose config sets core.sshCommand: git starts in
// the folder the link leads to and takes the bare repository, and the check refuses.
func TestAFolderTheCheckStartsInThatBecomesALinkKeepsNoMatch(t *testing.T) {
	isolated(t)
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	record(t, dir)
	keep(t, sub)
	elsewhere := filepath.Join(dir, "elsewhere")
	bareAt(t, elsewhere)
	if err := os.Mkdir(filepath.Join(elsewhere, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sub, filepath.Join(dir, "sub.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("elsewhere", "inner"), sub); err != nil {
		t.Fatal(err)
	}
	if got := gitDirFrom(t, sub); got != elsewhere {
		t.Fatalf("git uses %s, want %s", got, elsewhere)
	}
	refusedKey(t, revParse(t, sub), sub, "core.sshcommand")
}

// TestALinkOnTheWayToTheCommonDirectoryKeepsNoMatch records a checkout whose .git leads
// to the git directory g/x/wt, whose commondir is ../.., so that g is the common
// directory, and checks it 3 times, so that the check keeps what it read. A run then
// copies x to evil/a/b, puts an objects and a refs folder and a config that sets
// core.sshCommand in evil/a, and puts a link to evil/a/b where x was. The .git, the git
// directory's HEAD and commondir and g's config read as before through the link, but
// git resolves the link before it takes ../.. from the git directory, so evil/a is the
// common directory, and the check refuses.
func TestALinkOnTheWayToTheCommonDirectoryKeepsNoMatch(t *testing.T) {
	isolated(t)
	dir := newRepo(t)
	g := filepath.Join(dir, "g")
	if err := os.Rename(filepath.Join(dir, ".git"), g); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(g, "x", "wt")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(gitDir, "HEAD"):      "ref: refs/heads/main\n",
		filepath.Join(gitDir, "commondir"): "../..\n",
		filepath.Join(dir, ".git"):         "gitdir: g/x/wt\n",
	}
	for path, data := range files {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := git(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != g {
		t.Fatalf("git uses the common directory %s, want %s", got, g)
	}
	record(t, dir)
	keep(t, dir)
	evil := filepath.Join(dir, "evil", "a")
	if err := os.CopyFS(filepath.Join(evil, "b"), os.DirFS(filepath.Join(g, "x"))); err != nil {
		t.Fatal(err)
	}
	bareAt(t, evil)
	if err := os.Rename(filepath.Join(g, "x"), filepath.Join(g, "x.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(evil, "b"), filepath.Join(g, "x")); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir"); got != evil {
		t.Fatalf("git uses the common directory %s, want %s", got, evil)
	}
	err := revParse(t, dir)
	var r *hostgit.RefusedError
	if !errors.As(err, &r) || r.Reason != hostgit.RefusedPointer || err.Error() != "the checkout's .git points somewhere else since a walled run began here; qory runs no git there, since the run could have pointed it. qory setup repo --accept-checkout accepts it" {
		t.Fatalf("got %v, want the refusal of a .git that points somewhere else", err)
	}
}

// TestAnOwnerGitTrustsAgainKeepsNoMatch checks a recorded bare repository 3 times while
// another user owns it, so that git exits on it and the check keeps what it read. A run
// then sets core.sshCommand in its config and gives it back to this process's user: git
// takes it again, and the check refuses the key. Only root can give a folder to
// another user.
func TestAnOwnerGitTrustsAgainKeepsNoMatch(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a folder's owner needs root")
	}
	isolated(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(base, "b.git")
	git(t, base, "init", "-q", "--bare", bare)
	record(t, bare)
	owner := func(uid int) {
		t.Helper()
		err := filepath.WalkDir(bare, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(path, uid, uid)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	owner(4242)
	keep(t, bare)
	f, err := os.OpenFile(filepath.Join(bare, "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[core]\n\tsshCommand = touch ran\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	owner(0)
	if got := gitDirFrom(t, bare); got != bare {
		t.Fatalf("git uses %s, want %s", got, bare)
	}
	refusedKey(t, revParse(t, bare), bare, "core.sshcommand")
}

// TestACommondirReadAsGitReadsItKeepsNoMatch checks a folder of a recorded checkout 3
// times, so that the check keeps what it read, while a commondir names a common
// directory git does not find. A run then makes that common directory, or moves the
// objects of the one git used away, so that git reads a config that sets
// core.sshCommand, and the check refuses the key. git reads a commondir whole, strips
// the line ends at its end, and reads what is left up to the first NUL byte.
func TestACommondirReadAsGitReadsItKeepsNoMatch(t *testing.T) {
	onTheWayUp := func(t *testing.T, text func(sub string) string) {
		isolated(t)
		dir := newRepo(t)
		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{"HEAD": "ref: refs/heads/main\n", "commondir": text(sub)}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(sub, name), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := gitDirFrom(t, sub); got != filepath.Join(dir, ".git") {
			t.Fatalf("git uses %s, want %s", got, filepath.Join(dir, ".git"))
		}
		record(t, dir)
		keep(t, sub)
		evil := filepath.Join(sub, "evil")
		bareAt(t, evil)
		if got := gitDirFrom(t, sub); got != sub {
			t.Fatalf("git uses %s, want %s", got, sub)
		}
		refusedKey(t, revParse(t, sub), sub, "core.sshcommand")
	}
	t.Run("a NUL byte on the way up", func(t *testing.T) {
		onTheWayUp(t, func(string) string { return "evil\x00junk\n" })
	})
	t.Run("a file over 64 KiB on the way up", func(t *testing.T) {
		onTheWayUp(t, func(string) string { return strings.Repeat("./", 40000) + "evil\n" })
	})
	// The first 1 MiB of the file are slashes, which name the root folder; git reads
	// the whole file, which names sub/evil.
	t.Run("a file over 1 MiB on the way up", func(t *testing.T) {
		onTheWayUp(t, func(sub string) string { return strings.Repeat("/", 1<<20) + sub + "/evil\n" })
	})
	t.Run("a NUL byte in the git directory in use", func(t *testing.T) {
		isolated(t)
		dir := newRepo(t)
		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, sub, "init", "-q")
		g := filepath.Join(sub, ".git")
		c := filepath.Join(g, "c")
		if err := os.Mkdir(c, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"objects", "refs", "config"} {
			if err := os.Rename(filepath.Join(g, name), filepath.Join(c, name)); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(g, "commondir"), []byte("c\x00junk\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := gitDirFrom(t, sub); got != g {
			t.Fatalf("git uses %s, want %s", got, g)
		}
		record(t, dir)
		keep(t, sub)
		git(t, dir, "config", "core.sshCommand", "touch ran")
		if err := os.Rename(filepath.Join(c, "objects"), filepath.Join(c, "objects.old")); err != nil {
			t.Fatal(err)
		}
		if got := gitDirFrom(t, sub); got != filepath.Join(dir, ".git") {
			t.Fatalf("git uses %s, want %s", got, filepath.Join(dir, ".git"))
		}
		refusedKey(t, revParse(t, sub), sub, "core.sshcommand")
	})
}

// TestALineEndInAPathGitPrintsStays checks folders whose git directory, common
// directory or work tree has a line end in its name. git rev-parse prints such a path
// unchanged, with one line end after it: the check reads the whole path, lets git run
// in the recorded repository, and refuses a key a run adds to the configuration git
// reads there.
func TestALineEndInAPathGitPrintsStays(t *testing.T) {
	commondir := func(t *testing.T) (dir, sub string) {
		isolated(t)
		dir = newRepo(t)
		sub = filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{"HEAD": "ref: refs/heads/main\n", "commondir": "evil\n\x00x"}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(sub, name), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir, sub
	}
	t.Run("a common directory, through the full check", func(t *testing.T) {
		dir, sub := commondir(t)
		bareAt(t, filepath.Join(sub, "evil\n"))
		c := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
		c.Dir = sub
		if out, err := c.Output(); err != nil || string(out) != filepath.Join(sub, "evil")+"\n\n" {
			t.Fatalf("git prints the common directory %q, %v, want %q", out, err, filepath.Join(sub, "evil")+"\n\n")
		}
		record(t, dir)
		refusedKey(t, revParse(t, sub), sub, "core.sshcommand")
	})
	t.Run("a common directory, through the kept check", func(t *testing.T) {
		dir, sub := commondir(t)
		record(t, dir)
		keep(t, sub)
		bareAt(t, filepath.Join(sub, "evil\n"))
		if got := gitDirFrom(t, sub); got != sub {
			t.Fatalf("git uses %s, want %s", got, sub)
		}
		refusedKey(t, revParse(t, sub), sub, "core.sshcommand")
	})
	t.Run("a work tree", func(t *testing.T) {
		isolated(t)
		base, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(base, "a\nb")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "init", "-q", "--initial-branch=main")
		record(t, dir)
		if err := revParse(t, dir); err != nil {
			t.Fatalf("check as recorded: %v", err)
		}
		git(t, dir, "config", "core.sshCommand", "touch ran")
		refusedKey(t, revParse(t, dir), hostgit.Shown(dir), "core.sshcommand")
	})
}
