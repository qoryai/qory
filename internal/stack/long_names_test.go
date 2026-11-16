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
	dotted := "role." + strings.Repeat("r", 40) + "." + strings.Repeat("s", 44)
	kind := "kind-" + strings.Repeat("k", 85)
	const head = "apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: core\n"
	for name, c := range map[string]struct{ body, long string }{
		"a role with a space":          {head + "bind:\n  agents:\n    \"" + role + " x\": c\n", role},
		"a role with a list":           {head + "bind:\n  skills:\n    " + role + ": [a, b]\n", role},
		"a role with no value":         {head + "bind:\n  agents:\n    " + role + ":\n", role},
		"a role with a bad value":      {head + "bind:\n  agents:\n    " + role + ": a b\n", role},
		"a role with an empty list":    {head + "bind:\n  agents:\n    " + role + ": []\n", role},
		"a role with a bad item":       {head + "bind:\n  agents:\n    " + role + ": [a/b]\n", role},
		"a dotted role with no value":  {head + "bind:\n  agents:\n    " + dotted + ":\n", dotted},
		"a dotted role with a bad one": {head + "bind:\n  agents:\n    " + dotted + ": a b\n", dotted},
		"a quoted kind":                {head + "bind:\n  " + kind + ":\n    a: b\n", kind},
		"a module not one segment":     {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "/x\n", module + "/x"},
		"a module excluded":            {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n    source: {path: x}\n    exclude: {skills: [a]}\n    only: {skills: [a]}\n", module},
		"a module listed twice":        {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n  - name: " + module + "\n", module},
		"two modules with one link":    {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n    link: h\n  - name: other\n    link: h\n", module},
		"an exclude over a bad kind":   {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n    exclude: {prompts: [a]}\n", module},
		"a module with a bad link":     {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n    link: a/b\n", module},
		"a module's source stack":      {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n    source: {git: https://h, ref: v1, stack: s}\n", module},
		"a module's ref with no git":   {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n    source: {path: x, ref: v1}\n", module},
		"a module's path off the repo": {"apiVersion: qory.dev/v1alpha1\nmodules:\n  - name: " + module + "\n    source: {git: https://h, ref: v1, path: ../x}\n", module},
		"a runtime listed twice":       {head + "extending:\n  target:\n    runtime: [" + role + ", " + role + "]\n", role},
		"a workflow name not allowed":  {head + "workflows:\n  Wf-" + strings.Repeat("w", 87) + ": {}\n", "Wf-" + strings.Repeat("w", 87)},
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
