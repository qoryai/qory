package exports

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/primitives"
)

// ProcessType is one entry of artifact_types of a composed process.yaml, as a run reads it.
type ProcessType struct {
	// Name is the type's name.
	Name string
	// Send is deliver.send, "" for a type with no deliver: a type that is not delivered.
	Send string
	// Schema is json_schema as JSON, with each $ref to the primitives inlined as
	// [InlinePrimitives] has it; nil for a type with no json_schema.
	Schema json.RawMessage
	// Marks is each top-level member of Schema that has [fillKey], with its value; nil for
	// none.
	Marks map[string]string
	// SizeLimit is size_limit, the most bytes of a version's files, 0 when the entry has none.
	SizeLimit int64
	// Unread is why json_schema has no JSON form, one line, with Schema nil; "" when it has one.
	Unread string
}

// ProcessTypes returns artifact_types of the process.yaml at the root of the module folder
// dir, read through dir opened as an [os.Root], as [Exports.Verify] reads it. ok is false when
// the folder has no such file, and when the file cannot be read or decoded or has an alias.
// The compose checked the file, as [Exports.Verify] checks it, so an entry with no name, a
// name or a send that is not a string is left out, and a json_schema with no JSON form sets
// Unread.
func ProcessTypes(dir string) (types []ProcessType, ok bool) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false
	}
	defer root.Close()
	data, err := root.ReadFile(ProcessFileName)
	if err != nil || RefuseAliases(ProcessFileName, data) != nil {
		return nil, false
	}
	var f struct {
		ArtifactTypes yaml.Node `yaml:"artifact_types"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, false
	}
	if f.ArtifactTypes.Kind != yaml.SequenceNode {
		return nil, true
	}
	for _, entry := range f.ArtifactTypes.Content {
		name := value(entry, "name")
		if name == nil || name.Kind != yaml.ScalarNode || name.Tag != "!!str" {
			continue
		}
		t := ProcessType{Name: name.Value}
		if limit := value(entry, "size_limit"); limit != nil && limit.Kind == yaml.ScalarNode && limit.Tag == "!!int" {
			t.SizeLimit, _ = strconv.ParseInt(limit.Value, 10, 64)
		}
		if send := value(value(entry, "deliver"), "send"); send != nil && send.Kind == yaml.ScalarNode && send.Tag == "!!str" {
			t.Send = send.Value
		}
		if n := value(entry, "json_schema"); n != nil {
			doc, why := jsonValue(n, "json_schema")
			inlined := InlinePrimitives(doc)
			schema, err := json.Marshal(inlined)
			switch {
			case why != "":
				// why is the fix, then the cause on line 2.
				_, cause, _ := strings.Cut(why, "\n  ")
				t.Unread = strings.TrimSuffix(cmp.Or(cause, why), ".")
			case err != nil:
				t.Unread = err.Error()
			default:
				t.Schema, t.Marks = schema, fillMarks(inlined)
			}
		}
		types = append(types, t)
	}
	return types, true
}

// fillMarks is each top-level member of schema, a decoded JSON Schema, whose [fillKey] is a
// string, with that string; nil for none.
func fillMarks(schema any) map[string]string {
	root, _ := schema.(map[string]any)
	props, _ := root["properties"].(map[string]any)
	var marks map[string]string
	for name, member := range props {
		m, _ := member.(map[string]any)
		if mark, isString := m[fillKey].(string); isString {
			if marks == nil {
				marks = map[string]string{}
			}
			marks[name] = mark
		}
	}
	return marks
}

// InlinePrimitives is node, a decoded JSON Schema, with each object whose $ref is a
// definition of the primitives schema, [primitives.URL] then "#/$defs/" and a name, written
// out: the object keeps its other keywords and gets the definition as one more entry of its
// allOf, which validates as the $ref did under draft 2020-12. The definition's own $refs into
// the primitives' $defs are written out the same way, so the result refers to nothing outside
// itself. A $ref to a name that the primitives do not define stays. node is not changed.
func InlinePrimitives(node any) any {
	return inline(node, primitives.URL+defsPrefix, nil)
}

// inline is [InlinePrimitives] for refs that start with prefix; within the primitives'
// definitions prefix is [defsPrefix], and seen has the names being written out, so a
// definition that refers to itself stays a $ref.
func inline(node any, prefix string, seen []string) any {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, member := range v {
			out[key] = inline(member, prefix, seen)
		}
		ref, _ := v["$ref"].(string)
		name, found := strings.CutPrefix(ref, prefix)
		def, defined := primitiveDefinitions()[name]
		if !found || !defined || slices.Contains(seen, name) {
			return out
		}
		delete(out, "$ref")
		all, _ := out["allOf"].([]any)
		out["allOf"] = append(slices.Clone(all), inline(roundTrip(def), defsPrefix, append(slices.Clone(seen), name)))
		return out
	case []any:
		out := make([]any, len(v))
		for i, member := range v {
			out[i] = inline(member, prefix, seen)
		}
		return out
	}
	return node
}

// CompileType compiles schema, a type's JSON Schema with its primitives inlined, for a check
// of a document. The compiler has no loader: a $ref to a file or a URL fails the compile.
func CompileType(schema json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(jsonschema.SchemeURLLoader{})
	if err := c.AddResource(typeSchemaURL, doc); err != nil {
		return nil, err
	}
	return c.Compile(typeSchemaURL)
}

// FirstBrokenPlace is the place in the document of the first rule that err, a
// [jsonschema.ValidationError], has, as a JSON Pointer such as /title; "/" for the root, and
// for an error of another kind.
func FirstBrokenPlace(err error) string {
	var broken *jsonschema.ValidationError
	if !errors.As(err, &broken) {
		return "/"
	}
	for len(broken.Causes) > 0 {
		broken = broken.Causes[0]
	}
	if len(broken.InstanceLocation) == 0 {
		return "/"
	}
	var b strings.Builder
	for _, token := range broken.InstanceLocation {
		b.WriteString("/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(token))
	}
	return b.String()
}
