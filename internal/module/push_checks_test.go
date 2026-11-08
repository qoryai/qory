package module

import (
	"reflect"
	"strings"
	"testing"
)

// TestReadManifestReadsPushChecks reads the push_checks key: each item in the order of
// the file, the program's path cleaned, the arguments and reads as written, and no key as
// nil.
func TestReadManifestReadsPushChecks(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\npush_checks:\n" +
		"  - type: spec\n    command: [./skills/running/checks.js, push, spec]\n" +
		"  - type: review-comments\n    command: [checks/review.sh]\n    reads: [spec, questions]\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []PushCheck{
		{Type: "spec", Command: []string{"skills/running/checks.js", "push", "spec"}},
		{Type: "review-comments", Command: []string{"checks/review.sh"}, Reads: []string{"spec", "questions"}},
	}
	if !reflect.DeepEqual(m.PushChecks, want) {
		t.Errorf("push checks %v, want %v", m.PushChecks, want)
	}
	dir = tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: core\n"})
	if m, err = ReadManifest(dir); err != nil || m.PushChecks != nil {
		t.Errorf("no push_checks read as %v, %v", m.PushChecks, err)
	}
}

// TestReadManifestFailsOnPushChecks fails on a type that is not an artifact type, one type
// twice, an empty command, a word that is not a string, a program outside the module, a
// key the item does not have, and a read that is not a type or is listed twice.
func TestReadManifestFailsOnPushChecks(t *testing.T) {
	head := "apiVersion: qory.dev/v1alpha1\nname: core\npush_checks:\n"
	for _, c := range []struct{ name, items, want string }{
		{"upper case", "  - type: Spec\n    command: [c.js]\n", `push_checks[0].type: set it to an artifact type, lowercase letters and digits in groups joined by single hyphens, at most 64 bytes. The value is "Spec"`},
		{"an empty type", "  - type: \"\"\n    command: [c.js]\n", `push_checks[0].type: set it to an artifact type`},
		{"no type", "  - command: [c.js]\n", `push_checks[0].type: set it to an artifact type`},
		{"a long type", "  - type: " + strings.Repeat("a", 65) + "\n    command: [c.js]\n", `push_checks[0].type: set it to an artifact type`},
		{"one type twice", "  - type: spec\n    command: [a.js]\n  - type: spec\n    command: [b.js]\n", "push_checks: remove one of the two checks for spec. A module has one check per type"},
		{"no command", "  - type: spec\n", "push_checks[0].command: set it to the program's path inside the module, then its arguments. The list is empty"},
		{"an empty command", "  - type: spec\n    command: []\n", "push_checks[0].command: set it to the program's path inside the module"},
		{"an empty program", "  - type: spec\n    command: [\"\"]\n", `push_checks[0].command: set the first item to a path inside the module. "" is not one`},
		{"an absolute program", "  - type: spec\n    command: [/usr/bin/node]\n", `push_checks[0].command: set the first item to a path inside the module. "/usr/bin/node" is not one`},
		{"a program above the module", "  - type: spec\n    command: [../c.js]\n", `"../c.js" is not one`},
		{"a .. part", "  - type: spec\n    command: [a/../c.js]\n", `"a/../c.js" is not one`},
		{"another key", "  - type: spec\n    command: [c.js]\n    timeout: 5\n", "timeout"},
		{"a command as a string", "  - type: spec\n    command: c.js\n", "cannot unmarshal"},
		{"a number", "  - type: spec\n    command: [c.js, 3]\n", "push_checks[0].command[1]: set it to a string, quoted when it reads as a number or a boolean"},
		{"a list", "  - type: spec\n    command: [[c.js]]\n", "push_checks[0].command[0]: set it to a string"},
		{"a read that is not a type", "  - type: spec\n    command: [c.js]\n    reads: [questions, Spec]\n", `push_checks[0].reads[1]: set it to an artifact type, lowercase letters and digits in groups joined by single hyphens, at most 64 bytes. The value is "Spec"`},
		{"a read as a number", "  - type: spec\n    command: [c.js]\n    reads: [3]\n", `push_checks[0].reads[0]: set it to an artifact type`},
		{"one read twice", "  - type: spec\n    command: [c.js]\n    reads: [questions, questions]\n", "push_checks[0].reads: remove one of the two items questions. A type is listed once"},
	} {
		dir := tree(t, map[string]string{ManifestName: head + c.items})
		_, err := ReadManifest(dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}
