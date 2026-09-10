package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/ui"
)

// codexLinks are the paths Codex CLI reads, and claudeLink is the one Claude Code reads.
var (
	codexLinks = []string{".codex", ".agents/skills", "AGENTS.override.md"}
	claudeLink = ".claude"
)

// retarget rewrites the runtime line of the profile in the checkout.
func retarget(t *testing.T, root, runtime string) {
	t.Helper()
	file := filepath.Join(root, "harness-compose.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Replace(string(data), "runtime: claude", "runtime: "+runtime, 1)
	if out == string(data) {
		t.Fatalf("the fixture profile has no runtime line to rewrite:\n%s", data)
	}
	if err := os.WriteFile(file, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestComposeForSeveralRuntimesFromTheProfile is a target that names two runtimes: one
// checkout, two agents, one composed tree.
func TestComposeForSeveralRuntimesFromTheProfile(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	retarget(t, root, "[claude, codex]")
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · claude, codex", "composed 7 entries from 2 layers")
	wantsRow(t, out, "claude", claudeLink)
	wantsRow(t, out, "codex", strings.Join(codexLinks, "  "))
	for _, name := range append([]string{claudeLink}, codexLinks...) {
		linkTarget(t, root, name)
	}
	for _, dir := range []string{"claude", "codex"} {
		if _, err := os.Stat(filepath.Join(root, ".qory", "harness", dir)); err != nil {
			t.Errorf("the home lacks %s: %v", dir, err)
		}
	}
}

// TestComposeForSeveralRuntimesFromTheFlag is the same target given on the command line.
func TestComposeForSeveralRuntimesFromTheFlag(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	out, err := run(t, "hc", "--runtime", "claude, codex")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · claude, codex")
	for _, name := range append([]string{claudeLink}, codexLinks...) {
		linkTarget(t, root, name)
	}
}

// TestComposeKeepsARuntimeComposedEarlier is the switch mid-branch: composing for Codex in a
// checkout that already runs Claude Code leaves both agents working.
func TestComposeKeepsARuntimeComposedEarlier(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "hc", "--runtime", "codex")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · codex", "(composed here earlier, refreshed)")
	wantsRow(t, out, "claude", claudeLink+"  (composed here earlier, refreshed)")
	for _, name := range append([]string{claudeLink}, codexLinks...) {
		linkTarget(t, root, name)
	}
	if out, err := run(t, "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		wants(t, out, ui.Mark+" acme/app · codex")
	}
}

// TestRemoveTakesEveryRuntimesLinks pins that one remove cleans a checkout composed for
// several runtimes, whichever one the profile last targeted.
func TestRemoveTakesEveryRuntimesLinks(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-layers", root)
	before := snapshot(t, root)
	if out, err := run(t, "hc", "--runtime", "claude,codex"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "hr")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed "+claudeLink, "removed .codex", "removed .qory")
	gone(t, root, append([]string{claudeLink, ".qory", ".agents"}, codexLinks...)...)
	if got := snapshot(t, root); len(got) != len(before) {
		t.Errorf("the checkout holds %d paths, %d before the compose", len(got), len(before))
	}
}

// TestComposeRefusesATargetItCannotRender covers the flag forms a person mistypes.
func TestComposeRefusesATargetItCannotRender(t *testing.T) {
	for _, c := range []struct{ name, runtime, want string }{
		{"an unknown runtime in a list", "claude,nope", `runtime "nope" is not one this qory renders`},
		{"the same runtime twice", "claude,claude", "target.runtime names claude twice"},
		{"an empty name", "claude,", "target.runtime names an empty runtime"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := newCheckout(t)
			copyFixture(t, "two-layers", root)
			out, err := run(t, "hc", "--runtime", c.runtime)
			if err == nil {
				t.Fatalf("the compose was accepted:\n%s", out)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to name %q", err, c.want)
			}
			if _, err := os.Stat(filepath.Join(root, ".qory")); err == nil {
				t.Error("a refused compose wrote .qory")
			}
		})
	}
}

// TestRemoveSaysNothingWasComposed keeps the verb honest in a checkout it never touched.
func TestRemoveSaysNothingWasComposed(t *testing.T) {
	newCheckout(t)
	out, err := run(t, "harness", "remove")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "nothing composed here")
	lacks(t, out, "removed")
}
