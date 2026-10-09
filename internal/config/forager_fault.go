package config

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
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

// error is the fault as a refusal of the file at path; a fault of no line is one of the
// file as a whole.
func (f *fault) error(path string) error {
	if f.line == 0 {
		return fmt.Errorf("%s: %s", path, f.text)
	}
	return fmt.Errorf("%s: line %d: %s", path, f.line, f.text)
}

// keyFault is a fault of the value at key.
func keyFault(line int, key, wrong string) *fault {
	if key == "" {
		return &fault{line, "the file is not a mapping of sections"}
	}
	return &fault{line, key + " " + wrong}
}

// The decoder's refusals of the file's anchors, aliases and merges, which say nothing of
// where in the file: each is said in words of qory's own, which name no anchor and no
// value.
const (
	tooManyAliases = "its aliases expand to more values than qory reads"
	anchorInItself = "an anchor's value holds an alias of that anchor"
	mergeNotMaps   = "a merge, <<, holds a value that is not a mapping or a list of mappings"
	unknownAnchor  = "an alias names an anchor that is not defined before it"
)

// aliasRefusals are the decoder's messages of the file's anchors, aliases and merges, and
// what qory says of each.
var aliasRefusals = []struct {
	decoder *regexp.Regexp
	text    string
}{
	{regexp.MustCompile(`yaml: document contains excessive aliasing`), tooManyAliases},
	{regexp.MustCompile(`yaml: anchor '.*' value contains itself`), anchorInItself},
	{regexp.MustCompile(`yaml: map merge requires map or sequence of maps as the value`), mergeNotMaps},
	{regexp.MustCompile(`yaml: unknown anchor '.*' referenced`), unknownAnchor},
}

// aliasRefusal is what qory says of err when it is the decoder's refusal of the file's
// anchors, aliases or merges.
func aliasRefusal(err error) (string, bool) {
	for _, r := range aliasRefusals {
		if r.decoder.MatchString(err.Error()) {
			return r.text, true
		}
	}
	return "", false
}

// foragerSyntaxError is err, an error reading forager.yaml as YAML at all: a refusal of
// its anchors in qory's words, which name no anchor, and any other as [decodeError]
// says it.
func foragerSyntaxError(path string, err error) error {
	if text, ok := aliasRefusal(err); ok {
		return fmt.Errorf("%s: %s", path, text)
	}
	return decodeError(path, err)
}

// tagRefusal matches the decoder's refusal of a scalar whose tag its value does not fit,
// which quotes the value; [findFault] names its place instead.
var tagRefusal = regexp.MustCompile("^yaml: (cannot decode !!\\S+ `|!!binary value contains invalid base64 data$)")

// foragerDecodeError is an error decoding n, which stands at key, into t, said without a
// value: an unknown key as [decodeError] says it; a refusal of the file's anchors,
// aliases or merges in words of qory's own, without a walk of n, which they could make
// endless; a value of the wrong type or tag by the first place under n the decoder
// cannot take, which [findFault] names; and any other as the decoder says it, unless it
// quotes a value. known says whether the decode refused keys t has no field for.
func foragerDecodeError(path string, n *yaml.Node, t reflect.Type, key string, known bool, err error) error {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		for _, e := range te.Errors {
			if m := unknownKey.FindStringSubmatch(e); m != nil && m[0] == e {
				// The decoder names a key by what it decodes into, which for a key
				// written as an alias is a value written elsewhere in the file.
				if a := aliasKeyAt(n, m[1], m[2]); a != nil {
					return fmt.Errorf("%s: %skey *%s is an alias of a key %s does not read", path, m[1], a.Value, ForagerFileName)
				}
				return fmt.Errorf("%s: %skey %q is not one %s reads", path, m[1], m[2], ForagerFileName)
			}
		}
	}
	if text, ok := aliasRefusal(err); ok {
		if key != "" {
			return fmt.Errorf("%s: %s: %s", path, key, text)
		}
		return fmt.Errorf("%s: %s", path, text)
	}
	if te != nil || tagRefusal.MatchString(err.Error()) {
		if f := findFault(n, t, key, known); f != nil && f != walkLimited {
			return f.error(path)
		}
	}
	// A fault the walk does not place, as when it reached its limit before it reached
	// the fault, is said without its place. The decoder's messages that quote a value
	// are a TypeError's and those that quote it in backquotes or quotes; any other is the
	// decoder's own words.
	if te != nil || strings.ContainsAny(err.Error(), "`\"") {
		if key == "" {
			return fmt.Errorf("%s: a value is not of the type its key takes", path)
		}
		return fmt.Errorf("%s: %s holds a value that is not of the type its key takes", path, key)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// aliasKeyAt is a key under n written as an alias of name, on the line the decoder's
// "line <n>: " prefix names, or on any line when there is none; nil when no key is.
func aliasKeyAt(n *yaml.Node, line, name string) *yaml.Node {
	stack := []*yaml.Node{n}
	for len(stack) > 0 {
		n, stack = stack[len(stack)-1], stack[:len(stack)-1]
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind == yaml.AliasNode && followAliases(k).Value == name && (line == "" || line == fmt.Sprintf("line %d: ", k.Line)) {
					return k
				}
			}
		}
		stack = append(stack, n.Content...)
	}
	return nil
}

// keyName is a key as a refusal names it: a name as it is written, and a key written as
// an alias by the alias, *anchor, since what the alias stands for is a value written
// elsewhere in the file.
func keyName(k *yaml.Node) string {
	if k.Kind == yaml.AliasNode {
		return "*" + k.Value
	}
	return k.Value
}

// nodeType is a section read as written.
var nodeType = reflect.TypeFor[yaml.Node]()

// walkLimit is how many steps a walk takes under aliases and merges. A walk that reaches
// it walks nothing more under an alias or a merge, and walks the rest of the file.
const walkLimit = 100_000

// walkLimited is what [findFault] finds when its walk reached [walkLimit] and found no
// fault in what it walked. It is no fault of the file, which the decoder may read
// whatever its aliases expand to, so no refusal says it; the decoder's own refusal of
// aliases that expand too far is [tooManyAliases], said by [aliasRefusal].
var walkLimited = &fault{0, tooManyAliases}

// walk is one walk of [findFault]. It walks each value once for each type it is read
// as, so an alias read again is not walked again, and an alias inside the value it
// names, which the decoder refuses, is a fault and not a walk without end. A merge is
// walked each time a mapping merges it, the steps under aliases and merges counted.
type walk struct {
	known bool
	// keys is a walk for [mergeKeyFault]: it finds a key that is not a name in a
	// mapping that merges, and no other fault.
	keys bool
	// state is where the walk of a value as a type stands: walking or walked.
	state map[walkKey]walkState
	// merging are the mappings being walked as a merge.
	merging map[walkKey]bool
	// aliased is how many aliases and merges deep the walk is; steps, how many steps it
	// has taken under one.
	aliased, steps int
	// limited says the walk reached walkLimit, and left what it did not walk unwalked.
	limited bool
}

// walkKey is a value as a type.
type walkKey struct {
	n *yaml.Node
	t reflect.Type
}

// walkState is where a walk of a value as a type stands.
type walkState uint8

const (
	walking walkState = iota + 1
	walked
)

// findFault is the first place under n that the decoder cannot take into t: a value of
// the wrong kind, a scalar whose tag its value does not fit, and, when known is set, a
// key t has no field for. key is where n stands, as a dotted path; nil when there is
// none, and [walkLimited] when the walk reached its limit and found none in what it
// walked. A yaml.Node takes anything, and an interface every value JSON can represent.
func findFault(n *yaml.Node, t reflect.Type, key string, known bool) *fault {
	w := &walk{known: known, state: map[walkKey]walkState{}, merging: map[walkKey]bool{}}
	if f := w.fault(n, t, key); f != nil || !w.limited {
		return f
	}
	return walkLimited
}

// mergeKeyFault is the first mapping under n, read as t as the decoder reads it, that
// merges and has a key that is not a name, which the decoder refuses, and which yaml.v3
// fails on with a panic rather than an error; nil when there is none. It is for a
// check before n is decoded.
func mergeKeyFault(n *yaml.Node, t reflect.Type, key string, known bool) *fault {
	w := &walk{known: known, keys: true, state: map[walkKey]walkState{}, merging: map[walkKey]bool{}}
	// A walk that reaches its limit checks no mapping under an alias or a merge after
	// it. The decoder may read such a file, or refuse it, or fail on such a key with
	// its panic, which [safeDecode], under which every decode of the file runs, makes a
	// refusal.
	return w.fault(n, t, key)
}

// errDecoderFailed is a panic of the YAML decoder said in words of qory's own, without
// what the panic holds, which may quote a value.
var errDecoderFailed = errors.New("the YAML decoder failed reading the file")

// safeDecode is decode, a decode of forager.yaml, with a panic of the decoder as
// [errDecoderFailed].
func safeDecode(decode func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errDecoderFailed
		}
	}()
	return decode()
}

// step counts a step under an alias or a merge, and reports whether the walk takes it:
// a walk that has taken walkLimit steps takes no more.
func (w *walk) step() bool {
	if w.aliased == 0 {
		return true
	}
	if w.steps++; w.steps > walkLimit {
		w.limited = true
		return false
	}
	return true
}

// fault is [findFault] of n, which stands at key, read as t.
func (w *walk) fault(n *yaml.Node, t reflect.Type, key string) *fault {
	line := n.Line
	if n.Kind == yaml.AliasNode {
		w.aliased++
		defer func() { w.aliased-- }()
	}
	if !w.step() {
		return nil
	}
	n = followAliases(n)
	k := walkKey{n, t}
	switch w.state[k] {
	case walking:
		if w.keys {
			return nil
		}
		return keyFault(line, key, "is an alias of a value that holds it")
	case walked:
		return nil
	}
	w.state[k] = walking
	defer func() { w.state[k] = walked }()
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		return w.fault(n.Content[0], t, key)
	}
	if n.Kind == 0 || t == nodeType {
		return nil
	}
	if f := tagFault(n, line, key); f != nil && !w.keys {
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
			if w.keys {
				return nil
			}
			return keyFault(line, key, "is not a mapping")
		}
		return w.structFault(n, t, key, nil)
	case reflect.Slice:
		if null(n) {
			return nil
		}
		if n.Kind != yaml.SequenceNode {
			if w.keys {
				return nil
			}
			if t.Elem().Kind() == reflect.String {
				return keyFault(line, key, "is not a list of strings")
			}
			return keyFault(line, key, "is not a list")
		}
		for i, c := range n.Content {
			if f := w.fault(c, t.Elem(), fmt.Sprintf("%s[%d]", key, i)); f != nil {
				return f
			}
		}
		return nil
	case reflect.Interface:
		if w.keys {
			return nil
		}
		return w.anyFault(n, line, key)
	}
	if w.keys {
		return nil
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

// structFault is [findFault] of a mapping read into the struct t, as the decoder reads
// it: a key written twice, which it refuses before it reads the mapping; then each key
// by its field; then the mappings a merge merges, after the mapping's own keys, whose
// keys the mapping writes itself are left as the decoder leaves them. merged are the
// keys the mappings that merge this one write, nil when none does.
func (w *walk) structFault(n *yaml.Node, t reflect.Type, key string, merged map[string]bool) *fault {
	if !w.step() {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		for j := i + 2; j+1 < len(n.Content); j += 2 {
			if a, b := n.Content[i], n.Content[j]; a.Kind == b.Kind && a.Value == b.Value {
				// The decoder reads no further into a mapping that writes a key twice.
				if w.keys {
					return nil
				}
				if k := followAliases(a); k.Kind != yaml.ScalarNode {
					return keyNotAName(a.Line, key)
				}
				return writtenTwice(key, keyName(a), a.Line, b.Line)
			}
		}
	}
	// A key decodes into a name, and a field the mapping writes twice under keys the
	// decoder tells apart, such as an alias of a name and the name, is refused as one
	// written twice.
	written := map[string]int{}
	var merge *yaml.Node
	// The decoder's merge holds the keys of the mapping that merges first, and a key that
	// is a list or a mapping is no key it can hold: it fails, and is refused before it
	// decodes. A key that is not a name anywhere else is refused by the decoder.
	merges := slices.ContainsFunc(n.Content, func(k *yaml.Node) bool { return k.Kind == yaml.ScalarNode && k.ShortTag() == "!!merge" })
	for i := 0; i+1 < len(n.Content); i += 2 {
		if raw := n.Content[i]; raw.Kind == yaml.ScalarNode && raw.ShortTag() == "!!merge" {
			merge = n.Content[i+1]
			continue
		}
		k, v := followAliases(n.Content[i]), n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			if w.keys && (merged != nil || !merges) {
				continue
			}
			return keyNotAName(n.Content[i].Line, key)
		}
		if merged != nil {
			if merged[k.Value] {
				continue
			}
			merged[k.Value] = true
		}
		field, ok := fieldOf(t, k.Value)
		if !ok {
			if w.known && !w.keys {
				if raw := n.Content[i]; raw.Kind == yaml.AliasNode {
					return &fault{raw.Line, fmt.Sprintf("key *%s is an alias of a key %s does not read", raw.Value, ForagerFileName)}
				}
				return &fault{k.Line, fmt.Sprintf("key %q is not one %s reads", k.Value, ForagerFileName)}
			}
			continue
		}
		if first, ok := written[k.Value]; ok {
			if w.keys {
				continue
			}
			return writtenTwice(key, k.Value, first, n.Content[i].Line)
		}
		written[k.Value] = n.Content[i].Line
		if f := w.fault(v, field, join(key, k.Value)); f != nil {
			return f
		}
	}
	if merge == nil {
		return nil
	}
	if merged == nil {
		merged = map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := followAliases(n.Content[i]); k.Kind == yaml.ScalarNode {
				merged[k.Value] = true
			}
		}
	}
	each := []*yaml.Node{merge}
	if merge.Kind == yaml.SequenceNode {
		each = merge.Content
	}
	for _, m := range each {
		if f := w.mergeFault(m, t, key, merged); f != nil {
			return f
		}
	}
	return nil
}

// writtenTwice is the fault of name, a key of the mapping at key, written at the line
// first and again at the line again.
func writtenTwice(key, name string, first, again int) *fault {
	return &fault{again, fmt.Sprintf("%s is written twice; it was first written at line %d", join(key, name), first)}
}

// keyNotAName is the fault of a key at line, of the mapping at key, that is a list or a
// mapping and not a name.
func keyNotAName(line int, key string) *fault {
	if key == "" {
		return &fault{line, "the file has a key that is not a name"}
	}
	return &fault{line, key + " has a key that is not a name"}
}

// mergeFault is [structFault] of a mapping that a mapping at key merges, as t, merged
// the keys written before it. A mapping that merges itself is a fault, as the decoder
// refuses it.
func (w *walk) mergeFault(n *yaml.Node, t reflect.Type, key string, merged map[string]bool) *fault {
	line := n.Line
	w.aliased++
	defer func() { w.aliased-- }()
	if n = followAliases(n); n.Kind != yaml.MappingNode {
		return nil
	}
	k := walkKey{n, t}
	if w.merging[k] || w.state[k] == walking {
		if w.keys {
			return nil
		}
		return keyFault(line, key, "merges a mapping that holds the merge")
	}
	w.merging[k] = true
	defer delete(w.merging, k)
	return w.structFault(n, t, key, merged)
}

// anyFault is [findFault] of a value read as any and then written as JSON: every value
// under n, a number JSON cannot represent among the faults.
func (w *walk) anyFault(n *yaml.Node, line int, key string) *fault {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := followAliases(n.Content[i])
			// Forager decodes the list into maps, which take no list or mapping as a
			// key: it refuses one, or fails on one a merge holds.
			if k.Kind != yaml.ScalarNode {
				return keyNotAName(n.Content[i].Line, key)
			}
			if f := tagFault(k, n.Content[i].Line, key); f != nil {
				return f
			}
			if f := w.fault(n.Content[i+1], reflect.TypeFor[any](), join(key, keyName(n.Content[i]))); f != nil {
				return f
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if f := w.fault(c, reflect.TypeFor[any](), fmt.Sprintf("%s[%d]", key, i)); f != nil {
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
