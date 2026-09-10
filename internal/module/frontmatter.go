package module

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document is a Markdown entry read apart: the frontmatter keys and the body. An agent, a
// command and an output style are all written this way, and a renderer takes the keys a
// runtime reads and the body as the prompt.
type Document struct {
	// Front holds the frontmatter keys as YAML decoded them; nil when the file has none.
	Front map[string]any
	// Body is the Markdown after the frontmatter, with leading blank lines removed.
	Body string
}

// ReadDocument reads the file at path and parses it with [ParseDocument], which names the
// path in its errors.
func ReadDocument(path string) (Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Document{}, err
	}
	return ParseDocument(data, path)
}

// ParseDocument splits data into frontmatter and body. The name appears in the errors.
//
// Frontmatter runs from a "---" line at the very start of the file to the next "---"
// line, which may be the very next line or the last line of the file; a fence may carry
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
		return Document{}, fmt.Errorf("%s: frontmatter is not closed", name)
	}
	front := map[string]any{}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &front); err != nil {
		return Document{}, fmt.Errorf("%s: frontmatter: %w", name, err)
	}
	body := strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n")
	return Document{Front: front, Body: body}, nil
}

// String returns the frontmatter key as a string. A key that is absent, or holds anything
// other than a string, reads as "".
func (d Document) String(key string) string {
	s, _ := d.Front[key].(string)
	return s
}
