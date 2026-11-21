package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestAHandoffDescriptionWithAStoppedCharacterDropsTheText is a description with a character
// that the contract's text rule stops: U+200B, the tag character U+E0041, U+2028, and a control
// character other than tab, line feed and carriage return. The text is dropped with a line that
// has the code point and the position, and the stored submission matches the format. Tab, line
// feed, carriage return, U+200C and an emoji sequence stay.
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

// TestADraftTitleLineWithAStoppedCharacterIsLeftOut is a submit program whose draft_title line
// has U+200B, a tag character, a control-free title of only spaces and joiners, or a good
// title. The unusable line is left out and listed with its number and cause, the draft is
// program-failed as for any draft line that qory leaves out, and the stored submission
// matches the format; a good title is stored.
func TestADraftTitleLineWithAStoppedCharacterIsLeftOut(t *testing.T) {
	program := func(title string) *submitProgram {
		return &submitProgram{command: []string{programScript(t, `printf 'draft_number 7\n'
printf 'draft_url https://git.example.com/acme/shop/pull/7\n'
printf 'draft_title `+title+`\n'
printf 'draft_state draft\n'
printf 'draft_action opened\n'
`)}}
	}
	for _, c := range []struct {
		name, title, line string
	}{
		{"a zero width space", `a\342\200\213b`, fmt.Sprintf("  Line 3: "+reasonTitleChar+".", '\u200b', 2)},
		{"a tag character", `a\363\240\201\201b`, fmt.Sprintf("  Line 3: "+reasonTitleChar+".", '\U000E0041', 2)},
		{"a line separator", `a\342\200\250b`, fmt.Sprintf("  Line 3: "+reasonTitleChar+".", '\u2028', 2)},
		{"only joiners and spaces", `\342\200\214\342\200\215 \302\240`, "  Line 3: " + reasonTitleBlank + "."},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, printed := runProgram(t, program(c.title), passedDocument())
			if string(got.Repositories[0].Draft) != `{"none":"program-failed","exit":0}` {
				t.Errorf("the draft is %s", got.Repositories[0].Draft)
			}
			for _, want := range []string{
				"qory run: The submit program printed a line that qory cannot use, so qory left it out.",
				c.line,
				"  Line 1: the draft needs five lines, and draft_title is missing; the line is left out.",
			} {
				if !strings.Contains(printed, want) {
					t.Errorf("the step did not print %q:\n%s", want, printed)
				}
			}
		})
	}
	for _, title := range []string{`Add a page`, `Add \360\237\221\250\342\200\215\360\237\221\251`, `a\342\200\214b`} {
		got, printed := runProgram(t, program(title), passedDocument())
		if !strings.HasPrefix(string(got.Repositories[0].Draft), `{"number":7,`) || strings.Contains(printed, "cannot use") {
			t.Errorf("title %s: the draft is %s, the step printed %q", title, got.Repositories[0].Draft, printed)
		}
	}
}

// TestEveryUnusableDraftTitleLineIsLeftOut is a submit program that prints several
// draft_title lines. The contract's reader uses the first title that passes its checks,
// so qory leaves out each line that its rule stops, also after a line that the reader
// rejects for its length, and a good title after a bad one is stored.
func TestEveryUnusableDraftTitleLineIsLeftOut(t *testing.T) {
	const (
		zeroWidth = `a\342\200\213b`
		tagged    = `c\363\240\201\201d`
		joiners   = `\342\200\214`
		long      = `'; i=0; while [ $i -lt 300 ]; do printf a; i=$((i+1)); done; printf '`
	)
	program := func(titles ...string) *submitProgram {
		body := "printf 'draft_number 7\\ndraft_url https://git.example.com/acme/shop/pull/7\\n'\n"
		for _, title := range titles {
			body += "printf 'draft_title " + title + "\\n'\n"
		}
		body += "printf 'draft_state draft\\ndraft_action opened\\n'\n"
		return &submitProgram{command: []string{programScript(t, body)}}
	}
	for _, c := range []struct {
		name   string
		titles []string
		stored string // the title stored, empty for a draft of program-failed
		left   []string
	}{
		{"two bad lines", []string{zeroWidth, tagged}, "", []string{"  Line 3:", "  Line 4:"}},
		{"blank, then a tag", []string{joiners, tagged}, "", []string{"  Line 3:", "  Line 4:"}},
		{"300 characters, then a tag", []string{long, tagged}, "", []string{"  Line 4:"}},
		{"bad, then good", []string{zeroWidth, `Add a page`}, "Add a page", []string{"  Line 3:"}},
		{"good, then bad", []string{`Add a page`, tagged}, "Add a page", []string{"  Line 4:"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, printed := runProgram(t, program(c.titles...), passedDocument())
			draft := string(got.Repositories[0].Draft)
			if c.stored == "" && draft != `{"none":"program-failed","exit":0}` {
				t.Errorf("the draft is %s", draft)
			}
			if c.stored != "" && !strings.Contains(draft, `"title":"`+c.stored+`"`) {
				t.Errorf("the draft is %s, want the title %q", draft, c.stored)
			}
			if strings.Contains(draft, "\U000E0041") {
				t.Errorf("the draft has the tag character: %s", draft)
			}
			for _, want := range c.left {
				if !strings.Contains(printed, want) {
					t.Errorf("the step did not print %q:\n%s", want, printed)
				}
			}
		})
	}
}

// TestAnUnusableTitleIsNotListedWhenTheReaderIgnoresTheOutput is a program that prints a
// title with a tag character and then ends in a way that the reader ignores the output:
// an exit status above 2, a signal (-1) or a timeout. The step lists no line as one that
// qory cannot use. The program creates a marker file after it prints the title, and the
// test fails without the marker, so a program that the limit stopped before it printed
// cannot pass for one whose output the reader ignores.
func TestAnUnusableTitleIsNotListedWhenTheReaderIgnoresTheOutput(t *testing.T) {
	const title = `printf 'draft_title c\363\240\201\201d\n'` + "\n"
	for _, c := range []struct {
		name, end, draft string
		limit            time.Duration
	}{
		{"exit 9", "exit 9\n", `{"none":"program-failed","exit":1}`, 0},
		{"exit 3", "exit 3\n", `{"none":"login-needed","exit":3}`, 0},
		{"a signal", "kill -9 $$\n", `{"none":"program-failed","exit":1}`, 0},
		{"a timeout", "exec sleep 30\n", `{"none":"program-timeout"}`, 5 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "printed")
			p := &submitProgram{command: []string{programScript(t, title+"touch '"+marker+"'\n"+c.end)}, limit: c.limit}
			got, printed := runProgram(t, p, passedDocument())
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("the program did not print the title before it ended: %v", err)
			}
			if d := string(got.Repositories[0].Draft); d != c.draft {
				t.Errorf("the draft is %s, want %s", d, c.draft)
			}
			if strings.Contains(printed, "cannot use") {
				t.Errorf("the step lists a line that the reader never read:\n%s", printed)
			}
		})
	}
}
