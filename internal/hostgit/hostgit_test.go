package hostgit_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/hostgit"
)

// TestMain runs every test of the package with a home, the XDG directories and the
// ceiling of git's search in temporary directories, and without the system git
// configuration, so that no test reads or writes the person's files; a test that runs
// git also calls [hermetic], which gives it ones for that test alone.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hostgit-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for name, value := range homeEnv(dir) {
		if err := os.MkdirAll(value, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Setenv(name, value)
	}
	os.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// homeEnv is HOME and the four XDG directories, in home.
func homeEnv(home string) map[string]string {
	return map[string]string{
		"HOME":            home,
		"XDG_STATE_HOME":  filepath.Join(home, ".local", "state"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
	}
}

// hermetic gives the test a home, the four XDG directories, a global git configuration
// and a ceiling of git's search for the test alone.
func hermetic(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range homeEnv(home) {
		t.Setenv(name, value)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ceiling(t)
	return home
}

// git runs git in dir as a person would, without hostgit, and returns its output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a checkout with a commit and the settings a person has: an identity, an
// alias and an origin remote.
func newRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "--initial-branch=main")
	git(t, dir, "config", "user.name", "Tester")
	git(t, dir, "config", "user.email", "tester@example.com")
	git(t, dir, "config", "alias.st", "status")
	git(t, dir, "remote", "add", "origin", "https://git.example.com/acme/shop.git")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "README.md")
	git(t, dir, "commit", "-q", "-m", "first")
	return dir
}

// TestEnvDropsWhatAddsConfigurationOrChoosesARepository keeps the person's choice of
// global and system file, and drops the variables that would add settings after the
// checked files, choose git's programs or choose another repository.
func TestEnvDropsWhatAddsConfigurationOrChoosesARepository(t *testing.T) {
	hermetic(t)
	for _, name := range []string{"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_EXEC_PATH", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PAGER", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR"} {
		t.Setenv(name, "set-by-the-test")
	}
	t.Setenv("GIT_CONFIG_SYSTEM", "/etc/example-gitconfig")
	t.Setenv("GIT_SSH_COMMAND", "ssh -o BatchMode=yes")
	env := hostgit.Env()
	for _, kv := range env {
		if strings.HasSuffix(kv, "=set-by-the-test") {
			t.Errorf("%s is passed on", kv)
		}
	}
	for _, want := range []string{"GIT_CONFIG_GLOBAL=" + os.Getenv("GIT_CONFIG_GLOBAL"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/etc/example-gitconfig", "GIT_SSH_COMMAND=ssh -o BatchMode=yes", "GIT_PAGER=cat", "GIT_EDITOR=:", "GIT_SEQUENCE_EDITOR=:"} {
		if !slices.Contains(env, want) {
			t.Errorf("%s is missing", want)
		}
	}
}

// TestCommandCarriesTheFlagsAndTheEnvironment puts the flags before the caller's
// arguments, runs in the directory given, and gives git the environment of [hostgit.Env].
func TestCommandCarriesTheFlagsAndTheEnvironment(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	c, err := hostgit.Command(context.Background(), dir, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.ext.allow=never", "-c", "diff.ignoreSubmodules=all", "-c", "submodule.recurse=false", "-c", "fetch.recurseSubmodules=false", "-c", "core.useReplaceRefs=false", "-c", "core.commitGraph=false", "status", "--porcelain"}
	if !slices.Equal(c.Args, want) {
		t.Errorf("args %q, want %q", c.Args, want)
	}
	if c.Dir != dir || !slices.Equal(c.Env, hostgit.Env()) {
		t.Errorf("dir %s, env %v", c.Dir, c.Env)
	}
}

// TestEnvKeepsOnlyTheAbsoluteEntriesOfThePath gives the PATH an empty entry, a relative
// one and a dot among absolute ones: git's environment keeps the absolute ones alone, in
// their order, since git would take the others from the checkout it runs in. With none,
// or no PATH at all, git gets /usr/bin:/bin.
func TestEnvKeepsOnlyTheAbsoluteEntriesOfThePath(t *testing.T) {
	sep := string(filepath.ListSeparator)
	t.Setenv("PATH", strings.Join([]string{"/usr/local/bin", "", "bin", ".", "/usr/bin", "./tools"}, sep))
	var paths []string
	for _, kv := range hostgit.Env() {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			paths = append(paths, value)
		}
	}
	if want := "/usr/local/bin" + sep + "/usr/bin"; len(paths) != 1 || paths[0] != want {
		t.Errorf("PATH %q, want %q", paths, want)
	}
	// With no absolute entry at all, git gets a fixed one, not an empty PATH, which a
	// shell may search the working folder for.
	t.Setenv("PATH", strings.Join([]string{"", "bin", "."}, sep))
	paths = nil
	for _, kv := range hostgit.Env() {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			paths = append(paths, value)
		}
	}
	if want := "/usr/bin:/bin"; len(paths) != 1 || paths[0] != want {
		t.Errorf("PATH %q with no absolute entry, want %q", paths, want)
	}
	// With no PATH, too: a shell's default one may end in the working folder.
	if err := os.Unsetenv("PATH"); err != nil {
		t.Fatal(err)
	}
	paths = nil
	for _, kv := range hostgit.Env() {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			paths = append(paths, value)
		}
	}
	if want := "/usr/bin:/bin"; len(paths) != 1 || paths[0] != want {
		t.Errorf("PATH %q with no PATH, want %q", paths, want)
	}
}

// TestGitRunsByItsResolvedPath puts on the PATH a link to the folder of the git found
// before: git runs by its path with the symbolic links resolved, so a link pointed
// elsewhere after the search is not followed again.
func TestGitRunsByItsResolvedPath(t *testing.T) {
	hermetic(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := newRepo(t)
	link := filepath.Join(t.TempDir(), "tools")
	if err := os.Symlink(filepath.Dir(real), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", link)
	c, err := hostgit.Command(context.Background(), dir, "status")
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(real); err != nil || c.Path != resolved {
		t.Errorf("git is %s, want %s (%v)", c.Path, resolved, err)
	}
}

// ceiling keeps git, in the test and in hostgit, from looking for a repository above
// the test's temporary directories.
func ceiling(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
}
