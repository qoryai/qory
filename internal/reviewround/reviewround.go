// Package reviewround reads the report of one reviewer of a review round: the findings
// that the reviewer raises or repeats, and the findings that it closes. It checks the
// report against the report format and against the findings that the reviewer has in the
// work item, and starts no session. It imports nothing from Forager, so any program that
// runs the reviewers can read the same report.
//
// A finding of the report with no id is new. A finding with an id repeats a finding of the
// reviewer. A close has the id of a finding of the reviewer, the state fixed or dismissed
// and a reason. A finding of the reviewer that the report leaves out stays open.
package reviewround

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/qoryai/qory/internal/primitives"
	"github.com/qoryai/qory/internal/reportfile"
)

// The severities of a finding.
const (
	Blocking    = "blocking"
	NonBlocking = "non-blocking"
)

// The states that a close sets.
const (
	Fixed     = "fixed"
	Dismissed = "dismissed"
)

// maxCause is the most characters of a cause that a text of qory's quotes.
const maxCause = 300

// The texts of an error of [Read], [Parse] and [Report.Check]. Each is one line, and the
// point of the finding that qory writes for a reviewer with no valid report.
const (
	textNoReport      = "Reviewer %s wrote no report."
	textBadFile       = "Reviewer %s wrote no valid report: the report file %s."
	textNotText       = "Reviewer %s wrote a report that is not UTF-8 text."
	textNotJSON       = "Reviewer %s wrote a report that is not one JSON object: %s."
	textTwice         = "Reviewer %s wrote a report with a member twice in one object, or with an unpaired surrogate escape."
	textNotSchema     = "Reviewer %s wrote a report that does not match the report format: %s."
	textOtherName     = "Reviewer %s wrote a report for the reviewer %s."
	textBlankPoint    = "at '/findings/%d/point': the point has no character that shows"
	textRepeatsNot    = "Reviewer %s's report repeats %s, which is no finding of that reviewer."
	textClosesNot     = "Reviewer %s's report closes %s, which is no finding of that reviewer."
	textBoth          = "Reviewer %s's report both repeats and closes %s."
	textRepeatedTwice = "Reviewer %s's report repeats %s more than once."
	textClosedTwice   = "Reviewer %s's report closes %s more than once."
)

// Finding is one finding of a report. A finding with no ID is new, and one with an ID
// repeats that finding of the reviewer.
type Finding struct {
	ID       string `json:"id,omitempty,case:strict"`
	Severity string `json:"severity,case:strict"`
	Quote    string `json:"quote,case:strict"`
	Problem  string `json:"problem,case:strict"`
	Fix      string `json:"fix,case:strict"`
	Proof    string `json:"proof,case:strict"`
	Point    string `json:"point,case:strict"`
	File     string `json:"file,omitempty,case:strict"`
	Line     int    `json:"line,omitempty,case:strict"`
}

// ReviewerFindings is a reviewer and its findings, such as the entry of a hook's record of
// internal/hookrun.
type ReviewerFindings struct {
	Reviewer string    `json:"reviewer"`
	Findings []Finding `json:"findings"`
}

// Close is a reviewer's close of one of its findings: State is [Fixed] or [Dismissed].
type Close struct {
	ID     string `json:"id,case:strict"`
	State  string `json:"state,case:strict"`
	Reason string `json:"reason,case:strict"`
}

// Report is a reviewer's report.json.
type Report struct {
	Reviewer string    `json:"reviewer,case:strict"`
	Findings []Finding `json:"findings,case:strict"`
	Closes   []Close   `json:"closes,omitempty,case:strict"`
}

// NoReport is the error that [Read] returns for a reviewer whose report file is missing.
func NoReport(name string) error { return fmt.Errorf(textNoReport, name) }

// Read reads the report of the reviewer name from the file at rel below root, with
// reportfile.Read, which follows no link from root down, and parses it with [Parse]. Its
// error is [Parse]'s, or says that the file is missing or cannot be read. mask is as
// [Parse] has it.
func Read(root, rel, name string, mask func(string) string) (Report, error) {
	data, err := reportfile.Read(root, rel, reportfile.MaxBytes)
	if err != nil {
		var bad *reportfile.Error
		if errors.As(err, &bad) && bad.Cause == reportfile.Missing {
			return Report{}, NoReport(name)
		}
		what := "cannot be read"
		if bad != nil {
			what = bad.What()
		}
		return Report{}, fmt.Errorf(textBadFile, name, plainLine(what, maxCause, mask))
	}
	return Parse(name, data, mask)
}

// Parse reads data, the report of the reviewer name, against the report format and returns
// it. The error is one line, with the cause, for a report that is not UTF-8 text, not one
// JSON object, has a member twice in an object, does not match the format, is for another
// reviewer, or has a point with no character that shows. The point of a finding has no
// space at its ends. mask, when it is not nil, masks the report's text that an error
// quotes before the error cuts it, since a cut inside a secret leaves the secret's start.
func Parse(name string, data []byte, mask func(string) string) (Report, error) {
	if !utf8.Valid(data) || bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return Report{}, fmt.Errorf(textNotText, name)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return Report{}, fmt.Errorf(textNotJSON, name, plainLine(err.Error(), maxCause, mask))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Report{}, fmt.Errorf(textNotJSON, name, "the file continues after the JSON value")
	}
	if !jsontext.Value(data).IsValid() {
		return Report{}, fmt.Errorf(textTwice, name)
	}
	schema, err := reportSchema()
	if err != nil {
		return Report{}, fmt.Errorf(textNotSchema, name, plainLine(err.Error(), maxCause, mask))
	}
	if err := schema.Validate(doc); err != nil {
		return Report{}, fmt.Errorf(textNotSchema, name, plainLine(firstRule(err), maxCause, mask))
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return Report{}, fmt.Errorf(textNotJSON, name, plainLine(err.Error(), maxCause, mask))
	}
	if r.Reviewer != name {
		return Report{}, fmt.Errorf(textOtherName, name, r.Reviewer)
	}
	// The format's \S excludes only the spaces of ASCII. A point of other spaces, or of
	// characters that show nothing, such as U+200B, is empty to the harness: see
	// [harnessPlain].
	for i := range r.Findings {
		r.Findings[i].Point = strings.TrimSpace(r.Findings[i].Point)
		if harnessPlain(r.Findings[i].Point) == "" {
			return Report{}, fmt.Errorf(textNotSchema, name, fmt.Sprintf(textBlankPoint, i))
		}
	}
	return r, nil
}

// Check checks r against owned, the ids of the findings that the work item has from the
// reviewer, in any state. The report is invalid when a finding repeats, or a close has, an
// id that is not in owned, when one id is repeated twice or closed twice, and when one id
// is both repeated and closed. The error is one line.
func (r Report) Check(owned map[string]bool) error {
	repeated := map[string]bool{}
	for _, f := range r.Findings {
		if f.ID == "" {
			continue
		}
		if !owned[f.ID] {
			return fmt.Errorf(textRepeatsNot, r.Reviewer, f.ID)
		}
		if repeated[f.ID] {
			return fmt.Errorf(textRepeatedTwice, r.Reviewer, f.ID)
		}
		repeated[f.ID] = true
	}
	closed := map[string]bool{}
	for _, c := range r.Closes {
		switch {
		case !owned[c.ID]:
			return fmt.Errorf(textClosesNot, r.Reviewer, c.ID)
		case closed[c.ID]:
			return fmt.Errorf(textClosedTwice, r.Reviewer, c.ID)
		case repeated[c.ID]:
			return fmt.Errorf(textBoth, r.Reviewer, c.ID)
		}
		closed[c.ID] = true
	}
	return nil
}

// textValue stands for the value of a member in the text of an error: the value is the
// agent's text, and an error text must not repeat it.
const textValue = "<value>"

// firstRule is the first rule of the report format that a document breaks, with its place,
// such as "at '/findings/0/severity': value must be one of 'blocking', 'non-blocking'".
func firstRule(err error) string {
	var broken *jsonschema.ValidationError
	if !errors.As(err, &broken) {
		return err.Error()
	}
	for len(broken.Causes) > 0 {
		broken = broken.Causes[0]
	}
	kindOf := broken.ErrorKind
	if pattern, ok := kindOf.(*kind.Pattern); ok {
		// The text of a pattern error quotes the value, which can be a secret that the cut
		// of the text would split, so the quote has a stand-in.
		kindOf = &kind.Pattern{Got: textValue, Want: pattern.Want}
	}
	one := jsonschema.ValidationError{SchemaURL: broken.SchemaURL, InstanceLocation: broken.InstanceLocation, ErrorKind: kindOf}
	return one.Error()
}

// reportFormat is the report format of a reviewer: a copy of
// contracts/harness/v1/review-report.schema.json, byte for byte, which a test checks:
// go:embed does not reach a file above the package.
//
//go:embed review-report.schema.json
var reportFormat []byte

// reportSchemaURL is the URL that the report format compiles under.
const reportSchemaURL = "https://qory.dev/contracts/harness/v1/review-report.schema.json"

// reportSchema is the report format, compiled once.
var reportSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(reportFormat))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(reportSchemaURL, doc); err != nil {
		return nil, err
	}
	return c.Compile(reportSchemaURL)
})

// plainLine is text on one line of at most limit characters: each character that a
// verdict's line excludes becomes a space, the spaces at the ends go, and a longer text
// ends with "…" at the limit. A mask that is not nil is applied before the cut.
func plainLine(text string, limit int, mask func(string) string) string {
	if mask != nil {
		text = mask(text)
	}
	var b strings.Builder
	for _, r := range text {
		if primitives.StopsLine(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	line := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(line) <= limit {
		return line
	}
	runes := []rune(line)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

// harnessPlain is text as the harness's plain() leaves it before findings open checks a
// point (qoryai/harness, modules/core/skills/running-a-workflow/lib/base.cjs:130-131):
// each control character (CONTROL_ALL) a space, each character of REFUSED_SET and of
// HIDDEN removed, and the spaces at the ends trimmed. A point whose harnessPlain is "" is
// empty to the harness. HIDDEN's lone surrogate cannot be in a Go string: JSON decodes one
// as U+FFFD.
func harnessPlain(text string) string {
	var b strings.Builder
	for _, r := range text {
		switch {
		case r <= 0x1f, r >= 0x7f && r <= 0x9f:
			b.WriteRune(' ')
		case refused(r) || hidden(r):
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// refused reports whether r is in base.cjs's REFUSED_SET: the bidirectional marks and
// controls, the invisible characters, and the line and paragraph separators.
func refused(r rune) bool {
	switch {
	case r == 0x061c, r == 0x200e, r == 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069,
		r == 0x00ad, r == 0x180e, r == 0x200b, r >= 0x2060 && r <= 0x2064, r == 0xfeff,
		r >= 0xfff9 && r <= 0xfffb, r == 0x2028, r == 0x2029:
		return true
	}
	return false
}

// hidden reports whether r is in base.cjs's HIDDEN: Default_Ignorable_Code_Point, which
// Unicode derives from Other_Default_Ignorable_Code_Point, Cf and Variation_Selector, or
// Cf, or U+FFFC. Go's tables and Node's may be of different Unicode versions.
func hidden(r rune) bool {
	return unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || unicode.Is(unicode.Cf, r) ||
		unicode.Is(unicode.Variation_Selector, r) || r == 0xfffc
}
