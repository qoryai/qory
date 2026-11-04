// Package acting holds the keys of qory's files that qory acts on: the commands it runs,
// the paths it follows and the sources it fetches. A walled run can change a qory.yaml
// and a stack file, and qory on this machine reads those files afterwards, so a caller
// compares the acting keys a file sets before and after a run.
//
// The acting keys of a qory.yaml, or harness.yaml, are worktree.run.add and
// worktree.run.remove, worktree.link, worktree.copy and worktree.dir; harness.launch.*,
// harness.home, env.* and git.cache; harness.extends.git and harness.modules[].source.git;
// and harness.extends.path and harness.modules[].source.path when they lead outside the
// place. A stack file's are extends and modules[].source, the same way. Every other key
// changes freely, so a pull or an edit of the stack's modules changes no acting key.
package acting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/realpath"
)

// Entry is one acting key a file sets.
type Entry struct {
	// Key is the key as a person reads it, with a module's place in the list:
	// harness.modules[1].source.git.
	Key string
	// Value is the value as JSON.
	Value string
	// Leads is, for a path that leads out of the place through a link, where it leads,
	// with its links resolved; "" for any other.
	Leads string
	// id is the key the hash takes: Key without a module's place in the list, so a
	// module moved in the list keeps its hash.
	id string
	// target is, for a path outside the place, where it resolves, which the hash takes
	// with the value: a link on the way that is pointed elsewhere changes the hash.
	target string
}

// Hash is the entry's hash: the SHA-256 of its key, without a module's place in the
// list, and its value, and of where a path outside the place resolves.
func (e Entry) Hash() string {
	v := e.Value
	if e.target != "" {
		// A value as JSON holds no line break, so the target follows one.
		v += "\n" + e.target
	}
	h := sha256.New()
	h.Write([]byte("qory." + e.id))
	h.Write([]byte{0})
	h.Write([]byte(v))
	return hex.EncodeToString(h.Sum(nil))
}

// document reports whether path is read as a qory.yaml, through its sections, rather
// than as a stack file.
func document(path string) bool {
	base := filepath.Base(path)
	return base == exports.FileName || base == exports.AltFileName
}

// Entries lists the acting keys the file at path sets, data being its content, in the
// order the file sets them. place is the place the file lies in: a path that leads into
// it is no acting key, since everything there is checked as a file of the place. A file
// that is not YAML is an error.
func Entries(path string, data []byte, place string) ([]Entry, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	// A relative path resolves as compose resolves it: lexically, from the folder of the
	// file as given, not from where a link in the place on the way to that folder leads.
	// A link outside the place that leads to it, such as /tmp on macOS, is resolved, so
	// the file read through it sets the same keys as the file read at the place.
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	dir = atPlace(dir, place)
	var out []Entry
	add := func(key, id string, v any) {
		value, err := json.Marshal(v)
		if err != nil {
			value = []byte(fmt.Sprint(v))
		}
		out = append(out, Entry{Key: key, Value: string(value), id: id})
	}
	// A path outside the place, with where it resolves and, when a link on the way
	// leads there, where it leads.
	addPath := func(key, id string, v any, target, leads string) {
		add(key, id, v)
		out[len(out)-1].target, out[len(out)-1].Leads = target, leads
	}
	outsidePlace := func(v any) (outside bool, target, leads string) {
		s, ok := v.(string)
		if !ok || s == "" {
			return !ok, "", ""
		}
		if rest, ok := strings.CutPrefix(s, "~"); ok {
			// A path from a home is outside, wherever the home is.
			home, err := os.UserHomeDir()
			if err != nil || rest != "" && !strings.HasPrefix(rest, "/") {
				return true, "", ""
			}
			s = filepath.Join(home, rest)
		} else {
			if !filepath.IsAbs(s) {
				s = filepath.Join(dir, s)
			}
			// A path inside the place as written that leads out of it through a link is
			// outside: qory follows the link.
			if realpath.Inside(place, s) {
				return false, "", ""
			}
		}
		target = realpath.Real(s)
		if target != filepath.Clean(s) {
			leads = target
		}
		return true, target, leads
	}
	sources := func(prefix string, section map[string]any) {
		if ext, ok := section["extends"].(map[string]any); ok {
			if v, ok := ext["path"]; ok {
				if leaves, target, leads := outsidePlace(v); leaves {
					addPath(prefix+"extends.path", prefix+"extends.path", v, target, leads)
				}
			}
			if v, ok := ext["git"]; ok {
				add(prefix+"extends.git", prefix+"extends.git", v)
			}
		}
		modules, _ := section["modules"].([]any)
		for i, m := range modules {
			mod, _ := m.(map[string]any)
			src, _ := mod["source"].(map[string]any)
			if src == nil {
				continue
			}
			at := fmt.Sprintf("%smodules[%d].source.", prefix, i)
			if v, ok := src["path"]; ok {
				if leaves, target, leads := outsidePlace(v); leaves {
					addPath(at+"path", prefix+"modules.source.path", v, target, leads)
				}
			}
			if v, ok := src["git"]; ok {
				add(at+"git", prefix+"modules.source.git", v)
			}
		}
	}
	if !document(path) {
		sources("", doc)
		return out, nil
	}
	if w, ok := doc["worktree"].(map[string]any); ok {
		for _, k := range []string{"dir", "link", "copy"} {
			if v, ok := w[k]; ok {
				add("worktree."+k, "worktree."+k, v)
			}
		}
		if run, ok := w["run"].(map[string]any); ok {
			for _, k := range []string{"add", "remove"} {
				if v, ok := run[k]; ok {
					add("worktree.run."+k, "worktree.run."+k, v)
				}
			}
		}
	}
	if h, ok := doc["harness"].(map[string]any); ok {
		if v, ok := h["home"]; ok {
			add("harness.home", "harness.home", v)
		}
		if launch, ok := h["launch"].(map[string]any); ok {
			for _, rt := range sortedKeys(launch) {
				l, ok := launch[rt].(map[string]any)
				if !ok {
					add("harness.launch."+rt, "harness.launch."+rt, launch[rt])
					continue
				}
				for _, k := range []string{"command", "args"} {
					if v, ok := l[k]; ok {
						key := "harness.launch." + rt + "." + k
						add(key, key, v)
					}
				}
				if env, ok := l["env"].(map[string]any); ok {
					for _, name := range sortedKeys(env) {
						key := "harness.launch." + rt + ".env." + name
						add(key, key, env[name])
					}
				} else if v, ok := l["env"]; ok {
					key := "harness.launch." + rt + ".env"
					add(key, key, v)
				}
			}
		} else if v, ok := h["launch"]; ok {
			add("harness.launch", "harness.launch", v)
		}
		sources("harness.", h)
	}
	if env, ok := doc["env"].(map[string]any); ok {
		for _, name := range sortedKeys(env) {
			add("env."+name, "env."+name, env[name])
		}
	} else if v, ok := doc["env"]; ok {
		add("env", "env", v)
	}
	if g, ok := doc["git"].(map[string]any); ok {
		if v, ok := g["cache"]; ok {
			add("git.cache", "git.cache", v)
		}
	}
	return out, nil
}

// atPlace is dir, absolute and clean, with the folder above it, or dir itself, that
// leads to place through a link outside the place, put as place's real path; dir as it
// is when none does. The links in the place on the way stay as written.
func atPlace(dir, place string) string {
	if place == "" {
		return dir
	}
	real := realpath.Real(place)
	for q := dir; ; q = filepath.Dir(q) {
		if !within(place, q) && !within(real, q) && realpath.Real(q) == real {
			rel, err := filepath.Rel(q, dir)
			if err != nil {
				return dir
			}
			return filepath.Join(real, rel)
		}
		if q == filepath.Dir(q) {
			return dir
		}
	}
}

// within reports whether path lies in place as written, place itself included.
func within(place, path string) bool {
	rel, err := filepath.Rel(place, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Hashes is the sorted hashes of entries, each once.
func Hashes(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		h := e.Hash()
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// Rel is the path of the file at path in the place: relative to the place, with forward
// slashes, both with their links resolved.
func Rel(place, path string) string {
	rel, err := filepath.Rel(realpath.Real(place), realpath.Real(path))
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// Base is the stack file the file at path, data being its content, extends by a path:
// qory-stack.yaml in the directory its extends.path, or harness.extends.path for a
// qory.yaml, names; "" for none.
func Base(path string, data []byte) string {
	var doc map[string]any
	if yaml.Unmarshal(data, &doc) != nil {
		return ""
	}
	section := doc
	if document(path) {
		section, _ = doc["harness"].(map[string]any)
	}
	ext, _ := section["extends"].(map[string]any)
	p, _ := ext["path"].(string)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(path), p)
	}
	return filepath.Join(p, exports.StackFileName)
}
