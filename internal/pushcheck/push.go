package pushcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// errAmbiguous is the error of a push body that JSON readers can read in more than one
// way: a member twice in one object, or a member whose name differs from one of the push
// only in case, which encoding/json would take for it.
var errAmbiguous = errors.New("the push has a member twice, or one whose name differs from a member of the push only in case")

// The members of a push that the check reads, and of each of its files.
var (
	pushMembers = []string{"type", "name", "note", "files"}
	fileMembers = []string{"filename", "content_type", "content_base64"}
)

// readPush reads body, a POST of /artifacts as the run API reads it: its type, and its
// files with their names and decoded contents. A body that is not a push in that form is
// an error, whose text says which part is not; a body whose members can be read in more
// than one way is [errAmbiguous].
func readPush(body []byte) (string, []File, error) {
	top, err := members(body, pushMembers)
	if err != nil {
		return "", nil, err
	}
	var artifactType string
	if err := json.Unmarshal(top["type"], &artifactType); err != nil {
		return "", nil, errors.New("type is missing or not a string")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(top["files"], &items); err != nil {
		return "", nil, errors.New("files is missing or not a list")
	}
	files := make([]File, 0, len(items))
	for i, item := range items {
		m, err := members(item, fileMembers)
		if errors.Is(err, errAmbiguous) {
			return "", nil, err
		}
		if err != nil {
			return "", nil, fmt.Errorf("files[%d]: %w", i, err)
		}
		var f File
		if err := json.Unmarshal(m["filename"], &f.Name); err != nil {
			return "", nil, fmt.Errorf("files[%d].filename is missing or not a string", i)
		}
		if err := json.Unmarshal(m["content_base64"], &f.Content); err != nil {
			return "", nil, fmt.Errorf("files[%d].content_base64 is missing or not base64", i)
		}
		files = append(files, f)
	}
	return artifactType, files, nil
}

// members are the members of the JSON object raw by name. A name twice, or a name that
// differs from one of known only in case, is [errAmbiguous]; a raw that is not one object
// is another error. A member that is absent is nil.
func members(raw []byte, known []string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("it is not a JSON object")
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, notJSON(err)
		}
		name, ok := t.(string)
		if !ok {
			return nil, errors.New("the object has a name that is not a string")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, notJSON(err)
		}
		if _, twice := out[name]; twice {
			return nil, errAmbiguous
		}
		for _, k := range known {
			if name != k && strings.EqualFold(name, k) {
				return nil, errAmbiguous
			}
		}
		out[name] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, notJSON(err)
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("it has more after the JSON object")
	}
	return out, nil
}

// notJSON is the error of a decoder's err: a value that is not JSON.
func notJSON(err error) error {
	return fmt.Errorf("it is not JSON: %w", err)
}
