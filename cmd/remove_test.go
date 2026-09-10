package cmd_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/ui"
)

// TestRemoveTakesTheLinksAndTheQoryDir checks that remove after a compose prints what it
// removed, leaves neither link nor qory directory, and puts the checkout's own files back
// the way they were.
func TestRemoveTakesTheLinksAndTheQoryDir(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, "README.md"), "The checkout's own README.\n")
	before := snapshot(t, root)

	if out, err := run(t, "harness", "compose"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := run(t, "harness", "remove")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app", "removed .claude", "removed .mcp.json", "removed .qory")

	gone(t, root, ".claude", ".mcp.json", ".qory")
	if after := snapshot(t, root); !maps.Equal(after, before) {
		t.Errorf("the checkout after remove is not the checkout before compose:\nbefore %v\nafter  %v", before, after)
	}
}

// TestRemoveKeepsTheCheckoutsOwnAgentsFile composes for the runtime that links AGENTS.md at
// the checkout root, where the checkout ships its own. Compose keeps that file and says so,
// and remove takes its own links only.
func TestRemoveKeepsTheCheckoutsOwnAgentsFile(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	own := filepath.Join(root, "AGENTS.md")
	writeFile(t, own, "The checkout's own instructions.\n")

	out, err := run(t, "harness", "compose", "--runtime", "any")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, ui.Mark+" acme/app · any opus")
	wantsRow(t, out, "any", ".agents/skills")
	wantsRow(t, out, "kept", "AGENTS.md  (the checkout's own; not linked)")
	ownedByCheckout(t, own, "The checkout's own instructions.\n")
	dirLinks(t, root, filepath.Join(".agents", "skills"), "review", "test", "e2e")

	out, err = run(t, "harness", "remove")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed .agents/skills", "removed .qory")
	lacks(t, out, "removed AGENTS.md")
	gone(t, root, ".qory", ".agents")
	ownedByCheckout(t, own, "The checkout's own instructions.\n")
}

// TestComposeKeepsClaudeCodesOwnLocalSettings is a person who answered a permission
// prompt: .claude/settings.local.json is theirs, and it survives compose and remove.
func TestComposeKeepsClaudeCodesOwnLocalSettings(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	local := filepath.Join(root, ".claude", "settings.local.json")
	writeFile(t, local, "{\"permissions\": {\"allow\": [\"Bash(ls)\"]}}\n")

	out, err := run(t, "hc")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	lacks(t, out, "kept")
	dirLinks(t, root, ".claude", "settings.json", "skills")
	ownedByCheckout(t, local, "{\"permissions\": {\"allow\": [\"Bash(ls)\"]}}\n")
	exclude, _ := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if strings.Contains(string(exclude), "/.claude\n") {
		t.Errorf("the exclude file hides the whole .claude directory:\n%s", exclude)
	}
	wants(t, string(exclude), "/.claude/settings.json\n", "/.claude/skills\n")

	out, err = run(t, "hr")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wants(t, out, "removed .claude")
	ownedByCheckout(t, local, "{\"permissions\": {\"allow\": [\"Bash(ls)\"]}}\n")
	gone(t, root, ".claude/settings.json", ".qory")
}

// TestComposeForceReplacesTrackedFiles is a run machine composing over a repository that
// commits its own .claude/settings.json and AGENTS.md: --force replaces them, says so,
// and remove says how to get them back.
func TestComposeForceReplacesTrackedFiles(t *testing.T) {
	root := newCheckout(t)
	copyFixture(t, "two-modules", root)
	writeFile(t, filepath.Join(root, ".claude", "settings.json"), "{}\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "theirs\n")
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "own")

	out, err := run(t, "hc", "--runtime", "claude,any")
	if err == nil {
		t.Fatalf("composed over a tracked settings.json:\n%s", out)
	}
	wants(t, err.Error(), ".claude/settings.json is not a link qory wrote")

	out, err = run(t, "hc", "--runtime", "claude,any", "--force")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	// One replaced row per runtime, under the runtime whose link took the place.
	if rows := fieldRows(out)["replaced"]; !slices.Equal(rows, []string{
		".claude/settings.json  (the checkout's own; git checkout -- restores it)",
		"AGENTS.md  (the checkout's own; git checkout -- restores it)",
	}) {
		t.Errorf("replaced rows = %q", rows)
	}
	dirLinks(t, root, ".claude", "settings.json")
	linkTarget(t, root, "AGENTS.md")
	if rep := readReport(t, root); !slices.Equal(rep.Replaced, []string{".claude/settings.json", "AGENTS.md"}) {
		t.Errorf("report replaced = %q", rep.Replaced)
	}

	// A second compose without --force keeps naming them, and remove prints the command
	// that restores them; from a subdirectory it names the root.
	if out, err := run(t, "hc", "--runtime", "claude,any"); err != nil {
		t.Fatalf("second compose: %v\n%s", err, out)
	}
	if rep := readReport(t, root); !slices.Equal(rep.Replaced, []string{".claude/settings.json", "AGENTS.md"}) {
		t.Errorf("report replaced after a second compose = %q", rep.Replaced)
	}
	if out, err := run(t, "hr", "--runtime", "any"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		wants(t, out, "git checkout -- AGENTS.md")
		lacks(t, out, ".claude/settings.json")
	}
	if rep := readReport(t, root); !slices.Equal(rep.Replaced, []string{".claude/settings.json"}) {
		t.Errorf("report replaced after dropping any = %q", rep.Replaced)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	if out, err := run(t, "hr"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		wants(t, out, "git -C "+root+" checkout -- .claude/settings.json")
	}
	t.Chdir(root)
	runGit(t, root, "checkout", "--", ".claude/settings.json", "AGENTS.md")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "theirs, edited\n")
	out, err = run(t, "hc", "--runtime", "any", "--force")
	if err == nil {
		t.Fatalf("composed over a modified AGENTS.md:\n%s", out)
	}
	wants(t, err.Error(), "AGENTS.md has uncommitted changes; qory does not replace it")
}

// ownedByCheckout checks that path is still a regular file holding what the checkout wrote,
// and not a link into a home.
func ownedByCheckout(t *testing.T, path, content string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is %s, want a regular file", path, info.Mode())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("%s holds %q, want %q", path, data, content)
	}
}
