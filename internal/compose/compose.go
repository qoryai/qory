package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/layer"
	"github.com/qoryai/qory/internal/profile"
	"github.com/qoryai/qory/internal/source"
)

// Layer is a profile layer resolved to a directory.
type Layer struct {
	// Name is the profile's name for the layer, the name excludes and messages use.
	Name string
	// ManifestName is the layer's own name from harness.yaml, when it has one.
	ManifestName string
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
}

// Options are the choices a caller makes for one compose.
type Options struct {
	// Update fetches every git source again instead of reading the cached clone, so a
	// branch ref moves.
	Update bool
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
	res := &Result{Profile: p, Settings: map[string]map[string]map[string]any{}, MCP: map[string]map[string]any{}}
	owners := map[string][]string{}
	paths := map[string]string{}
	servers := map[string]map[string]any{}
	var instructions []string
	for _, pl := range p.Layers {
		src, err := source.Resolve(p.Dir(), pl.Source, opts.Update)
		if err != nil {
			return nil, fmt.Errorf("layer %s: %w", pl.Name, err)
		}
		m, err := layer.ReadManifest(src.Dir)
		if err != nil {
			return nil, fmt.Errorf("layer %s: %w", pl.Name, err)
		}
		variant, err := selectVariant(m, pl, p.Target.Runtimes)
		if err != nil {
			return nil, fmt.Errorf("layer %s: %w", pl.Name, err)
		}
		l, err := layer.Read(pl.Name, src.Dir, m, variant)
		if err != nil {
			return nil, err
		}
		rl := Layer{Name: pl.Name, Dir: l.Dir, Source: pl.Source.String(), Pin: src.Pin, Dirty: src.Dirty, Variant: variant}
		if m != nil {
			rl.ManifestName = m.Name
		}
		res.Layers = append(res.Layers, rl)
		entries, err := applyExcludes(l, pl.Exclude, res)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			key := e.Kind + "/" + e.Name
			owners[key] = append(owners[key], pl.Name)
			paths[key+"@"+pl.Name] = e.Path
			if e.Kind == "mcp" {
				servers[key+"@"+pl.Name] = l.MCP[e.Name]
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
				if err := mergeFile(res.Settings[runtime][file], path); err != nil {
					return nil, fmt.Errorf("layer %s: %w", pl.Name, err)
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
	if err := collisions(owners, res.Layers); err != nil {
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
	return res, nil
}

// selectVariant picks the one variant of a layer that serves every targeted runtime.
//
// A variant exists so that a layer can ship different material to different runtimes, and a
// target naming several runtimes can therefore ask a layer for two different things at once.
// The composed tree holds one copy of each entry, so there is nothing to render in that
// case: the compose refuses and names both runtimes, the way it refuses a collision. A
// layer with no variants, or one whose variants resolve the same way for every targeted
// runtime, composes for all of them.
func selectVariant(m *layer.Manifest, pl profile.Layer, runtimes profile.Runtimes) (string, error) {
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
				pl.Name, variantName(first), runtimes.First(), variantName(v), r)
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
}

// CollisionError is the compose refusing an undeclared collision. [Compose] returns it for
// every colliding entry at once, and the command layer matches it with errors.As to print
// the layers involved and the excludes that resolve them.
type CollisionError struct {
	// Collisions are the colliding entries, sorted by kind then name.
	Collisions []Collision
	// Pins maps a layer name to its pin, for the message.
	Pins map[string]string
}

// Suggest returns, per layer, the excludes that resolve every collision by keeping the last
// layer that ships each. The layers result holds the names that need an exclude, in the
// order of the order argument, which a caller passes in profile order. The excludes result
// is keyed by layer name, then by kind, and holds the entry names that layer excludes.
func (e *CollisionError) Suggest(order []string) (layers []string, excludes map[string]map[string][]string) {
	excludes = map[string]map[string][]string{}
	for _, c := range e.Collisions {
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
		b.WriteString("  keep one and exclude the others, for example\n")
		for _, l := range c.Layers[:len(c.Layers)-1] {
			fmt.Fprintf(&b, "    %-*s exclude: {%s: [%s]}\n", width, l+":", c.Kind, c.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// collisions builds the CollisionError for every entry more than one layer owns, and returns
// nil when no name is owned twice. Keys are sorted, so the message does not follow map order.
func collisions(owners map[string][]string, layers []Layer) error {
	pin := map[string]string{}
	for _, l := range layers {
		pin[l.Name] = l.Pin
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
	e := &CollisionError{Pins: pin}
	for _, key := range keys {
		kind, name, _ := strings.Cut(key, "/")
		e.Collisions = append(e.Collisions, Collision{Kind: kind, Name: name, Layers: owners[key]})
	}
	return e
}

// mergeFile decodes one settings fragment and folds it into dst, the target file merged so
// far. The extension decides the format, and a TOML document is normalized to the types the
// JSON decoder produces. The top-level key decides how lists combine, and the mode carries
// down the whole subtree under that key.
func mergeFile(dst map[string]any, path string) error {
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
	for key, v := range src {
		mode := replace
		switch key {
		case "permissions":
			mode = concatDedupe
		case "hooks":
			mode = concat
		}
		dst[key] = merge(dst[key], v, mode)
	}
	return nil
}

// listMode says how merge combines two lists: replace takes the later list whole, concat
// appends it, concatDedupe appends the elements the earlier list does not already hold.
type listMode int

const (
	replace listMode = iota
	concat
	concatDedupe
)

// merge folds src into dst and returns the result. Maps merge by key; lists follow mode;
// anything else is replaced. A dst of another type than src is dropped, so a later layer
// that changes a key's shape wins.
func merge(dst, src any, mode listMode) any {
	switch s := src.(type) {
	case map[string]any:
		d, ok := dst.(map[string]any)
		if !ok {
			d = map[string]any{}
		}
		for k, v := range s {
			d[k] = merge(d[k], v, mode)
		}
		return d
	case []any:
		d, ok := dst.([]any)
		if !ok || mode == replace {
			return s
		}
		out := append([]any{}, d...)
		for _, v := range s {
			if mode == concatDedupe && contains(out, v) {
				continue
			}
			out = append(out, v)
		}
		return out
	default:
		return src
	}
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
// every $QORY_HARNESS_HOME replaced the way [Result.SettingsFor] replaces it. It is nil
// when no layer ships a server.
func (r *Result) MCPFor(home string) map[string]any {
	if len(r.MCP) == 0 {
		return nil
	}
	out := map[string]any{}
	for name, server := range r.MCP {
		out[name] = ForHome(server, home)
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
