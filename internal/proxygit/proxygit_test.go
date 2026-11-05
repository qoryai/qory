package proxygit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/proxygit"
)

// TestEnvDropsTheTransportsOfTheEnvironment is an environment with GIT_ALLOW_PROTOCOL
// set to file, which wins over protocol.allow: Env drops it, with and without a proxy,
// and a git with [proxygit.HTTPSOnly] and that environment does not clone a repository
// over file://.
func TestEnvDropsTheTransportsOfTheEnvironment(t *testing.T) {
	env := []string{"PATH=" + os.Getenv("PATH"), "GIT_ALLOW_PROTOCOL=file", "GIT_PROTOCOL_FROM_USER=1"}
	for _, p := range []*proxygit.Proxy{nil, {URL: "http://qory:password@127.0.0.1:9"}} {
		got := proxygit.Env(env, p)
		if slices.ContainsFunc(got, func(kv string) bool {
			return strings.HasPrefix(kv, "GIT_ALLOW_PROTOCOL=") || strings.HasPrefix(kv, "GIT_PROTOCOL_FROM_USER=")
		}) {
			t.Errorf("proxy %v: %v keeps the transports of the environment", p != nil, got)
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	src := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", "-b", "main"}, {"-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "--quiet", "--allow-empty", "-m", "first"}} {
		c := exec.Command("git", args...)
		c.Dir = src
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	dest := filepath.Join(t.TempDir(), "clone")
	c := exec.Command("git", append(slices.Clone(proxygit.HTTPSOnly), "clone", "--quiet", "file://"+src, dest)...)
	c.Env = proxygit.Env(append(os.Environ(), "GIT_ALLOW_PROTOCOL=file"), nil)
	if out, err := c.CombinedOutput(); err == nil {
		t.Errorf("git cloned over file:// with GIT_ALLOW_PROTOCOL=file:\n%s", out)
	}
	// The same git with GIT_ALLOW_PROTOCOL kept clones: the variable is one to drop.
	c = exec.Command("git", append(slices.Clone(proxygit.HTTPSOnly), "clone", "--quiet", "file://"+src, dest)...)
	c.Env = append(proxygit.Env(os.Environ(), nil), "GIT_ALLOW_PROTOCOL=file")
	if out, err := c.CombinedOutput(); err != nil {
		t.Errorf("with GIT_ALLOW_PROTOCOL kept, git did not clone, so the test shows nothing: %v\n%s", err, out)
	}
}

// TestEnvDropsTheFormatsAndTheCeiling is an environment that chooses the formats git init
// writes and ends git's search for a repository early: Env drops each, with and without a
// proxy. A git init with GIT_DEFAULT_HASH=sha256 in that environment writes the format
// of a git init with none, and a git with the ceiling at the repository still finds it.
func TestEnvDropsTheFormatsAndTheCeiling(t *testing.T) {
	names := []string{"GIT_DEFAULT_HASH", "GIT_DEFAULT_REF_FORMAT", "GIT_CEILING_DIRECTORIES"}
	env := []string{"PATH=" + os.Getenv("PATH")}
	for _, name := range names {
		env = append(env, name+"=x")
	}
	for _, p := range []*proxygit.Proxy{nil, {URL: "http://qory:password@127.0.0.1:9"}} {
		for _, kv := range proxygit.Env(env, p) {
			if name, _, _ := strings.Cut(kv, "="); slices.Contains(names, name) {
				t.Errorf("proxy %v: Env keeps %s", p != nil, kv)
			}
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	git := func(dir string, env []string, args ...string) (string, error) {
		c := exec.Command("git", args...)
		c.Dir, c.Env = dir, env
		out, err := c.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	format := func(env []string) string {
		dir := t.TempDir()
		if out, err := git(dir, env, "init", "--quiet"); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		out, err := git(dir, env, "rev-parse", "--show-object-format")
		if err != nil {
			t.Fatalf("git rev-parse: %v\n%s", err, out)
		}
		return out
	}
	plain := proxygit.Env(os.Environ(), nil)
	if got, want := format(proxygit.Env(append(os.Environ(), "GIT_DEFAULT_HASH=sha256"), nil)), format(plain); got != want {
		t.Errorf("the object format with GIT_DEFAULT_HASH=sha256 is %s, want %s", got, want)
	}
	if got := format(append(slices.Clone(plain), "GIT_DEFAULT_HASH=sha256")); got != "sha256" {
		t.Errorf("with GIT_DEFAULT_HASH kept, the format is %s, so the test shows nothing", got)
	}
	top := t.TempDir()
	if out, err := git(top, plain, "init", "--quiet"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	inner := filepath.Join(top, "inner")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	ceiling := "GIT_CEILING_DIRECTORIES=" + top
	if out, err := git(inner, proxygit.Env(append(os.Environ(), ceiling), nil), "rev-parse", "--git-dir"); err != nil {
		t.Errorf("git did not find the repository above the folder: %v\n%s", err, out)
	}
	if _, err := git(inner, append(slices.Clone(plain), ceiling), "rev-parse", "--git-dir"); err == nil {
		t.Errorf("with GIT_CEILING_DIRECTORIES kept, git found the repository, so the test shows nothing")
	}
}

// TestEnvWithAProxyDropsNoProxy is an environment with NO_PROXY and no_proxy set to the
// loopback names, as the Env of Forager's forwarder sets them: with a proxy, Env drops
// both, since git reaches a host that NO_PROXY lists directly, past the run's proxy;
// with no proxy, Env keeps them.
func TestEnvWithAProxyDropsNoProxy(t *testing.T) {
	env := []string{"PATH=" + os.Getenv("PATH"), "NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1"}
	noProxy := func(kv string) bool {
		return strings.HasPrefix(kv, "NO_PROXY=") || strings.HasPrefix(kv, "no_proxy=")
	}
	if got := proxygit.Env(env, &proxygit.Proxy{URL: "http://qory:password@127.0.0.1:9"}); slices.ContainsFunc(got, noProxy) {
		t.Errorf("with a proxy, %v keeps NO_PROXY", got)
	}
	if got := proxygit.Env(env, nil); !slices.Contains(got, env[1]) || !slices.Contains(got, env[2]) {
		t.Errorf("with no proxy, %v drops NO_PROXY", got)
	}
}
