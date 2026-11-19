package module

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestReadManifestReadsScopeChecks reads the scope_checks key: for each type one command,
// the program's path cleaned and the arguments as written, and no key as nil.
func TestReadManifestReadsScopeChecks(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\nscope_checks:\n" +
		"  build-plan: [./checks/checks.js, scope, --plan]\n" +
		"  change-plan: [checks/change.sh]\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"build-plan":  {"checks/checks.js", "scope", "--plan"},
		"change-plan": {"checks/change.sh"},
	}
	if !reflect.DeepEqual(m.ScopeChecks, want) {
		t.Errorf("scope checks %v, want %v", m.ScopeChecks, want)
	}
	dir = tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\n"})
	if m, err = ReadManifest(dir); err != nil || m.ScopeChecks != nil {
		t.Errorf("no scope_checks read as %v, %v", m.ScopeChecks, err)
	}
}

// TestReadManifestFailsOnScopeChecks fails on a key that is not an artifact type, one type
// twice, a value that is not a list or is empty, a word that is not a string, and a
// program that is empty or outside the module. Each refusal is compared whole.
func TestReadManifestFailsOnScopeChecks(t *testing.T) {
	head := "apiVersion: qory.dev/v1alpha1\nname: core\nscope_checks:\n"
	typeRule := "An artifact type is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes."
	const notList = "Set scope_checks.plan to the program's path inside the module, then its arguments.\n  The value is not a list, or the list is empty."
	badKey := "Set each key of scope_checks to an artifact type.\n  " + typeRule + "\n  The key is "
	const oneCommand = "Give each plan type of scope_checks one command, a list of strings, not a list of lists."
	const notString = "\n  The item is not a string."
	for _, c := range []struct{ name, body, want string }{
		{"upper case", "  Plan: [c.js]\n", badKey + "\"Plan\"."},
		{"an empty type", "  \"\": [c.js]\n", badKey + "\"\"."},
		{"a long type", "  " + strings.Repeat("a", 65) + ": [c.js]\n", badKey + "\"" + strings.Repeat("a", 65) + "\"."},
		{"a type with an underscore", "  build_plan: [c.js]\n", badKey + "\"build_plan\"."},
		{"one type twice", "  plan: [c.js]\n  plan: [d.js]\n", "Remove one of the two scope_checks keys for plan.\n  A manifest has one command per type."},
		{"an empty command", "  plan: []\n", notList},
		{"no value", "  plan:\n", notList},
		{"a string", "  plan: c.js\n", notList},
		{"a list of commands", "  plan:\n    - [c.js]\n    - [d.js]\n", oneCommand + "\n  scope_checks.plan[0] is a list."},
		{"a list of commands under a 64-character type", "  " + strings.Repeat("p", 64) + ":\n    - [c.js]\n", oneCommand + "\n  scope_checks." + strings.Repeat("p", 64) + "[0] is a list."},
		{"an empty program", "  plan: [\"\", scope]\n", "Set the first item of scope_checks.plan to a path inside the module.\n  \"\" is not one."},
		{"an absolute program", "  plan: [/usr/bin/node]\n", "Set the first item of scope_checks.plan to a path inside the module.\n  \"/usr/bin/node\" is not one."},
		{"a program above the module", "  plan: [../c.js]\n", "Set the first item of scope_checks.plan to a path inside the module.\n  \"../c.js\" is not one."},
		{"a program with a .. part", "  plan: [a/../c.js]\n", "Set the first item of scope_checks.plan to a path inside the module.\n  \"a/../c.js\" is not one."},
		{"a number", "  plan: [c.js, 3]\n", "Set scope_checks.plan[1] to a string, quoted when it reads as a number or a boolean." + notString},
		{"a list", "  plan: [[c.js]]\n", oneCommand + "\n  scope_checks.plan[0] is a list."},
		{"a list instead of a map", "  - plan\n", "Set scope_checks to a map from an artifact type to a command.\n  The value is not a map."},
	} {
		dir := tree(t, map[string]string{ManifestName: head + c.body})
		_, err := ReadManifest(dir)
		want := c.want + "\n  The manifest is " + filepath.Join(dir, ManifestName) + "."
		if err == nil || err.Error() != want {
			t.Errorf("%s:\n%v\nwant:\n%s", c.name, err, want)
		}
	}
}
