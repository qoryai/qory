package module

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
)

// Document is a Markdown entry read apart: the frontmatter keys and the body. An agent, a
// command and an output style are all written this way, and a renderer takes the keys a
// runtime reads and the body as the prompt.
type Document struct {
	// Front contains the frontmatter keys as YAML decoded them; nil when the file has
	// none.
	Front map[string]any
	// Body is the Markdown after the frontmatter, with leading blank lines removed.
	Body string
}

// ReadDocument reads the file at path and parses it with [ParseDocument], which reports
// the path in its errors. An error of the file system prints the path as
// [exports.HideError] prints it.
func ReadDocument(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, exports.HideError(err)
	}
	return ParseDocument(data, path)
}

// ParseDocument splits data into frontmatter and body. The name appears in the errors, as
// [exports.HidePath] prints it, and a value the decoder quotes as [exports.Value] prints
// it.
//
// Frontmatter runs from a "---" line at the very start of the file to the next "---"
// line, which may be the very next line or the last line of the file; a fence may have
// trailing spaces. A byte order mark before the first fence is dropped, and CRLF line
// endings read as LF throughout, so a file saved on Windows parses the same. Data that
// does not start with "---" is all body, and [Document.Front] stays nil. An opening fence
// that is never closed is an error, and so is frontmatter YAML that does not decode into
// a mapping.
func ParseDocument(data []byte, name string) (Document, error) {
	const fence = "---"
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	lines := strings.Split(string(data), "\n")
	if lines[0] != fence {
		return Document{Body: string(data)}, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == fence {
			end = i
			break
		}
	}
	if end < 0 {
		return Document{}, fmt.Errorf("%s: frontmatter is not closed", exports.HidePath(name))
	}
	front := map[string]any{}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &front); err != nil {
		return Document{}, fmt.Errorf("%s: frontmatter: %w", exports.HidePath(name), exports.HideValueError(err, ""))
	}
	body := strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n")
	return Document{Front: front, Body: body}, nil
}

// String returns the frontmatter key as a string. A key that is absent, or contains
// anything other than a string, reads as "".
func (d Document) String(key string) string {
	s, _ := d.Front[key].(string)
	return s
}

// SetFront returns data with the top-level frontmatter key set to value, written as a YAML
// scalar: the key's line and every line of its value replaced, or a line added at the
// end of the frontmatter when there is none; data without frontmatter gets one. An empty
// value removes the key and its value instead. A byte order mark is dropped and CRLF line
// endings become LF, as [ParseDocument] reads them. Every other line is kept as written.
//
// The value's lines are the ones from the key's line up to the next top-level key, as
// YAML reads the frontmatter, so a block list written at the key's indentation goes with
// the key; blank and comment lines just before the next key stay. Frontmatter that does
// not parse as a mapping in block style loses the key's line and the indented lines
// that continue it.
func SetFront(data []byte, key, value string) []byte {
	const fence = "---"
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	var line []string
	if value != "" {
		v, err := yaml.Marshal(value)
		if err != nil {
			return data
		}
		line = []string{key + ": " + strings.TrimSuffix(string(v), "\n")}
	}
	lines := strings.Split(string(data), "\n")
	if lines[0] != fence {
		if line == nil {
			return data
		}
		return []byte(fence + "\n" + line[0] + "\n" + fence + "\n" + string(data))
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == fence {
			end = i
			break
		}
	}
	if end < 0 {
		return data
	}
	at, until, ok := keyLines(lines[1:end], key)
	if ok {
		at, until = at+1, until+1
	} else {
		at, until = end, end
		for i := 1; i < end; i++ {
			rest, ok := strings.CutPrefix(lines[i], key+":")
			if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
				continue
			}
			at, until = i, i+1
			for until < end && (strings.HasPrefix(lines[until], " ") || strings.HasPrefix(lines[until], "\t")) {
				until++
			}
			break
		}
	}
	out := append(append(append([]string{}, lines[:at]...), line...), lines[until:]...)
	return []byte(strings.Join(out, "\n"))
}

// frontText is the frontmatter of data as written, without its fences, "" when data has
// none or its frontmatter is not closed. It splits data the way [ParseDocument] does.
func frontText(data []byte) string {
	const fence = "---"
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	lines := strings.Split(string(data), "\n")
	if lines[0] != fence {
		return ""
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == fence {
			return strings.Join(lines[1:i], "\n")
		}
	}
	return ""
}

// keyLines finds the top-level key in front, the frontmatter's lines, as YAML reads them:
// the index of the key's line and the index after the last line of its value, which ends
// before the next top-level key, or at the end, with the blank and comment lines before
// that left out. With no such key, both are len(front). It reports false for frontmatter
// that does not parse as a block mapping, or whose key shares a line with the next.
func keyLines(front []string, key string) (int, int, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(strings.Join(front, "\n")), &doc); err != nil {
		return 0, 0, false
	}
	if len(doc.Content) == 0 {
		return len(front), len(front), true
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode || m.Style&yaml.FlowStyle != 0 {
		return 0, 0, false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		at, until := m.Content[i].Line-1, len(front)
		if i+2 < len(m.Content) {
			until = m.Content[i+2].Line - 1
		}
		if until <= at {
			return 0, 0, false
		}
		for until > at+1 {
			if t := strings.TrimSpace(front[until-1]); t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			until--
		}
		return at, until, true
	}
	return len(front), len(front), true
}
