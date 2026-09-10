package module

import (
	"strings"
	"testing"
)

// TestReadListsFilesUnderTheirRuntime reads files/<runtime>/<path> at any depth as
// entries named <runtime>/<path>, and skips a dot-named file or directory.
func TestReadListsFilesUnderTheirRuntime(t *testing.T) {
	dir := tree(t, map[string]string{
		"files/claude/rules/nextjs-15.md":                "rule\n",
		"files/claude/rules/deep/er.md":                  "deeper\n",
		"files/copilot/instructions/web.instructions.md": "web\n",
		"files/claude/rules/.draft.md":                   "draft\n",
		"files/claude/.private/x.md":                     "private\n",
		"files/.hidden/rules/x.md":                       "hidden\n",
		"files/cursor/":                                  "",
	})
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "files/claude/rules/deep/er.md files/claude/rules/deep/er.md\n" +
		"files/claude/rules/nextjs-15.md files/claude/rules/nextjs-15.md\n" +
		"files/copilot/instructions/web.instructions.md files/copilot/instructions/web.instructions.md\n"
	if got := entryLines(l); got != want {
		t.Fatalf("entries:\n%swant:\n%s", got, want)
	}
}

// TestReadRefusesAFileDirectlyUnderFiles names the shape a file ships in.
func TestReadRefusesAFileDirectlyUnderFiles(t *testing.T) {
	dir := tree(t, map[string]string{"files/rules.md": "rule\n"})
	_, err := Read("core", dir, nil, "")
	want := "module core: files/rules.md is not under a runtime directory; a file ships as files/<runtime>/<path>"
	if err == nil || err.Error() != want {
		t.Fatalf("error %v, want %q", err, want)
	}
}

// TestReadFollowsTheVariantDirectoryForFiles reads files from the directory the variant
// names, like any kind.
func TestReadFollowsTheVariantDirectoryForFiles(t *testing.T) {
	dir := tree(t, map[string]string{
		"files/claude/rules/a.md":      "a\n",
		"files-codex/codex/notes/b.md": "b\n",
	})
	m := &Manifest{Variants: map[string]Variant{"codex": {"files": "files-codex"}}}
	l, err := Read("core", dir, m, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got := entryLines(l); got != "files/codex/notes/b.md files-codex/codex/notes/b.md\n" {
		t.Fatalf("entries:\n%s", got)
	}
}

// TestReadRefusesADirectoryUnderASettingsRuntime names the directory and where a
// runtime's other files go, instead of skipping it.
func TestReadRefusesADirectoryUnderASettingsRuntime(t *testing.T) {
	dir := tree(t, map[string]string{
		"settings/claude/settings.json": "{}\n",
		"settings/claude/rules/a.md":    "a\n",
	})
	_, err := Read("core", dir, nil, "")
	want := "module core: settings/claude/rules is a directory; settings holds one fragment per target file, and a runtime's other files go under files/claude/rules"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error %v, want %q", err, want)
	}
}
