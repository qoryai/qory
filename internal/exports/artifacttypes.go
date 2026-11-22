package exports

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp/syntax"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"gopkg.in/yaml.v3"

	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/runapi"
)

// The limits of an artifact type in a process.yaml: the files of one version, and each
// base view's most bytes of all files of one version. Its name is at most
// [runapi.MaxArtifactName] bytes.
const (
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
// resolves, since the schema refers to nothing outside itself but the primitives.
const typeSchemaURL = "https://qory.dev/contracts/harness/v1/artifact-type"

// maxSchemaError is the most characters of the JSON Schema library's text that a message
// prints.
const maxSchemaError = 300

// typeKeys is every key an entry of artifact_types may have.
var typeKeys = []string{"name", "base_view", "size_limit", "max_files", "json_schema", "withdrawable", "deliver"}

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
// one refusal for each entry it stops on, each written fix first with shown on its
// last line.
func checkArtifactTypes(shown string, list *yaml.Node) error {
	if list.Kind == 0 {
		return nil
	}
	if list.Kind != yaml.SequenceNode {
		return processRefusal(shown, "Set artifact_types to a list of artifact types, or remove it.", "artifact_types is "+nodeKind(list)+".")
	}
	var lines []string
	first := map[string]int{}
	patterns := &patternBudget{counted: map[string]bool{}}
	for i, entry := range list.Content {
		name, why := checkArtifactType(entry, i, patterns)
		at, twice := first[name]
		switch {
		case why != "":
			lines = append(lines, strings.ReplaceAll(why, FilePlace, shown))
		case twice:
			lines = append(lines, fmt.Sprintf("Rename or remove entry %d of artifact_types.\n  Entry %d has the name %s too.\n  The entry is in %s.", i+1, at+1, name, shown))
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

// checkArtifactType checks entry i of artifact_types, counted from 0. An entry is a
// name alone, or a name with base_view, size_limit and max_files, and with json_schema
// exactly when base_view is structured. Either also may have withdrawable, true or
// false, and a structured type may have deliver ([checkDeliver]). It returns the
// entry's name, "" for none of the right form, and the first thing to fix in the entry,
// for a message, with [FilePlace] for the file, fix first; "" for an entry that passes.
// qory has no list of standard types, so a name alone passes in any name of the right
// form. patterns is the budget of the file's patterns.
func checkArtifactType(entry *yaml.Node, i int, patterns *patternBudget) (name, why string) {
	label := fmt.Sprintf("entry %d of artifact_types", i+1)
	if entry.Kind != yaml.MappingNode {
		return "", fmt.Sprintf("Set %s to an object with a name.\n  The entry is %s.\n  The entry is in %s.", label, nodeKind(entry), FilePlace)
	}
	seen := map[string]bool{}
	for j := 0; j+1 < len(entry.Content); j += 2 {
		key := entry.Content[j]
		if key.Kind != yaml.ScalarNode || !slices.Contains(typeKeys, key.Value) {
			return "", fmt.Sprintf("Remove the key %s from %s.\n  An artifact type has only name, base_view, size_limit, max_files, json_schema, withdrawable, and deliver.\n  The entry is in %s.", scalarText(key), label, FilePlace)
		}
		if seen[key.Value] {
			return "", fmt.Sprintf("Remove the second %s from %s.\n  The entry has the key twice.\n  The entry is in %s.", key.Value, label, FilePlace)
		}
		seen[key.Value] = true
	}
	n := value(entry, "name")
	if n == nil {
		return "", fmt.Sprintf("Add name to %s.\n  The entry has no name.\n  The entry is in %s.", label, FilePlace)
	}
	if n.Kind == yaml.ScalarNode && n.Tag != "!!str" && nodeKind(n) == "a string" {
		// A date or a value with an explicit tag: unquoted, YAML reads it as no string.
		return "", fmt.Sprintf("Set the name of %s to a quoted string.\n  YAML reads %s as a value that is not a string.\n  The entry is in %s.", label, scalarText(n), FilePlace)
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", fmt.Sprintf("Set the name of %s to a string.\n  A name is lowercase letters and digits in groups joined by single hyphens, and the name is %s.\n  The entry is in %s.", label, nodeKind(n), FilePlace)
	}
	if !runapi.ArtifactName(n.Value) {
		return "", fmt.Sprintf("Rename %s.\n  A name is lowercase letters and digits in groups joined by single hyphens, at most %d bytes.\n  The name is %s.\n  The entry is in %s.", label, runapi.MaxArtifactName, scalarText(n), FilePlace)
	}
	name = n.Value
	label = "artifact type " + name
	if w := value(entry, "withdrawable"); w != nil && !isBool(w) {
		shown := scalarText(w)
		if w.Tag == "!!null" {
			shown = "null"
		}
		return name, fmt.Sprintf("Set withdrawable of %s to true or false.\n  withdrawable is %s.\n  The type is in %s.", label, shown, FilePlace)
	}
	deliver := value(entry, "deliver")
	if deliver != nil {
		if why := checkDeliver(label, deliver); why != "" {
			return name, why
		}
	}
	if !seen["base_view"] && !seen["size_limit"] && !seen["max_files"] && !seen["json_schema"] {
		if deliver != nil {
			return name, notDelivered(label, "is a standard type")
		}
		return name, ""
	}
	var missing []string
	for _, key := range typeKeys[1:4] {
		if !seen[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return name, fmt.Sprintf("Add %s to %s, or leave only its name.\n  A custom type has base_view, size_limit, and max_files.\n  The type is in %s.", joinWords(missing), label, FilePlace)
	}
	view := value(entry, "base_view")
	most, known := baseViewSizes[view.Value]
	if view.Kind != yaml.ScalarNode || view.Tag != "!!str" || !known {
		return name, fmt.Sprintf("Set base_view of %s to document, image, video, file, or structured.\n  base_view is %s.\n  The type is in %s.", label, scalarText(view), FilePlace)
	}
	size, files := value(entry, "size_limit"), value(entry, "max_files")
	if !wholeNumber(size, most) {
		return name, fmt.Sprintf("Set size_limit of %s to a whole number from 1 to %d.\n  %d is the most for base_view %s, and size_limit is %s.\n  The type is in %s.", label, most, most, view.Value, numberText(size), FilePlace)
	}
	structured := view.Value == baseViewStructured
	if structured && !wholeNumber(files, 1) {
		return name, fmt.Sprintf("Set max_files of %s to 1.\n  A structured type has one file, and max_files is %s.\n  The type is in %s.", label, numberText(files), FilePlace)
	}
	if !wholeNumber(files, maxTypeFiles) {
		return name, fmt.Sprintf("Set max_files of %s to a whole number from 1 to %d.\n  max_files is %s.\n  The type is in %s.", label, maxTypeFiles, numberText(files), FilePlace)
	}
	schema := value(entry, "json_schema")
	if deliver != nil && !structured {
		return name, notDelivered(label, "has base_view "+view.Value)
	}
	switch {
	case !structured && schema != nil:
		return name, fmt.Sprintf("Remove json_schema from %s, or set its base_view to structured.\n  Only a structured type has a JSON Schema, and base_view is %s.\n  The type is in %s.", label, view.Value, FilePlace)
	case structured && schema == nil:
		return name, fmt.Sprintf("Add json_schema to %s.\n  A structured type has a JSON Schema.\n  The type is in %s.", label, FilePlace)
	case structured:
		if why := checkTypeSchema(label, schema, patterns); why != "" {
			return name, why
		}
		if deliver == nil {
			return name, checkNoFillMarks(label, schema)
		}
		var limit int64
		if size.Decode(&limit) == nil && limit > maxDeliveredBytes {
			return name, fmt.Sprintf("Set size_limit to at most %d.\n  Apiary delivers one version of a type with deliver, of at most %d bytes, and size_limit of %s is %d.\n  The type is in %s.", maxDeliveredBytes, maxDeliveredBytes, label, limit, FilePlace)
		}
		return name, checkDeliverTarget(label, schema)
	}
	return name, ""
}

// maxDeliveredBytes is the most bytes of the size_limit of a type with deliver: the size
// of the one version that Apiary delivers.
const maxDeliveredBytes = 786432

// deliverSends is each value deliver.send may have, the values of submit.send: when
// Apiary delivers the type's versions.
var deliverSends = []string{"end_of_run", "after_gate"}

// checkDeliver checks n, the deliver of the artifact type label: an object with send, one
// of [deliverSends], and no other key. It returns the fix for a message with [FilePlace]
// for the file, "" for a deliver that passes. Whether the type may have a deliver is
// [notDelivered] and [checkDeliverTarget].
func checkDeliver(label string, n *yaml.Node) string {
	sends := strings.Join(deliverSends, " or ")
	if n.Kind != yaml.MappingNode {
		return fmt.Sprintf("Set deliver to an object with send, or remove it.\n  deliver of %s is %s.\n  The type is in %s.", label, nodeKind(n), FilePlace)
	}
	seen := map[string]bool{}
	for j := 0; j+1 < len(n.Content); j += 2 {
		key := n.Content[j]
		if key.Kind != yaml.ScalarNode || key.Value != "send" {
			return fmt.Sprintf("Remove the extra key from deliver.\n  deliver of %s has the key %s, and has only send.\n  The type is in %s.", label, scalarText(key), FilePlace)
		}
		if seen[key.Value] {
			return fmt.Sprintf("Remove the second send from deliver.\n  deliver of %s has the key twice.\n  The type is in %s.", label, FilePlace)
		}
		seen[key.Value] = true
	}
	send := value(n, "send")
	switch {
	case send == nil:
		return fmt.Sprintf("Add send to deliver, %s.\n  deliver of %s has no send.\n  The type is in %s.", sends, label, FilePlace)
	case send.Kind == yaml.ScalarNode && send.Tag != "!!str" && nodeKind(send) == "a string":
		// A value with an explicit tag: YAML reads it as no string.
		return fmt.Sprintf("Set send in deliver to a quoted string.\n  YAML reads send of %s, %s, as a value that is not a string.\n  The type is in %s.", label, scalarText(send), FilePlace)
	case send.Kind != yaml.ScalarNode || send.Tag != "!!str":
		return fmt.Sprintf("Set send in deliver to %s.\n  send of %s is %s.\n  The type is in %s.", sends, label, nodeKind(send), FilePlace)
	case !slices.Contains(deliverSends, send.Value):
		return fmt.Sprintf("Set send in deliver to %s.\n  send of %s is %s.\n  The type is in %s.", sends, label, scalarText(send), FilePlace)
	}
	return ""
}

// notDelivered is the message for a deliver on the artifact type label that is not a
// structured type with a json_schema; what says what the type is or has.
func notDelivered(label, what string) string {
	return fmt.Sprintf("Remove deliver, or make the type structured with a json_schema.\n  Only a structured type is delivered, and %s %s.\n  The type is in %s.", label, what, FilePlace)
}

// primitiveReference is the $ref that a schema writes for the definition reference of the
// primitives schema.
const primitiveReference = primitives.URL + "#/$defs/reference"

// checkDeliverTarget checks that schema, the json_schema of the artifact type label, a
// compiled JSON Schema, has a top-level property target that is the primitive reference:
// the property is a $ref to [primitiveReference], or to a definition of the schema's
// $defs that is, a written-out copy of the definition whose local definitions are those
// of the primitives, or an allOf that has one. The check reads a $ref as a path into
// $defs of the root and as the address of the primitives, so it fails a schema that
// changes where a $ref resolves, or how a draft reads it: a $schema other than draft
// 2020-12, and an $id, $anchor or $dynamicAnchor anywhere in the schema. A $ref into
// $defs counts only with a plain name ([localName]). It returns the fix for a message
// with [FilePlace] for the file, "" for a type that passes.
func checkDeliverTarget(label string, schema *yaml.Node) string {
	doc, why := jsonValue(schema, "json_schema of "+label)
	if why != "" {
		return why
	}
	root, _ := doc.(map[string]any)
	if draft, has := root["$schema"]; has && !slices.Contains(typeSchemaDrafts[:2], fmt.Sprint(draft)) {
		return typeRefusal("Set the root $schema of the json_schema to the 2020-12 draft, or remove it.", "json_schema of "+label+" has another draft, and draft-07 ignores the keys next to a $ref.")
	}
	// With the root type object, a keyword such as anyOf or not can only narrow the
	// schema, so no value but an object validates.
	if kind, _ := root["type"].(string); kind != "object" {
		return typeRefusal("Set type of the json_schema to object, or remove `deliver`.", "json_schema of "+label+" has no root type object, and a target is a member of an object.")
	}
	if hasResolutionKey(root) {
		return typeRefusal("Remove $id, $anchor and $dynamicAnchor from the json_schema, or remove `deliver`.", "json_schema of "+label+" has one. A $ref under such a key can resolve to a different schema.")
	}
	props, _ := root["properties"].(map[string]any)
	target, has := props["target"]
	cause := label + " has no top-level target in json_schema."
	if has {
		cause = "The top-level target in json_schema of " + label + " is not the primitive reference."
	}
	if has && isReferenceMember(root, target) {
		// shortcut: only the root required list counts, so a type that states it through
		// an allOf fails; only "type" is read on the target, so "enum", "const", "not",
		// "anyOf", "oneOf", bounds, and a "type" in a local definition that the $ref goes
		// through are not, and a target that nothing can satisfy passes; replace it when
		// a type needs either.
		if required, _ := root["required"].([]any); !slices.Contains(required, any("target")) {
			return typeRefusal("Add target to required. Qory Apiary cannot deliver a document without its destination.", "json_schema of "+label+" has no required with target.")
		}
		return checkFillMarks(label, root)
	}
	return fmt.Sprintf("Add a top-level `target` of the primitive `reference` to the schema, or remove `deliver`.\n  %s\n  The type is in %s.", cause, FilePlace)
}

// hasResolutionKey reports whether node, a decoded JSON Schema, has the key $id, $anchor
// or $dynamicAnchor in any object.
// shortcut: a property or a definition with one of these names fails too, and a value
// of enum or const is read as a schema; replace it when a type needs such a name.
func hasResolutionKey(node any) bool {
	return hasKey(node, false, "$id", "$anchor", "$dynamicAnchor")
}

// hasKey reports whether node, a decoded JSON Schema, has one of keys in any object. With
// skipOwn, the keys of node itself do not count, and those of the objects in it do.
// shortcut: a nested property or a definition named x-qory-fill counts as a mark and
// fails the type, as does a property of that name in a type with no deliver; replace it
// when a type needs such a name.
func hasKey(node any, skipOwn bool, keys ...string) bool {
	switch v := node.(type) {
	case map[string]any:
		for key, member := range v {
			if !skipOwn && slices.Contains(keys, key) || hasKey(member, false, keys...) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(v, func(member any) bool { return hasKey(member, false, keys...) })
	}
	return false
}

// primitiveDefinitions is $defs of the primitives schema, as JSON decoded into Go values.
var primitiveDefinitions = sync.OnceValue(func() map[string]any {
	var doc struct {
		Defs map[string]any `json:"$defs,case:strict"`
	}
	if err := json.Unmarshal(primitives.Schema, &doc); err != nil {
		panic("exports: the embedded primitives schema is no JSON: " + err.Error())
	}
	return doc.Defs
})

// defsPrefix starts a $ref to a definition of the root schema.
const defsPrefix = "#/$defs/"

// localName is the name of the definition that ref, a $ref, refers to under [defsPrefix],
// and whether ref has one. A name has letters, digits, "_" and "-" only: the compiler
// decodes "%", "~" and "/" in a JSON Pointer, so a name with one could reach another
// definition than the name spells.
// shortcut: a definition name outside letters, digits, "_" and "-" on the $ref chain of
// the target, or of a marked pushed-head, pushed-base or pushed-commits member, fails;
// replace it when a type needs such a name.
func localName(ref string) (string, bool) {
	name, local := strings.CutPrefix(ref, defsPrefix)
	if !local || name == "" {
		return "", false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "", false
		}
	}
	return name, true
}

// localDefinitionsAreThePrimitives reports whether each definition of root under
// [defsPrefix] that node refers to, directly or through another such definition, is the
// definition of the same name in the primitives schema. A $ref whose name is not plain
// ([localName]) fails.
func localDefinitionsAreThePrimitives(root map[string]any, node any, checked map[string]bool) bool {
	switch v := node.(type) {
	case map[string]any:
		if ref, _ := v["$ref"].(string); strings.HasPrefix(ref, defsPrefix) {
			name, plain := localName(ref)
			if !plain {
				return false
			}
			if !checked[name] {
				checked[name] = true
				defs, _ := root["$defs"].(map[string]any)
				if !reflect.DeepEqual(roundTrip(defs[name]), primitiveDefinitions()[name]) {
					return false
				}
				if !localDefinitionsAreThePrimitives(root, primitiveDefinitions()[name], checked) {
					return false
				}
			}
		}
		for _, member := range v {
			if !localDefinitionsAreThePrimitives(root, member, checked) {
				return false
			}
		}
	case []any:
		for _, member := range v {
			if !localDefinitionsAreThePrimitives(root, member, checked) {
				return false
			}
		}
	}
	return true
}

// roundTrip is node as JSON decodes it, so that numbers and lists compare with the
// primitives'; nil when node has no JSON form.
func roundTrip(node any) any {
	data, err := json.Marshal(node)
	if err != nil {
		return nil
	}
	var same any
	if json.Unmarshal(data, &same) != nil {
		return nil
	}
	return same
}

// isPrimitiveReference reports whether node, a member of root, a JSON Schema, is the
// primitive reference: a $ref to [primitiveReference]; a copy of its definition, written
// out as a member does under $defs reference of the schemas of the harness, with the
// definitions that it refers to in $defs of root as the primitives have them; a $ref to a
// definition under "#/$defs/" of root that is one of these; or an allOf with an entry
// that is. visited has the definitions already tried, so a loop of definitions ends and
// each definition is read once.
func isPrimitiveReference(root map[string]any, node any, visited map[string]bool) bool {
	m, _ := node.(map[string]any)
	if m == nil {
		return false
	}
	if ref, _ := m["$ref"].(string); ref == primitiveReference {
		return true
	} else if name, plain := localName(ref); plain && !visited[name] {
		visited[name] = true
		defs, _ := root["$defs"].(map[string]any)
		if isPrimitiveReference(root, defs[name], visited) {
			return true
		}
	}
	if reflect.DeepEqual(roundTrip(m), primitiveDefinitions()["reference"]) &&
		localDefinitionsAreThePrimitives(root, m, map[string]bool{}) {
		return true
	}
	all, _ := m["allOf"].([]any)
	return slices.ContainsFunc(all, func(entry any) bool { return isPrimitiveReference(root, entry, visited) })
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

// joinWords is words in a sentence: "a", "a and b", "a, b, and c".
func joinWords(words []string) string {
	switch last := len(words) - 1; last {
	case 0:
		return words[0]
	case 1:
		return words[0] + " and " + words[1]
	default:
		return strings.Join(words[:last], ", ") + ", and " + words[last]
	}
}

// checkTypeSchema checks n, the json_schema of the artifact type label, for a message:
// it is an object, each value has a JSON form, $schema is a draft of [typeSchemaDrafts]
// or left out, it is within the limits, its patterns are within patterns, and it
// compiles with no file or network read. It returns the fix, "" for a schema that passes.
func checkTypeSchema(label string, n *yaml.Node, patterns *patternBudget) string {
	if n.Kind != yaml.MappingNode {
		return typeRefusal(fmt.Sprintf("Set json_schema of %s to an object, a JSON Schema.", label), "json_schema is "+nodeKind(n)+".")
	}
	where := "json_schema of " + label
	schema, why := jsonValue(n, where)
	if why != "" {
		return why
	}
	if draft := value(n, "$schema"); draft != nil && !slices.Contains(typeSchemaDrafts, draft.Value) {
		return typeRefusal("Set $schema in "+where+" to a draft qory reads, or remove it.", fmt.Sprintf("$schema is %s, and qory reads %s and %s.", scalarText(draft), typeSchemaDrafts[0], typeSchemaDrafts[2]))
	}
	values := 0
	if why := overSchemaLimit(schema, 1, &values); why != "" {
		return typeRefusal("Shorten "+where+".", capital(why)+".")
	}
	var compact bytes.Buffer
	enc := json.NewEncoder(&compact)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(schema); err != nil {
		return typeRefusal("Fix "+where+".", capital(oneLine(err.Error()))+".")
	}
	if size := compact.Len() - 1; size > maxSchemaBytes {
		return typeRefusal(fmt.Sprintf("Shorten %s to at most %d bytes of JSON.", where, maxSchemaBytes), fmt.Sprintf("It has %d bytes.", size))
	}
	if err := compileTypeSchema(compact.Bytes(), patterns); err != nil {
		if patterns.over > 0 {
			return typeRefusal("Shorten the patterns in "+where+".", fmt.Sprintf("The estimate for the patterns of all artifact types in the file is at most %d bytes, and this one brings it to %d.", maxPatternBytes, patterns.over))
		}
		return fmt.Sprintf("Fix %s so that it compiles as a JSON Schema.\n  The schema does not compile: %s\n  The type is in %s.", where, compileReason(err), FilePlace)
	}
	return ""
}

// typeRefusal is the refusal of a custom type's schema, fix first, with [FilePlace] on the
// last line.
func typeRefusal(fix, cause string) string {
	return fix + "\n  " + cause + "\n  The type is in " + FilePlace + "."
}

// capital is s with its first letter in upper case.
func capital(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
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
				return nil, typeRefusal(fmt.Sprintf("Quote the key %s in %s.", scalarText(key), where), "A JSON key is a string.")
			}
			if _, twice := m[key.Value]; twice {
				return nil, typeRefusal(fmt.Sprintf("Remove the second %s from its object in %s.", scalarText(key), where), "The object has the key twice.")
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
			return nil, typeRefusal(fmt.Sprintf("Replace %s in %s with a JSON value.", scalarText(n), where), "The value does not decode as "+n.Tag+".")
		}
		if f, isFloat := v.(float64); isFloat && (math.IsInf(f, 0) || math.IsNaN(f)) {
			return nil, typeRefusal(fmt.Sprintf("Replace %s in %s with a JSON number.", n.Value, where), "JSON has no such number.")
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
// schema fails when the schema reaches it, and no file or URL is read. Two kinds of
// reference pass: one to a JSON Schema metaschema, which the compiler has built in, and
// one into the primitives schema, which this function adds as a resource under
// [primitives.URL], such as primitives.URL + "#/$defs/title".
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
	known, err := jsonschema.UnmarshalJSON(bytes.NewReader(primitives.Schema))
	if err != nil {
		return err
	}
	if err := c.AddResource(primitives.URL, known); err != nil {
		return err
	}
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
// prints them. A text such as 'allOf' failed only leads to the rules under it, and a
// reference with one rule under it too, so neither prints. With no rule under it, as
// for a 'not', that text is the rule, and it prints. The library keeps a slice that
// later siblings overwrite as the place of a [kind.PropertyNames] or a
// [kind.ContentSchema], and the rules under one have a place inside the name or the
// content, so with unplaced true, or under such a rule, a rule prints without a place.
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
		if len(e.Causes) == 0 || !strings.HasSuffix(text, "' failed") {
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

// NotWithdrawable returns the names of the artifact types that the process.yaml of any
// exported module sets withdrawable: false for, in the order of the modules, each once. A
// module with no process.yaml, or one with no artifact_types, adds none. A file that
// [Exports.Verify] would refuse is an error, which starts with the file and the fix.
func (e *Exports) NotWithdrawable() ([]string, error) {
	var names []string
	for _, module := range e.Modules {
		dir, _ := e.Module(module)
		data, file, err := e.readProcess(dir)
		if err != nil {
			return nil, err
		}
		if data == nil {
			continue
		}
		if err := e.checkProcess(file, data); err != nil {
			return nil, err
		}
		var process processFile
		if err := yaml.Unmarshal(data, &process); err != nil {
			return nil, DecodeError(file, err)
		}
		for _, entry := range process.ArtifactTypes.Content {
			if flag := value(entry, "withdrawable"); flag != nil && isBool(flag) && !boolValue(flag) {
				if name := value(entry, "name"); name != nil && !slices.Contains(names, name.Value) {
					names = append(names, name.Value)
				}
			}
		}
	}
	return names, nil
}

// isBool reports whether n is a YAML boolean that decodes as one: true, false, and their
// capitalised forms. A scalar with the tag !!bool and a value that is no boolean, such as
// !!bool no, is not.
func isBool(n *yaml.Node) bool {
	var b bool
	return n.Kind == yaml.ScalarNode && n.Tag == "!!bool" && n.Decode(&b) == nil
}

// boolValue is the value of n, for which [isBool] is true.
func boolValue(n *yaml.Node) bool {
	var b bool
	_ = n.Decode(&b)

	return b
}
