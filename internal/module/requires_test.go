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
		{"no entry", head + "  - commands: [ship]\n", "requires[0]: names no entry"},
		{"two entries", head + "  - skill: deploy\n    agent: reviewer\n    commands: [ship]\n", "requires[0]: names two entries"},
		{"an unknown key", head + "  - skill: deploy\n    scripts: [ship]\n", `requires[0]: key "scripts" is not one requires reads`},
		{"an empty list", head + "  - skill: deploy\n    commands: []\n", "requires[0]: commands is empty"},
		{"nothing needed", head + "  - skill: deploy\n", "requires[0]: lists nothing the entry needs"},
		{"an empty entry name", head + "  - skill: \"\"\n    commands: [ship]\n", "requires[0]: skill names no entry"},
		{"an entry twice", head + "  - skill: deploy\n    commands: [ship]\n  - skill: deploy\n    agents: [reviewer]\n", "requires names skill deploy twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := tree(t, map[string]string{ManifestName: c.yaml})
			_, err := ReadManifest(dir)
			if err == nil {
				t.Fatalf("read %q without an error", c.yaml)
			}
			path := filepath.Join(dir, ManifestName)
			if !strings.HasPrefix(err.Error(), path+": ") {
				t.Fatalf("error %q does not start with %q", err, path)
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
	if err == nil || err.Error() != "module core: requires names skill release, which the module does not ship" {
		t.Fatalf("err = %v", err)
	}
	m.Requires = map[string][]string{"skills/deploy": {"commands/ship"}, "files/claude/rules/one.md": {"hooks/guard.sh"}}
	if _, err := Read("core", dir, m, ""); err != nil {
		t.Fatal(err)
	}
}
