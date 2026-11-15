package module

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/exports"
)

// contractKinds are the kinds of role whose map form a manifest reads: the kinds a role
// has. Another kind is left for [stack.ReadRoles] to refuse.
var contractKinds = []string{"agents", "commands", "skills"}

// readContracts reads the map form of an agents role under roles, the manifest at path's
// roles key: {fill: one, contract: <path>}, or fill: many for a group role. It returns
// each contract's path, cleaned, keyed <kind>/<role>, nil for none, and replaces each map
// in roles with its fill, so that [stack.ReadRoles] reads the role as the short form,
// one or many, and checks the value. A map for a skills or
// commands role fails, and so does a map with a key other than fill and contract,
// without fill, or with a contract that is not a path inside the module with no ..
// part. Every error is a fix line, then a cause line that ends with path.
func readContracts(path string, roles *yaml.Node) (map[string]string, error) {
	if roles.Kind != yaml.MappingNode {
		return nil, nil
	}
	var out map[string]string
	for i := 0; i+1 < len(roles.Content); i += 2 {
		kind, byRole := roles.Content[i].Value, roles.Content[i+1]
		if !slices.Contains(contractKinds, kind) || byRole.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(byRole.Content); j += 2 {
			role, value := byRole.Content[j].Value, byRole.Content[j+1]
			if value.Kind != yaml.MappingNode {
				continue
			}
			at := "roles." + kind + "." + accesskey.ShownName(role, false)
			if kind != "agents" {
				return nil, fmt.Errorf("Set %s to one.\n  Only an agents role is a map, to give it a contract. The role is in %s.", at, path)
			}
			var fill, contract *yaml.Node
			for k := 0; k+1 < len(value.Content); k += 2 {
				switch key := value.Content[k].Value; key {
				case "fill":
					fill = value.Content[k+1]
				case "contract":
					contract = value.Content[k+1]
				default:
					return nil, fmt.Errorf("Remove key %s from %s.\n  A role's map has fill and contract. The map is in %s.", accesskey.ShownName(key, false), at, path)
				}
			}
			if fill == nil {
				return nil, fmt.Errorf("Add fill: one or fill: many to %s.\n  A role's map sets fill. The map is in %s.", at, path)
			}
			if contract != nil {
				clean, ok := contractPath(contract)
				if !ok {
					why := "The value is empty or not a path."
					if contract.Kind == yaml.ScalarNode && contract.Tag == "!!str" && contract.Value != "" {
						why = "The value is " + exports.Value(contract.Value, true) + "."
					}
					return nil, fmt.Errorf("Set %s.contract to a path inside the module.\n  %s The map is in %s.", at, why, path)
				}
				if out == nil {
					out = map[string]string{}
				}
				out[kind+"/"+role] = clean
			}
			*value = *fill
		}
	}
	return out, nil
}

// contractPath is the contract's path n, cleaned, and whether it is a string that is a
// relative path inside the module with no .. part, as the schema has it.
func contractPath(n *yaml.Node) (string, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", false
	}
	clean, ok := InsideModule(n.Value)
	if !ok || slices.Contains(strings.Split(filepath.ToSlash(n.Value), "/"), "..") {
		return "", false
	}
	return clean, true
}

// Contract is a role contract: the parts an agent needs to fill an agents role. The
// module that declares the role sets roles.agents.<role>.contract to the file's path,
// and the compose checks every agent that fills the role against it.
type Contract struct {
	// Sections are the level-2 headings the agent's body has, each the heading's text
	// without the ##; nil for none.
	Sections []string
	// FrontMatter are the front-matter keys the agent sets to a value; nil for none.
	FrontMatter []string
	// Tools are the tools the agent's tools key lists; nil for none.
	Tools []string
	// Tier requires the agent's model to be a tier, such as ${qory:fast}.
	Tier bool
	// Report is the form of the agent's report, which the harness's checks read; the
	// compose checks only that it is a map. Nil when the file has no report key.
	Report map[string]any
}

// ContractError is a role contract that does not read: its fix, then its cause on the
// next line. [ContractError.In] puts the contract's place into the fix.
type ContractError struct {
	fix   func(where string) string
	cause string
}

func (e *ContractError) Error() string { return e.In("the role contract") }

// In is the error's text with where, such as "roles/developer.yaml of module core", as
// the contract's place.
func (e *ContractError) In(where string) string {
	return e.fix(where) + "\n  " + e.cause
}

// contractError is a [ContractError] whose fix is format with the contract's place at
// {where}.
func contractError(format, cause string) *ContractError {
	return &ContractError{fix: func(where string) string { return strings.Replace(format, "{where}", where, 1) }, cause: cause}
}

// contractKeys are the keys a role contract has, in the order the errors list them.
var contractKeys = []string{"sections", "front_matter", "tools", "tier", "report"}

// ParseContract reads data, a role contract file, see [Contract]. Every key is optional,
// so {} checks nothing. The errors are [*ContractError]s: a file that is not a map, a key
// the contract does not have, a list key that is not a list of non-empty strings on one
// line each with no item twice, a section that starts with #, a tier that is not a
// boolean and a report that is not a map.
func ParseContract(data []byte) (Contract, error) {
	var doc yaml.Node
	err := yaml.Unmarshal(data, &doc)
	if errors.Is(err, io.EOF) || err == nil && len(doc.Content) == 0 {
		return Contract{}, contractError("Write {where} as a role contract, a map.", "The file is empty.")
	}
	if err != nil {
		return Contract{}, contractError("Write {where} as a role contract, a map.", exports.HideValueError(err, "").Error())
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return Contract{}, contractError("Write {where} as a role contract, a map.", "The file is "+nodeKind(root)+".")
	}
	c := Contract{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, root.Content[i+1]
		if !slices.Contains(contractKeys, key) {
			return Contract{}, contractError("Remove key "+accesskey.ShownName(key, false)+" from {where}.",
				"A role contract has sections, front_matter, tools, tier and report, and "+accesskey.ShownName(key, false)+" is none of them.")
		}
		if seen[key] {
			return Contract{}, contractError("Remove one of the two keys "+key+" from {where}.", "A key is written once.")
		}
		seen[key] = true
		switch key {
		case "sections", "front_matter", "tools":
			items, err := contractList(key, value)
			if err != nil {
				return Contract{}, err
			}
			switch key {
			case "sections":
				c.Sections = items
			case "front_matter":
				c.FrontMatter = items
			default:
				c.Tools = items
			}
		case "tier":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return Contract{}, contractError("Set tier in {where} to true or false.", "tier is "+nodeValue(value)+".")
			}
			c.Tier = value.Value == "true"
		case "report":
			if value.Kind != yaml.MappingNode {
				return Contract{}, contractError("Write report in {where} as a map.", "report is "+nodeKind(value)+".")
			}
			if err := value.Decode(&c.Report); err != nil {
				return Contract{}, contractError("Write report in {where} as a map.", exports.HideValueError(err, "").Error())
			}
			if c.Report == nil {
				c.Report = map[string]any{}
			}
		}
	}
	return c, nil
}

// contractList reads value, the list under key of a role contract.
func contractList(key string, value *yaml.Node) ([]string, error) {
	fix := "Write " + key + " in {where} as a list of names."
	if value.Kind != yaml.SequenceNode {
		return nil, contractError(fix, key+" is "+nodeKind(value)+".")
	}
	var out []string
	for i, item := range value.Content {
		n := i + 1
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return nil, contractError(fix, fmt.Sprintf("Item %d of %s is %s.", n, key, nodeKind(item)))
		}
		switch {
		case item.Value == "":
			return nil, contractError(fix, fmt.Sprintf("Item %d of %s is empty.", n, key))
		case strings.ContainsAny(item.Value, "\n\r"):
			return nil, contractError(fix, fmt.Sprintf("Item %d of %s has a line break.", n, key))
		case key == "sections" && strings.HasPrefix(item.Value, "#"):
			return nil, contractError(fix, fmt.Sprintf("Item %d of sections is %s, and a section is the heading's text without ##.", n, exports.Value(item.Value, true)))
		}
		if j := slices.Index(out, item.Value); j >= 0 {
			return nil, contractError(fmt.Sprintf("Remove item %d from %s in {where}.", n, key), fmt.Sprintf("Item %d repeats item %d, %s.", n, j+1, exports.Value(item.Value, true)))
		}
		out = append(out, item.Value)
	}
	return out, nil
}

// nodeKind is what n is, for an error: "a string", "a number", "a list".
func nodeKind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a map"
	case yaml.AliasNode:
		return "an alias"
	}
	switch n.Tag {
	case "!!str":
		return "a string"
	case "!!int", "!!float":
		return "a number"
	case "!!bool":
		return "a boolean"
	case "!!null":
		return "empty"
	}
	return "not a name"
}

// nodeValue is n for an error: a scalar's value quoted, else what n is.
func nodeValue(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode && n.Tag != "!!null" {
		return exports.Value(n.Value, true)
	}
	return nodeKind(n)
}

// Headings are the level-2 ATX headings of body, each the heading's text with a closing
// sequence of # and the spaces around it removed, in order. A heading inside a fenced
// code block, of ``` or ~~~, does not count.
func Headings(body string) []string {
	var out []string
	fence := ""
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) > 3 {
			continue
		}
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]+" \t") == "" {
				fence = ""
			}
			continue
		}
		if marker := fenceMarker(trimmed); marker != "" {
			fence = marker
			continue
		}
		text, ok := strings.CutPrefix(trimmed, "##")
		if !ok || text != "" && text[0] != ' ' && text[0] != '\t' {
			continue
		}
		text = strings.TrimSpace(text)
		if closed := strings.TrimRight(text, "#"); closed == "" {
			text = ""
		} else if closed != text && (strings.HasSuffix(closed, " ") || strings.HasSuffix(closed, "\t")) {
			text = strings.TrimSpace(closed)
		}
		out = append(out, text)
	}
	return out
}

// fenceMarker is the run of ``` or ~~~ that opens a fenced code block at the start of
// line, "" when line opens none.
func fenceMarker(line string) string {
	for _, c := range []string{"`", "~"} {
		n := len(line) - len(strings.TrimLeft(line, c))
		if n >= 3 {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

// ToolsOf are the tools of an agent's front matter: its tools key as a comma-separated
// string or as a list of strings, each trimmed, the empty ones left out; nil for a key
// that is absent or neither.
func ToolsOf(front map[string]any) []string {
	var out []string
	switch v := front["tools"].(type) {
	case string:
		for t := range strings.SplitSeq(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				out = append(out, t)
			}
		}
	case []any:
		for _, item := range v {
			if t, ok := item.(string); ok {
				if t = strings.TrimSpace(t); t != "" {
					out = append(out, t)
				}
			}
		}
	}
	return out
}
