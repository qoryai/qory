package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/compose"
	"github.com/qoryai/qory/internal/layer"
)

// Link is one path in a checkout that points into the composed home.
type Link struct {
	// Checkout is the path relative to the checkout root, such as ".claude".
	Checkout string
	// Home is the path inside the home the link points at, "" being the home itself.
	Home string
	// Soft marks a link that yields to whatever qory did not write at its path.
	Soft bool
}

// Runtime renders for one program that runs the harness. A package under internal/render
// implements it for one program and registers the implementation from its init.
type Runtime interface {
	// Name is the target.runtime value.
	Name() string
	// Render writes the runtime's own files into dir, the runtime's directory inside a
	// staging copy of the home. Paths written into files name home, where the tree ends up.
	// The shared parts, AGENTS.md, skills/ and hooks/, are already at the home's root.
	Render(res *compose.Result, dir, home string) error
	// Links are the paths a checkout needs so the program reads the home. A nil res asks for
	// the full set, which is what [Unlink] removes.
	Links(res *compose.Result) []Link
	// Skips are the entry kinds the runtime has no place for.
	Skips() []string
}

// runtimes holds every registered runtime by its target.runtime value.
var runtimes = map[string]Runtime{}

// Register adds a runtime, and the runtime package's init is the only caller. A second
// runtime of the same name replaces the first.
func Register(p Runtime) { runtimes[p.Name()] = p }

// Lookup returns the runtime registered for a target.runtime value. The error for a name
// nothing registered lists the names that are.
func Lookup(name string) (Runtime, error) {
	p, ok := runtimes[name]
	if !ok {
		return nil, fmt.Errorf("runtime %q is not one this qory renders; runtimes: %s", name, strings.Join(Names(), ", "))
	}
	return p, nil
}

// Names lists the registered runtimes in alphabetical order.
func Names() []string {
	var names []string
	for n := range runtimes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Composed reports the runtimes the home already holds, by the directories in it that are
// named after a registered runtime. It returns nothing when the home does not exist, which
// is the first compose of a checkout. A directory of any other name is ignored, so a home
// written by a later qory that knows more runtimes loses only what this build cannot render.
func Composed(home string) []Runtime {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil
	}
	var found []Runtime
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p, ok := runtimes[e.Name()]; ok {
			found = append(found, p)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name() < found[j].Name() })
	return found
}

// Build writes the composed home for one or more runtimes. It stages the tree in home with
// ".tmp" appended, discarding whatever that path held, links the shared skills and hooks and
// writes AGENTS.md at the staging root, calls each runtime's Render for the subdirectory
// named after it, then removes the old home and renames the staging directory over it. A
// failure before the rename leaves the previous home as it was.
//
// Every runtime the home is to hold must be passed in one call, because the rename replaces
// the whole tree. A caller composing for one runtime passes the runtimes already in the home
// alongside it, which [Composed] reports, so that the links of a checkout composed for
// several runtimes keep resolving and stay current. Build with no runtime is an error.
func Build(res *compose.Result, home string, runtimes ...Runtime) error {
	tmp := home + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	if err := LinkEntries(res, tmp, "skills", "hooks"); err != nil {
		return err
	}
	if res.Instructions != "" {
		if err := WriteFile(tmp, "AGENTS.md", []byte(res.Instructions)); err != nil {
			return err
		}
	}
	if len(runtimes) == 0 {
		return errors.New("build needs at least one runtime")
	}
	for _, p := range runtimes {
		dir := filepath.Join(tmp, p.Name())
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := p.Render(res, dir, home); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(home); err != nil {
		return err
	}
	return os.Rename(tmp, home)
}

// LinkInto writes the runtime's links into the checkout at root, each one relative so a
// checkout that moves keeps them valid, and adds every link and the qory directory to the
// clone-local exclude file. home is the composed tree under that qory directory.
//
// A path holding something qory did not write fails a hard link and is passed over for a
// soft one. LinkInto returns the checkout paths it passed over, for the caller to report.
// It stops at the first error, so the links before it are already written.
func LinkInto(p Runtime, res *compose.Result, root, home string) ([]string, error) {
	var skipped []string
	if err := exclude(root, "/"+checkout.Dir); err != nil {
		return nil, err
	}
	for _, l := range p.Links(res) {
		path := filepath.Join(root, l.Checkout)
		if err := removeOwnLink(path); err != nil {
			if l.Soft {
				skipped = append(skipped, l.Checkout)
				continue
			}
			return skipped, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return skipped, err
		}
		target, err := filepath.Rel(filepath.Dir(path), filepath.Join(home, l.Home))
		if err != nil {
			return skipped, err
		}
		if err := os.Symlink(target, path); err != nil {
			return skipped, err
		}
		if err := exclude(root, "/"+l.Checkout); err != nil {
			return skipped, err
		}
	}
	return skipped, nil
}

// Unlink removes the runtime's links that qory wrote, the relative symlinks into the qory
// directory, and returns their checkout paths. A parent directory the links left empty,
// such as .agents, is removed too.
//
// Anything else at a link's path is not qory's to remove: a hard link's path fails, and a
// soft link's path is left alone, which is how a checkout's own AGENTS.md survives.
// Unlink leaves the home for the caller to remove, and it leaves the exclude lines,
// because worktrees of one repository share one exclude file and a line without its link
// is harmless.
func Unlink(p Runtime, root string) ([]string, error) {
	var removed []string
	for _, l := range p.Links(nil) {
		path := filepath.Join(root, l.Checkout)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := removeOwnLink(path); err != nil {
			if l.Soft {
				continue
			}
			return removed, err
		}
		removed = append(removed, l.Checkout)
		if parent := filepath.Dir(path); parent != root {
			_ = os.Remove(parent)
		}
	}
	return removed, nil
}

// removeOwnLink removes path when it is a link qory wrote, a relative symlink whose
// target runs through the qory directory. A path that does not exist is nothing to remove
// and no error. A regular file, a directory, or a link pointing elsewhere is an error
// naming what stands in the way, and it is the check that keeps a render from eating a
// repository's own files.
func removeOwnLink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s is not a link qory wrote; qory does not replace it", path)
	}
	target, err := os.Readlink(path)
	if err != nil {
		return err
	}
	if filepath.IsAbs(target) || !strings.Contains(filepath.ToSlash(target), "/"+checkout.Dir+"/") && !strings.HasPrefix(filepath.ToSlash(target), checkout.Dir+"/") {
		return fmt.Errorf("%s links to %s, which qory did not write; qory does not replace it", path, target)
	}
	return os.Remove(path)
}

// exclude appends one line to the checkout's clone-local exclude file, once. A checkout
// outside git has no such file, and then there is nothing to exclude and no error.
func exclude(root, line string) error {
	file := checkout.ExcludeFile(root)
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return os.WriteFile(file, []byte(text+line+"\n"), 0o644)
}

// LinkEntries writes one symlink per composed entry of the kinds in keep, at
// dir/<kind>/<name>, pointing at the entry in its layer. Skills and hooks keep their
// name, the Markdown kinds get ".md" appended. Entries of any other kind are left out,
// and it is the caller's job to pass every kind the runtime reads from a link.
func LinkEntries(res *compose.Result, dir string, keep ...string) error {
	wanted := map[string]bool{}
	for _, k := range keep {
		wanted[k] = true
	}
	for _, e := range res.Entries {
		if !wanted[e.Kind] {
			continue
		}
		kindDir := filepath.Join(dir, e.Kind)
		if err := os.MkdirAll(kindDir, 0o755); err != nil {
			return err
		}
		link := filepath.Join(kindDir, e.Name)
		switch e.Kind {
		case "skills", "hooks":
		default:
			link += ".md"
		}
		if err := os.Symlink(e.Path, link); err != nil {
			return err
		}
	}
	return nil
}

// Skipped lists the composed entries of the kinds a runtime has no place for, as
// "<kind>/<name>", in the order the compose produced them. The compose prints the list so
// an entry that went nowhere is announced rather than silently dropped.
func Skipped(p Runtime, res *compose.Result) []string {
	skip := map[string]bool{}
	for _, k := range p.Skips() {
		skip[k] = true
	}
	var out []string
	for _, e := range res.Entries {
		if skip[e.Kind] {
			out = append(out, e.Kind+"/"+e.Name)
		}
	}
	return out
}

// WriteSettings writes the runtime's merged settings files into dir, one per target file
// a layer contributed a fragment to, with every $QORY_HARNESS_HOME already replaced by
// home. A ".toml" name is encoded as TOML, every other name as indented JSON.
//
// patch, when it is not nil, is called with each file's name and its merged map before
// the encoding, and changing the map there is how a runtime writes the target model.
// Names in ensure are written even when no layer contributed to them, so a model reaches
// a file that would otherwise not exist.
func WriteSettings(res *compose.Result, runtime, dir, home string, ensure []string, patch func(file string, m map[string]any)) error {
	files := res.SettingsFiles(runtime)
	for _, e := range ensure {
		found := false
		for _, f := range files {
			if f == e {
				found = true
			}
		}
		if !found {
			files = append(files, e)
		}
	}
	for _, file := range files {
		m := res.SettingsFor(runtime, file, home)
		if patch != nil {
			patch(file, m)
		}
		var data []byte
		var err error
		switch filepath.Ext(file) {
		case ".toml":
			data, err = EncodeTOML(m)
		default:
			data, err = json.MarshalIndent(m, "", "  ")
			data = append(data, '\n')
		}
		if err != nil {
			return err
		}
		if err := WriteFile(dir, file, data); err != nil {
			return err
		}
	}
	return nil
}

// EncodeTOML encodes a settings map as TOML. A list whose every element is a map becomes
// an array of tables, which the encoder will not do for a list of the untyped maps a JSON
// or YAML decoder produces.
func EncodeTOML(m map[string]any) ([]byte, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(tables(m)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// tables retypes a value for the TOML encoder: a []any of maps becomes a
// []map[string]any, the shape the encoder emits as an array of tables. A list with one
// non-map element is left as it is, because a mixed list is no array of tables.
func tables(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = tables(e)
		}
		return out
	case []any:
		allMaps := len(x) > 0
		for _, e := range x {
			if _, ok := e.(map[string]any); !ok {
				allMaps = false
			}
		}
		if !allMaps {
			return x
		}
		out := make([]map[string]any, len(x))
		for i, e := range x {
			out[i] = tables(e).(map[string]any)
		}
		return out
	default:
		return v
	}
}

// WriteAgents writes one Markdown file per composed agent at dir/sub/<name><suffix>,
// where suffix carries the runtime's extension, such as ".agent.md". Each file is the
// agent's body under frontmatter holding only the keys in keep that the source agent has,
// so a key one runtime reads does not reach another. When keep names "name" and the
// source has none, the entry's name fills it. The keys come out in alphabetical order,
// not the order of keep.
func WriteAgents(res *compose.Result, dir, sub, suffix string, keep ...string) error {
	for _, e := range res.Entries {
		if e.Kind != "agents" {
			continue
		}
		doc, err := layer.ReadDocument(e.Path)
		if err != nil {
			return err
		}
		front := map[string]any{}
		for _, k := range keep {
			if v, ok := doc.Front[k]; ok {
				front[k] = v
			}
		}
		if _, ok := front["name"]; !ok && contains(keep, "name") {
			front["name"] = e.Name
		}
		var b bytes.Buffer
		b.WriteString("---\n")
		data, err := yaml.Marshal(front)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteString("---\n\n")
		b.WriteString(doc.Body)
		if err := WriteFile(dir, filepath.Join(sub, e.Name+suffix), b.Bytes()); err != nil {
			return err
		}
	}
	return nil
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// WriteFile writes data to dir/name with mode 0644, creating the parent directories and
// overwriting a file already there. name may hold separators, such as
// "agents/reviewer.md".
func WriteFile(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
