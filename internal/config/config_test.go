package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/config"
)

// hermetic points HOME and the configuration directory at temporary paths, so no test
// reads the machine's own qory.yaml.
func hermetic(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("apiVersion: qory.ai/v1alpha1\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadWithoutAFileIsTheDefaults is a checkout with no qory.yaml anywhere: every value
// is its default and says so.
func TestLoadWithoutAFileIsTheDefaults(t *testing.T) {
	hermetic(t)
	c, err := config.Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Runtime != nil || c.Model != "" || c.Force || c.Update || c.Git.Timeout != 10*time.Minute || c.Git.Cache != "" || len(c.Env) != 0 || len(c.Files) != 0 {
		t.Errorf("defaults: %+v", c)
	}
	for _, r := range c.Rows() {
		if r.Origin != config.Default {
			t.Errorf("%s comes from %s, want %s", r.Key, r.Origin, config.Default)
		}
	}
	rows := c.Rows()
	if rows[0].Value != "(stack)" || rows[3].Value != "never" || rows[4].Value != "10m0s" {
		t.Errorf("rows: %+v", rows)
	}
}

// TestNearerFilesWin is the user's file, an ancestor's and the checkout's, each setting
// the runtime: the checkout's wins, a key only the user's file sets keeps that value, and
// every file is listed in the order it applied.
func TestNearerFilesWin(t *testing.T) {
	home := hermetic(t)
	user := filepath.Join(home, ".config", "qory", "qory.yaml")
	write(t, user, "runtime: gemini\nforce: true\nenv: {A: user, B: user}\n")
	base := t.TempDir()
	root := filepath.Join(base, "code", "app")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(base, "code", "qory.yaml")
	write(t, ancestor, "runtime: codex\nupdate: always\nenv: {B: ancestor}\n")
	own := filepath.Join(root, "qory.yaml")
	write(t, own, "runtime: [claude, codex]\ngit: {timeout: 30s, cache: cache}\n")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Runtime.String() != "claude, codex" || !c.Force || !c.Update || c.Git.Timeout != 30*time.Second || c.Git.Cache != filepath.Join(root, "cache") {
		t.Errorf("effective: %+v", c)
	}
	if c.Env["A"] != "user" || c.Env["B"] != "ancestor" {
		t.Errorf("env: %v", c.Env)
	}
	if strings.Join(c.Files, " ") != user+" "+ancestor+" "+own {
		t.Errorf("files: %v", c.Files)
	}
	origins := map[string]string{}
	for _, r := range c.Rows() {
		origins[r.Key] = r.Origin
	}
	if origins["runtime"] != own || origins["force"] != user || origins["update"] != ancestor || origins["env.B"] != ancestor || origins["model"] != config.Default {
		t.Errorf("origins: %v", origins)
	}
}

// TestLoadRefusesAMistake is every value a file cannot carry: an unknown key, a wrong
// kind, an update that is neither always nor never, a timeout that is not a duration, an
// empty cache, a variable that is not a name, and qory's own variable.
func TestLoadRefusesAMistake(t *testing.T) {
	hermetic(t)
	for _, c := range []struct{ body, want string }{
		{"runtim: claude\n", `line 2: key "runtim" is not one qory.yaml reads`},
		{"update: sometimes\n", `update "sometimes" is not always or never`},
		{"git: {timeout: soon}\n", `git.timeout "soon" is not a duration above zero, such as 10m`},
		{"git: {timeout: 0s}\n", `git.timeout "0s" is not a duration above zero`},
		{"git: {cache: \"\"}\n", "git.cache is empty"},
		{"env: {1A: x}\n", "env: 1A is not an environment variable name"},
		{"env: {QORY_HARNESS_HOME: x}\n", "env.QORY_HARNESS_HOME is qory's own"},
		{"runtime: []\n", "runtime is empty"},
	} {
		root := t.TempDir()
		write(t, filepath.Join(root, "qory.yaml"), c.body)
		_, err := config.Load(root, true)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), root) {
			t.Errorf("%q: error %v, want one naming the file and %q", c.body, err, c.want)
		}
	}
}

// TestAnEmptyFileSetsNothing is a qory.yaml holding only its apiVersion and kind: it is
// read, listed, and changes no value.
func TestAnEmptyFileSetsNothing(t *testing.T) {
	hermetic(t)
	root := t.TempDir()
	write(t, filepath.Join(root, "qory.yaml"), "")
	c, err := config.Load(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Files) != 1 || c.Git.Timeout != config.DefaultTimeout {
		t.Errorf("got %+v", c)
	}
}
