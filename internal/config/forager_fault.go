package config

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"
)

// What follows says what is wrong with forager.yaml without any value of it: a refusal
// names the file, the key and what is wrong, and never what the key holds, since a
// value may be a secret written in the wrong place. The decoder's own messages quote a
// value, or a Go type, so forager.yaml does not print them; [decodeError], which qory's
// other files use, is left as it is.

// fault is what is wrong at one place of forager.yaml: the line, and what follows it,
// the key and what is wrong with what it holds, which never holds the value.
type fault struct {
	line int
	text string
}

// error is the fault as a refusal of the file at path.
func (f *fault) error(path string) error {
	return fmt.Errorf("%s: line %d: %s", path, f.line, f.text)
}

// keyFault is a fault of the value at key.
func keyFault(line int, key, wrong string) *fault {
	if key == "" {
		return &fault{line, "the file is not a mapping of sections"}
	}
	return &fault{line, key + " " + wrong}
}

// foragerDecodeError is an error decoding n, which stands at key, into t, said without a
// value: an unknown key as [decodeError] says it, and any other by the first place under
// n the decoder cannot take, which [findFault] names. known says whether the decode
// refused keys t has no field for.
func foragerDecodeError(path string, n *yaml.Node, t reflect.Type, key string, known bool, err error) error {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		for _, e := range te.Errors {
			if m := unknownKey.FindStringSubmatch(e); m != nil && m[0] == e {
				return fmt.Errorf("%s: %skey %q is not one %s reads", path, m[1], m[2], ForagerFileName)
			}
		}
	}
	if f := findFault(n, t, key, known); f != nil {
		return f.error(path)
	}
	// The decoder's messages that quote a value are a TypeError's and those that quote
	// it in backquotes or quotes; any other is the decoder's own words.
	if te != nil || strings.ContainsAny(err.Error(), "`\"") {
		if key == "" {
			return fmt.Errorf("%s: a value is not of the type its key takes", path)
		}
		return fmt.Errorf("%s: %s holds a value that is not of the type its key takes", path, key)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// nodeType is a section read as written.
var nodeType = reflect.TypeFor[yaml.Node]()

// findFault is the first place under n, in the file's order, that the decoder cannot
// take into t: a value of the wrong kind, a scalar whose tag its value does not fit, and,
// when known is set, a key t has no field for. key is where n stands, as a dotted path;
// nil when there is none. A yaml.Node takes anything, and an interface every value JSON
// can represent.
func findFault(n *yaml.Node, t reflect.Type, key string, known bool) *fault {
	line := n.Line
	n = followAliases(n)
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		return findFault(n.Content[0], t, key, known)
	}
	if n.Kind == 0 || t == nodeType {
		return nil
	}
	if f := tagFault(n, line, key); f != nil {
		return f
	}
	for t.Kind() == reflect.Pointer {
		if null(n) {
			return nil
		}
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if t == nodeType || null(n) {
			return nil
		}
		if n.Kind != yaml.MappingNode {
			return keyFault(line, key, "is not a mapping")
		}
		return structFault(n, t, key, known)
	case reflect.Slice:
		if null(n) {
			return nil
		}
		if n.Kind != yaml.SequenceNode {
			if t.Elem().Kind() == reflect.String {
				return keyFault(line, key, "is not a list of strings")
			}
			return keyFault(line, key, "is not a list")
		}
		for i, c := range n.Content {
			if f := findFault(c, t.Elem(), fmt.Sprintf("%s[%d]", key, i), known); f != nil {
				return f
			}
		}
		return nil
	case reflect.Interface:
		return anyFault(n, line, key)
	}
	if err := n.Decode(reflect.New(t).Interface()); err != nil {
		switch t.Kind() {
		case reflect.String:
			return keyFault(line, key, "is not a string")
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return keyFault(line, key, "is not a whole number")
		case reflect.Bool:
			return keyFault(line, key, "is not true or false")
		}
		return keyFault(line, key, "is not of the type it takes")
	}
	return nil
}

// structFault is [findFault] of a mapping read into the struct t: each key by its
// field, a merge by the mappings it merges.
func structFault(n *yaml.Node, t reflect.Type, key string, known bool) *fault {
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := followAliases(n.Content[i]), n.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.ShortTag() == "!!merge" {
			m := followAliases(v)
			merged := []*yaml.Node{m}
			if m.Kind == yaml.SequenceNode {
				merged = m.Content
			}
			for _, each := range merged {
				if each = followAliases(each); each.Kind == yaml.MappingNode {
					if f := structFault(each, t, key, known); f != nil {
						return f
					}
				}
			}
			continue
		}
		field, ok := fieldOf(t, k.Value)
		if !ok {
			if known {
				return &fault{k.Line, fmt.Sprintf("key %q is not one %s reads", k.Value, ForagerFileName)}
			}
			continue
		}
		if f := findFault(v, field, join(key, k.Value), known); f != nil {
			return f
		}
	}
	return nil
}

// anyFault is [findFault] of a value read as any and then written as JSON: every value
// under n, a number JSON cannot represent among the faults.
func anyFault(n *yaml.Node, line int, key string) *fault {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := followAliases(n.Content[i])
			if f := tagFault(k, n.Content[i].Line, key); f != nil {
				return f
			}
			if f := findFault(n.Content[i+1], reflect.TypeFor[any](), join(key, k.Value), false); f != nil {
				return f
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if f := findFault(c, reflect.TypeFor[any](), fmt.Sprintf("%s[%d]", key, i), false); f != nil {
				return f
			}
		}
	case yaml.ScalarNode:
		var v any
		if n.Decode(&v) == nil {
			if f, ok := v.(float64); ok && (math.IsInf(f, 0) || math.IsNaN(f)) {
				return keyFault(line, key, "is not a number that JSON can represent")
			}
		}
	}
	return nil
}

// yamlTags are the tags of YAML's own that a scalar's value may fail to fit.
var yamlTags = []string{"!!str", "!!bool", "!!int", "!!float", "!!timestamp", "!!null", "!!binary"}

// tagFault is the fault of a scalar whose tag its value does not fit, such as !!int on
// a word; nil for any other node. The tag is named when it is one of YAML's own, and a
// tag of the file's own never is.
func tagFault(n *yaml.Node, line int, key string) *fault {
	if n.Kind != yaml.ScalarNode {
		return nil
	}
	var v any
	if n.Decode(&v) == nil {
		return nil
	}
	if tag := n.ShortTag(); slices.Contains(yamlTags, tag) {
		return keyFault(line, key, "is tagged "+tag+", and its value is not of that type")
	}
	return keyFault(line, key, "has a tag its value is not of")
}

// null reports whether n is null as the decoder reads it.
func null(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null"
}

// fieldOf is the type of the field of the struct t that the key name decodes into.
func fieldOf(t reflect.Type, name string) (reflect.Type, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag == "" {
			tag = strings.ToLower(f.Name)
		}
		if tag == name {
			return f.Type, true
		}
	}
	return nil, false
}

// join is key under section, as a dotted path.
func join(section, key string) string {
	if section == "" {
		return key
	}
	return section + "." + key
}

// valueFree replaces each error kind under e whose message may quote the value with one
// that does not, so the schema's report of gateway.run_credentials names the place and
// what is wrong, and never what the file holds there. A kind that quotes no value is
// kept as the schema words it.
func valueFree(e *jsonschema.ValidationError) {
	switch k := e.ErrorKind.(type) {
	case *kind.Schema, *kind.Group, *kind.Not, *kind.AllOf, *kind.AnyOf, *kind.OneOf, *kind.FalseSchema,
		*kind.RefCycle, *kind.Reference, *kind.Type, *kind.Enum, *kind.Const, *kind.MinProperties,
		*kind.MaxProperties, *kind.MinItems, *kind.MaxItems, *kind.AdditionalItems, *kind.Required,
		*kind.Dependency, *kind.DependentRequired, *kind.AdditionalProperties, *kind.PropertyNames,
		*kind.UniqueItems, *kind.Contains, *kind.MinContains, *kind.MaxContains, *kind.MinLength,
		*kind.MaxLength, *kind.ContentSchema:
	case *kind.Pattern:
		e.ErrorKind = noValue{k.KeywordPath(), "value does not match pattern " + schemaQuote(k.Want)}
	case *kind.Format:
		e.ErrorKind = noValue{k.KeywordPath(), "value is not valid " + k.Want}
	default:
		e.ErrorKind = noValue{e.ErrorKind.KeywordPath(), "value fails '" + strings.Join(e.ErrorKind.KeywordPath(), "/") + "'"}
	}
	for _, c := range e.Causes {
		valueFree(c)
	}
}

// schemaQuote is s quoted as the schema's report quotes it.
func schemaQuote(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("%q", s), `\"`, `"`), "'", `\'`)
	return "'" + s[1:len(s)-1] + "'"
}

// noValue is an error kind whose message holds no value.
type noValue struct {
	path []string
	text string
}

// KeywordPath is the schema's keyword the value fails.
func (k noValue) KeywordPath() []string { return k.path }

// LocalizedString is the message, which holds no value.
func (k noValue) LocalizedString(*message.Printer) string { return k.text }
