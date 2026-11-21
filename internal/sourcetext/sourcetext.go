// Package sourcetext writes the hidden characters of source text where a person sees them.
//
// qory hands source text (work items, run inputs, answers of the run API) to an AI agent.
// A hidden character in that text can mislead the agent: U+202E (right-to-left override)
// reorders the text that follows, and the tag characters U+E0000 to U+E007F spell words
// that no person sees. [Escape] and [EscapeJSON] write each hidden character as
// `<U+XXXX>`, so the agent reads the same text a person reads. [Hidden] is the one
// definition of which characters are hidden.
package sourcetext

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Hidden reports whether r is a character that source text must show as `<U+XXXX>`.
//
// These are hidden: each control character (Unicode class Cc) except newline (U+000A)
// and tab (U+0009), so the carriage return, NUL, DEL and U+0085 are hidden; each format
// character (class Cf) except the zero width non-joiner (U+200C) and the zero width
// joiner (U+200D), which spell some scripts and emoji; and the line separator U+2028
// and the paragraph separator U+2029. Every other rune is not hidden.
func Hidden(r rune) bool {
	switch r {
	case '\n', '\t', 0x200C, 0x200D:
		return false
	case 0x2028, 0x2029:
		return true
	}
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

// Escape returns s with each hidden rune (see [Hidden]) written as `<U+XXXX>`: the
// code point in at least four upper-case hexadecimal digits, as in `<U+E0041>`.
//
// Escape changes nothing else. A literal `<U+0041>` in s stays as it is, so the output
// of two inputs can be the same text. A byte that is not valid UTF-8 stays as it is,
// because it is not a rune and no `<U+XXXX>` form names it. Escape returns s itself when
// no rune is hidden.
func Escape(s string) string {
	first := strings.IndexFunc(s, Hidden)
	if first < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	b.WriteString(s[:first])
	for i := first; i < len(s); {
		// An invalid byte decodes as U+FFFD with size 1, and is written as it is.
		r, size := utf8.DecodeRuneInString(s[i:])
		if Hidden(r) {
			fmt.Fprintf(&b, "<U+%04X>", r)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// EscapeJSON returns doc with [Escape] applied to each string value and each member name,
// read after JSON decoding. A hidden character written as a JSON escape, such as
// `‮`, or as a surrogate pair, such as `󠁁` (U+E0041), is caught.
//
// A string that has no hidden rune keeps its bytes. A string that has one is written
// again, with the minimal JSON escapes of RFC 8785 and no HTML escaping of `<`, `>` and
// `&`. Whitespace between tokens, member order and the bytes of numbers stay as they are,
// so a carriage return between tokens stays a carriage return. EscapeJSON returns doc
// itself when nothing is hidden.
//
// EscapeJSON returns an error when doc is not one JSON value (RFC 8259) or has invalid
// UTF-8 or an unpaired surrogate escape in a string. Duplicate member names are allowed,
// and two names that differ only by a hidden character become the same name.
func EscapeJSON(doc []byte) ([]byte, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(doc), jsontext.AllowDuplicateNames(true))
	var out []byte // doc with each changed string replaced; nil until one changes
	copied := 0    // doc[:copied] is already in out
	for {
		var changed []byte
		var start, end int
		if dec.PeekKind() == '"' {
			v, err := dec.ReadValue()
			if err != nil {
				return nil, err
			}
			end = int(dec.InputOffset())
			start = end - len(v)
			decoded, err := jsontext.AppendUnquote(nil, v)
			if err != nil {
				return nil, err
			}
			if escaped := Escape(string(decoded)); escaped != string(decoded) {
				if changed, err = jsontext.AppendQuote(nil, escaped); err != nil {
					return nil, err
				}
			}
		} else if _, err := dec.ReadToken(); err != nil {
			return nil, err
		}
		if changed != nil {
			out = append(out, doc[copied:start]...)
			out = append(out, changed...)
			copied = end
		}
		if dec.StackDepth() == 0 {
			break
		}
	}
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("sourcetext: more than one JSON value in the document")
		}
		return nil, err
	}
	if out == nil {
		return doc, nil
	}
	return append(out, doc[copied:]...), nil
}
