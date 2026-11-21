package verdictformat_test

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/verdictformat"
)

// contract is the schema's file in the contract.
const contract = "../../contracts/harness/v1/verdict.schema.json"

// The points of a file that passes: one to answer and one to run again.
const (
	answerPoint   = `{"text": "The spec does not say which customers see the new price.", "action": "answer", "question": "q4"}`
	runAgainPoint = `{"text": "The checks script failed on the spec's links.", "action": "run_again"}`
)

// fixPoint is a point to run again with a fix, which passes.
const fixPoint = `{"text": "The checks script failed on the spec's links.", "action": "run_again", "fix": "Fix the link to /docs/start in the spec."}`

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

// TestPointsReadsTheFile reads a file of three points, one with a fix, into the points of
// the run API, in their order.
func TestPointsReadsTheFile(t *testing.T) {
	points, err := verdictformat.Points([]byte("[\n " + answerPoint + ",\n " + runAgainPoint + ",\n " + fixPoint + "\n]\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []runapi.Point{
		{Text: "The spec does not say which customers see the new price.", Action: runapi.ActionAnswer, Question: "q4"},
		{Text: "The checks script failed on the spec's links.", Action: runapi.ActionRunAgain},
		{Text: "The checks script failed on the spec's links.", Action: runapi.ActionRunAgain, Fix: "Fix the link to /docs/start in the spec."},
	}
	if !slices.Equal(points, want) {
		t.Errorf("points = %+v", points)
	}
}

// TestPointsStopsAtTheFirstBrokenRule is each file that [verdictformat.Points] stops
// at, with the error, which starts with the fix; an error about one point contains its
// number. The schema fails the verdict of each such file too, and passes the verdict of
// each file that Points reads.
func TestPointsStopsAtTheFirstBrokenRule(t *testing.T) {
	schema := compiled(t)
	two := func(second string) string { return "[" + runAgainPoint + ", " + second + "]" }
	many := func(n int) string {
		return "[" + strings.TrimSuffix(strings.Repeat(runAgainPoint+", ", n), ", ") + "]"
	}
	text := func(s string) string { return two(`{"text": "` + s + `", "action": "run_again"}`) }
	fix := func(s string) string { return two(`{"text": "a", "action": "run_again", "fix": "` + s + `"}`) }
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
		{two(`"The spec's links are broken."`), "Write each point as {text, action}: point 2 is a JSON string."},
		{two(`{"text": "The checks script failed."}`), "Write each point as {text, action}: point 2 has no action."},
		{two(`{"action": "run_again"}`), "Write each point as {text, action}: point 2 has no text."},
		{two(`{"text": "a", "action": "run_again", "gaps": "Add the empty cart."}`), `Remove "gaps" from point 2: a point has only text, action, question and fix.`},
		{two(`{"text": 1, "action": "run_again"}`), "Write the text of point 2 as a JSON string. The text is a JSON number."},
		{text(""), "Write the text of point 2: one sentence on one line, 1 to 500 characters. The text is empty."},
		{text(strings.Repeat("a", 501)), "Shorten the text of point 2 to at most 500 characters. The text has 501."},
		{text(`One\u0007sentence.`), "Remove the character U+0007 from the text of point 2: the contract's $defs line excludes it."},
		{text("One \u202esentence."), "Remove the character U+202E from the text of point 2: the contract's $defs line excludes it."},
		{two(`{"text": "a", "action": "ask"}`), `Set the action of point 2 to answer or run_again. The action is "ask".`},
		{two(`{"text": "a", "action": 3}`), `Set the action of point 2 to answer or run_again. The action is 3.`},
		{two(`{"text": "a", "action": "answer"}`), "Add question, the id of an open question, to point 2: a point with the action answer has one."},
		{two(`{"text": "a", "action": "run_again", "question": "q4"}`), "Remove question from point 2: a point with the action run_again has none."},
		{two(`{"text": "a", "action": "answer", "question": "Q 4"}`), `Set the question of point 2 to a question's id, such as q1: 1 to 32 lower-case letters, digits and hyphens. The id is "Q 4".`},
		{two(`{"text": "a", "action": "answer", "question": null}`), `Set the question of point 2 to a question's id, such as q1: 1 to 32 lower-case letters, digits and hyphens. The id is null.`},
		{two(`{"text": "a", "action": "answer", "question": "` + strings.Repeat("a", 33) + `"}`), `Set the question of point 2 to a question's id, such as q1: 1 to 32 lower-case letters, digits and hyphens. The id is "` + strings.Repeat("a", 33) + `".`},
		{fix(strings.Repeat("é", 500)), ""},
		{fix(`Fix the link to /docs/start.`), ""},
		{two(`{"text": "a", "action": "answer", "question": "q4", "fix": "Answer q4."}`), "Remove fix from point 2: a point with the action answer has none."},
		{two(`{"text": "a", "action": "run_again", "fix": 1}`), "Write the fix of point 2 as a JSON string. The fix is a JSON number."},
		{two(`{"text": "a", "action": "run_again", "fix": null}`), "Write the fix of point 2 as a JSON string. The fix is a JSON null."},
		{fix(""), "Write the fix of point 2: one sentence on one line, 1 to 500 characters. The fix is empty."},
		{fix(strings.Repeat("a", 501)), "Shorten the fix of point 2 to at most 500 characters. The fix has 501."},
		{fix(`One\nsentence.`), "Write the fix of point 2 on one line. It has a line break (U+000A)."},
		{fix(`One\tsentence.`), "Write the fix of point 2 on one line. It has a tab (U+0009)."},
		{fix("One \u202esentence."), "Remove the character U+202E from the fix of point 2: the contract's $defs line excludes it."},
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
		{text(`One \ud800 sentence.`), verdictformat.ErrLoneSurrogate},
		{text(`One \udc00 sentence.`), verdictformat.ErrLoneSurrogate},
		{text(`One \ud800\u0041 sentence.`), verdictformat.ErrLoneSurrogate},
		{text(`One \ud800`), verdictformat.ErrLoneSurrogate},
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
	if got := verdictformat.ErrLoneSurrogate.Error(); got != `Remove the escape of a lone surrogate, such as \ud800, from the file.` {
		t.Errorf("ErrLoneSurrogate = %q", got)
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
		{`{"verdict": "changes-requested", "points": [` + runAgainPoint + `]}`, true},
		{`{"verdict": "changes-requested", "points": [` + answerPoint + `]}`, false},
		{`{"verdict": "changes-requested", "points": [` + fixPoint + `]}`, true},
		{`{"verdict": "escalate", "points": [` + fixPoint + `, ` + answerPoint + `]}`, true},
		{`{"verdict": "escalate", "points": [{"text": "a", "action": "answer", "question": "q4", "fix": "Answer q4."}]}`, false},
		{`{"verdict": "escalate", "points": [` + answerPoint + `]}`, true},
		{`{"verdict": "escalate"}`, false},
		{`{"verdict": "passed", "points": [` + runAgainPoint + `]}`, false},
		{`{"verdict": "passed", "gaps": "Add the empty cart."}`, false},
		{`{"verdict": "maybe"}`, false},
		{`{"verdict": "passed", "track": {"id": "small-change", "reason": "The change touches one file."}}`, true},
		{`{"verdict": "escalate", "points": [` + answerPoint + `], "track": {"id": "standard", "reason": "A person decides."}}`, true},
		{`{"verdict": "passed", "track": {"id": "small-change", "kind": "A text that the product shows", "reason": "One file."}}`, false},
		{`{"verdict": "passed", "track": {"id": "small-change"}}`, false},
		{`{"verdict": "passed", "track": {"reason": "The change touches one file."}}`, false},
		{`{"verdict": "passed", "track": {"id": "small-change", "reason": ""}}`, false},
		{`{"verdict": "passed", "track": {"id": "small-change", "reason": "One file.", "value": "short"}}`, false},
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

// TestLineRuleIsTheSchemasCharacters keeps [primitives.StopsLine] and the pattern of the
// schema's $defs line in step, tab, line feed and carriage return included, for every
// code point that is not a surrogate, the tag characters past U+FFFF too.
func TestLineRuleIsTheSchemasCharacters(t *testing.T) {
	schema := compiled(t)
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		doc := map[string]any{"verdict": "escalate", "points": []any{
			map[string]any{"text": "a" + string(r), "action": "run_again"},
		}}
		if passed := schema.Validate(doc) == nil; passed == primitives.StopsLine(r) {
			t.Errorf("%U: the schema passes it: %v, StopsLine: %v", r, passed, primitives.StopsLine(r))
		}
	}
}

// TestPointsWithATagOrAZeroWidthCharacterAreStopped is a point whose text has a tag
// character or a zero width space: the schema stops it, and a joiner in an emoji sequence
// passes.
func TestPointsWithATagOrAZeroWidthCharacterAreStopped(t *testing.T) {
	schema := compiled(t)
	family := "\U0001F468\u200d\U0001F469"
	for text, valid := range map[string]bool{
		"Fix a\U000E0041b": false, "Fix a\u200bb": false, "Fix a\u0600b": false,
		"Fix a\u200cb": true, "Fix " + family: true,
	} {
		doc := map[string]any{"verdict": "escalate", "points": []any{
			map[string]any{"text": text, "action": "run_again"},
		}}
		if got := schema.Validate(doc) == nil; got != valid {
			t.Errorf("%q: the schema's result is %v", text, got)
		}
	}
}

// TestTrackLimitsAreTheSchemasLimits keeps [verdictformat.ValidTrackID], [verdictformat.MaxReason]
// and [verdictformat.CheckTrack] in step with the schema: each id and reason is valid for
// the one when it is for the other.
func TestTrackLimitsAreTheSchemasLimits(t *testing.T) {
	schema := compiled(t)
	valid := func(track map[string]any) bool {
		return schema.Validate(map[string]any{"verdict": "passed", "track": track}) == nil
	}
	for _, id := range []string{
		"a", "small-change", "a1", "a-1", "a1-b2-c3", strings.Repeat("a", 40), strings.Repeat("a", 41),
		"", "A", "small_change", "small--change", "small-", "-small", "1small", "small change", "small-é",
	} {
		track := map[string]any{"id": id, "reason": "One file."}
		if got, want := verdictformat.ValidTrackID(id), valid(track); got != want {
			t.Errorf("id %q: ValidTrackID = %v, the schema passes it: %v", id, got, want)
		}
		err := verdictformat.CheckTrack(runapi.Track{ID: id, Reason: "One file."})
		if (err == nil) != valid(track) {
			t.Errorf("id %q: CheckTrack = %v, the schema passes it: %v", id, err, valid(track))
		}
	}
	for _, reason := range []string{
		"a", strings.Repeat("a", verdictformat.MaxReason), strings.Repeat("é", verdictformat.MaxReason),
		strings.Repeat("a", verdictformat.MaxReason+1), "", "One\nfile.", "One\tfile.", "One\u200bfile.",
	} {
		err := verdictformat.CheckTrack(runapi.Track{ID: "small-change", Reason: reason})
		if want := valid(map[string]any{"id": "small-change", "reason": reason}); (err == nil) != want {
			t.Errorf("reason %q: CheckTrack = %v, the schema passes it: %v", reason, err, want)
		}
	}
}
