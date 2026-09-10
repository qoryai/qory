package render_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/render"
)

// shipFile adds a files entry to the result the way the compose will: named
// <runtime>/<path>, from the nextjs module, pointing at a file written under a module
// directory of the test's own. It returns the file's absolute path.
func shipFile(t *testing.T, res *compose.Result, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "files", filepath.FromSlash(name))
	write(t, path, body)
	res.Entries = append(res.Entries, compose.Entry{Kind: "files", Name: name, Module: "nextjs", Path: path})
	return path
}

// dropFiles removes every files entry from the result, the compose after a module stopped
// shipping them.
func dropFiles(res *compose.Result) {
	kept := res.Entries[:0]
	for _, e := range res.Entries {
		if e.Kind != "files" {
			kept = append(kept, e)
		}
	}
	res.Entries = kept
}

// TestFileReachesClaudeThroughTheDirectoryLink is a module shipping
// files/claude/rules/nextjs-15.md: the home holds it under claude/rules, the checkout's
// .claude/rules is one link like .claude/skills, and Claude Code reads the file through
// it. A remove takes the link with the rest of .claude.
func TestFileReachesClaudeThroughTheDirectoryLink(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	claude := lookup(t, "claude")
	shipFile(t, res, "claude/rules/nextjs-15.md", "# Next.js 15\n")
	if err := render.CheckFiles(res); err != nil {
		t.Fatal(err)
	}
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	if !isLink(t, filepath.Join(home, "claude", "rules", "nextjs-15.md")) {
		t.Error("claude/rules/nextjs-15.md in the home is not a link to the module's file")
	}
	linked, err := render.LinkInto(claude, res, root, home, false)
	if err != nil || len(linked.Skipped) != 0 {
		t.Fatalf("linked=%+v err=%v", linked, err)
	}
	if !isLink(t, filepath.Join(root, ".claude", "rules")) {
		t.Error(".claude/rules is not one link")
	}
	data, err := os.ReadFile(filepath.Join(root, ".claude", "rules", "nextjs-15.md"))
	if err != nil || string(data) != "# Next.js 15\n" {
		t.Errorf(".claude/rules/nextjs-15.md = %q, %v", data, err)
	}
	if _, err := render.Unlink(claude, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".claude", "rules")); err == nil {
		t.Error(".claude/rules survived the remove")
	}
}

// TestFileWithANestedPath is a files entry three directories deep: every directory
// between is created in the home, and the checkout reads the file through the one link
// at .claude/rules.
func TestFileWithANestedPath(t *testing.T) {
	res, root, home := composeFixture(t, "claude")
	claude := lookup(t, "claude")
	shipFile(t, res, "claude/rules/web/react/hooks.md", "# Hooks\n")
	if err := render.Build(res, home, claude); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(claude, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".claude", "rules", "web", "react", "hooks.md"))
	if err != nil || string(data) != "# Hooks\n" {
		t.Errorf(".claude/rules/web/react/hooks.md = %q, %v", data, err)
	}
	if info, err := os.Lstat(filepath.Join(home, "claude", "rules", "web")); err != nil || !info.IsDir() {
		t.Errorf("claude/rules/web in the home is not a real directory: %v", err)
	}
}

// TestCopilotLinksEachFileSoftlyAndTakesItBack is a module shipping
// files/copilot/instructions/web.instructions.md: the checkout gets a relative soft link
// at .github/instructions/web.instructions.md with its exclude line, and a later compose
// without the entry takes the link and its line back while the repository's own
// .github/CODEOWNERS and .github/instructions/own.instructions.md stay.
func TestCopilotLinksEachFileSoftlyAndTakesItBack(t *testing.T) {
	res, root, home := composeFixture(t, "copilot")
	copilot := lookup(t, "copilot")
	shipFile(t, res, "copilot/instructions/web.instructions.md", "web\n")
	write(t, filepath.Join(root, ".github", "CODEOWNERS"), "* @acme\n")
	write(t, filepath.Join(root, ".github", "instructions", "own.instructions.md"), "ours\n")
	if err := render.Build(res, home, copilot); err != nil {
		t.Fatal(err)
	}
	linked, err := render.LinkInto(copilot, res, root, home, false)
	if err != nil || len(linked.Skipped) != 0 {
		t.Fatalf("linked=%+v err=%v", linked, err)
	}
	link := filepath.Join(root, ".github", "instructions", "web.instructions.md")
	checkLink(t, root, home, ".github/instructions/web.instructions.md", "copilot/instructions/web.instructions.md", readExclude(t, root))
	if data, _ := os.ReadFile(link); string(data) != "web\n" {
		t.Errorf("the file read through the link = %q", data)
	}
	// The same compose again changes nothing.
	if _, err := render.LinkInto(copilot, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	if !isLink(t, link) {
		t.Fatal("the link went in the second compose")
	}
	// The module stops shipping the file.
	dropFiles(res)
	if err := render.Build(res, home, copilot); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(copilot, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err == nil {
		t.Error(".github/instructions/web.instructions.md is still there after the module dropped it")
	}
	if strings.Contains(readExclude(t, root), "/.github/instructions/web.instructions.md\n") {
		t.Errorf("exclude file still holds the link's line:\n%s", readExclude(t, root))
	}
	for _, name := range []string{"CODEOWNERS", "instructions/own.instructions.md"} {
		if _, err := os.Stat(filepath.Join(root, ".github", name)); err != nil {
			t.Errorf("the repository's own .github/%s went: %v", name, err)
		}
	}
	if !isLink(t, filepath.Join(root, ".github", "agents", "reviewer.agent.md")) {
		t.Error(".github/agents/reviewer.agent.md went with the file")
	}
}

// TestCopilotFileLinkYieldsToTheRepositorysOwn is a repository that already has
// .github/copilot-instructions.md: the soft link is passed over and reported, and the
// file stays.
func TestCopilotFileLinkYieldsToTheRepositorysOwn(t *testing.T) {
	res, root, home := composeFixture(t, "copilot")
	copilot := lookup(t, "copilot")
	shipFile(t, res, "copilot/copilot-instructions.md", "theirs\n")
	own := filepath.Join(root, ".github", "copilot-instructions.md")
	write(t, own, "ours\n")
	if err := render.Build(res, home, copilot); err != nil {
		t.Fatal(err)
	}
	linked, err := render.LinkInto(copilot, res, root, home, false)
	if err != nil || len(linked.Skipped) != 1 || linked.Skipped[0] != ".github/copilot-instructions.md" {
		t.Fatalf("linked=%+v err=%v", linked, err)
	}
	if data, _ := os.ReadFile(own); string(data) != "ours\n" {
		t.Errorf(".github/copilot-instructions.md = %q", data)
	}
}

// TestUnlinkTakesCopilotFileLinks is qory harness remove after a compose with a copilot
// files entry: the per-file link, which Links with a nil result cannot name, goes with
// its exclude line and the directory it left empty, and the remove names it.
func TestUnlinkTakesCopilotFileLinks(t *testing.T) {
	res, root, home := composeFixture(t, "copilot")
	copilot := lookup(t, "copilot")
	shipFile(t, res, "copilot/instructions/web.instructions.md", "web\n")
	if err := render.Build(res, home, copilot); err != nil {
		t.Fatal(err)
	}
	if _, err := render.LinkInto(copilot, res, root, home, false); err != nil {
		t.Fatal(err)
	}
	removed, err := render.Unlink(copilot, root)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(removed, ".github/instructions/web.instructions.md") {
		t.Errorf("removed = %v, want the file link among them", removed)
	}
	if _, err := os.Lstat(filepath.Join(root, ".github", "instructions")); err == nil {
		t.Error(".github/instructions is left behind empty")
	}
	if _, err := os.Lstat(filepath.Join(root, ".github")); err == nil {
		t.Error(".github is left behind empty")
	}
	if strings.Contains(readExclude(t, root), "/.github/instructions/web.instructions.md\n") {
		t.Errorf("exclude file still holds the link's line:\n%s", readExclude(t, root))
	}
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestGooseSkipsFiles is a files entry for goose, which has no directory in the checkout:
// the compose lists it as skipped, as it sits in the module, and the build links nothing.
func TestGooseSkipsFiles(t *testing.T) {
	res, _, home := composeFixture(t, "goose")
	goose := lookup(t, "goose")
	shipFile(t, res, "goose/config/x.yaml", "x\n")
	if skipped := render.Skipped(goose, res); !contains(skipped, "files/goose/config/x.yaml") {
		t.Errorf("skipped = %v, want files/goose/config/x.yaml among them", skipped)
	}
	if err := render.Build(res, home, goose); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(home, "goose", "config")); err == nil {
		t.Error("the build linked a file goose has no place for")
	}
}

// TestAnotherRuntimesFileIsNeitherRenderedNorSkipped is a cursor files entry in a compose
// for claude and goose: claude renders nothing for it, and neither claude nor goose, which
// skips its own files, lists it as skipped. The fixture's other entries goose skips are
// not the point here.
func TestAnotherRuntimesFileIsNeitherRenderedNorSkipped(t *testing.T) {
	res, _, home := composeFixture(t, "claude", "goose")
	claude, goose := lookup(t, "claude"), lookup(t, "goose")
	shipFile(t, res, "cursor/rules/nextjs-15.mdc", "rule\n")
	if err := render.Build(res, home, claude, goose); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(home, "claude", "rules")); err == nil {
		t.Error("claude rendered a file shipped for cursor")
	}
	for _, p := range []render.Runtime{claude, goose} {
		for _, s := range render.Skipped(p, res) {
			if strings.HasPrefix(s, "files/") {
				t.Errorf("%s skipped %s, want no files entry", p.Name(), s)
			}
		}
	}
}

// TestCheckFilesRefusesReservedPaths is a module shipping a file where a runtime writes,
// where the program reads its settings, in a kind directory, and in copilot's workflows:
// each refusal names the module, the entry as it sits in the module, and why.
func TestCheckFilesRefusesReservedPaths(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"claude/settings.json", "module nextjs ships files/claude/settings.json, and qory writes it"},
		{"claude/settings.local.json", "module nextjs ships files/claude/settings.local.json, and Claude Code reads it as settings; a module sets those through settings/claude/settings.json"},
		{"claude/skills/review/SKILL.md", "module nextjs ships files/claude/skills/review/SKILL.md, and skills are linked there; ship it as skills/<name>"},
		{"copilot/workflows/ci.yml", "module nextjs ships files/copilot/workflows/ci.yml, and GitHub runs it on every push"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, _, _ := composeFixture(t, "claude")
			shipFile(t, res, c.name, "x\n")
			err := render.CheckFiles(res)
			if err == nil || err.Error() != c.want {
				t.Fatalf("err = %v\nwant %s", err, c.want)
			}
		})
	}
}

// TestCheckFilesRefusesAnUnknownRuntime is a module shipping files/vscode/settings.json:
// the refusal names the runtimes qory renders.
func TestCheckFilesRefusesAnUnknownRuntime(t *testing.T) {
	res, _, _ := composeFixture(t, "claude")
	shipFile(t, res, "vscode/settings.json", "{}\n")
	want := "module nextjs ships files/vscode/settings.json, and vscode is not a runtime qory renders; runtimes: " + strings.Join(render.Names(), ", ")
	err := render.CheckFiles(res)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
}

// TestCheckFilesMatchesByPathSegment is a reserved directory name as a prefix of another:
// skills covers skills/x and skills itself, and not skills-private/x.
func TestCheckFilesMatchesByPathSegment(t *testing.T) {
	for _, c := range []struct {
		name    string
		refused bool
	}{
		{"claude/skills-private/x.md", false},
		{"claude/skills/x.md", true},
		{"claude/skills", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, _, _ := composeFixture(t, "claude")
			shipFile(t, res, c.name, "x\n")
			err := render.CheckFiles(res)
			if (err != nil) != c.refused {
				t.Fatalf("err = %v, want refused=%v", err, c.refused)
			}
		})
	}
}

// TestCheckFilesChecksEveryRuntimesEntries is a cursor files entry at a reserved path in a
// compose for claude: the module is wrong wherever it composes, so the check refuses it.
func TestCheckFilesChecksEveryRuntimesEntries(t *testing.T) {
	res, _, _ := composeFixture(t, "claude")
	shipFile(t, res, "cursor/mcp.json", "{}\n")
	want := "module nextjs ships files/cursor/mcp.json, and Cursor reads it as settings; a module sets those through settings/cursor/mcp.json"
	err := render.CheckFiles(res)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
}

// TestBuildRefusesAFileWhereRenderWrote is a files entry at a path a runtime's reserved
// table missed, here CLAUDE.md with the check not run: the build fails naming the entry
// rather than replacing what Render wrote.
func TestBuildRefusesAFileWhereRenderWrote(t *testing.T) {
	res, _, home := composeFixture(t, "claude")
	shipFile(t, res, "claude/CLAUDE.md", "theirs\n")
	err := render.Build(res, home, lookup(t, "claude"))
	if err == nil || !strings.Contains(err.Error(), "files/claude/CLAUDE.md from module nextjs") {
		t.Fatalf("err = %v, want a refusal naming the entry", err)
	}
	if _, err := os.Lstat(home + ".tmp"); err == nil {
		t.Error("the staging directory is left behind")
	}
}
