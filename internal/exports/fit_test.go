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

// TestFitKeepsTheKeysOfADottedName is a role of 90 characters after bind.agents.,
// provides.skills. and agents.: Fit shortens the role and leaves the keys in front of it,
// and the last line has the role without them.
func TestFitKeepsTheKeysOfADottedName(t *testing.T) {
	role := "role-" + strings.Repeat("r", 85)
	for _, keys := range []string{"bind.agents.", "provides.skills.", "agents.", "roles.commands."} {
		text := "Set " + keys + role + " to the name of one entry, which is a path segment and no more.\n  The cause."
		got := exports.Fit(text)
		want := "Set " + keys + "role-rrrr…rrrrrrrrrr to the name of one entry, which is a path segment and no more.\n  The cause.\n  The full name of role-rrrr…rrrrrrrrrr is " + role + "."
		if got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", keys, got, want)
		}
	}
	// A key path with a name of 24 characters or fewer stays whole.
	text := "Set bind.agents." + strings.Repeat("a", 24) + " to the name of one entry, which is a path segment and no more, and more, and more.\n  The cause."
	if got := exports.Fit(text); got != text {
		t.Errorf("a name of 24 characters after keys:\n%s", got)
	}
}

// TestFitTakesAQuotedValueWithSpacesAsOneName is a quoted role of 90 characters with a
// space in it: the whole text between the quotes is one name, shortened with its
// quotes, and the last line has it whole.
func TestFitTakesAQuotedValueWithSpacesAsOneName(t *testing.T) {
	role := `"role-` + strings.Repeat("r", 80) + ` x y"`
	text := "Rename role " + role + " of bind.agents to one path segment, and keep it as a segment.\n  The cause."
	got := exports.Fit(text)
	first := firstLines(got)[0]
	if !strings.HasPrefix(first, `Rename role "role-rrr`) || !strings.Contains(first, ` x y" of bind.agents`) || strings.Count(first, "…") != 1 {
		t.Errorf("the quoted name is not shortened whole:\n%s", got)
	}
	if !strings.HasSuffix(got, " is "+role+".") {
		t.Errorf("no last line has the whole name:\n%s", got)
	}
}

// TestFitLeavesTwoShortQuotedValuesAlone is a first line over 100 characters with two short
// quoted values and a long text between them: the text between the closing quote of the
// first and the opening quote of the second is no name, so the line stays as it is.
func TestFitLeavesTwoShortQuotedValuesAlone(t *testing.T) {
	text := `Set "a" to one of the listed values in the file, and the value "b" is not one of them, and no other value is allowed there.` + "\n  The cause."
	if utf8.RuneCountInString(strings.SplitN(text, "\n", 2)[0]) <= 100 {
		t.Fatal("the first line is not over 100 characters")
	}
	if got := exports.Fit(text); got != text {
		t.Errorf("Fit changed the text between two short quoted values:\n%s", got)
	}
}

// TestFitMatchesTheWholeNameOnALaterLine is a later line with a longer name that has the
// shortened name inside it: that is not the whole name, so the last line is added. A name
// that a later line has whole, before a full stop, adds none.
func TestFitMatchesTheWholeNameOnALaterLine(t *testing.T) {
	role := "role-" + strings.Repeat("r", 85)
	first := "Remove agents/" + role + " from the exclude of the module, and keep it in one module only.\n"
	if got := exports.Fit(first + "  The cause is " + role + "x."); !strings.Contains(got, "The full name of role-rrrr…rrrrrrrrrr is "+role+".") {
		t.Errorf("a longer name on a later line counted:\n%s", got)
	}
	if got := exports.Fit(first + "  The cause is " + role + "."); strings.Contains(got, "The full name of") {
		t.Errorf("the whole name on a later line was not found:\n%s", got)
	}
}

// TestFitCountsAQuotedValueWithoutItsQuotes is a quoted value of 24 characters between its
// quotes, which stays whole, and one of 25, which is shortened.
func TestFitCountsAQuotedValueWithoutItsQuotes(t *testing.T) {
	pad := " is not one of the values that the file may set here, and nothing else is allowed.\n  The cause."
	a := `Set "` + strings.Repeat("a", 24) + `"` + pad
	if got := exports.Fit(a); got != a {
		t.Errorf("a quoted value of 24 characters changed:\n%s", got)
	}
	b := `Set "` + strings.Repeat("b", 25) + `"` + pad
	if got := exports.Fit(b); !strings.Contains(got, "…") {
		t.Errorf("a quoted value of 25 characters stayed whole:\n%s", got)
	}
}

// TestFitKeepsTheKeysOfAGatewayName is a first line over 100 characters with a name of 64
// characters under a key of forager.yaml: the keys stay whole and the name is shortened.
func TestFitKeepsTheKeysOfAGatewayName(t *testing.T) {
	name := "n" + strings.Repeat("x", 63)
	for _, key := range []string{"gateway.credentials.", "gateway.tools.", "gateway.integrations.", "gateway.egress.paths.", "wall.images.", "harness.launch.", "harness.tiers."} {
		text := "Set " + key + name + ".file to an absolute path, then say it is so, again and again, at length.\n  The value is not one."
		got := exports.Fit(text)
		if first := strings.SplitN(got, "\n", 2)[0]; !strings.Contains(first, key+name[:9]) || strings.Contains(first, name) {
			t.Errorf("%s: %q, want the key whole and the name shortened", key, first)
		}
	}
}

// TestFitMovesAHiddenPhraseOutOfALongFirstLine is a first line over 100 characters with
// the phrase of a name that contains an access key secret, which no shortening cuts: the
// phrase moves to a later line, and a line over 100 characters is the only one that changes.
func TestFitMovesAHiddenPhraseOutOfALongFirstLine(t *testing.T) {
	const phrase = "(a name that contains an access key secret)"
	long := "Remove gateway.integrations." + phrase + ", or give it a program that defines a credential."
	got := exports.Fit(long + "\n  A cause.")
	lines := strings.Split(got, "\n")
	if n := utf8.RuneCountInString(lines[0]); n > 100 || strings.Contains(lines[0], phrase) {
		t.Errorf("line 1 has %d characters and the phrase: %s", n, lines[0])
	}
	if !strings.Contains(got, "The hidden name is "+phrase+".") || !strings.Contains(got, "A cause.") {
		t.Errorf("the refusal lost the phrase or its cause: %q", got)
	}
	short := "Remove " + phrase + " from the module."
	if got := exports.Fit(short); got != short {
		t.Errorf("a short line changed: %q", got)
	}
}
