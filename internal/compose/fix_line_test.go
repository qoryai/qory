package compose_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// TestAFixLineHasAtMost100CharactersWithNamesOf64Characters is each refusal of a module, a
// source, a manifest, a settings fragment, an export and a variant that prints a name in its
// first line, composed with names of 64 characters: the first line has at most 100
// characters, and a name that it shortens is whole in a later line.
func TestAFixLineHasAtMost100CharactersWithNamesOf64Characters(t *testing.T) {
	long := func(prefix string) string { return prefix + strings.Repeat("x", 64-len(prefix)) }
	mod, other, entry, variant, env := long("mod-"), long("oth-"), long("ent-"), long("var-"), long("ENV_")
	one := "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + mod + "\n    source:\n      path: modules/" + mod + "\n"
	two := one + "  - name: " + other + "\n    source:\n      path: modules/" + other + "\n"
	header := "apiVersion: qory.dev/v1alpha1\nname: "
	for _, c := range []struct {
		name     string
		files    map[string]string
		runtimes []string
		fix      string
	}{
		{"a module composed from two sources",
			map[string]string{stack.FileName: one + "  - source:\n      path: modules/dup\n", "modules/" + mod + "/": "", "modules/dup/qory-module.yaml": header + mod + "\n"},
			nil, "Remove one of the two sources of module "},
		{"an only that selects nothing",
			map[string]string{stack.FileName: one + "    only:\n      skills: [" + entry + "]\n", "modules/" + mod + "/": ""},
			nil, "Remove skills/"},
		{"an exclude of a variable the module does not export",
			map[string]string{stack.FileName: one + "    exclude:\n      env: [" + env + "]\n", "modules/" + mod + "/": ""},
			nil, "Remove env "},
		{"a settings fragment that is no JSON",
			map[string]string{stack.FileName: one, "modules/" + mod + "/settings/claude/settings.json": "{\n"},
			nil, "Correct the JSON in the settings fragment of module "},
		{"a settings fragment of another extension",
			map[string]string{stack.FileName: one, "modules/" + mod + "/settings/claude/settings.yaml": "a: b\n"},
			nil, "Rename the settings fragment of module "},
		{"a variable that two modules export",
			map[string]string{stack.FileName: two, "modules/" + mod + "/qory-module.yaml": manifest(mod, env, "."), "modules/" + other + "/qory-module.yaml": manifest(other, env, ".")},
			nil, "Set env "},
		{"a variable that a fragment and a module set",
			map[string]string{stack.FileName: two, "modules/" + mod + "/settings/claude/settings.json": `{"env": {"` + env + `": "/usr/bin"}}`, "modules/" + other + "/qory-module.yaml": manifest(other, env, "bin")},
			nil, "Set env "},
		{"a forced variant of a module with no variants",
			map[string]string{stack.FileName: one + "    variant: " + variant + "\n", "modules/" + mod + "/": ""},
			nil, "Remove variant "},
		{"a forced variant that the module has not",
			map[string]string{stack.FileName: one + "    variant: " + variant + "\n", "modules/" + mod + "/qory-module.yaml": header + mod + "\nvariants:\n  codex: {agents: agents/codex}\n"},
			nil, "Set the variant of module "},
		{"two runtimes that read two variants",
			map[string]string{stack.FileName: one, "modules/" + mod + "/qory-module.yaml": header + mod + "\nvariants:\n  claude: {agents: a}\n  codex: {agents: b}\n"},
			[]string{"claude", "codex"}, "Select one variant of module "},
		{"a variable key that is no name",
			map[string]string{stack.FileName: one, "modules/" + mod + "/qory-module.yaml": manifest(mod, "a-"+strings.Repeat("x", 61), ".")},
			nil, "Rename the key "},
		{"a requires item listed twice",
			map[string]string{stack.FileName: one, "modules/" + mod + "/qory-module.yaml": header + mod + "\nrequires:\n  - skill: " + entry + "\n    agents: [a]\n  - skill: " + entry + "\n    agents: [b]\n"},
			nil, "Remove one of the two requires items for skill "},
		{"a requires item the module does not ship",
			map[string]string{stack.FileName: one, "modules/" + mod + "/qory-module.yaml": header + mod + "\nrequires:\n  - skill: " + entry + "\n    agents: [a]\n"},
			nil, "Ship skill "},
		{"a push check type listed twice",
			map[string]string{stack.FileName: one, "modules/" + mod + "/qory-module.yaml": header + mod + "\npush_checks:\n  - type: " + entry + "\n    command: [a.js]\n  - type: " + entry + "\n    command: [b.js]\n"},
			nil, "Remove one of the two push checks for "},
		{"a folder that is neither a skill nor a group",
			map[string]string{stack.FileName: one, "modules/" + mod + "/skills/" + entry + "/notes.md": "n\n"},
			nil, "Add SKILL.md to the folder"},
		{"a variant that sets a kind it does not read",
			map[string]string{stack.FileName: one, "modules/" + mod + "/qory-module.yaml": header + mod + "\nvariants:\n  claude: {" + entry + ": x}\n"},
			nil, "Remove "},
	} {
		t.Run(c.name, func(t *testing.T) {
			runtimes := c.runtimes
			if runtimes == nil {
				runtimes = []string{"claude"}
			}
			_, err := composeTreeFor(t, c.files, runtimes...)
			if err == nil {
				t.Fatal("composed without an error")
			}
			lines := strings.Split(err.Error(), "\n")
			if !strings.HasPrefix(lines[0], c.fix) {
				t.Errorf("the first line is not the fix %q:\n%s", c.fix, err)
			}
			for i, line := range lines {
				if !strings.HasPrefix(line, " ") && utf8.RuneCountInString(line) > 100 {
					t.Errorf("line %d has %d characters: %s", i+1, utf8.RuneCountInString(line), line)
				}
			}
			for _, name := range []string{mod, other, entry, variant, env} {
				if strings.Contains(lines[0], exports.Short(name)) && !strings.Contains(strings.Join(lines[1:], "\n"), name) {
					t.Errorf("the first line has %s shortened, and no later line has it whole:\n%s", exports.Short(name), err)
				}
			}
		})
	}
}

// TestAFixFirstRefusalKeepsTheErrorThatCausedIt is a settings fragment that is no JSON or
// TOML and a manifest whose variant is no map, composed: the refusal starts with its fix,
// has the module, and still matches the decoder's error with errors.As.
func TestAFixFirstRefusalKeepsTheErrorThatCausedIt(t *testing.T) {
	var syntax *json.SyntaxError
	var parse toml.ParseError
	var typed *yaml.TypeError
	for _, c := range []struct {
		name  string
		files map[string]string
		as    any
		fix   string
	}{
		{"a JSON fragment", map[string]string{stack.FileName: twoModules, "modules/a/settings/claude/settings.json": "{\n", "modules/b/": ""}, &syntax, "Correct the JSON in the settings fragment of module a.\n"},
		{"a TOML fragment", map[string]string{stack.FileName: twoModules, "modules/a/settings/claude/settings.toml": "model = \n", "modules/b/": ""}, &parse, "Correct the TOML in the settings fragment of module a.\n"},
		{"a variant that is no map", map[string]string{stack.FileName: twoModules, "modules/a/qory-module.yaml": "apiVersion: qory.dev/v1alpha1\nname: a\nvariants:\n  codex: agents/codex\n", "modules/b/": ""}, &typed, "Set variants.codex to a map of a kind to a directory.\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := composeTree(t, c.files)
			if err == nil || !strings.HasPrefix(err.Error(), c.fix) {
				t.Fatalf("err = %v, want it to start with %q", err, c.fix)
			}
			if !errors.As(err, c.as) {
				t.Errorf("err = %v, want it to match %T", err, c.as)
			}
		})
	}
}
