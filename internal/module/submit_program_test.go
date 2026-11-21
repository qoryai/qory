package module

import (
	"reflect"
	"strings"
	"testing"
)

// TestReadManifestReadsSubmitProgram reads the submit_program key with the program's path
// cleaned and the arguments as written, and no key as nil.
func TestReadManifestReadsSubmitProgram(t *testing.T) {
	dir := tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: forge\n" +
		"submit_program:\n  command: [./bin/open-draft, --strict]\n"})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := &SubmitProgram{Command: []string{"bin/open-draft", "--strict"}}
	if !reflect.DeepEqual(m.SubmitProgram, want) {
		t.Errorf("submit program %v, want %v", m.SubmitProgram, want)
	}
	dir = tree(t, map[string]string{ManifestName: "apiVersion: qory.dev/v1alpha1\nname: forge\n"})
	if m, err = ReadManifest(dir); err != nil || m.SubmitProgram != nil {
		t.Errorf("no submit_program read as %v, %v", m.SubmitProgram, err)
	}
}

// TestReadManifestFailsOnSubmitProgram fails on an empty command, a word that is not a
// string, a program outside the module and a key the item does not have.
func TestReadManifestFailsOnSubmitProgram(t *testing.T) {
	head := "apiVersion: qory.dev/v1alpha1\nname: forge\nsubmit_program:\n"
	for _, c := range []struct{ name, item, want string }{
		{"no command", "  {}\n", "Set submit_program.command to the program's path inside the module, then its arguments.\n  The list is empty."},
		{"an empty command", "  command: []\n", "Set submit_program.command to the program's path inside the module"},
		{"an empty program", "  command: [\"\"]\n", "Set the first item of submit_program.command to a path inside the module.\n  \"\" is not one."},
		{"an absolute program", "  command: [/usr/bin/node]\n", "Set the first item of submit_program.command to a path inside the module."},
		{"a program above the module", "  command: [../c.js]\n", `"../c.js" is not one`},
		{"a .. part", "  command: [a/../c.js]\n", `"a/../c.js" is not one`},
		{"a number", "  command: [c.js, 3]\n", "Set submit_program.command[1] to a string, quoted when it reads as a number or a boolean."},
		{"another key", "  command: [c.js]\n  timeout: 5\n", "timeout"},
		{"a command as a string", "  command: c.js\n", "cannot unmarshal"},
	} {
		dir := tree(t, map[string]string{ManifestName: head + c.item})
		_, err := ReadManifest(dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want it to contain %q", c.name, err, c.want)
		}
	}
}
