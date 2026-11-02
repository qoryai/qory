package stack

import (
	"fmt"
	"sort"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"
)

// Roles maps a kind of [Referable] to its roles, and each role to the name of the one
// entry of that kind that fills it. bind: {agents: {coder: rails-coder}} in a stack or a
// checkout's qory.yaml is Roles{"agents": {"coder": "rails-coder"}}, and so is provides in
// a module's qory-module.yaml.
type Roles map[string]map[string]string

// UnmarshalYAML reads bind as a file writes it, see [ReadRoles].
func (r *Roles) UnmarshalYAML(n *yaml.Node) error {
	out, err := ReadRoles(n, "bind", false)
	if err != nil {
		return err
	}
	*r = out
	return nil
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

// RolesError is a refusal of the roles under key in a file, whose text contains the
// file's name. [RolesError.In] sets the name, and the callers that put a path in front of
// an error return this one without it, see [exports.InFile].
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

// In is the error with file as the name of the file it is about.
func (e *RolesError) In(file string) error { return &RolesError{text: e.text, file: file} }

// rolesError is a [RolesError] of format and args, with the file's name at {file} in
// format. The text is formatted first, with a NUL byte at {file}, so a % in the name
// stays as it is.
func rolesError(format string, args ...any) *RolesError {
	text := fmt.Sprintf(strings.Replace(format, "{file}", "\x00", 1), args...)
	return &RolesError{text: func(file string) string {
		return strings.Replace(text, "\x00", file, 1)
	}}
}

// ReadRoles reads n, the value of key in a file, as kinds to roles to one name each:
// bind and provides, and with counts set, roles, whose value per role is one. A null
// value is no role. The errors are [RolesError]s, each starting with its fix:
//
//   - a key under key that contains a slash, the form <kind>/<role>, which a file wrote
//     before bind was nested;
//   - a kind other than agents, commands and skills, and a kind that is no map;
//   - a list as a role's value, a value that is no name, and with counts, a value other
//     than one;
//   - a role or a name that is not one path segment without a space, an @ or a leading
//     dot.
func ReadRoles(n *yaml.Node, key string, counts bool) (Roles, error) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, rolesError("Write %s in {file} as a map of kinds, then roles: %s is not a map.", key, key)
	}
	// Decoding into a map refuses a key written twice, which a node keeps.
	var kinds map[string]yaml.Node
	if err := n.Decode(&kinds); err != nil {
		return nil, err
	}
	var out Roles
	for _, kind := range sortedNodeKeys(kinds) {
		v := kinds[kind]
		if k, role, flat := strings.Cut(kind, "/"); flat {
			value := "<name>"
			if v.Kind == yaml.ScalarNode {
				value = accesskey.ShownName(v.Value, false)
			}
			return nil, rolesError("Write %s as %s.%s.%s: %s in {file}: %s keys are kinds, and %s is a kind and a role.", key, key, accesskey.ShownName(k, false), accesskey.ShownName(role, false), value, key, accesskey.ShownName(kind, false))
		}
		if !isReferable(kind) {
			return nil, rolesError("Use agents, commands or skills as a kind under %s in {file}: %s is none of them.", key, accesskey.ShownName(kind, true))
		}
		at := key + "." + kind
		if v.Kind != yaml.MappingNode {
			return nil, rolesError("Write %s in {file} as a map of roles: %s is not a map.", at, at)
		}
		var roles map[string]yaml.Node
		if err := v.Decode(&roles); err != nil {
			return nil, err
		}
		for _, role := range sortedNodeKeys(roles) {
			x := roles[role]
			if !segment(role) || strings.Contains(role, " ") {
				return nil, rolesError("Rename role %s of %s in {file} to one path segment with no space, @ or leading dot.", accesskey.ShownName(role, false), at)
			}
			path := at + "." + role
			var name string
			switch {
			case x.Kind == yaml.SequenceNode && counts:
				return nil, rolesError("Set %s to one in {file}: a role is filled by one entry, and the value is a list.", path)
			case x.Kind == yaml.SequenceNode:
				return nil, rolesError("Write one name for %s in {file}: a role is filled by one entry, and the value is a list.", path)
			case x.Kind != yaml.ScalarNode || x.Tag == "!!null" || x.Value == "":
				return nil, rolesError("Set %s in {file} to the name of one entry: the value is empty or not a name.", path)
			}
			name = x.Value
			if counts {
				if name != "one" {
					return nil, rolesError("Set %s to one in {file}: a role is filled by one entry, and the value is %s.", path, accesskey.ShownName(name, true))
				}
			} else if !entryName(name) {
				return nil, rolesError("Set %s in {file} to an entry's name, one path segment with no space, @ or leading dot: the value is %s.", path, accesskey.ShownName(name, true))
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
	return out, nil
}

// CheckRoles is the check [ReadRoles] makes of each kind, role and name, for roles that
// come from no YAML, such as a [Stack] a caller builds.
func CheckRoles(r Roles, key string) error {
	for _, kind := range sortedRoleKinds(r) {
		if !isReferable(kind) {
			return rolesError("Use agents, commands or skills as a kind under %s in {file}: %s is none of them.", key, accesskey.ShownName(kind, true))
		}
		at := key + "." + kind
		for _, role := range sortedKeys(r[kind]) {
			if !segment(role) || strings.Contains(role, " ") {
				return rolesError("Rename role %s of %s in {file} to one path segment with no space, @ or leading dot.", accesskey.ShownName(role, false), at)
			}
			if name := r[kind][role]; !entryName(name) {
				return rolesError("Set %s.%s in {file} to an entry's name, one path segment with no space, @ or leading dot: the value is %s.", at, role, accesskey.ShownName(name, true))
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
