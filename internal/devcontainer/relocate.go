package devcontainer

import (
	"bytes"
	"encoding/json"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/exports"
)

// Has reports whether the configuration has the member name at its top, whatever its
// value, null included. It is false for a configuration [Parse] did not read.
func (c *Config) Has(name string) bool {
	top, err := members(c.clean)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(top, func(m member) bool { return m.name == name })
}

// Relocate is the configuration, read from the repository at root, as JSON for a file
// in the folder dir, an absolute path, in place of [Config.Path]: the text [Parse]
// read, its comments left out, with each member of its top named in drop left out.
// Three kinds of path are written relative to dir, so that each names the same place:
// build.dockerfile and build.context, which the reference implementation resolves with
// path.posix.resolve from the configuration's folder
// (spec-configuration/configuration.ts, resolveConfigFilePath), and the ID of a local
// Feature, in features and in overrideFeatureInstallOrder, one that starts with ./ or
// ../, which it joins to that folder with path.join
// (spec-configuration/containerFeaturesConfiguration.ts, getFeatureIdType and
// processFeatureIdentifier). Each is taken as written, never through a link, so a path
// written relative to dir resolves to the same one. An absolute path, which names the
// same place from any folder, an empty one and a value that is not a string are left as
// they are, and so is every other member, as written.
//
// The reference implementation resolves the earlier dockerFile and context, and
// dockerComposeFile, from the configuration's folder too. Relocate leaves them as
// written, so in the copy they would name other places: a caller refuses a
// configuration that has them first. qory run prebuild refuses dockerComposeFile
// whenever the configuration has it, and dockerFile and context whenever they are set.
func (c *Config) Relocate(root, dir string, drop ...string) ([]byte, error) {
	top, err := members(c.clean)
	if err != nil {
		return nil, errors.New("the configuration was not read")
	}
	from := filepath.Join(root, filepath.FromSlash(path.Dir(c.Path)))
	move := func(p string) (string, error) {
		rel, err := filepath.Rel(dir, filepath.Join(from, filepath.FromSlash(p)))
		if err != nil {
			return "", exports.HideError(err)
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(p, "/") && !strings.HasSuffix(rel, "/") {
			rel += "/"
		}
		return rel, nil
	}
	// local is a local Feature's ID moved: it starts with ./ or ../ again, which is how
	// the reference implementation tells one.
	local := func(id string) (string, bool, error) {
		if !strings.HasPrefix(id, "./") && !strings.HasPrefix(id, "../") {
			return id, false, nil
		}
		rel, err := move(id)
		if err != nil {
			return "", false, err
		}
		if !strings.HasPrefix(rel, "../") {
			rel = "./" + rel
		}
		return rel, true, nil
	}
	var out []member
	for _, m := range top {
		if slices.Contains(drop, m.name) {
			continue
		}
		switch m.name {
		case "build":
			m.value, err = editObject(m.value, func(name string, value json.RawMessage) (string, json.RawMessage, error) {
				if name != "dockerfile" && name != "context" {
					return name, value, nil
				}
				var p string
				if json.Unmarshal(value, &p) != nil || p == "" || path.IsAbs(p) {
					return name, value, nil
				}
				moved, err := move(p)
				if err != nil {
					return "", nil, err
				}
				encoded, err := encode(moved)
				return name, encoded, err
			})
		case "features":
			m.value, err = editObject(m.value, func(name string, value json.RawMessage) (string, json.RawMessage, error) {
				id, _, err := local(name)
				return id, value, err
			})
		case "overrideFeatureInstallOrder":
			m.value, err = editArray(m.value, func(value json.RawMessage) (json.RawMessage, error) {
				var id string
				if json.Unmarshal(value, &id) != nil {
					return value, nil
				}
				moved, ok, err := local(id)
				if err != nil || !ok {
					return value, err
				}
				return encode(moved)
			})
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return writeObject(out)
}

// editObject is raw, an object, with each member as edit has it, in the order
// written; a value that is not an object is left as it is.
func editObject(raw json.RawMessage, edit func(string, json.RawMessage) (string, json.RawMessage, error)) (json.RawMessage, error) {
	if len(raw) == 0 || raw[0] != '{' {
		return raw, nil
	}
	ms, err := members(raw)
	if err != nil {
		return nil, err
	}
	for i, m := range ms {
		if ms[i].name, ms[i].value, err = edit(m.name, m.value); err != nil {
			return nil, err
		}
	}
	return writeObject(ms)
}

// editArray is raw, an array, with each entry as edit has it; a value that is not an
// array is left as it is.
func editArray(raw json.RawMessage, edit func(json.RawMessage) (json.RawMessage, error)) (json.RawMessage, error) {
	if len(raw) == 0 || raw[0] != '[' {
		return raw, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, e := range entries {
		e, err := edit(e)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(e)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

// writeObject is ms as one object, in their order, each value as it is.
func writeObject(ms []member) (json.RawMessage, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := encode(m.name)
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(m.value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// encode is s as a JSON string, <, > and & as they are.
func encode(s string) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
