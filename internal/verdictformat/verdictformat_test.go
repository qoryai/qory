package verdictformat_test

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/verdictformat"
)

// contract is the schema's file in the contract.
const contract = "../../contracts/harness/v1/verdict.schema.json"

// The points of a file that passes: one to answer and one to run again.
const (
	answerPoint   = `{"text": "The spec does not say which customers see the new price. Answer question q4.", "action": "answer", "question": "q4"}`
	runAgainPoint = `{"text": "The checks script failed on the spec's links. Run another round.", "action": "run_again"}`
)

// compiled is the contract's file, compiled.
func compiled(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schema, err := jsonschema.NewCompiler().Compile(contract)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// TestEmbeddedSchemaIsTheContract keeps the package's copy of the schema equal to the
// contract's file, byte for byte.
func TestEmbeddedSchemaIsTheContract(t *testing.T) {
	want, err := os.ReadFile(contract)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("verdict.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/verdictformat/verdict.schema.json differs from %s", contract)
	}
}

// TestPointsReadsTheFile reads a file of two points into the points of the run API, in
// their order.
func TestPointsReadsTheFile(t *testing.T) {
	points, err := verdictformat.Points([]byte("[\n " + answerPoint + ",\n " + runAgainPoint + "\n]\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []runapi.Point{
		{Text: "The spec does not say which customers see the new price. Answer question q4.", Action: runapi.ActionAnswer, Question: "q4"},
		{Text: "The checks script failed on the spec's links. Run another round.", Action: runapi.ActionRunAgain},
	}
	if !slices.Equal(points, want) {
		t.Errorf("points = %+v", points)
	}
}

// TestPointsStopsAtTheFirstBrokenRule is each file that [verdictformat.Points] stops
// at, with the error, which starts with the fix and contains the point's number from 1.
// The schema fails the verdict of each such file too, and passes the verdict of each
// file that Points reads.
func TestPointsStopsAtTheFirstBrokenRule(t *testing.T) {
	schema := compiled(t)
	two := func(second string) string { return "[" + runAgainPoint + ", " + second + "]" }
	many := func(n int) string {
		return "[" + strings.TrimSuffix(strings.Repeat(runAgainPoint+", ", n), ", ") + "]"
	}
	text := func(s string) string { return two(`{"text": "` + s + `", "action": "run_again"}`) }
	for _, c := range []struct {
		file, want string
	}{
		{many(1), ""},
		{many(20), ""},
		{text(strings.Repeat("é", 500)), ""},
		{text(`One sentence, with ' and \" and \\.`), ""},
		{text(`One\tsentence.`), "Write the text of point 2 on one line. It has a tab (U+0009)."},
		{text(`One\nsentence.`), "Write the text of point 2 on one line. It has a line break (U+000A)."},
		{text(`One\r\nsentence.`), "Write the text of point 2 on one line. It has a line break (U+000D)."},
		{two(`{"text": "a", "action": "answer", "question": "` + strings.Repeat("a", 32) + `"}`), ""},
		{`{"text": "a", "action": "run_again"}`, `Write the points as a JSON array, such as [{"text": "…", "action": "run_again"}]. The file is a JSON object.`},
		{`[` + runAgainPoint, `Write the points as one JSON array, such as [{"text": "…", "action": "run_again"}]. Reading the JSON failed: unexpected EOF`},
		{many(1) + many(1), `Write the points as one JSON array, such as [{"text": "…", "action": "run_again"}]. Reading the JSON failed: the file continues after the JSON array`},
		{many(1) + "\n\\ud800", `Write the points as one JSON array, such as [{"text": "…", "action": "run_again"}]. Reading the JSON failed: the file continues after the JSON array`},
		{`{"text": "a"} []`, `Write the points as a JSON array, such as [{"text": "…", "action": "run_again"}]. The file is a JSON object.`},
		{" \n", `Write the points as a JSON array, such as [{"text": "…", "action": "run_again"}]. The file is empty.`},
		{`[]`, "Write at least one point. The array is empty."},
		{many(21), "Write at most 20 points. The array has 21."},
		{two(`"Run another round."`), "Write each point as {text, action}: point 2 is a JSON string."},
		{two(`{"text": "The checks script failed."}`), "Write each point as {text, action}: point 2 has no action."},
		{two(`{"action": "run_again"}`), "Write each point as {text, action}: point 2 has no text."},
		{two(`{"text": "a", "action": "run_again", "gaps": "Add the empty cart."}`), `Remove "gaps" from point 2: a point has only text, action and question.`},
		{two(`{"text": 1, "action": "run_again"}`), "Write the text of point 2 as a JSON string. The text is a JSON number."},
		{text(""), "Write the text of point 2: one sentence on one line, 1 to 500 characters. The text is empty."},
		{text(strings.Repeat("a", 501)), "Shorten the text of point 2 to at most 500 characters. The text has 501."},
		{text(`One\u0007sentence.`), "Remove the character U+0007 from the text of point 2. contracts/harness/v1/verdict.schema.json lists the characters a text cannot have."},
		{text("One \u202esentence."), "Remove the character U+202E from the text of point 2. contracts/harness/v1/verdict.schema.json lists the characters a text cannot have."},
		{two(`{"text": "a", "action": "ask"}`), `Set the action of point 2 to answer or run_again. The action is "ask".`},
		{two(`{"text": "a", "action": 3}`), `Set the action of point 2 to answer or run_again. The action is 3.`},
		{two(`{"text": "a", "action": "answer"}`), "Add question, the id of an open question, to point 2: a point with the action answer has one."},
		{two(`{"text": "a", "action": "run_again", "question": "q4"}`), "Remove question from point 2: a point with the action run_again has none."},
		{two(`{"text": "a", "action": "answer", "question": "Q 4"}`), `Set the question of point 2 to a question's id, such as q1: 1 to 32 lower-case letters, digits and hyphens. The id is "Q 4".`},
		{two(`{"text": "a", "action": "answer", "question": null}`), `Set the question of point 2 to a question's id, such as q1: 1 to 32 lower-case letters, digits and hyphens. The id is null.`},
		{two(`{"text": "a", "action": "answer", "question": "` + strings.Repeat("a", 33) + `"}`), `Set the question of point 2 to a question's id, such as q1: 1 to 32 lower-case letters, digits and hyphens. The id is "` + strings.Repeat("a", 33) + `".`},
	} {
		_, err := verdictformat.Points([]byte(c.file))
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.file, err)
		case c.want != "" && (err == nil || err.Error() != c.want):
			t.Errorf("%s:\n got %v\nwant %s", c.file, err, c.want)
		}

		var points any
		if json.Unmarshal([]byte(c.file), &points) != nil {
			continue
		}
		doc := map[string]any{"verdict": "escalate", "points": points}
		if err := schema.Validate(doc); (err == nil) != (c.want == "") {
			t.Errorf("%s: the schema's result is %v, Points' is %q", c.file, err, c.want)
		}
	}
}

// TestPointsNeedsPlainUTF8 stops at a file with a byte order mark, a file that is not
// UTF-8, and a string with an escape of a lone UTF-16 surrogate, which encoding/json
// reads as U+FFFD. An escaped pair and an escaped backslash before a u read.
func TestPointsNeedsPlainUTF8(t *testing.T) {
	text := func(s string) string { return `[{"text": "` + s + `", "action": "run_again"}]` }
	for _, c := range []struct {
		file string
		want error
	}{
		{"\xef\xbb\xbf" + text("One sentence."), verdictformat.ErrByteOrderMark},
		{text("One \xff sentence."), verdictformat.ErrNotUTF8},
		{text(`One \ud800 sentence.`), verdictformat.ErrNotUTF8},
		{text(`One \udc00 sentence.`), verdictformat.ErrNotUTF8},
		{text(`One \ud800\u0041 sentence.`), verdictformat.ErrNotUTF8},
		{text(`One \ud800`), verdictformat.ErrNotUTF8},
		{text(`One \ud83d\ude00 sentence.`), nil},
		{text(`One \\ud800 sentence.`), nil},
		{text(`One \u00e9 sentence.`), nil},
	} {
		_, err := verdictformat.Points([]byte(c.file))
		if err != c.want {
			t.Errorf("%q: err = %v, want %v", c.file, err, c.want)
		}
	}
	if got := verdictformat.ErrByteOrderMark.Error(); got != "Save the file without a byte order mark." {
		t.Errorf("ErrByteOrderMark = %q", got)
	}
	if got := verdictformat.ErrNotUTF8.Error(); got != "Save the file as UTF-8." {
		t.Errorf("ErrNotUTF8 = %q", got)
	}
}

// TestCheck keeps [verdictformat.Check] and the contract's file in step: each verdict is
// passed by both or failed by both, and the error starts with the fix.
func TestCheck(t *testing.T) {
	schema := compiled(t)
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{`{"verdict": "passed"}`, true},
		{`{"verdict": "changes-requested"}`, true},
		{`{"verdict": "escalate", "points": [` + answerPoint + `]}`, true},
		{`{"verdict": "escalate"}`, false},
		{`{"verdict": "passed", "points": [` + runAgainPoint + `]}`, false},
		{`{"verdict": "passed", "gaps": "Add the empty cart."}`, false},
		{`{"verdict": "maybe"}`, false},
	} {
		err := verdictformat.Check([]byte(c.doc))
		if (err == nil) != c.valid {
			t.Errorf("%s: valid=%v err=%v", c.doc, c.valid, err)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "Fix the verdict to match contracts/harness/v1/verdict.schema.json: ") {
			t.Errorf("%s: err = %v", c.doc, err)
		}
		var doc any
		if err := json.Unmarshal([]byte(c.doc), &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("%s: the schema's result is %v", c.doc, err)
		}
	}
}

// TestExcludedIsTheSchemasCharacters keeps [verdictformat.Excluded] and the pattern of
// the schema's $defs line in step, tab, line feed and carriage return included, character by character, through U+FFFF and for a
// character past it.
func TestExcludedIsTheSchemasCharacters(t *testing.T) {
	schema := compiled(t)
	for r := rune(0); r <= 0x10000; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		doc := map[string]any{"verdict": "escalate", "points": []any{
			map[string]any{"text": "a" + string(r), "action": "run_again"},
		}}
		if passed := schema.Validate(doc) == nil; passed == verdictformat.Excluded(r) {
			t.Errorf("%U: the schema passes it: %v, Excluded: %v", r, passed, verdictformat.Excluded(r))
		}
	}
}
