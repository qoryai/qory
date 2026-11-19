package module

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/exports"
)

// TestAModuleRefusalKeepsTheFixLineShortUnderALongPath is each refusal of a module that
// prints a path inside the module: a skill folder two groups hold, a directory under
// hooks, and an MCP server file that is no JSON object, each at a path of 90 characters
// or more. The first line has at most 100 characters and no path, and the path is on a
// later line.
func TestAModuleRefusalKeepsTheFixLineShortUnderALongPath(t *testing.T) {
	long := strings.Repeat("d", 90)
	for name, files := range map[string]map[string]string{
		"two skills of one name":  {"skills/" + long + "/review/SKILL.md": "s\n", "skills/b" + long + "/review/SKILL.md": "s\n"},
		"a directory in hooks":    {"hooks/" + long + "/x.sh": "x\n"},
		"a server that is a list": {"mcp/" + long + ".json": "[]\n"},
		"a server that is null":   {"mcp/" + long + ".json": "null\n"},
	} {
		_, err := Read("core", tree(t, files), nil, "")
		if err == nil {
			t.Errorf("%s: no refusal", name)
			continue
		}
		lines := strings.Split(err.Error(), "\n")
		if n := utf8.RuneCountInString(lines[0]); n > 100 || strings.Contains(lines[0], long) {
			t.Errorf("%s: the first line has %d characters, or the path: %s", name, n, lines[0])
		}
		if !strings.Contains(strings.Join(lines[1:], "\n"), long) {
			t.Errorf("%s: no later line has the path: %v", name, err)
		}
	}
}

// TestAModuleRefusalKeepsTheFixLineShortWithNamesOf64Characters is each refusal that prints
// a module, a variant, an entry or a type name in its first line, read with names of 64
// characters: the first line has at most 100 characters, and a name that it shortens is
// whole in a later line.
func TestAModuleRefusalKeepsTheFixLineShortWithNamesOf64Characters(t *testing.T) {
	isolate(t)
	long := func(prefix string) string { return prefix + strings.Repeat("x", 64-len(prefix)) }
	mod, variant, entry := long("mod-"), long("var-"), long("ent-")
	head := "apiVersion: qory.dev/v1alpha1\nname: " + mod + "\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		fix   string
		read  func(dir string) error
	}{
		{"a variant that sets no kind", map[string]string{ManifestName: head + "variants:\n  " + variant + ": {" + entry + ": x}\n"}, "Remove ", func(dir string) error {
			m, err := ReadManifest(dir)
			if err != nil {
				return err
			}
			_, err = Read(mod, dir, m, variant)
			return err
		}},
		{"a variant that reads a directory outside the module", map[string]string{ManifestName: head + "variants:\n  " + variant + ": {skills: ../x}\n"}, "Set the skills directory of variant ", func(dir string) error {
			m, err := ReadManifest(dir)
			if err != nil {
				return err
			}
			_, err = Read(mod, dir, m, variant)
			return err
		}},
		{"a requires entry that the module does not ship", map[string]string{ManifestName: head + "requires:\n  - skill: " + entry + "\n    agents: [a]\n"}, "Ship skill ", func(dir string) error {
			m, err := ReadManifest(dir)
			if err != nil {
				return err
			}
			_, err = Read(mod, dir, m, "")
			return err
		}},
		{"a hook directory", map[string]string{"hooks/" + entry + "/x.sh": "x\n"}, "Move ", func(dir string) error {
			_, err := Read(mod, dir, nil, "")
			return err
		}},
		{"a skill folder without SKILL.md", map[string]string{"skills/" + entry + "/notes.md": "n\n"}, "Add SKILL.md to the folder", func(dir string) error {
			_, err := Read(mod, dir, nil, "")
			return err
		}},
		{"a manifest key that is no variable name", map[string]string{ManifestName: head + "env:\n  a-" + strings.Repeat("x", 61) + ": bin\n"}, "Rename the key ", func(dir string) error {
			_, err := ReadManifest(dir)
			return err
		}},
		{"a push check type listed twice", map[string]string{ManifestName: head + "push_checks:\n  - type: " + entry + "\n    command: [a.js]\n  - type: " + entry + "\n    command: [b.js]\n"}, "Remove one of the two push checks for ", func(dir string) error {
			_, err := ReadManifest(dir)
			return err
		}},
		{"a requires item listed twice", map[string]string{ManifestName: head + "requires:\n  - skill: " + entry + "\n    agents: [a]\n  - skill: " + entry + "\n    agents: [b]\n"}, "Remove one of the two requires items for skill ", func(dir string) error {
			_, err := ReadManifest(dir)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := c.read(tree(t, c.files))
			if err == nil {
				t.Fatal("no refusal")
			}
			lines := strings.Split(err.Error(), "\n")
			if !strings.HasPrefix(lines[0], c.fix) || utf8.RuneCountInString(lines[0]) > 100 {
				t.Errorf("the first line is not the fix %q of at most 100 characters:\n%s", c.fix, err)
			}
			for _, name := range []string{mod, variant, entry} {
				if short := exports.Short(name); strings.Contains(lines[0], short) && !strings.Contains(strings.Join(lines[1:], "\n"), name) {
					t.Errorf("the first line has %s shortened, and no later line has it whole:\n%s", short, err)
				}
			}
		})
	}
	m := &Manifest{Variants: map[string]Variant{variant: {}}}
	for name, err := range map[string]error{
		"a forced variant the module has not": func() error { _, err := SelectVariant(m, long("oth-"), "codex", "qory.yaml"); return err }(),
		"a forced variant, none declared":     func() error { _, err := SelectVariant(&Manifest{}, variant, "codex", "qory.yaml"); return err }(),
		"a runtime with no variant":           func() error { _, err := SelectVariant(m, "", long("run-"), "qory.yaml"); return err }(),
	} {
		if err == nil {
			t.Errorf("%s: no refusal", name)
			continue
		}
		// The module's place is 7 characters and a name of 24 in a refusal that prints it.
		line := strings.ReplaceAll(strings.Split(err.Error(), "\n")[0], exports.ModulePlace, "module "+strings.Repeat("m", exports.NameMax))
		if n := utf8.RuneCountInString(exports.Fit(line)); n > 100 {
			t.Errorf("%s: the first line has %d characters: %s", name, n, line)
		}
	}
}

// TestAFixLineOf98CharactersStaysAndOneOf99MovesTheValue is the refusal for an MCP server
// file that is no JSON object, for the longest module name a refusal prints: a fix line of
// 98 characters, which is 100 under the "✗ " that qory prints, names the file, and one of
// 99 characters names it on the second line.
func TestAFixLineOf98CharactersStaysAndOneOf99MovesTheValue(t *testing.T) {
	at98 := strings.Repeat("f", 37)
	at99 := strings.Repeat("f", 38)
	if got := jsonObjectRefusal(at98, "cause").Error(); !strings.HasPrefix(got, "Write "+at98+" in the module as one JSON object.\n") {
		t.Errorf("98 characters: %q", got)
	}
	got := jsonObjectRefusal(at99, "cause").Error()
	first, rest, _ := strings.Cut(got, "\n")
	if strings.Contains(first, at99) || !strings.Contains(rest, at99) {
		t.Errorf("99 characters: the file is in line 1, or not in the next lines:\n%s", got)
	}
}
