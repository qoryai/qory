package render_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/render"
)

// errorsAs is errors.As under a name that reads at the call site.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

// gitIn runs git in dir and fails the test on an error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// write creates a file and the directories above it.
func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// isLink reports whether path is a symlink.
func isLink(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

// TestDirectoryLinkKeepsTheCheckoutsOwnFiles is a person who answered a permission prompt:
// Claude Code wrote .claude/settings.local.json, and it survives a compose and a remove.
func TestDirectoryLinkKeepsTheCheckoutsOwnFiles(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	claude := lookup(t, "claude")
	local := filepath.Join(root, ".claude", "settings.local.json")
	write(t, local, "{}\n")
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	linked, err := render.LinkInto(claude, res, root, home, false)
	if err != nil || len(linked.Skipped) != 0 {
		t.Fatalf("linked=%+v err=%v", linked, err)
	}
	if info, err := os.Lstat(filepath.Join(root, ".claude")); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf(".claude is not a real directory: %v %v", info, err)
	}
	for _, name := range []string{"skills", "agents", "settings.json", "CLAUDE.md"} {
		if !isLink(t, filepath.Join(root, ".claude", name)) {
			t.Errorf(".claude/%s is not a link", name)
		}
	}
	if data, _ := os.ReadFile(local); string(data) != "{}\n" {
		t.Errorf("settings.local.json = %q", data)
	}
	// A second compose leaves it too.
	if _, err := render.LinkInto(claude, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(local); string(data) != "{}\n" {
		t.Errorf("settings.local.json after the second compose = %q", data)
	}
	removed, err := render.Unlink(claude, root)
	if err != nil || len(removed) != 2 || removed[0] != ".claude" || removed[1] != ".mcp.json" {
		t.Fatalf("unlink: removed=%v err=%v", removed, err)
	}
	if data, _ := os.ReadFile(local); string(data) != "{}\n" {
		t.Errorf("settings.local.json after remove = %q", data)
	}
	if isLink(t, filepath.Join(root, ".claude", "settings.json")) {
		t.Error(".claude/settings.json is still linked")
	}
}

// TestDirectoryLinkReplacesTheWholeDirectoryLinkOfAnEarlierRelease is a checkout composed
// by a qory that linked .claude whole: the link goes and a directory takes its place.
func TestDirectoryLinkReplacesTheWholeDirectoryLinkOfAnEarlierRelease(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	claude := lookup(t, "claude")
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(".qory", "harness", "claude"), filepath.Join(root, ".claude")); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(claude, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	if isLink(t, filepath.Join(root, ".claude")) {
		t.Error(".claude is still one link")
	}
	if !isLink(t, filepath.Join(root, ".claude", "settings.json")) {
		t.Error(".claude/settings.json is not linked")
	}
}

// TestLinkIntoPrunesWhatTheComposeNoLongerAsksFor composes twice, the second time with no
// instructions: the AGENTS.override.md link and the .claude/CLAUDE.md link both go, and
// nothing else does. A kind directory left empty stays linked, so a runtime keeps every
// place it reads.
func TestLinkIntoPrunesWhatTheComposeNoLongerAsksFor(t *testing.T) {
	res, root, home := composeFixture(t, "claude", "codex")
	claude, codex := lookup(t, "claude"), lookup(t, "codex")
	if err := render.Build(res, home, claude, codex); err != nil {
		t.Fatal(err)
	}
	for _, p := range []render.Runtime{claude, codex} {
		if _, err := render.LinkInto(p, res, root, home, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"AGENTS.override.md", ".claude/CLAUDE.md"} {
		if !isLink(t, filepath.Join(root, name)) {
			t.Fatalf("%s was not linked", name)
		}
	}
	res.Instructions = ""
	var entries = res.Entries[:0]
	for _, e := range res.Entries {
		if e.Kind != "output-styles" {
			entries = append(entries, e)
		}
	}
	res.Entries = entries
	if err := render.Build(res, home, claude, codex); err != nil {
		t.Fatal(err)
	}
	for _, p := range []render.Runtime{claude, codex} {
		if _, err := render.LinkInto(p, res, root, home, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"AGENTS.override.md", ".claude/CLAUDE.md"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s is still there", name)
		}
	}
	for _, name := range []string{".claude/skills", ".claude/output-styles", ".claude/settings.json", ".codex/config.toml", ".agents/skills/review"} {
		if !isLink(t, filepath.Join(root, name)) {
			t.Errorf("%s was pruned", name)
		}
	}
}

// TestForceReplacesWhatGitCanRestore is a run machine composing over a repository that
// tracks its own AGENTS.md and .claude/settings.json: tracked and unmodified, they are
// replaced and named; modified or untracked, they are refused.
func TestForceReplacesWhatGitCanRestore(t *testing.T) {
	res, root, home := composeFixture(t, "claude", "opencode")
	claude, opencode := lookup(t, "claude"), lookup(t, "opencode")
	write(t, filepath.Join(root, "AGENTS.md"), "theirs\n")
	write(t, filepath.Join(root, ".claude", "settings.json"), "{}\n")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "-m", "own")
	if err := render.Build(res, home, claude, opencode); err != nil {
		t.Fatal(err)
	}

	linked, err := render.LinkInto(claude, res, root, home, true)
	if err != nil || len(linked.Replaced) != 1 || linked.Replaced[0] != ".claude/settings.json" {
		t.Fatalf("claude: linked=%+v err=%v", linked, err)
	}
	if !isLink(t, filepath.Join(root, ".claude", "settings.json")) {
		t.Error(".claude/settings.json is not a link after force")
	}
	linked, err = render.LinkInto(opencode, res, root, home, true)
	if err != nil || len(linked.Replaced) != 1 || linked.Replaced[0] != "AGENTS.md" || len(linked.Skipped) != 0 {
		t.Fatalf("opencode: linked=%+v err=%v", linked, err)
	}

	// A modified file is not replaced, with or without force, and the error says why.
	gitIn(t, root, "checkout", "--", "AGENTS.md")
	write(t, filepath.Join(root, "AGENTS.md"), "theirs, edited\n")
	_, err = render.LinkInto(opencode, res, root, home, true)
	var foreign *render.ForeignPathError
	if !errorsAs(err, &foreign) || foreign.Reason != "has uncommitted changes" {
		t.Fatalf("err = %v, want a ForeignPathError for uncommitted changes", err)
	}
	// An untracked file neither.
	write(t, filepath.Join(root, "GEMINI.md"), "theirs\n")
	gemini := lookup(t, "gemini")
	if err := render.Build(res, home, claude, opencode, gemini); err != nil {
		t.Fatal(err)
	}
	_, err = render.LinkInto(gemini, res, root, home, true)
	if !errorsAs(err, &foreign) || foreign.Reason != "is not tracked in git" {
		t.Fatalf("err = %v, want a ForeignPathError for an untracked file", err)
	}
	// Without force, the soft link passes it over as before.
	linked, err = render.LinkInto(gemini, res, root, home, false)
	if err != nil || len(linked.Skipped) != 1 || linked.Skipped[0] != "GEMINI.md" || len(linked.Replaced) != 0 {
		t.Fatalf("without force: linked=%+v err=%v", linked, err)
	}
	// And a hard link's path is refused with a plain ForeignPathError. The link written
	// above goes first, because writing through it would land in the home.
	if err := os.Remove(filepath.Join(root, ".gemini", "settings.json")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".gemini", "settings.json"), "{}\n")
	_, err = render.LinkInto(gemini, res, root, home, false)
	if !errorsAs(err, &foreign) || foreign.Reason != "" {
		t.Fatalf("err = %v, want a plain ForeignPathError", err)
	}
}

// TestBuildLinksEveryModule is what a hook or a server reaches through
// $QORY_HARNESS_HOME/modules/<name>: the module's own directory, whole.
func TestBuildLinksEveryModule(t *testing.T) {
	res, _, home := composeFixture(t, "claude")
	if err := render.Build(res, home, lookup(t, "claude")); err != nil {
		t.Fatal(err)
	}
	for _, l := range res.Modules {
		path := filepath.Join(home, "modules", l.Name)
		target, err := os.Readlink(path)
		if err != nil || target != l.Dir {
			t.Errorf("modules/%s links to %q (%v), want %s", l.Name, target, err, l.Dir)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "modules", "core", "scripts", "db.py")); err != nil {
		t.Errorf("a module's own file is not reachable: %v", err)
	}
}

// TestForeignPathAtADirectoryLink is what stands where a runtime directory goes: a symlink
// elsewhere at a soft link's path is passed over and reported, a regular file at a hard
// link's path is refused with the error the exit status is read from.
func TestForeignPathAtADirectoryLink(t *testing.T) {
	res, root, home := composeFixture(t, "copilot", "amp")
	copilot, amp := lookup(t, "copilot"), lookup(t, "amp")
	if err := render.Build(res, home, copilot, amp); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "own", "agents", "triage.agent.md"), "theirs\n")
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "own", "agents"), filepath.Join(root, ".github", "agents")); err != nil {
		t.Fatal(err)
	}
	linked, err := render.LinkInto(copilot, res, root, home, false)
	if err != nil || len(linked.Skipped) != 1 || linked.Skipped[0] != ".github/agents" {
		t.Fatalf("copilot: linked=%+v err=%v", linked, err)
	}
	if target, _ := os.Readlink(filepath.Join(root, ".github", "agents")); target != filepath.Join("..", "own", "agents") {
		t.Errorf(".github/agents now links to %q", target)
	}

	write(t, filepath.Join(root, ".amp"), "not a directory\n")
	_, err = render.LinkInto(amp, res, root, home, false)
	var foreign *render.ForeignPathError
	if !errorsAs(err, &foreign) || foreign.Path != filepath.Join(root, ".amp") {
		t.Fatalf("amp: err = %v, want a ForeignPathError for .amp", err)
	}
}

// TestUnlinkLeavesWhatAnotherRuntimeShares is remove --runtime in a checkout composed for
// two runtimes that both read .agents/skills and AGENTS.md: the links stay for the other.
func TestUnlinkLeavesWhatAnotherRuntimeShares(t *testing.T) {
	res, root, home := composeFixture(t, "codex", "opencode")
	codex, opencode := lookup(t, "codex"), lookup(t, "opencode")
	if err := render.Build(res, home, codex, opencode); err != nil {
		t.Fatal(err)
	}
	for _, p := range []render.Runtime{codex, opencode} {
		if _, err := render.LinkInto(p, res, root, home, false); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := render.Unlink(codex, root, opencode)
	if err != nil || len(removed) != 2 || removed[0] != ".codex" || removed[1] != "AGENTS.override.md" {
		t.Fatalf("unlink: removed=%v err=%v", removed, err)
	}
	for _, name := range []string{".agents/skills/review", "AGENTS.md", ".opencode/opencode.json"} {
		if !isLink(t, filepath.Join(root, name)) {
			t.Errorf("%s went with codex", name)
		}
	}
}

// TestForceLeavesADirectoryHoldingIgnoredFiles is a tracked .claude/skills with a
// gitignored scratch skill in it: git checkout could not bring scratch back, so --force
// does not remove it.
func TestForceLeavesADirectoryHoldingIgnoredFiles(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	claude := lookup(t, "claude")
	write(t, filepath.Join(root, ".claude", "skills", "own", "SKILL.md"), "theirs\n")
	write(t, filepath.Join(root, ".gitignore"), ".claude/skills/scratch/\n")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "-m", "own")
	write(t, filepath.Join(root, ".claude", "skills", "scratch", "SKILL.md"), "a day's work\n")
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	_, err := render.LinkInto(claude, res, root, home, true)
	var foreign *render.ForeignPathError
	if !errorsAs(err, &foreign) || foreign.Reason != "holds files git does not track" {
		t.Fatalf("err = %v, want a refusal for the ignored file", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, ".claude", "skills", "scratch", "SKILL.md")); string(data) != "a day's work\n" {
		t.Errorf("the ignored file is gone: %q", data)
	}
}

// TestUnlinkTakesTheLinksAfterTheQoryDirectoryIsGone is a person who ran rm -rf .qory
// before qory harness remove: the links dangle, and the remove still knows them as
// qory's and takes them.
func TestUnlinkTakesTheLinksAfterTheQoryDirectoryIsGone(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	claude := lookup(t, "claude")
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(claude, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(home)); err != nil {
		t.Fatal(err)
	}
	removed, err := render.Unlink(claude, root)
	if err != nil || len(removed) != 2 || removed[0] != ".claude" || removed[1] != ".mcp.json" {
		t.Fatalf("unlink: removed=%v err=%v", removed, err)
	}
	for _, name := range []string{".claude/settings.json", ".claude", ".mcp.json"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s is still there", name)
		}
	}
}

// TestUnlinkTakesTheExcludeLinesWithTheLinks is remove --runtime in a checkout composed
// for two runtimes: the removed runtime's lines leave the exclude file, the lines of the
// links the other runtime keeps stay, and so does a line the person wrote by hand, byte
// for byte. The qory directory's own line goes on the command module's word.
func TestUnlinkTakesTheExcludeLinesWithTheLinks(t *testing.T) {
	res, root, home := composeFixture(t, "codex", "opencode")
	codex, opencode := lookup(t, "codex"), lookup(t, "opencode")
	file := filepath.Join(root, ".git", "info", "exclude")
	write(t, file, "# mine\n/notes.local\n\n")
	if err := render.Build(res, home, codex, opencode); err != nil {
		t.Fatal(err)
	}
	for _, p := range []render.Runtime{codex, opencode} {
		if _, err := render.LinkInto(p, res, root, home, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := render.Unlink(codex, root, opencode); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	exclude := string(data)
	if !strings.HasPrefix(exclude, "# mine\n/notes.local\n\n") {
		t.Errorf("the person's own lines changed:\n%s", exclude)
	}
	for _, line := range []string{"/.codex/config.toml", "/AGENTS.override.md"} {
		if strings.Contains(exclude, line+"\n") {
			t.Errorf("exclude file still holds %s after codex was removed:\n%s", line, exclude)
		}
	}
	if strings.Contains(exclude, "/.codex/") {
		t.Errorf("exclude file still holds a codex line:\n%s", exclude)
	}
	for _, line := range []string{"/.qory", "/AGENTS.md", "/.agents/skills/review", "/.opencode/opencode.json"} {
		if !strings.Contains(exclude, line+"\n") {
			t.Errorf("exclude file lost %s, which opencode still links:\n%s", line, exclude)
		}
	}
	if err := render.RemoveExclude(root, "/.qory"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(file)
	if strings.Contains(string(data), "/.qory\n") {
		t.Errorf("exclude file still holds /.qory:\n%s", data)
	}
}

// TestUnexcludeKeepsALineAnotherWorktreeHolds is a repository with a linked worktree that
// has a .qory of its own: the exclude file is one for both, so a remove in the main
// checkout leaves the line for the worktree, and the line goes once the worktree's .qory
// is gone too. A line the worktree does not need goes at once.
func TestUnexcludeKeepsALineAnotherWorktreeHolds(t *testing.T) {
	_, root, _ := composeFixture(t, "claude")
	gitIn(t, root, "config", "user.name", "Tester")
	gitIn(t, root, "config", "user.email", "tester@example.com")
	write(t, filepath.Join(root, "README.md"), "hello\n")
	gitIn(t, root, "add", "README.md")
	gitIn(t, root, "commit", "-q", "-m", "first")
	wt := filepath.Join(t.TempDir(), "feature")
	gitIn(t, root, "worktree", "add", "-q", "-b", "feature", wt)
	if err := os.MkdirAll(filepath.Join(wt, ".qory", "harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, ".git", "info", "exclude")
	write(t, file, "/.qory\n/.mcp.json\n")

	if err := render.RemoveExclude(root, "/.mcp.json"); err != nil {
		t.Fatal(err)
	}
	if err := render.RemoveExclude(root, "/.qory"); err != nil {
		t.Fatal(err)
	}
	if got := readExclude(t, root); got != "/.qory\n" {
		t.Errorf("exclude file after the main checkout's remove:\n%s\nwant /.qory alone, which the worktree still holds", got)
	}

	if err := os.RemoveAll(filepath.Join(wt, ".qory")); err != nil {
		t.Fatal(err)
	}
	if err := render.RemoveExclude(root, "/.qory"); err != nil {
		t.Fatal(err)
	}
	if got := readExclude(t, root); got != "" {
		t.Errorf("exclude file once no worktree holds .qory:\n%s\nwant it empty", got)
	}
}
