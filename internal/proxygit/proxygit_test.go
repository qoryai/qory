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
