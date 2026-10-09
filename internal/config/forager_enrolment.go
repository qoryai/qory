package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/stack"
)

// Enrolment is what qory access-key enrol writes into the runner file's server
// section: the server's URL where the section has none, the access key's id, and the
// pin where the section has none. A nil Pin leaves apiary_public_key as it is.
type Enrolment struct {
	URL         string
	AccessKeyID string
	Pin         accesskey.Pin
}

// WriteEnrolment writes an enrolment into the runner file at path, editing its YAML
// tree so its comments, its order and every other key stay as they are: server.url
// when the section has none, server.access_key_id, and server.apiary_public_key when
// e.Pin is set and the section has none. A file that does not exist is created with
// mode 0600 and the apiVersion line; the directory is the caller's to create. The new
// content replaces the file in one rename, in the directory of the file a link names,
// with the mode the file had.
func WriteEnrolment(path string, e Enrolment) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	data, err := os.ReadFile(target)
	mode := os.FileMode(0o600)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		data = nil
	case err != nil:
		return err
	default:
		if info, err := os.Stat(target); err == nil {
			mode = info.Mode().Perm()
		}
	}
	out, err := editEnrolment(data, e)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return replaceFile(target, out, mode)
}

// editEnrolment returns the runner file's content with the enrolment written into it.
func editEnrolment(data []byte, e Enrolment) ([]byte, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
	}
	// A file with no document, empty or comments alone, keeps what it has, and the new
	// document follows it.
	var before []byte
	if doc.Kind == 0 || len(doc.Content) == 0 {
		if t := bytes.TrimRight(data, "\n"); len(t) > 0 {
			before = append(t, '\n')
		}
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setScalar(root, "apiVersion", stack.APIVersion)
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	}
	if doc.Kind != yaml.DocumentNode {
		return nil, errors.New("the file is not a YAML document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("the file is not a mapping of sections")
	}
	server := mappingValue(root, "server")
	if server == nil {
		return nil, errors.New("server is not a mapping")
	}
	if valueOf(server, "url") == nil {
		setScalar(server, "url", e.URL)
	}
	setScalar(server, "access_key_id", e.AccessKeyID)
	if e.Pin != nil && valueOf(server, "apiary_public_key") == nil {
		set(server, "apiary_public_key", pinNode(e.Pin))
	}
	b := bytes.NewBuffer(before)
	enc := yaml.NewEncoder(b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// valueOf returns the value of key in a mapping, nil when the mapping has none or its
// value is null.
func valueOf(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
				return nil
			}
			return v
		}
	}
	return nil
}

// mappingValue returns the mapping under key, adding an empty one when the key is
// absent or null; nil when the value is something else.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	v := valueOf(m, key)
	if v == nil {
		v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		set(m, key, v)
		return v
	}
	if v.Kind != yaml.MappingNode {
		return nil
	}
	return v
}

// set sets key in a mapping to v, in its place when the key is there, else at the end.
func set(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			old := m.Content[i+1]
			v.LineComment, v.HeadComment, v.FootComment = old.LineComment, old.HeadComment, old.FootComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
}

// setScalar sets key in a mapping to a string.
func setScalar(m *yaml.Node, key, value string) {
	set(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// pinNode is a pin as the runner file writes it: one flow mapping per key.
func pinNode(p accesskey.Pin) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, k := range p {
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
		setScalar(entry, "alg", k.Alg)
		setScalar(entry, "public_key", k.PublicKey)
		seq.Content = append(seq.Content, entry)
	}
	return seq
}

// replaceFile replaces the file at path with b in one rename: a new file beside it,
// created with O_EXCL and exactly mode, written and synced, then renamed over it.
func replaceFile(path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	var tmp string
	var f *os.File
	for i := 0; ; i++ {
		tmp = filepath.Join(dir, "."+filepath.Base(path)+"."+strconv.FormatInt(time.Now().UnixNano(), 36)+strconv.Itoa(i))
		var err error
		f, err = os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return fail(err)
	}
	if _, err := f.Write(b); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
