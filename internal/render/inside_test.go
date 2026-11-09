package render_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
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
// is written under hooks/ as qory-hooks.json, behind the .github/hooks link.
func TestCopilotHooksLandWhereCopilotReadsThem(t *testing.T) {
	res, _, home := composeFixture(t, "copilot")
	res.Settings["copilot"] = map[string]map[string]any{"hooks.json": {"version": float64(1)}}
	if err := render.Build(res, home, lookup(t, "copilot")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "copilot", "hooks", "qory-hooks.json")); err != nil {
		t.Errorf("copilot/hooks/qory-hooks.json: %v", err)
	}
	for _, name := range []string{"hooks.json", filepath.Join("hooks", "hooks.json")} {
		if _, err := os.Stat(filepath.Join(home, "copilot", name)); err == nil {
			t.Errorf("copilot/%s was written, at a name a repository's hook file can have", name)
		}
	}
}

// copilotHooks composes res with a settings/copilot/hooks.json fragment for copilot into
// the checkout at root and returns what LinkInto reported.
func copilotHooks(t *testing.T, res *compose.Result, root, home string) render.Linked {
	t.Helper()
	res.Settings["copilot"] = map[string]map[string]any{"hooks.json": {"version": float64(1), "hooks": map[string]any{"agentStop": []any{}}}}
	copilot := lookup(t, "copilot")
	if err := render.Build(res, home, copilot); err != nil {
		t.Fatal(err)
	}
	linked, err := render.LinkInto(copilot, res, root, home, false)
	if err != nil {
		t.Fatal(err)
	}
	return linked
}

// harnessHooksLinked checks that .github/hooks/qory-hooks.json is a link to the harness's
// hook file and that the exclude file lists it.
func harnessHooksLinked(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".github", "hooks", "qory-hooks.json")
	if !isLink(t, path) {
		t.Fatalf("%s is not a link", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "agentStop") {
		t.Errorf("%s does not resolve to the harness hooks: %q, %v", path, data, err)
	}
	if !strings.Contains(readExclude(t, root), "/.github/hooks/qory-hooks.json\n") {
		t.Errorf("the exclude file does not list .github/hooks/qory-hooks.json:\n%s", readExclude(t, root))
	}
}

// TestCopilotHooksInACheckoutWithNoHooks is a repository with no .github/hooks: the
// directory is made with the harness's hook file linked in it.
func TestCopilotHooksInACheckoutWithNoHooks(t *testing.T) {
	res, root, home := composeFixture(t, "copilot")
	linked := copilotHooks(t, res, root, home)
	if len(linked.Skipped) != 0 {
		t.Errorf("skipped %v", linked.Skipped)
	}
	harnessHooksLinked(t, root)
	if _, err := os.Lstat(filepath.Join(root, ".github", "hooks", "hooks.json")); err == nil {
		t.Error(".github/hooks/hooks.json was written")
	}
}

// TestCopilotHooksBesideTheRepositorysHooks is a repository that commits
// .github/hooks/hooks.json: the harness's hook file is linked beside it, so Copilot CLI
// loads both, and the repository's file stays as committed, with git status clean.
func TestCopilotHooksBesideTheRepositorysHooks(t *testing.T) {
	res, root, home := composeFixture(t, "copilot")
	theirs := filepath.Join(root, ".github", "hooks", "hooks.json")
	const body = "{\"version\": 1, \"hooks\": {\"sessionStart\": []}}\n"
	write(t, theirs, body)
	gitIn(t, root, "add", ".github")
	gitIn(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "hooks")
	linked := copilotHooks(t, res, root, home)
	if len(linked.Skipped) != 0 || len(linked.Replaced) != 0 {
		t.Errorf("linked=%+v, want nothing skipped or replaced", linked)
	}
	harnessHooksLinked(t, root)
	if isLink(t, theirs) {
		t.Fatal("the repository's hooks.json was replaced by a link")
	}
	if data, err := os.ReadFile(theirs); err != nil || string(data) != body {
		t.Errorf("the repository's hooks.json changed: %q, %v", data, err)
	}
	out, err := exec.Command("git", "-C", root, "status", "--porcelain").CombinedOutput()
	if err != nil || len(out) != 0 {
		t.Errorf("git status after the compose: %q, %v", out, err)
	}
}

// TestCopilotHooksPassOverAFileAtTheQoryName is a repository with a file that qory did not
// write at .github/hooks/qory-hooks.json: the soft link passes over it and reports it, and the
// file stays as it was.
func TestCopilotHooksPassOverAFileAtTheQoryName(t *testing.T) {
	res, root, home := composeFixture(t, "copilot")
	theirs := filepath.Join(root, ".github", "hooks", "qory-hooks.json")
	const body = "{\"version\": 1}\n"
	write(t, theirs, body)
	linked := copilotHooks(t, res, root, home)
	if len(linked.Skipped) != 1 || linked.Skipped[0] != ".github/hooks/qory-hooks.json" {
		t.Errorf("skipped %v, want .github/hooks/qory-hooks.json", linked.Skipped)
	}
	if isLink(t, theirs) {
		t.Fatal("the repository's qory-hooks.json was replaced by a link")
	}
	if data, err := os.ReadFile(theirs); err != nil || string(data) != body {
		t.Errorf("the repository's qory-hooks.json changed: %q, %v", data, err)
	}
}

// TestCopilotHooksComposedTwice is a second compose of the same checkout, and one of a
// checkout that a compose before qory-hooks.json linked as .github/hooks/hooks.json: the
// harness's hook file stays linked once, and the link at the old name is taken back with
// its exclude line, while the repository's other hook file stays.
func TestCopilotHooksComposedTwice(t *testing.T) {
	res, root, home := composeFixture(t, "copilot")
	theirs := filepath.Join(root, ".github", "hooks", "audit.json")
	write(t, theirs, "{\"version\": 1}\n")
	copilotHooks(t, res, root, home)
	// The link a compose before the rename wrote, into the home's copilot/hooks.
	old := filepath.Join(root, ".github", "hooks", "hooks.json")
	if err := os.Symlink(filepath.Join("..", "..", ".qory", "harness", "copilot", "hooks", "hooks.json"), old); err != nil {
		t.Fatal(err)
	}
	if err := render.Exclude(root, "/.github/hooks/hooks.json"); err != nil {
		t.Fatal(err)
	}
	linked := copilotHooks(t, res, root, home)
	if len(linked.Skipped) != 0 || len(linked.Replaced) != 0 {
		t.Errorf("linked=%+v, want nothing skipped or replaced", linked)
	}
	harnessHooksLinked(t, root)
	if _, err := os.Lstat(old); err == nil {
		t.Error("the link at .github/hooks/hooks.json is still there")
	}
	exclude := readExclude(t, root)
	if strings.Contains(exclude, "/.github/hooks/hooks.json\n") {
		t.Errorf("the exclude file still lists .github/hooks/hooks.json:\n%s", exclude)
	}
	if strings.Count(exclude, "/.github/hooks/qory-hooks.json\n") != 1 {
		t.Errorf("the exclude file lists .github/hooks/qory-hooks.json other than once:\n%s", exclude)
	}
	if isLink(t, theirs) {
		t.Error("the repository's audit.json was replaced")
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Errorf("the repository's audit.json: %v", err)
	}
}

// TestCopilotHooksPassOverASymlinkedHooksDirectory is a repository that commits
// .github/hooks as a symlink: the soft link passes over the directory, and nothing is
// written where the symlink points.
func TestCopilotHooksPassOverASymlinkedHooksDirectory(t *testing.T) {
	res, root, home := composeFixture(t, "copilot")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "hooks.json"), "{\"version\": 1}\n")
	write(t, filepath.Join(root, ".github", "keep.md"), "theirs\n")
	if err := os.Symlink(outside, filepath.Join(root, ".github", "hooks")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".github")
	gitIn(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "hooks")
	linked := copilotHooks(t, res, root, home)
	if len(linked.Skipped) != 1 || linked.Skipped[0] != ".github/hooks" {
		t.Errorf("skipped %v, want [.github/hooks]", linked.Skipped)
	}
	items, err := os.ReadDir(outside)
	if err != nil || len(items) != 1 || items[0].Name() != "hooks.json" {
		t.Errorf("behind the symlink: %v, %v; want hooks.json alone", items, err)
	}
	if !isLink(t, filepath.Join(root, ".github", "hooks")) {
		t.Error("the repository's .github/hooks symlink was replaced")
	}
}

// TestCopilotUnlinkLeavesTheRepositorysHooks is a remove in a checkout whose repository
// has .github/hooks/hooks.json: the qory-hooks.json link and its exclude line go, and
// hooks.json and the .github/hooks folder stay.
func TestCopilotUnlinkLeavesTheRepositorysHooks(t *testing.T) {
	res, root, home := composeFixture(t, "copilot")
	theirs := filepath.Join(root, ".github", "hooks", "hooks.json")
	const body = "{\"version\": 1}\n"
	write(t, theirs, body)
	copilotHooks(t, res, root, home)
	harnessHooksLinked(t, root)
	if _, err := render.Unlink(lookup(t, "copilot"), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".github", "hooks", "qory-hooks.json")); err == nil {
		t.Error(".github/hooks/qory-hooks.json is still there")
	}
	if strings.Contains(readExclude(t, root), "/.github/hooks/qory-hooks.json\n") {
		t.Errorf("the exclude file still lists .github/hooks/qory-hooks.json:\n%s", readExclude(t, root))
	}
	if data, err := os.ReadFile(theirs); err != nil || string(data) != body {
		t.Errorf("the repository's hooks.json changed: %q, %v", data, err)
	}
	if info, err := os.Lstat(filepath.Join(root, ".github", "hooks")); err != nil || !info.IsDir() {
		t.Errorf(".github/hooks is not a folder after the remove: %v", err)
	}
}
