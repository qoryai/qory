package runcopy

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/acting"
	"github.com/qoryai/qory/internal/exports"
)

// maxQoryFiles is the most qory files read at one commit or in one folder: the files
// at the top and the stack files they extend by a path, one after another.
const maxQoryFiles = 32

// maxQoryFile is the largest qory file, in bytes, whose acting keys are read. A blob
// from the run can inflate far past its size in the bundle; a larger file counts as a
// change to the file.
const maxQoryFile = 1 << 20

// ActingChange is an acting key of a qory file that the run changes: set, set to
// another value, or no longer set.
type ActingChange struct {
	// File is the file, relative to the top of the repository or the folder with
	// forward slashes: qory.yaml.
	File string
	// Key is the key, as acting.Entry's Key is: env.TOKEN. It is empty when the file
	// changed in a way that no key covers: it became or stopped being a link, a folder
	// or a file reached through a link, it is no longer valid YAML, it grew past 1 MiB,
	// or it is a link the run added or changed anywhere in the repository.
	Key string
}

// shape is what a qory file is at a commit or in a folder.
type shape int

const (
	// shapeMissing is no entry at the path.
	shapeMissing shape = iota
	// shapeYAML is a regular file that is valid YAML.
	shapeYAML
	// shapeOther is a link, a folder, a submodule, an entry reached through a link, or
	// a regular file larger than [maxQoryFile] or that is no valid YAML.
	shapeOther
)

// qoryFile is one qory file: its shape, an id of its content, and each acting key it
// sets with its value.
type qoryFile struct {
	shape shape
	id    string
	keys  map[string]string
}

// qoryName is the name of a qory file at the top that name matches regardless of letter
// case and of the code points macOS ignores, "" for none.
func qoryName(name string) string {
	for _, q := range []string{exports.FileName, exports.AltFileName, exports.StackFileName} {
		if folds(name, q) {
			return q
		}
	}
	return ""
}

// readQoryFile reads the entry at rel as a qory file: its shape and id, and the
// content of a file that may be valid YAML.
type readQoryFile func(rel string) (qoryFile, []byte, error)

// qoryChanges is the acting changes between the qory files before and after: each side
// has the names at its top and a reader. The files are those at the top, and each stack
// file one of them extends by a relative path, on either side, at most [maxQoryFiles]
// per side; each is read on both sides.
func qoryChanges(topBefore []string, readBefore readQoryFile, topAfter []string, readAfter readQoryFile) ([]ActingChange, error) {
	before, beforeNames, err := walkQory(topBefore, readBefore)
	if err != nil {
		return nil, err
	}
	after, afterNames, err := walkQory(topAfter, readAfter)
	if err != nil {
		return nil, err
	}
	for rel, canonical := range afterNames {
		if _, ok := before[rel]; !ok {
			if before[rel], _, err = parseQoryFile(readBefore, rel, canonical); err != nil {
				return nil, err
			}
		}
	}
	for rel, canonical := range beforeNames {
		if _, ok := after[rel]; !ok {
			if after[rel], _, err = parseQoryFile(readAfter, rel, canonical); err != nil {
				return nil, err
			}
		}
	}
	return compareQoryFiles(before, after), nil
}

// walkQory reads the qory files from top, the names at the top, and each stack file
// one of them extends by a relative path, with read, at most [maxQoryFiles]. It returns
// them by path, with the name qory reads each by: qory.yaml, harness.yaml or
// qory-stack.yaml.
func walkQory(top []string, read readQoryFile) (map[string]qoryFile, map[string]string, error) {
	files, names := map[string]qoryFile{}, map[string]string{}
	type queued struct{ rel, canonical string }
	var queue []queued
	for _, name := range top {
		if q := qoryName(name); q != "" {
			queue = append(queue, queued{name, q})
		}
	}
	for len(queue) > 0 && len(files) < maxQoryFiles {
		next := queue[0]
		queue = queue[1:]
		if _, seen := files[next.rel]; seen {
			continue
		}
		f, base, err := parseQoryFile(read, next.rel, next.canonical)
		if err != nil {
			return nil, nil, err
		}
		if base != "" {
			queue = append(queue, queued{base, exports.StackFileName})
		}
		files[next.rel], names[next.rel] = f, next.canonical
	}
	return files, names, nil
}

// parseQoryFile reads the entry at rel with read and, for a file that is valid YAML,
// its acting keys as the file canonical, and the stack file it extends by a relative
// path, "" for none.
func parseQoryFile(read readQoryFile, rel, canonical string) (qoryFile, string, error) {
	f, data, err := read(rel)
	if err != nil || f.shape != shapeYAML {
		return f, "", err
	}
	keys, base, err := actingKeys(path.Join(path.Dir(rel), canonical), data)
	if err != nil {
		f.shape = shapeOther
		return f, "", nil
	}
	f.keys = keys
	return f, base, nil
}

// compareQoryFiles is the acting changes between before and after, sorted by file, then
// by key.
func compareQoryFiles(before, after map[string]qoryFile) []ActingChange {
	var out []ActingChange
	for _, file := range unionKeys(before, after) {
		b, a := before[file], after[file]
		if (b.shape == shapeOther || a.shape == shapeOther) && (b.shape != a.shape || b.id != a.id) {
			out = append(out, ActingChange{File: file})
		}
		for _, key := range unionKeys(b.keys, a.keys) {
			bv, bok := b.keys[key]
			av, aok := a.keys[key]
			if bok != aok || bv != av {
				out = append(out, ActingChange{File: file, Key: key})
			}
		}
	}
	return sortChanges(out)
}

// sortChanges sorts changes by file, then by key, and drops repeats.
func sortChanges(changes []ActingChange) []ActingChange {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].File != changes[j].File {
			return changes[i].File < changes[j].File
		}
		return changes[i].Key < changes[j].Key
	})
	out := changes[:0]
	for i, c := range changes {
		if i == 0 || c != changes[i-1] {
			out = append(out, c)
		}
	}
	return out
}

// fakeTop is the folder a qory file is read in for its acting keys. No path is
// resolved on this machine: every path value is compared as written.
const fakeTop = "/qory-file-top"

// actingKeys is each acting key, by package acting's list, that the qory file at rel,
// with data as its content, sets, with its value as JSON; and the stack file it extends
// by a relative path inside the repository, "" for none. Each path value is compared as
// the file has it: actingKeys wraps it before package acting reads the file, so acting
// resolves no path on this machine.
func actingKeys(rel string, data []byte) (map[string]string, string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, "", err
	}
	document := path.Base(rel) == exports.FileName || path.Base(rel) == exports.AltFileName
	section := doc
	if document {
		section, _ = doc["harness"].(map[string]any)
	}
	extends := wrapPaths(section)
	wrapped, err := yaml.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	at := filepath.Join(fakeTop, filepath.FromSlash(rel))
	entries, err := acting.Entries(at, wrapped, "")
	if err != nil {
		return nil, "", err
	}
	keys := make(map[string]string, len(entries))
	for _, e := range entries {
		keys[e.Key] = e.Value
	}
	base := ""
	if extends != "" && !strings.HasPrefix(extends, "~") {
		if p := acting.Base(at, data); p != "" {
			if r, err := filepath.Rel(fakeTop, p); err == nil && cleanRel(filepath.ToSlash(r)) {
				base = filepath.ToSlash(r)
			}
		}
	}
	return keys, base, nil
}

// wrapPaths replaces each path value of section, extends.path and
// modules[].source.path, with a mapping that contains it as written, which package
// acting compares as a value and does not resolve. It returns extends.path as written.
func wrapPaths(section map[string]any) string {
	if section == nil {
		return ""
	}
	wrap := func(m map[string]any) string {
		s, ok := m["path"].(string)
		if ok {
			m["path"] = map[string]any{"as written": s}
		}
		return s
	}
	extends := ""
	if ext, ok := section["extends"].(map[string]any); ok {
		extends = wrap(ext)
	}
	modules, _ := section["modules"].([]any)
	for _, m := range modules {
		mod, _ := m.(map[string]any)
		if src, ok := mod["source"].(map[string]any); ok {
			wrap(src)
		}
	}
	return extends
}

// unionKeys is the keys of a and b, sorted, each once.
func unionKeys[V any](a, b map[string]V) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}
