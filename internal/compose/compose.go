package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// Module is a stack module resolved to a directory.
type Module struct {
	// Name is the module's name as its manifest declares it, the name excludes, the tree and
	// messages use.
	Name string
	// Description is the manifest's description, "" for none.
	Description string
	// Dir is the absolute directory the module was read from.
	Dir string
	// Source is the stack's source as text: the path of a path source, <git>#<ref> for a
	// git source.
	Source string
	// Pin is what the source resolved to: "working-tree" for a path, the commit for a git
	// source.
	Pin string
	// Dirty is set when git sees uncommitted changes under Dir.
	Dirty bool
	// Variant is the variant chosen for the target runtime, "" for a module without one.
	Variant string
	// Link is the checkout-root name the stack links the module's directory as, "" for
	// none.
	Link string
	// Base marks a module of the base stack, when a checkout's qory.yaml extends one.
	Base bool
}

// Entry is one entry of the composed tree and the module it came from.
type Entry struct {
	// Kind is one of skills, agents, commands, output-styles, hooks, mcp and files.
	Kind string
	// Name is the entry's name within its kind, one per kind across the composed tree.
	Name string
	// Module is the name of the module that provides the entry.
	Module string
	// Path is absolute: the skill directory, the markdown file, the hook script or the MCP
	// server's JSON file.
	Path string
	// For is the entry, as <kind>/<name>, that required this one, when an only block
	// brought it in for that entry rather than naming it; "" for an entry composed in its
	// own right.
	For string
}

// Exclude is one entry a module left out.
type Exclude struct {
	// Module is the module the entry was dropped from.
	Module string
	// Kind is the entry kind the exclude named.
	Kind string
	// Name is the dropped entry's name.
	Name string
}

// Result is a composed stack: what a renderer writes and what the report records.
type Result struct {
	// Stack is the stack that was composed.
	Stack *stack.Stack
	// Modules are the stack's modules, resolved, in stack order.
	Modules []Module
	// Entries are the composed entries, sorted by kind then name.
	Entries []Entry
	// Excludes are the entries the modules left out, in module order, by kind.
	Excludes []Exclude
	// Settings are the merged fragments: per runtime, per target file, in module order.
	Settings map[string]map[string]map[string]any
	// MCP holds the composed MCP servers by name, each the object its module's
	// mcp/<name>.json holds, with every $QORY_HARNESS_HOME still in place. [Result.MCPFor]
	// renders it for a home.
	MCP map[string]map[string]any
	// Instructions are the modules' AGENTS.md files joined by a blank line, or "".
	Instructions string
	// Env are the variables the harness exports, name to value, with every
	// $QORY_HARNESS_HOME still in place: what the module manifests export, as
	// $QORY_HARNESS_HOME/modules/<name>/<path>, and the configuration's variables over
	// them. [Result.EnvFor] renders it for a home.
	Env map[string]string
	// Base is the stack the checkout's qory.yaml extends, nil for a stack that extends none.
	Base *Base
	// setBy is the module that set each settings leaf, keyed
	// settings/<runtime>/<file>/<dotted.key.path>, so a later fragment setting the same
	// path to another value names both modules.
	setBy map[string]string
}

// Options are the choices a caller makes for one compose.
type Options struct {
	// Update resolves every git source's ref again instead of reading the pin or the
	// cached resolution, so a branch ref moves.
	Update bool
	// Pins are the commits the checkout was composed from last time, by source as the
	// last report recorded it, so a git source stays on its commit until Update.
	Pins map[string]string
	// Cache is the directory git sources are fetched to, "" for [source.CacheDir].
	Cache string
	// Timeout is the longest one git command may run, 0 for no limit.
	Timeout time.Duration
	// Env are the configuration's variables, written over what the modules export.
	Env map[string]string
	// Base is the base stack [LoadBase] resolved, recorded in the result and enforced on
	// the modules that are not its own; nil for a stack that extends none.
	Base *Base
}

// Compose is [ComposeWith] and the default options.
func Compose(p *stack.Stack) (*Result, error) { return ComposeWith(p, Options{}) }

// ComposeWith reads every module, applies each one's exclude or only block and refuses a
// collision. The result
// holds one entry per kind and name, the settings merged per runtime and target file, the
// MCP servers by name, and the modules' instructions joined. A collision returns a
// [*CollisionError], which a caller matches with errors.As, and a git source that cannot
// be fetched a [*source.FetchError]. The package comment has the order of the rules and
// the merge semantics.
func ComposeWith(p *stack.Stack, opts Options) (*Result, error) {
	res := &Result{Stack: p, Base: opts.Base, Settings: map[string]map[string]map[string]any{}, MCP: map[string]map[string]any{}, Env: map[string]string{}, setBy: map[string]string{}}
	owners := map[string][]string{}
	paths := map[string]string{}
	fors := map[string]string{}
	servers := map[string]map[string]any{}
	exporters := map[string]string{}
	var instructions []string
	dirs := map[string]string{}
	requires := map[string]map[string][]string{}
	for _, pl := range p.Modules {
		ps := p.SourceOf(pl)
		who := "module " + pl.Name
		if pl.Name == "" {
			who = "module at " + ps.String()
		}
		so := source.Options{Pin: opts.Pins[ps.String()], Update: opts.Update, Cache: opts.Cache, Timeout: opts.Timeout}
		if pl.Base && opts.Base != nil && ps.Git != "" {
			// A base module is in the base's clone, fetched this compose: its pin is the
			// base's, and no update fetches it again.
			so.Pin, so.Update = opts.Base.Pin, false
		}
		src, err := source.Resolve(p.DirOf(pl), ps, so)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", who, err)
		}
		m, err := module.ReadManifest(src.Dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", who, err)
		}
		if pl.Name != "" && m.Name != pl.Name {
			return nil, fmt.Errorf("module %s: the module at %s is named %s in its %s", pl.Name, ps.String(), m.Name, module.ManifestName)
		}
		name := m.Name
		if other, ok := dirs[name]; ok {
			return nil, fmt.Errorf("module %s is composed twice, from %s and from %s", name, other, ps.String())
		}
		dirs[name] = ps.String()
		requires[name] = m.Requires
		variant, err := selectVariant(m, pl, p.Target.Runtimes, name)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", name, err)
		}
		l, err := module.Read(name, src.Dir, m, variant)
		if err != nil {
			return nil, err
		}
		rl := Module{Name: name, Description: m.Description, Dir: l.Dir, Source: ps.String(), Pin: src.Pin, Dirty: src.Dirty, Variant: variant, Link: pl.Link, Base: pl.Base}
		// The selection comes first, so that what the base allows and what the module
		// exports are checked on what the module contributes, not on what it ships.
		env, pulled, err := applySelection(l, pl, m.Env, m.Requires, res)
		if err != nil {
			return nil, err
		}
		if !pl.Base && opts.Base != nil {
			if err := checkExtending(l, opts.Base); err != nil {
				return nil, err
			}
		}
		if err := exportEnv(res, exporters, name, env, opts.Env); err != nil {
			return nil, err
		}
		res.Modules = append(res.Modules, rl)
		for _, e := range l.Entries {
			key := e.Kind + "/" + e.Name
			owners[key] = append(owners[key], name)
			paths[key+"@"+name] = e.Path
			if by, ok := pulled[key]; ok {
				fors[key+"@"+name] = by
			}
			if e.Kind == "mcp" {
				servers[key+"@"+name] = l.MCP[e.Name]
			}
		}
		for runtime, files := range l.Settings {
			if res.Settings[runtime] == nil {
				res.Settings[runtime] = map[string]map[string]any{}
			}
			for file, path := range files {
				if res.Settings[runtime][file] == nil {
					res.Settings[runtime][file] = map[string]any{}
				}
				if err := mergeFile(res, runtime, file, path, name, opts.Env); err != nil {
					return nil, fmt.Errorf("module %s: %w", name, err)
				}
			}
		}
		if l.Instructions != "" {
			data, err := os.ReadFile(l.Instructions)
			if err != nil {
				return nil, err
			}
			instructions = append(instructions, strings.TrimRight(string(data), "\n"))
		}
	}
	if err := collisions(owners, res.Modules, opts.Base); err != nil {
		return nil, err
	}
	if err := exportsAgainstSettings(res, exporters, opts.Env); err != nil {
		return nil, err
	}
	for key, ls := range owners {
		kind, name, _ := strings.Cut(key, "/")
		res.Entries = append(res.Entries, Entry{Kind: kind, Name: name, Module: ls[0], Path: paths[key+"@"+ls[0]], For: fors[key+"@"+ls[0]]})
		if kind == "mcp" {
			res.MCP[name] = servers[key+"@"+ls[0]]
		}
	}
	sort.Slice(res.Entries, func(i, j int) bool {
		if res.Entries[i].Kind != res.Entries[j].Kind {
			return res.Entries[i].Kind < res.Entries[j].Kind
		}
		return res.Entries[i].Name < res.Entries[j].Name
	})
	if err := checkRequires(res, requires); err != nil {
		return nil, err
	}
	if len(instructions) > 0 {
		res.Instructions = strings.Join(instructions, "\n\n") + "\n"
	}
	for name, value := range opts.Env {
		res.Env[name] = value
	}
	return res, nil
}

// exportEnv adds a module's exported variables to the result, each as
// $QORY_HARNESS_HOME/modules/<module>/<path>, the path left out for ".". Two modules
// exporting one name with different values is an error, since neither is the one to
// keep, unless the configuration's env, decided, names it. The same value twice is fine.
func exportEnv(res *Result, exporters map[string]string, module string, env, decided map[string]string) error {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := "$QORY_HARNESS_HOME/modules/" + module
		if env[name] != "." {
			value += "/" + filepath.ToSlash(env[name])
		}
		if _, settled := decided[name]; settled {
			continue
		}
		if other, ok := exporters[name]; ok && res.Env[name] != value {
			return fmt.Errorf("env %s is exported by modules %s and %s; set it in qory.yaml to decide", name, other, module)
		}
		exporters[name] = module
		res.Env[name] = value
	}
	return nil
}

// EnvFor is a copy of the exported variables rendered for a home, with every
// $QORY_HARNESS_HOME replaced the way [Result.SettingsFor] replaces it. It is empty, not
// nil, when nothing is exported.
func (r *Result) EnvFor(home string) map[string]string {
	out := map[string]string{}
	for name, value := range r.Env {
		out[name] = ForHome(value, home).(string)
	}
	return out
}

// selectVariant picks the one variant of a module that serves every targeted runtime.
//
// A variant exists so that a module can ship different material to different runtimes, and a
// target naming several runtimes can therefore ask a module for two different things at once.
// The composed tree holds one copy of each entry, so there is nothing to render in that
// case: the compose refuses and names both runtimes, the way it refuses a collision. A
// module with no variants, or one whose variants resolve the same way for every targeted
// runtime, composes for all of them.
func selectVariant(m *module.Manifest, pl stack.Module, runtimes stack.Runtimes, name string) (string, error) {
	first, err := module.SelectVariant(m, pl.Variant, runtimes.First())
	if err != nil {
		return "", err
	}
	for _, r := range runtimes[1:] {
		v, err := module.SelectVariant(m, pl.Variant, r)
		if err != nil {
			return "", err
		}
		if v != first {
			return "", fmt.Errorf("module %s reads from %s for %s and from %s for %s; compose one runtime at a time, or give the module one variant for both",
				name, variantName(first), runtimes.First(), variantName(v), r)
		}
	}
	return first, nil
}

// variantName names a variant in a message, including the empty one.
func variantName(v string) string {
	if v == "" {
		return "the module root"
	}
	return "variant " + v
}

// checkRequires refuses a compose in which an entry's requirements, from its module's
// manifest, are not all composed. requires holds each module's manifest requires by module
// name, keyed <kind>/<name>. A required entry may come from any module, since a name is
// composed once; an entry that was left out has no requirements to meet. The message
// says why the entry is missing: a module's exclude left it out, or no module ships it.
// Entries are walked in their sorted order and requirements in theirs, so the first
// failure is the same every time.
func checkRequires(res *Result, requires map[string]map[string][]string) error {
	composed := map[string]bool{}
	for _, e := range res.Entries {
		composed[e.Kind+"/"+e.Name] = true
	}
	for _, e := range res.Entries {
		for _, need := range requires[e.Module][e.Kind+"/"+e.Name] {
			if composed[need] {
				continue
			}
			kind, name, _ := strings.Cut(need, "/")
			for _, x := range res.Excludes {
				if x.Kind == kind && x.Name == name {
					return fmt.Errorf("module %s: %s requires %s, which module %s leaves out", e.Module, module.Describe(e.Kind, e.Name), module.Describe(kind, name), x.Module)
				}
			}
			return fmt.Errorf("module %s: %s requires %s, which no module ships", e.Module, module.Describe(e.Kind, e.Name), module.Describe(kind, name))
		}
	}
	return nil
}

// applySelection applies the module's exclude and only blocks, reducing the module in
// place to what it contributes and recording every entry and part left out in res. It
// returns the exported variables that remain and, for each entry an only block brought
// in without naming it, the entry that required it. A name that matches nothing the
// module ships is an error, so a module that stops shipping something is noticed rather
// than composed without it. Kinds and names are walked in sorted order, so the recorded
// excludes and the first error do not follow map order.
//
// Under exclude, what is named is left out. Under only, what is named is kept, and so is
// what the kept entries require from this module, transitively, as the manifest's
// requires declares it; a kind the block does not name contributes no entry beyond
// those, and a part it does not name is left out. An exclude beside an only names
// entries the only brought in, to leave them out after all; a requirement left out that
// way has to come from another module, which [checkRequires] sees to.
func applySelection(l *module.Module, pl stack.Module, env map[string]string, requires map[string][]string, res *Result) (map[string]string, map[string]string, error) {
	sel, only := pl.Exclude, false
	if !pl.Only.Empty() {
		sel, only = pl.Only, true
	}
	block := "exclude"
	if only {
		block = "only"
	}
	if sel.Empty() {
		return env, nil, nil
	}
	shipped := map[string]bool{}
	for _, e := range l.Entries {
		shipped[e.Kind+"/"+e.Name] = true
	}
	// named is every entry the block names, checked against what the module ships.
	named := map[string]bool{}
	for _, kind := range sel.KindNames() {
		for _, name := range sel.Kinds[kind] {
			if !shipped[kind+"/"+name] {
				return nil, nil, fmt.Errorf("module %s: %s %s/%s names nothing the module ships", l.Name, block, kind, name)
			}
			named[kind+"/"+name] = true
		}
	}
	// Under only, the named entries bring in what they require from this module, each
	// pulled entry recording the first entry that needed it, in sorted order.
	pulled := map[string]string{}
	if only {
		queue := sortedKeys(named)
		for len(queue) > 0 {
			key := queue[0]
			queue = queue[1:]
			for _, need := range requires[key] {
				if !shipped[need] || named[need] {
					continue
				}
				if _, seen := pulled[need]; seen {
					continue
				}
				pulled[need] = key
				queue = append(queue, need)
			}
		}
		// An exclude beside the only names entries the only brought in, and nothing else:
		// one it did not bring in would name nothing the module contributes.
		for _, kind := range pl.Exclude.KindNames() {
			for _, name := range pl.Exclude.Kinds[kind] {
				key := kind + "/" + name
				if _, ok := pulled[key]; !ok {
					if !shipped[key] {
						return nil, nil, fmt.Errorf("module %s: exclude %s names nothing the module ships", l.Name, key)
					}
					return nil, nil, fmt.Errorf("module %s: exclude %s names nothing only brings in; only leaves it out already", l.Name, key)
				}
				delete(pulled, key)
			}
		}
	}
	var kept []module.Entry
	for _, e := range l.Entries {
		key := e.Kind + "/" + e.Name
		_, isPulled := pulled[key]
		if (named[key] || isPulled) != only {
			res.Excludes = append(res.Excludes, Exclude{Module: l.Name, Kind: e.Kind, Name: e.Name})
			continue
		}
		kept = append(kept, e)
	}
	l.Entries = kept
	// The instruction section: named means true in the block.
	if sel.Instructions && l.Instructions == "" {
		return nil, nil, fmt.Errorf("module %s: %s instructions names nothing the module ships; it has no %s", l.Name, block, module.InstructionsName)
	}
	if l.Instructions != "" && sel.Instructions != only {
		res.Excludes = append(res.Excludes, Exclude{Module: l.Name, Kind: "instructions", Name: module.InstructionsName})
		l.Instructions = ""
	}
	// The settings fragments, named as <runtime>/<file>.
	fragments := map[string]bool{}
	for runtime, files := range l.Settings {
		for file := range files {
			fragments[runtime+"/"+file] = true
		}
	}
	if sel.Settings.All && len(fragments) == 0 {
		return nil, nil, fmt.Errorf("module %s: %s settings names nothing the module ships; it has no settings fragment", l.Name, block)
	}
	for _, name := range sel.Settings.Names {
		if !fragments[name] {
			return nil, nil, fmt.Errorf("module %s: %s settings/%s names nothing the module ships", l.Name, block, name)
		}
	}
	for _, name := range sortedKeys(fragments) {
		keep := sel.Settings.All || slices.Contains(sel.Settings.Names, name)
		if keep == only {
			continue
		}
		runtime, file, _ := strings.Cut(name, "/")
		delete(l.Settings[runtime], file)
		if len(l.Settings[runtime]) == 0 {
			delete(l.Settings, runtime)
		}
		res.Excludes = append(res.Excludes, Exclude{Module: l.Name, Kind: "settings", Name: name})
	}
	// The exported variables.
	if sel.Env.All && len(env) == 0 {
		return nil, nil, fmt.Errorf("module %s: %s env names nothing the module ships; it exports no variable", l.Name, block)
	}
	for _, name := range sel.Env.Names {
		if _, ok := env[name]; !ok {
			return nil, nil, fmt.Errorf("module %s: %s env %s names nothing the module ships", l.Name, block, name)
		}
	}
	remaining := map[string]string{}
	for _, name := range sortedKeys(env) {
		keep := sel.Env.All || slices.Contains(sel.Env.Names, name)
		if keep == only {
			remaining[name] = env[name]
			continue
		}
		res.Excludes = append(res.Excludes, Exclude{Module: l.Name, Kind: "env", Name: name})
	}
	return remaining, pulled, nil
}

// Collision is one entry name that more than one module provides.
type Collision struct {
	// Kind is the entry kind the collision is in.
	Kind string
	// Name is the entry name more than one module provides.
	Name string
	// Modules provide the entry, in stack order.
	Modules []string
	// Base names the base stack, as <name>@<pin>, when one of the modules is the base's:
	// no exclude resolves that collision, the extending module renames its entry.
	Base string
}

// CollisionError is the compose refusing an undeclared collision. [Compose] returns it for
// every colliding entry at once, and the command module matches it with errors.As to print
// the modules involved and the excludes that resolve them.
type CollisionError struct {
	// Collisions are the colliding entries, sorted by kind then name.
	Collisions []Collision
	// Pins maps a module name to its pin, for the message.
	Pins map[string]string
	// Sources maps a module name to its source as text, for the command's table.
	Sources map[string]string
	// Order lists the module names in stack order, for [CollisionError.Suggest].
	Order []string
}

// Suggest returns, per module, the excludes that resolve every collision by keeping the last
// module that ships each. The modules result holds the names that need an exclude, in the
// order of the order argument, which a caller passes in stack order. The excludes result
// is keyed by module name, then by kind, and holds the entry names that module excludes.
func (e *CollisionError) Suggest(order []string) (modules []string, excludes map[string]map[string][]string) {
	excludes = map[string]map[string][]string{}
	for _, c := range e.Collisions {
		if c.Base != "" {
			continue
		}
		for _, l := range c.Modules[:len(c.Modules)-1] {
			if excludes[l] == nil {
				excludes[l] = map[string][]string{}
			}
			excludes[l][c.Kind] = append(excludes[l][c.Kind], c.Name)
		}
	}
	for _, l := range order {
		if excludes[l] != nil {
			modules = append(modules, l)
		}
	}
	return modules, excludes
}

// Error is the plain-text form: each collision with the modules that ship it and their pins,
// then the excludes that resolve it. Collisions are separated by a blank line and the text
// carries no trailing newline. The fixtures under contracts/harness/v1 hold it verbatim.
func (e *CollisionError) Error() string {
	var b strings.Builder
	for i, c := range e.Collisions {
		if i > 0 {
			b.WriteString("\n")
		}
		var named []string
		width := 0
		for _, l := range c.Modules {
			named = append(named, l+"@"+e.Pins[l])
			if len(l)+1 > width {
				width = len(l) + 1
			}
		}
		fmt.Fprintf(&b, "%s/%s is provided by %d modules: %s\n", c.Kind, c.Name, len(c.Modules), strings.Join(named, ", "))
		if c.Base != "" {
			fmt.Fprintf(&b, "  it belongs to the base stack %s; rename yours\n", c.Base)
			continue
		}
		b.WriteString("  keep one and exclude the others, for example\n")
		for _, l := range c.Modules[:len(c.Modules)-1] {
			fmt.Fprintf(&b, "    %-*s exclude: {%s: [%s]}\n", width, l+":", c.Kind, c.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// collisions builds the CollisionError for every entry more than one module owns, and returns
// nil when no name is owned twice. Keys are sorted, so the message does not follow map order.
func collisions(owners map[string][]string, modules []Module, base *Base) error {
	pin := map[string]string{}
	sources := map[string]string{}
	inBase := map[string]bool{}
	var order []string
	for _, l := range modules {
		pin[l.Name] = l.Pin
		sources[l.Name] = l.Source
		inBase[l.Name] = l.Base
		order = append(order, l.Name)
	}
	var keys []string
	for key, ls := range owners {
		if len(ls) > 1 {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	e := &CollisionError{Pins: pin, Sources: sources, Order: order}
	for _, key := range keys {
		kind, name, _ := strings.Cut(key, "/")
		c := Collision{Kind: kind, Name: name, Modules: owners[key]}
		for _, l := range owners[key] {
			if inBase[l] && base != nil {
				c.Base = base.String()
			}
		}
		e.Collisions = append(e.Collisions, c)
	}
	return e
}

// mergeFile decodes one settings fragment of the named module and folds it into the
// result's target file for the runtime. The extension decides the format, and a TOML
// document is normalized to the types the JSON decoder produces. The top-level key decides
// how lists combine, and the mode carries down the whole subtree under that key. A key path
// another module set to a different value is an error, unless it is env.<NAME> and decided
// names NAME: the configuration's value is written then, whatever the fragments say.
func mergeFile(res *Result, runtime, file, path, module string, decided map[string]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src := map[string]any{}
	switch filepath.Ext(path) {
	case ".json":
		if err := json.Unmarshal(data, &src); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	case ".toml":
		if err := toml.Unmarshal(data, &src); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		src = normalize(src).(map[string]any)
	default:
		return fmt.Errorf("%s: settings fragments are .json or .toml", path)
	}
	m := &merger{file: "settings/" + runtime + "/" + file, module: module, setBy: res.setBy}
	dst := res.Settings[runtime][file]
	for _, key := range sortedKeys(src) {
		v := src[key]
		mode := once
		switch key {
		case "permissions":
			mode = concatDedupe
		case "hooks":
			mode = concat
		case "env":
			v = m.decide(v, decided)
		}
		merged, err := m.merge(dst[key], v, key, mode)
		if err != nil {
			return err
		}
		dst[key] = merged
	}
	return nil
}

// listMode says how merge combines two lists: once takes a list as one value that a
// second module may repeat and not change, concat appends the later list, concatDedupe
// appends the elements the earlier list does not already hold.
type listMode int

const (
	once listMode = iota
	concat
	concatDedupe
)

// merger folds one module's fragment into one target file and records which module set
// each leaf.
type merger struct {
	// file is settings/<runtime>/<file>, the prefix of the setBy keys and the messages.
	file string
	// module is the module whose fragment is merged.
	module string
	// setBy is [Result.setBy], shared across the modules.
	setBy map[string]string
}

// decide writes the configuration's value over every key of a fragment's env map the
// configuration names, so the fragments' values for it are never compared. Any other
// value is returned as is.
func (m *merger) decide(v any, decided map[string]string) any {
	env, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for name := range env {
		if value, ok := decided[name]; ok {
			env[name] = value
		}
	}
	return env
}

// merge folds src into dst at path and returns the result. Maps merge by key; lists follow
// mode; a scalar, a list in mode once and a value whose type differs from dst's are one
// leaf: the first module sets it, another may repeat the value and not change it.
func (m *merger) merge(dst, src any, path string, mode listMode) (any, error) {
	key := m.file + "/" + path
	switch s := src.(type) {
	case map[string]any:
		d, ok := dst.(map[string]any)
		if dst != nil && !ok {
			return nil, m.collision(path)
		}
		if !ok {
			d = map[string]any{}
			m.setBy[key] = m.module
		}
		for _, k := range sortedKeys(s) {
			v, err := m.merge(d[k], s[k], path+"."+k, mode)
			if err != nil {
				return nil, err
			}
			d[k] = v
		}
		return d, nil
	case []any:
		if d, ok := dst.([]any); ok && mode != once {
			out := append([]any{}, d...)
			for _, v := range s {
				if mode == concatDedupe && contains(out, v) {
					continue
				}
				out = append(out, v)
			}
			return out, nil
		}
	}
	if dst != nil && !reflect.DeepEqual(dst, src) {
		return nil, m.collision(path)
	}
	if _, set := m.setBy[key]; !set {
		m.setBy[key] = m.module
	}
	return src, nil
}

// collision is the error for a key path two modules set to different values.
func (m *merger) collision(path string) error {
	return fmt.Errorf("%s: %s is set by modules %s and %s with different values", m.file, path, m.setBy[m.file+"/"+path], m.module)
}

// exportsAgainstSettings refuses a variable a module's manifest exports that a fragment's
// env map sets to a different value, for every runtime and target file, unless decided
// names it. Runtimes, files and names are walked in sorted order, so the error does not
// follow map order.
func exportsAgainstSettings(res *Result, exporters map[string]string, decided map[string]string) error {
	for _, runtime := range sortedKeys(res.Settings) {
		for _, file := range sortedKeys(res.Settings[runtime]) {
			env, _ := res.Settings[runtime][file]["env"].(map[string]any)
			for _, name := range sortedKeys(env) {
				exporter, exported := exporters[name]
				if _, settled := decided[name]; settled || !exported || reflect.DeepEqual(env[name], res.Env[name]) {
					continue
				}
				prefix := "settings/" + runtime + "/" + file
				return fmt.Errorf("%s: env.%s is set by module %s and exported by module %s with different values",
					prefix, name, res.setBy[prefix+"/env."+name], exporter)
			}
		}
	}
	return nil
}

// sortedKeys is the keys of a map, sorted.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// contains reports whether list holds v, compared by printed form, so a number and the
// string of the same digits count as one element.
func contains(list []any, v any) bool {
	for _, x := range list {
		if fmt.Sprint(x) == fmt.Sprint(v) {
			return true
		}
	}
	return false
}

// SettingsFor is a copy of one merged settings file rendered for a home: every
// "$QORY_HARNESS_HOME" and "${QORY_HARNESS_HOME}" in a string becomes the home path, at
// any depth. The copy shares nothing with [Result.Settings], so the caller may write to it.
// A file no module contributed to is an empty map.
func (r *Result) SettingsFor(runtime, file, home string) map[string]any {
	src := r.Settings[runtime][file]
	if src == nil {
		src = map[string]any{}
	}
	return ForHome(src, home).(map[string]any)
}

// MCPFor is a copy of the composed MCP servers rendered for a home, name to object, with
// every $QORY_HARNESS_HOME replaced the way [Result.SettingsFor] replaces it, and without
// the description, which is a note to the module's readers and not a key a runtime
// starts a server with. It is nil when no module ships a server.
func (r *Result) MCPFor(home string) map[string]any {
	if len(r.MCP) == 0 {
		return nil
	}
	out := map[string]any{}
	for name, server := range r.MCP {
		rendered := ForHome(server, home).(map[string]any)
		delete(rendered, "description")
		out[name] = rendered
	}
	return out
}

// ForHome is a copy of a decoded settings value with every "$QORY_HARNESS_HOME" and
// "${QORY_HARNESS_HOME}" inside a string replaced by home, at any depth. The copy shares
// nothing mutable with v.
func ForHome(v any, home string) any {
	out := substitute(v, "${QORY_HARNESS_HOME}", home)
	return substitute(out, "$QORY_HARNESS_HOME", home)
}

// SettingsFiles lists the target files modules contributed to for a runtime, sorted by name.
// A runtime no module ships a fragment for lists nothing.
func (r *Result) SettingsFiles(runtime string) []string {
	var files []string
	for f := range r.Settings[runtime] {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

// normalize turns the map types a TOML decoder produces into the JSON ones merge works on.
// A TOML array of tables decodes as []map[string]any, which merge does not read as a list.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalize(e)
		}
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case []any:
		for i, e := range x {
			x[i] = normalize(e)
		}
		return x
	default:
		return v
	}
}

// substitute returns a copy of v with every occurrence of from inside a string replaced by
// to. It rebuilds the maps and lists it walks, so the result shares nothing mutable with v.
func substitute(v any, from, to string) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, from, to)
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			out[k] = substitute(e, from, to)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = substitute(e, from, to)
		}
		return out
	default:
		return v
	}
}
