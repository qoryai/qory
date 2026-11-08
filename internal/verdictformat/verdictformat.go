// Package verdictformat is version 1 of the verdict format: a run's verdict, and for
// escalate the points that a person acts on. The contract is
// contracts/harness/v1/verdict.schema.json.
package verdictformat

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/runapi"
)

// The limits of the format.
const (
	// MaxPoints is the most points of an escalation.
	MaxPoints = 20
	// MaxText is the most characters of a point's text.
	MaxText = 500
)

// schemaURL is the schema's $id, which it is compiled under.
const schemaURL = "https://qory.dev/contracts/harness/v1/verdict.schema.json"

// contractPath is the contract's schema, as a message prints it.
const contractPath = "contracts/harness/v1/verdict.schema.json"

// verdictSchema is a copy of contracts/harness/v1/verdict.schema.json, byte for byte,
// which a test checks: go:embed does not reach a file above the package.
//
//go:embed verdict.schema.json
var verdictSchema []byte

// contract is the schema, compiled once.
var contract = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(verdictSchema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(jsonschema.SchemeURLLoader{})
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, err
	}
	return c.Compile(schemaURL)
})

// questionID is the id form of a question in contracts/harness/v1/questions.schema.json.
var questionID = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// pointMembers are the members a point has.
var pointMembers = []string{"text", "action", "question"}

// Check checks document, one verdict document, against the schema. The error starts
// with the fix, and then has each rule the document breaks, with its place in the
// document.
func Check(document []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return fmt.Errorf("Write the verdict as one JSON object, the format of %s: %s", contractPath, exports.HideValues(err.Error()))
	}
	schema, err := contract()
	if err != nil {
		return err
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("Fix the verdict to match %s: %s", contractPath, exports.SchemaRules(err))
	}
	return nil
}

// Points reads data, a JSON array of the points of an escalation, and checks it against
// the schema. The error starts with the fix and contains the number, from 1, of the
// first point that breaks a rule, such as "Write each point as {text, action}: point 2
// has no action." Points does not check that a question is open.
func Points(data []byte) ([]runapi.Point, error) {
	items, err := pointsArray(data)
	if err != nil {
		return nil, err
	}
	points := make([]runapi.Point, 0, len(items))
	for i, item := range items {
		point, err := readPoint(i+1, item)
		if err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	body, err := runapi.VerdictBody(runapi.VerdictEscalate, points)
	if err != nil {
		return nil, err
	}
	if err := Check(body); err != nil {
		return nil, err
	}
	return points, nil
}

// pointsArray reads data, a JSON array of 1 to [MaxPoints] values.
func pointsArray(data []byte) ([]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New(`Write the points as a JSON array, such as [{"text": "…", "action": "run_again"}]. The file is empty.`)
	}
	var doc any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	err := decoder.Decode(&doc)
	if err == nil {
		if _, next := decoder.Token(); !errors.Is(next, io.EOF) {
			err = errors.New("the file has more than one JSON value")
		}
	}
	if err != nil {
		return nil, fmt.Errorf(`Write the points as one JSON array, such as [{"text": "…", "action": "run_again"}]. Reading the JSON failed: %s`,
			exports.HideValues(err.Error()))
	}
	array, ok := doc.([]any)
	if !ok {
		return nil, fmt.Errorf(`Write the points as a JSON array, such as [{"text": "…", "action": "run_again"}]. The file is a JSON %s.`, kind(doc))
	}
	if len(array) == 0 {
		return nil, errors.New("Write at least one point. The array is empty.")
	}
	if len(array) > MaxPoints {
		return nil, fmt.Errorf("Write at most %d points. The array has %d.", MaxPoints, len(array))
	}
	return array, nil
}

// readPoint checks item, point number n, and returns it.
func readPoint(n int, item any) (runapi.Point, error) {
	members, ok := item.(map[string]any)
	if !ok {
		return runapi.Point{}, fmt.Errorf("Write each point as {text, action}: point %d is a JSON %s.", n, kind(item))
	}
	for _, name := range []string{"text", "action"} {
		if _, ok := members[name]; !ok {
			return runapi.Point{}, fmt.Errorf("Write each point as {text, action}: point %d has no %s.", n, name)
		}
	}
	var extra []string
	for name := range members {
		if !slices.Contains(pointMembers, name) {
			extra = append(extra, name)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return runapi.Point{}, fmt.Errorf("Remove %s from point %d: a point has only text, action and question.", shown(extra[0]), n)
	}

	text, ok := members["text"].(string)
	if !ok {
		return runapi.Point{}, fmt.Errorf("Write the text of point %d as a JSON string. The text is a JSON %s.", n, kind(members["text"]))
	}
	if err := checkText(n, text); err != nil {
		return runapi.Point{}, err
	}

	action, ok := members["action"].(string)
	if !ok || (action != runapi.ActionAnswer && action != runapi.ActionRunAgain) {
		return runapi.Point{}, fmt.Errorf("Set the action of point %d to answer or run_again. The action is %s.", n, shown(members["action"]))
	}

	value, hasQuestion := members["question"]
	switch {
	case action == runapi.ActionAnswer && !hasQuestion:
		return runapi.Point{}, fmt.Errorf("Add question, the id of an open question, to point %d: a point with the action answer has one.", n)
	case action == runapi.ActionRunAgain && hasQuestion:
		return runapi.Point{}, fmt.Errorf("Remove question from point %d: a point with the action run_again has none.", n)
	}
	question, _ := value.(string)
	if hasQuestion && !questionID.MatchString(question) {
		return runapi.Point{}, fmt.Errorf("Set the question of point %d to a question's id, such as q1: 1 to 32 lower-case letters, digits and hyphens. The id is %s.",
			n, shown(value))
	}

	return runapi.Point{Text: text, Action: action, Question: question}, nil
}

// checkText checks text, the text of point number n: 1 to [MaxText] characters, and none
// that the schema's $defs text excludes.
func checkText(n int, text string) error {
	length := utf8.RuneCountInString(text)
	if length == 0 {
		return fmt.Errorf("Write the text of point %d: one sentence of 1 to %d characters. The text is empty.", n, MaxText)
	}
	if length > MaxText {
		return fmt.Errorf("Shorten the text of point %d to at most %d characters. The text has %d.", n, MaxText, length)
	}
	for _, r := range text {
		if Excluded(r) {
			return fmt.Errorf("Remove the character %U from the text of point %d. %s lists the characters a text cannot have.", r, n, contractPath)
		}
	}
	return nil
}

// Excluded reports whether the schema's $defs text excludes r: a control character other
// than tab, line feed and carriage return, a bidirectional control, a separator U+2028 or
// U+2029, or one of the format characters it lists.
func Excluded(r rune) bool {
	switch {
	case r <= 0x08, r == 0x0b, r == 0x0c, r >= 0x0e && r <= 0x1f, r >= 0x7f && r <= 0x9f:
		return true
	case r == 0xad, r == 0x61c, r == 0x180e, r == 0x200b, r == 0x200e, r == 0x200f:
		return true
	case r == 0x2028, r == 0x2029, r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069:
		return true
	case r == 0xfeff, r >= 0xfff9 && r <= 0xfffb:
		return true
	}
	return false
}

// kind is the JSON kind of value, a value of a decoder with UseNumber.
func kind(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	default:
		return "null"
	}
}

// shown is value as JSON, cut to 64 characters, for a message.
func shown(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return kind(value)
	}
	text := string(data)
	if utf8.RuneCountInString(text) > 64 {
		text = string([]rune(text)[:63]) + "…"
	}
	return exports.HideValues(text)
}
