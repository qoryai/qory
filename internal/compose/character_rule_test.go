package compose_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/primitives"
)

// ruleSchemas are the contract schemas that write the character rule of a line, and of a
// text where they have Markdown, in $defs, with the pattern inline: each is compiled with
// no loader as the schema of a custom type, so none refers to primitives.schema.json.
var ruleSchemas = []struct {
	name string
	text bool
}{
	{"questions", true},
	{"acceptance-criteria", true},
	{"verification", true},
	{"review-comments", true},
	{"proposal-sources", false},
	{"verdict", false},
	{"process", false},
}

// patternOf is the pattern of $defs name in the schema file at path, decoded.
func patternOf(t *testing.T, path, name string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]struct {
			Pattern string `json:"pattern"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Defs[name].Pattern
}

// TestContractSchemasHaveThePrimitivesPatterns: the pattern of $defs line, and of $defs
// text where a schema has it, is the string of the same definition in
// primitives.schema.json, and no other pattern of the file lists control characters.
func TestContractSchemasHaveThePrimitivesPatterns(t *testing.T) {
	const dir = "../../contracts/harness/v1/"
	line, text := patternOf(t, dir+"primitives.schema.json", "line"), patternOf(t, dir+"primitives.schema.json", "text")
	if line == "" || text == "" {
		t.Fatal("primitives.schema.json has no pattern for line or text")
	}
	for _, c := range ruleSchemas {
		path := dir + c.name + ".schema.json"
		if got := patternOf(t, path, "line"); got != line {
			t.Errorf("%s: $defs line has the pattern %q", c.name, got)
		}
		wantText := ""
		if c.text {
			wantText = text
		}
		if got := patternOf(t, path, "text"); got != wantText {
			t.Errorf("%s: $defs text has the pattern %q", c.name, got)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// The pattern is written once for a line and once for a text, in $defs.
		var lists int
		for _, l := range strings.Split(string(raw), "\n") {
			if strings.Contains(l, `"pattern"`) && strings.Contains(l, `\\x7f`) {
				lists++
			}
		}
		want := 1
		if c.text {
			want = 2
		}
		if lists != want {
			t.Errorf("%s: %d patterns list control characters, want %d: the members refer to $defs", c.name, lists, want)
		}
	}
}

// TestContractSchemasStopTheRuleCharacters: for every code point that is not a surrogate,
// a string of that code point alone is valid for $defs line of each schema, and for its
// $defs text, if and only if [primitives.StopsLine] and [primitives.StopsText] let it
// pass. A tag character and U+200B are stopped, and U+200C and U+200D pass.
func TestContractSchemasStopTheRuleCharacters(t *testing.T) {
	for _, c := range ruleSchemas {
		path := "../../contracts/harness/v1/" + c.name + ".schema.json"
		compile := func(name string) *jsonschema.Schema {
			s, err := jsonschema.NewCompiler().Compile(path + "#/$defs/" + name)
			if err != nil {
				t.Fatalf("%s %s: %v", c.name, name, err)
			}
			return s
		}
		line := compile("line")
		var text *jsonschema.Schema
		if c.text {
			text = compile("text")
		}
		for r := rune(0); r <= unicode.MaxRune; r++ {
			if r >= 0xd800 && r <= 0xdfff {
				continue
			}
			if got := line.Validate(string(r)) == nil; got == primitives.StopsLine(r) {
				t.Fatalf("%s: U+%04X: $defs line is valid: %v, StopsLine: %v", c.name, r, got, primitives.StopsLine(r))
			}
			if text != nil {
				if got := text.Validate(string(r)) == nil; got == primitives.StopsText(r) {
					t.Fatalf("%s: U+%04X: $defs text is valid: %v, StopsText: %v", c.name, r, got, primitives.StopsText(r))
				}
			}
		}
	}
}

// patternStrings are the decoded strings of every "pattern" in a decoded JSON document.
func patternStrings(doc any) []string {
	var out []string
	switch v := doc.(type) {
	case map[string]any:
		for k, m := range v {
			if p, ok := m.(string); ok && k == "pattern" {
				out = append(out, p)
				continue
			}
			out = append(out, patternStrings(m)...)
		}
	case []any:
		for _, m := range v {
			out = append(out, patternStrings(m)...)
		}
	}
	return out
}

// TestNoPatternHasARawControlCharacter: a reader fails on a schema file whose pattern
// decodes to a control character, so every pattern of the contract schemas, of the
// primitives and of the copies that Go embeds writes U+0000 to U+001F and U+007F to
// U+009F as regex escapes such as `\x00`, never as a JSON escape such as `\u0000`.
func TestNoPatternHasARawControlCharacter(t *testing.T) {
	var files []string
	for _, glob := range []string{"../../contracts/harness/v1/*.schema.json", "../../internal/*/*.schema.json"} {
		m, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) < 14 {
		t.Fatalf("found %d schema files: %v", len(files), files)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, p := range patternStrings(doc) {
			for i, r := range p {
				if r < 0x20 || 0x7f <= r && r <= 0x9f {
					t.Errorf("%s: the pattern %.30q has the control character %U at byte %d", file, p, r, i)
					break
				}
			}
		}
	}
}
