package stack

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parts are the three merged parts of a module a selection may name beside the entry
// kinds: the instruction section, the settings fragments and the exported variables.
var Parts = []string{"instructions", "settings", "env"}

// Selection is a module's exclude or only block: what of the module the compose leaves
// out, or the only things it takes. Both read the same keys.
//
//	exclude:
//	  skills: [test]              # entries by kind, from [Kinds]
//	  instructions: true          # the module's AGENTS.md
//	  settings: true              # every settings fragment, or a list: [claude/settings.json]
//	  env: [HARNESS_PROFILE]      # exported variables, or true for all of them
//
// Under exclude a named thing is left out and everything else is composed. Under only a
// named thing is composed and everything else is left out: a kind not named contributes
// no entry, and a part not named is left out. A name that matches nothing the module
// ships is an error either way, so a module that stops shipping something is noticed.
type Selection struct {
	// Kinds are the entry names per kind, for the kinds named.
	Kinds map[string][]string
	// Instructions names the module's instruction section.
	Instructions bool
	// Settings names the settings fragments: all of them, or the listed
	// <runtime>/<file> ones.
	Settings Part
	// Env names the exported variables: all of them, or the listed names.
	Env Part

	// raw is the block as read, parsed by [Selection.parse] from the stack's validation,
	// which names the module and the key in the error.
	raw *yaml.Node
}

// Part is how a selection names a merged part: all of it with true, or some of it by
// name. The zero Part names nothing.
type Part struct {
	// All is the part named whole.
	All bool
	// Names are the fragments or variables named, when All is not set.
	Names []string
}

// Set reports whether the part is named at all.
func (p Part) Set() bool { return p.All || len(p.Names) > 0 }

// Empty reports whether the selection names nothing: the block was left out.
func (s Selection) Empty() bool {
	return len(s.Kinds) == 0 && !s.Instructions && !s.Settings.Set() && !s.Env.Set()
}

// IsZero is [Selection.Empty], for the YAML encoder's omitempty.
func (s Selection) IsZero() bool { return s.Empty() }

// KindNames lists the kinds the selection names, sorted, so a walk over them does not
// follow map order.
func (s Selection) KindNames() []string {
	kinds := make([]string, 0, len(s.Kinds))
	for k := range s.Kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// UnmarshalYAML keeps the block for [Selection.parse], so that the error for a mistake
// in it can name the module and the key, which the decoder cannot.
func (s *Selection) UnmarshalYAML(n *yaml.Node) error {
	*s = Selection{raw: n}
	return nil
}

// parse reads the kept block: a mapping whose keys are kinds with a list of names,
// instructions with true, and settings and env with true or a list of names. An unknown
// key, a false, and an empty list are errors, each naming the key; the caller prefixes
// the module and whether the block is exclude or only. A selection built in code, with
// no block kept, is left as it is.
func (s *Selection) parse() error {
	n := s.raw
	if n == nil {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return errors.New("is a mapping of kinds and parts to what they name")
	}
	*s = Selection{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i].Value, n.Content[i+1]
		switch {
		case isKind(key):
			var names []string
			if err := value.Decode(&names); err != nil || len(names) == 0 {
				return fmt.Errorf("%s is a list of entry names", key)
			}
			for _, name := range names {
				if name == "" {
					return fmt.Errorf("%s names an empty name", key)
				}
			}
			if s.Kinds == nil {
				s.Kinds = map[string][]string{}
			}
			s.Kinds[key] = names
		case key == "instructions":
			var all bool
			if err := value.Decode(&all); err != nil || !all {
				return errors.New("instructions is true, which names the module's AGENTS.md")
			}
			s.Instructions = true
		case key == "settings", key == "env":
			part, err := decodePart(key, value)
			if err != nil {
				return err
			}
			if key == "settings" {
				s.Settings = part
			} else {
				s.Env = part
			}
		default:
			return fmt.Errorf("names kind %q; kinds: %s; parts: %s", key, strings.Join(Kinds, ", "), strings.Join(Parts, ", "))
		}
	}
	return nil
}

// decodePart reads true, or a non-empty list of names, for a settings or env key.
func decodePart(key string, n *yaml.Node) (Part, error) {
	var all bool
	if err := n.Decode(&all); err == nil {
		if !all {
			return Part{}, fmt.Errorf("%s is true or a list of names", key)
		}
		return Part{All: true}, nil
	}
	var names []string
	if err := n.Decode(&names); err != nil || len(names) == 0 {
		return Part{}, fmt.Errorf("%s is true or a list of names", key)
	}
	for _, name := range names {
		if name == "" {
			return Part{}, fmt.Errorf("%s names an empty name", key)
		}
	}
	return Part{Names: names}, nil
}

// MarshalYAML writes the block the way [Selection.UnmarshalYAML] reads it.
func (s Selection) MarshalYAML() (any, error) {
	out := map[string]any{}
	for kind, names := range s.Kinds {
		out[kind] = names
	}
	if s.Instructions {
		out["instructions"] = true
	}
	for key, part := range map[string]Part{"settings": s.Settings, "env": s.Env} {
		switch {
		case part.All:
			out[key] = true
		case len(part.Names) > 0:
			out[key] = part.Names
		}
	}
	return out, nil
}
