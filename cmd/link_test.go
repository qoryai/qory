package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// linkedStack is the two-modules fixture's stack with the core module linked as harness.
const linkedStack = `apiVersion: qory.ai/v1alpha1
target:
  runtime: claude
modules:
  - name: core
    source: {path: modules/core}
    link: harness
  - name: nextjs
    source: {path: modules/nextjs}
extensions:
  acme:
    required_check: Harness self-tests
`

// TestModuleLinkIsWrittenReportedAndRemoved is a stack linking the core module as harness:
// the compose writes the link at the checkout root, excludes it, the report and inspect
// name it, a compose without it prunes it, and remove takes it with every exclude line.
func TestModuleLinkIsWrittenReportedAndRemoved(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, "qory-stack.yaml"), linkedStack)
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "link", "harness  (module core)")
	if target := linkTarget(t, root, "harness"); target != filepath.Join(".qory", "harness", "modules", "core") {
		t.Errorf("harness links to %s", target)
	}
	if _, err := os.Stat(filepath.Join(root, "harness", "scripts", "db.py")); err != nil {
		t.Errorf("the module's script is not reachable through the link: %v", err)
	}
	exclude, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	wants(t, string(exclude), "/harness\n", "/.qory\n")
	out, err = run(t, "harness", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "linked as harness", "Extensions", `acme.required_check  "Harness self-tests"`)
	writeFile(t, filepath.Join(root, "qory-stack.yaml"), strings.Replace(linkedStack, "    link: harness\n", "", 1))
	if _, err := run(t, "harness", "compose"); err != nil {
		t.Fatal(err)
	}
	gone(t, root, "harness")
	exclude, _ = os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	lacks(t, string(exclude), "/harness\n")
	writeFile(t, filepath.Join(root, "qory-stack.yaml"), linkedStack)
	if _, err := run(t, "harness", "compose"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, "harness", "remove")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "removed harness")
	gone(t, root, "harness", ".claude", ".qory")
	exclude, _ = os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	for _, line := range strings.Split(string(exclude), "\n") {
		if strings.HasPrefix(line, "/") {
			t.Errorf("exclude line %q outlives the harness", line)
		}
	}
}

// TestModuleLinkRefusesTheCheckoutsOwnPath is a repository with its own harness directory,
// and one with an untracked symlink there: the compose refuses both with exit 4, since the
// permission rules and scripts of the harness depend on that path, and --force replaces
// the directory once it is committed. A link named like a runtime's path is refused
// before anything is written.
func TestModuleLinkRefusesTheCheckoutsOwnPath(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, "qory-stack.yaml"), linkedStack)
	writeFile(t, filepath.Join(root, "harness", "own.txt"), "mine\n")
	_, err := run(t, "harness", "compose")
	if cmd.ExitCode(err) != cmd.ExitForeign || !strings.Contains(err.Error(), "harness is not a link qory wrote") {
		t.Fatalf("own directory: err = %v, exit %d", err, cmd.ExitCode(err))
	}
	if data, _ := os.ReadFile(filepath.Join(root, "harness", "own.txt")); string(data) != "mine\n" {
		t.Errorf("the checkout's own directory was touched: %q", data)
	}
	_, err = run(t, "harness", "compose", "--force")
	if cmd.ExitCode(err) != cmd.ExitForeign || !strings.Contains(err.Error(), "is not tracked in git") {
		t.Fatalf("untracked directory under --force: err = %v", err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "own harness")
	out, err := run(t, "harness", "compose", "--force")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "replaced", "harness  (the checkout's own; git checkout -- restores it)")
	if _, err := os.Stat(filepath.Join(root, "harness", "scripts", "db.py")); err != nil {
		t.Errorf("the link does not reach the module: %v", err)
	}
	if _, err := run(t, "harness", "remove"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("docs", filepath.Join(root, "harness")); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, "harness", "compose")
	if cmd.ExitCode(err) != cmd.ExitForeign || !strings.Contains(err.Error(), "harness links to docs") {
		t.Fatalf("foreign symlink: err = %v", err)
	}
	if err := os.Remove(filepath.Join(root, "harness")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "qory-stack.yaml"), strings.Replace(linkedStack, "link: harness", "link: AGENTS.md", 1))
	_, err = run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), "link AGENTS.md is where the") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("err = %v, exit %d", err, cmd.ExitCode(err))
	}
}

// TestRemoveTakesTheModuleLinkWithoutTheReport is a checkout whose .qory was deleted by
// hand: remove still takes the module link at the root and its exclude line.
func TestRemoveTakesTheModuleLinkWithoutTheReport(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, "qory-stack.yaml"), linkedStack)
	if _, err := run(t, "harness", "compose"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".qory")); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "harness", "remove")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "removed harness")
	gone(t, root, "harness", ".claude")
	exclude, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	lacks(t, string(exclude), "/harness\n", "/.qory\n")
}

// TestForceFromTheConfigurationAndTheFlag is qory.yaml setting force on a checkout that
// tracks .claude/settings.json: the compose replaces it, and --force=false on the command
// line refuses it again.
func TestForceFromTheConfigurationAndTheFlag(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, ".claude", "settings.json"), "{}\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "own settings")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.ai/v1alpha1\nharness: {force: true}\n")
	_, err := run(t, "harness", "compose", "--force=false")
	if cmd.ExitCode(err) != cmd.ExitForeign {
		t.Fatalf("with --force=false: err = %v, exit %d", err, cmd.ExitCode(err))
	}
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "replaced", ".claude/settings.json")
}
