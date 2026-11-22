// Package sourcetext writes the hidden characters of source text where a person sees them.
//
// qory hands source text (work items, run inputs, answers of the run API) to an AI agent.
// A hidden character in that text can mislead the agent: U+202E (right-to-left override)
// reorders the text that follows, and the tag characters U+E0001 and U+E0020 to U+E007F
// spell words that no person sees. [Escape], [EscapeJSON] and [EscapeJSONFile] write each
// hidden character as `<U+XXXX>`, so the agent reads the same text a person reads.
// [Hidden] is the one definition of which characters are hidden.
package sourcetext

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/primitives"
)

// Hidden reports whether r is a character that source text must show as `<U+XXXX>`:
// [primitives.StopsLine] except tab (U+0009) and line feed (U+000A), so the rule of the
// characters is written once.
//
// These are hidden: each control character (Unicode class Cc) except newline (U+000A)
// and tab (U+0009), so the carriage return, NUL, DEL and U+0085 are hidden; each format
// character (class Cf) except the zero width non-joiner (U+200C) and the zero width
// joiner (U+200D), which spell some scripts and emoji; and the line separator U+2028
// and the paragraph separator U+2029. Every other rune is not hidden.
func Hidden(r rune) bool {
	return r != '\n' && r != '\t' && primitives.StopsLine(r)
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
// `\u202e`, or as a surrogate pair, such as `\udb40\udc41` (U+E0041), is caught.
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
	out, _, err := escapeJSON(doc, false)

	return out, err
}

// bom is the UTF-8 byte order mark, which some tools write at the start of a file.
const bom = "\xEF\xBB\xBF"

// EscapeJSONFile is [EscapeJSON] for the content of a stored file, which a tool reads
// as JSON text although it is not strict.
//
// It keeps a byte order mark (U+FEFF) at the start, and it accepts invalid UTF-8 and an
// unpaired surrogate escape such as `\ud800` in a string. A string with no hidden rune
// keeps its bytes. A string with one is written again, and each invalid sequence in it
// becomes U+FFFD. EscapeJSONFile returns an error when the rest is not one JSON value.
func EscapeJSONFile(doc []byte) ([]byte, error) {
	rest := bytes.TrimPrefix(doc, []byte(bom))
	out, changed, err := escapeJSON(rest, true)
	if err != nil || !changed {
		return doc, err
	}

	return append(append([]byte(nil), doc[:len(doc)-len(rest)]...), out...), nil
}

// escapeJSON is the one reader of [EscapeJSON] and [EscapeJSONFile]. With lenient, a
// string may have invalid UTF-8 and unpaired surrogate escapes. It returns doc itself,
// and false, when no string changes.
func escapeJSON(doc []byte, lenient bool) ([]byte, bool, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(doc),
		jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(lenient))
	var out []byte // doc with each changed string replaced; nil until one changes
	copied := 0    // doc[:copied] is already in out
	for {
		var changed []byte
		var start, end int
		if dec.PeekKind() == '"' {
			v, err := dec.ReadValue()
			if err != nil {
				return nil, false, err
			}
			end = int(dec.InputOffset())
			start = end - len(v)
			decoded, err := unquote(v, lenient)
			if err != nil {
				return nil, false, err
			}
			// An invalid sequence is U+FFFD in decoded, which is not hidden, so a string
			// with no hidden rune keeps its bytes.
			if strings.IndexFunc(decoded, Hidden) >= 0 {
				if changed, err = jsontext.AppendQuote(nil, Escape(decoded)); err != nil {
					return nil, false, err
				}
			}
		} else if _, err := dec.ReadToken(); err != nil {
			return nil, false, err
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
		return nil, false, err
	}
	if out == nil {
		return doc, false, nil
	}
	return append(out, doc[copied:]...), true, nil
}

// unquote returns the text of the JSON string v. [jsontext.AppendUnquote] is strict, so
// with lenient the text is read as a token, which turns each invalid sequence into U+FFFD.
func unquote(v jsontext.Value, lenient bool) (string, error) {
	if !lenient {
		decoded, err := jsontext.AppendUnquote(nil, v)

		return string(decoded), err
	}
	dec := jsontext.NewDecoder(bytes.NewReader(v), jsontext.AllowInvalidUTF8(true))
	tok, err := dec.ReadToken()
	if err != nil {
		return "", err
	}

	return tok.String(), nil
}
