package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// TestConfigPrintsEveryValueWithItsOrigin is a checkout with a qory.yaml setting the
// runtime and a user file setting force: each row names its value and the file that set
// it, and a value no file set says default.
func TestConfigPrintsEveryValueWithItsOrigin(t *testing.T) {
	root := newCheckout(t)
	user := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "qory", "qory.yaml")
	writeFile(t, user, "apiVersion: qory.dev/v1alpha1\nharness: {force: true}\n")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness: {runtime: codex}\nworktree: {link: [.env, .env.local]}\nenv: {HARNESS_PROFILE: nextjs}\n")
	out, err := run(t, "config")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 3 {
			rows[f[0]] = f[1:]
		} else if len(f) == 4 && f[0] == "worktree.link" {
			rows[f[0]] = []string{f[1] + " " + f[2], f[3]}
		}
	}
	for key, want := range map[string][]string{
		"harness.runtime":     {"codex", "qory.yaml"},
		"harness.force":       {"true", "~/.config/qory/qory.yaml"},
		"harness.update":      {"never", "default"},
		"worktree.dir":        {"..", "default"},
		"worktree.name":       {"wt-{branch}", "default"},
		"worktree.branch":     {"delete", "default"},
		"git.timeout":         {"10m0s", "default"},
		"env.HARNESS_PROFILE": {"nextjs", "qory.yaml"},
		"worktree.link":       {".env, .env.local", "qory.yaml"},
	} {
		if got := rows[key]; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("%s = %v, want %v:\n%s", key, got, want, out)
		}
	}
}

// TestConfigWithoutAFileSaysSo is a checkout with no qory.yaml anywhere: the values are
// the defaults and the output says no file was found.
func TestConfigWithoutAFileSaysSo(t *testing.T) {
	newCheckout(t)
	out, err := run(t, "config")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "No qory.yaml or harness.yaml was found", "default")
}

// TestConfigRefusesAMistake is a qory.yaml with a key qory does not read: the command
// names the file and exits as an input error.
func TestConfigRefusesAMistake(t *testing.T) {
	root := newCheckout(t)
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness: {runtim: codex}\n")
	_, err := run(t, "config")
	if err == nil || !strings.Contains(err.Error(), "qory.yaml") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("err = %v, exit %d", err, cmd.ExitCode(err))
	}
}

// TestComposeReadsTheConfiguration is a qory.yaml naming codex as the runtime and
// exporting a variable, on a stack that targets claude: the compose renders for codex,
// the variable is the launch's, a default, and config.toml holds no variable, and
// --runtime on the command line still wins, with the same for Claude Code's
// settings.json.
func TestComposeReadsTheConfiguration(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	configure(t, root, []string{"runtime: codex"}, []string{"env: {HARNESS_PROFILE: nextjs}"})
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "codex")
	lacks(t, out, "claude")
	data, err := os.ReadFile(filepath.Join(root, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(data), "shell_environment_policy", "HARNESS_PROFILE", "QORY_HARNESS_HOME")
	out, err = run(t, "harness", "launch")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "HARNESS_PROFILE=nextjs", "QORY_HARNESS_HOME=")
	out, err = run(t, "harness", "compose", "--runtime", "claude")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "claude")
	data, err = os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, string(data), `"env"`, "HARNESS_PROFILE", "QORY_HARNESS_HOME")
	out, err = run(t, "harness", "launch", "--runtime", "claude")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "HARNESS_PROFILE=nextjs", "A=core", "B=1")
}
