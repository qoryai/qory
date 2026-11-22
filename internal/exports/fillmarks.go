package exports

import (
	"fmt"
	"maps"
	"slices"

	"github.com/qoryai/forager/accesskey"
	"gopkg.in/yaml.v3"
)

// fillKey is the JSON Schema annotation keyword that marks a top-level property of a type
// with deliver as a member that qory fills from the push, one of [fillShapes].
const fillKey = "x-qory-fill"

// fillShapes is each value of [fillKey] with the shape that the property needs, for a
// message, in the order that the message lists them.
var fillShapes = []struct{ value, needs string }{
	{"pushed-commits", "a list of primitive references"},
	{"pushed-head", "a primitive reference"},
	{"pushed-base", "a primitive reference"},
	{"pushed-branch", "a string"},
	{"pushed-base-branch", "a string"},
	{"pushed-commits-left-out", "an integer"},
}

// fillHasShape reports whether member, a property of root, a decoded JSON Schema, has the
// shape that the value of [fillKey] needs: a list is "type": "array" with items that
// [isPrimitiveReference] passes, a reference is what that function passes, and a string
// or an integer is the "type" of that name written on the property. A reference has no
// "type" beside it but "object", so that some value can match it.
// shortcut: the "type" and the "items" are read on the property, not through a $ref or an
// allOf, a "type" in a local definition that a reference goes through is not read, and a
// reference with a mark is a $ref, not a written-out copy, which the mark makes unequal
// to the primitives' definition; only "type" is read, so "enum", "const", "not" and
// bounds such as "minLength" or "maximum" are not, and a member that nothing can
// satisfy passes; replace it when a type needs any of these.
func fillHasShape(root, member map[string]any, value string) bool {
	switch value {
	case "pushed-commits":
		return member["type"] == "array" && isReferenceMember(root, member["items"])
	case "pushed-head", "pushed-base":
		return isReferenceMember(root, member)
	case "pushed-branch", "pushed-base-branch":
		return member["type"] == "string"
	}
	return member["type"] == "integer"
}

// isReferenceMember reports whether node is the primitive reference by
// [isPrimitiveReference] and has no "type" beside it but "object", on the node and on
// each entry of its allOf.
// shortcut: an allOf inside an allOf entry is not read; replace it when a type needs it.
func isReferenceMember(root map[string]any, node any) bool {
	member, _ := node.(map[string]any)
	kind, has := member["type"]
	if !isPrimitiveReference(root, node, map[string]bool{}) || (has && kind != "object") {
		return false
	}
	all, _ := member["allOf"].([]any)
	return !slices.ContainsFunc(all, func(entry any) bool {
		entryMap, _ := entry.(map[string]any)
		entryType, has := entryMap["type"]
		return has && entryType != "object"
	})
}

// checkFillMarks checks the [fillKey] of the json_schema root of the artifact type label,
// a type with deliver whose target passed: the key is on a property of the top level
// only, its value is one of [fillShapes], no value is on two properties, and the
// property has the shape of its value. It returns the fix for a message with [FilePlace]
// for the file, "" for a schema that passes.
// A nested property or a definition named x-qory-fill counts as a mark: see [hasKey].
func checkFillMarks(label string, root map[string]any) string {
	where := "json_schema of " + label
	// A key on the root, or in any value of the root but the properties, is below the top
	// level, and so is a key inside a property.
	below := false
	for key, member := range root {
		if key == "properties" {
			props, _ := member.(map[string]any)
			for _, p := range props {
				below = below || hasKey(p, true, fillKey)
			}
		} else {
			below = below || key == fillKey || hasKey(member, false, fillKey)
		}
	}
	if below {
		return typeRefusal("Move x-qory-fill to a property of the top level, or remove it.", where+" has x-qory-fill below the top level.")
	}
	props, _ := root["properties"].(map[string]any)
	used := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(props)) {
		member, _ := props[name].(map[string]any)
		raw, has := member[fillKey]
		if !has {
			continue
		}
		if name == "target" {
			return typeRefusal("Remove x-qory-fill from target. target is the destination; qory fills only other members.", "json_schema of "+label+" has x-qory-fill on target.")
		}
		shown := accesskey.ShownName(name, false)
		i := slices.IndexFunc(fillShapes, func(s struct{ value, needs string }) bool { return s.value == raw })
		if i < 0 {
			values := make([]string, len(fillShapes))
			for j, s := range fillShapes {
				values[j] = s.value
			}
			return typeRefusal("Set x-qory-fill to a value that qory fills, or remove it.", fmt.Sprintf("x-qory-fill of property %s in %s is none of %s.", shown, where, joinWords(values)))
		}
		value := fillShapes[i].value
		if first, twice := used[value]; twice {
			return typeRefusal("Remove the second x-qory-fill "+value+" from the json_schema.", fmt.Sprintf("Properties %s and %s in %s have it.", first, shown, where))
		}
		used[value] = shown
		if !fillHasShape(root, member, value) {
			return typeRefusal("Give the property the shape that its x-qory-fill needs, or remove it.", fmt.Sprintf("x-qory-fill %s on property %s in %s needs %s.", value, shown, where, fillShapes[i].needs))
		}
	}
	return ""
}

// checkNoFillMarks checks that schema, the json_schema of the artifact type label, a type
// with no deliver, has no [fillKey]: qory fills a member from a push only for a type that
// Apiary delivers.
func checkNoFillMarks(label string, schema *yaml.Node) string {
	doc, _ := jsonValue(schema, "json_schema of "+label)
	if !hasKey(doc, false, fillKey) {
		return ""
	}
	return typeRefusal("Remove x-qory-fill from the json_schema, or add `deliver`.", "json_schema of "+label+" has x-qory-fill, and qory reads it only for a type with deliver.")
}
