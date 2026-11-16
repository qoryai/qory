package module

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestReadManifestReadsRequires reads the requires items: each comes back keyed by the
// entry as <kind>/<name>, with what it needs as sorted <kind>/<name> keys, and the mcp key
// is the entry's name as a string in one item and the needed servers as a list in another.
func TestReadManifestReadsRequires(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\n" +
		"name: core\n" +
		"requires:\n" +
		"  - skill: deploy\n" +
		"    commands: [ship, release]\n" +
		"    agents: [reviewer]\n" +
		"  - mcp: db\n" +
		"    hooks: [guard.sh]\n" +
		"  - hook: guard.sh\n" +
		"    mcp: [cache]\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"skills/deploy":  {"agents/reviewer", "commands/release", "commands/ship"},
		"mcp/db":         {"hooks/guard.sh"},
		"hooks/guard.sh": {"mcp/cache"},
	}
	if !reflect.DeepEqual(m.Requires, want) {
		t.Fatalf("requires %v, want %v", m.Requires, want)
	}
	dir = tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\n"})
	if m, err = ReadManifest(dir); err != nil || m.Requires != nil {
		t.Fatalf("requires %v, err %v; want nil", m.Requires, err)
	}
}

// TestReadManifestRefusesRequires covers every requires item the reader turns down: no
// entry, two entries, a key that is neither an entry nor a kind, an empty list, nothing
// needed, and an entry named twice. The message starts with the manifest path.
func TestReadManifestRefusesRequires(t *testing.T) {
	head := "apiVersion: qory.dev/v1alpha1\nname: core\nrequires:\n"
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"no entry", head + "  - commands: [ship]\n", "Select an entry in requires[0], as skill: <name>."},
		{"two entries", head + "  - skill: deploy\n    agent: reviewer\n    commands: [ship]\n", "Remove one of the two entries from requires[0]."},
		{"an unknown key", head + "  - skill: deploy\n    scripts: [ship]\n", `Remove the key "scripts" from requires[0].`},
		{"an empty list", head + "  - skill: deploy\n    commands: []\n", "List the commands the entry needs under commands in requires[0], or remove commands.\n  commands in requires[0] is empty."},
		{"nothing needed", head + "  - skill: deploy\n", "List what the entry needs in requires[0], under skills, agents, commands, output-styles, hooks, mcp, or files.\n  requires[0] lists nothing the entry needs."},
		{"an empty entry name", head + "  - skill: \"\"\n    commands: [ship]\n", "Set skill in requires[0] to the entry's name, as skill: <name>."},
		{"an entry twice", head + "  - skill: deploy\n    commands: [ship]\n  - skill: deploy\n    agents: [reviewer]\n", "Remove one of the two requires items for skill deploy.\n  The requires lists skill deploy twice."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := tree(t, map[string]string{ManifestName: c.yaml})
			_, err := ReadManifest(dir)
			if err == nil {
				t.Fatalf("read %q without an error", c.yaml)
			}
			path := filepath.Join(dir, ManifestName)
			if !strings.HasSuffix(err.Error(), "\n  The manifest is "+path+".") {
				t.Fatalf("error %q does not end with the path %q", err, path)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not name %q", err, c.want)
			}
		})
	}
}

// TestReadRefusesARequirementOfAnEntryNotShipped is a manifest whose requires names an
// entry the module does not ship: Read refuses it, naming the module and the entry, and a
// manifest naming a shipped entry reads.
func TestReadRefusesARequirementOfAnEntryNotShipped(t *testing.T) {
	dir := tree(t, map[string]string{
		ManifestName:                "apiVersion: qory.dev/v1alpha1\nname: core\nrequires:\n  - skill: release\n    commands: [ship]\n",
		"skills/deploy/SKILL.md":    "# deploy\n",
		"commands/ship.md":          "# ship\n",
		"agents/reviewer.md":        "# reviewer\n",
		"output-styles/terse.md":    "# terse\n",
		"hooks/guard.sh":            "#!/bin/sh\n",
		"files/claude/rules/one.md": "# one\n",
	})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Read("core", dir, m, "")
	if err == nil || err.Error() != "Ship skill release in module core, or remove it from the requires of the module.\n  The requires of module core lists skill release, and the module does not ship it." {
		t.Fatalf("err = %v", err)
	}
	m.Requires = map[string][]string{"skills/deploy": {"commands/ship"}, "files/claude/rules/one.md": {"hooks/guard.sh"}}
	if _, err := Read("core", dir, m, ""); err != nil {
		t.Fatal(err)
	}
}
