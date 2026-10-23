package module

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/stack"
)

// ManifestName is the manifest's file name at the module root. Every module has one; it
// defines the module's name.
const ManifestName = "qory-module.yaml"

// InstructionsName is the module's instruction file, read by every runtime.
const InstructionsName = "AGENTS.md"

// Variant maps an entry kind to the directory that variant reads it from, relative to the
// module root. A kind the variant does not list is read from the directory named like the
// kind.
type Variant map[string]string

// Manifest is one qory-module.yaml, validated.
type Manifest struct {
	// APIVersion is the format the manifest is read as, [stack.APIVersion].
	APIVersion string `yaml:"apiVersion"`
	// RetiredAPIVersion is the apiVersion the file declared when it is a retired
	// spelling of the current one, one of [exports.RetiredAPIVersions], "" when the file
	// declares the current one; a compose reports which files want the line rewritten.
	RetiredAPIVersion string
	// Name is the module's name, the one the stack refers to it by.
	Name string `yaml:"name"`
	// Description is what the module is for, copied into the report.
	Description string
	// Variants are the module's variants by name, without the default key.
	Variants map[string]Variant
	// Default is the variant a runtime without its own gets, or "fail" to refuse the
	// compose. It comes from the default key among the variants.
	Default string
	// Env are the environment variables the module exports, name to a path inside the
	// module relative to its root, cleaned, "." for the root itself. The compose turns
	// each into $QORY_HARNESS_HOME/modules/<name>/<path> and the runtimes with a place
	// for environment write them. A key is a POSIX environment variable name other than
	// QORY_HARNESS_HOME.
	Env map[string]string
	// Requires is what the module's entries need composed beside them: per entry this
	// module ships, keyed <kind>/<name>, the entries it needs as <kind>/<name>, sorted.
	// In the file each is one item that selects the entry by its singular kind and what
	// it needs by the plural kinds an exclude uses, see [ReadManifest]. A required entry
	// may come from any module; the compose refuses a stack that leaves one out.
	Requires map[string][]string
	// Egress are the hosts the module's skills, hooks and servers reach, sorted, each
	// once: a lower-case host name, or "*." followed by a name for every host below it,
	// in the grammar the Forager contract defines for a policy's allow list. Nil when the
	// manifest has no egress key; empty, and not nil, when it declares an empty list,
	// which states that the module reaches nothing. Forager distinguishes the two.
	Egress []string
}

// EgressHost is the grammar of a declared host, copied from the Forager contract's
// policy.schema.json, egress.allow items, which defines it once for both contracts.
var EgressHost = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// rawManifest decodes qory-module.yaml as it is written, where the variants map contains
// both the variants and the default, a string among the maps. [ReadManifest] splits the
// two apart.
type rawManifest struct {
	APIVersion  string                 `yaml:"apiVersion"`
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description,omitempty"`
	Variants    map[string]yaml.Node   `yaml:"variants,omitempty"`
	Env         map[string]string      `yaml:"env,omitempty"`
	Requires    []map[string]yaml.Node `yaml:"requires,omitempty"`
	Egress      *[]string              `yaml:"egress,omitempty"`
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

// Describe returns an entry's text for a message the way a requires item writes it,
// "skill deploy".
func Describe(kind, name string) string { return singularOf(kind) + " " + name }

// Entry is one atomic entry: a skill, an agent, a command, an output style, a hook script
// or an MCP server.
type Entry struct {
	// Kind is one of skills, agents, commands, output-styles, hooks, mcp and files.
	Kind string
	// Name identifies the entry within its kind: the skill directory name, the Markdown
	// file name without its extension, the hook file name with its extension, the MCP
	// server's file name without .json, or a file's <runtime>/<path> under files/.
	Name string
	// Path is absolute: the skill directory, the Markdown file, the hook script, the MCP
	// server's JSON file or the file itself.
	Path string
	// References are the entries the entry's documents reference, as sorted <kind>/<name>
	// keys, each once, see [References]; nil for an entry that references none.
	References []string
	// Tier is the tier an agent's frontmatter model sets as a placeholder, one of
	// [Tiers]; "" for an agent with a model id or none, and for every other kind.
	Tier string
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
	// Settings are the settings/<runtime>/<file> fragments: per runtime, per target file,
	// the absolute path of the one fragment this module contributes to it.
	Settings map[string]map[string]string
	// MCP contains the MCP servers by name, each the JSON object its mcp/<name>.json file
	// contains, with every $QORY_HARNESS_HOME as written, before substitution.
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
				return "", nil, fmt.Errorf("%s selects no entry; it is the entry's name, as %s: <name>", k, k)
			}
			if entry != "" {
				return "", nil, fmt.Errorf("selects two entries; an item selects one, as skill: <name>, and lists what it needs under skills, agents, commands, output-styles, hooks, mcp and files")
			}
			entry = kind + "/" + name
			continue
		}
		if !isKind(k) {
			return "", nil, fmt.Errorf("key %s is not one requires reads; an item selects its entry as skill, agent, command, output-style, hook, mcp or file, and what it needs under skills, agents, commands, output-styles, hooks, mcp and files", accesskey.ShownName(k, true))
		}
		var names []string
		if err := node.Decode(&names); err != nil || len(names) == 0 {
			return "", nil, fmt.Errorf("%s is empty; it lists the %s the entry needs", k, k)
		}
		for _, name := range names {
			if name == "" {
				return "", nil, fmt.Errorf("%s lists an empty entry", k)
			}
			needs = append(needs, k+"/"+name)
		}
	}
	if entry == "" {
		return "", nil, fmt.Errorf("selects no entry; an item selects one, as skill: <name>, and lists what it needs under skills, agents, commands, output-styles, hooks, mcp and files")
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

// ReadManifest reads and validates qory-module.yaml at dir. A directory without one is
// not a module, and the error states that and contains the directory, because a source
// that points at the wrong directory is the mistake this catches.
//
// These are errors: an unknown field, an apiVersion other than [stack.APIVersion] or a
// retired spelling of it, a missing name, a default that selects something other than
// a declared variant or "fail", an env key that is not an environment variable name or
// is QORY_HARNESS_HOME, and an env value that is not a relative path inside the module.
// Every error contains the manifest path. A name the file writes that contains an access
// key secret or a run key, a key of env, of variants or of a requires item, an entry a
// requires item lists, and the variant default selects, is printed as
// [accesskey.ShownName] prints it, and a value it writes as [exports.Value] prints it.
// The env values come back cleaned, "." for the root.
//
// A requires item selects one entry of the module by its singular kind and lists what it
// needs under the plural kinds:
//
//	requires:
//	  - skill: deploy
//	    commands: [ship]
//	    agents: [reviewer]
//
// An item with no entry or two, a key that is neither, an empty list, and an entry listed
// twice are errors. Whether the entry is one the module ships is [Read]'s check, since
// the entries are read then. The result is keyed <kind>/<name> with sorted values.
func ReadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestName))
	return parseManifest(dir, data, err)
}

// ReadManifestIn is [ReadManifest] for the module directory dir opened as root: it reads
// qory-module.yaml through root, so the file comes from the directory root was opened
// at, even when a link on the way to dir leads elsewhere since. dir is what the errors
// print.
func ReadManifestIn(root *os.Root, dir string) (*Manifest, error) {
	data, err := root.ReadFile(ManifestName)
	return parseManifest(dir, data, err)
}

// parseManifest is the manifest of the module directory dir in data, the bytes a read of
// its qory-module.yaml returned, or the refusal of err, that read's error.
func parseManifest(dir string, data []byte, err error) (*Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s has no %s; a module has one that defines its name", exports.HidePath(dir), ManifestName)
	}
	// The directory of a module listed by name is that name, which the errors print as
	// exports.HidePath prints it.
	if err != nil {
		return nil, exports.HideError(err)
	}
	path = exports.HidePath(path)
	raw := rawManifest{}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, exports.DecodeError(path, err)
	}
	current, err := exports.ResolveAPIVersion(raw.APIVersion)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, exports.HideValueError(err, ""))
	}
	if raw.Name == "" {
		return nil, fmt.Errorf("%s: name is required", path)
	}
	m := &Manifest{APIVersion: current, Name: raw.Name, Description: raw.Description}
	if current != raw.APIVersion {
		m.RetiredAPIVersion = raw.APIVersion
	}
	for key, value := range raw.Env {
		if key == "QORY_HARNESS_HOME" {
			return nil, fmt.Errorf("%s: env.QORY_HARNESS_HOME is qory's; a module exports another name", path)
		}
		if !envName.MatchString(key) {
			return nil, fmt.Errorf("%s: env: %s is not an environment variable name", path, accesskey.ShownName(key, false))
		}
		clean := filepath.Clean(value)
		if value == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s: env.%s: %s is not a path inside the module", path, accesskey.ShownName(key, false), exports.Value(value, false))
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
			return nil, fmt.Errorf("%s: requires lists %s twice", path, Describe(kind, accesskey.ShownName(name, false)))
		}
		if m.Requires == nil {
			m.Requires = map[string][]string{}
		}
		m.Requires[entry] = needs
	}
	if raw.Egress != nil {
		m.Egress = []string{}
		for _, host := range *raw.Egress {
			if !EgressHost.MatchString(host) {
				return nil, fmt.Errorf("%s: egress: %s is not a lower-case host name or a *. suffix; no port, path or scheme", path, exports.Value(host, true))
			}
			if !slices.Contains(m.Egress, host) {
				m.Egress = append(m.Egress, host)
			}
		}
		sort.Strings(m.Egress)
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
			// The decoder quotes up to ten characters of a value that is not a map.
			return nil, fmt.Errorf("%s: variants.%s: %w", path, accesskey.ShownName(name, false), exports.HideValueError(err, ""))
		}
		if m.Variants == nil {
			m.Variants = map[string]Variant{}
		}
		m.Variants[name] = v
	}
	if m.Default != "" && m.Default != "fail" {
		if _, ok := m.Variants[m.Default]; !ok {
			return nil, fmt.Errorf("%s: variants.default selects %s, which is not a variant", path, accesskey.ShownName(m.Default, true))
		}
	}
	return m, nil
}

// SelectVariant picks the variant for a runtime: the forced one from the stack, else the
// one named like the runtime, else the manifest's default. A module with no manifest and
// a manifest that declares no variant serve every runtime from the module root, and
// return "".
//
// It returns an error when the stack forces a variant the module does not have, or forces
// one on a module that declares none, and when the module has no variant for the runtime
// and its default is missing or "fail". Every message lists the variant names the module
// has. A variant's name and the runtime's are printed as [accesskey.ShownName] prints
// them.
func SelectVariant(m *Manifest, forced, runtime string) (string, error) {
	if m == nil || len(m.Variants) == 0 {
		if forced != "" {
			return "", fmt.Errorf("variant %s is forced, and the module declares no variants", accesskey.ShownName(forced, true))
		}
		return "", nil
	}
	if forced != "" {
		if _, ok := m.Variants[forced]; !ok {
			return "", fmt.Errorf("variant %s is forced, and the module has no such variant; variants: %s", accesskey.ShownName(forced, true), names(m.Variants))
		}
		return forced, nil
	}
	if _, ok := m.Variants[runtime]; ok {
		return runtime, nil
	}
	if m.Default == "fail" {
		return "", fmt.Errorf("the module has no variant for runtime %s and its default is fail; variants: %s", accesskey.ShownName(runtime, false), names(m.Variants))
	}
	if m.Default == "" {
		return "", fmt.Errorf("the module has no variant for runtime %s and declares no default; variants: %s", accesskey.ShownName(runtime, false), names(m.Variants))
	}
	return m.Default, nil
}

// names lists the variant names, sorted, for the error messages of [SelectVariant], each
// as [accesskey.ShownName] prints it.
func names(v map[string]Variant) string {
	ns := make([]string, 0, len(v))
	for n := range v {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	for i, n := range ns {
		ns[i] = accesskey.ShownName(n, false)
	}
	return strings.Join(ns, ", ")
}

// Read scans the module at dir for one variant and returns what it ships. The name is the
// stack's name for the module and appears in every error, as [accesskey.ShownName]
// prints it. The variant is the one
// [SelectVariant] returned for m; "" reads every kind from the module root.
//
// A kind the variant redirects is read from the directory it sets, which must be a
// relative directory inside the module: an empty value, ".", an absolute path and a path
// leaving the module are all refused. A variant that sets a kind outside the seven known
// kinds is refused too.
//
// A kind directory that is not there leaves the module without entries of that kind. A
// skills subdirectory without SKILL.md is an error. In the Markdown kinds only .md files
// count. Under hooks every file is a hook and a directory is an error, because a hook is
// identified by its file name and a script's helpers belong elsewhere in the module,
// reached as $QORY_HARNESS_HOME/modules/<name>/<path>. Under mcp every .json file is one
// server and must contain a JSON object. Under files every file below a runtime directory
// is one entry named <runtime>/<path>, at any depth, and a file directly under files is
// an error. A name starting with a dot is skipped everywhere. Settings are read from
// settings/ at the module root, which no variant redirects, and AGENTS.md from the root
// as well. A path inside the module that an error prints, and the path in an error of the
// file system, are printed as [exports.HidePath] prints them.
func Read(name, dir string, m *Manifest, variant string) (*Module, error) {
	return ReadWith(name, dir, m, variant, os.ReadFile)
}

// ReadWith is [Read] with readFile reading the bytes of each settings fragment, AGENTS.md,
// MCP server, hook and files entry, such as [ReadThrough] for a module the home holds a
// copy of. Which files there are is read by path.
func ReadWith(name, dir string, m *Manifest, variant string, readFile func(path string) ([]byte, error)) (*Module, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	l := &Module{Name: name, Dir: abs, Manifest: m, Variant: variant}
	// shown is the module's name as the errors print it.
	shown := accesskey.ShownName(name, false)
	dirs := map[string]string{"skills": "skills", "agents": "agents", "commands": "commands", "output-styles": "output-styles", "hooks": "hooks", "mcp": "mcp", "files": "files"}
	if m != nil && variant != "" {
		for kind, sub := range m.Variants[variant] {
			if _, ok := dirs[kind]; !ok {
				return nil, fmt.Errorf("module %s: variant %s sets kind %s", shown, accesskey.ShownName(variant, false), accesskey.ShownName(kind, true))
			}
			clean := filepath.Clean(sub)
			if sub == "" || clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("module %s: variant %s reads %s from %s, which is not a directory inside the module", shown, accesskey.ShownName(variant, false), kind, exports.Value(sub, true))
			}
			dirs[kind] = clean
		}
	}
	skills, err := readSkills(dirs["skills"], filepath.Join(abs, dirs["skills"]))
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", shown, err)
	}
	l.Entries = append(l.Entries, skills...)
	for _, kind := range []string{"agents", "commands", "output-styles"} {
		es, err := readMarkdown(kind, filepath.Join(abs, dirs[kind]))
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", shown, err)
		}
		l.Entries = append(l.Entries, es...)
	}
	hooks, err := readHooks(shown, dirs["hooks"], filepath.Join(abs, dirs["hooks"]))
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", shown, err)
	}
	l.Entries = append(l.Entries, hooks...)
	servers, mcp, err := readMCP(dirs["mcp"], filepath.Join(abs, dirs["mcp"]), readFile)
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", shown, err)
	}
	l.Entries = append(l.Entries, servers...)
	l.MCP = mcp
	files, err := readFiles(dirs["files"], filepath.Join(abs, dirs["files"]))
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", shown, err)
	}
	l.Entries = append(l.Entries, files...)
	for i, e := range l.Entries {
		if err := inside(abs, e.Path); err != nil {
			if out := linkOut(shown, exports.HidePath(e.Kind+"/"+e.Name), err); out != nil {
				return nil, out
			}
			return nil, fmt.Errorf("module %s: %s %w", shown, exports.HidePath(e.Kind+"/"+e.Name), err)
		}
		if e.Kind == "skills" {
			if err := insideAll(abs, e.Path); err != nil {
				if out := linkOut(shown, exports.HidePath("skills/"+e.Name), err); out != nil {
					return nil, out
				}
				return nil, fmt.Errorf("module %s: %s/%w", shown, exports.HidePath("skills/"+e.Name), err)
			}
		}
		refs, tier, err := references(abs, e)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", shown, err)
		}
		l.Entries[i].References = refs
		l.Entries[i].Tier = tier
	}
	sort.Slice(l.Entries, func(i, j int) bool {
		if l.Entries[i].Kind != l.Entries[j].Kind {
			return l.Entries[i].Kind < l.Entries[j].Kind
		}
		return l.Entries[i].Name < l.Entries[j].Name
	})
	// Hooks, servers and files are not read for references; a tier placeholder in one is
	// refused all the same. The entries are sorted, so the first one is the same each run.
	for _, e := range l.Entries {
		if e.Kind != "hooks" && e.Kind != "mcp" && e.Kind != "files" {
			continue
		}
		data, err := readFile(e.Path)
		if err != nil {
			return nil, exports.HideError(err)
		}
		if err := NoTier(string(data)); err != nil {
			rel, _ := filepath.Rel(abs, e.Path)
			return nil, fmt.Errorf("module %s: %s: %w", shown, exports.HidePath(filepath.ToSlash(rel)), err)
		}
	}
	if m != nil {
		shipped := map[string]bool{}
		for _, e := range l.Entries {
			shipped[e.Kind+"/"+e.Name] = true
		}
		for _, key := range sortedKeys(m.Requires) {
			if !shipped[key] {
				kind, entry, _ := strings.Cut(key, "/")
				return nil, fmt.Errorf("module %s: requires lists %s, which the module does not ship", shown, Describe(kind, accesskey.ShownName(entry, false)))
			}
		}
	}
	l.Settings, err = readSettings(filepath.Join(abs, "settings"))
	if err != nil {
		return nil, fmt.Errorf("module %s: %w", shown, err)
	}
	for _, runtime := range slices.Sorted(maps.Keys(l.Settings)) {
		files := l.Settings[runtime]
		for _, file := range slices.Sorted(maps.Keys(files)) {
			path := files[file]
			if err := inside(abs, path); err != nil {
				if out := linkOut(shown, exports.HidePath("settings/"+runtime+"/"+file), err); out != nil {
					return nil, out
				}
				return nil, fmt.Errorf("module %s: %s %w", shown, exports.HidePath("settings/"+runtime+"/"+file), err)
			}
			data, err := readFile(path)
			if err != nil {
				return nil, exports.HideError(err)
			}
			if err := NoTier(string(data)); err != nil {
				return nil, fmt.Errorf("module %s: %s: %w", shown, exports.HidePath("settings/"+runtime+"/"+file), err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(abs, InstructionsName)); err == nil {
		l.Instructions = filepath.Join(abs, InstructionsName)
		if err := inside(abs, l.Instructions); err != nil {
			if out := linkOut(shown, InstructionsName, err); out != nil {
				return nil, out
			}
			return nil, fmt.Errorf("module %s: %s %w", shown, InstructionsName, err)
		}
		data, err := readFile(l.Instructions)
		if err != nil {
			return nil, exports.HideError(err)
		}
		if err := NoTier(string(data)); err != nil {
			return nil, fmt.Errorf("module %s: %s: %w", shown, InstructionsName, err)
		}
	}
	return l, nil
}

// ReadThrough returns a read of a file at an absolute path inside the module directory
// dir through root, dir opened as a root, see [ReadWith]: the bytes come from the
// directory root was opened at, whatever a link on the way to dir leads to since. Each
// link on the path is followed through root alone, as [ResolveIn] follows it, with
// realDir the real path of root's directory as recorded when root was opened.
func ReadThrough(root *os.Root, dir, realDir string) func(path string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, fmt.Errorf("Move %s inside the module. It lies outside the module, and qory reads only the module's files.", exports.HidePath(path))
		}
		inner, err := ResolveIn(root, realDir, rel)
		if err != nil {
			return nil, err
		}
		data, err := root.ReadFile(inner)
		return data, exports.HideError(err)
	}
}

// maxLinks is the most links [ResolveIn] follows for one path, as many as Linux follows.
const maxLinks = 40

// ResolveIn is rel, a path relative to the directory root was opened at, with every link
// on it followed through root: a relative link as root follows it, and an absolute one
// only when its target lies under realDir, the real path of root's directory as recorded
// when root was opened, at that part of the target. Nothing is resolved outside root. A
// link that leads out of the module, an absolute link when realDir is "", and a path with
// more than 40 links are refused. The result contains no link, as root showed it.
func ResolveIn(root *os.Root, realDir, rel string) (string, error) {
	out := func() error {
		return fmt.Errorf("%s leads out of the module", exports.HidePath(filepath.ToSlash(rel)))
	}
	pending := splitPath(rel)
	var cur []string
	for hops := 0; len(pending) > 0; {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case ".":
			continue
		case "..":
			if len(cur) == 0 {
				return "", out()
			}
			cur = cur[:len(cur)-1]
			continue
		}
		next := filepath.Join(append(cur[:len(cur):len(cur)], part)...)
		info, err := root.Lstat(next)
		if err != nil {
			return "", exports.HideError(err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			cur = append(cur, part)
			continue
		}
		if hops++; hops > maxLinks {
			return "", fmt.Errorf("%s passes more than %d links", exports.HidePath(filepath.ToSlash(rel)), maxLinks)
		}
		target, err := root.Readlink(next)
		if err != nil {
			return "", exports.HideError(err)
		}
		if filepath.IsAbs(target) {
			prefix := realDir + string(filepath.Separator)
			if realDir == "" || target != realDir && !strings.HasPrefix(target, prefix) {
				return "", out()
			}
			cur = nil
			target = strings.TrimPrefix(strings.TrimPrefix(target, realDir), string(filepath.Separator))
		}
		pending = append(splitPath(target), pending...)
	}
	if len(cur) == 0 {
		return ".", nil
	}
	return filepath.Join(cur...), nil
}

// splitPath is the names of path between separators, without empty ones, as written:
// a ".." is kept, since the name before it may be a link.
func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, string(filepath.Separator)) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// inside checks that path, with every symlink resolved, is under the module root. A
// module may link an entry to elsewhere in itself; a link that leaves the module is
// refused, since the harness reads through it a file the report does not show.
func inside(root, path string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return exports.HideError(err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return exports.HideError(err)
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
		return &outsideError{real: exports.HidePath(real)}
	}
	return nil
}

// outsideError is the refusal of [inside]: real, the path with every link resolved,
// lies outside the module. under is the path below a skill's directory that
// [insideAll] found, or "".
type outsideError struct {
	real  string
	under string
}

func (e *outsideError) Error() string { return "links to " + e.real + ", outside the module" }

// linkOut is the refusal of the entry shown as name in the module shown when err is an
// [outsideError], with its fix first, and nil for any other err.
func linkOut(shown, name string, err error) error {
	var out *outsideError
	if !errors.As(err, &out) {
		return nil
	}
	if out.under != "" {
		name += "/" + out.under
	}
	return fmt.Errorf("module %s: Remove the link %s or point it inside the module. It links to %s, outside the module, and qory reads only the module's files.", shown, name, out.real)
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
			return exports.HideError(err)
		}
		if path == dir {
			return nil
		}
		if err := inside(root, path); err != nil {
			rel, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				rel = path
			}
			if out, ok := err.(*outsideError); ok {
				return &outsideError{real: out.real, under: exports.HidePath(filepath.ToSlash(rel))}
			}
			return fmt.Errorf("%s %w", exports.HidePath(filepath.ToSlash(rel)), err)
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
		return nil, exports.HideError(err)
	}
	var es []Entry
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		info, err := os.Stat(path)
		if err != nil {
			return nil, exports.HideError(err)
		}
		if !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
			return nil, fmt.Errorf("%s has no SKILL.md", exports.HidePath(filepath.ToSlash(rel)+"/"+it.Name()))
		}
		es = append(es, Entry{Kind: "skills", Name: it.Name(), Path: path})
	}
	return es, nil
}

// readMarkdown lists the .md files of dir as entries of kind, named without the
// extension. A directory, a link to one, or any other extension in there is not an entry
// of this kind.
func readMarkdown(kind, dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, exports.HideError(err)
	}
	var es []Entry
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") || !strings.HasSuffix(it.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		info, err := os.Stat(path)
		if err != nil {
			return nil, exports.HideError(err)
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
		return nil, exports.HideError(err)
	}
	var es []Entry
	for _, rt := range runtimes {
		if strings.HasPrefix(rt.Name(), ".") {
			continue
		}
		root := filepath.Join(dir, rt.Name())
		info, err := os.Stat(root)
		if err != nil {
			return nil, exports.HideError(err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not under a runtime directory; a file ships as %s/<runtime>/<path>", exports.HidePath(filepath.ToSlash(rel)+"/"+rt.Name()), exports.HidePath(filepath.ToSlash(rel)))
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
			return nil, exports.HideError(err)
		}
	}
	return es, nil
}

// readSettings lists settings/<runtime>/<file>: the directory name is the runtime, the
// file name is the target file that runtime reads, and a module contributes at most one
// fragment per target file. A runtime directory holding no file at all is left out of the
// map. A link to a directory counts as the directory, the way the other readers count
// one. A directory under a runtime's directory is an error rather than a skip, so a
// module that keeps a runtime's other files there learns where they go:
// files/<runtime>/<path>.
func readSettings(dir string) (map[string]map[string]string, error) {
	runtimes, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, exports.HideError(err)
	}
	out := map[string]map[string]string{}
	for _, p := range runtimes {
		if strings.HasPrefix(p.Name(), ".") {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, p.Name()))
		if err != nil {
			return nil, exports.HideError(err)
		}
		if !info.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dir, p.Name()))
		if err != nil {
			return nil, exports.HideError(err)
		}
		for _, f := range files {
			if strings.HasPrefix(f.Name(), ".") {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, p.Name(), f.Name()))
			if err != nil {
				return nil, exports.HideError(err)
			}
			if info.IsDir() {
				sub := exports.HidePath(p.Name() + "/" + f.Name())
				return nil, fmt.Errorf("settings/%s is a directory; settings contains one fragment per target file, and a runtime's other files go under files/%s", sub, sub)
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
// module is reached. name is the module's name as the errors print it, and rel the hooks
// directory relative to the module root.
func readHooks(name, rel, dir string) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, exports.HideError(err)
	}
	var es []Entry
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		info, err := os.Stat(path)
		if err != nil {
			return nil, exports.HideError(err)
		}
		if info.IsDir() {
			sub := exports.HidePath(filepath.ToSlash(filepath.Join(rel, it.Name())))
			return nil, fmt.Errorf("%s is a directory; a hook is one file. Keep its helpers elsewhere in the module, reached as $QORY_HARNESS_HOME/modules/%s/<path>", sub, name)
		}
		es = append(es, Entry{Kind: "hooks", Name: it.Name(), Path: path})
	}
	return es, nil
}

// serverKeys are the keys an MCP server object may contain, the ones Claude Code's
// .mcp.json reads: the transport, the command with its arguments and environment for a
// stdio server, the URL with its headers for a remote one, and a description for the
// report, which the compose drops on emit. Any other key is refused, the way every other
// document of the contract refuses one.
var serverKeys = map[string]bool{"type": true, "command": true, "args": true, "env": true, "url": true, "headers": true, "description": true}

// readMCP lists mcp/<name>.json as MCP server entries named without the extension, and
// returns each file's object by name. A file that is not one JSON object, or an object
// [checkServer] refuses, is an error naming the file as the variant reads it; when the
// file does not parse, the message keeps the decoder's message, which names the offset. A
// directory or a file of another extension is not a server. An error of the file system
// prints its path as [exports.HideError] prints it.
func readMCP(rel, dir string, readFile func(string) ([]byte, error)) ([]Entry, map[string]map[string]any, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, exports.HideError(err)
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
			return nil, nil, exports.HideError(err)
		}
		if info.IsDir() {
			continue
		}
		data, err := readFile(path)
		if err != nil {
			return nil, nil, exports.HideError(err)
		}
		// The file's name is the server's name, which errors print as
		// accesskey.ShownName prints it.
		file := filepath.ToSlash(filepath.Join(rel, accesskey.ShownName(strings.TrimSuffix(it.Name(), ".json"), false)+".json"))
		var server map[string]any
		if err := json.Unmarshal(data, &server); err != nil {
			return nil, nil, fmt.Errorf("%s does not contain a JSON object: %w", file, err)
		}
		if server == nil {
			return nil, nil, fmt.Errorf("%s does not contain a JSON object", file)
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
// string. A value the refusal prints is printed as [shownJSON] prints it.
func checkServer(server map[string]any) error {
	for k := range server {
		if !serverKeys[k] {
			return fmt.Errorf("key %s is not one of an MCP server's; keys: type, command, args, env, url, headers, description", accesskey.ShownName(k, true))
		}
	}
	command, hasCommand := server["command"]
	url, hasURL := server["url"]
	switch {
	case hasCommand && hasURL:
		return errors.New("sets both a command and a url; a server is one or the other")
	case !hasCommand && !hasURL:
		return errors.New("sets neither a command nor a url")
	case hasCommand && !nonEmptyString(command):
		return errors.New("command is not a non-empty string")
	case hasURL && !nonEmptyString(url):
		return errors.New("url is not a non-empty string")
	}
	if t, ok := server["type"]; ok {
		if s, _ := t.(string); s != "stdio" && s != "http" && s != "sse" {
			return fmt.Errorf("type %s is not stdio, http or sse", shownJSON(t))
		}
	}
	if args, ok := server["args"]; ok {
		list, isList := args.([]any)
		if !isList {
			return errors.New("args is not a list")
		}
		for _, a := range list {
			if _, ok := a.(string); !ok {
				return fmt.Errorf("args contains %s, which is not a string", shownJSON(a))
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
				return fmt.Errorf("%s.%s is %s, which is not a string", key, accesskey.ShownName(k, false), shownJSON(e))
			}
		}
	}
	if d, ok := server["description"]; ok {
		if _, isString := d.(string); !isString {
			return fmt.Errorf("description is %s, which is not a string", shownJSON(d))
		}
	}
	return nil
}

// shownJSON is a value of a server object as a refusal prints it: as %v formats it, or,
// when that text contains an access key secret or a run key, as [exports.Value] prints
// it. A map or a list prints its keys and values, so a secret in one is left out too.
func shownJSON(v any) string { return exports.Value(fmt.Sprint(v), false) }

// nonEmptyString reports whether v is a string with at least one character.
func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s != ""
}
