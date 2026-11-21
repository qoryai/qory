// Package primitives has the character rule of a line, a text and a title, and a copy of
// contracts/harness/v1/primitives.schema.json, the schema that defines them. The package
// imports nothing from qory, so every package that checks a string can use it.
package primitives

import (
	_ "embed"
	"unicode"
)

// URL is the $id of the primitives schema: a schema of an artifact type refers to the
// definitions in it by this address, such as URL + "#/$defs/title".
const URL = "https://qory.dev/contracts/harness/v1/primitives.schema.json"

// Schema is a copy of contracts/harness/v1/primitives.schema.json, byte for byte, which a
// test checks: go:embed does not reach a file above the package.
//
//go:embed primitives.schema.json
var Schema []byte

// StopsLine reports whether a line has no room for r: a control character (Unicode
// category Cc), U+2028, U+2029, or a Cf character other than U+200C and U+200D, the joiners
// that emoji sequences and some scripts need. The tag characters and the bidirectional
// controls are Cf.
func StopsLine(r rune) bool {
	if r == '\u200c' || r == '\u200d' {
		return false
	}
	return unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
}

// StopsText reports whether a text has no room for r: the characters that [StopsLine]
// stops, except tab, line feed and carriage return.
func StopsText(r rune) bool {
	return r != '\t' && r != '\n' && r != '\r' && StopsLine(r)
}

// ShowsInTitle reports whether r may be in a title and shows: [StopsLine] passes it, and
// it is neither a space (U+0020, U+00A0, U+1680, U+2000 to U+200A, U+202F, U+205F,
// U+3000) nor U+200C or U+200D. A title needs at least one such character.
func ShowsInTitle(r rune) bool {
	return !StopsLine(r) && !unicode.IsSpace(r) && r != '\u200c' && r != '\u200d'
}
