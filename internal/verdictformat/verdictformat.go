// Package verdictformat is version 1 of the verdict format: a run's verdict, and for
// escalate or changes-requested its points. The contract is
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
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/runapi"
)

// The limits of the format.
const (
	// MaxPoints is the most points of a verdict.
	MaxPoints = 20
	// MaxText is the most characters of a point's text, and of its fix.
	MaxText = 500
	// MaxTrackID is the most characters of a track's id.
	MaxTrackID = 40
	// MaxReason is the most characters of a track's reason.
	MaxReason = 300
	// MaxKind is the most characters of a verdict's kind.
	MaxKind = 64
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

// byteOrderMark starts a file that an editor saved with a UTF-8 byte order mark.
const byteOrderMark = "\xef\xbb\xbf"

// The errors of [Points] for data that is not plain UTF-8. A caller with the file's path
// writes a line that contains it.
var (
	// ErrByteOrderMark is the error of data that starts with a UTF-8 byte order mark.
	ErrByteOrderMark = errors.New("Save the file without a byte order mark.")
	// ErrNotUTF8 is the error of data that is not UTF-8.
	ErrNotUTF8 = errors.New("Save the file as UTF-8.")
	// ErrLoneSurrogate is the error of data with an escape of a UTF-16 surrogate that is
	// not half of a pair, such as \ud800, in a string. The escape is ASCII, so saving the
	// file as UTF-8 keeps it.
	ErrLoneSurrogate = errors.New("Remove the escape of a lone surrogate, such as \\ud800, from the file.")
)

// questionID is the id form of a question in contracts/harness/v1/questions.schema.json.
var questionID = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// kindForm is the form of a verdict's kind: a lower-case letter, then lower-case letters,
// digits and hyphens.
var kindForm = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidKind reports whether kind has the form of a verdict's kind, at most [MaxKind]
// characters: one of the three kinds of this version, or one that a later version adds.
func ValidKind(kind string) bool {
	return len(kind) <= MaxKind && kindForm.MatchString(kind)
}

// Known reports whether kind is one of the kinds this version of the format defines:
// passed, changes-requested or escalate.
func Known(kind string) bool {
	return kind == runapi.VerdictPassed || kind == runapi.VerdictChangesRequested || kind == runapi.VerdictEscalate
}

// commandAction reports whether action is one that a point of qory run verdict takes:
// answer or run_again. The format takes any action of its form.
// shortcut: Qory Apiary returns an error for any other verdict kind or point action; allow
// every kind and action of the format once Qory Apiary stores them.
func commandAction(action string) bool {
	return action == runapi.ActionAnswer || action == runapi.ActionRunAgain
}

// trackID is the form of a track's id: lower-case words joined by hyphens.
var trackID = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// ValidTrackID reports whether id has the form of a track's id, at most [MaxTrackID]
// characters.
func ValidTrackID(id string) bool {
	return len(id) <= MaxTrackID && trackID.MatchString(id)
}

// CheckTrack checks track against the schema, as the track of a verdict that passed. The
// error starts with the fix.
func CheckTrack(track runapi.Track) error {
	body, err := runapi.VerdictBody(runapi.VerdictPassed, nil, &track)
	if err != nil {
		return err
	}

	return Check(body)
}

// pointMembers are the members a point has.
var pointMembers = []string{"text", "action", "question", "fix"}

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

// Points reads data, a JSON array of the points of an escalation for qory run verdict,
// and checks it against the schema and [commandAction]. The error starts with the fix.
// A changes-requested verdict takes run_again points alone, which its caller checks.
// An error about one point contains the point's number, from 1, such as "Write each
// point as {text, action}: point 2 has no action." Points does not check that a
// question is open.
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
	body, err := runapi.VerdictBody(runapi.VerdictEscalate, points, nil)
	if err != nil {
		return nil, err
	}
	if err := Check(body); err != nil {
		return nil, err
	}
	return points, nil
}

// pointsArray reads data, a JSON array of 1 to [MaxPoints] values in UTF-8.
func pointsArray(data []byte) ([]any, error) {
	if bytes.HasPrefix(data, []byte(byteOrderMark)) {
		return nil, ErrByteOrderMark
	}
	if !utf8.Valid(data) {
		return nil, ErrNotUTF8
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New(`Write the points as a JSON array, such as [{"text": "…", "action": "run_again"}]. The file is empty.`)
	}
	var doc any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, notJSON(err.Error())
	}
	if loneSurrogate(data[:decoder.InputOffset()]) {
		return nil, ErrLoneSurrogate
	}
	array, ok := doc.([]any)
	if !ok {
		return nil, fmt.Errorf(`Write the points as a JSON array, such as [{"text": "…", "action": "run_again"}]. The file is a JSON %s.`, kind(doc))
	}
	if _, next := decoder.Token(); !errors.Is(next, io.EOF) {
		return nil, notJSON("the file continues after the JSON array")
	}
	if len(array) == 0 {
		return nil, errors.New("Write at least one point. The array is empty.")
	}
	if len(array) > MaxPoints {
		return nil, fmt.Errorf("Write at most %d points. The array has %d.", MaxPoints, len(array))
	}
	return array, nil
}

// notJSON is the error of a file that does not read as one JSON value, with cause.
func notJSON(cause string) error {
	return fmt.Errorf(`Write the points as one JSON array, such as [{"text": "…", "action": "run_again"}]. Reading the JSON failed: %s`,
		exports.HideValues(cause))
}

// loneSurrogate reports whether data, one JSON value, has an escape of a UTF-16
// surrogate that is not half of a pair, such as \ud800, in a string. encoding/json reads
// such an escape as U+FFFD, so the text would change on its way to the run API.
func loneSurrogate(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' || i+1 >= len(data) {
			continue
		}
		if data[i+1] != 'u' {
			i++
			continue
		}
		r := escaped(data, i)
		i += 5
		switch {
		case r >= 0xdc00 && r <= 0xdfff:
			return true
		case r >= 0xd800 && r <= 0xdbff:
			if next := escaped(data, i+1); next < 0xdc00 || next > 0xdfff {
				return true
			}
			i += 6
		}
	}
	return false
}

// escaped is the code unit of the escape \uXXXX at data[i:], or -1 when there is none.
func escaped(data []byte, i int) int {
	if i+6 > len(data) || data[i] != '\\' || data[i+1] != 'u' {
		return -1
	}
	unit, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
	if err != nil {
		return -1
	}
	return int(unit)
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
		return runapi.Point{}, fmt.Errorf("Remove %s from point %d: a point has only text, action, question and fix.", shown(extra[0]), n)
	}

	text, ok := members["text"].(string)
	if !ok {
		return runapi.Point{}, fmt.Errorf("Write the text of point %d as a JSON string. The text is a JSON %s.", n, kind(members["text"]))
	}
	if err := checkLine(n, "text", text); err != nil {
		return runapi.Point{}, err
	}

	action, ok := members["action"].(string)
	if !ok || !commandAction(action) {
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

	fix, err := readFix(n, action, members)
	if err != nil {
		return runapi.Point{}, err
	}

	return runapi.Point{Text: text, Action: action, Question: question, Fix: fix}, nil
}

// readFix checks the fix of point number n, which has members and action, and returns
// it, empty for none. Only a point with the action run_again has a fix.
func readFix(n int, action string, members map[string]any) (string, error) {
	value, ok := members["fix"]
	if !ok {
		return "", nil
	}
	if action != runapi.ActionRunAgain {
		return "", fmt.Errorf("Remove fix from point %d: a point with the action %s has none.", n, action)
	}
	fix, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("Write the fix of point %d as a JSON string. The fix is a JSON %s.", n, kind(value))
	}
	if err := checkLine(n, "fix", fix); err != nil {
		return "", err
	}
	return fix, nil
}

// checkLine checks line, the member name of point number n, a text or a fix: 1 to
// [MaxText] characters on one line, and none that the schema's $defs line excludes.
func checkLine(n int, name, line string) error {
	length := utf8.RuneCountInString(line)
	if length == 0 {
		return fmt.Errorf("Write the %s of point %d: one sentence on one line, 1 to %d characters. The %s is empty.", name, n, MaxText, name)
	}
	if length > MaxText {
		return fmt.Errorf("Shorten the %s of point %d to at most %d characters. The %s has %d.", name, n, MaxText, name, length)
	}
	for _, r := range line {
		switch r {
		case '\t':
			return fmt.Errorf("Write the %s of point %d on one line. It has a tab (%U).", name, n, r)
		case '\n', '\r':
			return fmt.Errorf("Write the %s of point %d on one line. It has a line break (%U).", name, n, r)
		}
		if primitives.StopsLine(r) {
			return fmt.Errorf("Remove the character %U from the %s of point %d: the contract's $defs line excludes it.", r, name, n)
		}
	}
	return nil
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
