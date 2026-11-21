package cmd

import (
	"bytes"
	"os"
	"testing"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/submission"
)

// TestVisibleTitleIsTheContractsVisibleRule: for every code point that is not a surrogate and
// that a title may have, visibleTitle shows it if and only if $defs visible of
// submission.schema.json passes it, so a title that the program accepts is a title that
// the contract accepts.
func TestVisibleTitleIsTheContractsVisibleRule(t *testing.T) {
	raw, err := os.ReadFile("../contracts/harness/v1/submission.schema.json")
	if err != nil {
		t.Fatal(err)
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
	visible, err := c.Compile(url + "#/$defs/visible")
	if err != nil {
		t.Fatal(err)
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff || !submission.TitleRune(r) {
			continue
		}
		if got := visible.Validate(string(r)) == nil; got != visibleTitle(string(r)) {
			t.Fatalf("%U: $defs visible passes it: %v, visibleTitle: %v", r, got, visibleTitle(string(r)))
		}
		if got := visibleTitle(string(r)); got != primitives.ShowsInTitle(r) {
			t.Fatalf("%U: visibleTitle is %v, ShowsInTitle is %v", r, got, primitives.ShowsInTitle(r))
		}
	}
}
