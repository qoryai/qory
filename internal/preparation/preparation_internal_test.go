package preparation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/proxygit"
)

// shellQuote is s as one word of a shell script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// needGit skips a test that runs git when the machine has none, and keeps the user's
// and the system's git configuration out of it.
func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Example")
	t.Setenv("GIT_AUTHOR_EMAIL", "example@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Example")
	t.Setenv("GIT_COMMITTER_EMAIL", "example@example.com")
}

// run runs git in dir and returns its output, trimmed, failing the test on an error.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// remote makes the repository base/path.git, bare, with two commits on branch, its
// default, and files in the second, and points the fetches at base.
func remote(t *testing.T, base, path, branch string, files map[string]string) {
	t.Helper()
	work := t.TempDir()
	run(t, work, "init", "--quiet", "-b", branch)
	write(t, filepath.Join(work, "first.txt"), "first\n")
	run(t, work, "add", "-A")
	run(t, work, "commit", "--quiet", "-m", "first")
	if files == nil {
		files = map[string]string{"README.md": path + "\n"}
	}
	for name, content := range files {
		write(t, filepath.Join(work, filepath.FromSlash(name)), content)
	}
	run(t, work, "add", "-A")
	run(t, work, "commit", "--quiet", "-m", "second")
	dest := filepath.Join(base, filepath.FromSlash(path)+".git")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, base, "clone", "--quiet", "--bare", work, dest)
	setBase(t, base)
}

// setBase points the fetches at the repositories under base, over file://, which the
// fetches then take beside https.
func setBase(t *testing.T, base string) {
	t.Helper()
	old, oldProtocols := baseURL, protocols
	baseURL = "file://" + filepath.ToSlash(base)
	protocols = append(slices.Clone(protocols), "-c", "protocol.file.allow=always")
	t.Cleanup(func() { baseURL, protocols = old, oldProtocols })
}

// testProxy is the host proxy of the tests that fetch over file://, which git reaches
// with no proxy: its port takes no connection.
var testProxy = proxygit.Proxy{URL: "http://qory:test-proxy-password@127.0.0.1:9"}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// emptyFolder is a fresh git working tree on master, which no remote here uses.
func emptyFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "-b", "master")
	return dir
}

// env is a getenv of the two variables.
func env(system, repositories string) func(string) string {
	return func(name string) string {
		switch name {
		case EnvSystem:
			return system
		case EnvRepositories:
			return repositories
		}
		return ""
	}
}

// times are the times in the lines of the preparation, and commits the commits.
var (
	times   = regexp.MustCompile(` in [0-9]+\.[0-9]s$`)
	commits = regexp.MustCompile(` at [0-9a-f]{12}( |$)`)
)

// prepare is [Run] through [testProxy], which returns what Run printed, its lines
// trimmed and joined by newlines, each time in them as Xs and each commit as C, and ""
// when it fetched nothing.
func prepare(ctx context.Context, dir string, getenv func(string) string) (string, error) {
	return prepareFrom(ctx, dir, "", getenv)
}

// prepareFrom is [prepare], then, with seed set, [Prepare] with seed, the image's copy,
// as qory run prepare runs it once qory run fetched; the lines are Run's, then Prepare's.
func prepareFrom(ctx context.Context, dir, seed string, getenv func(string) string) (string, error) {
	var report strings.Builder
	fetched, err := Run(ctx, dir, os.Environ(), getenv, testProxy, &report)
	if err == nil && fetched != (report.Len() > 0) {
		return "", fmt.Errorf("Run reports fetched %v and printed %q", fetched, report.String())
	}
	if err == nil && seed != "" {
		_, err = Prepare(ctx, dir, seed, getenv, &report)
	}
	lines := strings.Split(strings.TrimSuffix(report.String(), "\n"), "\n")
	for i, line := range lines {
		lines[i] = commits.ReplaceAllString(times.ReplaceAllString(line, " in Xs"), " at C$1")
	}
	return strings.Join(lines, "\n"), err
}

func TestNothingWithoutRepositories(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "kept.txt"), "kept\n")
	for _, system := range []string{"", "github.com", "Not A Host"} {
		line, err := prepare(context.Background(), dir, env(system, ""))
		if line != "" || err != nil {
			t.Errorf("system %q, no repositories: %q, %v; want nothing", system, line, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the folder holds %d entries after a preparation of nothing, want 1", len(entries))
	}
}

// TestRefusesTheListInWords holds each refusal of the variables to its words, before
// anything is fetched: the folder is no git working tree, so a fetch would fail
// differently.
func TestRefusesTheListInWords(t *testing.T) {
	long := "acme/" + strings.Repeat("a", 196)
	for _, c := range []struct {
		system, list, want string
	}{
		{"", "acme/shop", "qory run: set QORY_RUN_SYSTEM beside QORY_RUN_REPOSITORIES in the run configuration: QORY_RUN_SYSTEM is the host of the repositories QORY_RUN_REPOSITORIES lists, and it is not set"},
		{"GitHub.com", "acme/shop", "qory run: set QORY_RUN_SYSTEM to a host name, such as github.com"},
		{"github.com.", "acme/shop,other/lib", "qory run: set QORY_RUN_SYSTEM to a host name, such as github.com"},
		{"git.example.com/acme", "acme/shop", "qory run: set QORY_RUN_SYSTEM to a host name, such as github.com"},
		{"https://git.example.com", "acme/shop", "qory run: set QORY_RUN_SYSTEM to a host name, such as github.com"},
		{strings.Repeat("a", 254), "acme/shop", "qory run: set QORY_RUN_SYSTEM to a host name, such as github.com"},
		{"git.example.com", "acme/shop,acme/lib" + strings.Repeat(",acme/x", 35), "qory run: shorten QORY_RUN_REPOSITORIES to 256 bytes or fewer"},
		{"git.example.com", "acme/shop,", "qory run: make entry 2 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", ",acme/shop", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop,,acme/lib", "qory run: make entry 2 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop, acme/lib", "qory run: make entry 2 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/../shop", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/./shop", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "/acme/shop", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop/", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop:x", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", long, "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop,acme/lib,acme/shop", "qory run: list acme/shop once in QORY_RUN_REPOSITORIES, which lists it twice"},
		{"github.com", "acme/shop,Acme/Shop", "qory run: list Acme/Shop once in QORY_RUN_REPOSITORIES, which lists it twice"},
		{"github.com", "acme/shop,other/lib", "qory run: list in QORY_RUN_REPOSITORIES only repositories of owner acme: entry 2 is of owner other, and on github.com the repositories of a run are of one owner"},
		{"github.com", "acme/shop/x", "qory run: write entry 1 of QORY_RUN_REPOSITORIES as owner/name, the form of a repository on github.com"},
		{"git.example.com", "acme/.git", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop,acme/lib/.GIT", "qory run: make entry 2 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", ".Git/shop", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/.git/x", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"github.com", "acme/.git", "qory run: make entry 1 of QORY_RUN_REPOSITORIES a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "shop,group/app,group/app/sub", "qory run: remove entry 3, group/app/sub, or entry 2, group/app, from QORY_RUN_REPOSITORIES: the first is inside the second, and a repository is not cloned inside another"},
		{"git.example.com", "shop,group/app/sub,group/app", "qory run: remove entry 2, group/app/sub, or entry 3, group/app, from QORY_RUN_REPOSITORIES: the first is inside the second, and a repository is not cloned inside another"},
		{"git.example.com", "group/a,group/b,group/b/c/d,group", "qory run: remove entry 3, group/b/c/d, or entry 2, group/b, from QORY_RUN_REPOSITORIES: the first is inside the second, and a repository is not cloned inside another"},
		{"git.example.com", "group/lib,group,group/lib/x", "qory run: remove entry 3, group/lib/x, or entry 2, group, from QORY_RUN_REPOSITORIES: the first is inside the second, and a repository is not cloned inside another"},
		{"git.example.com", "acme/shop,Group/app,group/app/sub,Group/app/x", "qory run: remove entry 4, Group/app/x, or entry 2, Group/app, from QORY_RUN_REPOSITORIES: the first is inside the second, and a repository is not cloned inside another"},
		{"github.com", "acme/shop,lib", "qory run: write entry 2 of QORY_RUN_REPOSITORIES as owner/name, the form of a repository on github.com"},
	} {
		line, err := prepare(context.Background(), t.TempDir(), env(c.system, c.list))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s %q: %q, %v; want %s", c.system, c.list, line, err, c.want)
		}
	}
	for _, c := range []struct{ system, list string }{
		{"git.example.com", "acme/" + strings.Repeat("a", 195)},
		{"git.example.com", "a"},
		{"git.example.com", "group/sub/project,group/other"},
		{"git.example.com", "group/app,group/application,group/ap"},
		{"git.example.com", "group/app,Group/app/sub"},
		{"git.example.com", "group/lib,group"},
		{"git.example.com", "group/app,group/app/sub"},
		{"git.example.com", "group,group/lib,group/docs"},
		{"git.example.com", "acme/.github,acme/x.git,acme/.gitx"},
		{"github.com", "acme/shop,ACME/lib,acme/.github"},
		{"a", "acme/shop"},
		{"git.example.com", "acme/" + strings.Repeat("a", 195) + ",acme/" + strings.Repeat("b", 50)},
	} {
		if _, err := Check(c.system, c.list); err != nil {
			t.Errorf("%s %q refused: %v", c.system, c.list, err)
		}
	}
}

func TestTheURLIsHTTPSOnTheSystem(t *testing.T) {
	if got := repoURL("github.com", "acme/shop"); got != "https://github.com/acme/shop.git" {
		t.Errorf("the URL is %s, want https://github.com/acme/shop.git", got)
	}
}

// TestFetchesTheRunRepository fetches the run's repository into an empty working tree:
// origin is its URL, its default branch is checked out by name, tracking origin, at
// depth one.
func TestFetchesTheRunRepository(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "trunk", nil)
	dir := emptyFolder(t)
	line, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop"))
	if err != nil {
		t.Fatal(err)
	}
	if line != "qory run: fetched acme/shop at C in Xs" {
		t.Errorf("the line is %q", line)
	}
	if got := run(t, dir, "remote", "get-url", "origin"); got != baseURL+"/acme/shop.git" {
		t.Errorf("origin is %s", got)
	}
	if got := run(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "trunk" {
		t.Errorf("the branch is %s, want trunk", got)
	}
	if got := run(t, dir, "rev-parse", "--abbrev-ref", "trunk@{upstream}"); got != "origin/trunk" {
		t.Errorf("trunk tracks %s, want origin/trunk", got)
	}
	if got := run(t, dir, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Errorf("the repository is shallow: %s", got)
	}
	if got := run(t, dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("%s commits fetched, want 1", got)
	}
	if got := run(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("git status: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Error(err)
	}
	if got, _ := exec.Command("git", "-C", dir, "config", "--get-regexp", "credential").Output(); len(got) != 0 {
		t.Errorf("git's configuration names a credential: %s", got)
	}
}

// TestFetchesIntoAFolderWithQory takes a working folder that holds .qory already.
func TestFetchesIntoAFolderWithQory(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	dir := emptyFolder(t)
	write(t, filepath.Join(dir, ".qory", "harness", "kept.txt"), "kept\n")
	if _, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop")); err != nil {
		t.Fatal(err)
	}
	if got := run(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Errorf("the branch is %s, want main", got)
	}
}

// TestClonesTheFurtherRepositories clones each further repository under
// .qory/repositories at its path and default branch, and keeps them out of the run
// repository's git status with one exclude line.
func TestClonesTheFurtherRepositories(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	remote(t, base, "acme/lib", "stable", nil)
	remote(t, base, "acme/docs", "main", nil)
	for _, c := range []struct {
		list, want string
	}{
		{"acme/shop,acme/lib", "qory run: fetched acme/shop at C and 1 further repository in Xs"},
		{"acme/shop,acme/lib,acme/docs", "qory run: fetched acme/shop at C and 2 further repositories in Xs"},
	} {
		dir := emptyFolder(t)
		exclude := filepath.Join(dir, ".git", "info", "exclude")
		write(t, exclude, "# kept\n/.qory/repositories/")
		line, err := prepare(context.Background(), dir, env("github.com", c.list))
		if err != nil {
			t.Fatal(err)
		}
		if line != c.want {
			t.Errorf("the line is %q, want %q", line, c.want)
		}
		lib := filepath.Join(dir, ".qory", "repositories", "acme", "lib")
		if got := run(t, lib, "rev-parse", "--abbrev-ref", "HEAD"); got != "stable" {
			t.Errorf("acme/lib is on %s, want stable", got)
		}
		if got := run(t, lib, "rev-list", "--count", "HEAD"); got != "1" {
			t.Errorf("acme/lib has %s commits, want 1", got)
		}
		if _, err := os.Stat(filepath.Join(lib, "README.md")); err != nil {
			t.Error(err)
		}
		if got := run(t, dir, "status", "--porcelain", "--untracked-files=all"); got != "" {
			t.Errorf("git status in the working folder: %s", got)
		}
		data, err := os.ReadFile(exclude)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(data), "/.qory/repositories/"); n != 1 {
			t.Errorf("the exclude file names /.qory/repositories/ %d times:\n%s", n, data)
		}
	}
	dir := emptyFolder(t)
	if err := os.Remove(filepath.Join(dir, ".git", "info", "exclude")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := prepare(context.Background(), dir, env("github.com", "acme/shop,acme/docs")); err != nil {
		t.Fatal(err)
	}
	if got := run(t, dir, "status", "--porcelain", "--untracked-files=all"); got != "" {
		t.Errorf("git status with an exclude file qory made: %s", got)
	}
}

// TestKeepsQoryRepositories refuses to clone over what the run repository has at
// .qory/repositories, and clones nothing.
func TestKeepsQoryRepositories(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", map[string]string{".qory/repositories/acme/lib/kept.txt": "kept\n"})
	remote(t, base, "acme/lib", "main", nil)
	dir := emptyFolder(t)
	_, err := prepare(context.Background(), dir, env("github.com", "acme/shop,acme/lib"))
	want := "qory run: remove .qory/repositories from the working folder of acme/shop: qory clones the further repositories there, and nothing is cloned over it"
	if err == nil || err.Error() != want {
		t.Fatalf("%v, want %s", err, want)
	}
	if _, err := os.Stat(filepath.Join(dir, ".qory", "repositories", "acme", "lib", "README.md")); !os.IsNotExist(err) {
		t.Errorf("acme/lib was cloned over the run repository's .qory/repositories: %v", err)
	}
	// A run of the one repository has nothing to clone, and takes it.
	if _, err := prepare(context.Background(), emptyFolder(t), env("github.com", "acme/shop")); err != nil {
		t.Errorf("a run of acme/shop alone: %v", err)
	}
}

// TestStopsOnAFolderThatIsNotEmpty stops on a working folder with a file, one that is no
// git working tree and not empty, one whose repository has a commit and one inside a
// working tree, and fetches nothing. A folder with no entries gets past the check, and
// the repository is fetched.
func TestStopsOnAFolderThatIsNotEmpty(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	notEmpty := "qory run: start the run in an empty git working tree: the working folder contains files already, and the run's repository is fetched only into an empty one"
	notTree := "qory run: start the run in an empty folder outside any git repository, or at the top of a git working tree without commits: the working folder is neither, and qory fetches the run's repository only into one of these"

	withFile := emptyFolder(t)
	write(t, filepath.Join(withFile, "notes.txt"), "notes\n")
	plain := t.TempDir()
	write(t, filepath.Join(plain, "notes.txt"), "notes\n")
	qoryOnly := t.TempDir()
	if err := os.Mkdir(filepath.Join(qoryOnly, ".qory"), 0o755); err != nil {
		t.Fatal(err)
	}
	committed := emptyFolder(t)
	run(t, committed, "commit", "--quiet", "--allow-empty", "-m", "first")
	nested := filepath.Join(emptyFolder(t), "inner")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := t.TempDir()
	run(t, bare, "init", "--quiet", "--bare")
	inBare := filepath.Join(bare, "inner")
	if err := os.Mkdir(inBare, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, dir, want string }{
		{"a file", withFile, notEmpty},
		{"no git", plain, notTree},
		{"only .qory", qoryOnly, notTree},
		{"a commit", committed, notTree},
		{"a folder inside a working tree", nested, notTree},
		{"a folder inside a bare repository", inBare, notTree},
		{"an empty folder", t.TempDir(), ""},
	} {
		_, err := prepare(context.Background(), c.dir, env("git.example.com", "acme/shop"))
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			if _, err := os.Stat(filepath.Join(c.dir, "README.md")); err != nil {
				t.Errorf("%s: acme/shop was not fetched: %v", c.name, err)
			}
			continue
		}
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
		if out, _ := exec.Command("git", "-C", c.dir, "remote").Output(); strings.TrimSpace(string(out)) != "" {
			t.Errorf("%s: a remote was added: %s", c.name, out)
		}
	}
	for _, dir := range []string{plain, qoryOnly, nested, inBare} {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
			t.Errorf("%s has a .git after the stop: %v", dir, err)
		}
	}
}

// TestAGitThatFailsAboveAnEmptyFolderStops is an empty folder under a .git file git cannot
// use, or a repository git cannot use: the check of the folder fails with git's line,
// and git init does not run, since a repository in the folder would hide what is above it.
func TestAGitThatFailsAboveAnEmptyFolderStops(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	want := "qory run: check git on this machine, the working folder and each repository above it, then run again. git failed: "
	type row struct {
		name  string
		plant func(parent string)
	}
	rows := []row{
		{"a .git file that leads nowhere", func(parent string) { write(t, filepath.Join(parent, ".git"), "gitdir: /nowhere\n") }},
		{"a .git file that is no git file", func(parent string) { write(t, filepath.Join(parent, ".git"), "not a git file\n") }},
		{"a bad config", func(parent string) {
			run(t, parent, "init", "--quiet")
			write(t, filepath.Join(parent, ".git", "config"), "not a config\n[")
		}},
		{"a format git does not take", func(parent string) {
			run(t, parent, "init", "--quiet")
			run(t, parent, "config", "core.repositoryformatversion", "99")
		}},
	}
	// Root reads a file with no read permission.
	if os.Geteuid() != 0 {
		rows = append(rows, row{"a .git file with no read permission", func(parent string) {
			write(t, filepath.Join(parent, ".git"), "gitdir: /nowhere\n")
			if err := os.Chmod(filepath.Join(parent, ".git"), 0); err != nil {
				t.Fatal(err)
			}
		}})
	}
	for _, c := range rows {
		parent := t.TempDir()
		c.plant(parent)
		dir := filepath.Join(parent, "inner")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop"))
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v, want a line that starts %s", c.name, err, want)
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
			t.Errorf("%s: the folder has a .git after the stop: %v", c.name, err)
		}
	}
}

// TestABadHeadBelowABadGitFileStops is a working folder whose .git has a HEAD git does not
// take, below a .git file that leads nowhere: git goes on to the folder above, and the
// line sends the reader to each repository above the working folder.
func TestABadHeadBelowABadGitFileStops(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	parent := t.TempDir()
	write(t, filepath.Join(parent, ".git"), "gitdir: /nowhere\n")
	dir := filepath.Join(parent, "inner")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "--quiet", "-b", "main")
	write(t, filepath.Join(dir, ".git", "HEAD"), "not a head\n")
	_, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop"))
	want := "qory run: check git on this machine, the working folder's .git and each repository above it, then run again. git failed: "
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("%v, want a line that starts %s", err, want)
	}
}

// TestAGitThatFailsInTheWorkingFolderStops is a repository that git finds and cannot use:
// the line for a failed look for the top of the working tree, as for a .git above it, since
// git can print the same line for both.
func TestAGitThatFailsInTheWorkingFolderStops(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	want := "qory run: check git on this machine, the working folder's .git and each repository above it, then run again. git failed: "
	for _, c := range []struct{ key, value string }{
		{"core.bare", "true"},
		{"core.repositoryformatversion", "99"},
	} {
		dir := emptyFolder(t)
		run(t, dir, "config", c.key, c.value)
		_, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop"))
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: %v, want a line that starts %s", c.key, err, want)
		}
	}
}

// TestEmptyStopHasTheFixOfEachStop is the whole line for each stop of hostgit's check on a
// folder with no entries, with paths long enough that a cut would lose hostgit's fix: a new
// folder for a walled run that may use it, hostgit's text alone for what the check cannot
// read, and a folder outside the repository for a stop of that repository.
func TestEmptyStopHasTheFixOfEachStop(t *testing.T) {
	long := "/state/" + strings.Repeat("x", 300)
	inUse := "qory run: start the run in a new empty folder that no walled run uses, then run again. The check of the working folder failed: "
	above := "qory run: start the run in an empty folder outside the repository above the working folder, then run again. The check of that repository failed: "
	for _, c := range []struct {
		name string
		stop *hostgit.RefusedError
		want string
	}{
		{"in use", &hostgit.RefusedError{Dir: "/work", Reason: hostgit.RefusedInUse},
			inUse + "/work is in use by a walled run; qory runs no git there until the run ends, since the run could change git's configuration between qory's check and git's read"},
		{"engine out of reach", &hostgit.RefusedError{Dir: "/work", Key: "rn_1", Target: long, Reason: hostgit.RefusedEngineUnreachable},
			"qory run: start the run in a new empty folder that no walled run uses, then run again. If it stops there too: The check for a container of the walled run rn_1 failed. That run used /work, so qory runs no git there. If docker ps --all --filter label=dev.qory.run=rn_1 lists no container, or the Docker engine of that run no longer exists, delete " + long},
		{"entry unreadable", &hostgit.RefusedError{Dir: "/work", Key: "rn_1", Target: long, Reason: hostgit.RefusedEntryUnreadable},
			"qory run: qory cannot read " + long + ", the record of the walled run rn_1, so it cannot tell whether that run used /work or is still going, and runs no git there. If docker ps --all --filter label=dev.qory.run=rn_1 lists no container, delete " + long},
		{"no records", &hostgit.RefusedError{Reason: hostgit.RefusedNoRecords, Err: errors.New("no\nhome")}, `qory run: no\nhome`},
		{"no git", &hostgit.RefusedError{Dir: "/work", Reason: hostgit.RefusedNoGit},
			"qory run: git is not on the PATH, so qory cannot check the git configuration of /work; qory runs no git there, since a run could have set it to make git on this machine run a program"},
		{"a key above", &hostgit.RefusedError{Dir: "/work", Key: "filter.x.smudge", Reason: hostgit.RefusedSetting},
			above + "the git configuration of /work sets filter.x.smudge, which qory does not write; qory runs no git there, since a run could have set it to make git on this machine run a program"},
		{"a link above", &hostgit.RefusedError{Dir: "/work", Key: "objects", Target: long, Reason: hostgit.RefusedObjectsLink},
			above + "the git directory of /work holds objects, a link to " + long + "; qory runs no git there, since a run could have pointed it at another repository's objects. Remove the link to go on"},
	} {
		if got := emptyStop(c.stop).Error(); got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

// TestAnEmptyFolderWithNoRecordsStops is a run with neither HOME nor XDG_STATE_HOME: the
// line is the reason qory cannot find the records, with no other folder as a fix, and the
// folder has no .git.
func TestAnEmptyFolderWithNoRecordsStops(t *testing.T) {
	needGit(t)
	isolatedPlaces(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	t.Setenv("HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	_, dirErr := places.Dir()
	if dirErr == nil {
		t.Fatal("the records have a folder with neither HOME nor XDG_STATE_HOME")
	}
	dir := t.TempDir()
	_, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop"))
	if want := "qory run: " + dirErr.Error(); err == nil || err.Error() != want {
		t.Errorf("%v, want %s", err, want)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Errorf("the folder has a .git after the stop: %v", err)
	}
}

// TestAnEmptyFolderBelowARecordedRepositoryStops is an empty folder inside a repository that
// a walled run used, whose configuration has a filter the run wrote: the line sends the
// reader outside that repository, and the folder has no .git.
func TestAnEmptyFolderBelowARecordedRepositoryStops(t *testing.T) {
	needGit(t)
	isolatedPlaces(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	parent, err := filepath.EvalSymlinks(emptyFolder(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := hostgit.Record(context.Background(), parent, places.KindCheckout, "run-1"); err != nil {
		t.Fatal(err)
	}
	smudging(t, parent, filepath.Join(t.TempDir(), "ran"))
	dir := filepath.Join(parent, "inner")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = prepare(context.Background(), dir, env("git.example.com", "acme/shop"))
	want := "qory run: start the run in an empty folder outside the repository above the working folder, then run again. The check of that repository failed: "
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("%v, want a line that starts %s", err, want)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Errorf("the folder has a .git after the stop: %v", err)
	}
}

// TestAnEmptyFolderStartsOnMain is a fetch that fails after git init: HEAD is the unborn
// branch main, whichever branch git would start on by default.
func TestAnEmptyFolderStartsOnMain(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	dir := t.TempDir()
	if _, err := prepare(context.Background(), dir, env("git.example.com", "acme/missing")); err == nil {
		t.Fatal("acme/missing was fetched")
	}
	if got := run(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Errorf("HEAD is %s, want refs/heads/main", got)
	}
}

// TestAFolderNameMayEndInASpace is an empty folder and a git working tree whose names end in
// a space: each gets the fetch.
func TestAFolderNameMayEndInASpace(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	empty, tree := filepath.Join(t.TempDir(), "name "), filepath.Join(t.TempDir(), "name ")
	for _, dir := range []string{empty, tree} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run(t, tree, "init", "--quiet", "-b", "master")
	for _, dir := range []string{empty, tree} {
		if _, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop")); err != nil {
			t.Errorf("%q: %v", dir, err)
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
			t.Errorf("%q: acme/shop was not fetched: %v", dir, err)
		}
	}
}

// TestAGitFolderThatIsNoRepositoryStops is a working folder whose .git is empty, or has no
// refs: the line starts with its fix and has what is missing, with no repository above
// the folder and with one. A .git with a commondir file and no objects or refs gets the
// line of the commondir file, since git follows that file.
func TestAGitFolderThatIsNoRepositoryStops(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	line := "qory run: start the run in a new empty folder or a new empty git working tree: the working folder's .git has no %s, so git does not take that folder for a repository"
	empty := t.TempDir()
	noRefs := emptyFolder(t)
	if err := os.RemoveAll(filepath.Join(noRefs, ".git", "refs")); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(emptyFolder(t), "inner")
	common := t.TempDir()
	for _, dir := range []string{filepath.Join(empty, ".git"), filepath.Join(nested, ".git"), filepath.Join(common, ".git")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(common, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(common, ".git", "commondir"), t.TempDir()+"\n")
	for _, c := range []struct{ name, dir, want string }{
		{"an empty .git", empty, fmt.Sprintf(line, "HEAD")},
		{"no refs", noRefs, fmt.Sprintf(line, "refs")},
		{"an empty .git inside a working tree", nested, fmt.Sprintf(line, "HEAD")},
		{"HEAD and a commondir file", common, "qory run: start the run in a new empty git working tree, then run again: the working folder's .git has a commondir file, which can point git at another repository"},
	} {
		_, err := prepare(context.Background(), c.dir, env("git.example.com", "acme/shop"))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
}

// TestAnEmptyFolderGetsARepository runs git init in a folder with no entries, with no
// template, and fetches the run's repository on its default branch, whichever that is,
// and a further one, whose line in the exclude file qory writes.
func TestAnEmptyFolderGetsARepository(t *testing.T) {
	needGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	remote(t, base, "acme/old", "master", nil)
	remote(t, base, "acme/docs", "main", nil)
	for _, c := range []struct{ list, branch string }{
		{"acme/shop", "main"},
		{"acme/old", "master"},
		{"acme/shop,acme/docs", "main"},
	} {
		dir := t.TempDir()
		if _, err := prepare(context.Background(), dir, env("git.example.com", c.list)); err != nil {
			t.Errorf("%s: %v", c.list, err)
			continue
		}
		if got := run(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != c.branch {
			t.Errorf("%s: HEAD is on %s, want %s", c.list, got, c.branch)
		}
		if got := run(t, dir, "rev-parse", "--abbrev-ref", "@{upstream}"); got != "origin/"+c.branch {
			t.Errorf("%s: the upstream is %s, want origin/%s", c.list, got, c.branch)
		}
		if got := run(t, dir, "status", "--porcelain", "--untracked-files=all"); got != "" {
			t.Errorf("%s: git status: %s", c.list, got)
		}
		for _, name := range []string{"hooks", "description"} {
			if _, err := os.Lstat(filepath.Join(dir, ".git", name)); !os.IsNotExist(err) {
				t.Errorf("%s: .git/%s comes from a template: %v", c.list, name, err)
			}
		}
	}
}

// TestSaysWhatCouldNotBeFetched is git's first line for a repository that is not there,
// the run's or a further one.
func TestSaysWhatCouldNotBeFetched(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	for _, c := range []struct{ list, path string }{
		{"acme/missing", "acme/missing"},
		{"acme/shop,acme/missing", "acme/missing"},
	} {
		_, err := prepare(context.Background(), emptyFolder(t), env("git.example.com", c.list))
		prefix := "qory run: add a credential for git.example.com that reads " + c.path + " to the run configuration, then run again. The fetch of " + c.path + " through the run's gateway failed: "
		if err == nil || !strings.HasPrefix(err.Error(), prefix) || len(err.Error()) == len(prefix) || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: %v, want one line starting %q and git's words", c.list, err, prefix)
		}
	}
}

func TestDefaultBranch(t *testing.T) {
	for out, want := range map[string]string{
		"ref: refs/heads/main\tHEAD\n0123\tHEAD\n":      "main",
		"ref: refs/heads/release/2\tHEAD\n0123\tHEAD\n": "release/2",
		"0123\tHEAD\n": "",
		"":             "",
		"ref: refs/heads/main\trefs/heads/x\n0123\tHEAD\n": "",
	} {
		if got := defaultBranch(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
}

func TestGitEnvTurnsThePromptsOff(t *testing.T) {
	got := gitEnv([]string{
		"GIT_DIR=/elsewhere", "GIT_TERMINAL_PROMPT=1", "GIT_ASKPASS=/bin/ask", "HTTPS_PROXY=http://relay:3128", "GIT_SSL_CAINFO=/ca.pem", "PATH=/bin",
		"GIT_CONFIG_GLOBAL=/home/.gitconfig", "GIT_CONFIG_NOSYSTEM=0", "GIT_CONFIG_SYSTEM=/etc/gitconfig", "GIT_CONFIG=/x",
		"GIT_CONFIG_PARAMETERS='core.hookspath'='/x'", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/x",
		"GIT_EXEC_PATH=/x", "GIT_TEMPLATE_DIR=/x", "GIT_SSH=/x", "GIT_SSH_COMMAND=/x", "GIT_PROXY_COMMAND=/x", "GIT_SSL_NO_VERIFY=1",
		"GIT_TRACE=1", "GIT_TRACE_PACKET=/tmp/trace", "GIT_REPLACE_REF_BASE=refs/x/", "GIT_NO_REPLACE_OBJECTS=0", "http_proxy=http://relay:3128",
	})
	want := []string{
		"HTTPS_PROXY=http://relay:3128", "GIT_SSL_CAINFO=/ca.pem", "PATH=/bin", "http_proxy=http://relay:3128",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("%v, want %v", got, want)
	}
}

// bareFrom makes base/path.git, bare, from the repository work, and points the fetches
// at base.
func bareFrom(t *testing.T, base, path, work string) {
	t.Helper()
	dest := filepath.Join(base, filepath.FromSlash(path)+".git")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, base, "clone", "--quiet", "--bare", work, dest)
	setBase(t, base)
}

// TestRefusesAQoryThatIsNoFolder refuses a run repository whose .qory is a file or a
// link, in words that say so, and one whose .qory/repositories is a link, as one that
// has it already; nothing is cloned, through the link or beside it.
func TestRefusesAQoryThatIsNoFolder(t *testing.T) {
	needGit(t)
	notDir := "qory run: remove .qory from the working folder of acme/shop: it is a file or a link, where qory clones the further repositories into a folder, and nothing is cloned over it"
	kept := "qory run: remove .qory/repositories from the working folder of acme/shop: qory clones the further repositories there, and nothing is cloned over it"
	outside := t.TempDir()
	for _, c := range []struct {
		name  string
		setup func(work string)
		want  string
	}{
		{".qory a file", func(work string) { write(t, filepath.Join(work, ".qory"), "a file\n") }, notDir},
		{".qory a link", func(work string) {
			if err := os.Symlink(outside, filepath.Join(work, ".qory")); err != nil {
				t.Fatal(err)
			}
		}, notDir},
		{".qory/repositories a link", func(work string) {
			if err := os.MkdirAll(filepath.Join(work, ".qory"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(work, ".qory", "repositories")); err != nil {
				t.Fatal(err)
			}
		}, kept},
	} {
		base := t.TempDir()
		work := t.TempDir()
		run(t, work, "init", "--quiet", "-b", "main")
		write(t, filepath.Join(work, "README.md"), "shop\n")
		c.setup(work)
		run(t, work, "add", "-A")
		run(t, work, "commit", "--quiet", "-m", "first")
		bareFrom(t, base, "acme/shop", work)
		remote(t, base, "acme/lib", "main", nil)
		dir := emptyFolder(t)
		_, err := prepare(context.Background(), dir, env("github.com", "acme/shop,acme/lib"))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("%s: %d entries were made through the link", c.name, len(entries))
		}
	}
}

// TestSaysWhenGitDoesNotStart is a git that is not on the PATH, or an empty folder that git
// cannot start in: each stop is the line for a failed git.
func TestSaysWhenGitDoesNotStart(t *testing.T) {
	needGit(t)
	const (
		missing = `git failed: exec: "git": executable file not found in $PATH`
		above   = "qory run: check git on this machine, the working folder and each repository above it, then run again. "
	)
	type row struct {
		name, dir, want string
		noGit           bool
	}
	rows := []row{
		{"a working tree, no git", emptyFolder(t), "qory run: check that git on this machine can read the working folder, then run again. " + missing, true},
		{"an empty folder, no git", t.TempDir(), above + missing, true},
	}
	// Root starts git in a folder with no search permission.
	if os.Geteuid() != 0 {
		closed := t.TempDir()
		if err := os.Chmod(closed, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })
		path, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		if path, err = filepath.EvalSymlinks(path); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row{"an empty folder at mode 0400", closed, above + "git failed: fork/exec " + path + ": permission denied", false})
	}
	for _, c := range rows {
		t.Run(c.name, func(t *testing.T) {
			if c.noGit {
				t.Setenv("PATH", t.TempDir())
			}
			_, err := prepare(context.Background(), c.dir, env("git.example.com", "acme/shop"))
			if err == nil || err.Error() != c.want {
				t.Errorf("%v, want %s", err, c.want)
			}
		})
	}
}

// TestRefusesARemoteAlready refuses a working tree whose repository has a remote, before
// any fetch: the remote is left as it was.
func TestRefusesARemoteAlready(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	dir := emptyFolder(t)
	run(t, dir, "remote", "add", "origin", "https://git.example.com/acme/other.git")
	_, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop"))
	want := "qory run: start the run in a new empty folder or a new empty git working tree: the working folder's repository has a remote already, and qory fetches the run's repository only into a repository with no remote"
	if err == nil || err.Error() != want {
		t.Errorf("%v, want %s", err, want)
	}
	if got := run(t, dir, "remote", "get-url", "origin"); got != "https://git.example.com/acme/other.git" {
		t.Errorf("origin is %s", got)
	}
	if out, _ := exec.Command("git", "-C", dir, "for-each-ref").Output(); len(out) != 0 {
		t.Errorf("refs were fetched: %s", out)
	}
}

// TestSaysWhyTheDefaultBranchCannotBeFetched words each way the remote's default
// branch fails: a remote with no refs has no commits, one with refs whose HEAD names
// no branch it has names no default branch, and one whose HEAD names a branch that
// starts with a dash names a branch qory does not fetch. Nothing is fetched.
func TestSaysWhyTheDefaultBranchCannotBeFetched(t *testing.T) {
	needGit(t)
	prefix := "qory run: give acme/shop on git.example.com a default branch with a commit, then run again. The fetch through the run's gateway failed: "
	for _, c := range []struct {
		name  string
		setup func(base string)
		want  string
	}{
		{"an empty remote", func(base string) {
			run(t, base, "init", "--quiet", "--bare", "-b", "main", filepath.Join(base, "acme", "shop.git"))
			setBase(t, base)
		}, "the repository has no commits"},
		{"a HEAD that names no branch the remote has", func(base string) {
			remote(t, base, "acme/shop", "main", nil)
			write(t, filepath.Join(base, "acme", "shop.git", "HEAD"), "ref: refs/heads/gone\n")
		}, "the remote names no default branch"},
		{"a default branch -x", func(base string) {
			remote(t, base, "acme/shop", "main", nil)
			bare := filepath.Join(base, "acme", "shop.git")
			write(t, filepath.Join(bare, "refs", "heads", "-x"), run(t, bare, "rev-parse", "main")+"\n")
			write(t, filepath.Join(bare, "HEAD"), "ref: refs/heads/-x\n")
			if got := run(t, bare, "ls-remote", "--symref", ".", "HEAD"); !strings.HasPrefix(got, "ref: refs/heads/-x\tHEAD") {
				t.Fatalf("the remote does not name -x as its default branch:\n%s", got)
			}
		}, "the default branch the remote names, -x, is not a branch name qory fetches"},
	} {
		c.setup(t.TempDir())
		dir := emptyFolder(t)
		if _, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop")); err == nil || err.Error() != prefix+c.want {
			t.Errorf("%s: %v, want %s", c.name, err, prefix+c.want)
		}
		if out, _ := exec.Command("git", "-C", dir, "for-each-ref").Output(); len(out) != 0 {
			t.Errorf("%s: refs were fetched: %s", c.name, out)
		}
	}
}

// TestTellsARefusedBranchNameFromAFailedCheck runs git check-ref-format through a
// stand-in: an exit of 1 is a name it refuses, and any other failure, or a check that
// runs past its limit, is git's failure in git's words, never a refused name.
func TestTellsARefusedBranchNameFromAFailedCheck(t *testing.T) {
	needGit(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	shortLimit(t, "check-ref-format", 2*time.Second)
	noBranch := "qory run: give acme/shop on git.example.com a default branch with a commit, then run again. The fetch through the run's gateway failed: "
	notReached := "qory run: check that the run's policy allows git.example.com and that git.example.com is reachable, then run again. The fetch of acme/shop through the run's gateway failed: "
	for _, c := range []struct{ name, check, want string }{
		{"refused", "exit 1", noBranch + "the default branch the remote names, main, is not a branch name qory fetches"},
		{"failed", "echo 'fatal: the check failed' >&2; exit 128", notReached + "fatal: the check failed"},
		{"stopped", "exec sleep 30", notReached + "git check-ref-format ran past 2s and was stopped"},
	} {
		bin := t.TempDir()
		write(t, filepath.Join(bin, "git"), "#!/bin/sh\nfor a; do [ \"$a\" = check-ref-format ] && { "+c.check+"; }; done\nexec "+shellQuote(gitPath)+" \"$@\"\n")
		if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
			t.Fatal(err)
		}
		dir := emptyFolder(t)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		start := time.Now()
		if _, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop")); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
		if took := time.Since(start); took > 10*time.Second {
			t.Errorf("%s: returned after %s", c.name, took)
		}
	}
}

// TestIgnoresGitDirInTheEnvironment fetches as if GIT_DIR were unset: every git, the
// one that finds the exclude file among them, works on the working folder.
func TestIgnoresGitDirInTheEnvironment(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	remote(t, base, "acme/lib", "main", nil)
	other := emptyFolder(t)
	otherExclude := filepath.Join(other, ".git", "info", "exclude")
	write(t, otherExclude, "# other\n")
	dir := emptyFolder(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	if _, err := prepare(context.Background(), dir, env("github.com", "acme/shop,acme/lib")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(otherExclude); string(b) != "# other\n" {
		t.Errorf("the other repository's exclude file was written:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude")); !strings.Contains(string(b), "/.qory/repositories/\n") {
		t.Errorf("the working folder's exclude file:\n%s", b)
	}
}

// shortLimit has a git call of subcommand stop at limit, and every other call at
// [gitLimit], for the test or until what it returns is called.
func shortLimit(t *testing.T, subcommand string, limit time.Duration) func() {
	t.Helper()
	restore := func() { limitOf = func(string) time.Duration { return gitLimit } }
	t.Cleanup(restore)
	limitOf = func(sub string) time.Duration {
		if sub == subcommand {
			return limit
		}
		return gitLimit
	}
	return restore
}

// stalled is an https address that accepts connections and never answers, with no
// proxy on the way to it; a test makes it the host proxy, too.
func stalled(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				c.Close()
			}
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, "")
	}
	t.Setenv("NO_PROXY", "*")
	return "https://" + ln.Addr().String()
}

// TestStopsAStalledGit stops a git whose server never answers: at the limit of one call,
// in words that say so, and when the caller's context ends, as SIGTERM ends it, in
// words that do not claim the limit. Either way the call returns within seconds,
// although git-remote-https holds git's output open.
func TestStopsAStalledGit(t *testing.T) {
	needGit(t)
	restore := shortLimit(t, "ls-remote", 2*time.Second)
	baseURL = stalled(t)
	proxy := testProxy
	testProxy.URL = "http://qory:test-proxy-password@" + strings.TrimPrefix(baseURL, "https://")
	t.Cleanup(func() { baseURL, testProxy = "", proxy })
	start := time.Now()
	_, err := prepare(context.Background(), emptyFolder(t), env("git.example.com", "acme/shop"))
	want := "qory run: check that the run's policy allows git.example.com and that git.example.com is reachable, then run again. The fetch of acme/shop through the run's gateway failed: git ls-remote ran past 2s and was stopped"
	if err == nil || err.Error() != want {
		t.Errorf("at the limit: %v, want %s", err, want)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("at the limit: returned after %s", took)
	}

	restore()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start = time.Now()
	_, err = prepare(ctx, emptyFolder(t), env("git.example.com", "acme/shop"))
	want = "qory run: check that the run's policy allows git.example.com and that git.example.com is reachable, then run again. The fetch of acme/shop through the run's gateway failed: git ls-remote was stopped before it finished"
	if err == nil || err.Error() != want {
		t.Errorf("the caller's end: %v, want %s", err, want)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the caller's end: returned after %s", took)
	}
}

// hostile gives the repository at gitDir a hook for each event a fetch or a checkout
// may run one for, and a file system monitor in its configuration, each of which
// writes to the file marker when it runs.
func hostile(t *testing.T, gitDir, marker string) {
	t.Helper()
	script := "#!/bin/sh\necho \"$0\" >> '" + marker + "'\nexit 0\n"
	for _, hook := range []string{"post-checkout", "reference-transaction", "post-merge", "post-index-change", "pre-auto-gc", "fsmonitor-watchman"} {
		file := filepath.Join(gitDir, "hooks", hook)
		write(t, file, script)
		if err := os.Chmod(file, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	monitor := filepath.Join(t.TempDir(), "monitor")
	write(t, monitor, "#!/bin/sh\necho monitor >> '"+marker+"'\nexit 1\n")
	if err := os.Chmod(monitor, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, gitDir, "config", "--file", filepath.Join(gitDir, "config"), "core.fsmonitor", monitor)
}

// TestRunsNoHookAndNoMonitor is a working folder whose repository has hooks and a file
// system monitor: the fetch stops on the monitor's key, and once it is gone, the run's
// repository and a further one are fetched and checked out, and no hook runs.
func TestRunsNoHookAndNoMonitor(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	remote(t, base, "acme/lib", "main", nil)
	dir := emptyFolder(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hostile(t, filepath.Join(dir, ".git"), marker)
	// The monitor is a key of the repository's configuration, which git init does not
	// write: nothing is fetched until it is gone.
	want := "qory run: start the run in a new empty git working tree, or remove core.fsmonitor from .git/config, then run again. "
	if _, err := prepare(context.Background(), dir, env("github.com", "acme/shop,acme/lib")); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("a monitor in the configuration: %v, want %s…", err, want)
	}
	run(t, dir, "config", "--unset", "core.fsmonitor")
	if _, err := prepare(context.Background(), dir, env("github.com", "acme/shop,acme/lib")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(marker); !os.IsNotExist(err) {
		t.Errorf("a hook or the monitor ran: %v\n%s", err, got)
	}
	// The hooks are there, and run for a git without the preparation's options.
	run(t, dir, "checkout", "--quiet", "-b", "other")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the hooks of the test do not run: %v", err)
	}
}

// TestRefusesAGitFile refuses a working folder whose .git is a file, as a worktree's
// or a separate git directory's is, or a link, and fetches nothing.
func TestRefusesAGitFile(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	want := "qory run: start the run in a git working tree whose .git is a folder: the working folder's .git is a file or a link, which points at a repository elsewhere"
	separate := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "worktree")
	run(t, separate, "init", "--quiet", "--separate-git-dir", filepath.Join(t.TempDir(), "git"), "-b", "main")
	main := emptyFolder(t)
	run(t, main, "commit", "--quiet", "--allow-empty", "-m", "first")
	run(t, main, "worktree", "add", "--quiet", "--orphan", worktree)
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(emptyFolder(t), ".git"), filepath.Join(linked, ".git")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, dir string }{
		{"a separate git directory", separate},
		{"a worktree", worktree},
		{"a link", linked},
	} {
		_, err := prepare(context.Background(), c.dir, env("git.example.com", "acme/shop"))
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %s", c.name, err, want)
		}
		if out, _ := exec.Command("git", "-C", c.dir, "remote").Output(); strings.TrimSpace(string(out)) != "" {
			t.Errorf("%s: a remote was added: %s", c.name, out)
		}
	}
}

// TestReadsNoOtherConfiguration is a global configuration and one of the environment,
// through GIT_CONFIG_COUNT, that each set a folder of hooks by core.hooksPath, a hook by
// hook.<name>.command where the git supports it, and a filter the repository's
// attributes name: a fetch, a checkout and a clone run none of them, and the files are
// as committed. A git without the preparation's environment runs each, the global
// configuration's alone and then the environment's over it.
func TestReadsNoOtherConfiguration(t *testing.T) {
	needGit(t)
	attributes := "global.txt filter=global\nenvironment.txt filter=environment\n"
	files := map[string]string{".gitattributes": attributes, "README.md": "shop\n", "global.txt": "global\n", "environment.txt": "environment\n"}
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", files)
	remote(t, base, "acme/lib", "main", nil)
	dir := emptyFolder(t)
	write(t, filepath.Join(dir, ".git", "info", "attributes"), attributes)
	marker := filepath.Join(t.TempDir(), "ran")
	configHooks := hookCommands(t)
	global := filepath.Join(t.TempDir(), "gitconfig")
	config := "[core]\n\thooksPath = " + hooksFolder(t, marker, "hooks-global") + "\n" +
		"[filter \"global\"]\n\tsmudge = " + configValue(smudge(marker, "smudge-global")) + "\n\tclean = cat\n"
	if configHooks {
		config += "[hook \"global\"]\n\tevent = post-checkout\n\tevent = reference-transaction\n\tcommand = " + configValue("echo hook-global >> '"+marker+"'") + "\n"
	}
	write(t, global, config)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	environment := [][2]string{
		{"core.hooksPath", hooksFolder(t, marker, "hooks-environment")},
		{"filter.environment.smudge", smudge(marker, "smudge-environment")},
	}
	if configHooks {
		environment = append(environment, [2]string{"hook.environment.event", "post-checkout"},
			[2]string{"hook.environment.command", "echo hook-environment >> '" + marker + "'"})
	}
	for i, kv := range environment {
		t.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i), kv[0])
		t.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i), kv[1])
	}
	t.Setenv("GIT_CONFIG_COUNT", fmt.Sprint(len(environment)))
	if _, err := prepare(context.Background(), dir, env("github.com", "acme/shop,acme/lib")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(marker); !os.IsNotExist(err) {
		t.Errorf("a hook or a filter ran: %v\n%s", err, got)
	}
	for name, content := range files {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != content {
			t.Errorf("%s holds %q", name, got)
		}
	}
	// The global configuration alone, then the environment's over it: a branch
	// checked out and the files checked out again run each hook and filter.
	for _, c := range []struct {
		count string
		want  []string
	}{
		{"0", []string{"hooks-global post-checkout", "smudge-global"}},
		{fmt.Sprint(len(environment)), []string{"hooks-environment post-checkout", "smudge-environment"}},
	} {
		t.Setenv("GIT_CONFIG_COUNT", c.count)
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for name := range files {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		run(t, dir, "checkout", "--quiet", "-B", "other")
		run(t, dir, "checkout", "--quiet", "--", ".")
		want := c.want
		if configHooks {
			want = append(want, strings.Replace(strings.Fields(c.want[0])[0], "hooks-", "hook-", 1))
		}
		got, _ := os.ReadFile(marker)
		for _, w := range want {
			if !strings.Contains(string(got), w) {
				t.Errorf("with GIT_CONFIG_COUNT=%s, %s does not run for the test's git: %q", c.count, w, got)
			}
		}
	}
}

// hookCommands reports whether the machine's git runs a hook a configuration defines by
// hook.<name>.command, which a git before that support ignores.
func hookCommands(t *testing.T) bool {
	t.Helper()
	dir := emptyFolder(t)
	probe := filepath.Join(t.TempDir(), "probe")
	cmd := exec.Command("git", "-c", "hook.probe.event=post-checkout", "-c", "hook.probe.command=echo probe > '"+probe+"'",
		"hook", "run", "--ignore-missing", "post-checkout")
	cmd.Dir = dir
	_ = cmd.Run()
	_, err := os.Stat(probe)
	return err == nil
}

// hooksFolder is a folder of hooks, for the events a fetch, a checkout and a clone run,
// each of which writes name and the event to marker.
func hooksFolder(t *testing.T, marker, name string) string {
	t.Helper()
	folder := filepath.Join(t.TempDir(), name)
	for _, hook := range []string{"post-checkout", "reference-transaction"} {
		file := filepath.Join(folder, hook)
		write(t, file, "#!/bin/sh\necho "+name+" "+hook+" >> '"+marker+"'\ncat > /dev/null\nexit 0\n")
		if err := os.Chmod(file, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return folder
}

// configValue is value as a git configuration file holds it: in double quotes, with a
// double quote and a backslash escaped, so that a semicolon in it starts no comment.
func configValue(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

// smudge is a smudge filter that writes word to marker and passes the file through.
func smudge(marker, word string) string {
	return "sh -c 'echo " + word + " >> \"" + marker + "\"; cat'"
}
