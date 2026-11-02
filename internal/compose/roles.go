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

// Role is one role of a composed harness and the entry that fills it.
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

// setRoles sets each role of p from three layers, a later layer replacing the name: the
// base's bind, then the provides of the opened modules, then the bind of p. A provided
// entry for which dropped reports true provides nothing.
//
// Two modules that provide one role with different names fail the compose, unless the
// bind of p sets the role. With first set, for the roles before the selection, such a
// role keeps the name of the layer below and no error is returned, since the selection
// may still leave one of the entries out.
func setRoles(p *stack.Stack, opened []openedModule, dropped func(o openedModule, kind, name string) bool, first bool) ([]Role, error) {
	set := map[string]Role{}
	for kind, roles := range p.BaseBind {
		for role, name := range roles {
			set[kind+"/"+role] = Role{Role: kind + "/" + role, Name: name, From: "base"}
		}
	}
	provided := map[string][]Role{}
	for _, o := range opened {
		for kind, roles := range o.m.Provides {
			for role, name := range roles {
				if dropped(o, kind, name) {
					continue
				}
				key := kind + "/" + role
				provided[key] = append(provided[key], Role{Role: key, Name: name, From: "provides " + o.m.Name, Module: o.m.Name})
			}
		}
	}
	for _, key := range sortedKeys(provided) {
		if kind, role, _ := strings.Cut(key, "/"); p.Bind[kind][role] != "" {
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
			return nil, twoProviders(p, key, list)
		}
		set[key] = list[0]
	}
	for kind, roles := range p.Bind {
		for role, name := range roles {
			set[kind+"/"+role] = Role{Role: kind + "/" + role, Name: name, From: "bind"}
		}
	}
	out := make([]Role, 0, len(set))
	for _, key := range sortedKeys(set) {
		out = append(out, set[key])
	}
	return out, nil
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
	file := "qory.yaml"
	if p.File != "" {
		file = exports.HidePath(filepath.Base(p.File))
	}
	both := "both modules"
	if len(list) > 2 {
		both = fmt.Sprintf("all %d modules", len(list))
	}
	return fmt.Errorf("Bind %s.%s in %s, or compose one of %s: %s provide %s (%s).", shown(kind), shown(role), file, andList(modules), both, shown(key), strings.Join(names, ", "))
}

// andList joins names as a sentence lists them: "a and b", "a, b and c".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// excludedBy reports whether the exclude of o, or the exclude a checkout's qory.yaml
// lists for o, lists the entry of kind and name. Both are known before the compose reads
// the entries, so the roles before the selection drop what they leave out.
func excludedBy(o openedModule, kind, name string) bool {
	return slices.Contains(o.pl.Exclude.Kinds[kind], name) || slices.Contains(o.pl.CheckoutExclude.Kinds[kind], name)
}

// flatRoles is roles keyed <kind>/<role>, nil for none.
func flatRoles(roles []Role) map[string]string {
	if len(roles) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, r := range roles {
		out[r.Role] = r.Name
	}
	return out
}

// checkProvides returns an error for a module of a checkout that extends base and
// provides a kind of role that the base's extending.kinds does not list.
func checkProvides(m *module.Manifest, base *Base) error {
	kinds := make([]string, 0, len(m.Provides))
	for kind := range m.Provides {
		kinds = append(kinds, kind)
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
