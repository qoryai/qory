package exports_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/exports"
)

// TestShortKeepsTheHeadAndTheTailOfALongName is a name of 20 characters, which stays, and
// one of 21 and one of 90, which become 20 characters with the middle replaced by "…".
func TestShortKeepsTheHeadAndTheTailOfALongName(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"", ""},
		{"core", "core"},
		{strings.Repeat("a", 20), strings.Repeat("a", 20)},
		{"abcdefghi" + strings.Repeat("-", 72) + "jklmnopqrs", "abcdefghi…jklmnopqrs"},
		{"abcdefghijklmnopqrstu", "abcdefghi…lmnopqrstu"},
	} {
		got := exports.Short(c.name)
		if got != c.want {
			t.Errorf("Short(%q) = %q, want %q", c.name, got, c.want)
		}
		if n := utf8.RuneCountInString(got); n > exports.NameMax {
			t.Errorf("Short(%q) has %d characters", c.name, n)
		}
	}
	if got := exports.Short("é" + strings.Repeat("ü", 30) + "ß"); utf8.RuneCountInString(got) != exports.NameMax {
		t.Errorf("a name with letters outside ASCII: %q", got)
	}
}

// TestFitShortensTheNamesOfAFirstLineOverTheLimit is a refusal whose first line passes 100
// characters with long names, and one that does not: the first shortens the names and has
// a last line with each whole name that no later line has, and the second stays as it is.
func TestFitShortensTheNamesOfAFirstLineOverTheLimit(t *testing.T) {
	role := "role-" + strings.Repeat("r", 85)
	module := "mod-" + strings.Repeat("m", 86)
	fits := "Keep skills/" + strings.Repeat("s", 40) + " in module core.\n  The cause."
	if got := exports.Fit(fits); got != fits {
		t.Errorf("a first line within 100 characters: %q", got)
	}
	text := "Remove agents/" + role + " from the exclude of module " + module + ".\n  The cause is the role " + role + "."
	want := "Remove agents/role-rrrr…rrrrrrrrrr from the exclude of module mod-mmmmm…mmmmmmmmmm.\n" +
		"  The cause is the role " + role + ".\n" +
		"  The full name of mod-mmmmm…mmmmmmmmmm is " + module + "."
	if got := exports.Fit(text); got != want {
		t.Errorf("Fit:\n%s\nwant:\n%s", got, want)
	}
	// Each refusal of several is fitted on its own.
	both := text + "\nRemove agents/" + role + " from the exclude of module " + module + ".\n  The cause."
	got := exports.Fit(both)
	if n := strings.Count(got, "The full name of"); n != 3 {
		t.Errorf("two refusals: %d full-name lines, want 3:\n%s", n, got)
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, " ") && utf8.RuneCountInString(line) > 100 {
			t.Errorf("a first line of %d characters: %s", utf8.RuneCountInString(line), line)
		}
	}
}

// TestARefusalFitsItsModuleName is a refusal with ModulePlace, in a module whose name has
// 90 characters: the first line has at most 100 characters, and a later line has the
// whole name.
func TestARefusalFitsItsModuleName(t *testing.T) {
	name := "mod-" + strings.Repeat("m", 86)
	module := "module " + name
	r := exports.Refuse("Write the MCP server file of "+exports.ModulePlace+" as one JSON object, and keep it small.", "The file is x.json.")
	err := exports.Prefixed(module, r).Error()
	lines := strings.Split(err, "\n")
	if utf8.RuneCountInString(lines[0]) > 100 || strings.Contains(lines[0], module) || !strings.Contains(strings.Join(lines[1:], "\n"), name) {
		t.Errorf("%s", err)
	}
}
