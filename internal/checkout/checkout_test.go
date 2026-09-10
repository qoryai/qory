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
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
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
