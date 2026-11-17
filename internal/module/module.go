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
	"unicode/utf8"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/runapi"
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
	// Roles are the roles the module's documents reference, by kind and then by role, each
	// set to one, for a role one entry fills, or many, for a group role several agents
	// fill. Only an agents role can be many. Nil when the manifest has no roles key.
	Roles stack.Roles
	// Provides are the roles the module fills, by kind and then by role: a role to the
	// name of the entry that fills it, a group role to a list of agents it adds to the
	// group. The compose sets a role from the modules' provides after the base's bind
	// and before the checkout's. Zero when the manifest has none.
	Provides stack.Fills
	// PushChecks are the checks the module declares for the files of an artifact type,
	// one per type, in the order of the file; nil when the manifest has none.
	PushChecks []PushCheck
	// SubmitProgram is the program of the submit step the module ships, nil when the
	// manifest has no submit_program key. See [SubmitProgram].
	SubmitProgram *SubmitProgram
	// Contracts are the role contracts the module declares, each the path of the
	// contract file inside the module, cleaned, keyed <kind>/<role>: the path in the
	// role's map form, roles: {agents: {developer: {fill: one, contract: <path>}}}. Nil
	// when the manifest declares none. See [Contract].
	Contracts map[string]string
}

// PushCheck is one check a module declares for the files of one artifact type.
type PushCheck struct {
	// Type is the artifact type: lowercase letters and digits in groups joined by single
	// hyphens, at most 64 bytes.
	Type string
	// Command is the program and its arguments. The program is a path inside the module,
	// relative to its root, cleaned.
	Command []string
	// Reads are the other artifact types the check reads, each once, in the order of the
	// file; nil for none.
	Reads []string
}

// SubmitProgram is the program a module ships for the submit step: it reads the
// submission and writes the forge's links and draft into it. A composed harness has at
// most one.
type SubmitProgram struct {
	// Command is the program and its arguments. The program is a path inside the module,
	// relative to its root, cleaned, with no .. part.
	Command []string
}

// InsideModule reports whether path is a relative path that stays inside the module:
// not empty, not absolute, and with no .. part. It returns the path cleaned.
func InsideModule(path string) (string, bool) {
	clean := filepath.Clean(path)
	if path == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return clean, true
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
	Roles       yaml.Node              `yaml:"roles,omitempty"`
	Provides    yaml.Node              `yaml:"provides,omitempty"`
	PushChecks  []rawPushCheck         `yaml:"push_checks,omitempty"`
	// SubmitProgram is the submit_program item as it is written; nil when absent.
	SubmitProgram *rawSubmitProgram `yaml:"submit_program,omitempty"`
}

// rawSubmitProgram is the submit_program item as it is written. The command's items stay
// nodes for the reason [rawPushCheck] gives.
type rawSubmitProgram struct {
	Command []yaml.Node `yaml:"command"`
}

// rawPushCheck is one push_checks item as it is written. The command's items stay nodes,
// so a number or a boolean among them fails the read as it fails the schema, where a
// decode into strings would take it.
type rawPushCheck struct {
	Type    string      `yaml:"type"`
	Command []yaml.Node `yaml:"command"`
	Reads   []yaml.Node `yaml:"reads"`
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
	// Front is, per key of References that a document's frontmatter references, the
	// first such document, relative to the module root with forward slashes; nil for
	// none.
	Front map[string]string
	// In is, per key of References, the first document that references it, relative to
	// the module root with forward slashes, in the order [Documents] lists the files.
	In map[string]string
	// Paths are the globs an agent's frontmatter paths sets, the files of a change the
	// agent reads, from the checkout root; nil for an agent without the key, which reads
	// every change, and for every other kind. A renderer leaves the key out of the
	// agent's file.
	Paths []string
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

// readRequirement reads one requires item, the item at index i of the list: the entry it
// names, as <kind>/<name>, and what the entry needs, as sorted <kind>/<name> keys. The mcp
// key is the entry's name when it is a string, and the needed servers when it is a list. A
// refusal has [exports.FilePlace], for the manifest's path, and starts with its fix.
func readRequirement(item map[string]yaml.Node, i int) (entry string, needs []string, err error) {
	at := fmt.Sprintf("requires[%d]", i)
	const kinds = "skills, agents, commands, output-styles, hooks, mcp, and files"
	refuse := func(fix string, lines ...string) error {
		return exports.Refuse(fix, append(lines, "The manifest is "+exports.FilePlace+".")...)
	}
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
				return "", nil, refuse(fmt.Sprintf("Set %s in %s to the entry's name, as %s: <name>.", k, at, k),
					fmt.Sprintf("The value of %s in %s is empty or not a string.", k, at))
			}
			if entry != "" {
				return "", nil, refuse(fmt.Sprintf("Remove one of the two entries from %s.", at),
					"An item selects one entry, as skill: <name>, and lists what it needs under "+kinds+".")
			}
			entry = kind + "/" + name
			continue
		}
		if !isKind(k) {
			return "", nil, refuse(fmt.Sprintf("Remove the key %s from %s.", accesskey.ShownName(k, true), at),
				"An item selects its entry as skill, agent, command, output-style, hook, mcp, or file, and lists what it needs under "+kinds+".")
		}
		var names []string
		if err := node.Decode(&names); err != nil || len(names) == 0 {
			return "", nil, refuse(fmt.Sprintf("List the %s the entry needs under %s in %s, or remove %s.", k, k, at, k),
				fmt.Sprintf("%s in %s is empty.", k, at))
		}
		for _, name := range names {
			if name == "" {
				return "", nil, refuse(fmt.Sprintf("Remove the empty entry from %s in %s.", k, at),
					fmt.Sprintf("%s in %s lists an empty entry.", k, at))
			}
			needs = append(needs, k+"/"+name)
		}
	}
	if entry == "" {
		return "", nil, refuse(fmt.Sprintf("Select an entry in %s, as skill: <name>.", at),
			"An item selects one entry, as skill: <name>, and lists what it needs under "+kinds+".")
	}
	if len(needs) == 0 {
		return "", nil, refuse(fmt.Sprintf("List what the entry needs in %s.", at),
			"Use "+strings.Replace(kinds, "and files", "or files", 1)+".",
			fmt.Sprintf("%s lists nothing the entry needs.", at))
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
// is QORY_HARNESS_HOME, an env value that is not a relative path inside the module,
// roles or provides for which [stack.ReadRoles] returns an error, and a push_checks item whose type is not an artifact type or repeats one,
// or whose command is empty or does not start with a relative path inside the module
// with no .. part, or whose reads lists a value that is not an artifact type, or one
// type twice. Every error contains the manifest path; an error about roles or
// provides contains it inside the text, after the fix. A name the file writes that contains an access
// key secret or a run key, a key of env, of variants or of a requires item, an entry a
// requires item lists, and the variant default selects, is printed as
// [accesskey.ShownName] prints it, and a value it writes as [exports.Value] prints it.
// The env values come back cleaned, "." for the root.
//
// An agents role under roles may be a map, {fill: one, contract: <path>}, which reads as
// its fill, one or many, with the contract's path in [Manifest.Contracts]. A map with
// another key or without fill, a contract that is not a path inside the module, and a
// map for a role that is not an agents role are errors.
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

// CausedRefusal is a [exports.Refusal] that unwraps to the error that caused it, so a caller
// still matches that error, such as a *yaml.TypeError, with errors.As. The refusal's text
// has the cause only as the caller printed it, with any value that contains a secret left
// out, and the cause under Unwrap is as [exports.HideValueError] returns it.
type CausedRefusal struct {
	*exports.Refusal
	cause error
}

// Caused is a [CausedRefusal] of cause with the fix and the lines after it, as
// [exports.Refuse] writes them.
func Caused(cause error, fix string, lines ...string) error {
	return &CausedRefusal{exports.Refuse(fix, lines...), cause}
}

// Unwrap returns the error that caused the refusal.
func (c *CausedRefusal) Unwrap() error { return c.cause }

// In is the refusal with file at [exports.FilePlace], as [exports.Refusal.In].
func (c *CausedRefusal) In(file string) error {
	return &CausedRefusal{c.Refusal.In(file).(*exports.Refusal), c.cause}
}

// Prefixed is err with prefix at [exports.ModulePlace] when err is a [CausedRefusal], and
// otherwise as [exports.Prefixed] returns it.
func Prefixed(prefix string, err error) error {
	if c, ok := err.(*CausedRefusal); ok {
		return &CausedRefusal{exports.Prefixed(prefix, c.Refusal).(*exports.Refusal), c.cause}
	}
	return exports.Prefixed(prefix, err)
}

// parseManifest is the manifest of the module directory dir in data, the bytes a read of
// its qory-module.yaml returned, or the refusal of err, that read's error.
func parseManifest(dir string, data []byte, err error) (*Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, exports.Refuse("Add "+ManifestName+" to the module directory.",
			fmt.Sprintf("%s has no %s, and a module has one that defines its name.", exports.HidePath(dir), ManifestName))
	}
	// The directory of a module listed by name is that name, which the errors print as
	// exports.HidePath prints it.
	if err != nil {
		return nil, exports.ReadErrorOf(ManifestName+" of "+exports.ModulePlace, path, err)
	}
	path = exports.HidePath(path)
	refuse := func(fix string, lines ...string) error {
		return exports.Refuse(fix, append(lines, "The manifest is "+path+".")...)
	}
	// caused is refuse for a refusal that unwraps to err, with its text as HideValueError prints it.
	caused := func(err error, fix string) error {
		err = exports.HideValueError(err, "")
		return Caused(err, fix, err.Error(), "The manifest is "+path+".")
	}
	raw := rawManifest{}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, exports.DecodeError(path, err)
	}
	current, err := exports.ResolveAPIVersion(raw.APIVersion)
	if err != nil {
		return nil, exports.HideValueError(exports.AtPath(path, err), "")
	}
	if raw.Name == "" {
		return nil, refuse("Add name to "+ManifestName+".", "A manifest defines the module's name.")
	}
	m := &Manifest{APIVersion: current, Name: raw.Name, Description: raw.Description}
	if current != raw.APIVersion {
		m.RetiredAPIVersion = raw.APIVersion
	}
	for key, value := range raw.Env {
		if key == "QORY_HARNESS_HOME" {
			return nil, refuse("Remove env.QORY_HARNESS_HOME from the manifest.", "qory sets QORY_HARNESS_HOME, so a module exports another name.")
		}
		if !envName.MatchString(key) {
			return nil, refuse(fmt.Sprintf("Rename the key %s in env of the manifest.", accesskey.ShownName(key, false)),
				fmt.Sprintf("%s is not an environment variable name.", accesskey.ShownName(key, false)))
		}
		clean, ok := InsideModule(value)
		if !ok {
			return nil, refuse(fmt.Sprintf("Set env.%s to a path inside the module.", accesskey.ShownName(key, false)),
				fmt.Sprintf("%s is not a path inside the module.", exports.Value(value, false)))
		}
		if m.Env == nil {
			m.Env = map[string]string{}
		}
		m.Env[key] = clean
	}
	for i, item := range raw.Requires {
		entry, needs, err := readRequirement(item, i)
		if err != nil {
			return nil, exports.AtPath(path, err)
		}
		if _, dup := m.Requires[entry]; dup {
			kind, name, _ := strings.Cut(entry, "/")
			return nil, refuse(fmt.Sprintf("Remove one of the two requires items for %s.", Describe(kind, accesskey.ShownName(name, false))),
				fmt.Sprintf("The requires lists %s twice.", Describe(kind, accesskey.ShownName(name, false))))
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
				return nil, refuse("Set each egress entry to a lower-case host name or a *. suffix.",
					fmt.Sprintf("%s is not one, and an entry has no port, path, or scheme.", exports.Value(host, true)))
			}
			if !slices.Contains(m.Egress, host) {
				m.Egress = append(m.Egress, host)
			}
		}
		sort.Strings(m.Egress)
	}
	if m.PushChecks, err = readPushChecks(path, raw.PushChecks); err != nil {
		return nil, err
	}
	if m.SubmitProgram, err = readSubmitProgram(path, raw.SubmitProgram); err != nil {
		return nil, err
	}
	if m.Contracts, err = readContracts(path, &raw.Roles); err != nil {
		return nil, err
	}
	for _, part := range []struct {
		key    string
		node   *yaml.Node
		counts bool
		to     *stack.Fills
	}{{"roles", &raw.Roles, true, nil}, {"provides", &raw.Provides, false, &m.Provides}} {
		if part.node.Kind == 0 {
			continue
		}
		one, many, err := stack.ReadRoles(part.node, part.key, part.counts)
		var in exports.InFile
		if errors.As(err, &in) {
			return nil, in.In(path)
		}
		if err != nil {
			return nil, caused(err, fmt.Sprintf("Correct %s in the manifest.", part.key))
		}
		if part.to == nil {
			m.Roles = one
			continue
		}
		*part.to = stack.Fills{One: one, Many: many}
	}
	for name, node := range raw.Variants {
		if name == "default" {
			if err := node.Decode(&m.Default); err != nil {
				return nil, caused(err, "Set variants.default to a variant name, or `fail`.")
			}
			continue
		}
		v := Variant{}
		if err := node.Decode(&v); err != nil {
			// The decoder quotes up to ten characters of a value that is not a map.
			return nil, caused(err, fmt.Sprintf("Set variants.%s to a map of a kind to a directory.", accesskey.ShownName(name, false)))
		}
		if m.Variants == nil {
			m.Variants = map[string]Variant{}
		}
		m.Variants[name] = v
	}
	if m.Default != "" && m.Default != "fail" {
		if _, ok := m.Variants[m.Default]; !ok {
			return nil, refuse("Set variants.default to a variant the manifest declares, or `fail`.",
				fmt.Sprintf("variants.default is %s, which is not a variant.", accesskey.ShownName(m.Default, true)))
		}
	}
	return m, nil
}

// readPushChecks checks the push_checks items of the manifest at path: a type in the
// form [runapi.ArtifactName] checks, at most once, and a command whose first item is a path inside
// the module with no .. part, and reads, artifact types, each once. It returns the
// items with each program's path cleaned, nil for none.
func readPushChecks(path string, items []rawPushCheck) ([]PushCheck, error) {
	var out []PushCheck
	seen := map[string]bool{}
	refuse := func(fix string, lines ...string) error {
		return exports.Refuse(fix, append(lines, "The manifest is "+path+".")...)
	}
	const typeRule = "An artifact type is lowercase letters and digits in groups joined by single hyphens, at most 64 bytes."
	for i, item := range items {
		if !runapi.ArtifactName(item.Type) {
			return nil, refuse(fmt.Sprintf("Set push_checks[%d].type to an artifact type.", i), typeRule, "The value is "+exports.Value(item.Type, true)+".")
		}
		if seen[item.Type] {
			return nil, refuse(fmt.Sprintf("Remove one of the two push checks for %s.", item.Type), "A module has one check per type.")
		}
		seen[item.Type] = true
		if len(item.Command) == 0 {
			return nil, refuse(fmt.Sprintf("Set push_checks[%d].command to the program's path inside the module, then its arguments.", i), "The list is empty.")
		}
		words := make([]string, len(item.Command))
		for j, node := range item.Command {
			if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
				return nil, refuse(fmt.Sprintf("Set push_checks[%d].command[%d] to a string, quoted when it reads as a number or a boolean.", i, j), "The item is not a string.")
			}
			words[j] = node.Value
		}
		// No .. part at all, as the schema has it, though a/../b would stay inside.
		program, ok := InsideModule(words[0])
		if !ok || slices.Contains(strings.Split(filepath.ToSlash(words[0]), "/"), "..") {
			return nil, refuse(fmt.Sprintf("Set the first item of push_checks[%d].command to a path inside the module.", i), exports.Value(words[0], true)+" is not one.")
		}
		command := append([]string{program}, words[1:]...)
		var reads []string
		for j, node := range item.Reads {
			if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || !runapi.ArtifactName(node.Value) {
				return nil, refuse(fmt.Sprintf("Set push_checks[%d].reads[%d] to an artifact type.", i, j), typeRule, "The value is "+exports.Value(node.Value, true)+".")
			}
			if slices.Contains(reads, node.Value) {
				return nil, refuse(fmt.Sprintf("Remove one of the two items %s from push_checks[%d].reads.", node.Value, i), "A type is listed once.")
			}
			reads = append(reads, node.Value)
		}
		out = append(out, PushCheck{Type: item.Type, Command: command, Reads: reads})
	}
	return out, nil
}

// readSubmitProgram checks the submit_program item of the manifest at path: a command
// whose first item is a path inside the module with no .. part, and whose other items are
// strings. It returns the item with the program's path cleaned, nil for none.
func readSubmitProgram(path string, item *rawSubmitProgram) (*SubmitProgram, error) {
	if item == nil {
		return nil, nil
	}
	refuse := func(fix string, lines ...string) error {
		return exports.Refuse(fix, append(lines, "The manifest is "+path+".")...)
	}
	if len(item.Command) == 0 {
		return nil, refuse("Set submit_program.command to the program's path inside the module, then its arguments.", "The list is empty.")
	}
	words := make([]string, len(item.Command))
	for i, node := range item.Command {
		if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return nil, refuse(fmt.Sprintf("Set submit_program.command[%d] to a string, quoted when it reads as a number or a boolean.", i), "The item is not a string.")
		}
		words[i] = node.Value
	}
	program, ok := InsideModule(words[0])
	if !ok || slices.Contains(strings.Split(filepath.ToSlash(words[0]), "/"), "..") {
		return nil, refuse("Set the first item of submit_program.command to a path inside the module.", exports.Value(words[0], true)+" is not one.")
	}
	return &SubmitProgram{Command: append([]string{program}, words[1:]...)}, nil
}

// SelectVariant picks the variant for a runtime: the forced one from the stack, else the
// one named like the runtime, else the manifest's default. A module with no manifest and
// a manifest that declares no variant serve every runtime from the module root, and
// return "".
//
// It returns an error when the stack forces a variant the module does not have, or forces
// one on a module that declares none, and when the module has no variant for the runtime
// and its default is missing or "fail". Every message lists the variant names the module
// has. file is the name of the file whose modules entry lists the module, which the
// refusals print. A variant's name and the runtime's are printed as [accesskey.ShownName] prints
// them.
func SelectVariant(m *Manifest, forced, runtime, file string) (string, error) {
	if m == nil || len(m.Variants) == 0 {
		if forced != "" {
			return "", exports.Refuse(fmt.Sprintf("Remove variant %s from %s in %s.", accesskey.ShownName(forced, true), exports.ModulePlace, file),
				fmt.Sprintf("Variant %s is forced, and the module declares no variants.", accesskey.ShownName(forced, true)))
		}
		return "", nil
	}
	if forced != "" {
		if _, ok := m.Variants[forced]; !ok {
			return "", exports.Refuse(fmt.Sprintf("Set the variant of %s in %s to one the module has.", exports.ModulePlace, file),
				fmt.Sprintf("Variant %s is forced, and the module has no such variant.", accesskey.ShownName(forced, true)),
				"The variants are "+names(m.Variants)+".")
		}
		return forced, nil
	}
	if _, ok := m.Variants[runtime]; ok {
		return runtime, nil
	}
	if m.Default == "fail" {
		return "", exports.Refuse(fmt.Sprintf("Select a variant of %s in %s.", exports.ModulePlace, file),
			fmt.Sprintf("The module has no variant for runtime %s, and its default is `fail`.", accesskey.ShownName(runtime, false)),
			"The variants are "+names(m.Variants)+".")
	}
	if m.Default == "" {
		return "", exports.Refuse(fmt.Sprintf("Select a variant of %s in %s.", exports.ModulePlace, file),
			fmt.Sprintf("The module has no variant for runtime %s, and declares no default.", accesskey.ShownName(runtime, false)),
			"The variants are "+names(m.Variants)+".")
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
	switch len(ns) {
	case 0, 1:
		return strings.Join(ns, "")
	case 2:
		return ns[0] + " and " + ns[1]
	}
	return strings.Join(ns[:len(ns)-1], ", ") + ", and " + ns[len(ns)-1]
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
// A kind directory that is not there leaves the module without entries of that kind.
// Skills may be grouped in folders, see [readSkills]; a folder that is neither a skill
// nor a group is an error. In the Markdown kinds only .md files count. Under hooks every
// file is a hook and a directory is an error, because a hook is identified by its file
// name and a script's helpers belong elsewhere in the module, reached as
// $QORY_HARNESS_HOME/modules/<name>/<path>. Under mcp every .json file is one server and
// must contain a JSON object. Under files every file below a runtime directory
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
	// fail is err, an error of reading the module's files, as a refusal that starts with its
	// fix: a refusal with the module at its place, a resolveError as it is, and any other
	// error under the line "Check that you can read the files of module <name>."
	fail := func(err error) error {
		if _, ok := err.(*exports.Refusal); ok {
			return exports.Prefixed("module "+shown, err)
		}
		// A resolveError starts with a fix, which the line below would push down.
		var resolve *resolveError
		if errors.As(err, &resolve) {
			return fmt.Errorf("%w\n  The module is %s.", err, shown)
		}
		return fmt.Errorf("%s\n  %w", exports.Fit("Check that you can read the files of module "+shown+"."), err)
	}
	dirs := map[string]string{"skills": "skills", "agents": "agents", "commands": "commands", "output-styles": "output-styles", "hooks": "hooks", "mcp": "mcp", "files": "files"}
	if m != nil && variant != "" {
		for kind, sub := range m.Variants[variant] {
			if _, ok := dirs[kind]; !ok {
				return nil, exports.Refuse(fmt.Sprintf("Remove %s from variant %s of module %s.", accesskey.ShownName(kind, true), accesskey.ShownName(variant, false), shown),
					"A variant sets the directory of one of skills, agents, commands, output-styles, hooks, mcp, or files.")
			}
			clean := filepath.Clean(sub)
			if sub == "" || clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return nil, exports.Refuse(fmt.Sprintf("Set the %s directory of variant %s to a directory inside the module.", kind, accesskey.ShownName(variant, false)),
					fmt.Sprintf("Variant %s of module %s reads %s from %s, which is not one.", accesskey.ShownName(variant, false), shown, kind, exports.Value(sub, true)))
			}
			dirs[kind] = clean
		}
	}
	skills, err := readSkills(dirs["skills"], filepath.Join(abs, dirs["skills"]), variantSkillDirs(m))
	if err != nil {
		return nil, fail(err)
	}
	l.Entries = append(l.Entries, skills...)
	for _, kind := range []string{"agents", "commands", "output-styles"} {
		es, err := readMarkdown(kind, filepath.Join(abs, dirs[kind]))
		if err != nil {
			return nil, fail(err)
		}
		l.Entries = append(l.Entries, es...)
	}
	hooks, err := readHooks(shown, dirs["hooks"], filepath.Join(abs, dirs["hooks"]))
	if err != nil {
		return nil, fail(err)
	}
	l.Entries = append(l.Entries, hooks...)
	servers, mcp, err := readMCP(dirs["mcp"], filepath.Join(abs, dirs["mcp"]), readFile)
	if err != nil {
		return nil, fail(err)
	}
	l.Entries = append(l.Entries, servers...)
	l.MCP = mcp
	files, err := readFiles(dirs["files"], filepath.Join(abs, dirs["files"]))
	if err != nil {
		return nil, fail(err)
	}
	l.Entries = append(l.Entries, files...)
	for i, e := range l.Entries {
		// at is the entry as an error prints it. A skill's name does not show its folder,
		// which may be in a group or a variant's directory.
		at := e.Kind + "/" + e.Name
		if e.Kind == "skills" {
			if rel, err := filepath.Rel(abs, e.Path); err == nil {
				at = filepath.ToSlash(rel)
			}
		}
		if err := inside(abs, e.Path); err != nil {
			if out := linkOut(exports.HidePath(at), err); out != nil {
				return nil, fail(out)
			}
			return nil, fail(fmt.Errorf("%s %w", exports.HidePath(at), err))
		}
		if e.Kind == "skills" {
			if err := insideAll(abs, e.Path); err != nil {
				if out := linkOut(exports.HidePath(at), err); out != nil {
					return nil, fail(out)
				}
				return nil, fail(fmt.Errorf("%s/%w", exports.HidePath(at), err))
			}
		}
		refs, err := references(abs, e)
		if err != nil {
			return nil, fail(err)
		}
		l.Entries[i].References = refs.keys
		l.Entries[i].Tier = refs.tier
		l.Entries[i].Front = refs.front
		l.Entries[i].In = refs.in
		l.Entries[i].Paths = refs.paths
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
			return nil, fail(exports.HideError(err))
		}
		if err := NoTier(string(data)); err != nil {
			rel, _ := filepath.Rel(abs, e.Path)
			return nil, fail(exports.AtPath(exports.HidePath(filepath.ToSlash(rel)), err))
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
				needed := Describe(kind, accesskey.ShownName(entry, false))
				fix := fmt.Sprintf("Ship %s in module %s, or remove it from the requires of the module.", needed, shown)
				if !fits(fix) {
					fix = fmt.Sprintf("Ship %s in module %s.\n  You can also remove it from the requires of the module.", needed, shown)
				}
				return nil, exports.Errorf("%s\n  The requires of module %s lists %s, and the module does not ship it.", fix, shown, needed)
			}
		}
	}
	l.Settings, err = readSettings(filepath.Join(abs, "settings"))
	if err != nil {
		return nil, fail(err)
	}
	for _, runtime := range slices.Sorted(maps.Keys(l.Settings)) {
		files := l.Settings[runtime]
		for _, file := range slices.Sorted(maps.Keys(files)) {
			path := files[file]
			if err := inside(abs, path); err != nil {
				if out := linkOut(exports.HidePath("settings/"+runtime+"/"+file), err); out != nil {
					return nil, fail(out)
				}
				return nil, fail(fmt.Errorf("%s %w", exports.HidePath("settings/"+runtime+"/"+file), err))
			}
			data, err := readFile(path)
			if err != nil {
				return nil, fail(exports.HideError(err))
			}
			if err := NoTier(string(data)); err != nil {
				return nil, fail(exports.AtPath(exports.HidePath("settings/"+runtime+"/"+file), err))
			}
		}
	}
	if _, err := os.Stat(filepath.Join(abs, InstructionsName)); err == nil {
		l.Instructions = filepath.Join(abs, InstructionsName)
		if err := inside(abs, l.Instructions); err != nil {
			if out := linkOut(InstructionsName, err); out != nil {
				return nil, fail(out)
			}
			return nil, fail(fmt.Errorf("%s %w", InstructionsName, err))
		}
		data, err := readFile(l.Instructions)
		if err != nil {
			return nil, fail(exports.HideError(err))
		}
		if err := NoTier(string(data)); err != nil {
			return nil, fail(exports.AtPath(InstructionsName, err))
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
			return nil, exports.Refuse("Move the file inside the module.", exports.HidePath(path)+" lies outside the module, and qory reads only the module's files.")
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

// ErrLeadsOut is the error [ResolveIn] wraps for a path that leads out of the module, and
// ErrTooManyLinks the one it wraps for a path with more than 40 links. A caller matches
// each with errors.Is.
var (
	ErrLeadsOut     = errors.New("the path leads out of the module")
	ErrTooManyLinks = errors.New("the path passes more than 40 links")
)

// resolveError is an error of [ResolveIn]: its text, and the sentinel it wraps.
type resolveError struct {
	text string
	err  error
}

func (e *resolveError) Error() string { return e.text }

// Unwrap returns the sentinel, [ErrLeadsOut] or [ErrTooManyLinks].
func (e *resolveError) Unwrap() error { return e.err }

// ResolveIn is rel, a path relative to the directory root was opened at, with every link
// on it followed through root: a relative link as root follows it, and an absolute one
// only when its target lies under realDir, the real path of root's directory as recorded
// when root was opened, at that part of the target. Nothing is resolved outside root. A
// link that leads out of the module, an absolute link when realDir is "", and a path with
// more than 40 links are refused, wrapping [ErrLeadsOut] and [ErrTooManyLinks]. The
// result contains no link, as root showed it.
func ResolveIn(root *os.Root, realDir, rel string) (string, error) {
	out := func() error {
		return &resolveError{"Point the link inside the module, or remove it.\n  " + exports.HidePath(filepath.ToSlash(rel)) + " leads out of the module.", ErrLeadsOut}
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
			return "", &resolveError{fmt.Sprintf("Shorten the chain of links on the path.\n  %s passes more than %d links.", exports.HidePath(filepath.ToSlash(rel)), maxLinks), ErrTooManyLinks}
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

// linkOut is the refusal of the entry shown as name, with [exports.ModulePlace] for the
// module, when err is an [outsideError], with its fix first, and nil for any other err.
func linkOut(name string, err error) error {
	var out *outsideError
	if !errors.As(err, &out) {
		return nil
	}
	if out.under != "" {
		name += "/" + out.under
	}
	return exports.Refuse("Point the link inside "+exports.ModulePlace+", or remove it.",
		fmt.Sprintf("The link %s leads to %s, outside the module, and qory reads only the module's files.", name, out.real))
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

// RuntimeFolders lists the names of the runtimes qory renders. A folder with one of these
// names under skills is not a group, see [readSkills], so a skill meant for one runtime
// stops the compose for the others. A test in the cmd package checks this list against
// the registered runtimes: this package cannot import render, which imports it.
func RuntimeFolders() []string {
	return []string{"amp", "any", "claude", "codex", "copilot", "cursor", "gemini", "goose", "opencode"}
}

// variantSkillDirs lists the directories that the variants of m read skills from.
func variantSkillDirs(m *Manifest) map[string]bool {
	dirs := map[string]bool{}
	if m == nil {
		return dirs
	}
	for _, v := range m.Variants {
		if sub, ok := v["skills"]; ok {
			dirs[filepath.ToSlash(filepath.Clean(sub))] = true
		}
	}
	return dirs
}

// readSkills lists the skills below dir, which errors print as rel, relative to the module
// root. Below dir, each folder whose name starts with no dot is one of these:
//
//   - A skill: it has SKILL.md. Everything below it is that skill's files, a nested
//     SKILL.md included. Its name is the folder's name.
//   - A group: a real folder with no SKILL.md and with a folder below it. A group is read
//     by these same rules, at any depth, and its name appears in no entry's name. A link is
//     never a group, so no cycle can occur. A folder named after a runtime, or that a
//     variant in variants reads skills from, is never a group, because its skills are for
//     that runtime or variant.
//   - Anything else, which is an error that starts with its fix and shows the folder's full
//     path: for folders with no skill below them, the topmost one.
//
// Loose files are skipped, and a link that leads nowhere is an error. Two skills of one
// name are an error, because each skill goes to skills/<name> and an exclude cannot separate
// two skills of one module. Two names that differ only in case are one folder on a file
// system that ignores case, and are an error on every file system, so a module composes
// alike everywhere.
func readSkills(rel, dir string, variants map[string]bool) ([]Entry, error) {
	items, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, exports.HideError(err)
	}
	r := skillReader{variants: variants, seen: map[string]string{}}
	if err := r.read(filepath.ToSlash(rel), dir, items); err != nil {
		return nil, err
	}
	return r.entries, nil
}

// skillReader is one read of a module's skills folder, see [readSkills].
type skillReader struct {
	variants map[string]bool   // the folders that are for a variant, see [readSkills]
	seen     map[string]string // the folder of each skill read so far, by its lower-case name
	entries  []Entry
}

// read adds the skills of items, the contents of dir, which errors print as rel.
func (r *skillReader) read(rel, dir string, items []os.DirEntry) error {
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, it.Name())
		at := rel + "/" + it.Name()
		info, err := os.Stat(path)
		if err != nil {
			if leadsNowhere(path, err) {
				return exports.Refuse("Point the link at a path that exists, or remove it.", fmt.Sprintf("The link %s in %s leads nowhere.", exports.HidePath(at), exports.ModulePlace))
			}
			return exports.HideError(err)
		}
		if !info.IsDir() {
			continue
		}
		if isSkill(path) {
			if err := r.add(at, it.Name(), path); err != nil {
				return err
			}
			continue
		}
		if err := r.checkGroup(at, path); err != nil {
			return err
		}
		below, err := os.ReadDir(path)
		if err != nil {
			return exports.HideError(err)
		}
		if err := r.read(at, path, below); err != nil {
			return err
		}
	}
	return nil
}

// add adds the skill name in the folder at path, printed as at, unless the module already
// has a skill with that name, in any case.
func (r *skillReader) add(at, name, path string) error {
	key := strings.ToLower(name)
	if first, ok := r.seen[key]; ok {
		same := "both are the skill " + accesskey.ShownName(name, false)
		if filepath.Base(first) != name {
			same = "these two names differ only in case"
		}
		a, b := exports.HidePath(first), exports.HidePath(at)
		fix := fmt.Sprintf("Rename %s or %s in %s.", a, b, exports.ModulePlace)
		lines := []string{fmt.Sprintf("Each skill goes to skills/<name>, and %s.", same)}
		if !fits(fix) {
			fix = "Rename one of two skills in " + exports.ModulePlace + "."
			lines = append([]string{fmt.Sprintf("The skills are %s and %s.", a, b)}, lines...)
		}
		return exports.Refuse(fix, lines...)
	}
	r.seen[key] = at
	r.entries = append(r.entries, Entry{Kind: "skills", Name: name, Path: path})
	return nil
}

// fits reports whether line, the first line of a refusal, has at most 100 characters, with
// [exports.ModulePlace] in it a module as long as a refusal prints one: "module " and a
// name of [exports.NameMax] characters.
func fits(line string) bool {
	line = strings.ReplaceAll(line, exports.ModulePlace, strings.Repeat("m", len("module ")+exports.NameMax))
	return utf8.RuneCountInString(line) <= 100
}

// jsonObjectRefusal is the refusal of the MCP server file, a path in the module as a
// message prints it, whose content is no JSON object. The path moves to the second line
// when it would push the first past 100 characters.
func jsonObjectRefusal(file, cause string) error {
	fix := fmt.Sprintf("Write %s in %s as one JSON object.", file, exports.ModulePlace)
	if !fits(fix) {
		return exports.Refuse("Write the MCP server file in "+exports.ModulePlace+" as one JSON object.", "The file is "+file+".", cause)
	}
	return exports.Refuse(fix, cause)
}

// checkGroup returns the stop for the folder at path, with no SKILL.md, printed as at, when
// it cannot be read as a group of skills, see [readSkills].
func (r *skillReader) checkGroup(at, path string) error {
	if err := r.notAGroup(at, path); err != nil {
		return err
	}
	none, folders := r.noSkillBelow(at, path)
	switch {
	case !none:
		return nil
	case folders:
		return exports.Refuse("Add SKILL.md to the folder, or put a skill in a folder below it.", fmt.Sprintf("%s in %s has no SKILL.md and no skill below it.", exports.HidePath(at), exports.ModulePlace))
	}
	return exports.Refuse("Add SKILL.md to the folder.", fmt.Sprintf("%s in %s has no SKILL.md and no skill below it.", exports.HidePath(at), exports.ModulePlace))
}

// notAGroup is the stop for a folder that is never a group whatever is below it, nil for
// any other: one that a variant reads skills from, one named after a runtime, and a link.
func (r *skillReader) notAGroup(at, path string) error {
	shown := exports.HidePath(at)
	if r.variants[at] {
		return exports.Refuse("Select the variant that reads the folder, or change that variant's skills.", fmt.Sprintf("%s in %s is a folder that a variant reads skills from, and such a folder is not a group.", shown, exports.ModulePlace))
	}
	if slices.Contains(RuntimeFolders(), filepath.Base(path)) {
		return exports.Refuse("Rename the folder, or read it through a variant.", fmt.Sprintf("%s in %s is named after a runtime, and such a folder is not a group.", shown, exports.ModulePlace))
	}
	self, err := os.Lstat(path)
	if err != nil {
		return exports.HideError(err)
	}
	if !self.IsDir() {
		return exports.Refuse("Point the link at a skill, or remove it.", fmt.Sprintf("The link %s in %s leads to a folder with no SKILL.md.", shown, exports.ModulePlace))
	}
	return nil
}

// noSkillBelow reports whether the real folder at path, printed as at, has no skill below
// it, and whether it has a folder below it. A dot name does not count. It returns false
// when anything below it has a stop of a more exact kind, which [skillReader.read] reaches.
func (r *skillReader) noSkillBelow(at, path string) (none, folders bool) {
	items, err := os.ReadDir(path)
	if err != nil {
		return false, false
	}
	for _, it := range items {
		if strings.HasPrefix(it.Name(), ".") {
			continue
		}
		below := filepath.Join(path, it.Name())
		info, err := os.Stat(below)
		if err != nil {
			return false, false
		}
		if !info.IsDir() {
			continue
		}
		folders = true
		belowAt := at + "/" + it.Name()
		if isSkill(below) || r.notAGroup(belowAt, below) != nil {
			return false, true
		}
		if empty, _ := r.noSkillBelow(belowAt, below); !empty {
			return false, true
		}
	}
	return true, folders
}

// isSkill reports whether the folder at path has SKILL.md.
func isSkill(path string) bool {
	_, err := os.Stat(filepath.Join(path, "SKILL.md"))
	return err == nil
}

// leadsNowhere reports whether path, whose stat failed with err, is a link to nothing: its
// target does not exist, or it links to itself in a loop.
func leadsNowhere(path string, err error) bool {
	self, lerr := os.Lstat(path)
	return lerr == nil && self.Mode()&os.ModeSymlink != 0 && !errors.Is(err, os.ErrPermission)
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
			return nil, exports.Refuse("Move the file into a runtime directory under files.",
				fmt.Sprintf("%s in %s is not under a runtime directory, and a file ships as %s/<runtime>/<path>.", exports.HidePath(filepath.ToSlash(rel)+"/"+rt.Name()), exports.ModulePlace, exports.HidePath(filepath.ToSlash(rel))))
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
				return nil, exports.Refuse("Move the directory to files/<runtime>/, or replace it with a fragment file.",
					fmt.Sprintf("settings/%s in %s is a directory, and settings has one fragment per target file.", sub, exports.ModulePlace),
					fmt.Sprintf("A runtime's other files go under files/%s.", sub))
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
			fix := fmt.Sprintf("Move %s out of the hooks directory of module %s.", sub, name)
			if !fits(fix) {
				fix = fmt.Sprintf("Move the directory out of the hooks directory of module %s.", name)
			}
			return nil, exports.Refuse(fix,
				fmt.Sprintf("%s is a directory, and a hook is one file.", sub),
				fmt.Sprintf("A hook reads its helpers at $QORY_HARNESS_HOME/modules/%s/<path>.", name))
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
			return nil, nil, jsonObjectRefusal(file, "The file does not contain a JSON object: "+exports.HideValueError(err, "").Error())
		}
		if server == nil {
			return nil, nil, jsonObjectRefusal(file, "The file contains null, which is no JSON object.")
		}
		if err := checkServer(file, server); err != nil {
			return nil, nil, err
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
// string. The refusal starts with its fix, and its last line has file, the path of the
// server file in the module. A value the refusal prints is printed as [shownJSON] prints
// it.
func checkServer(file string, server map[string]any) error {
	refuse := func(fix, cause string) error {
		return exports.Refuse(fix, cause, "The file is "+file+" in "+exports.ModulePlace+".")
	}
	for k := range server {
		if !serverKeys[k] {
			return refuse("Remove the key "+accesskey.ShownName(k, true)+" from the MCP server file.", "An MCP server has only type, command, args, env, url, headers, and description.")
		}
	}
	command, hasCommand := server["command"]
	url, hasURL := server["url"]
	switch {
	case hasCommand && hasURL:
		return refuse("Remove command or url from the MCP server file.", "A server sets one of them, and the file sets both.")
	case !hasCommand && !hasURL:
		return refuse("Add command or url to the MCP server file.", "A server sets one of them, and the file sets neither.")
	case hasCommand && !nonEmptyString(command):
		return refuse("Set command of the MCP server to a non-empty string.", "command is not a non-empty string.")
	case hasURL && !nonEmptyString(url):
		return refuse("Set url of the MCP server to a non-empty string.", "url is not a non-empty string.")
	}
	if t, ok := server["type"]; ok {
		if s, _ := t.(string); s != "stdio" && s != "http" && s != "sse" {
			return refuse("Set type of the MCP server to stdio, http, or sse.", "type is "+shownJSON(t)+".")
		}
	}
	if args, ok := server["args"]; ok {
		list, isList := args.([]any)
		if !isList {
			return refuse("Set args of the MCP server to a list of strings.", "args is not a list.")
		}
		for _, a := range list {
			if _, ok := a.(string); !ok {
				return refuse("Set each item of args of the MCP server to a string.", "args contains "+shownJSON(a)+", which is not a string.")
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
			return refuse("Set "+key+" of the MCP server to an object of strings.", key+" is not an object.")
		}
		for k, e := range m {
			if _, ok := e.(string); !ok {
				return refuse("Set each value of "+key+" of the MCP server to a string.", key+"."+accesskey.ShownName(k, false)+" is "+shownJSON(e)+", which is not a string.")
			}
		}
	}
	if d, ok := server["description"]; ok {
		if _, isString := d.(string); !isString {
			return refuse("Set description of the MCP server to a string.", "description is "+shownJSON(d)+", which is not a string.")
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
