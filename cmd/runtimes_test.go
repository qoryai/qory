package cmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/ui"
)

// codexLinks are the paths Codex CLI reads, and claudeLink is the one Claude Code reads.
var (
	codexLinks = []string{".codex", ".agents/skills", "AGENTS.override.md"}
	claudeLink = ".claude"
)

// retarget rewrites the runtime line of the stack in the checkout.
func retarget(t *testing.T, root, runtime string) {
	t.Helper()
	file := filepath.Join(root, "qory-stack.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Replace(string(data), "runtime: claude", "runtime: "+runtime, 1)
	if out == string(data) {
		t.Fatalf("the fixture stack has no runtime line to rewrite:\n%s", data)
	}
	if err := os.WriteFile(file, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestComposeForSeveralRuntimesFromTheStack is a target that names two runtimes: one
// checkout, two agents, one composed tree.
func TestComposeForSeveralRuntimesFromTheStack(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	retarget(t, root, "[claude, codex]")
	out, err := run(t, "harness", "compose")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · claude, codex", "composed 8 entries from 2 modules")
	wantsRow(t, out, "claude", claudeLink+"  .mcp.json")
	wantsRow(t, out, "codex", strings.Join(codexLinks, "  "))
	linkedFor(t, root, "claude", "codex")
	for _, dir := range []string{"claude", "codex"} {
		if _, err := os.Stat(filepath.Join(root, ".qory", "harness", dir)); err != nil {
			t.Errorf("the home lacks %s: %v", dir, err)
		}
	}
}

// TestComposeForSeveralRuntimesFromTheFlag is the same target given on the command line.
func TestComposeForSeveralRuntimesFromTheFlag(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	out, err := run(t, "hc", "--runtime", "claude, codex")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · claude, codex")
	linkedFor(t, root, "claude", "codex")
}

// TestComposeKeepsARuntimeComposedEarlier is the switch mid-branch: composing for Codex in a
// checkout that already runs Claude Code leaves both agents working.
func TestComposeKeepsARuntimeComposedEarlier(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "hc"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "hc", "--runtime", "codex")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · codex", "(composed here earlier, refreshed)")
	wantsRow(t, out, "claude", claudeLink+"  .mcp.json  (composed here earlier, refreshed)")
	linkedFor(t, root, "claude", "codex")
	// The report names every runtime the home holds, the target first.
	if rep := readReport(t, root); !slices.Equal(rep.Target.Runtimes, []string{"codex", "claude"}) {
		t.Errorf("report runtimes = %q", rep.Target.Runtimes)
	}
	if out, err := run(t, "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		wants(t, out, ui.Mark+" acme/app · codex, claude")
	}
}

// linkedFor checks the links of the runtimes named: the directory links with one child
// each runtime always writes, and the file links.
func linkedFor(t *testing.T, root string, runtimes ...string) {
	t.Helper()
	for _, r := range runtimes {
		switch r {
		case "claude":
			dirLinks(t, root, claudeLink, "settings.json", "skills")
		case "codex":
			dirLinks(t, root, ".codex", "config.toml")
			dirLinks(t, root, filepath.Join(".agents", "skills"), "review")
			linkTarget(t, root, "AGENTS.override.md")
		default:
			t.Fatalf("linkedFor knows no runtime %s", r)
		}
	}
}

// TestRemoveOneRuntimeKeepsTheOther is the switch back: a checkout composed for both
// drops Codex and keeps Claude Code working, and dropping the last runtime takes the
// harness with it.
func TestRemoveOneRuntimeKeepsTheOther(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "hc", "--runtime", "claude,codex"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "hr", "--runtime", "codex")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed .codex", "removed .agents/skills", "removed AGENTS.override.md", "removed .qory/harness/codex")
	lacks(t, out, "removed .claude", "removed .qory\n")
	gone(t, root, append([]string{".agents", ".qory/harness/codex"}, codexLinks...)...)
	linkedFor(t, root, "claude")
	if rep := readReport(t, root); !slices.Equal(rep.Target.Runtimes, []string{"claude"}) {
		t.Errorf("report runtimes = %q after dropping codex", rep.Target.Runtimes)
	}
	if out, err := run(t, "hi"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		wants(t, out, ui.Mark+" acme/app · claude opus")
	}

	out, err = run(t, "hr", "--runtime", "claude")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed .claude", "removed .qory")
	gone(t, root, claudeLink, ".qory")

	out, err = run(t, "hr", "--runtime", "codex")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "nothing composed here for codex")
	if out, err := run(t, "hr", "--runtime", "nope"); err == nil || !strings.Contains(err.Error(), `runtime "nope" is not one this qory renders`) {
		t.Errorf("remove --runtime nope: %v\n%s", err, out)
	}
}

// TestRemoveTakesEveryRuntimesLinks pins that one remove cleans a checkout composed for
// several runtimes, whichever one the stack last targeted.
func TestRemoveTakesEveryRuntimesLinks(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	before := snapshot(t, root)
	if out, err := run(t, "hc", "--runtime", "claude,codex"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "hr")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed "+claudeLink, "removed .codex", "removed .mcp.json", "removed .qory")
	gone(t, root, append([]string{claudeLink, ".mcp.json", ".qory", ".agents"}, codexLinks...)...)
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
			copyFixture(t, "two-modules", root)
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

// TestRemoveOneRuntimeLeavesWhatTheOtherShares is codex and opencode, which both read
// .agents/skills and AGENTS.md: dropping one leaves those for the other.
func TestRemoveOneRuntimeLeavesWhatTheOtherShares(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	if out, err := run(t, "hc", "--runtime", "codex,opencode"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "hr", "--runtime", "codex")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed .codex", "removed AGENTS.override.md")
	lacks(t, out, "removed .agents/skills", "removed AGENTS.md")
	dirLinks(t, root, filepath.Join(".agents", "skills"), "review")
	linkTarget(t, root, "AGENTS.md")
	dirLinks(t, root, ".opencode", "opencode.json")
}

// TestComposeAModuleWithoutSkills links every place a runtime reads, a kind directory
// nothing fills included, so a module of instructions alone composes for a runtime that
// reads skills from .agents/skills.
func TestComposeAModuleWithoutSkills(t *testing.T) {
	root := newCheckout(t)
	writeManifest(t, filepath.Join(root, "harness"), "own")
	writeFile(t, filepath.Join(root, "harness", "AGENTS.md"), "# Only instructions\n")
	writeFile(t, filepath.Join(root, "qory-stack.yaml"), "apiVersion: qory.ai/v1alpha1\ntarget:\n  runtime: any\nmodules:\n  - name: own\n    source: {path: harness}\n")
	out, err := run(t, "hc")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "composed 0 entries from 1 module")
	linkTarget(t, root, "AGENTS.md")
	if info, err := os.Stat(filepath.Join(root, ".qory", "harness", "skills")); err != nil || !info.IsDir() {
		t.Errorf("the home has no skills directory: %v", err)
	}
}
