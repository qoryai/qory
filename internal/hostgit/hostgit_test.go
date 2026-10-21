package hostgit_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/places"
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
// and a ceiling of git's search for the test alone, and forgets what the check kept from
// an earlier test.
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
	hostgit.Forget()
	t.Cleanup(hostgit.Forget)
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

// record records dir as the checkout of a walled run.
func record(t *testing.T, dir string) {
	t.Helper()
	if err := hostgit.Record(context.Background(), dir, places.KindCheckout, "run-1"); err != nil {
		t.Fatal(err)
	}
}

// run runs git status through hostgit in dir and returns the refusal, nil when it ran.
func run(t *testing.T, dir string) error {
	t.Helper()
	c, err := hostgit.Command(context.Background(), dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return nil
}

// refusedKey fails the test unless err refuses key in dir with the approved line.
func refusedKey(t *testing.T, err error, dir, key string) {
	t.Helper()
	want := "the git configuration of " + dir + " sets " + key + ", which qory does not write; qory runs no git there, since a run could have set it to make git on this machine run a program"
	var r *hostgit.RefusedError
	if !errors.As(err, &r) || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
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

// TestNoPlaceNoCheck runs git in a repository no walled run used, whatever it sets.
func TestNoPlaceNoCheck(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	git(t, dir, "config", "alias.x", "log")
	if err := run(t, dir); err != nil {
		t.Fatal(err)
	}
}

// TestTheFingerprintTakesThePersonsSettings accepts what the checkout held when it was
// recorded, and the keys qory and git write with any value, added later or changed: a
// branch's remote, merge and qory-base, and a remote's refspec.
func TestTheFingerprintTakesThePersonsSettings(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	if err := run(t, dir); err != nil {
		t.Fatalf("the settings the checkout held: %v", err)
	}
	git(t, dir, "config", "branch.feature.qory-base", "main")
	git(t, dir, "config", "branch.feature.remote", "origin")
	git(t, dir, "config", "branch.feature.merge", "refs/heads/feature")
	git(t, dir, "config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*")
	if err := run(t, dir); err != nil {
		t.Fatalf("keys qory and git write: %v", err)
	}
}

// refusedURL fails the test unless err refuses the remote's url key in dir with its line.
func refusedURL(t *testing.T, err error, dir, key string) {
	t.Helper()
	want := "the git configuration of " + dir + " sets " + key + " to a URL it did not have when a walled run began here; qory runs no git there, since the run could have pointed it at another server, which git on this machine would reach with this machine's credentials. qory setup repo --accept-checkout accepts it"
	var r *hostgit.RefusedError
	if !errors.As(err, &r) || r.Reason != hostgit.RefusedRemote || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// TestARemoteURLTheRecordDoesNotHoldIsRefused changes the origin remote's URL, adds a
// second URL to it, and adds a remote, each since the checkout was recorded: git on this
// machine would fetch from, push to and ask that server with the person's credentials,
// so each is refused; the URL the record holds passes again. A repository inside the
// place with no fingerprint has its remote's URL refused too.
func TestARemoteURLTheRecordDoesNotHoldIsRefused(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	git(t, dir, "remote", "set-url", "origin", "https://git.example.com/acme/other.git")
	refusedURL(t, run(t, dir), dir, "remote.origin.url")
	git(t, dir, "remote", "set-url", "origin", "https://git.example.com/acme/shop.git")
	if err := run(t, dir); err != nil {
		t.Fatalf("the URL the record holds: %v", err)
	}
	git(t, dir, "config", "--add", "remote.origin.url", "https://git.example.com/acme/other.git")
	refusedURL(t, run(t, dir), dir, "remote.origin.url")
	git(t, dir, "config", "--unset", "remote.origin.url", "other")
	git(t, dir, "remote", "add", "mirror", "https://git.example.com/acme/mirror.git")
	refusedURL(t, run(t, dir), dir, "remote.mirror.url")
	git(t, dir, "remote", "remove", "mirror")
	if err := run(t, dir); err != nil {
		t.Fatalf("the remote taken out: %v", err)
	}
	inner := filepath.Join(dir, "vendor", "lib")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, inner, "init", "-q")
	git(t, inner, "remote", "add", "origin", "https://git.example.com/acme/lib.git")
	// qory holds no fingerprint of this repository, so its line says so, not that the URL
	// changed.
	want := "the git configuration of " + inner + " sets remote.origin.url to a URL, and qory holds no record of the repository from when a walled run began here; qory runs no git there, since a run could have pointed it at another server, which git on this machine would reach with this machine's credentials. qory setup repo --accept-checkout accepts it"
	var r *hostgit.RefusedError
	if err := run(t, inner); !errors.As(err, &r) || r.Reason != hostgit.RefusedRemoteUnrecorded || err.Error() != want {
		t.Errorf("a repository with no fingerprint: got %v, want %q", err, want)
	}
}

// TestARemoteWithNoURLIsRefused gives a remote keys but no url, which git takes the
// remote's name for: a remote named by a URL, and origin with its url taken out and its
// fetch kept, which git fetches from ./origin. Each is refused, by the check and by
// Changes, whatever the fingerprint holds, and passes once removed.
func TestARemoteWithNoURLIsRefused(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	refused := func(what, key string) {
		t.Helper()
		want := "the git configuration of " + dir + " sets " + key + " for a remote it gives no URL; qory runs no git there, since git takes the remote's name for its URL then, which a run could have chosen to reach another server. Remove it to go on"
		var r *hostgit.RefusedError
		if err := run(t, dir); !errors.As(err, &r) || r.Reason != hostgit.RefusedRemoteNoURL || err.Error() != want {
			t.Errorf("%s: got %v, want %q", what, err, want)
		}
		recs, err := places.Load()
		if err != nil {
			t.Fatal(err)
		}
		repo, _ := hostgit.Resolve(context.Background(), dir)
		if _, err := hostgit.Changes(context.Background(), recs, repo); err == nil || err.Error() != want {
			t.Errorf("%s: Changes: %v, want %q", what, err, want)
		}
	}
	named := "remote.https://git.example.com/acme/other.git.fetch"
	git(t, dir, "config", named, "+refs/heads/*:refs/remotes/other/*")
	refused("a remote named by a URL", named)
	git(t, dir, "config", "--unset", named)
	if err := run(t, dir); err != nil {
		t.Fatalf("the remote taken out: %v", err)
	}
	git(t, dir, "config", "--unset", "remote.origin.url")
	refused("origin with no url", "remote.origin.fetch")
	git(t, dir, "config", "remote.origin.url", "https://git.example.com/acme/shop.git")
	if err := run(t, dir); err != nil {
		t.Fatalf("the url put back: %v", err)
	}
}

// TestARemoteFileIsRefused writes a file in the git directory's remotes folder, and one
// in its branches folder, which git reads for the URL of a remote the configuration
// gives none: each is refused, and qory runs no git until it is removed. The empty
// folders git init may leave pass.
func TestARemoteFileIsRefused(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	for _, f := range []string{"remotes", "branches"} {
		folder := filepath.Join(dir, ".git", f)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := run(t, dir); err != nil {
			t.Fatalf("an empty %s folder: %v", f, err)
		}
		file := filepath.Join(folder, "origin")
		if err := os.WriteFile(file, []byte("URL: https://git.example.com/acme/other.git\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		want := "the git directory of " + dir + " holds " + file + ", which qory does not write; qory runs no git there, since a run could have written it to point a remote at another server. Remove it to go on"
		var r *hostgit.RefusedError
		if err := run(t, dir); !errors.As(err, &r) || r.Reason != hostgit.RefusedRemoteFile || err.Error() != want {
			t.Errorf("%s: got %v, want %q", f, err, want)
		}
		recs, err := places.Load()
		if err != nil {
			t.Fatal(err)
		}
		repo, _ := hostgit.Resolve(context.Background(), dir)
		if _, err := hostgit.Changes(context.Background(), recs, repo); err == nil || err.Error() != want {
			t.Errorf("%s: Changes: %v", f, err)
		}
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		if err := run(t, dir); err != nil {
			t.Fatalf("%s removed: %v", f, err)
		}
	}
}

// TestAKeyAddedOrChangedSinceTheRecordIsRefused refuses a key the fingerprint does not
// hold, and a key it holds with another value, each with the approved line; and sees
// the change in the same process, after a check that passed.
func TestAKeyAddedOrChangedSinceTheRecordIsRefused(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	if err := run(t, dir); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "config", "alias.x", "log")
	refusedKey(t, run(t, dir), dir, "alias.x")
	git(t, dir, "config", "--unset", "alias.x")
	if err := run(t, dir); err != nil {
		t.Fatalf("the key taken out again: %v", err)
	}
	git(t, dir, "config", "user.email", "someone@example.com")
	refusedKey(t, run(t, dir), dir, "user.email")
}

// TestASettingWithoutAValueHashesApart refuses a key written with no value where the
// fingerprint holds it with an empty one.
func TestASettingWithoutAValueHashesApart(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	git(t, dir, "config", "alias.empty", "")
	record(t, dir)
	config := filepath.Join(dir, ".git", "config")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(strings.Replace(string(data), "empty = ", "empty", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	refusedKey(t, run(t, dir), dir, "alias.empty")
}

// TestIncludes accepts an include the checkout held when it was recorded only when it
// names a file outside every recorded place by an absolute path or one under ~/, and
// refuses every include added since.
func TestIncludes(t *testing.T) {
	cases := []struct {
		name   string
		path   func(home, dir string) string
		before bool
		ok     bool
	}{
		{"absolute, outside, there before", func(home, _ string) string { return filepath.Join(home, "team.gitconfig") }, true, true},
		{"under ~/, outside, there before", func(string, string) string { return "~/team.gitconfig" }, true, true},
		{"relative, there before", func(string, string) string { return "../team.gitconfig" }, true, false},
		{"absolute, inside the place, there before", func(_, dir string) string { return filepath.Join(dir, "team.gitconfig") }, true, false},
		{"absolute, outside, added since", func(home, _ string) string { return filepath.Join(home, "team.gitconfig") }, false, false},
	}
	for _, c := range cases {
		for _, include := range []string{"include.path", "includeIf.gitdir:%s/.path"} {
			t.Run(c.name+", "+include[:strings.Index(include, ".")], func(t *testing.T) {
				home := hermetic(t)
				dir := newRepo(t)
				for _, d := range []string{home, dir} {
					if err := os.WriteFile(filepath.Join(d, "team.gitconfig"), []byte("[alias]\n\tlg = log\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				key := include
				if strings.Contains(key, "%s") {
					key = strings.Replace(key, "%s", dir, 1)
				}
				if c.before {
					git(t, dir, "config", key, c.path(home, dir))
				}
				record(t, dir)
				if !c.before {
					git(t, dir, "config", key, c.path(home, dir))
				}
				err := run(t, dir)
				switch {
				case c.ok && err != nil:
					t.Errorf("%s: %v", key, err)
				case !c.ok:
					refusedKey(t, err, dir, strings.Replace(key, "includeIf", "includeif", 1))
				}
			})
		}
	}
}

// TestAMovedGitIsRefused refuses a checkout whose .git points at another git directory
// than when it was recorded, with the approved line, and accepts the one it pointed at.
// It checks the checkout 3 times first, so that the check keeps what it read.
func TestAMovedGitIsRefused(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	for i := 0; i < 3; i++ {
		if err := run(t, dir); err != nil {
			t.Fatalf("check %d as recorded: %v", i+1, err)
		}
	}
	moved := filepath.Join(dir, "elsewhere.git")
	if err := os.Rename(filepath.Join(dir, ".git"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+moved+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run(t, dir)
	var r *hostgit.RefusedError
	if !errors.As(err, &r) || r.Reason != hostgit.RefusedPointer || err.Error() != "the checkout's .git points somewhere else since a walled run began here; qory runs no git there, since the run could have pointed it. qory setup repo --accept-checkout accepts it" {
		t.Fatalf("got %v", err)
	}
}

// TestAConfigGitCannotReadIsRefused refuses a malformed configuration in a recorded place
// with the approved line.
func TestAConfigGitCannotReadIsRefused(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[broken\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	err = run(t, dir)
	want := "the git configuration of " + dir + " cannot be read; qory runs no git there, since a run could have set it to make git on this machine run a program"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

// TestConfigWorktreeIsChecked refuses a per-worktree configuration added since the
// record: the key that turns it on, and a key inside it.
func TestConfigWorktreeIsChecked(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".git", "config.worktree"), []byte("[alias]\n\tlg = log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refusedKey(t, run(t, dir), dir, "alias.lg")
	hostgit.Forget()
	if err := os.Remove(filepath.Join(dir, ".git", "config.worktree")); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "config", "extensions.worktreeConfig", "true")
	refusedKey(t, run(t, dir), dir, "extensions.worktreeconfig")
}

// TestOnlyFilesInARecordedPlaceAreChecked records a linked worktree whose repository lies
// outside it: the main checkout's configuration is the person's, and a key added there
// is not refused; its .git is still checked.
func TestOnlyFilesInARecordedPlaceAreChecked(t *testing.T) {
	hermetic(t)
	main := newRepo(t)
	wt := filepath.Join(filepath.Dir(main), filepath.Base(main)+"-feature")
	git(t, main, "worktree", "add", "-q", "-b", "feature", wt)
	t.Cleanup(func() { os.RemoveAll(wt) })
	record(t, wt)
	git(t, main, "config", "alias.x", "log")
	if err := run(t, wt); err != nil {
		t.Fatalf("a key in the main checkout's configuration: %v", err)
	}
	if err := run(t, main); err != nil {
		t.Fatalf("the main checkout: %v", err)
	}
}

// TestAControlCharacterIsQuoted shows a key that carries a control character as Go
// quotes it, so it cannot act on the terminal.
func TestAControlCharacterIsQuoted(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	git(t, dir, "config", "alias.a\x1b[2Jb.x", "log")
	err := run(t, dir)
	if err == nil || !strings.Contains(err.Error(), ` sets "alias.a\x1b[2Jb.x", which`) || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("got %q", err)
	}
	for _, plain := range []string{"acme/shop", "/home/you/my shop", "café/日本"} {
		if got := hostgit.Shown(plain); got != plain {
			t.Errorf("%q shows as %q", plain, got)
		}
	}
	for s, want := range map[string]string{
		"acme/\u202epohs":       `"acme/\u202epohs"`,
		"acme/sh\u200bop":       `"acme/sh\u200bop"`,
		"acme/\u2066shop\u2069": `"acme/\u2066shop\u2069"`,
		"acme/\ufeffshop":       `"acme/\ufeffshop"`,
		"acme/\u00a0shop":       `"acme/\u00a0shop"`,
	} {
		if got := hostgit.Shown(s); got != want {
			t.Errorf("%q shows as %s, want %s", s, got, want)
		}
	}
}

// TestTheRecordHoldsHashesOnly writes the record mode 0600 in a folder of mode 0700, with
// the fingerprint as hashes and no value of any setting, and keeps it as it is when the
// place is recorded again.
func TestTheRecordHoldsHashesOnly(t *testing.T) {
	home := hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	file := filepath.Join(home, ".local", "state", "qory", "places", places.Name(dir))
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"tester@example.com", "Tester", "git.example.com", "status"} {
		if strings.Contains(string(data), value) {
			t.Errorf("the record holds %q:\n%s", value, data)
		}
	}
	for path, mode := range map[string]os.FileMode{file: 0o600, filepath.Dir(file): 0o700} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v %v", path, info.Mode().Perm(), err)
		}
	}
	r, err := places.Get(dir)
	if err != nil || r == nil || r.Run != "run-1" || r.Kind != places.KindCheckout || len(r.Git) != 1 || len(r.Git[0].Settings) == 0 {
		t.Fatalf("record %+v, %v", r, err)
	}
	git(t, dir, "config", "alias.x", "log")
	if err := hostgit.Record(context.Background(), dir, places.KindCheckout, "run-2"); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(file)
	if err != nil || string(again) != string(data) {
		t.Errorf("a later run changed the record:\n%s", again)
	}
	refusedKey(t, run(t, dir), dir, "alias.x")
}

// TestChangesListsWhatTheCheckWouldRefuse names each key the fingerprint does not hold,
// once, never a value, and a .git that moved.
func TestChangesListsWhatTheCheckWouldRefuse(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	git(t, dir, "config", "alias.x", "log")
	git(t, dir, "config", "--add", "alias.x", "show")
	git(t, dir, "config", "user.email", "someone@example.com")
	git(t, dir, "config", "branch.feature.qory-base", "main")
	recs, err := places.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := hostgit.Resolve(context.Background(), dir)
	if !ok {
		t.Fatal("no repository")
	}
	ch, err := hostgit.Changes(context.Background(), recs, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ch.Keys, []string{"user.email", "alias.x"}) || ch.Moved {
		t.Errorf("changes %+v", ch)
	}
}

// TestNoStateDirectoryRefusesEveryGit is a state directory qory cannot take, HOME and
// XDG_STATE_HOME unset or XDG_STATE_HOME relative: qory cannot read which places a walled
// run used, so it runs no git, in a checkout a walled run used or anywhere else.
func TestNoStateDirectoryRefusesEveryGit(t *testing.T) {
	for _, c := range []struct {
		name, home, state string
		want              error
	}{
		{"no HOME and no XDG_STATE_HOME", "", "", places.ErrNoStateDir},
		{"a relative XDG_STATE_HOME", "home", "state", places.ErrRelativeStateHome},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := hermetic(t)
			dir := newRepo(t)
			record(t, dir)
			other := newRepo(t)
			if c.home != "" {
				c.home = home
			}
			t.Setenv("HOME", c.home)
			t.Setenv("XDG_STATE_HOME", c.state)
			hostgit.Forget()
			for _, d := range []string{dir, other} {
				err := run(t, d)
				if !hostgit.Refused(err) || !errors.Is(err, c.want) || err.Error() != c.want.Error() {
					t.Errorf("%s: %v, want %v", d, err, c.want)
				}
			}
		})
	}
}

// TestAGitFileInARecordedPlaceTakesItsConfigurationAlong records a folder that is no
// repository, as a walled run's mount, and writes in it a .git file that leads to a
// repository outside every recorded place, as a run could: that repository's
// configuration is checked wherever it lies, and a key qory and git do not write is
// refused. So is one reached through a commondir file the run wrote in a git directory
// inside the place. The repository itself, outside, is the person's, and git runs there.
func TestAGitFileInARecordedPlaceTakesItsConfigurationAlong(t *testing.T) {
	hermetic(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git(t, outside, "init", "-q", "--initial-branch=main")
	git(t, outside, "config", "alias.x", "log")
	place, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := places.Update(place, places.KindMount, func(*places.Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(place, "lib")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, ".git"), []byte("gitdir: "+filepath.Join(outside, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refusedKey(t, run(t, inner), inner, "alias.x")

	gitDir := filepath.Join(place, "lib.git")
	for _, d := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(gitDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{"HEAD": "ref: refs/heads/main\n", "commondir": filepath.Join(outside, ".git") + "\n"} {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(inner, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refusedKey(t, run(t, inner), inner, "alias.x")
	if err := run(t, outside); err != nil {
		t.Fatalf("the repository outside: %v", err)
	}
}

// TestNoGitIsSaid runs with no git on the PATH in a checkout a walled run used: the
// refusal says git is missing, not that the configuration cannot be read.
func TestNoGitIsSaid(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	t.Setenv("PATH", t.TempDir())
	hostgit.Forget()
	want := "git is not on the PATH, so qory cannot check the git configuration of " + dir + "; qory runs no git there, since a run could have set it to make git on this machine run a program"
	var r *hostgit.RefusedError
	if _, err := hostgit.Command(context.Background(), dir, "status"); !errors.As(err, &r) || r.Reason != hostgit.RefusedNoGit || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}

// TestAHookTheConfigurationDefinesIsRefused sets hook.x.command and hook.x.event in a
// recorded repository, a hook git runs whatever core.hooksPath says: the check refuses
// each key, as it refuses every key qory and git do not write. Only the configuration is
// read; no hook runs.
func TestAHookTheConfigurationDefinesIsRefused(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	for _, key := range []string{"hook.x.command", "hook.x.event"} {
		value := "pre-commit"
		if key == "hook.x.command" {
			value = "true"
		}
		git(t, dir, "config", key, value)
		want := "the git configuration of " + dir + " sets " + key + ", which qory does not write; qory runs no git there, since a run could have set it to make git on this machine run a program"
		var r *hostgit.RefusedError
		if err := run(t, dir); !errors.As(err, &r) || r.Reason != hostgit.RefusedSetting || err.Error() != want {
			t.Errorf("%s: got %v, want %q", key, err, want)
		}
		git(t, dir, "config", "--unset", key)
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

// TestGitIsFoundOutsideTheRecordedPlaces puts on the PATH a folder of a recorded
// checkout that holds a file named git, empty and marked to be run, as a run could have
// written it, and a link outside every place that leads to that folder, before the
// folder of the git found before. The check, and the git qory runs, take git from that
// folder; git's PATH holds neither entry. Nothing runs the file.
func TestGitIsFoundOutsideTheRecordedPlaces(t *testing.T) {
	hermetic(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := newRepo(t)
	record(t, dir)
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "git"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "tools")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.ListSeparator)
	t.Setenv("PATH", bin+sep+link+sep+filepath.Dir(real))
	hostgit.Forget()
	c, err := hostgit.Command(context.Background(), dir, "status")
	if err != nil {
		t.Fatalf("the check: %v", err)
	}
	// git runs by its path with the symbolic links resolved, as gitIn checked it.
	if resolved, err := filepath.EvalSymlinks(real); err != nil || c.Path != resolved {
		t.Errorf("git is %s, want %s (%v)", c.Path, resolved, err)
	}
	for _, kv := range c.Env {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok && value != filepath.Dir(real) {
			t.Errorf("PATH %q, want %q", value, filepath.Dir(real))
		}
	}
}

// TestARemoteFolderQoryCannotListIsRefused checks a recorded repository with no remotes
// folder, then makes one that holds a file and that qory may not list, mode 0311, as a
// run could: git still opens a file in it by name, so it is refused, though the folder
// was missing when the check last passed. A link where the branches folder goes is
// refused too. A test run as root lists any folder, so it skips the first part.
func TestARemoteFolderQoryCannotListIsRefused(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	refused := func(what, folder string) {
		t.Helper()
		want := "the git directory of " + dir + " holds " + folder + ", which qory cannot list; qory runs no git there, since a run could have hidden in it a file that points a remote at another server. Remove it to go on"
		var r *hostgit.RefusedError
		if err := run(t, dir); !errors.As(err, &r) || r.Reason != hostgit.RefusedRemoteFolder || err.Error() != want {
			t.Errorf("%s: got %v, want %q", what, err, want)
		}
	}
	if os.Geteuid() != 0 {
		remotes := filepath.Join(dir, ".git", "remotes")
		if err := os.RemoveAll(remotes); err != nil {
			t.Fatal(err)
		}
		if err := run(t, dir); err != nil {
			t.Fatalf("no remotes folder: %v", err)
		}
		if err := os.Mkdir(remotes, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remotes, "origin"), []byte("URL: https://git.example.com/acme/other.git\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(remotes, 0o311); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(remotes, 0o755) })
		refused("a folder qory may not list", remotes)
		if err := os.Chmod(remotes, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(remotes); err != nil {
			t.Fatal(err)
		}
	}
	branches := filepath.Join(dir, ".git", "branches")
	if err := os.RemoveAll(branches); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), branches); err != nil {
		t.Fatal(err)
	}
	refused("a link where the branches folder goes", branches)
}

// TestAPlaceAWalledRunHoldsIsRefused checks a recorded checkout and a worktree of it
// outside every recorded place, then holds the checkout as a walled run that starts
// does: git in the checkout is refused, and so is git in the worktree, whose git
// directory and common directory lie in the checkout, though both passed the check
// before, and again once the check forgot it had. Once the run lets go, both pass again.
func TestAPlaceAWalledRunHoldsIsRefused(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	worktree := filepath.Join(t.TempDir(), "feature")
	git(t, dir, "worktree", "add", "-q", "-b", "feature", worktree)
	worktree, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, worktree} {
		if err := run(t, d); err != nil {
			t.Fatalf("%s before the run: %v", d, err)
		}
	}
	live, err := places.Starting([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Release()
	for i, d := range []string{dir, worktree, worktree} {
		if i == 2 {
			hostgit.Forget()
		}
		want := d + " is in use by a walled run; qory runs no git there until the run ends, since the run could change git's configuration between qory's check and git's read"
		var r *hostgit.RefusedError
		if err := run(t, d); !errors.As(err, &r) || r.Reason != hostgit.RefusedInUse || err.Error() != want {
			t.Errorf("%s: got %v, want %q", d, err, want)
		}
	}
	live.Release()
	for _, d := range []string{dir, worktree} {
		if err := run(t, d); err != nil {
			t.Errorf("%s after the run: %v", d, err)
		}
	}
}

// TestARunThatStartsWaitsForGit checks a recorded checkout, and starts a walled run
// there before git runs: the run waits from the check until git has run, or until the
// command is released unrun, and starts then.
func TestARunThatStartsWaitsForGit(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	record(t, dir)
	for _, end := range []string{"run", "released"} {
		c, err := hostgit.Command(context.Background(), dir, "status", "--porcelain")
		if err != nil {
			t.Fatal(err)
		}
		started := make(chan *places.Hold, 1)
		go func() {
			h, err := places.Starting([]string{dir})
			if err != nil {
				t.Error(err)
			}
			started <- h
		}()
		select {
		case h := <-started:
			h.Release()
			t.Fatalf("%s: a run started between the check and git", end)
		case <-time.After(300 * time.Millisecond):
		}
		if end == "run" {
			if out, err := c.Output(); err != nil {
				t.Fatalf("git status: %v\n%s", err, out)
			}
		} else {
			c.Release()
		}
		select {
		case h := <-started:
			h.Release()
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the run did not start once git was done", end)
		}
	}
}

// TestAFileThatLeadsToOtherObjectsIsRefusedUnlessTheRecordHoldsIt writes
// objects/info/alternates, then info/grafts, into a recorded repository that held
// neither: each is refused, by the check and by the changes accept-checkout reads. An
// alternates file there when the place was recorded, as in a clone made with
// --reference, passes until it changes. A record written before qory took these files,
// and a repository the records hold no entry for, refuse any such file with the line that
// says qory holds no record of it. Only the check runs: no git reads the files.
func TestAFileThatLeadsToOtherObjectsIsRefusedUnlessTheRecordHoldsIt(t *testing.T) {
	hermetic(t)
	ctx := context.Background()
	checkOnly := func(dir string) error {
		_, err := hostgit.Command(ctx, dir, "status")
		return err
	}
	refused := func(what, dir, file string, err error) {
		t.Helper()
		want := "the git directory of " + dir + " holds " + file + ", which it did not hold when a walled run began here; qory runs no git there, since a run could have written it to change which objects or history git reads. Remove it to go on"
		var r *hostgit.RefusedError
		if !errors.As(err, &r) || r.Reason != hostgit.RefusedObjectsFile || err.Error() != want {
			t.Errorf("%s: got %v, want %q", what, err, want)
		}
	}
	changes := func(dir string) error {
		t.Helper()
		recs, err := places.Load()
		if err != nil {
			t.Fatal(err)
		}
		repo, ok := hostgit.Resolve(ctx, dir)
		if !ok {
			t.Fatal("no repository")
		}
		_, err = hostgit.Changes(ctx, recs, repo)
		return err
	}
	write := func(file, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(t.TempDir(), "other.git", "objects")

	dir := newRepo(t)
	record(t, dir)
	for _, f := range []string{"objects/info/alternates", "info/grafts"} {
		file := filepath.Join(dir, ".git", filepath.FromSlash(f))
		write(file, other+"\n")
		refused(f, dir, file, checkOnly(dir))
		refused(f+", the changes", dir, file, changes(dir))
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		if err := checkOnly(dir); err != nil {
			t.Errorf("%s removed: %v", f, err)
		}
	}

	referenced := newRepo(t)
	alternates := filepath.Join(referenced, ".git", "objects", "info", "alternates")
	write(alternates, other+"\n")
	record(t, referenced)
	if err := checkOnly(referenced); err != nil {
		t.Errorf("alternates there when the place was recorded: %v", err)
	}
	if err := changes(referenced); err != nil {
		t.Errorf("alternates there when the place was recorded, the changes: %v", err)
	}
	write(alternates, other+"\n"+filepath.Join(t.TempDir(), "objects")+"\n")
	refused("alternates changed", referenced, alternates, checkOnly(referenced))
	write(alternates, other+"\n")
	if err := checkOnly(referenced); err != nil {
		t.Errorf("alternates as recorded again: %v", err)
	}
	err := places.Update(referenced, places.KindCheckout, func(r *places.Record) error {
		for i := range r.Git {
			r.Git[i].Objects = nil
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	unrecorded := func(what, dir, file string, err error) {
		t.Helper()
		want := "the git directory of " + dir + " holds " + file + ", and qory holds no record of whether it did when a walled run began here; qory runs no git there, since a run could have written it to change which objects or history git reads. Remove it to go on"
		var r *hostgit.RefusedError
		if !errors.As(err, &r) || r.Reason != hostgit.RefusedObjectsFileUnrecorded || err.Error() != want {
			t.Errorf("%s: got %v, want %q", what, err, want)
		}
	}
	unrecorded("a record without the files", referenced, alternates, checkOnly(referenced))
	unrecorded("a record without the files, the changes", referenced, alternates, changes(referenced))

	// A repository in a recorded place the records hold no entry for.
	mount, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := places.Update(mount, places.KindMount, func(*places.Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(mount, "lib")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, nested, "init", "-q", "--initial-branch=main")
	grafts := filepath.Join(nested, ".git", "info", "grafts")
	write(grafts, "\n")
	unrecorded("no entry for the repository", nested, grafts, checkOnly(nested))
}

// TestAURLInConfigWorktreeCountsOnlyWhileGitReadsIt records a repository whose remote
// has its url in config.worktree, which extensions.worktreeConfig turns on, and its fetch
// in the common config. Once the run takes that line out, or sets it false, git reads
// config.worktree no more and takes the remote's name for its URL: the check refuses
// the fetch as a key of a remote with no URL. Only the configuration is read.
func TestAURLInConfigWorktreeCountsOnlyWhileGitReadsIt(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	git(t, dir, "config", "extensions.worktreeConfig", "true")
	git(t, dir, "config", "--worktree", "remote.origin.url", "https://git.example.com/acme/shop.git")
	git(t, dir, "config", "--unset", "remote.origin.url")
	record(t, dir)
	if err := run(t, dir); err != nil {
		t.Fatalf("as recorded: %v", err)
	}
	want := "the git configuration of " + dir + " sets remote.origin.fetch for a remote it gives no URL; qory runs no git there, since git takes the remote's name for its URL then, which a run could have chosen to reach another server. Remove it to go on"
	for _, value := range []string{"", "false", "0", "off"} {
		what := "extensions.worktreeConfig " + value
		if value == "" {
			what = "no extensions.worktreeConfig"
			git(t, dir, "config", "--unset", "extensions.worktreeConfig")
		} else {
			git(t, dir, "config", "extensions.worktreeConfig", value)
		}
		var r *hostgit.RefusedError
		if err := run(t, dir); !errors.As(err, &r) || r.Reason != hostgit.RefusedRemoteNoURL || err.Error() != want {
			t.Errorf("%s: got %v, want %q", what, err, want)
		}
	}
}

// TestALinkAmongTheObjectsIsRefused records a repository and runs the check once, then
// makes a link of objects/pack, of a loose objects' folder, of objects/info, of a pack
// or a pack's index in objects/pack, or of a loose object, to another repository's: the
// check, which the first run cached, and the changes accept-checkout reads, refuse it
// before any git runs. A link there when the place was recorded, leading where it led,
// passes.
func TestALinkAmongTheObjectsIsRefused(t *testing.T) {
	hermetic(t)
	ctx := context.Background()
	other := newRepo(t)
	git(t, other, "gc", "-q")
	packFile := func(dir, ext string) string {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(dir, ".git", "objects", "pack"))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ext) {
				return e.Name()
			}
		}
		t.Fatalf("no %s in the packs of %s", ext, dir)
		return ""
	}
	loose := func(dir string) string {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(dir, ".git", "objects"))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if len(e.Name()) == 2 {
				return e.Name()
			}
		}
		t.Fatalf("no loose objects' folder in %s", dir)
		return ""
	}
	for _, c := range []struct {
		what string
		rel  func(dir string) string
	}{
		{"objects/pack", func(string) string { return "objects/pack" }},
		{"a loose objects' folder", func(dir string) string { return "objects/" + loose(dir) }},
		{"objects/info", func(string) string { return "objects/info" }},
		{"a pack in objects/pack", func(string) string { return "objects/pack/" + packFile(other, ".pack") }},
		{"a pack's index in objects/pack", func(string) string { return "objects/pack/" + packFile(other, ".idx") }},
		{"a loose object", func(dir string) string {
			folder := loose(dir)
			entries, err := os.ReadDir(filepath.Join(dir, ".git", "objects", folder))
			if err != nil || len(entries) == 0 {
				t.Fatalf("no loose object in %s: %v", folder, err)
			}
			return "objects/" + folder + "/" + entries[0].Name()
		}},
	} {
		dir := newRepo(t)
		record(t, dir)
		if _, err := hostgit.Command(ctx, dir, "status"); err != nil {
			t.Fatalf("%s: as recorded: %v", c.what, err)
		}
		rel := c.rel(dir)
		link := filepath.Join(dir, ".git", filepath.FromSlash(rel))
		target := filepath.Join(other, ".git", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(link, filepath.Join(t.TempDir(), "moved")); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		if _, err := os.Lstat(target); err != nil {
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		want := "the git directory of " + dir + " holds " + link + ", a link to " + target + "; qory runs no git there, since a run could have pointed it at another repository's objects. Remove the link to go on"
		var r *hostgit.RefusedError
		if _, err := hostgit.Command(ctx, dir, "status"); !errors.As(err, &r) || r.Reason != hostgit.RefusedObjectsLink || err.Error() != want {
			t.Errorf("%s: the check: got %v, want %q", c.what, err, want)
		}
		recs, err := places.Load()
		if err != nil {
			t.Fatal(err)
		}
		repo, ok := hostgit.Resolve(ctx, dir)
		if !ok {
			t.Fatalf("%s: no repository", c.what)
		}
		if _, err := hostgit.Changes(ctx, recs, repo); !errors.As(err, &r) || r.Reason != hostgit.RefusedObjectsLink || err.Error() != want {
			t.Errorf("%s: the changes: got %v, want %q", c.what, err, want)
		}
	}

	linked := newRepo(t)
	packs := filepath.Join(linked, ".git", "objects", "pack")
	moved := filepath.Join(t.TempDir(), "pack")
	if err := os.Rename(packs, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, packs); err != nil {
		t.Fatal(err)
	}
	record(t, linked)
	if _, err := hostgit.Command(ctx, linked, "status"); err != nil {
		t.Errorf("a link there when the place was recorded: %v", err)
	}
}

// TestAnObjectsOrRefsFolderQoryCannotListIsRefused checks a recorded repository, then
// sets each folder in turn to mode 0311, as a run could: objects/pack, a loose objects'
// folder, refs/heads and worktrees. git opens a file in such a folder by name, and qory
// cannot list it for links, so the check returns the refusal, after it passed with the
// folder listable. A test run as root lists any folder, so it skips.
func TestAnObjectsOrRefsFolderQoryCannotListIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lists any folder")
	}
	hermetic(t)
	ctx := context.Background()
	dir := newRepo(t)
	gitDir := filepath.Join(dir, ".git")
	for _, f := range []string{"objects/ab", "worktrees"} {
		if err := os.MkdirAll(filepath.Join(gitDir, filepath.FromSlash(f)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	record(t, dir)
	for _, f := range []string{"objects/pack", "objects/ab", "refs/heads", "worktrees"} {
		folder := filepath.Join(gitDir, filepath.FromSlash(f))
		if _, err := hostgit.Command(ctx, dir, "status"); err != nil {
			t.Fatalf("%s listable: %v", f, err)
		}
		if err := os.Chmod(folder, 0o311); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(folder, 0o755) })
		want := "the git directory of " + dir + " holds " + folder + ", which qory cannot list; qory runs no git there, since a run could have hidden in it a link to another repository's objects or history. Remove it to go on"
		var r *hostgit.RefusedError
		if _, err := hostgit.Command(ctx, dir, "status"); !errors.As(err, &r) || r.Reason != hostgit.RefusedUnlistedFolder || err.Error() != want {
			t.Errorf("%s: got %v, want %q", f, err, want)
		}
		if err := os.Chmod(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestARefLinkOutOfThePlaceIsRefused records a repository and runs the check once,
// then makes refs/heads/feature a link to another repository's ref file: the check, and
// the changes accept-checkout reads, refuse it before any git runs. A link that leads to
// a ref in the same place passes, and so does one there when the place was recorded,
// leading where it led.
func TestARefLinkOutOfThePlaceIsRefused(t *testing.T) {
	hermetic(t)
	ctx := context.Background()
	dir := newRepo(t)
	other := newRepo(t)
	git(t, other, "branch", "feature")
	record(t, dir)
	if _, err := hostgit.Command(ctx, dir, "status"); err != nil {
		t.Fatalf("as recorded: %v", err)
	}
	link := filepath.Join(dir, ".git", "refs", "heads", "feature")
	target := filepath.Join(other, ".git", "refs", "heads", "feature")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	want := "the git directory of " + dir + " holds " + link + ", a link to " + target + "; qory runs no git there, since a run could have pointed it at another repository's history. Remove the link to go on"
	var r *hostgit.RefusedError
	if _, err := hostgit.Command(ctx, dir, "status"); !errors.As(err, &r) || r.Reason != hostgit.RefusedRefLink || err.Error() != want {
		t.Errorf("the check: got %v, want %q", err, want)
	}
	recs, err := places.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := hostgit.Resolve(ctx, dir)
	if !ok {
		t.Fatal("no repository")
	}
	if _, err := hostgit.Changes(ctx, recs, repo); !errors.As(err, &r) || r.Reason != hostgit.RefusedRefLink || err.Error() != want {
		t.Errorf("the changes: got %v, want %q", err, want)
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("main", link); err != nil {
		t.Fatal(err)
	}
	if _, err := hostgit.Command(ctx, dir, "status"); err != nil {
		t.Errorf("a link to a ref in the place: %v", err)
	}

	linked := newRepo(t)
	if err := os.Symlink(target, filepath.Join(linked, ".git", "refs", "heads", "feature")); err != nil {
		t.Fatal(err)
	}
	record(t, linked)
	if _, err := hostgit.Command(ctx, linked, "status"); err != nil {
		t.Errorf("a link there when the place was recorded: %v", err)
	}
}

// TestAHeadOrAWorktreesRefLinkOutOfThePlaceIsRefused records a repository with a linked
// worktree and runs the check once, then makes a link to another repository's file of
// HEAD, of the worktree's HEAD in its git directory, or of a ref among the worktree's
// refs: the check refuses each before any git runs, a worktree's with the ref-link
// line. git takes a HEAD that is a link only as one to a path under refs, so with HEAD
// leading out git finds no repository, and the check refuses it as a configuration it
// cannot read.
func TestAHeadOrAWorktreesRefLinkOutOfThePlaceIsRefused(t *testing.T) {
	for _, rel := range []string{"HEAD", "worktrees/wt/HEAD", "worktrees/wt/refs/bisect/bad"} {
		t.Run(rel, func(t *testing.T) {
			hermetic(t)
			ctx := context.Background()
			dir := newRepo(t)
			git(t, dir, "worktree", "add", "-q", "-b", "wt", filepath.Join(t.TempDir(), "wt"))
			other := newRepo(t)
			record(t, dir)
			if _, err := hostgit.Command(ctx, dir, "status"); err != nil {
				t.Fatalf("as recorded: %v", err)
			}
			link := filepath.Join(dir, ".git", filepath.FromSlash(rel))
			target := filepath.Join(other, ".git", "HEAD")
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			reason := hostgit.RefusedRefLink
			want := "the git directory of " + dir + " holds " + link + ", a link to " + target + "; qory runs no git there, since a run could have pointed it at another repository's history. Remove the link to go on"
			if rel == "HEAD" {
				reason = hostgit.RefusedUnreadable
				want = "the git configuration of " + dir + " cannot be read; qory runs no git there, since a run could have set it to make git on this machine run a program"
			}
			var r *hostgit.RefusedError
			if _, err := hostgit.Command(ctx, dir, "status"); !errors.As(err, &r) || r.Reason != reason || err.Error() != want {
				t.Errorf("got %v, want %q", err, want)
			}
		})
	}
}

// TestAnObjectsFolderLinkIsRefusedUnlessTheRecordHoldsIt records a repository, then
// replaces its objects folder with a link to another repository's: the check, and the
// changes accept-checkout reads, refuse it, before any git runs. A link there when the
// place was recorded, leading where it led, passes.
func TestAnObjectsFolderLinkIsRefusedUnlessTheRecordHoldsIt(t *testing.T) {
	hermetic(t)
	ctx := context.Background()
	dir := newRepo(t)
	other := newRepo(t)
	record(t, dir)
	objects := filepath.Join(dir, ".git", "objects")
	if err := os.Rename(objects, filepath.Join(t.TempDir(), "objects")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(other, ".git", "objects")
	if err := os.Symlink(target, objects); err != nil {
		t.Fatal(err)
	}
	want := "the git directory of " + dir + " holds " + objects + ", a link to " + target + "; qory runs no git there, since a run could have pointed it at another repository's objects. Remove the link to go on"
	var r *hostgit.RefusedError
	if _, err := hostgit.Command(ctx, dir, "status"); !errors.As(err, &r) || r.Reason != hostgit.RefusedObjectsLink || err.Error() != want {
		t.Errorf("the check: got %v, want %q", err, want)
	}
	recs, err := places.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := hostgit.Resolve(ctx, dir)
	if !ok {
		t.Fatal("no repository")
	}
	if _, err := hostgit.Changes(ctx, recs, repo); !errors.As(err, &r) || r.Reason != hostgit.RefusedObjectsLink || err.Error() != want {
		t.Errorf("the changes: got %v, want %q", err, want)
	}

	linked := newRepo(t)
	linkedObjects := filepath.Join(linked, ".git", "objects")
	moved := filepath.Join(t.TempDir(), "objects")
	if err := os.Rename(linkedObjects, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, linkedObjects); err != nil {
		t.Fatal(err)
	}
	record(t, linked)
	if _, err := hostgit.Command(ctx, linked, "status"); err != nil {
		t.Errorf("a link there when the place was recorded: %v", err)
	}
}

// upperLoose is a name of a loose objects' folder in upper case, such as D9, whose
// lower-case form is not in the objects of the repository at dir: on a file system that
// ignores case, git reads objects/d9/<rest> through it.
func upperLoose(t *testing.T, dir string) string {
	t.Helper()
	const hex = "0123456789ABCDEF"
	for _, a := range hex[10:] {
		for _, b := range hex {
			name := string(a) + string(b)
			if _, err := os.Lstat(filepath.Join(dir, ".git", "objects", strings.ToLower(name))); errors.Is(err, fs.ErrNotExist) {
				return name
			}
		}
	}
	t.Fatalf("every loose objects' folder is in %s", dir)
	return ""
}

// TestALinkInAnObjectsFolderOfAnyNameIsRefused records a repository, then makes a
// folder of objects named in upper case, as a run could, with a link in it to another
// repository's object: the check, and the changes accept-checkout reads, refuse the
// link, as they refuse one in a folder of two lower-case hex digits.
func TestALinkInAnObjectsFolderOfAnyNameIsRefused(t *testing.T) {
	hermetic(t)
	ctx := context.Background()
	dir := newRepo(t)
	other := newRepo(t)
	record(t, dir)
	if _, err := hostgit.Command(ctx, dir, "status"); err != nil {
		t.Fatalf("as recorded: %v", err)
	}
	folder := filepath.Join(dir, ".git", "objects", upperLoose(t, dir))
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(other, ".git", "HEAD")
	link := filepath.Join(folder, "0123456789abcdef0123456789abcdef012345")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	want := "the git directory of " + dir + " holds " + link + ", a link to " + target + "; qory runs no git there, since a run could have pointed it at another repository's objects. Remove the link to go on"
	var r *hostgit.RefusedError
	if _, err := hostgit.Command(ctx, dir, "status"); !errors.As(err, &r) || r.Reason != hostgit.RefusedObjectsLink || err.Error() != want {
		t.Errorf("the check: got %v, want %q", err, want)
	}
	recs, err := places.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := hostgit.Resolve(ctx, dir)
	if !ok {
		t.Fatal("no repository")
	}
	if _, err := hostgit.Changes(ctx, recs, repo); !errors.As(err, &r) || r.Reason != hostgit.RefusedObjectsLink || err.Error() != want {
		t.Errorf("the changes: got %v, want %q", err, want)
	}
}

// TestAnObjectsFolderOfAnyNameQoryCannotListIsRefused records a repository, then makes
// a folder of objects named in upper case that qory cannot list, mode 0311: the check
// refuses it as a folder qory cannot list. A test run as root lists any folder, so it
// skips.
func TestAnObjectsFolderOfAnyNameQoryCannotListIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lists any folder")
	}
	hermetic(t)
	ctx := context.Background()
	dir := newRepo(t)
	record(t, dir)
	if _, err := hostgit.Command(ctx, dir, "status"); err != nil {
		t.Fatalf("as recorded: %v", err)
	}
	folder := filepath.Join(dir, ".git", "objects", upperLoose(t, dir))
	if err := os.Mkdir(folder, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(folder, 0o755) })
	want := "the git directory of " + dir + " holds " + folder + ", which qory cannot list; qory runs no git there, since a run could have hidden in it a link to another repository's objects or history. Remove it to go on"
	var r *hostgit.RefusedError
	if _, err := hostgit.Command(ctx, dir, "status"); !errors.As(err, &r) || r.Reason != hostgit.RefusedUnlistedFolder || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}

// gitLogged puts first on the PATH a git that writes its arguments, one line per
// command, to the file it returns, and runs the git found after it.
func gitLogged(t *testing.T) string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "git.log")
	wrapper := filepath.Join(dir, "bin", "git")
	if err := os.MkdirAll(filepath.Dir(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\necho \"$*\" >> '"+log+"'\nexec '"+real+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(wrapper)+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return log
}

// logged returns the lines of the log of [gitLogged], and empties it.
func logged(t *testing.T, log string) string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Remove(log); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

// TestAGitDirectoryInAnotherPlaceIsHeldBeforeItIsRead records a checkout and its
// linked worktree as two places, the worktree's git directory and common directory in
// the checkout's. A walled run starts in the checkout, and writes its config: the check
// in the worktree refuses it as in use, and runs no git, neither git rev-parse to find
// the repository nor git config to read the settings, on the first check of the
// process and on one whose earlier check it kept.
func TestAGitDirectoryInAnotherPlaceIsHeldBeforeItIsRead(t *testing.T) {
	for _, kept := range []bool{false, true} {
		name := map[bool]string{false: "first check", true: "kept check"}[kept]
		t.Run(name, func(t *testing.T) {
			hermetic(t)
			ctx := context.Background()
			main := newRepo(t)
			worktree := filepath.Join(t.TempDir(), "feature")
			git(t, main, "worktree", "add", "-q", "-b", "feature", worktree)
			worktree, err := filepath.EvalSymlinks(worktree)
			if err != nil {
				t.Fatal(err)
			}
			record(t, main)
			record(t, worktree)
			log := gitLogged(t)
			// The first check makes the lock files of the places, and the second reads the
			// records again since; the third finds no file changed and runs no git.
			for i := 0; i < 3; i++ {
				logged(t, log)
				c, err := hostgit.Command(ctx, worktree, "status")
				if err != nil {
					t.Fatalf("check %d with no run: %v", i+1, err)
				}
				c.Release()
			}
			if got := logged(t, log); kept && got != "" {
				t.Fatalf("the kept check ran git:\n%s", got)
			}
			if !kept {
				hostgit.Forget()
			}
			live, err := places.Starting([]string{main})
			if err != nil {
				t.Fatal(err)
			}
			defer live.Release()
			config := filepath.Join(main, ".git", "config")
			f, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString("[alias]\n\tgo = status\n"); err != nil {
				t.Fatal(err)
			}
			f.Close()
			want := worktree + " is in use by a walled run; qory runs no git there until the run ends, since the run could change git's configuration between qory's check and git's read"
			var r *hostgit.RefusedError
			if _, err := hostgit.Command(ctx, worktree, "status"); !errors.As(err, &r) || r.Reason != hostgit.RefusedInUse || err.Error() != want {
				t.Errorf("got %v, want %q", err, want)
			}
			if got := logged(t, log); got != "" {
				t.Errorf("git ran before the check held the checkout:\n%s", got)
			}
		})
	}
}

// TestAGitFolderGitLooksPastIsNoWorkTree makes a checkout whose .git is a file naming a
// git directory inside it, with an empty .git folder in its folder sub, which git looks
// past to the checkout's .git. The check from sub passes, and keeps what it read. A run
// then points the checkout's .git at another repository's git directory: the check
// from sub refuses it as a .git that points somewhere else, since it holds and watches
// the .git git uses, not the empty folder.
func TestAGitFolderGitLooksPastIsNoWorkTree(t *testing.T) {
	hermetic(t)
	ctx := context.Background()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "w")
	git(t, base, "init", "-q", "--initial-branch=main", "--separate-git-dir="+filepath.Join(dir, "gd"), dir)
	git(t, dir, "config", "user.name", "Tester")
	git(t, dir, "config", "user.email", "tester@example.com")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := git(t, sub, "rev-parse", "--show-toplevel"); got != dir {
		t.Fatalf("git finds the work tree %s, want %s", got, dir)
	}
	other := newRepo(t)
	record(t, dir)
	for i := 0; i < 3; i++ {
		if err := run(t, sub); err != nil {
			t.Fatalf("check %d as recorded: %v", i+1, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := "the checkout's .git points somewhere else since a walled run began here; qory runs no git there, since the run could have pointed it. qory setup repo --accept-checkout accepts it"
	var r *hostgit.RefusedError
	if _, err := hostgit.Command(ctx, sub, "status"); !errors.As(err, &r) || r.Reason != hostgit.RefusedPointer || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}

// TestAGitFolderGitLooksPastKeepsNoMatch records a checkout with an empty sub/.git
// folder, which git looks past to the checkout's .git, and checks sub 3 times, so that
// the check keeps what it read. A run then makes sub/.git a repository whose config sets
// core.sshCommand: the check from sub reads it, and refuses the key.
func TestAGitFolderGitLooksPastKeepsNoMatch(t *testing.T) {
	hermetic(t)
	dir := newRepo(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	record(t, dir)
	for i := 0; i < 3; i++ {
		if err := run(t, sub); err != nil {
			t.Fatalf("check %d as recorded: %v", i+1, err)
		}
	}
	git(t, sub, "init", "-q")
	git(t, sub, "config", "core.sshCommand", "touch ran")
	if got := git(t, sub, "rev-parse", "--show-toplevel"); got != sub {
		t.Fatalf("git finds the work tree %s, want %s", got, sub)
	}
	refusedKey(t, run(t, sub), sub, "core.sshcommand")
}

// ceiling keeps git, in the test and in the check, from looking for a repository above
// the test's temporary directories.
func ceiling(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(t.TempDir()))
}

// TestAGitDirectoryGitStopsUsingKeepsNoMatch records a checkout with a repository of
// its own in sub, and checks sub 3 times, so that the check keeps what it read. A run
// then sets core.sshCommand in the checkout's config, and removes HEAD, objects or refs
// from sub/.git, so that git from sub looks past sub/.git to the checkout's .git: the
// check from sub reads the checkout's config, and refuses the key.
func TestAGitDirectoryGitStopsUsingKeepsNoMatch(t *testing.T) {
	for _, removed := range []string{"HEAD", "objects", "refs"} {
		t.Run(removed, func(t *testing.T) {
			hermetic(t)
			dir := newRepo(t)
			sub := filepath.Join(dir, "sub")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			git(t, sub, "init", "-q")
			record(t, dir)
			for i := 0; i < 3; i++ {
				if err := run(t, sub); err != nil {
					t.Fatalf("check %d as recorded: %v", i+1, err)
				}
			}
			git(t, dir, "config", "core.sshCommand", "touch ran")
			if err := os.Rename(filepath.Join(sub, ".git", removed), filepath.Join(t.TempDir(), removed)); err != nil {
				t.Fatal(err)
			}
			if got := git(t, sub, "rev-parse", "--show-toplevel"); got != dir {
				t.Fatalf("git finds the work tree %s, want %s", got, dir)
			}
			refusedKey(t, run(t, sub), sub, "core.sshcommand")
		})
	}
}

// TestAGitFolderGitLooksPastToABareRepositoryKeepsNoMatch records a bare repository
// with an empty .git folder in its folder info/x, which git looks past to the bare
// repository, and checks info/x 3 times, so that the check keeps what it read. A run
// then makes info/x/.git a repository whose config sets core.sshCommand: the check
// from info/x reads it, and refuses the key.
func TestAGitFolderGitLooksPastToABareRepositoryKeepsNoMatch(t *testing.T) {
	hermetic(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(base, "b.git")
	git(t, base, "init", "-q", "--bare", bare)
	x := filepath.Join(bare, "info", "x")
	if err := os.MkdirAll(filepath.Join(x, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := git(t, x, "rev-parse", "--absolute-git-dir"); got != bare {
		t.Fatalf("git finds the git directory %s, want %s", got, bare)
	}
	record(t, bare)
	// git status needs a work tree, which a bare repository has none of: the check runs
	// before git rev-parse.
	check := func() error {
		c, err := hostgit.Command(context.Background(), x, "rev-parse", "--absolute-git-dir")
		if err != nil {
			return err
		}
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git rev-parse: %v\n%s", err, out)
		}
		return nil
	}
	for i := 0; i < 3; i++ {
		if err := check(); err != nil {
			t.Fatalf("check %d as recorded: %v", i+1, err)
		}
	}
	git(t, x, "init", "-q")
	git(t, x, "config", "core.sshCommand", "touch ran")
	refusedKey(t, check(), x, "core.sshcommand")
}
