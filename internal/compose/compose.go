package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/layer"
	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/source"
)

// Layer is a profile layer resolved to a directory.
type Layer struct {
	// Name is the layer's name as its manifest declares it, the name excludes, the tree and
	// messages use.
	Name string
	// Dir is the absolute directory the layer was read from.
	Dir string
	// Source is the profile's source as text: the path of a path source, <git>#<ref> for a
	// git source.
	Source string
	// Pin is what the source resolved to: "working-tree" for a path, the commit for a git
	// source.
	Pin string
	// Dirty is set when git sees uncommitted changes under Dir.
	Dirty bool
	// Variant is the variant chosen for the target runtime, "" for a layer without one.
	Variant string
	// Link is the checkout-root name the profile links the layer's directory as, "" for
	// none.
	Link string
	// Base marks a layer of the base profile, when the profile extends one.
	Base bool
}

// Entry is one entry of the composed tree and the layer it came from.
type Entry struct {
	// Kind is one of skills, agents, commands, output-styles, hooks, mcp.
	Kind string
	// Name is the entry's name within its kind, one per kind across the composed tree.
	Name string
	// Layer is the name of the layer that provides the entry.
	Layer string
	// Path is absolute: the skill directory, the markdown file, the hook script or the MCP
	// server's JSON file.
	Path string
}

// Exclude is one entry a layer left out.
type Exclude struct {
	// Layer is the layer the entry was dropped from.
	Layer string
	// Kind is the entry kind the exclude named.
	Kind string
	// Name is the dropped entry's name.
	Name string
}

// Result is a composed profile: what a renderer writes and what the report records.
type Result struct {
	// Profile is the profile that was composed.
	Profile *profile.Profile
	// Layers are the profile's layers, resolved, in profile order.
	Layers []Layer
	// Entries are the composed entries, sorted by kind then name.
	Entries []Entry
	// Excludes are the entries the layers left out, in layer order, by kind.
	Excludes []Exclude
	// Settings are the merged fragments: per runtime, per target file, in layer order.
	Settings map[string]map[string]map[string]any
	// MCP holds the composed MCP servers by name, each the object its layer's
	// mcp/<name>.json holds, with every $QORY_HARNESS_HOME still in place. [Result.MCPFor]
	// renders it for a home.
	MCP map[string]map[string]any
	// Instructions are the layers' AGENTS.md files joined by a blank line, or "".
	Instructions string
	// Env are the variables the harness exports, name to value, with every
	// $QORY_HARNESS_HOME still in place: what the layer manifests export, as
	// $QORY_HARNESS_HOME/layers/<name>/<path>, and the configuration's variables over
	// them. [Result.EnvFor] renders it for a home.
	Env map[string]string
	// Base is the base profile the profile extends, nil when it extends none.
	Base *Base
	// setBy is the layer that set each settings leaf, keyed
	// settings/<runtime>/<file>/<dotted.key.path>, so a later fragment setting the same
	// path to another value names both layers.
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
	// Env are the configuration's variables, written over what the layers export.
	Env map[string]string
	// Base is the base profile [LoadBase] resolved, recorded in the result and enforced on
	// the layers that are not its own; nil when the profile extends none.
	Base *Base
}

// Compose is [ComposeWith] and the default options.
func Compose(p *profile.Profile) (*Result, error) { return ComposeWith(p, Options{}) }

// ComposeWith reads every layer, applies the excludes and refuses a collision. The result
// holds one entry per kind and name, the settings merged per runtime and target file, the
// MCP servers by name, and the layers' instructions joined. A collision returns a
// [*CollisionError], which a caller matches with errors.As, and a git source that cannot
// be fetched a [*source.FetchError]. The package comment has the order of the rules and
// the merge semantics.
func ComposeWith(p *profile.Profile, opts Options) (*Result, error) {
	res := &Result{Profile: p, Base: opts.Base, Settings: map[string]map[string]map[string]any{}, MCP: map[string]map[string]any{}, Env: map[string]string{}, setBy: map[string]string{}}
	owners := map[string][]string{}
	paths := map[string]string{}
	servers := map[string]map[string]any{}
	exporters := map[string]string{}
	var instructions []string
	dirs := map[string]string{}
	for _, pl := range p.Layers {
		ps := p.SourceOf(pl)
		who := "layer " + pl.Name
		if pl.Name == "" {
			who = "layer at " + ps.String()
		}
		so := source.Options{Pin: opts.Pins[ps.String()], Update: opts.Update, Cache: opts.Cache, Timeout: opts.Timeout}
		if pl.Base && opts.Base != nil && ps.Git != "" {
			// A base layer is in the base's clone, fetched this compose: its pin is the
			// base's, and no update fetches it again.
			so.Pin, so.Update = opts.Base.Pin, false
		}
		src, err := source.Resolve(p.DirOf(pl), ps, so)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", who, err)
		}
		m, err := layer.ReadManifest(src.Dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", who, err)
		}
		if pl.Name != "" && m.Name != pl.Name {
			return nil, fmt.Errorf("layer %s: the layer at %s is named %s in its %s", pl.Name, ps.String(), m.Name, layer.ManifestName)
		}
		name := m.Name
		if other, ok := dirs[name]; ok {
			return nil, fmt.Errorf("layer %s is composed twice, from %s and from %s", name, other, ps.String())
		}
		dirs[name] = ps.String()
		variant, err := selectVariant(m, pl, p.Target.Runtimes, name)
		if err != nil {
			return nil, fmt.Errorf("layer %s: %w", name, err)
		}
		l, err := layer.Read(name, src.Dir, m, variant)
		if err != nil {
			return nil, err
		}
		rl := Layer{Name: name, Dir: l.Dir, Source: ps.String(), Pin: src.Pin, Dirty: src.Dirty, Variant: variant, Link: pl.Link, Base: pl.Base}
		if !pl.Base && opts.Base != nil {
			if err := checkExtending(l, opts.Base); err != nil {
				return nil, err
			}
		}
		if err := exportEnv(res, exporters, name, m.Env, opts.Env); err != nil {
			return nil, err
		}
		res.Layers = append(res.Layers, rl)
		entries, err := applyExcludes(l, pl.Exclude, res)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			key := e.Kind + "/" + e.Name
			owners[key] = append(owners[key], name)
			paths[key+"@"+name] = e.Path
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
					return nil, fmt.Errorf("layer %s: %w", name, err)
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
	if err := collisions(owners, res.Layers, opts.Base); err != nil {
		return nil, err
	}
	if err := exportsAgainstSettings(res, exporters, opts.Env); err != nil {
		return nil, err
	}
	for key, ls := range owners {
		kind, name, _ := strings.Cut(key, "/")
		res.Entries = append(res.Entries, Entry{Kind: kind, Name: name, Layer: ls[0], Path: paths[key+"@"+ls[0]]})
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
	if len(instructions) > 0 {
		res.Instructions = strings.Join(instructions, "\n\n") + "\n"
	}
	for name, value := range opts.Env {
		res.Env[name] = value
	}
	return res, nil
}

// exportEnv adds a layer's exported variables to the result, each as
// $QORY_HARNESS_HOME/layers/<layer>/<path>, the path left out for ".". Two layers
// exporting one name with different values is an error, since neither is the one to
// keep, unless the configuration's env, decided, names it. The same value twice is fine.
func exportEnv(res *Result, exporters map[string]string, layer string, env, decided map[string]string) error {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := "$QORY_HARNESS_HOME/layers/" + layer
		if env[name] != "." {
			value += "/" + filepath.ToSlash(env[name])
		}
		if _, settled := decided[name]; settled {
			continue
		}
		if other, ok := exporters[name]; ok && res.Env[name] != value {
			return fmt.Errorf("env %s is exported by layers %s and %s; set it in qory.yaml to decide", name, other, layer)
		}
		exporters[name] = layer
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

// selectVariant picks the one variant of a layer that serves every targeted runtime.
//
// A variant exists so that a layer can ship different material to different runtimes, and a
// target naming several runtimes can therefore ask a layer for two different things at once.
// The composed tree holds one copy of each entry, so there is nothing to render in that
// case: the compose refuses and names both runtimes, the way it refuses a collision. A
// layer with no variants, or one whose variants resolve the same way for every targeted
// runtime, composes for all of them.
func selectVariant(m *layer.Manifest, pl profile.Layer, runtimes profile.Runtimes, name string) (string, error) {
	first, err := layer.SelectVariant(m, pl.Variant, runtimes.First())
	if err != nil {
		return "", err
	}
	for _, r := range runtimes[1:] {
		v, err := layer.SelectVariant(m, pl.Variant, r)
		if err != nil {
			return "", err
		}
		if v != first {
			return "", fmt.Errorf("layer %s reads from %s for %s and from %s for %s; compose one runtime at a time, or give the layer one variant for both",
				name, variantName(first), runtimes.First(), variantName(v), r)
		}
	}
	return first, nil
}

// variantName names a variant in a message, including the empty one.
func variantName(v string) string {
	if v == "" {
		return "the layer root"
	}
	return "variant " + v
}

// applyExcludes returns the layer's entries minus the ones its excludes name, and records
// each dropped entry in res. An exclude that names nothing the layer ships is an error, so a
// layer that stops shipping an entry is noticed rather than composed without it. Kinds are
// walked in sorted order, so the recorded excludes and the error do not follow map order.
func applyExcludes(l *layer.Layer, exclude map[string][]string, res *Result) ([]layer.Entry, error) {
	drop := map[string]bool{}
	kinds := make([]string, 0, len(exclude))
	for kind := range exclude {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		for _, name := range exclude[kind] {
			found := false
			for _, e := range l.Entries {
				if e.Kind == kind && e.Name == name {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("layer %s: exclude %s/%s names nothing the layer ships", l.Name, kind, name)
			}
			drop[kind+"/"+name] = true
			res.Excludes = append(res.Excludes, Exclude{Layer: l.Name, Kind: kind, Name: name})
		}
	}
	var kept []layer.Entry
	for _, e := range l.Entries {
		if !drop[e.Kind+"/"+e.Name] {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

// Collision is one entry name that more than one layer provides.
type Collision struct {
	// Kind is the entry kind the collision is in.
	Kind string
	// Name is the entry name more than one layer provides.
	Name string
	// Layers provide the entry, in profile order.
	Layers []string
	// Base names the base profile, as <name>@<pin>, when one of the layers is the base's:
	// no exclude resolves that collision, the extending layer renames its entry.
	Base string
}

// CollisionError is the compose refusing an undeclared collision. [Compose] returns it for
// every colliding entry at once, and the command layer matches it with errors.As to print
// the layers involved and the excludes that resolve them.
type CollisionError struct {
	// Collisions are the colliding entries, sorted by kind then name.
	Collisions []Collision
	// Pins maps a layer name to its pin, for the message.
	Pins map[string]string
	// Sources maps a layer name to its source as text, for the command's table.
	Sources map[string]string
	// Order lists the layer names in profile order, for [CollisionError.Suggest].
	Order []string
}

// Suggest returns, per layer, the excludes that resolve every collision by keeping the last
// layer that ships each. The layers result holds the names that need an exclude, in the
// order of the order argument, which a caller passes in profile order. The excludes result
// is keyed by layer name, then by kind, and holds the entry names that layer excludes.
func (e *CollisionError) Suggest(order []string) (layers []string, excludes map[string]map[string][]string) {
	excludes = map[string]map[string][]string{}
	for _, c := range e.Collisions {
		if c.Base != "" {
			continue
		}
		for _, l := range c.Layers[:len(c.Layers)-1] {
			if excludes[l] == nil {
				excludes[l] = map[string][]string{}
			}
			excludes[l][c.Kind] = append(excludes[l][c.Kind], c.Name)
		}
	}
	for _, l := range order {
		if excludes[l] != nil {
			layers = append(layers, l)
		}
	}
	return layers, excludes
}

// Error is the plain-text form: each collision with the layers that ship it and their pins,
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
		for _, l := range c.Layers {
			named = append(named, l+"@"+e.Pins[l])
			if len(l)+1 > width {
				width = len(l) + 1
			}
		}
		fmt.Fprintf(&b, "%s/%s is provided by %d layers: %s\n", c.Kind, c.Name, len(c.Layers), strings.Join(named, ", "))
		if c.Base != "" {
			fmt.Fprintf(&b, "  it belongs to the base profile %s; rename yours\n", c.Base)
			continue
		}
		b.WriteString("  keep one and exclude the others, for example\n")
		for _, l := range c.Layers[:len(c.Layers)-1] {
			fmt.Fprintf(&b, "    %-*s exclude: {%s: [%s]}\n", width, l+":", c.Kind, c.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// collisions builds the CollisionError for every entry more than one layer owns, and returns
// nil when no name is owned twice. Keys are sorted, so the message does not follow map order.
func collisions(owners map[string][]string, layers []Layer, base *Base) error {
	pin := map[string]string{}
	sources := map[string]string{}
	inBase := map[string]bool{}
	var order []string
	for _, l := range layers {
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
		c := Collision{Kind: kind, Name: name, Layers: owners[key]}
		for _, l := range owners[key] {
			if inBase[l] && base != nil {
				c.Base = base.String()
			}
		}
		e.Collisions = append(e.Collisions, c)
	}
	return e
}

// mergeFile decodes one settings fragment of the named layer and folds it into the
// result's target file for the runtime. The extension decides the format, and a TOML
// document is normalized to the types the JSON decoder produces. The top-level key decides
// how lists combine, and the mode carries down the whole subtree under that key. A key path
// another layer set to a different value is an error, unless it is env.<NAME> and decided
// names NAME: the configuration's value is written then, whatever the fragments say.
func mergeFile(res *Result, runtime, file, path, layer string, decided map[string]string) error {
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
	m := &merger{file: "settings/" + runtime + "/" + file, layer: layer, setBy: res.setBy}
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
// second layer may repeat and not change, concat appends the later list, concatDedupe
// appends the elements the earlier list does not already hold.
type listMode int

const (
	once listMode = iota
	concat
	concatDedupe
)

// merger folds one layer's fragment into one target file and records which layer set
// each leaf.
type merger struct {
	// file is settings/<runtime>/<file>, the prefix of the setBy keys and the messages.
	file string
	// layer is the layer whose fragment is merged.
	layer string
	// setBy is [Result.setBy], shared across the layers.
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
// leaf: the first layer sets it, another may repeat the value and not change it.
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
			m.setBy[key] = m.layer
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
		m.setBy[key] = m.layer
	}
	return src, nil
}

// collision is the error for a key path two layers set to different values.
func (m *merger) collision(path string) error {
	return fmt.Errorf("%s: %s is set by layers %s and %s with different values", m.file, path, m.setBy[m.file+"/"+path], m.layer)
}

// exportsAgainstSettings refuses a variable a layer's manifest exports that a fragment's
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
				return fmt.Errorf("%s: env.%s is set by layer %s and exported by layer %s with different values",
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
// A file no layer contributed to is an empty map.
func (r *Result) SettingsFor(runtime, file, home string) map[string]any {
	src := r.Settings[runtime][file]
	if src == nil {
		src = map[string]any{}
	}
	return ForHome(src, home).(map[string]any)
}

// MCPFor is a copy of the composed MCP servers rendered for a home, name to object, with
// every $QORY_HARNESS_HOME replaced the way [Result.SettingsFor] replaces it, and without
// the description, which is a note to the layer's readers and not a key a runtime
// starts a server with. It is nil when no layer ships a server.
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

// SettingsFiles lists the target files layers contributed to for a runtime, sorted by name.
// A runtime no layer ships a fragment for lists nothing.
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
