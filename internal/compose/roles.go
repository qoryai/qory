package compose

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// Role is one role of a composed harness and the entry that fills it: for a group role,
// one member of the group.
type Role struct {
	// Role is the role as a reference writes it, <kind>/<role>.
	Role string
	// Name is the name of the entry of the role's kind that fills the role.
	Name string
	// From is the layer that set the name: "base" for the bind of the stack a checkout's
	// qory.yaml extends, "provides <module>" for a module's provides, and "bind" for the
	// bind of the document composed.
	From string
	// Module is the module whose provides set the name, "" for a bind.
	Module string
	// Group marks a member of a group role, a role that several agents fill.
	Group bool
	// Replaced marks a member of a group role that the bind of the document composed
	// replaced: the group had it from the base or a module's provides, and the bind's
	// list does not have it. A replaced member fills nothing.
	Replaced bool
}

// openedModule is one module of the stack with its source resolved and its manifest
// read, before the compose reads its entries.
type openedModule struct {
	pl       stack.Module
	ps       stack.Source
	src      source.Resolved
	root     *os.Root
	rootInfo fs.FileInfo
	realDir  string
	copied   bool
	m        *module.Manifest
}

// setRoles sets each role of p from three layers: the base's bind, then the provides of
// the opened modules, then the bind of p. A provided entry for which dropped reports true
// provides nothing.
//
// A role that one module's roles declares many is a group role, which several agents
// fill; every other role is a single role. A single role's name is set by each layer in
// turn, a later layer replacing the name. Two modules that provide one single role with
// different names fail the compose, unless the bind of p sets the role. A group's members
// are the base bind's list, then each module's provides list in module order, each name
// once; a bind of p that sets the group replaces the list, and the members it replaced
// come back marked [Role.Replaced]. A base member for which dropped reports true is no
// member either.
//
// With first set, for the roles before the selection, no error is returned: a single
// role that two modules provide keeps the name of the layer below, since the selection
// may still leave one of the entries out, and a role written in the wrong shape is
// skipped, so the compose after the selection refuses it.
//
// The result is sorted by role, the members of a group in their order, the replaced
// ones after them; groups is the members' names of each group role a module declares, in
// their order, nil for a group with no member.
func setRoles(p *stack.Stack, opened []openedModule, dropped func(o openedModule, kind, name string) bool, first bool) (roles []Role, groups map[string][]string, err error) {
	declared, err := groupRoles(opened)
	if err != nil && !first {
		return nil, nil, err
	}
	file := docFile(p)
	// wrong is the error of a role written in the wrong shape, nil with first set.
	wrong := func(format string, args ...any) error {
		if first {
			return nil
		}
		return fmt.Errorf(format, args...)
	}
	set := map[string]Role{}
	for _, kind := range sortedKeys(p.BaseBind.One) {
		for _, role := range sortedKeys(p.BaseBind.One[kind]) {
			key := kind + "/" + role
			if declared[key] != "" {
				if err := wrong("Write bind.%s.%s in the base stack as a list: it is a group role.", shown(kind), shown(role)); err != nil {
					return nil, nil, err
				}
				continue
			}
			set[key] = Role{Role: key, Name: p.BaseBind.One[kind][role], From: "base"}
		}
	}
	for _, kind := range sortedKeys(p.BaseBind.Many) {
		for _, role := range sortedKeys(p.BaseBind.Many[kind]) {
			if declared[kind+"/"+role] == "" {
				if err := wrong("Write one name for bind.%s.%s in the base stack: no module declares %s as many, so one agent fills it.", shown(kind), shown(role), shown(kind+"/"+role)); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	provided := map[string][]Role{}
	for _, o := range opened {
		for _, kind := range sortedKeys(o.m.Provides.One) {
			for _, role := range sortedKeys(o.m.Provides.One[kind]) {
				key, name := kind+"/"+role, o.m.Provides.One[kind][role]
				if declared[key] != "" {
					if err := wrong("Write provides.%s.%s in module %s as a list: it is a group role.", shown(kind), shown(role), shown(o.m.Name)); err != nil {
						return nil, nil, err
					}
					continue
				}
				if dropped(o, kind, name) {
					continue
				}
				provided[key] = append(provided[key], Role{Role: key, Name: name, From: "provides " + o.m.Name, Module: o.m.Name})
			}
		}
		for _, kind := range sortedKeys(o.m.Provides.Many) {
			for _, role := range sortedKeys(o.m.Provides.Many[kind]) {
				if declared[kind+"/"+role] == "" {
					if err := wrong("Write one name for provides.%s.%s in module %s: no module declares %s as many, so one agent fills it.", shown(kind), shown(role), shown(o.m.Name), shown(kind+"/"+role)); err != nil {
						return nil, nil, err
					}
				}
			}
		}
	}
	for _, key := range sortedKeys(provided) {
		if kind, role, _ := strings.Cut(key, "/"); p.Bind.Sets(kind, role) {
			continue
		}
		list := provided[key]
		names := []string{}
		for _, r := range list {
			if !slices.Contains(names, r.Name) {
				names = append(names, r.Name)
			}
		}
		if len(names) > 1 {
			if first {
				continue
			}
			return nil, nil, twoProviders(p, key, list)
		}
		set[key] = list[0]
	}
	for _, kind := range sortedKeys(p.Bind.One) {
		for _, role := range sortedKeys(p.Bind.One[kind]) {
			key := kind + "/" + role
			if declared[key] != "" {
				if err := wrong("Write bind.%s.%s in %s as a list: it is a group role.", shown(kind), shown(role), file); err != nil {
					return nil, nil, err
				}
				continue
			}
			set[key] = Role{Role: key, Name: p.Bind.One[kind][role], From: "bind"}
		}
	}
	for _, kind := range sortedKeys(p.Bind.Many) {
		for _, role := range sortedKeys(p.Bind.Many[kind]) {
			if declared[kind+"/"+role] == "" {
				if err := wrong("Write one name for bind.%s.%s in %s: no module declares %s as many, so one agent fills it.", shown(kind), shown(role), file, shown(kind+"/"+role)); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	members := map[string][]Role{}
	for key := range declared {
		kind, role, _ := strings.Cut(key, "/")
		var list []Role
		add := func(r Role) {
			for _, have := range list {
				if have.Name == r.Name {
					return
				}
			}
			list = append(list, r)
		}
		for _, name := range p.BaseBind.Many[kind][role] {
			if !dropped(openedModule{}, kind, name) {
				add(Role{Role: key, Name: name, From: "base", Group: true})
			}
		}
		for _, o := range opened {
			for _, name := range o.m.Provides.Many[kind][role] {
				if !dropped(o, kind, name) {
					add(Role{Role: key, Name: name, From: "provides " + o.m.Name, Module: o.m.Name, Group: true})
				}
			}
		}
		if names, ok := p.Bind.Many[kind][role]; ok {
			before := list
			list = nil
			for _, name := range names {
				add(Role{Role: key, Name: name, From: "bind", Group: true})
			}
			for _, r := range before {
				if !slices.Contains(names, r.Name) {
					r.Replaced = true
					list = append(list, r)
				}
			}
		}
		members[key] = list
	}
	groups = map[string][]string{}
	for key, list := range members {
		groups[key] = nil
		for _, r := range list {
			if !r.Replaced {
				groups[key] = append(groups[key], r.Name)
			}
		}
	}
	if len(groups) == 0 {
		groups = nil
	}
	for _, key := range sortedKeys(set) {
		members[key] = []Role{set[key]}
	}
	out := []Role{}
	for _, key := range sortedKeys(members) {
		out = append(out, members[key]...)
	}
	return out, groups, nil
}

// groupRoles is each group role the roles of the opened modules declare, keyed
// <kind>/<role>, to the first module in module order that declares it many. A module
// that declares one for a role that another declares many fails the compose.
func groupRoles(opened []openedModule) (map[string]string, error) {
	out := map[string]string{}
	for _, o := range opened {
		for kind, roles := range o.m.Roles {
			for role, count := range roles {
				if key := kind + "/" + role; count == "many" && out[key] == "" {
					out[key] = o.m.Name
				}
			}
		}
	}
	for _, o := range opened {
		for _, kind := range sortedKeys(o.m.Roles) {
			for _, role := range sortedKeys(o.m.Roles[kind]) {
				key := kind + "/" + role
				if by := out[key]; by != "" && o.m.Roles[kind][role] == "one" {
					return out, fmt.Errorf("Declare %s as many in module %s, or remove it: module %s declares it many.", shown(key), shown(o.m.Name), shown(by))
				}
			}
		}
	}
	return out, nil
}

// docFile is the name of the file of p, the document composed, as a message prints it.
func docFile(p *stack.Stack) string {
	if p.File != "" {
		return exports.HidePath(filepath.Base(p.File))
	}
	return "qory.yaml"
}

// twoProviders is the refusal of a role that several modules provide, list, with
// different names.
func twoProviders(p *stack.Stack, key string, list []Role) error {
	kind, role, _ := strings.Cut(key, "/")
	modules := make([]string, 0, len(list))
	names := make([]string, 0, len(list))
	for _, r := range list {
		modules = append(modules, shown(r.Module))
		names = append(names, shown(r.Name))
	}
	both := "both modules"
	if len(list) > 2 {
		both = fmt.Sprintf("all %d modules", len(list))
	}
	return fmt.Errorf("Bind %s.%s in %s, or compose one of %s: %s provide %s (%s).", shown(kind), shown(role), docFile(p), andList(modules), both, shown(key), strings.Join(names, ", "))
}

// andList joins names as a sentence lists them: "a and b", "a, b and c".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// droppedProviders is, per single role keyed <kind>/<role>, the provided entries of the
// opened modules for which dropped reports true, in the order of the modules.
func droppedProviders(opened []openedModule, dropped func(o openedModule, kind, name string) bool) map[string][]Role {
	out := map[string][]Role{}
	for _, o := range opened {
		for _, kind := range sortedKeys(o.m.Provides.One) {
			roles := o.m.Provides.One[kind]
			for _, role := range sortedKeys(roles) {
				if name := roles[role]; dropped(o, kind, name) {
					key := kind + "/" + role
					out[key] = append(out[key], Role{Role: key, Name: name, From: "provides " + o.m.Name, Module: o.m.Name})
				}
			}
		}
	}
	return out
}

// excludedBy reports whether the exclude of o, or the exclude a checkout's qory.yaml
// lists for o, lists the entry of kind and name. Both are known before the compose reads
// the entries, so the roles before the selection drop what they leave out.
func excludedBy(o openedModule, kind, name string) bool {
	return slices.Contains(o.pl.Exclude.Kinds[kind], name) || slices.Contains(o.pl.CheckoutExclude.Kinds[kind], name)
}

// flatRoles is the single roles of roles keyed <kind>/<role>, nil for none. A group's
// members are in [Result.Groups].
func flatRoles(roles []Role) map[string]string {
	var out map[string]string
	for _, r := range roles {
		if r.Group {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[r.Role] = r.Name
	}
	return out
}

// checkProvides returns an error for a module of a checkout that extends base and
// provides a kind of role that the base's extending.kinds does not list.
func checkProvides(m *module.Manifest, base *Base) error {
	kinds := make([]string, 0, len(m.Provides.One)+len(m.Provides.Many))
	for kind := range m.Provides.One {
		kinds = append(kinds, kind)
	}
	for kind := range m.Provides.Many {
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		if !slices.Contains(base.Extending.Kinds, kind) {
			return fmt.Errorf("Remove provides.%s from module %s, or use a base that allows %s: the base's extending.kinds is [%s].", shown(kind), shown(m.Name), shown(kind), strings.Join(shownAll(base.Extending.Kinds), ", "))
		}
	}
	return nil
}

// shownAll is names, each as [shown] prints it.
func shownAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = shown(n)
	}
	return out
}
