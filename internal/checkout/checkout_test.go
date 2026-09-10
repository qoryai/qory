package checkout_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/checkout"
)

// hermetic points git at an empty global configuration and away from the system one, so no
// test reads the machine's git identity or its excludes file.
func hermetic(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// initRepo makes dir a git checkout with a local identity and returns dir.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// real resolves the symlinks in a path, because git reports the resolved toplevel and a
// temporary directory on macOS is reached through one.
func real(t *testing.T, path string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestRootFindsTheGitWorkingTree looks up from a directory nested inside the checkout.
func TestRootFindsTheGitWorkingTree(t *testing.T) {
	hermetic(t)
	root := initRepo(t, t.TempDir())
	inside := filepath.Join(root, "internal", "checkout")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := checkout.Root(inside)
	if err != nil {
		t.Fatal(err)
	}
	if want := real(t, root); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestRootOutsideGitReturnsTheDirectory records what Root does with no checkout above it: it
// answers with the absolute directory and no error.
func TestRootOutsideGitReturnsTheDirectory(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	got, err := checkout.Root(dir)
	if err != nil {
		t.Fatalf("Root outside git: %v", err)
	}
	if got != dir {
		t.Errorf("got %s, want %s", got, dir)
	}
}

// TestRootMakesARelativeDirectoryAbsolute checks the one thing Root does before it asks git.
func TestRootMakesARelativeDirectoryAbsolute(t *testing.T) {
	hermetic(t)
	dir := t.TempDir()
	t.Chdir(dir)
	got, err := checkout.Root(".")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("got %s, want an absolute path", got)
	}
	if real(t, got) != real(t, dir) {
		t.Errorf("got %s, want %s", got, dir)
	}
}

// TestQoryDirIsUnderTheRoot names the directory qory keeps in a checkout.
func TestQoryDirIsUnderTheRoot(t *testing.T) {
	if got, want := checkout.QoryDir("/work/app"), filepath.Join("/work/app", ".qory"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if checkout.Dir != ".qory" {
		t.Errorf("Dir is %q, want .qory", checkout.Dir)
	}
}

// TestExcludeFileIsTheCloneLocalExcludeFile covers both answers: the path inside .git, and
// the empty string outside a checkout.
func TestExcludeFileIsTheCloneLocalExcludeFile(t *testing.T) {
	hermetic(t)
	root := initRepo(t, t.TempDir())
	got := checkout.ExcludeFile(root)
	if want := filepath.Join(root, ".git", "info", "exclude"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := checkout.ExcludeFile(t.TempDir()); got != "" {
		t.Errorf("got %s outside git, want the empty string", got)
	}
}

// TestRepoKeyReturnsOwnerAndNameFromTheOriginRemote covers the URL forms a remote takes, and
// the fallback when the checkout has no remote.
func TestRepoKeyReturnsOwnerAndNameFromTheOriginRemote(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"an https URL", "https://git.example.com/acme/app.git", "acme/app"},
		{"an https URL without the git suffix", "https://git.example.com/acme/app", "acme/app"},
		{"an https URL with a trailing slash", "https://git.example.com/acme/app/", "acme/app"},
		{"an ssh URL", "git@git.example.com:acme/app.git", "acme/app"},
		{"a nested owner path keeps the last two parts", "https://git.example.com/acme/team/app.git", "team/app"},
		{"a local path", "/srv/git/acme/app.git", "acme/app"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hermetic(t)
			root := initRepo(t, t.TempDir())
			cmd := exec.Command("git", "remote", "add", "origin", c.url)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git remote add: %v\n%s", err, out)
			}
			if got := checkout.RepoKey(root); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

// TestRepoKeyFallsBackToTheDirectoryName covers a checkout with no origin remote and a
// directory that is no checkout at all.
func TestRepoKeyFallsBackToTheDirectoryName(t *testing.T) {
	hermetic(t)
	for _, name := range []string{"a checkout without a remote", "no checkout"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "app")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if name == "a checkout without a remote" {
				initRepo(t, root)
			}
			if got := checkout.RepoKey(root); got != "app" {
				t.Errorf("got %s, want app", got)
			}
		})
	}
}

// git runs one git command in dir and fails the test with its output.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// write creates a file and the directories above it.
func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRestorableIsTrackedAndUnmodified is the rule --force replaces under: git checkout --
// must be able to bring the path back.
func TestRestorableIsTrackedAndUnmodified(t *testing.T) {
	hermetic(t)
	root := initRepo(t, t.TempDir())
	write(t, filepath.Join(root, "AGENTS.md"), "ours\n")
	write(t, filepath.Join(root, ".claude", "settings.json"), "{}\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "first")
	write(t, filepath.Join(root, "NOTES.md"), "untracked\n")
	if reason := checkout.Restorable(root, filepath.Join(root, "AGENTS.md")); reason != "" {
		t.Errorf("a tracked, unmodified file: %q", reason)
	}
	if reason := checkout.Restorable(root, filepath.Join(root, ".claude")); reason != "" {
		t.Errorf("a directory of tracked files: %q", reason)
	}
	if reason := checkout.Restorable(root, filepath.Join(root, "NOTES.md")); reason != "is not tracked in git" {
		t.Errorf("an untracked file: %q", reason)
	}
	write(t, filepath.Join(root, "AGENTS.md"), "edited\n")
	if reason := checkout.Restorable(root, filepath.Join(root, "AGENTS.md")); reason != "has uncommitted changes" {
		t.Errorf("a modified file: %q", reason)
	}
	write(t, filepath.Join(root, ".claude", "settings.local.json"), "{}\n")
	if reason := checkout.Restorable(root, filepath.Join(root, ".claude")); reason != "holds files git does not track" {
		t.Errorf("a directory with an untracked file in it: %q", reason)
	}
	write(t, filepath.Join(root, ".gitignore"), ".claude/settings.local.json\n")
	git(t, root, "add", ".gitignore")
	git(t, root, "commit", "-q", "-m", "ignore")
	if reason := checkout.Restorable(root, filepath.Join(root, ".claude")); reason != "holds files git does not track" {
		t.Errorf("a directory with an ignored file in it: %q", reason)
	}
	if reason := checkout.Restorable(root, filepath.Join(t.TempDir(), "elsewhere")); reason != "is outside the checkout" {
		t.Errorf("a path outside the checkout: %q", reason)
	}
}

// TestRestorableSeesUntrackedFilesTheConfigurationHides is a repository whose config sets
// status.showUntrackedFiles to no: an untracked file under a tracked directory still
// makes the directory not restorable, because git checkout would not bring it back.
func TestRestorableSeesUntrackedFilesTheConfigurationHides(t *testing.T) {
	hermetic(t)
	root := initRepo(t, t.TempDir())
	git(t, root, "config", "status.showUntrackedFiles", "no")
	write(t, filepath.Join(root, ".claude", "settings.json"), "{}\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "first")
	write(t, filepath.Join(root, ".claude", "settings.local.json"), "{}\n")
	if reason := checkout.Restorable(root, filepath.Join(root, ".claude")); reason != "holds files git does not track" {
		t.Errorf("a directory with an untracked file the configuration hides: %q", reason)
	}
}

// TestRepoKeyNeverTakesTheHostForAnOwner covers remotes with one path segment, a scheme
// with no owner, and a trailing slash: the directory name is the answer.
func TestRepoKeyNeverTakesTheHostForAnOwner(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ url, want string }{
		{"https://git.example.com/acme/app.git", "acme/app"},
		{"git@git.example.com:acme/app.git", "acme/app"},
		{"ssh://git@git.example.com:2222/acme/app", "acme/app"},
		{"https://git.example.com/group/sub/app.git", "sub/app"},
		{"https://git.example.com/app.git", ""},
		{"file:///srv/git/app.git", "git/app"},
		{"https://git.example.com/acme/", ""},
		{"/srv/app", "srv/app"},
	} {
		root := initRepo(t, t.TempDir())
		git(t, root, "remote", "add", "origin", c.url)
		want := c.want
		if want == "" {
			want = filepath.Base(root)
		}
		if got := checkout.RepoKey(root); got != want {
			t.Errorf("%s: got %q, want %q", c.url, got, want)
		}
	}
}
