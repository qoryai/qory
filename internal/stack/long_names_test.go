package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRefusalsKeepTheFixLineShortForLongNames is each refusal of a stack file that prints
// a role name or a module name, with a name of 90 characters: the first line has at most
// 100 characters and the name shortened, and a later line has the whole name.
func TestRefusalsKeepTheFixLineShortForLongNames(t *testing.T) {
	isolate(t)
	role := "role-" + strings.Repeat("r", 85)
	module := "mod-" + strings.Repeat("m", 86)
	const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n"
	for name, c := range map[string]struct{ body, long string }{
		"a role with a space":         {head + "bind:\n  agents:\n    \"" + role + " x\": c\n", role},
		"a role with a list":          {head + "bind:\n  skills:\n    " + role + ": [a, b]\n", role},
		"a role with no value":        {head + "bind:\n  agents:\n    " + role + ":\n", role},
		"a role with a bad value":     {head + "bind:\n  agents:\n    " + role + ": a b\n", role},
		"a role with an empty list":   {head + "bind:\n  agents:\n    " + role + ": []\n", role},
		"a role with a bad item":      {head + "bind:\n  agents:\n    " + role + ": [a/b]\n", role},
		"a module not one segment":    {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "/x\n", module + "/x"},
		"a module excluded":           {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n    source: {path: x}\n    exclude: {skills: [a]}\n    only: {skills: [a]}\n", module},
		"a workflow name not allowed": {head + "workflows:\n  Wf-" + strings.Repeat("w", 87) + ": {}\n", "Wf-" + strings.Repeat("w", 87)},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, FileName)
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Errorf("%s: no refusal", name)
			continue
		}
		lines := strings.Split(err.Error(), "\n")
		if n := utf8.RuneCountInString(lines[0]); n > 100 {
			t.Errorf("%s: the first line has %d characters: %s", name, n, lines[0])
		}
		if strings.Contains(lines[0], c.long) {
			t.Errorf("%s: the first line has the whole name: %s", name, lines[0])
		}
		if !strings.Contains(strings.Join(lines[1:], "\n"), c.long) {
			t.Errorf("%s: no later line has the whole name: %v", name, err)
		}
	}
}
