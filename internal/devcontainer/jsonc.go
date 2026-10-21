package devcontainer

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
)

// bom is the byte order mark of UTF-8, which an editor may write first.
var bom = []byte("\xef\xbb\xbf")

// strip is data, JSON with comments, as JSON: a byte order mark first, each // comment
// up to the first line feed or carriage return after it, each /* */ comment but its
// line feeds and carriage returns, and each comma after an object's last member or an
// array's last entry written as spaces, and every other byte where it was. A carriage
// return alone ends a // comment, as it ends one for the Development Containers CLI's
// reader, so no member that reader reads is hidden in a comment here. So an offset into the result is one into data, and the line and
// column of a JSON reader's error are those of data. A comma counts as trailing only
// after a value: in [,] or {,} or after another comma it is kept, for the JSON reader
// to refuse. strip returns false when a /* comment is never closed.
func strip(data []byte) ([]byte, bool) {
	out := slices.Clone(data)
	if bytes.HasPrefix(out, bom) {
		copy(out, "   ")
	}
	inString := false
	var prev byte // the last byte of JSON outside a string, 0 at the start
	comma := -1   // where a comma that may be trailing is
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inString {
			switch c {
			case '\\':
				i++ // the escaped byte, which never ends the string
			case '"':
				inString = false
			}
			continue
		}
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			continue
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			for ; i < len(out) && out[i] != '\n' && out[i] != '\r'; i++ {
				out[i] = ' '
			}
			continue
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			end := bytes.Index(out[i+2:], []byte("*/"))
			if end < 0 {
				return nil, false
			}
			last := i + 2 + end + 2
			for j := i; j < last; j++ {
				if out[j] != '\n' && out[j] != '\r' {
					out[j] = ' '
				}
			}
			i = last - 1
			continue
		case c == ',':
			comma = -1
			if prev != 0 && prev != '[' && prev != '{' && prev != ',' {
				comma = i
			}
			prev = c
			continue
		case (c == '}' || c == ']') && comma >= 0:
			out[comma] = ' '
		case c == '"':
			inString = true
		}
		comma = -1
		prev = c
	}
	return out, true
}

// member is one member of an object: its name and its value as written.
type member struct {
	name  string
	value json.RawMessage
}

// members are the members of raw, one JSON object, in the order written.
func members(raw []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, errors.New("not an object")
	}
	var out []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, member{tok.(string), value})
	}
	return out, nil
}

// scan refuses raw, the configuration at path as one JSON document of valid UTF-8,
// when it holds a \u escape of a lone surrogate in a string or a member name, or an
// object that names a member twice, the names compared as they read; or returns nil.
// Go's JSON reader takes all of these, reading the first as U+FFFD and keeping the
// last of a repeated member. The first of these in the document is the one refused.
// scan reads raw once, token by token: the raw text of each string token is what lies
// between the decoder's offset before the token and after it, a colon, a comma and
// space at most before the string, and that text is scanned once for its escapes. The
// decoder reads the escapes itself, and reports none it replaced, so the raw text is
// the only place a lone surrogate shows. Each open object holds the set
// of the names it has read, dropped when it closes.
func scan(path string, raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var open []*container
	for {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf(textNotObject, path)
		}
		var top *container
		if len(open) > 0 {
			top = open[len(open)-1]
		}
		if d, ok := tok.(json.Delim); ok && (d == '}' || d == ']') {
			open = open[:len(open)-1]
			if len(open) > 0 && open[len(open)-1].object {
				open[len(open)-1].named = false
			}
			continue
		}
		name := top != nil && top.object && !top.named
		if top != nil && !top.object {
			top.index++
		}
		switch t := tok.(type) {
		case json.Delim:
			c := &container{object: t == '{', index: -1}
			if c.object {
				c.names = map[string]struct{}{}
			}
			open = append(open, c)
			continue
		case string:
			lone := loneSurrogate(raw[start:dec.InputOffset()])
			switch {
			case name && lone:
				return fmt.Errorf(textSurrogateName, path, integration.Printable(exports.HidePath(cmp.Or(pointer(open, false)+"/"+pointerToken(t), "/"))))
			case lone:
				return fmt.Errorf(textSurrogateValue, path, integration.Printable(exports.HidePath(cmp.Or(pointer(open, true), "/"))))
			case name:
				if _, twice := top.names[t]; twice {
					return fmt.Errorf(textRepeated, path, integration.Printable(exports.HidePath(cmp.Or(pointer(open, false), "/"))), integration.Printable(accesskey.ShownName(t, false)))
				}
				top.names[t] = struct{}{}
				top.name, top.named = t, true
				continue
			}
		}
		// A value other than an array or an object ends here.
		if top != nil && top.object {
			top.named = false
		}
	}
}

// container is an array or an object [scan] is reading: an array's entry being read,
// or an object's member, whether its name is read, and the names read before it.
type container struct {
	object bool
	index  int
	name   string
	named  bool
	names  map[string]struct{}
}

// pointer is the JSON pointer of the value [scan] is reading inside open, or, without
// last, of the innermost container itself.
func pointer(open []*container, last bool) string {
	if !last {
		open = open[:len(open)-1]
	}
	var at strings.Builder
	for _, c := range open {
		if c.object {
			at.WriteString("/" + pointerToken(c.name))
		} else {
			at.WriteString("/" + strconv.Itoa(c.index))
		}
	}
	return at.String()
}

// pointerToken is a name as one token of a JSON pointer, ~ and / escaped.
func pointerToken(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
}

// loneSurrogate reports whether text, a JSON string token as it was written, with at
// most a colon, a comma and space before it, holds a \u escape of a surrogate that is
// not a pair: a high surrogate, D800 to DBFF, not followed at once by an escaped low
// one, DC00 to DFFF, or a low one standing alone. Every other escape is two bytes, so
// a backslash read as an escape's first byte is never mistaken for one.
func loneSurrogate(text []byte) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' {
			continue
		}
		r, ok := escaped(text[i:])
		if !ok {
			i++ // the escaped character
			continue
		}
		switch {
		case r >= 0xDC00 && r <= 0xDFFF:
			return true
		case r >= 0xD800 && r <= 0xDBFF:
			low, ok := escaped(text[i+6:])
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return true
			}
			i += 11
		default:
			i += 5
		}
	}
	return false
}

// escaped is the code unit of the \u escape text starts with, and false when it starts
// with none.
func escaped(text []byte) (rune, bool) {
	if len(text) < 6 || text[0] != '\\' || text[1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(string(text[2:6]), 16, 16)
	return rune(n), err == nil
}
