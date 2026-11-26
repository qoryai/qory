package claude

import (
	"strings"
	"testing"
)

// TestTheFrontTextIsPrintableUTF8 checks the text of a frontmatter directly: a byte that
// is not UTF-8 stops, with its line, also where the parse of the file would not reach the
// check; a carriage return stops unless a line feed or the end of the frontmatter follows
// it; an em dash, a tab and U+FFFD pass.
func TestTheFrontTextIsPrintableUTF8(t *testing.T) {
	for front, want := range map[string]string{
		"a: b\nc: \xff\n":      "Write the frontmatter of the skill s in UTF-8, then run again\n  Line 3 has the byte 0xFF",
		"a: b\r\nc: d\r":       "",
		"a: b\rc: d\n":         "Remove the character U+000D from line 2 of the skill s, then run again",
		"a: \u2014\tb\uFFFD\n": "",
		"\ufeffa: b\n":         "Remove the character U+FEFF from line 2 of the skill s, then run again",
	} {
		err := checkFrontText(front, "skill s")
		got := ""
		if err != nil {
			got = err.Error()
		}
		if !strings.HasPrefix(got, want) || (want == "") != (got == "") {
			t.Errorf("%q: the error is %q, want one that starts with %q", front, got, want)
		}
	}
}

// TestADocumentEndIsThreeDotsAloneOrBeforeABlank is the line that ends a YAML document:
// ... alone, before a space or a tab, with or without a carriage return at the end.
func TestADocumentEndIsThreeDotsAloneOrBeforeABlank(t *testing.T) {
	for line, want := range map[string]bool{
		"...": true, "...\r": true, "... x": true, "...\tx": true, "...\t\r": true,
		"....": false, "...x": false, "..": false, " ...": false, "a ...": false,
	} {
		if got := endsDocument(line); got != want {
			t.Errorf("endsDocument(%q) is %v, want %v", line, got, want)
		}
	}
}
