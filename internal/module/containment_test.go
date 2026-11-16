package module

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadRefusesMCPValuesOfTheWrongType covers the value shapes the schema refuses: a
// number among the args, a number in env, a type outside the three, an empty command, and
// data after the object.
func TestReadRefusesMCPValuesOfTheWrongType(t *testing.T) {
	const file = "\n  The file is mcp/db.json in module core."
	for _, c := range []struct{ body, want string }{
		{`{"command": "x", "args": [30]}`, "Set each item of args of the MCP server to a string.\n  args contains 30, which is not a string." + file},
		{`{"command": "x", "env": {"TIMEOUT": 30}}`, "Set each value of env of the MCP server to a string.\n  env.TIMEOUT is 30, which is not a string." + file},
		{`{"command": "x", "type": "grpc"}`, "Set type of the MCP server to stdio, http, or sse.\n  type is grpc." + file},
		{`{"command": ""}`, "Set command of the MCP server to a non-empty string.\n  command is not a non-empty string." + file},
		{`{"command": "x", "description": 3}`, "Set description of the MCP server to a string.\n  description is 3, which is not a string." + file},
		{`{"command": "x"} trailing`, "Write mcp/db.json in module core as one JSON object.\n  The file does not contain a JSON object: invalid character 't' after top-level value"},
	} {
		dir := tree(t, map[string]string{"mcp/db.json": c.body})
		_, err := Read("core", dir, nil, "")
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: error %v, want %q", c.body, err, c.want)
		}
	}
	// A description that is a string is accepted and kept in the object.
	dir := tree(t, map[string]string{"mcp/db.json": `{"command": "x", "description": "the app database"}`})
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.MCP["db"]["description"] != "the app database" {
		t.Errorf("mcp %v, want the description kept", l.MCP)
	}
}

// TestReadRefusesAPathThatLeavesTheModule is an entry, a fragment, AGENTS.md or a file
// inside a skill that is a symlink to a file or directory outside the module: the harness
// would read what the report does not show, so the compose refuses and names the target.
func TestReadRefusesAPathThatLeavesTheModule(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "skill", "SKILL.md"), []byte("# s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ link, target, want string }{
		{"AGENTS.md", filepath.Join(outside, "secret"), "Point the link inside module core, or remove it.\n  The link AGENTS.md leads to "},
		{"agents/reviewer.md", filepath.Join(outside, "secret"), "Point the link inside module core, or remove it.\n  The link agents/reviewer leads to "},
		{"hooks/guard.sh", filepath.Join(outside, "secret"), "Point the link inside module core, or remove it.\n  The link hooks/guard.sh leads to "},
		{"skills/s", filepath.Join(outside, "skill"), "Point the link inside module core, or remove it.\n  The link skills/s leads to "},
		{"skills/ok/notes.md", filepath.Join(outside, "secret"), "Point the link inside module core, or remove it.\n  The link skills/ok/notes.md leads to "},
		{"skills/ok/lib/helper.py", filepath.Join(outside, "secret"), "Point the link inside module core, or remove it.\n  The link skills/ok/lib/helper.py leads to "},
		{"skills/ok/vendor", outside, "Point the link inside module core, or remove it.\n  The link skills/ok/vendor leads to "},
		{"settings/claude/settings.json", filepath.Join(outside, "secret"), "Point the link inside module core, or remove it.\n  The link settings/claude/settings.json leads to "},
	} {
		dir := tree(t, map[string]string{"skills/ok/SKILL.md": "# ok\n"})
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, c.link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(c.target, filepath.Join(dir, c.link)); err != nil {
			t.Fatal(err)
		}
		_, err := Read("core", dir, nil, "")
		if err == nil || !strings.HasPrefix(err.Error(), c.want) || !strings.HasSuffix(err.Error(), ", outside the module, and qory reads only the module's files.") {
			t.Errorf("%s: error %v, want it to start with %q", c.link, err, c.want)
		}
	}
	// A link inside the module is fine, at the root and inside a skill.
	dir := tree(t, map[string]string{"shared/AGENTS.md": "# shared\n", "shared/lib/helper.py": "pass\n", "skills/ok/SKILL.md": "# ok\n"})
	if err := os.Symlink(filepath.Join("shared", "AGENTS.md"), filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "shared", "lib"), filepath.Join(dir, "skills", "ok", "lib")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read("core", dir, nil, ""); err != nil {
		t.Errorf("a link inside the module was refused: %v", err)
	}
}

// TestReadRefusesADanglingLinkInsideASkill is a file in a skill that links nowhere: the
// walk cannot resolve it, and the error names the skill.
func TestReadRefusesADanglingLinkInsideASkill(t *testing.T) {
	dir := tree(t, map[string]string{"skills/ok/SKILL.md": "# ok\n"})
	if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "skills", "ok", "notes.md")); err != nil {
		t.Fatal(err)
	}
	_, err := Read("core", dir, nil, "")
	if err == nil || !strings.Contains(err.Error(), "skills/ok/notes.md ") || !strings.HasPrefix(err.Error(), "Check that you can read the files of module core.\n  ") {
		t.Errorf("error %v, want it to name skills/ok/notes.md", err)
	}
}

// TestReadFollowsALinkedSettingsDirectory is settings/claude as a symlink to a directory
// in the module: it reads like the directory.
func TestReadFollowsALinkedSettingsDirectory(t *testing.T) {
	dir := tree(t, map[string]string{"shared/settings.json": "{}\n"})
	if err := os.MkdirAll(filepath.Join(dir, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "shared"), filepath.Join(dir, "settings", "claude")); err != nil {
		t.Fatal(err)
	}
	l, err := Read("core", dir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if l.Settings["claude"]["settings.json"] == "" {
		t.Errorf("settings %v, want claude/settings.json through the link", l.Settings)
	}
}

// TestReadNamesTheVariantDirectoryInItsMessages is a skill without SKILL.md and a server
// that is not an object under a variant: the messages name the directory read.
func TestReadNamesTheVariantDirectoryInItsMessages(t *testing.T) {
	manifest := "apiVersion: qory.dev/v1alpha1\nname: core\nvariants:\n  codex: {skills: codex/skills, mcp: codex/mcp}\n"
	dir := tree(t, map[string]string{"codex/skills/x/notes.md": "n\n", "qory-module.yaml": manifest})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Read("core", dir, m, "codex")
	if err == nil || err.Error() != "Add SKILL.md to the folder.\n  codex/skills/x in module core has no SKILL.md and no skill below it." {
		t.Errorf("skills: %v", err)
	}
	dir = tree(t, map[string]string{"codex/mcp/db.json": "[]\n", "qory-module.yaml": manifest})
	m, _ = ReadManifest(dir)
	_, err = Read("core", dir, m, "codex")
	if err == nil || !strings.HasPrefix(err.Error(), "Write codex/mcp/db.json in module core as one JSON object.\n  The file does not contain a JSON object") {
		t.Errorf("mcp: %v", err)
	}
}

// TestReadAcceptsADirectoryNamedWithTwoDots is a variant directory named ..agents, which
// is inside the module however it reads.
func TestReadAcceptsADirectoryNamedWithTwoDots(t *testing.T) {
	dir := tree(t, map[string]string{
		"..agents/planner.md": "p\n",
		"qory-module.yaml":    "apiVersion: qory.dev/v1alpha1\nname: core\nvariants:\n  codex: {agents: ..agents}\n",
	})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Read("core", dir, m, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 1 || l.Entries[0].Name != "planner" {
		t.Errorf("entries %+v", l.Entries)
	}
}

// TestParseDocumentReadsWindowsFiles is an agent file saved with CRLF line endings, or
// with a byte order mark: the frontmatter is still frontmatter, a fence may carry trailing
// spaces, and an empty block is a mapping with no keys.
func TestParseDocumentReadsWindowsFiles(t *testing.T) {
	for _, c := range []struct{ name, data string }{
		{"crlf", "---\r\nname: reviewer\r\n---\r\n\r\nBody line.\r\n"},
		{"bom", "\ufeff---\nname: reviewer\n---\n\nBody line.\n"},
		{"trailing space on the fence", "---\nname: reviewer\n---  \n\nBody line.\n"},
	} {
		d, err := ParseDocument([]byte(c.data), c.name)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if d.String("name") != "reviewer" || d.Body != "Body line.\n" {
			t.Errorf("%s: got %+v", c.name, d)
		}
	}
	d, err := ParseDocument([]byte("---\n---\nbody\n"), "b.md")
	if err != nil {
		t.Fatal(err)
	}
	if d.Front == nil || len(d.Front) != 0 || d.Body != "body\n" {
		t.Fatalf("empty block: got %+v", d)
	}
}
