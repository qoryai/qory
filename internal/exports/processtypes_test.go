package exports_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/primitives"
)

// TestInlinePrimitivesWritesOutEachPrimitiveRef is a schema whose members refer to the
// primitives directly and through a local definition, one with a sibling keyword and one with
// an allOf: no $ref to the primitives is left, each definition is an entry of the
// object's allOf, siblings stay, and the $ref to line inside title is written out too. A
// local $ref stays, and so does a $ref to a name the primitives do not define.
func TestInlinePrimitivesWritesOutEachPrimitiveRef(t *testing.T) {
	var schema any
	doc := `{"type":"object","properties":{
		"title":{"$ref":"` + primitives.URL + `#/$defs/title","title":"Title"},
		"head":{"$ref":"#/$defs/reference","x-qory-fill":"pushed-head"},
		"note":{"$ref":"` + primitives.URL + `#/$defs/text","allOf":[{"maxLength":5}]},
		"other":{"$ref":"` + primitives.URL + `#/$defs/nothing"}},
		"$defs":{"reference":{"$ref":"` + primitives.URL + `#/$defs/reference"}}}`
	if err := json.Unmarshal([]byte(doc), &schema); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(schema)
	out, err := json.Marshal(exports.InlinePrimitives(schema))
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(schema); string(after) != string(before) {
		t.Error("InlinePrimitives changed its argument")
	}
	text := string(out)
	if strings.Count(text, primitives.URL) != 1 || !strings.Contains(text, primitives.URL+"#/$defs/nothing") {
		t.Errorf("the refs to the primitives that stay are wrong:\n%s", text)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	props := got["properties"].(map[string]any)
	title := props["title"].(map[string]any)
	if title["title"] != "Title" || len(title["allOf"].([]any)) != 1 {
		t.Errorf("the title member is %v", title)
	}
	if strings.Contains(mustJSON(t, title), `"$ref"`) {
		t.Errorf("the title's ref to line is not written out: %s", mustJSON(t, title))
	}
	note := props["note"].(map[string]any)
	if all := note["allOf"].([]any); len(all) != 2 || !reflect.DeepEqual(all[0], map[string]any{"maxLength": float64(5)}) {
		t.Errorf("the note member's allOf is %v", note["allOf"])
	}
	if head := props["head"].(map[string]any); head["$ref"] != "#/$defs/reference" || head["x-qory-fill"] != "pushed-head" {
		t.Errorf("the head member is %v", head)
	}
	// The $ref to a name the primitives do not define stays, and no loader reads it.
	if _, err := exports.CompileType(out); err == nil {
		t.Error("a schema with a $ref to the primitives compiles with no loader")
	}
	delete(props, "other")
	compiled, err := exports.CompileType([]byte(mustJSON(t, got)))
	if err != nil {
		t.Fatalf("the written-out schema does not compile: %v", err)
	}
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{`{"title":"A title","head":{"collection":"R_1","address":{"system":"code.example.com","path":"0123456789abcdef0123456789abcdef01234567"}}}`, true},
		{`{"title":"A‮title"}`, false},
		{`{"head":{"address":{"system":"code.example.com","path":"x"}}}`, false},
		{`{"note":"123456"}`, false},
	} {
		var v any
		if err := json.Unmarshal([]byte(c.doc), &v); err != nil {
			t.Fatal(err)
		}
		if err := compiled.Validate(v); (err == nil) != c.valid {
			t.Errorf("%s: valid %v, want %v: %v", c.doc, err == nil, c.valid, err)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestProcessTypesReadsEachTypeWithItsDeliverSchemaAndMarks is a process.yaml with a
// delivered structured type with marks, a type with no deliver and no schema, and an entry
// with no name: the two types are read in order, the first with its send, its schema with
// the primitives written out, and its marks; the entry with no name is left out. A folder
// with no process.yaml is not ok.
func TestProcessTypesReadsEachTypeWithItsDeliverSchemaAndMarks(t *testing.T) {
	dir := t.TempDir()
	if _, ok := exports.ProcessTypes(dir); ok {
		t.Error("a folder with no process.yaml is ok")
	}
	body := `artifact_types:
  - name: reply
    base_view: structured
    deliver: {send: end_of_run}
    json_schema:
      type: object
      required: [target]
      properties:
        target: {$ref: "` + primitives.URL + `#/$defs/reference"}
        head: {$ref: "` + primitives.URL + `#/$defs/reference", x-qory-fill: pushed-head}
        commits: {type: array, x-qory-fill: pushed-commits}
  - name: notes
    base_view: document
  - base_view: document
`
	if err := os.WriteFile(filepath.Join(dir, exports.ProcessFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	types, ok := exports.ProcessTypes(dir)
	if !ok || len(types) != 2 {
		t.Fatalf("ok %v, types %+v", ok, types)
	}
	reply, notes := types[0], types[1]
	if reply.Name != "reply" || reply.Send != "end_of_run" || strings.Contains(string(reply.Schema), primitives.URL) {
		t.Errorf("reply is %+v", reply)
	}
	if !reflect.DeepEqual(reply.Marks, map[string]string{"head": "pushed-head", "commits": "pushed-commits"}) {
		t.Errorf("the marks are %v", reply.Marks)
	}
	if notes.Name != "notes" || notes.Send != "" || notes.Schema != nil || notes.Marks != nil {
		t.Errorf("notes is %+v", notes)
	}
}

// TestFirstBrokenPlaceIsThePointerOfTheFirstRule is a document that breaks its schema at
// /title and one that breaks it at the root: the place is /title and /.
func TestFirstBrokenPlaceIsThePointerOfTheFirstRule(t *testing.T) {
	compiled, err := exports.CompileType([]byte(`{"type":"object","required":["text"],"properties":{"title":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for doc, want := range map[string]string{`{"title":1,"text":"a"}`: "/title", `{"title":"a"}`: "/"} {
		var v any
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatal(err)
		}
		if got := exports.FirstBrokenPlace(compiled.Validate(v)); got != want {
			t.Errorf("%s: the place is %s, want %s", doc, got, want)
		}
	}
}
