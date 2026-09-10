package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/cmd"
)

// linkedProfile is the two-layers fixture's profile with the core layer linked as harness.
const linkedProfile = `apiVersion: qory.ai/v1alpha1
kind: HarnessProfile
target:
  runtime: claude
layers:
  - name: core
    source: {path: layers/core}
    link: harness
  - name: nextjs
    source: {path: layers/nextjs}
extensions:
  acme:
    required_check: Harness self-tests
`

// TestLayerLinkIsWrittenReportedAndRemoved is a profile linking the core layer as harness:
// the compose writes the link at the checkout root, excludes it, the report and inspect
// name it, a compose without it prunes it, and remove takes it with every exclude line.
func TestLayerLinkIsWrittenReportedAndRemoved(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	writeFile(t, filepath.Join(root, "harness-compose.yaml"), linkedProfile)
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "link", "harness  (layer core)")
	if target := linkTarget(t, root, "harness"); target != filepath.Join(".qory", "harness", "layers", "core") {
		t.Errorf("harness links to %s", target)
	}
	if _, err := os.Stat(filepath.Join(root, "harness", "scripts", "db.py")); err != nil {
		t.Errorf("the layer's script is not reachable through the link: %v", err)
	}
	exclude, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	wants(t, string(exclude), "/harness\n", "/.qory\n")
	out, err = run(t, "harness", "inspect")
	if err != nil {
		t.Fatal(err)
	}
	wants(t, out, "linked as harness", "Extensions", `acme.required_check  "Harness self-tests"`)
	writeFile(t, filepath.Join(root, "harness-compose.yaml"), strings.Replace(linkedProfile, "    link: harness\n", "", 1))
	if _, err := run(t, "harness", "compose"); err != nil {
		t.Fatal(err)
	}
	gone(t, root, "harness")
	exclude, _ = os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	lacks(t, string(exclude), "/harness\n")
	writeFile(t, filepath.Join(root, "harness-compose.yaml"), linkedProfile)
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

// TestLayerLinkYieldsToTheCheckoutsOwnPath is a repository with its own harness directory:
// the compose keeps it, says so, and exits 0; a link named like a runtime's path is refused
// before anything is written.
func TestLayerLinkYieldsToTheCheckoutsOwnPath(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	writeFile(t, filepath.Join(root, "harness-compose.yaml"), linkedProfile)
	writeFile(t, filepath.Join(root, "harness", "own.txt"), "mine\n")
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatal(err)
	}
	wantsRow(t, out, "kept", "harness  (the checkout's own; not linked to layer core)")
	if data, _ := os.ReadFile(filepath.Join(root, "harness", "own.txt")); string(data) != "mine\n" {
		t.Errorf("the checkout's own directory was touched: %q", data)
	}
	writeFile(t, filepath.Join(root, "harness-compose.yaml"), strings.Replace(linkedProfile, "link: harness", "link: AGENTS.md", 1))
	_, err = run(t, "harness", "compose")
	if err == nil || !strings.Contains(err.Error(), "link AGENTS.md is where the") || cmd.ExitCode(err) != cmd.ExitInput {
		t.Fatalf("err = %v, exit %d", err, cmd.ExitCode(err))
	}
}

// TestForceFromTheConfigurationAndTheFlag is qory.yaml setting force on a checkout that
// tracks .claude/settings.json: the compose replaces it, and --force=false on the command
// line refuses it again.
func TestForceFromTheConfigurationAndTheFlag(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	writeFile(t, filepath.Join(root, ".claude", "settings.json"), "{}\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "own settings")
	writeFile(t, filepath.Join(root, "qory.yaml"), "apiVersion: qory.ai/v1alpha1\nkind: QoryConfig\nforce: true\n")
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
