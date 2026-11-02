package exports

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"gopkg.in/yaml.v3"
)

// The limits of an artifact type in a process.yaml: its name in bytes, the files of one
// version, and each base view's most bytes of all files of one version.
const (
	maxTypeName  = 64
	maxTypeFiles = 20
	mebibyte     = 1 << 20
)

// baseViewStructured is the base view of a type whose one file is a JSON document, and
// the only base view with a JSON Schema.
const baseViewStructured = "structured"

// baseViewSizes is each base view with its most bytes.
var baseViewSizes = map[string]int64{
	"document":         10 * mebibyte,
	"image":            50 * mebibyte,
	"video":            2048 * mebibyte,
	"file":             1024 * mebibyte,
	baseViewStructured: mebibyte,
}

// The limits of a custom type's JSON Schema: its bytes as compact JSON, the entries of a
// list, the members of an object, all its values, the bytes of a string or a member
// name, and the lists and objects nested in one another. A value is the schema itself,
// each entry and each member's value.
const (
	maxSchemaBytes   = 65536
	maxSchemaList    = 1024
	maxSchemaMembers = 1024
	maxSchemaValues  = 4096
	maxSchemaString  = 16384
	maxSchemaDepth   = 64
)

// The limit of the patterns of all custom types in one process.yaml, in bytes of
// [patternBudget]'s estimate. A pattern counts a fixed part and one [syntax.Inst] of 40
// bytes for each instruction of a program, a repeat counting its group once for each
// count. qory compiles no pattern, so the estimate is not memory that qory uses.
const (
	maxPatternBytes   = 16 << 20
	patternFixedBytes = 1024
	patternInstBytes  = 40
)

// typeSchemaURL is what a custom type's JSON Schema is compiled under: a name no loader
// resolves, since the schema refers to nothing outside itself.
const typeSchemaURL = "https://qory.dev/contracts/harness/v1/artifact-type"

// maxSchemaError is the most characters of the JSON Schema library's text that a message
// prints.
const maxSchemaError = 300

// typeKeys is every key an entry of artifact_types may have.
var typeKeys = []string{"name", "base_view", "size_limit", "max_files", "json_schema"}

// typeName is the form of an artifact type's name.
var typeName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// typeSchemaDrafts is what $schema may be set to in a custom type's JSON Schema, each
// with and without a # at its end.
var typeSchemaDrafts = []string{
	"https://json-schema.org/draft/2020-12/schema",
	"https://json-schema.org/draft/2020-12/schema#",
	"http://json-schema.org/draft-07/schema",
	"http://json-schema.org/draft-07/schema#",
}

// checkArtifactTypes checks artifact_types in the process.yaml shown, which may be left
// out, as [checkArtifactType] checks each entry, and that no two entries have one name.
// The entries share one [patternBudget]. A list with no entries passes. The error has
// one line for each entry it stops on, each starting with shown.
func checkArtifactTypes(shown string, list *yaml.Node) error {
	if list.Kind == 0 {
		return nil
	}
	if list.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: set artifact_types to a list of artifact types, or remove it: artifact_types is %s", shown, nodeKind(list))
	}
	var lines []string
	first := map[string]int{}
	patterns := &patternBudget{counted: map[string]bool{}}
	for i, entry := range list.Content {
		name, why := checkArtifactType(entry, i, patterns)
		at, twice := first[name]
		switch {
		case why != "":
			lines = append(lines, shown+": "+why)
		case twice:
			lines = append(lines, fmt.Sprintf("%s: rename or remove entry %d of artifact_types: entry %d has the name %s too", shown, i+1, at+1, name))
		}
		if name != "" && !twice {
			first[name] = i
		}
	}
	if len(lines) > 0 {
		return errors.New(strings.Join(lines, "\n"))
	}
	return nil
}

// checkArtifactType checks entry i of artifact_types, counted from 0. An entry is a name
// alone, or a name with base_view, size_limit and max_files, and with json_schema
// exactly when base_view is structured. It returns the entry's name, "" for none of the
// right form, and the first thing to fix in the entry, for a message; "" for an entry
// that passes. qory has no list of standard types, so a name alone passes in any name
// of the right form. patterns is the budget of the file's patterns.
func checkArtifactType(entry *yaml.Node, i int, patterns *patternBudget) (name, why string) {
	label := fmt.Sprintf("entry %d of artifact_types", i+1)
	if entry.Kind != yaml.MappingNode {
		return "", fmt.Sprintf("set %s to an object with a name: the entry is %s", label, nodeKind(entry))
	}
	seen := map[string]bool{}
	for j := 0; j+1 < len(entry.Content); j += 2 {
		key := entry.Content[j]
		if key.Kind != yaml.ScalarNode || !slices.Contains(typeKeys, key.Value) {
			return "", fmt.Sprintf("remove the key %s from %s: an artifact type has only name, base_view, size_limit, max_files and json_schema", scalarText(key), label)
		}
		if seen[key.Value] {
			return "", fmt.Sprintf("remove the second %s from %s: the entry has the key twice", key.Value, label)
		}
		seen[key.Value] = true
	}
	n := value(entry, "name")
	if n == nil {
		return "", fmt.Sprintf("add name to %s: the entry has no name", label)
	}
	if n.Kind == yaml.ScalarNode && n.Tag != "!!str" && nodeKind(n) == "a string" {
		// A date or a value with an explicit tag: unquoted, YAML reads it as no string.
		return "", fmt.Sprintf("set the name of %s to a quoted string: YAML reads %s as a type that is not a string", label, scalarText(n))
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", fmt.Sprintf("set the name of %s to a string of lowercase letters and digits in groups joined by single hyphens: the name is %s", label, nodeKind(n))
	}
	if len(n.Value) > maxTypeName || !typeName.MatchString(n.Value) {
		return "", fmt.Sprintf("set the name of %s to lowercase letters and digits in groups joined by single hyphens, at most %d bytes: the name is %s", label, maxTypeName, scalarText(n))
	}
	name = n.Value
	if len(entry.Content) == 2 {
		return name, ""
	}
	label = "artifact type " + name
	var missing []string
	for _, key := range typeKeys[1:4] {
		if !seen[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return name, fmt.Sprintf("add %s to %s, or leave only its name: a custom type has base_view, size_limit and max_files", joinWords(missing), label)
	}
	view := value(entry, "base_view")
	most, known := baseViewSizes[view.Value]
	if view.Kind != yaml.ScalarNode || view.Tag != "!!str" || !known {
		return name, fmt.Sprintf("set base_view of %s to document, image, video, file or structured: base_view is %s", label, scalarText(view))
	}
	size, files := value(entry, "size_limit"), value(entry, "max_files")
	if !wholeNumber(size, most) {
		return name, fmt.Sprintf("set size_limit of %s to a whole number from 1 to %d, the most for base_view %s: size_limit is %s", label, most, view.Value, numberText(size))
	}
	structured := view.Value == baseViewStructured
	if structured && !wholeNumber(files, 1) {
		return name, fmt.Sprintf("set max_files of %s to 1: a structured type has one file, and max_files is %s", label, numberText(files))
	}
	if !wholeNumber(files, maxTypeFiles) {
		return name, fmt.Sprintf("set max_files of %s to a whole number from 1 to %d: max_files is %s", label, maxTypeFiles, numberText(files))
	}
	schema := value(entry, "json_schema")
	switch {
	case !structured && schema != nil:
		return name, fmt.Sprintf("remove json_schema from %s, or set base_view to structured: only a structured type has a JSON Schema, and base_view is %s", label, view.Value)
	case structured && schema == nil:
		return name, fmt.Sprintf("add json_schema to %s: a structured type has a JSON Schema", label)
	case structured:
		return name, checkTypeSchema(label, schema, patterns)
	}
	return name, ""
}

// wholeNumber reports whether n is a whole number from 1 to most, written as one: 1.0
// and "1" are not.
func wholeNumber(n *yaml.Node, most int64) bool {
	var v int64
	return n.Kind == yaml.ScalarNode && n.Tag == "!!int" && n.Decode(&v) == nil && v >= 1 && v <= most
}

// numberText is a limit's value for a message: a number as written, any other value by
// its kind, and a value tagged as a number that does not decode as one as [scalarText]
// prints it, since an explicit tag lets any text have that kind.
func numberText(n *yaml.Node) string {
	if kind := nodeKind(n); kind != "a number" {
		return kind
	}
	var v any
	if n.Decode(&v) != nil {
		return scalarText(n)
	}
	return n.Value
}

// joinWords is words in a sentence: a, b and c.
func joinWords(words []string) string {
	last := len(words) - 1
	if last == 0 {
		return words[0]
	}
	return strings.Join(words[:last], ", ") + " and " + words[last]
}

// checkTypeSchema checks n, the json_schema of the artifact type label, for a message:
// it is an object, each value has a JSON form, $schema is a draft of [typeSchemaDrafts]
// or left out, it is within the limits, its patterns are within patterns, and it
// compiles with no file or network read. It returns the fix, "" for a schema that passes.
func checkTypeSchema(label string, n *yaml.Node, patterns *patternBudget) string {
	if n.Kind != yaml.MappingNode {
		return fmt.Sprintf("set json_schema of %s to an object, a JSON Schema: json_schema is %s", label, nodeKind(n))
	}
	where := "json_schema of " + label
	schema, why := jsonValue(n, where)
	if why != "" {
		return why
	}
	if draft := value(n, "$schema"); draft != nil && !slices.Contains(typeSchemaDrafts, draft.Value) {
		return fmt.Sprintf("set $schema in %s to %s or %s, or remove it: $schema is %s", where, typeSchemaDrafts[0], typeSchemaDrafts[2], scalarText(draft))
	}
	values := 0
	if why := overSchemaLimit(schema, 1, &values); why != "" {
		return fmt.Sprintf("shorten %s: %s", where, why)
	}
	var compact bytes.Buffer
	enc := json.NewEncoder(&compact)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(schema); err != nil {
		return fmt.Sprintf("fix %s: %s", where, oneLine(err.Error()))
	}
	if size := compact.Len() - 1; size > maxSchemaBytes {
		return fmt.Sprintf("shorten %s to at most %d bytes of JSON: it has %d", where, maxSchemaBytes, size)
	}
	if err := compileTypeSchema(compact.Bytes(), patterns); err != nil {
		if patterns.over > 0 {
			return fmt.Sprintf("shorten the patterns in %s: the estimate for the patterns of all artifact types in the file is at most %d bytes, and this one brings it to %d", where, maxPatternBytes, patterns.over)
		}
		return fmt.Sprintf("fix %s so that it compiles as a JSON Schema: %s", where, compileReason(err))
	}
	return ""
}

// jsonValue is n as a JSON value, with where for a message: a mapping is a
// map[string]any, a list is a []any, and a scalar with a tag of no JSON type, such as a
// date, is the string as written. It returns the fix for a value with no JSON form: a
// key that is not a string, a key given twice in one mapping, which the decoder reports
// for no value of a [yaml.Node] field, and a number that is infinite or not a number.
func jsonValue(n *yaml.Node, where string) (any, string) {
	switch n.Kind {
	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, fmt.Sprintf("quote the key %s in %s: a JSON key is a string", scalarText(key), where)
			}
			if _, twice := m[key.Value]; twice {
				return nil, fmt.Sprintf("remove the second %s from its object in %s: the object has the key twice", scalarText(key), where)
			}
			v, why := jsonValue(n.Content[i+1], where)
			if why != "" {
				return nil, why
			}
			m[key.Value] = v
		}
		return m, ""
	case yaml.SequenceNode:
		list := make([]any, len(n.Content))
		for i, entry := range n.Content {
			v, why := jsonValue(entry, where)
			if why != "" {
				return nil, why
			}
			list[i] = v
		}
		return list, ""
	}
	switch n.Tag {
	case "!!null", "!!bool", "!!int", "!!float":
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, fmt.Sprintf("replace %s in %s with a JSON value: the value does not decode as %s", scalarText(n), where, n.Tag)
		}
		if f, isFloat := v.(float64); isFloat && (math.IsInf(f, 0) || math.IsNaN(f)) {
			return nil, fmt.Sprintf("replace %s in %s with a JSON number: JSON has no such number", n.Value, where)
		}
		return v, ""
	}
	return n.Value, ""
}

// overSchemaLimit is the limit v passes, for a message, "" for none. v is at depth
// lists and objects deep, itself counted, and values counts each value read.
func overSchemaLimit(v any, depth int, values *int) string {
	*values++
	if *values > maxSchemaValues {
		return fmt.Sprintf("it has more than %d values", maxSchemaValues)
	}
	var inner []any
	switch v := v.(type) {
	case string:
		if len(v) > maxSchemaString {
			return fmt.Sprintf("it has a string of more than %d bytes", maxSchemaString)
		}
		return ""
	case []any:
		if len(v) > maxSchemaList {
			return fmt.Sprintf("it has a list of more than %d entries", maxSchemaList)
		}
		inner = v
	case map[string]any:
		if len(v) > maxSchemaMembers {
			return fmt.Sprintf("it has an object of more than %d members", maxSchemaMembers)
		}
		for name, member := range v {
			if len(name) > maxSchemaString {
				return fmt.Sprintf("it has a member name of more than %d bytes", maxSchemaString)
			}
			inner = append(inner, member)
		}
	default:
		return ""
	}
	if depth > maxSchemaDepth {
		return fmt.Sprintf("it nests lists and objects more than %d deep", maxSchemaDepth)
	}
	for _, member := range inner {
		if why := overSchemaLimit(member, depth+1, values); why != "" {
			return why
		}
	}
	return ""
}

// compileTypeSchema compiles a JSON Schema, draft 2020-12 unless $schema sets another,
// with its patterns parsed through patterns, and drops the result: nothing is validated
// against it. The compiler has no loader, so a reference to a file or a URL outside the
// schema fails when the schema reaches it, and no file or URL is read. The JSON Schema
// metaschemas are built into the compiler, so a reference to one passes.
func compileTypeSchema(schema []byte, patterns *patternBudget) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(jsonschema.SchemeURLLoader{})
	c.UseRegexpEngine(patterns.parse)
	patterns.over = 0
	if err := c.AddResource(typeSchemaURL, doc); err != nil {
		return err
	}
	_, err = c.Compile(typeSchemaURL)
	return err
}

// patternBudget is the regular expression engine of the JSON Schema compiler for one
// process.yaml. Every pattern the compiler reads goes through parse, in the check against
// the metaschema and in the schema's compile. parse compiles no pattern: this check never
// matches one, and a compiled program can take far more memory than its source.
type patternBudget struct {
	// used is the estimated bytes of the patterns in counted.
	used int64
	// over is what used would have become with the pattern that passed the limit, 0
	// when no pattern of the current schema passed it.
	over int64
	// counted has the source of each pattern that used includes.
	counted map[string]bool
}

// errPatternBudget is the compiler's error for a pattern past the limit. A message uses
// [patternBudget.over] in its place.
var errPatternBudget = errors.New("the pattern passes the limit of the file's patterns")

// parse returns source as a [parsedPattern], or the parser's error, or [errPatternBudget].
// The parser keeps a repeat as one node, so the estimate comes from its tree at the cost
// of reading the source. A source counts once.
func (b *patternBudget) parse(source string) (jsonschema.Regexp, error) {
	if b.counted[source] {
		return parsedPattern(source), nil
	}
	tree, err := syntax.Parse(source, syntax.Perl)
	if err != nil {
		return nil, err
	}
	total := b.used + patternFixedBytes + patternInstBytes*patternInsts(tree)
	if total > maxPatternBytes {
		b.over = total
		return nil, errPatternBudget
	}
	b.used = total
	b.counted[source] = true
	return parsedPattern(source), nil
}

// parsedPattern is the source of a pattern the parser read, with no program. The
// compiler keeps it in a schema that [compileTypeSchema] drops, so nothing calls
// MatchString.
type parsedPattern string

// String is the pattern's source.
func (p parsedPattern) String() string { return string(p) }

// MatchString panics: a parsedPattern has no program, and a schema with one is never
// validated against. A test that reaches it fails.
func (p parsedPattern) MatchString(string) bool {
	panic("exports: a process.yaml's json_schema was validated against, and its patterns have no program")
}

// patternInsts estimates the instructions of a program compiled from re. It adds some for
// each node, for what [syntax.Regexp.Simplify] and [syntax.Compile] add for a capture, a
// star, a plus, a quest or an alternative. It is no upper bound: a plain literal is one
// instruction under its program, and in a run of 100,201 valid random patterns 9,233
// were under, each by one. [patternFixedBytes] covers that gap. A repeat counts its group
// once for each count, and a character class one instruction for each
// [patternInstBytes] of its ranges.
func patternInsts(re *syntax.Regexp) int64 {
	var inner int64
	for _, sub := range re.Sub {
		inner += patternInsts(sub) + 1
	}
	switch re.Op {
	case syntax.OpLiteral:
		return int64(len(re.Rune)) + 1
	case syntax.OpCharClass:
		return int64(len(re.Rune))/10 + 2
	case syntax.OpRepeat:
		return int64(max(re.Min, re.Max, 1))*inner + 2
	}
	return inner + 2
}

// compileReason is err, the compiler's error, as one line of a message: each rule of the
// JSON Schema draft that the schema breaks, with the place in the schema, or the
// compiler's text with [typeSchemaURL] left out.
func compileReason(err error) string {
	var invalid *jsonschema.SchemaValidationError
	if errors.As(err, &invalid) {
		return SchemaRules(invalid.Err)
	}
	return HideValues(oneLine(strings.ReplaceAll(err.Error(), typeSchemaURL, "")))
}

// SchemaRules is err, a [jsonschema.ValidationError], as one line of a message: each
// rule the document breaks, with the place in the document, with each word that
// contains a secret hidden as [HideValues] hides it. A rule about a member's name or a
// string's content has no place, as [schemaRules] has it. The text of any other error
// is one line in the same way.
func SchemaRules(err error) string {
	var broken *jsonschema.ValidationError
	if !errors.As(err, &broken) {
		return HideValues(oneLine(err.Error()))
	}
	var rules []string
	for _, cause := range broken.Causes {
		rules = append(rules, schemaRules(cause, false)...)
	}
	return HideValues(oneLine(strings.Join(rules, "; ")))
}

// schemaRules is the rule e states and the rules under it, in the order the library
// prints them. An allOf or an anyOf only leads to the rules under it, and a reference
// with one rule under it too. The library keeps a slice that later siblings overwrite
// as the place of a [kind.PropertyNames] or a [kind.ContentSchema], and the rules under
// one have a place inside the name or the content, so with unplaced true, or under such
// a rule, a rule prints without a place.
func schemaRules(e *jsonschema.ValidationError, unplaced bool) []string {
	switch e.ErrorKind.(type) {
	case *kind.PropertyNames, *kind.ContentSchema:
		unplaced = true
	}
	var rules []string
	_, reference := e.ErrorKind.(*kind.Reference)
	if !reference || len(e.Causes) != 1 {
		one := jsonschema.ValidationError{SchemaURL: e.SchemaURL, InstanceLocation: e.InstanceLocation, ErrorKind: e.ErrorKind}
		if unplaced {
			one.InstanceLocation = nil
		}
		text := one.Error()
		if unplaced {
			text = strings.TrimPrefix(text, "at '': ")
		}
		if !strings.HasSuffix(text, "' failed") {
			rules = append(rules, text)
		}
	}
	for _, cause := range e.Causes {
		rules = append(rules, schemaRules(cause, unplaced)...)
	}
	return rules
}

// oneLine is text as one line of a message: at most [maxSchemaError] characters, with
// ... after what is cut, and each character that does not print as ?.
func oneLine(text string) string {
	var b strings.Builder
	n := 0
	for _, r := range text {
		if n == maxSchemaError {
			b.WriteString("...")
			break
		}
		if !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
