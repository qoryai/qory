package primitives_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/primitives"
)

// contractPath is the path of the contract's primitives schema under the repository root.
const contractPath = "contracts/harness/v1/primitives.schema.json"

// definition compiles $defs/name of the embedded schema, with no loader.
func definition(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(primitives.Schema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(primitives.URL, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(primitives.URL + "#/$defs/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSchemaIsTheContract keeps the embedded copy equal to the contract, byte for byte.
func TestSchemaIsTheContract(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", contractPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(primitives.Schema, want) {
		t.Fatalf("internal/primitives/primitives.schema.json differs from %s", contractPath)
	}
}

// TestRuleAgreesWithTheDefinitions: for every code point that is not a surrogate, a string
// of that code point alone is valid for $defs line, text and title if and only if the rule
// of the package accepts it. A change of Go's Unicode tables or of the schema that moves
// one code point fails here.
func TestRuleAgreesWithTheDefinitions(t *testing.T) {
	line, text, title := definition(t, "line"), definition(t, "text"), definition(t, "title")
	stopped := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		s := string(r)
		if primitives.StopsLine(r) {
			stopped++
		}
		for _, c := range []struct {
			name  string
			def   *jsonschema.Schema
			valid bool
		}{
			{"line", line, !primitives.StopsLine(r)},
			{"text", text, !primitives.StopsText(r)},
			{"title", title, primitives.ShowsInTitle(r)},
		} {
			if got := c.def.Validate(s) == nil; got != c.valid {
				t.Errorf("U+%04X: the schema's %s is valid: %v, the rule says %v", r, c.name, got, c.valid)
			}
		}
	}
	t.Logf("%d code points stopped in a line", stopped)
}

// TestDefinitionsOnExamples checks what the rule is for, on whole strings.
func TestDefinitionsOnExamples(t *testing.T) {
	line, text, title := definition(t, "line"), definition(t, "text"), definition(t, "title")
	family := "\U0001F468\u200d\U0001F469\u200d\U0001F467"
	for _, c := range []struct {
		name  string
		def   *jsonschema.Schema
		value string
		valid bool
	}{
		{"line", line, "Add the export " + family, true},
		{"line", line, "a\u200cb", true},
		{"line", line, "a\U000E0041b", false},
		{"line", line, "a\u200Bb", false},
		{"line", line, "a\tb", false},
		{"line", line, "a\nb", false},
		{"text", text, "# Title\n\n\tindented\r\n" + family, true},
		{"text", text, "a\U000E0041b", false},
		{"text", text, "a\u200Bb", false},
		{"text", text, "a\u2028b", false},
		{"title", title, "Add " + family, true},
		{"title", title, "", false},
		{"title", title, "  　", false},
		{"title", title, "\u200c\u200d \u200c", false},
		{"title", title, "\u200c\u200da", true},
		{"title", title, "a\U000E0041", false},
	} {
		if got := c.def.Validate(c.value) == nil; got != c.valid {
			t.Errorf("%s %q: valid is %v, want %v", c.name, c.value, got, c.valid)
		}
	}
}

// TestReferenceAndLink: a reference's address has a host and a path, and a link is https.
func TestReferenceAndLink(t *testing.T) {
	reference, link := definition(t, "reference"), definition(t, "link")
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{`{"collection": "acme/web"}`, true},
		{`{"collection": "acme/web", "address": {"system": "github.com", "path": "acme/web"}}`, true},
		{`{"collection": "acme/web", "address": {"path": "acme/web"}}`, false},
		{`{"collection": "acme/web", "address": {"system": "", "path": "acme/web"}}`, false},
		{`{"collection": "a\u200bb"}`, false},
		{`{"collection": "acme/web", "address": {"system": "github.com", "path": "a\u202eb"}}`, false},
	} {
		var v any
		if err := json.Unmarshal([]byte(c.doc), &v); err != nil {
			t.Fatal(err)
		}
		if got := reference.Validate(v) == nil; got != c.valid {
			t.Errorf("reference %s: valid is %v, want %v", c.doc, got, c.valid)
		}
	}
	for url, valid := range map[string]bool{
		"https://example.com/a?b=c#d":                      true,
		"http://example.com":                               false,
		"https://user@example.com":                         false,
		"https://example.com/a b":                          false,
		"https://example.com/" + strings.Repeat("a", 2048): false,
	} {
		if got := link.Validate(url) == nil; got != valid {
			t.Errorf("link %.40q: valid is %v, want %v", url, got, valid)
		}
	}
}

// TestAmount: an amount has a decimal value as a string, and exactly one of a currency and
// a unit.
func TestAmount(t *testing.T) {
	amount := definition(t, "amount")
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{`{"value": "12.50", "currency": "EUR"}`, true},
		{`{"value": "-3", "unit": "km"}`, true},
		{`{"value": "0.000001", "unit": "x"}`, true},
		{`{"value": "` + strings.Repeat("9", 15) + `", "currency": "USD"}`, true},
		{`{"value": "-` + strings.Repeat("9", 15) + `.999999", "currency": "USD"}`, true},
		{`{"value": "1", "unit": "` + strings.Repeat("a", 32) + `"}`, true},
		{`{"value": "1", "unit": "👨\u200d👩"}`, true},
		{`{"value": "1", "currency": "EUR", "unit": "km"}`, false},
		{`{"value": "1"}`, false},
		{`{"currency": "EUR"}`, false},
		{`{"value": "0.1e1", "currency": "EUR"}`, false},
		{`{"value": "1.", "currency": "EUR"}`, false},
		{`{"value": ".5", "currency": "EUR"}`, false},
		{`{"value": "+1", "currency": "EUR"}`, false},
		{`{"value": "", "currency": "EUR"}`, false},
		{`{"value": "` + strings.Repeat("9", 16) + `", "currency": "EUR"}`, false},
		{`{"value": "1.1234567", "currency": "EUR"}`, false},
		{`{"value": 12.5, "currency": "EUR"}`, false},
		{`{"value": 1, "unit": "km"}`, false},
		{`{"value": "1", "currency": "eur"}`, false},
		{`{"value": "1", "currency": "EURO"}`, false},
		{`{"value": "1", "currency": "EU"}`, false},
		{`{"value": "1", "unit": "k\udb40\udc41m"}`, false},
		{`{"value": "1", "unit": "k\u200bm"}`, false},
		{`{"value": "1", "unit": ""}`, false},
		{`{"value": "1", "unit": "  "}`, false},
		{`{"value": "1", "unit": "` + strings.Repeat("a", 33) + `"}`, false},
		{`{"value": "1", "unit": "km", "note": "x"}`, false},
	} {
		var v any
		if err := json.Unmarshal([]byte(c.doc), &v); err != nil {
			t.Fatal(err)
		}
		if got := amount.Validate(v) == nil; got != c.valid {
			t.Errorf("amount %.80s: valid is %v, want %v", c.doc, got, c.valid)
		}
	}
}
