package stack

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/exports"
	"gopkg.in/yaml.v3"
)

// Roles maps a kind of [Referable] to its roles, and each role to the name of the one
// entry of that kind that fills it: bind: {agents: {coder: rails-coder}} in a stack or a
// checkout's qory.yaml is Fills{One: Roles{"agents": {"coder": "rails-coder"}}}, and so
// is provides in a module's qory-module.yaml. The roles key of a manifest is a Roles too,
// each role set to one or, for an agents role, many.
type Roles map[string]map[string]string

// Groups maps the kind agents to its group roles, and each group role to the names of
// the agents listed for it, in the order written, each once: bind: {agents:
// {reviewers: [code-reviewer, test-reviewer]}} is Groups{"agents": {"reviewers":
// {"code-reviewer", "test-reviewer"}}}. Only an agents role can be a group.
type Groups map[string]map[string][]string

// Len is the number of roles in g.
func (g Groups) Len() int {
	n := 0
	for _, roles := range g {
		n += len(roles)
	}
	return n
}

// Fills is a bind or a provides: the roles set to one name, and the group roles set to
// a list of names. The roles key of a manifest sets whether a role is a group, and the
// compose checks a list against it.
type Fills struct {
	// One are the roles set to one name.
	One Roles
	// Many are the agents roles set to a list of names.
	Many Groups
}

// UnmarshalYAML reads bind as a file writes it, see [ReadRoles].
func (f *Fills) UnmarshalYAML(n *yaml.Node) error {
	one, many, err := ReadRoles(n, "bind", false)
	if err != nil {
		return err
	}
	*f = Fills{One: one, Many: many}
	return nil
}

// Len is the number of roles in f, over every kind, a group role counted once.
func (f Fills) Len() int { return f.One.Len() + f.Many.Len() }

// IsZero reports whether f sets no role, so a file written from it leaves the key out.
func (f Fills) IsZero() bool { return f.Len() == 0 }

// Sets reports whether f sets the role of kind, to one name or to a list.
func (f Fills) Sets(kind, role string) bool {
	if _, ok := f.One[kind][role]; ok {
		return true
	}
	_, ok := f.Many[kind][role]
	return ok
}

// Flat is r keyed <kind>/<role>, the form a reference writes, nil for no role.
func (r Roles) Flat() map[string]string {
	var out map[string]string
	for kind, roles := range r {
		for role, name := range roles {
			if out == nil {
				out = map[string]string{}
			}
			out[kind+"/"+role] = name
		}
	}
	return out
}

// Len is the number of roles in r, over every kind.
func (r Roles) Len() int {
	n := 0
	for _, roles := range r {
		n += len(roles)
	}
	return n
}

// RolesError is an error about the roles under key in a file, whose text contains the
// file's path. [RolesError.In] sets the path, and the callers that put a path in front of
// an error put it inside this one instead, see [exports.InFile].
type RolesError struct {
	text func(file string) string
	file string
}

func (e *RolesError) Error() string {
	file := e.file
	if file == "" {
		file = "the file"
	}
	return e.text(file)
}

// In is the error with file as the path of the file it is about.
func (e *RolesError) In(file string) error { return &RolesError{text: e.text, file: file} }

// rolesError is a [RolesError] of format and args, which has no path, and a last line,
// "<key> is in <file>.", with the file's path. The text is formatted first, with a NUL
// byte at the path, so a % in the name stays as it is, and a long path never pushes the
// fix past 100 characters.
func rolesError(key, format string, args ...any) *RolesError {
	text := fmt.Sprintf(format, args...) + "\n  " + key + " is in \x00."
	return &RolesError{text: func(file string) string {
		return strings.Replace(text, "\x00", file, 1)
	}}
}

// ReadRoles reads n, the value of key in a file, as kinds to roles: bind and provides,
// whose value per role is one name or, for an agents role, a list of names, and with
// counts set, roles, whose value per role is one or, for an agents role, many. A null
// value is no role. The roles set to one name come back in the first result, the lists
// in the second, each name of a list once, in the order written. The errors are
// [RolesError]s, each starting with its fix:
//
//   - a key under key that contains a slash, the form <kind>/<role>, which a file wrote
//     before bind was nested;
//   - a kind other than agents, commands and skills, and a kind that is no map;
//   - a value that is no name and no list of names, an empty list, a list or many for a
//     kind other than agents, and with counts, a list or a value other than one and many;
//   - a role or a name that is not one path segment without a space, an @ or a leading
//     dot.
func ReadRoles(n *yaml.Node, key string, counts bool) (Roles, Groups, error) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil, nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, nil, rolesError(key, "Write %s as a map of kinds, then roles: %s is not a map.", key, key)
	}
	// Decoding into a map returns an error for a key written twice, which a node keeps.
	var kinds map[string]yaml.Node
	if err := n.Decode(&kinds); err != nil {
		return nil, nil, err
	}
	var out Roles
	var lists Groups
	for _, kind := range sortedNodeKeys(kinds) {
		v := kinds[kind]
		if k, role, flat := strings.Cut(kind, "/"); flat {
			value := "<name>"
			if v.Kind == yaml.ScalarNode {
				value = accesskey.ShownName(v.Value, false)
			}
			return nil, nil, exports.Refuse(fmt.Sprintf("Write %s as %s.%s.%s: %s.", key, key, accesskey.ShownName(k, false), accesskey.ShownName(role, false), value),
				fmt.Sprintf("The keys of %s are kinds, and %s is a kind and a role.", key, accesskey.ShownName(kind, false)),
				fmt.Sprintf("%s is in %s.", key, exports.FilePlace))
		}
		if !isReferable(kind) {
			return nil, nil, rolesError(key, "Use agents, commands, or skills as a kind under %s: %s is none of them.", key, accesskey.ShownName(kind, true))
		}
		at := key + "." + kind
		if v.Kind != yaml.MappingNode {
			return nil, nil, rolesError(key, "Write %s as a map of roles: %s is not a map.", at, at)
		}
		var roles map[string]yaml.Node
		if err := v.Decode(&roles); err != nil {
			return nil, nil, err
		}
		for _, role := range sortedNodeKeys(roles) {
			x := roles[role]
			if !segment(role) || strings.Contains(role, " ") {
				return nil, nil, rolesError(key, "Rename role %s of %s to one path segment with no space, @ or leading dot.", accesskey.ShownName(role, false), at)
			}
			path := at + "." + accesskey.ShownName(role, false)
			if x.Kind == yaml.SequenceNode {
				switch {
				case counts:
					return nil, nil, rolesError(key, "Set %s to one or many.\n  One entry fills a role and several fill a group, and the value is a list.", path)
				case kind != "agents":
					return nil, nil, rolesError(key, "Write one name for %s.\n  Only an agents role can be a group, and the value is a list.", path)
				}
				names, err := readNames(x, key, path)
				if err != nil {
					return nil, nil, err
				}
				if lists == nil {
					lists = Groups{}
				}
				if lists[kind] == nil {
					lists[kind] = map[string][]string{}
				}
				lists[kind][role] = names
				continue
			}
			if x.Kind != yaml.ScalarNode || x.Tag == "!!null" || x.Value == "" {
				return nil, nil, rolesError(key, "Set %s to the name of one entry: the value is empty or not a name.", path)
			}
			name := x.Value
			if counts {
				switch {
				case name == "many" && kind != "agents":
					return nil, nil, exports.Refuse(fmt.Sprintf("Set %s of %s to one.", path, exports.ModulePlace),
						"Only an agents role can be a group. The map is in "+exports.FilePlace+".")
				case name != "one" && name != "many":
					return nil, nil, rolesError(key, "Set %s to one or many.\n  One entry fills a role and several fill a group, and the value is %s.", path, accesskey.ShownName(name, true))
				}
			} else if !entryName(name) {
				return nil, nil, rolesError(key, "Set %s to an entry's name, one path segment with no space, @ or leading dot: the value is %s.", path, accesskey.ShownName(name, true))
			}
			if out == nil {
				out = Roles{}
			}
			if out[kind] == nil {
				out[kind] = map[string]string{}
			}
			out[kind][role] = name
		}
	}
	return out, lists, nil
}

// readNames reads x, the list a file writes at path for a group role, as names, each
// once in the order written. An empty list and an item that is no entry's name are
// errors.
func readNames(x yaml.Node, key, path string) ([]string, error) {
	if len(x.Content) == 0 {
		return nil, rolesError(key, "Write one name or more in %s.\n  The list is empty.", path)
	}
	var names []string
	for _, item := range x.Content {
		if item.Kind != yaml.ScalarNode || item.Tag == "!!null" || !entryName(item.Value) {
			value := "not a name"
			if item.Kind == yaml.ScalarNode && item.Value != "" {
				value = accesskey.ShownName(item.Value, true)
			}
			return nil, rolesError(key, "Write entry names in %s.\n  An entry's name is one path segment with no space, @ or leading dot, and an item is %s.", path, value)
		}
		if !slices.Contains(names, item.Value) {
			names = append(names, item.Value)
		}
	}
	return names, nil
}

// CheckRoles is the check [ReadRoles] makes of each kind, role and name, for roles that
// come from no YAML, such as a [Stack] a caller builds.
func CheckRoles(r Roles, key string) error {
	for _, kind := range sortedRoleKinds(r) {
		if !isReferable(kind) {
			return rolesError(key, "Use agents, commands, or skills as a kind under %s: %s is none of them.", key, accesskey.ShownName(kind, true))
		}
		at := key + "." + kind
		for _, role := range sortedKeys(r[kind]) {
			if !segment(role) || strings.Contains(role, " ") {
				return rolesError(key, "Rename role %s of %s to one path segment with no space, @ or leading dot.", accesskey.ShownName(role, false), at)
			}
			if name := r[kind][role]; !entryName(name) {
				return rolesError(key, "Set %s.%s to an entry's name, one path segment with no space, @ or leading dot: the value is %s.", at, accesskey.ShownName(role, false), accesskey.ShownName(name, true))
			}
		}
	}
	return nil
}

// CheckFills is [CheckRoles] for both parts of f: the roles set to one name, and each
// list, which only an agents role takes and which has one name or more.
func CheckFills(f Fills, key string) error {
	if err := CheckRoles(f.One, key); err != nil {
		return err
	}
	kinds := make([]string, 0, len(f.Many))
	for kind := range f.Many {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		roles := make([]string, 0, len(f.Many[kind]))
		for role := range f.Many[kind] {
			roles = append(roles, role)
		}
		sort.Strings(roles)
		for _, role := range roles {
			path := key + "." + accesskey.ShownName(kind, false) + "." + accesskey.ShownName(role, false)
			if kind != "agents" {
				return rolesError(key, "Write one name for %s.\n  Only an agents role can be a group, and the value is a list.", path)
			}
			if !segment(role) || strings.Contains(role, " ") {
				return rolesError(key, "Rename role %s of %s.%s to one path segment with no space, @ or leading dot.", accesskey.ShownName(role, false), key, kind)
			}
			names := f.Many[kind][role]
			if len(names) == 0 {
				return rolesError(key, "Write one name or more in %s.\n  The list is empty.", path)
			}
			for _, name := range names {
				if !entryName(name) {
					return rolesError(key, "Write entry names in %s.\n  An entry's name is one path segment with no space, @ or leading dot, and an item is %s.", path, accesskey.ShownName(name, true))
				}
			}
		}
	}
	return nil
}

// entryName reports whether name is the name of an entry a role may be filled by: one
// path segment with no space.
func entryName(name string) bool {
	return segment(name) && !strings.Contains(name, " ")
}

// sortedNodeKeys lists a map's keys in order.
func sortedNodeKeys(m map[string]yaml.Node) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedRoleKinds lists the kinds of r in order.
func sortedRoleKinds(r Roles) []string {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
