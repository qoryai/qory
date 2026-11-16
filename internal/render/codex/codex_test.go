package codex_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/render"
	_ "github.com/qoryai/qory/internal/render/codex"
	"github.com/qoryai/qory/internal/stack"
)

// roleFiles composes a module whose agents are the given files, by name and whole text,
// builds it for codex, and returns the text of each agents/<name>.toml.
func roleFiles(t *testing.T, agents map[string]string) map[string]string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(tmp, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	src := filepath.Join(t.TempDir(), "src")
	mod := filepath.Join(src, "modules", "example")
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "qory.yaml"), "apiVersion: qory.dev/v1alpha1\nharness:\n  target:\n    runtime: codex\n  modules:\n    - name: example\n      source: {path: modules/example}\n")
	write(filepath.Join(mod, "qory-module.yaml"), "apiVersion: qory.dev/v1alpha1\nname: example\n")
	for name, text := range agents {
		write(filepath.Join(mod, "agents", name+".md"), text)
	}
	p, err := config.Compose(filepath.Join(src, "qory.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p.Target.Runtimes = stack.Runtimes{"codex"}
	res, err := compose.Compose(p)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	home := filepath.Join(root, ".qory", "harness")
	rt, err := render.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := render.Build(res, home, rt); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for name := range agents {
		data, err := os.ReadFile(filepath.Join(home, "codex", "agents", name+".toml"))
		if err != nil {
			t.Fatal(err)
		}
		got[name] = string(data)
	}
	return got
}

func agent(tools string) string {
	front := "---\nname: role\ndescription: A role.\n" + tools + "---\n\nDo the work.\n"
	return front
}

// TestARoleWithoutAShellToolGetsNoShell composes agents that list tools without a shell,
// with a shell, and with no tools key. Only the first kind gets [features] shell_tool =
// false; a Bash rule such as Bash(git:*) and PowerShell count as a shell.
func TestARoleWithoutAShellToolGetsNoShell(t *testing.T) {
	cases := []struct {
		name  string
		tools string
		off   bool
	}{
		{"no-shell-list", "tools: [Read, Grep]\n", true},
		{"no-shell-string", "tools: Read, Grep\n", true},
		{"empty-list", "tools: []\n", true},
		{"bash-list", "tools: [Read, Bash]\n", false},
		{"bash-string", "tools: Read, Bash\n", false},
		{"bash-rule", "tools: [Read, \"Bash(git:*)\"]\n", false},
		{"powershell", "tools: [PowerShell]\n", false},
		{"no-tools-key", "", false},
		{"null-tools", "tools: ~\n", false},
		{"empty-tools-value", "tools:\n", false},
	}
	agents := map[string]string{}
	for _, c := range cases {
		agents[c.name] = agent(c.tools)
	}
	got := roleFiles(t, agents)
	for _, c := range cases {
		text := got[c.name]
		has := strings.Contains(text, "[features]\n  shell_tool = false\n")
		if has != c.off {
			t.Errorf("%s: shell_tool = false present is %v, want %v:\n%s", c.name, has, c.off, text)
		}
		if !strings.Contains(text, "developer_instructions") {
			t.Errorf("%s: no developer_instructions:\n%s", c.name, text)
		}
	}
}
