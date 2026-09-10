package render_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// TestLinkStaysInsideTheCheckout is a repository that commits .github as a symlink: the
// soft copilot links behind it are passed over, a hard link behind such a parent is
// refused, nothing is written or pruned where the link points, and a remove leaves what
// lies there and the link itself alone.
func TestLinkStaysInsideTheCheckout(t *testing.T) {
	res, root, home := composeFixture(t, "copilot", "goose")
	copilot, goose := lookup(t, "copilot"), lookup(t, "goose")
	if err := render.Build(res, home, copilot, goose); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	// Behind the link: a stale-looking link of another checkout, and an empty hooks dir.
	write(t, filepath.Join(outside, "agents", "keep.md"), "theirs\n")
	if err := os.Symlink(filepath.Join("..", ".qory", "harness", "x"), filepath.Join(outside, "agents", "stale.agent.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".github")); err != nil {
		t.Fatal(err)
	}
	linked, err := render.LinkInto(copilot, res, root, home, false)
	if err != nil || len(linked.Skipped) != 2 || linked.Skipped[0] != ".github/agents" || linked.Skipped[1] != ".github/hooks" {
		t.Fatalf("copilot: linked=%+v err=%v", linked, err)
	}
	for _, name := range []string{"agents/keep.md", "agents/stale.agent.md", "hooks"} {
		if _, err := os.Lstat(filepath.Join(outside, name)); err != nil {
			t.Errorf("%s behind the symlink was touched: %v", name, err)
		}
	}
	// A hard link behind a symlinked parent: .agents as a symlink for goose, in place of
	// the directory the copilot links made.
	if err := os.RemoveAll(filepath.Join(root, ".agents")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".agents")); err != nil {
		t.Fatal(err)
	}
	_, err = render.LinkInto(goose, res, root, home, false)
	var foreign *render.ForeignPathError
	if !errorsAs(err, &foreign) || foreign.Path != filepath.Join(root, ".agents") {
		t.Fatalf("goose: err = %v, want a ForeignPathError for .agents", err)
	}
	// The remove takes copilot's AGENTS.md link at the root and nothing behind .github.
	removed, err := render.Unlink(copilot, root)
	if err != nil || len(removed) != 1 || removed[0] != "AGENTS.md" {
		t.Fatalf("unlink: removed=%v err=%v", removed, err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".github")); err != nil {
		t.Errorf("the checkout's own .github link was removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "agents", "stale.agent.md")); err != nil {
		t.Errorf("a link behind the symlink was removed on unlink: %v", err)
	}
}

// TestOwnLinkIsThisCheckoutsOnly is checkout B whose .github links into checkout A: A's
// links are not B's to remove, even though their targets run through a .qory directory.
func TestOwnLinkIsThisCheckoutsOnly(t *testing.T) {
	res, a, homeA := composeFixture(t, "copilot")
	copilot := lookup(t, "copilot")
	if err := render.Build(res, homeA, copilot); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(copilot, res, a, homeA, false); err != nil {
		t.Fatal(err)
	}
	b := t.TempDir()
	if err := os.Symlink(filepath.Join(a, ".github"), filepath.Join(b, ".github")); err != nil {
		t.Fatal(err)
	}
	homeB := filepath.Join(b, ".qory", "harness")
	if err := render.Build(res, homeB, copilot); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(copilot, res, b, homeB, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(a, ".github", "agents", "reviewer.agent.md")); err != nil {
		t.Errorf("A's link was removed by B's compose: %v", err)
	}
}

// TestBuildRefusesAQoryDirectoryThatIsALink is a repository that commits .qory as a
// symlink: the build refuses before it removes or writes anything where it points, and
// a failed build leaves no staging directory behind.
func TestBuildRefusesAQoryDirectoryThatIsALink(t *testing.T) {
	res, root, _ := composeFixture(t, "claude")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "harness", "keep.txt"), "precious\n")
	if err := os.Symlink(outside, filepath.Join(root, ".qory")); err != nil {
		t.Fatal(err)
	}
	err := render.Build(res, filepath.Join(root, ".qory", "harness"), lookup(t, "claude"))
	var foreign *render.ForeignPathError
	if !errorsAs(err, &foreign) {
		t.Fatalf("err = %v, want a ForeignPathError", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "harness", "keep.txt")); err != nil {
		t.Errorf("the file behind the link is gone: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "harness.tmp")); err == nil {
		t.Error("a staging directory was written behind the link")
	}
}

// TestCopilotHooksLandWhereCopilotReadsThem is a settings/copilot/hooks.json fragment: it
// is written under hooks/, behind the .github/hooks link.
func TestCopilotHooksLandWhereCopilotReadsThem(t *testing.T) {
	res, _, home := composeFixture(t, "copilot")
	res.Settings["copilot"] = map[string]map[string]any{"hooks.json": {"version": float64(1)}}
	if err := render.Build(res, home, lookup(t, "copilot")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "copilot", "hooks", "hooks.json")); err != nil {
		t.Errorf("copilot/hooks/hooks.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "copilot", "hooks.json")); err == nil {
		t.Error("hooks.json was written beside hooks/, where nothing links it")
	}
}
