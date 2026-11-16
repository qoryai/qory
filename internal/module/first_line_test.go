package module

import (
	"strings"
	"testing"
	"unicode/utf8"
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
