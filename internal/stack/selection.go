package stack

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/qory/internal/exports"
	"gopkg.in/yaml.v3"
)

// Parts are the three merged parts of a module a selection may list beside the entry
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
// Under exclude a listed thing is left out and everything else is composed. Under only a
// listed thing is composed and everything else is left out: a kind not listed contributes
// no entry, and a part not listed is left out. A name that matches nothing the module
// ships is an error either way, so a module that stops shipping something is noticed.
type Selection struct {
	// Kinds are the entry names per kind, for the kinds listed.
	Kinds map[string][]string
	// Instructions selects the module's instruction section.
	Instructions bool
	// Settings selects the settings fragments: all of them, or the listed
	// <runtime>/<file> ones.
	Settings Part
	// Env selects the exported variables: all of them, or the listed names.
	Env Part

	// raw is the block as read, parsed by [Selection.parse] from the stack's validation,
	// which names the module and the key in the error.
	raw *yaml.Node
}

// Part is how a selection takes a merged part: all of it with true, or some of it by
// name. The zero Part selects nothing.
type Part struct {
	// All is the part selected whole.
	All bool
	// Names are the fragments or variables listed, when All is not set.
	Names []string
}

// Set reports whether the part is selected at all.
func (p Part) Set() bool { return p.All || len(p.Names) > 0 }

// Empty reports whether the selection selects nothing: the block was left out.
func (s Selection) Empty() bool {
	return len(s.Kinds) == 0 && !s.Instructions && !s.Settings.Set() && !s.Env.Set()
}

// IsZero is [Selection.Empty], for the YAML encoder's omitempty.
func (s Selection) IsZero() bool { return s.Empty() }

// KindNames lists the kinds the selection contains, sorted, so a walk over them does not
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
// in it can contain the module and the key, which the decoder cannot.
func (s *Selection) UnmarshalYAML(n *yaml.Node) error {
	*s = Selection{raw: n}
	return nil
}

// parse reads the kept block: a mapping whose keys are kinds with a list of names,
// instructions with true, and settings and env with true or a list of names. An unknown
// key, a false, and an empty list are errors, each naming the key, or "the name at line
// N" for an unknown key whose name contains an access key secret or a run key. block is
// exclude or only, and the caller prefixes the module, see [exports.Prefixed]. A selection
// built in code, with no block kept, is left as it is.
func (s *Selection) parse(block string) error {
	n := s.raw
	if n == nil {
		return nil
	}
	where := "The " + block + " is in " + exports.FilePlace + "."
	if n.Kind != yaml.MappingNode {
		return exports.Refuse(fmt.Sprintf("Set the %s of %s to a mapping of kinds and parts to what they select.", block, exports.ModulePlace), "The block is not a mapping.", where)
	}
	*s = Selection{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i].Value, n.Content[i+1]
		switch {
		case isKind(key):
			var names []string
			if err := value.Decode(&names); err != nil || len(names) == 0 {
				return exports.Refuse(fmt.Sprintf("Set %s in the %s of %s to a list of entry names.", key, block, exports.ModulePlace), "The value is not a list, or the list is empty.", where)
			}
			for _, name := range names {
				if name == "" {
					return exports.Refuse(fmt.Sprintf("Remove the empty name from %s in the %s of %s.", key, block, exports.ModulePlace), "A name is not empty.", where)
				}
			}
			if s.Kinds == nil {
				s.Kinds = map[string][]string{}
			}
			s.Kinds[key] = names
		case key == "instructions":
			var all bool
			if err := value.Decode(&all); err != nil || !all {
				return exports.Refuse(fmt.Sprintf("Set instructions in the %s of %s to true.", block, exports.ModulePlace), "instructions stands for the module's AGENTS.md: exclude leaves it out, only keeps it.", where)
			}
			s.Instructions = true
		case key == "settings", key == "env":
			part, err := decodePart(key, block, value)
			if err != nil {
				return err
			}
			if key == "settings" {
				s.Settings = part
			} else {
				s.Env = part
			}
		default:
			kind := strconv.Quote(key)
			if accesskey.ContainsSecret(key) {
				// The key's line places it, and its name is left out.
				kind = fmt.Sprintf("the name at line %d", n.Content[i].Line)
			}
			return exports.Refuse(fmt.Sprintf("Rename %s in the %s of %s to a kind or a part.", kind, block, exports.ModulePlace), "Kinds: "+strings.Join(Kinds, ", ")+".", "Parts: "+strings.Join(Parts, ", ")+".", where)
		}
	}
	return nil
}

// decodePart reads true, or a non-empty list of names, for a settings or env key.
func decodePart(key, block string, n *yaml.Node) (Part, error) {
	where := "The " + block + " is in " + exports.FilePlace + "."
	var all bool
	if err := n.Decode(&all); err == nil {
		if !all {
			return Part{}, exports.Refuse(fmt.Sprintf("Set %s in the %s of %s to true or a list of names.", key, block, exports.ModulePlace), "The value is false.", where)
		}
		return Part{All: true}, nil
	}
	var names []string
	if err := n.Decode(&names); err != nil || len(names) == 0 {
		return Part{}, exports.Refuse(fmt.Sprintf("Set %s in the %s of %s to true or a list of names.", key, block, exports.ModulePlace), "The value is not true, and not a list with a name in it.", where)
	}
	for _, name := range names {
		if name == "" {
			return Part{}, exports.Refuse(fmt.Sprintf("Remove the empty name from %s in the %s of %s.", key, block, exports.ModulePlace), "A name is not empty.", where)
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
