package cmd

import (
	"bytes"
	"fmt"
	"os"
	"strings"
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

// TestAHandoffDescriptionWithAStoppedCharacterDropsTheText is a description with a
// character that the contract's text rule stops: U+200B, the tag character U+E0041,
// U+2028, and a control character other than tab, line feed and carriage return. The
// text is dropped with a line that has the code point and the position, and the stored
// submission matches the format. Tab, line feed, carriage return, U+200C and an emoji
// sequence stay.
func TestAHandoffDescriptionWithAStoppedCharacterDropsTheText(t *testing.T) {
	for _, c := range []struct {
		name, description string
		want              string
	}{
		{"a zero width space", `Fix\u200bit`, fmt.Sprintf(textDroppedChar, '\u200b', 4)},
		{"a tag character", `Fix\udb40\udc41it`, fmt.Sprintf(textDroppedChar, '\U000E0041', 4)},
		{"a line separator", `Fix\u2028it`, fmt.Sprintf(textDroppedChar, '\u2028', 4)},
		{"a bell", `Fix\u0007it`, fmt.Sprintf(textDroppedChar, '\a', 4)},
		{"a bidirectional override", `Fix\u202eit`, fmt.Sprintf(textDroppedChar, '\u202e', 4)},
		{"a NUL character", `Fix\u0000it`, fmt.Sprintf(textDroppedNUL, 4)},
		{"tab, line feed and carriage return", `# Fix\n\n\tit\r\nnow`, ""},
		{"a joiner", `Fix\u200cit`, ""},
		{"an emoji sequence", `Fix \ud83d\udc68\u200d\ud83d\udc69\u200d\ud83d\udc67`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, err := readHandoff(handoffIn(t, `{"text":{"title":"T","description":"`+c.description+`"}}`))
			if err != nil || h == nil {
				t.Fatalf("%v, %v", h, err)
			}
			document := passedDocument()
			document.Text = h.text()
			if err := checkDocument(document); err != nil {
				t.Errorf("the submission is not valid: %v", err)
			}
			if c.want == "" {
				if h.text() == nil || len(h.dropped) != 0 {
					t.Errorf("the text is %v with the lines %v, want it kept", h.text(), h.dropped)
				}
				return
			}
			if h.text() != nil || len(h.dropped) != 1 || h.dropped[0] != c.want {
				t.Errorf("the text is %v with the lines %q, want it dropped with %q", h.text(), h.dropped, c.want)
			}
			if strings.ContainsAny(h.dropped[0], "\u200b\u2028\a\u202e\U000E0041") {
				t.Errorf("the line has the character itself: %q", h.dropped[0])
			}
		})
	}
}
