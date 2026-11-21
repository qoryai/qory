package submission_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/submission"
)

// patterns are the patterns of the primitives that the contract copies into its $defs.
func patterns(t *testing.T) (line, text, visible string) {
	t.Helper()
	var doc struct {
		Defs struct {
			Line  struct{ Pattern string } `json:"line"`
			Text  struct{ Pattern string } `json:"text"`
			Title struct {
				AllOf []struct{ Pattern string } `json:"allOf"`
			} `json:"title"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(primitives.Schema, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Defs.Title.AllOf) != 2 {
		t.Fatal("the title of the primitives has no allOf of two members")
	}
	return doc.Defs.Line.Pattern, doc.Defs.Text.Pattern, doc.Defs.Title.AllOf[1].Pattern
}

// TestTitleRuneIsTheLineRuleOfThePrimitives: for every code point that is not a surrogate,
// [submission.TitleRune] passes it if and only if [primitives.StopsLine] does not stop it.
func TestTitleRuneIsTheLineRuleOfThePrimitives(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		if submission.TitleRune(r) == primitives.StopsLine(r) {
			t.Fatalf("%U: TitleRune is %v, StopsLine is %v", r, submission.TitleRune(r), primitives.StopsLine(r))
		}
	}
}

// TestSchemaHasThePrimitivesRule: $defs line, text and visible of the contract have the
// patterns of the primitives, and for every code point that is not a surrogate each
// definition passes a string of that code point if and only if the rule of the primitives
// and [submission.TitleRune] do.
func TestSchemaHasThePrimitivesRule(t *testing.T) {
	raw, err := os.ReadFile(contract)
	if err != nil {
		t.Fatal(err)
	}
	var defs struct {
		Defs map[string]struct{ Pattern string } `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &defs); err != nil {
		t.Fatal(err)
	}
	line, text, visible := patterns(t)
	for name, want := range map[string]string{"line": line, "text": text, "visible": visible} {
		if got := defs.Defs[name].Pattern; got != want {
			t.Errorf("$defs %s has the pattern %q, want the primitives' %q", name, got, want)
		}
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	const url = "https://qory.dev/contracts/harness/v1/submission.schema.json"
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	compile := func(name string) *jsonschema.Schema {
		s, err := c.Compile(url + "#/$defs/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	lineDef, textDef, visibleDef := compile("line"), compile("text"), compile("visible")
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		s := string(r)
		if got := lineDef.Validate(s) == nil; got != submission.TitleRune(r) {
			t.Fatalf("%U: $defs line passes it: %v, TitleRune: %v", r, got, submission.TitleRune(r))
		}
		if got := textDef.Validate(s) == nil; got != !primitives.StopsText(r) {
			t.Fatalf("%U: $defs text passes it: %v, StopsText: %v", r, got, primitives.StopsText(r))
		}
		// A title is a line with a character that shows, as ShowsInTitle says.
		if got := lineDef.Validate(s) == nil && visibleDef.Validate(s) == nil; got != primitives.ShowsInTitle(r) {
			t.Fatalf("%U: a title of it is valid: %v, ShowsInTitle: %v", r, got, primitives.ShowsInTitle(r))
		}
	}
}

// TestCheckStopsTheRuleCharactersInTheMembers is a document with one character in the title
// of the text, the description, a step and the title of a draft: a tag character, U+200B
// and a title of only joiners and spaces stop it, and an emoji sequence, U+200C, and a
// Markdown description with tab, line feed and carriage return pass.
func TestCheckStopsTheRuleCharactersInTheMembers(t *testing.T) {
	family := "\U0001F468‍\U0001F469‍\U0001F467"
	with := func(member, value string) string {
		quoted, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		doc := strings.NewReplacer(
			`"text": {"title": "Add a page", "description": "Adds it."}`,
			`"text": {"title": `+pick(member == "text.title", string(quoted), `"Add a page"`)+`, "description": `+pick(member == "text.description", string(quoted), `"Adds it."`)+`}`,
			`"step": "deliverable:d-1"`, `"step": `+pick(member == "step", string(quoted), `"deliverable:d-1"`),
			`"title": "Add a page", "state"`, `"title": `+pick(member == "draft.title", string(quoted), `"Add a page"`)+`, "state"`,
		).Replace(valid())
		return doc
	}
	for _, c := range []struct {
		member, value string
		ok            bool
	}{
		{"text.title", "Add " + family, true},
		{"text.title", "Add‌it", true},
		{"text.title", "Add\U000E0041it", false},
		{"text.title", "Add​it", false},
		{"text.title", "‌‍  　", false},
		{"text.title", "‌‍a", true},
		{"text.title", "Add it", false},
		{"draft.title", "Add " + family, true},
		{"draft.title", "Add\U000E0041it", false},
		{"draft.title", "Add​it", false},
		{"draft.title", "‍ ‌", false},
		{"step", "deliverable:" + family, true},
		{"step", "deliverable:\U000E0041", false},
		{"step", "deliverable:​", false},
		{"step", "deliverable:\t", false},
		{"text.description", "# Page\n\n\tindented\r\n" + family, true},
		{"text.description", "Adds‌it", true},
		{"text.description", "Adds\U000E0041it", false},
		{"text.description", "Adds​it", false},
		{"text.description", "Adds it", false},
	} {
		err := submission.Check([]byte(with(c.member, c.value)))
		if (err == nil) != c.ok {
			t.Errorf("%s %q: Check returned %v", c.member, c.value, err)
		}
	}
}

// pick is a when ok, else b.
func pick(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}
