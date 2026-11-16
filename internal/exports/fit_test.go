package exports_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/exports"
)

// TestShortKeepsTheHeadAndTheTailOfALongName is a name of 21 and one of 24 characters,
// which stay whole, and one of 25 and one of 90, which become 20 characters with the
// middle replaced by "…".
func TestShortKeepsTheHeadAndTheTailOfALongName(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"", ""},
		{"core", "core"},
		{strings.Repeat("a", 21), strings.Repeat("a", 21)},
		{strings.Repeat("a", 24), strings.Repeat("a", 24)},
		{"abcdefghijklmnopqrstuvwxy", "abcdefghi…pqrstuvwxy"},
		{"abcdefghi" + strings.Repeat("-", 72) + "jklmnopqrs", "abcdefghi…jklmnopqrs"},
	} {
		got := exports.Short(c.name)
		if got != c.want {
			t.Errorf("Short(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	if got := exports.Short("é" + strings.Repeat("ü", 30) + "ß"); utf8.RuneCountInString(got) != 20 {
		t.Errorf("a name with letters outside ASCII: %q", got)
	}
}

// firstLines are the unindented lines of text.
func firstLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, " ") {
			out = append(out, line)
		}
	}
	return out
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
	for _, line := range firstLines(got) {
		if utf8.RuneCountInString(line) > 100 {
			t.Errorf("a first line of %d characters: %s", utf8.RuneCountInString(line), line)
		}
	}
}

// TestFitShortensADottedRoleAndAQuotedKind is a role of 90 characters with dots in it, as
// in bind.agents.<role>, and a quoted kind value of 90 characters: each first line has at
// most 100 characters, the full-stop of the sentence stays, and the dotted name is one
// name.
func TestFitShortensADottedRoleAndAQuotedKind(t *testing.T) {
	role := "role." + strings.Repeat("r", 40) + "." + strings.Repeat("s", 44)
	kind := "kind" + strings.Repeat("k", 86)
	for name, text := range map[string]string{
		"a dotted role":     "Write one name for bind.agents." + role + " in the base stack and in qory.yaml.\n  The cause.",
		"a quoted kind":     "Use agents, commands, or skills as a kind under bind in qory.yaml, not \"" + kind + "\".\n  The cause.",
		"a role at the end": "Write one name for the role in the base stack of qory.yaml and bind.agents." + role + ".\n  The cause.",
	} {
		got := exports.Fit(text)
		first := firstLines(got)[0]
		if n := utf8.RuneCountInString(first); n > 100 {
			t.Errorf("%s: the first line has %d characters: %s", name, n, first)
		}
		if !strings.HasSuffix(first, ".") && !strings.HasSuffix(first, "\".") {
			t.Errorf("%s: the full stop is gone: %s", name, first)
		}
		if !strings.Contains(got, "The full name of ") || !strings.Contains(got, role) && !strings.Contains(got, kind) {
			t.Errorf("%s: no line has the whole name:\n%s", name, got)
		}
	}
}

// TestFitKeepsMoreCharactersForTwoNamesWithOneForm is two names that share their first 9
// and last 10 characters: each keeps more characters until the two forms differ.
func TestFitKeepsMoreCharactersForTwoNamesWithOneForm(t *testing.T) {
	a := "common-ha" + strings.Repeat("a", 30) + "x" + strings.Repeat("a", 5) + "common-tail"
	b := "common-ha" + strings.Repeat("b", 30) + "y" + strings.Repeat("b", 5) + "common-tail"
	text := "Rename " + a + " or " + b + " in the stack, and the names stay unique.\n  The cause."
	got := exports.Fit(text)
	first := firstLines(got)[0]
	if n := utf8.RuneCountInString(first); n > 100 {
		t.Errorf("the first line has %d characters: %s", n, first)
	}
	forms := strings.Fields(strings.TrimPrefix(strings.SplitN(first, " in the stack", 2)[0], "Rename "))
	if len(forms) != 3 || forms[0] == forms[2] {
		t.Fatalf("want two different forms: %s", first)
	}
	for _, name := range []string{a, b} {
		if !strings.Contains(got, " is "+name+".") {
			t.Errorf("no line has %s whole:\n%s", name, got)
		}
	}
}

// TestFitLeavesAShortEnoughNameWhole is a name of 21 and one of 24 characters in a first
// line over 100 characters: both stay whole, since shortening them saves less than 5.
func TestFitLeavesAShortEnoughNameWhole(t *testing.T) {
	a, b := strings.Repeat("a", 21), strings.Repeat("b", 24)
	text := "Rename " + a + " or " + b + " in a module of the stack, so that each name is unique and fits, and more.\n  The cause."
	if got := exports.Fit(text); got != text {
		t.Errorf("Fit changed names of 21 and 24 characters:\n%s", got)
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
