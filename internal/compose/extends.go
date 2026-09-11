package compose

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/qoryai/qory/internal/module"
	"github.com/qoryai/qory/internal/source"
	"github.com/qoryai/qory/internal/stack"
)

// Base is the stack a checkout's qory.yaml appends to, as the report records it.
type Base struct {
	// Name is the base stack's name, or the name of the directory holding it.
	Name string
	// Source is the extends source as the stack writes it.
	Source string
	// Pin is what the source resolved to: the commit for a git source, "working-tree" for
	// a path.
	Pin string
	// Extending is what the base lets an extending module ship.
	Extending *stack.Extending
	// Qory is the range of qory versions the base is written for, empty for none. The
	// command checks the running qory against it, as it does the checkout's own.
	Qory stack.Constraint
}

// String names the base in a message, <name>@<pin>.
func (b *Base) String() string { return b.Name + "@" + b.Pin }

// LoadBase resolves the base stack p extends and returns the stack to compose, the
// base's modules first and closed, with the base recorded for the result. It is
// [stack.Extend] with the base fetched: the extends source names a directory holding
// [stack.FileName]; pin is the commit the last report recorded for it, "" for none. A
// stack, which extends nothing, comes back as it is with a nil base.
//
// A git source that cannot be fetched is a [*source.FetchError] whose message says the
// stack is not reachable from here, because a base is usually reachable from the
// machine that runs the harness and not from every laptop.
func LoadBase(p *stack.Stack, pin string, opts Options) (*stack.Stack, *Base, error) {
	if p.Extends.Path == "" && p.Extends.Git == "" {
		return p, nil, nil
	}
	src, err := source.Resolve(p.Dir(), p.Extends, source.Options{Pin: pin, Update: opts.Update, Cache: opts.Cache, Timeout: opts.Timeout})
	var fetch *source.FetchError
	if errors.As(err, &fetch) {
		return nil, nil, fmt.Errorf("the stack %s is not reachable from here: %w", p.Extends.String(), err)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("extends %s: %w", p.Extends.String(), err)
	}
	file := filepath.Join(src.Dir, stack.FileName)
	base, err := stack.Load(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("extends %s: %s holds no %s", p.Extends.String(), src.Dir, stack.FileName)
	}
	if err != nil {
		return nil, nil, err
	}
	merged, err := stack.Extend(base, p)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", p.File, err)
	}
	b := &Base{Name: base.Name, Source: p.Extends.String(), Pin: src.Pin, Extending: base.Extending, Qory: base.Qory}
	if b.Name == "" {
		b.Name = filepath.Base(src.Dir)
	}
	return merged, b, nil
}

// checkExtending refuses what an extending module ships beyond what the base allows: an
// entry of a kind outside extending.kinds, a file outside the extending.files prefixes,
// AGENTS.md without extending.instructions, and a settings fragment setting a key outside
// extending.settings. Each message names the module, what it ships and the base.
func checkExtending(l *module.Module, base *Base) error {
	allowed := map[string]bool{}
	for _, k := range base.Extending.Kinds {
		allowed[k] = true
	}
	for _, e := range l.Entries {
		if e.Kind == "files" {
			if !stack.FileAllowed(e.Name, base.Extending.Files) {
				return fmt.Errorf("module %s ships files/%s, and the base stack %s lets an extending module ship files under %s", l.Name, e.Name, base, filesText(base.Extending.Files))
			}
			continue
		}
		if !allowed[e.Kind] {
			return fmt.Errorf("module %s ships %s/%s, and the base stack %s lets an extending module ship %s", l.Name, e.Kind, e.Name, base, kindsText(base.Extending.Kinds))
		}
	}
	if l.Instructions != "" && !base.Extending.Instructions {
		return fmt.Errorf("module %s ships %s, and the base stack %s lets an extending module ship no instructions", l.Name, module.InstructionsName, base)
	}
	runtimes := make([]string, 0, len(l.Settings))
	for rt := range l.Settings {
		runtimes = append(runtimes, rt)
	}
	sort.Strings(runtimes)
	for _, rt := range runtimes {
		files := make([]string, 0, len(l.Settings[rt]))
		for f := range l.Settings[rt] {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			keys, err := leafKeys(l.Settings[rt][f])
			if err != nil {
				return fmt.Errorf("module %s: %w", l.Name, err)
			}
			for _, key := range keys {
				if !settingAllowed(key, base.Extending.Settings) {
					return fmt.Errorf("module %s sets %s in settings/%s/%s, and the base stack %s lets an extending module set %s", l.Name, key, rt, f, base, settingsText(base.Extending.Settings))
				}
			}
		}
	}
	return nil
}

// settingAllowed reports whether a dotted key path is one of the allowed paths or under
// one of them.
func settingAllowed(key string, allowed []string) bool {
	for _, a := range allowed {
		if key == a || strings.HasPrefix(key, a+".") {
			return true
		}
	}
	return false
}

// leafKeys lists the dotted key paths a settings fragment sets, sorted: every scalar and
// every list, at any depth, and an empty map by its own path.
func leafKeys(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc := map[string]any{}
	switch filepath.Ext(path) {
	case ".json":
		err = json.Unmarshal(data, &doc)
	case ".toml":
		err = toml.Unmarshal(data, &doc)
	default:
		return nil, fmt.Errorf("%s: settings fragments are .json or .toml", path)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var keys []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		m, ok := v.(map[string]any)
		if !ok || len(m) == 0 {
			keys = append(keys, prefix)
			return
		}
		for k, e := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			walk(key, e)
		}
	}
	for k, v := range doc {
		walk(k, v)
	}
	sort.Strings(keys)
	return keys, nil
}

// kindsText lists kinds for a message, "nothing" for none.
func kindsText(kinds []string) string {
	if len(kinds) == 0 {
		return "no entries"
	}
	return strings.Join(kinds, ", ")
}

// filesText lists allowed file prefixes for a message, "no path" for none.
func filesText(prefixes []string) string {
	if len(prefixes) == 0 {
		return "no path"
	}
	return strings.Join(prefixes, ", ")
}

// settingsText lists allowed settings paths for a message, "no settings" for none.
func settingsText(keys []string) string {
	if len(keys) == 0 {
		return "no settings"
	}
	return strings.Join(keys, ", ")
}
