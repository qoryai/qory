package module

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// ManifestName is the manifest's file name at the module root. Every module carries one; it
// is what names the module.
const ManifestName = "qory-module.yaml"

// InstructionsName is the module's instruction file, read by every runtime.
const InstructionsName = "AGENTS.md"

// Variant maps an entry kind to the directory that variant reads it from, relative to the
// module root. A kind the variant does not name is read from the directory named like the
// kind.
type Variant map[string]string

// Manifest is one qory-module.yaml, validated.
type Manifest struct {
	APIVersion string `yaml:"apiVersion"`
	// Name is the module's name, the one the stack refers to it by.
	Name string `yaml:"name"`
	// Description says what the module is for, carried into the report.
	Description string
	// Variants are the module's variants by name, without the default key.
	Variants map[string]Variant
	// Default is the variant a runtime without its own gets, or "fail" to refuse the compose.
	// It comes from the default key among the variants.
	Default string
	// Env are the environment variables the module exports, name to a path inside the module
	// relative to its root, cleaned, "." for the root itself. The compose turns each into
	// $QORY_HARNESS_HOME/modules/<name>/<path> and the runtimes with a place for environment
	// write them. A key is a POSIX environment variable name other than QORY_HARNESS_HOME.
	Env map[string]string
	// Requires is what the module's entries need composed beside them: per entry this
	// module ships, keyed <kind>/<name>, the entries it needs as <kind>/<name>, sorted. In
	// the file each is one item naming the entry by its singular kind and what it needs
	// by the plural kinds an exclude uses, see [ReadManifest]. A required entry may come
	// from any module; the compose refuses a stack that leaves one out.
	Requires map[string][]string
}

// rawManifest decodes qory-module.yaml as it is written, where the variants map holds both the
// variants and the default, a string among the maps. [ReadManifest] splits the two apart.
type rawManifest struct {
	APIVersion  string                 `yaml:"apiVersion"`
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description,omitempty"`
	Variants    map[string]yaml.Node   `yaml:"variants,omitempty"`
	Env         map[string]string      `yaml:"env,omitempty"`
	Requires    []map[string]yaml.Node `yaml:"requires,omitempty"`
}

// singular maps the key a requires item names its entry by to the entry's kind. mcp is
// its own singular, so under requires the key holds the entry's name as a string and the
// needed servers as a list; YAML allows a key once per item, so an item naming a server
// cannot list servers it needs.
var singular = map[string]string{"skill": "skills", "agent": "agents", "command": "commands", "output-style": "output-styles", "hook": "hooks", "mcp": "mcp", "file": "files"}

// singularOf is the key a message names an entry's kind by: skill for skills.
func singularOf(kind string) string {
	for one, many := range singular {
		if many == kind {
			return one
		}
	}
	return kind
}

// Describe names an entry for a message the way a requires item does, "skill deploy".
func Describe(kind, name string) string { return singularOf(kind) + " " + name }

// Entry is one atomic entry: a skill, an agent, a command, an output style, a hook script
// or an MCP server.
type Entry struct {
	// Kind is one of skills, agents, commands, output-styles, hooks, mcp and files.
	Kind string
	// Name identifies the entry within its kind: the skill directory name, the Markdown file
	// name without its extension, the hook file name with its extension, the MCP server's
	// file name without .json, or a file's <runtime>/<path> under files/.
	Name string
	// Path is absolute: the skill directory, the Markdown file, the hook script, the MCP
	// server's JSON file or the file itself.
	Path string
}

// Module is a module read from disk.
type Module struct {
	// Name is the stack's name for the module, not the manifest's own name.
	Name string
	// Dir is the absolute module root.
	Dir string
	// Manifest is the module's manifest, nil when it ships none.
	Manifest *Manifest
	// Variant is the variant [Read] scanned, "" for a module without variants.
	Variant string
	// Entries are the module's entries, sorted by kind, then by name.
	Entries []Entry
	// Settings are the settings/<runtime>/<file> fragments: per runtime, per target file, the
	// absolute path of the one fragment this module contributes to it.
	Settings map[string]map[string]string
	// MCP holds the MCP servers by name, each the JSON object its mcp/<name>.json file
	// holds, as written, with every $QORY_HARNESS_HOME still in place.
	MCP map[string]map[string]any
	// Instructions is the absolute AGENTS.md path, or "" when the module ships none.
	Instructions string
}

// readRequirement reads one requires item: the entry it names, as <kind>/<name>, and what
// the entry needs, as sorted <kind>/<name> keys. The mcp key holds the entry's name when
// it is a string and needed servers when it is a list.
func readRequirement(item map[string]yaml.Node) (entry string, needs []string, err error) {
	keys := make([]string, 0, len(item))
	for k := range item {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		node := item[k]
		kind, one := singular[k]
		if one && !(k == "mcp" && node.Kind == yaml.SequenceNode) {
			var name string
			if err := node.Decode(&name); err != nil || name == "" {
				return "", nil, fmt.Errorf("%s names no entry; it is the entry's name, as %s: <name>", k, k)
			}
			if entry != "" {
				return "", nil, fmt.Errorf("names two entries; an item names one, as skill: <name>, and lists what it needs under skills, agents, commands, output-styles, hooks, mcp and files")
			}
			entry = kind + "/" + name
			continue
		}
		if !isKind(k) {
			return "", nil, fmt.Errorf("key %q is not one requires reads; an item names its entry as skill, agent, command, output-style, hook, mcp or file, and what it needs under skills, agents, commands, output-styles, hooks, mcp and files", k)
		}
		var names []string
		if err := node.Decode(&names); err != nil || len(names) == 0 {
			return "", nil, fmt.Errorf("%s is empty; it lists the %s the entry needs", k, k)
		}
		for _, name := range names {
			if name == "" {
				return "", nil, fmt.Errorf("%s names an empty entry", k)
			}
			needs = append(needs, k+"/"+name)
		}
	}
	if entry == "" {
		return "", nil, fmt.Errorf("names no entry; an item names one, as skill: <name>, and lists what it needs under skills, agents, commands, output-styles, hooks, mcp and files")
	}
	if len(needs) == 0 {
		return "", nil, fmt.Errorf("lists nothing the entry needs under skills, agents, commands, output-styles, hooks, mcp or files")
	}
	sort.Strings(needs)
	return entry, needs, nil
}

// isKind reports whether k is one of [stack.Kinds].
func isKind(k string) bool {
	for _, kind := range stack.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// sortedKeys lists a map's keys in order, so a check over them reports the same first
// key every time.
func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// envName is the shape of a POSIX environment variable name, which every key of a
// manifest's env has.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// unknownKey is the decoder's report of a key the document has no field for. It names the
// Go type, which the message a person reads leaves out.
var unknownKey = regexp.MustCompile(`(line \d+: )?field (\S+) not found in type \S+`)

// decodeError prefixes a decode error with path. An unknown key is reported as one the
// manifest does not read, with the decoder's line number when it gives one; any other
// error is returned as the decoder wrote it.
func decodeError(path string, err error) error {
	if m := unknownKey.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%s: %skey %q is not one %s reads", path, m[1], m[2], ManifestName)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// ReadManifest reads and validates qory-module.yaml at dir. A directory without one is
// not a module, and the error says so and names the directory, because a source that
// points at the wrong directory is the mistake this catches.
//
// An unknown field is an error, so is an apiVersion other than [stack.APIVersion], a
// missing name, a default that names something other than a declared
// variant or "fail", an env key that is not an environment variable name or is
// QORY_HARNESS_HOME, and an env value that is not a relative path inside the module.
// Every error names the manifest path. The env values come back cleaned, "." for the root.
//
// A requires item names one entry of the module by its singular kind and lists what it
// needs under the plural kinds:
//
//	requires:
//	  - skill: deploy
//	    commands: [ship]
//	    agents: [reviewer]
//
// An item with no entry or two, a key that is neither, an empty list, and an entry named
// twice are errors. Whether the entry is one the module ships is [Read]'s check, since
// the entries are read then. The result is keyed <kind>/<name> with sorted values.
func ReadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s has no %s; a module carries one naming it", dir, ManifestName)
	}
	if err != nil {
		return nil, err
	}
	raw := rawManifest{}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, decodeError(path, err)
	}
	if err := exports.CheckAPIVersion(raw.APIVersion); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if raw.Name == "" {
		return nil, fmt.Errorf("%s: name is required", path)
	}
	m := &Manifest{APIVersion: raw.APIVersion, Name: raw.Name, Description: raw.Description}
	for key, value := range raw.Env {
		if key == "QORY_HARNESS_HOME" {
			return nil, fmt.Errorf("%s: env.QORY_HARNESS_HOME is qory's own; a module exports another name", path)
		}
		if !envName.MatchString(key) {
			return nil, fmt.Errorf("%s: env: %s is not an environment variable name", path, key)
		}
		clean := filepath.Clean(value)
		if value == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s: env.%s: %s is not a path inside the module", path, key, value)
		}
		if m.Env == nil {
			m.Env = map[string]string{}
		}
		m.Env[key] = clean
	}
	for i, item := range raw.Requires {
		entry, needs, err := readRequirement(item)
		if err != nil {
			return nil, fmt.Errorf("%s: requires[%d]: %w", path, i, err)
		}
		if _, dup := m.Requires[entry]; dup {
			kind, name, _ := strings.Cut(entry, "/")
			return nil, fmt.Errorf("%s: requires names %s twice", path, Describe(kind, name))
		}
		if m.Requires == nil {
			m.Requires = map[string][]string{}
		}
		m.Requires[entry] = needs
	}
	for name, node := range raw.Variants {
		if name == "default" {
			if err := node.Decode(&m.Default); err != nil {
				return nil, fmt.Errorf("%s: variants.default: %w", path, err)
			}
			continue
		}
		v := Variant{}
		if err := node.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s: variants.%s: %w", path, name, err)
		}
		if m.Variants == nil {
			m.Variants = map[string]Variant{}
		}
		m.Variants[name] = v
	}
	if m.Default != "" && m.Default != "fail" {
		if _, ok := m.Variants[m.Default]; !ok {
			return nil, fmt.Errorf("%s: variants.default names %q, which is not a variant", path, m.Default)
		}
	}
	return m, nil
}

// SelectVariant picks the variant for a runtime: the forced one from the stack, else the
// one named like the runtime, else the manifest's default. A module with no manifest and a
// manifest that declares no variant serve every runtime from the module root, and return "".
//
// It returns an error when the stack forces a variant the module does not have, or forces
// one on a module that declares none, and when the module has no variant for the runtime and
// its default is missing or "fail". Every message lists the variant names the module has.
func SelectVariant(m *Manifest, forced, runtime string) (string, error) {
	if m == nil || len(m.Variants) == 0 {
		if forced != "" {
			return "", fmt.Errorf("variant %q is forced, and the module declares no variants", forced)
		}
		return "", nil
	}
	if forced != "" {
		if _, ok := m.Variants[forced]; !ok {
			return "", fmt.Errorf("variant %q is forced, and the module has no such variant; variants: %s", forced, names(m.Variants))
		}
		return forced, nil
	}
	if _, ok := m.Variants[runtime]; ok {
		return runtime, nil
	}
	if m.Default == "fail" {
		return "", fmt.Errorf("the module has no variant for runtime %s and its default is fail; variants: %s", runtime, names(m.Variants))
	}
	if m.Default == "" {
		return "", fmt.Errorf("the module has no variant for runtime %s and declares no default; variants: %s", runtime, names(m.Variants))
	}
	return m.Default, nil
}

// names lists the variant names, sorted, for the error messages of [SelectVariant].
func names(v map[string]Variant) string {
	ns := make([]string, 0, len(v))
	for n := range v {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return strings.Join(ns, ", ")
}

// Read scans the module at dir for one variant and returns what it ships. The name is the
// stack's name for the module and appears in every error. The variant is the one
// [SelectVariant] returned for m; "" reads every kind from the module root.
//
// A kind the variant redirects is read from the directory it names, which must be a
// relative directory inside the module: an empty value, ".", an absolute path and a path
// leaving the module are all refused. A variant that names a kind outside the seven known
// kinds is refused too.
//
// A kind directory that is not there leaves the module without entries of that kind. A
// skills subdirectory without SKILL.md is an error. In the Markdown kinds only .md files
// count. Under hooks every file is a hook and a directory is an error, because a hook is
// named by its file name and a script's helpers belong elsewhere in the module, reached as
// $QORY_HARNESS_HOME/modules/<name>/<path>. Under mcp every .json file is one server and must hold
// a JSON object. Under files every file below a runtime directory is one entry named
// <runtime>/<path>, at any depth, and a file directly under files is an error. A name
// starting with a dot is skipped everywhere. Settings are read from settings/ at the module
// root, which no variant redirects, and AGENTS.md from the root as well.
func Read(name, dir string, m *Manifest, variant string) (*Module, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	l := &Module{Name: name, Dir: abs, Manifest: m, Variant: variant}
	dirs := map[string]string{"skills": "skills", "agents": "agents", "commands": "commands", "output-styles": "output-styles", "hooks": "hooks", "mcp": "mcp", "files": "files"}
	if m != nil && variant != "" {
		for kind, sub := range m.Variants[variant] {
			if _, ok := dirs[kind]; !ok {
				return nil, fmt.Errorf("module %s: variant %s names kind %q", name, variant, kind)
			}
			clean := filepath.Clean(sub)
			if sub == "" || clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("module %s: variant %s reads %s from %q, which is not a directory inside the module", name, variant, kind, sub)
			}
			dirs[kind] = clean
		}
	}
	skills, err := readSkills(dirs["skills"], filepath.Join(abs, dirs["skills"]))
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", name, err)
	}
	l.Entries = append(l.Entries, skills...)
	for _, kind := range []string{"agents", "commands", "output-styles"} {
		es, err := readMarkdown(kind, filepath.Join(abs, dirs[kind]))
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", name, err)
		}
		l.Entries = append(l.Entries, es...)
	}
	hooks, err := readHooks(name, dirs["hooks"], filepath.Join(abs, dirs["hooks"]))
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", name, err)
	}
	l.Entries = append(l.Entries, hooks...)
	servers, mcp, err := readMCP(dirs["mcp"], filepath.Join(abs, dirs["mcp"]))
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", name, err)
	}
	l.Entries = append(l.Entries, servers...)
	l.MCP = mcp
	files, err := readFiles(dirs["files"], filepath.Join(abs, dirs["files"]))
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", name, err)
	}
	l.Entries = append(l.Entries, files...)
	for _, e := range l.Entries {
		if err := inside(abs, e.Path); err != nil {
			return nil, fmt.Errorf("module %s: %s/%s %w", name, e.Kind, e.Name, err)
		}
		if e.Kind == "skills" {
			if err := insideAll(abs, e.Path); err != nil {
				return nil, fmt.Errorf("module %s: skills/%s/%w", name, e.Name, err)
			}
		}
	}
	sort.Slice(l.Entries, func(i, j int) bool {
		if l.Entries[i].Kind != l.Entries[j].Kind {
			return l.Entries[i].Kind < l.Entries[j].Kind
		}
		return l.Entries[i].Name < l.Entries[j].Name
	})
	if m != nil {
		shipped := map[string]bool{}
		for _, e := range l.Entries {
			shipped[e.Kind+"/"+e.Name] = true
		}
		for _, key := range sortedKeys(m.Requires) {
			if !shipped[key] {
				kind, entry, _ := strings.Cut(key, "/")
				return nil, fmt.Errorf("module %s: requires names %s, which the module does not ship", name, Describe(kind, entry))
			}
		}
	}
	l.Settings, err = readSettings(filepath.Join(abs, "settings"))
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", name, err)
	}
	for runtime, files := range l.Settings {
		for file, path := range files {
			if err := inside(abs, path); err != nil {
				return nil, fmt.Errorf("module %s: settings/%s/%s %w", name, runtime, file, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(abs, InstructionsName)); err == nil {
		l.Instructions = filepath.Join(abs, InstructionsName)
		if err := inside(abs, l.Instructions); err != nil {
			return nil, fmt.Errorf("module %s: %s %w", name, InstructionsName, err)
		}
	}
	return l, nil
}

// inside checks that path, with every symlink resolved, is under the module root. A module
// may link an entry to elsewhere in itself; a link that leaves the module would make the
// harness read a file the report does not show, and is refused.
func inside(root, path string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("links to %s, outside the module", real)
	}
	return nil
}

// insideAll walks the skill directory at dir and checks every file and directory in it
// with [inside], because a skill is shipped whole: a file inside it that links out of the
// module would be read the way a linked entry would. The walk lists directories by their
// own entries and does not follow a link to a directory, so a link out of the module is
// found by its target without listing that target. An error names the offending path
// relative to dir, so the caller prefixes the skill.
func insideAll(root, dir string) error {
	return filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if err := inside(root, path); err != nil {
			rel, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				rel = path
			}
			return fmt.Errorf("%s %w", filepath.ToSlash(rel), err)
		}
		return nil
	})
}

// readSkills lists the skill directories of dir. A skill is a directory holding SKILL.md,
// so a directory without one is a mistake worth an error rather than a silent skip; the
// message names the directory as the variant reads it, rel, relative to the module root.
func readSkills(rel, dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
			return nil, fmt.Errorf("%s/%s has no SKILL.md", filepath.ToSlash(rel), it.Name())
		}
		es = append(es, Entry{Kind: "skills", Name: it.Name(), Path: path})
	}
	return es, nil
}

// readMarkdown lists the .md files of dir as entries of kind, named without the extension.
// A directory, a link to one, or any other extension in there is not an entry of this
// kind.
func readMarkdown(kind, dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") || !strings.HasSuffix(it.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			continue
		}
		es = append(es, Entry{Kind: kind, Name: strings.TrimSuffix(it.Name(), ".md"), Path: path})
	}
	return es, nil
}

// readFiles lists files/<runtime>/<path> as entries of kind files, one per file at any
// depth below a runtime directory, named <runtime>/<path> with forward slashes. A file
// directly under files is an error naming the shape, because the runtime segment is what
// decides where the file lands. A directory named with a dot, or a file named with one,
// is skipped with everything below it. A link to a directory is not followed, so a module
// cannot ship a tree it does not hold. rel is the files directory relative to the module
// root, for the message.
func readFiles(rel, dir string) ([]Entry, error) {
	runtimes, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	for _, rt := range runtimes {
		if strings.HasPrefix(rt.Name(), ".") {
			continue
		}
		root := filepath.Join(dir, rt.Name())
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s/%s is not under a runtime directory; a file ships as %s/<runtime>/<path>", filepath.ToSlash(rel), rt.Name(), filepath.ToSlash(rel))
		}
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == root {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			sub, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			es = append(es, Entry{Kind: "files", Name: rt.Name() + "/" + filepath.ToSlash(sub), Path: path})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return es, nil
}

// readSettings lists settings/<runtime>/<file>: the directory name is the runtime, the file
// name is the target file that runtime reads, and a module contributes at most one fragment
// per target file. A runtime directory holding no file at all is left out of the map. A
// link to a directory counts as the directory, the way the other readers count one. A
// directory under a runtime's directory is an error rather than a skip, so a module that
// keeps a runtime's other files there learns where they go: files/<runtime>/<path>.
func readSettings(dir string) (map[string]map[string]string, error) {
	runtimes, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, p := range runtimes {
		if strings.HasPrefix(p.Name(), ".") {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, p.Name()))
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, p.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if strings.HasPrefix(f.Name(), ".") {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, p.Name(), f.Name()))
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				return nil, fmt.Errorf("settings/%s/%s is a directory; settings holds one fragment per target file, and a runtime's other files go under files/%s/%s", p.Name(), f.Name(), p.Name(), f.Name())
			}
			if out[p.Name()] == nil {
				out[p.Name()] = map[string]string{}
			}
			out[p.Name()][f.Name()] = filepath.Join(dir, p.Name(), f.Name())
		}
	}
	return out, nil
}

// readHooks lists the files of the hooks directory as hook entries, named by their file
// name with its extension, because a settings fragment names a hook by the file name it
// has. A directory in there, or a link to one, is an error rather than a silent skip, so
// a module that keeps a script's helpers under hooks/ learns where they go instead: the
// message names the directory as the variant reads it, and how a file elsewhere in the
// module is reached. rel is the hooks directory relative to the module root.
func readHooks(name, rel, dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var es []Entry
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			sub := filepath.ToSlash(filepath.Join(rel, it.Name()))
			return nil, fmt.Errorf("%s is a directory; a hook is one file. Keep its helpers elsewhere in the module, reached as $QORY_HARNESS_HOME/modules/%s/<path>", sub, name)
		}
		es = append(es, Entry{Kind: "hooks", Name: it.Name(), Path: path})
	}
	return es, nil
}

// serverKeys are the keys an MCP server object may carry, the ones Claude Code's .mcp.json
// reads: the transport, the command with its arguments and environment for a stdio
// server, the URL with its headers for a remote one, and a description for the report,
// which the compose drops on emit. Any other key is refused, the way every other document
// of the contract refuses one.
var serverKeys = map[string]bool{"type": true, "command": true, "args": true, "env": true, "url": true, "headers": true, "description": true}

// readMCP lists mcp/<name>.json as MCP server entries named without the extension, and
// returns each file's object by name. A file that is not one JSON object, or an object
// [checkServer] refuses, is an error naming the file as the variant reads it; when the
// file does not parse, the message keeps the decoder's own, which names the offset. A
// directory or a file of another extension is not a server.
func readMCP(rel, dir string) ([]Entry, map[string]map[string]any, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var es []Entry
	servers := map[string]map[string]any{}
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") || !strings.HasSuffix(it.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		if info.IsDir() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		file := filepath.ToSlash(filepath.Join(rel, it.Name()))
		var server map[string]any
		if err := json.Unmarshal(data, &server); err != nil {
			return nil, nil, fmt.Errorf("%s does not hold a JSON object: %w", file, err)
		}
		if server == nil {
			return nil, nil, fmt.Errorf("%s does not hold a JSON object", file)
		}
		if err := checkServer(server); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", file, err)
		}
		name := strings.TrimSuffix(it.Name(), ".json")
		servers[name] = server
		es = append(es, Entry{Kind: "mcp", Name: name, Path: path})
	}
	if len(servers) == 0 {
		servers = nil
	}
	return es, servers, nil
}

// checkServer refuses a server object a runtime could not start, the same shapes
// mcp.schema.json refuses: a key no runtime reads, no command and no url, both, a command
// or url that is not a non-empty string, a type outside stdio, http and sse, an args
// element or an env or headers value that is not a string, a description that is not a
// string.
func checkServer(server map[string]any) error {
	for k := range server {
		if !serverKeys[k] {
			return fmt.Errorf("key %q is not one of an MCP server's; keys: type, command, args, env, url, headers, description", k)
		}
	}
	command, hasCommand := server["command"]
	url, hasURL := server["url"]
	switch {
	case hasCommand && hasURL:
		return errors.New("names both a command and a url; a server is one or the other")
	case !hasCommand && !hasURL:
		return errors.New("names neither a command nor a url")
	case hasCommand && !nonEmptyString(command):
		return errors.New("command is not a non-empty string")
	case hasURL && !nonEmptyString(url):
		return errors.New("url is not a non-empty string")
	}
	if t, ok := server["type"]; ok {
		if s, _ := t.(string); s != "stdio" && s != "http" && s != "sse" {
			return fmt.Errorf("type %v is not stdio, http or sse", t)
		}
	}
	if args, ok := server["args"]; ok {
		list, isList := args.([]any)
		if !isList {
			return errors.New("args is not a list")
		}
		for _, a := range list {
			if _, ok := a.(string); !ok {
				return fmt.Errorf("args holds %v, which is not a string", a)
			}
		}
	}
	for _, key := range []string{"env", "headers"} {
		v, ok := server[key]
		if !ok {
			continue
		}
		m, isMap := v.(map[string]any)
		if !isMap {
			return fmt.Errorf("%s is not an object", key)
		}
		for k, e := range m {
			if _, ok := e.(string); !ok {
				return fmt.Errorf("%s.%s is %v, which is not a string", key, k, e)
			}
		}
	}
	if d, ok := server["description"]; ok {
		if _, isString := d.(string); !isString {
			return fmt.Errorf("description is %v, which is not a string", d)
		}
	}
	return nil
}

// nonEmptyString reports whether v is a string with at least one character.
func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s != ""
}
