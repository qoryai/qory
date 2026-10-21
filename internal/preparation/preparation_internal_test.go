package preparation

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

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

// setBase points the fetches at the repositories under base, over file://.
func setBase(t *testing.T, base string) {
	t.Helper()
	old := baseURL
	baseURL = "file://" + filepath.ToSlash(base)
	t.Cleanup(func() { baseURL = old })
}

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

// times are the times in the lines of the preparation.
var times = regexp.MustCompile(` in [0-9]+\.[0-9]s$`)

// prepare is [Run] with no image's copy, which returns what Run printed, its lines
// trimmed and joined by newlines, each time in them as Xs, and "" when it fetched
// nothing.
func prepare(ctx context.Context, dir string, getenv func(string) string) (string, error) {
	return prepareFrom(ctx, dir, "", getenv)
}

// prepareFrom is [prepare] with seed, the image's copy.
func prepareFrom(ctx context.Context, dir, seed string, getenv func(string) string) (string, error) {
	var report strings.Builder
	fetched, err := Run(ctx, dir, seed, getenv, &report)
	if err == nil && fetched != (report.Len() > 0) {
		return "", fmt.Errorf("Run reports fetched %v and printed %q", fetched, report.String())
	}
	lines := strings.Split(strings.TrimSuffix(report.String(), "\n"), "\n")
	for i, line := range lines {
		lines[i] = times.ReplaceAllString(line, " in Xs")
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
		{"", "acme/shop", "qory run prepare: QORY_RUN_REPOSITORIES is set but QORY_RUN_SYSTEM is not; both name where the code is"},
		{"GitHub.com", "acme/shop", "qory run prepare: QORY_RUN_SYSTEM is not a host name"},
		{"github.com.", "acme/shop,other/lib", "qory run prepare: QORY_RUN_SYSTEM is not a host name"},
		{"git.example.com/acme", "acme/shop", "qory run prepare: QORY_RUN_SYSTEM is not a host name"},
		{"https://git.example.com", "acme/shop", "qory run prepare: QORY_RUN_SYSTEM is not a host name"},
		{strings.Repeat("a", 254), "acme/shop", "qory run prepare: QORY_RUN_SYSTEM is not a host name"},
		{"git.example.com", "acme/shop,acme/lib" + strings.Repeat(",acme/x", 35), "qory run prepare: QORY_RUN_REPOSITORIES is more than 256 bytes"},
		{"git.example.com", "acme/shop,", "qory run prepare: QORY_RUN_REPOSITORIES entry 2 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", ",acme/shop", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop,,acme/lib", "qory run prepare: QORY_RUN_REPOSITORIES entry 2 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop, acme/lib", "qory run prepare: QORY_RUN_REPOSITORIES entry 2 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/../shop", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/./shop", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "/acme/shop", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop/", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop:x", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", long, "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop,acme/lib,acme/shop", "qory run prepare: QORY_RUN_REPOSITORIES names acme/shop twice"},
		{"github.com", "acme/shop,Acme/Shop", "qory run prepare: QORY_RUN_REPOSITORIES names Acme/Shop twice"},
		{"github.com", "acme/shop,other/lib", "qory run prepare: QORY_RUN_REPOSITORIES entry 2 is of owner other, not acme; on github.com the repositories of a run are of one owner"},
		{"github.com", "acme/shop/x", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not owner/name, the form of a repository on github.com"},
		{"git.example.com", "acme/.git", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/shop,acme/lib/.GIT", "qory run prepare: QORY_RUN_REPOSITORIES entry 2 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", ".Git/shop", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "acme/.git/x", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"github.com", "acme/.git", "qory run prepare: QORY_RUN_REPOSITORIES entry 1 is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"},
		{"git.example.com", "shop,group/app,group/app/sub", "qory run prepare: QORY_RUN_REPOSITORIES entry 3, group/app/sub, is inside entry 2, group/app; a repository is not cloned inside another"},
		{"git.example.com", "shop,group/app/sub,group/app", "qory run prepare: QORY_RUN_REPOSITORIES entry 2, group/app/sub, is inside entry 3, group/app; a repository is not cloned inside another"},
		{"git.example.com", "group/a,group/b,group/b/c/d,group", "qory run prepare: QORY_RUN_REPOSITORIES entry 3, group/b/c/d, is inside entry 2, group/b; a repository is not cloned inside another"},
		{"git.example.com", "group/lib,group,group/lib/x", "qory run prepare: QORY_RUN_REPOSITORIES entry 3, group/lib/x, is inside entry 2, group; a repository is not cloned inside another"},
		{"git.example.com", "acme/shop,Group/app,group/app/sub,Group/app/x", "qory run prepare: QORY_RUN_REPOSITORIES entry 4, Group/app/x, is inside entry 2, Group/app; a repository is not cloned inside another"},
		{"github.com", "acme/shop,lib", "qory run prepare: QORY_RUN_REPOSITORIES entry 2 is not owner/name, the form of a repository on github.com"},
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
	if line != "qory run prepare: fetched acme/shop in Xs" {
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
		{"acme/shop,acme/lib", "qory run prepare: fetched acme/shop and 1 further repository in Xs"},
		{"acme/shop,acme/lib,acme/docs", "qory run prepare: fetched acme/shop and 2 further repositories in Xs"},
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
	want := "qory run prepare: acme/shop has .qory/repositories already, which qory keeps for the further repositories; nothing is cloned over it"
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

// TestRefusesAFolderThatIsNotEmpty refuses a working folder with a file, one that is no
// git working tree and one whose repository has a commit, and fetches nothing.
func TestRefusesAFolderThatIsNotEmpty(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	notEmpty := "qory run prepare: the working folder holds files already; the run's repository is fetched only into an empty git working tree"
	notTree := "qory run prepare: the working folder is not a git working tree without commits; the run's repository is fetched only into an empty git working tree"

	withFile := emptyFolder(t)
	write(t, filepath.Join(withFile, "notes.txt"), "notes\n")
	plain := t.TempDir()
	committed := emptyFolder(t)
	run(t, committed, "commit", "--quiet", "--allow-empty", "-m", "first")
	nested := filepath.Join(emptyFolder(t), "inner")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, dir, want string }{
		{"a file", withFile, notEmpty},
		{"no git", plain, notTree},
		{"a commit", committed, notTree},
		{"a folder inside a working tree", nested, notTree},
	} {
		_, err := prepare(context.Background(), c.dir, env("git.example.com", "acme/shop"))
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
		if out, _ := exec.Command("git", "-C", c.dir, "remote").Output(); strings.TrimSpace(string(out)) != "" {
			t.Errorf("%s: a remote was added: %s", c.name, out)
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
		prefix := "qory run prepare: " + c.path + " could not be fetched from git.example.com: "
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
	notDir := "qory run prepare: acme/shop has .qory as a file or a link, where qory keeps the further repositories in a folder; nothing is cloned over it"
	kept := "qory run prepare: acme/shop has .qory/repositories already, which qory keeps for the further repositories; nothing is cloned over it"
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

// TestSaysWhenGitCannotReadTheFolder is a git that does not run, or fails on the
// folder, worded as git's failure and not as a refusal of the folder.
func TestSaysWhenGitCannotReadTheFolder(t *testing.T) {
	needGit(t)
	dir := emptyFolder(t)
	t.Setenv("PATH", t.TempDir())
	_, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop"))
	want := `qory run prepare: git could not read the working folder: exec: "git": executable file not found in $PATH`
	if err == nil || err.Error() != want {
		t.Errorf("git missing: %v, want %s", err, want)
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
	want := "qory run prepare: the working folder's repository has a remote already; the run's repository is fetched only into an empty git working tree"
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
	prefix := "qory run prepare: acme/shop could not be fetched from git.example.com: "
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
	prefix := "qory run prepare: acme/shop could not be fetched from git.example.com: "
	for _, c := range []struct{ name, check, want string }{
		{"refused", "exit 1", "the default branch the remote names, main, is not a branch name qory fetches"},
		{"failed", "echo 'fatal: the check failed' >&2; exit 128", "fatal: the check failed"},
		{"stopped", "exec sleep 30", "git check-ref-format ran past 2s and was stopped"},
	} {
		bin := t.TempDir()
		write(t, filepath.Join(bin, "git"), "#!/bin/sh\nfor a; do [ \"$a\" = check-ref-format ] && { "+c.check+"; }; done\nexec "+gitPath+" \"$@\"\n")
		if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
			t.Fatal(err)
		}
		dir := emptyFolder(t)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		start := time.Now()
		if _, err := prepare(context.Background(), dir, env("git.example.com", "acme/shop")); err == nil || err.Error() != prefix+c.want {
			t.Errorf("%s: %v, want %s", c.name, err, prefix+c.want)
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
// proxy on the way to it.
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
	t.Cleanup(func() { baseURL = "" })
	start := time.Now()
	_, err := prepare(context.Background(), emptyFolder(t), env("git.example.com", "acme/shop"))
	want := "qory run prepare: acme/shop could not be fetched from git.example.com: git ls-remote ran past 2s and was stopped"
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
	want = "qory run prepare: acme/shop could not be fetched from git.example.com: git ls-remote was stopped before it finished"
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
// system monitor: the run's repository and a further one are fetched and checked out,
// and none of them runs.
func TestRunsNoHookAndNoMonitor(t *testing.T) {
	needGit(t)
	base := t.TempDir()
	remote(t, base, "acme/shop", "main", nil)
	remote(t, base, "acme/lib", "main", nil)
	dir := emptyFolder(t)
	marker := filepath.Join(t.TempDir(), "ran")
	hostile(t, filepath.Join(dir, ".git"), marker)
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
	want := "qory run prepare: the working folder's .git is a file or a link, which points at a repository elsewhere; the run's repository is fetched only into an empty git working tree"
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
