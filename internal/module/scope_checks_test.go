package module

import (
	"reflect"
	"strings"
	"testing"
)

// TestReadManifestReadsScopeChecks reads the scope_checks key: for each type the commands
// in the order of the file, each program's path cleaned and the arguments as written, and
// no key as nil.
func TestReadManifestReadsScopeChecks(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\nscope_checks:\n" +
		"  build-plan:\n    - [./checks/checks.js, scope, --plan]\n    - [checks/checks.js, freeze, --plan]\n" +
		"  change-plan:\n    - [checks/change.sh]\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][][]string{
		"build-plan":  {{"checks/checks.js", "scope", "--plan"}, {"checks/checks.js", "freeze", "--plan"}},
		"change-plan": {{"checks/change.sh"}},
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
// twice, a value that is not a list or is empty, a command that is not a list or is empty,
// a word that is not a string, and a program that is empty or outside the module.
func TestReadManifestFailsOnScopeChecks(t *testing.T) {
	head := "apiVersion: qory.dev/v1alpha1\nname: core\nscope_checks:\n"
	typeRule := "An artifact type is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes."
	for _, c := range []struct{ name, body, want string }{
		{"upper case", "  Plan:\n    - [c.js]\n", "Set each key of scope_checks to an artifact type.\n  " + typeRule + "\n  The key is \"Plan\"."},
		{"an empty type", "  \"\":\n    - [c.js]\n", "Set each key of scope_checks to an artifact type."},
		{"a long type", "  " + strings.Repeat("a", 65) + ":\n    - [c.js]\n", "Set each key of scope_checks to an artifact type."},
		{"a type with an underscore", "  build_plan:\n    - [c.js]\n", "Set each key of scope_checks to an artifact type."},
		{"one type twice", "  plan:\n    - [c.js]\n  plan:\n    - [d.js]\n", "Remove one of the two scope_checks keys for plan.\n  A manifest has one list of commands per type."},
		{"an empty list", "  plan: []\n", "Set scope_checks.plan to a list of commands.\n  The value is not a list, or the list is empty."},
		{"no value", "  plan:\n", "Set scope_checks.plan to a list of commands."},
		{"a string", "  plan: c.js\n", "Set scope_checks.plan to a list of commands."},
		{"an empty command", "  plan:\n    - [c.js]\n    - []\n", "Set scope_checks.plan[1] to the program's path inside the module, then its arguments.\n  The value is not a list, or the list is empty."},
		{"a command as a string", "  plan:\n    - c.js scope\n", "Set scope_checks.plan[0] to the program's path inside the module, then its arguments."},
		{"an empty program", "  plan:\n    - [\"\", scope]\n", "Set the first item of scope_checks.plan[0] to a path inside the module.\n  \"\" is not one."},
		{"an absolute program", "  plan:\n    - [/usr/bin/node]\n", "Set the first item of scope_checks.plan[0] to a path inside the module.\n  \"/usr/bin/node\" is not one."},
		{"a program above the module", "  plan:\n    - [../c.js]\n", "Set the first item of scope_checks.plan[0] to a path inside the module."},
		{"a program with a .. part", "  plan:\n    - [a/../c.js]\n", "Set the first item of scope_checks.plan[0] to a path inside the module."},
		{"a number", "  plan:\n    - [c.js, 3]\n", "Set scope_checks.plan[0][1] to a string, quoted when it reads as a number or a boolean."},
		{"a list", "  plan:\n    - [[c.js]]\n", "Set scope_checks.plan[0][0] to a string"},
		{"a list instead of a map", "  - plan\n", "Set scope_checks to a map from an artifact type to a list of commands."},
	} {
		dir := tree(t, map[string]string{ManifestName: head + c.body})
		_, err := ReadManifest(dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
