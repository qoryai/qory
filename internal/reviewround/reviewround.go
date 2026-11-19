// Package reviewround decides a review round: from each reviewer's report file, the result
// of the full suite and the open findings of the round before, it returns the round's
// verdict, the verdict's points and the round's open findings, which qory writes as
// open-findings.json for the next session. It starts no session and imports nothing from
// Forager, so any program that runs the reviewers can run the same decision.
//
// The rule is the user's 694: every finding is blocking or non-blocking. A round with a
// blocking finding is not clean, and the verdict is changes-requested, with every open
// finding, blocking and non-blocking, for the next session to fix. A round with only
// non-blocking findings, or none, is clean, and the verdict is passed. qory writes a
// blocking finding for a reviewer whose session failed or timed out, or that wrote no valid
// report, and for a full suite that failed or did not run. A suite whose cause the agent
// cannot fix makes the verdict escalate, so no round passes over a suite that did not run.
package reviewround

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/reportfile"
	"github.com/qoryai/qory/internal/runapi"
)

// The severities of a finding.
const (
	Blocking    = "blocking"
	NonBlocking = "non-blocking"
)

// The verdicts of a round.
const (
	Passed           = runapi.VerdictPassed
	ChangesRequested = runapi.VerdictChangesRequested
	Escalate         = runapi.VerdictEscalate
)

// SuiteEntry is the reviewer of the entry in [OpenFindings] that has the finding qory writes
// for a full suite that failed or did not run. It comes before the reviewers' entries.
const SuiteEntry = "full-suite"

// The limits of a verdict, as contracts/harness/v1/verdict.schema.json has them.
const (
	maxPoints    = 20
	maxPointText = 500
	// maxCause is the most characters of a cause that a point of qory's quotes.
	maxCause = 300
)

// MaxOpenBytes is the most bytes of open-findings.json, as [OpenFindings.JSON] writes it:
// the harness's findings open reads no more of the file than 256 KiB.
const MaxOpenBytes = 256 << 10

// measureID is the id that a finding of qory's has while [Decide] measures
// open-findings.json: as long as the longest id, so that no real id makes the file larger,
// and of another form, so that no finding of a reviewer has it.
const measureID = "x999999"

// The points of the findings that qory writes. Each is one line.
const (
	textFailed      = "Reviewer %s wrote no report: its session failed."
	textTimedOut    = "Reviewer %s wrote no report: its session reached its time limit."
	textNoReport    = "Reviewer %s wrote no report."
	textBadFile     = "Reviewer %s wrote no valid report: the report file %s."
	textNotText     = "Reviewer %s wrote a report that is not UTF-8 text."
	textNotJSON     = "Reviewer %s wrote a report that is not one JSON object: %s."
	textNotSchema   = "Reviewer %s wrote a report that does not match the report format: %s."
	textOtherName   = "Reviewer %s wrote a report for the reviewer %s."
	textSuiteFailed = "The full suite failed."
	textSuiteNotRun = "The full suite did not run."
	textNoSuite     = "The full suite has no result for the reviewed commit."
	textNoPoint     = "A finding of %s has a point with no readable text."
	textMore        = "The review has %d more open findings than these points list."
	textCut         = "The review has %d more open findings of %s than this file lists."
	textBlankPoint  = "at '/findings/%d/point': the point has no character that is not a space"
)

// Finding is one finding of a reviewer, as its report has it and open-findings.json carries
// it: the id that qory gives it, its severity and its point, and the members the reviewer
// wrote. A finding that qory writes has id, severity and point.
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Quote    string `json:"quote,omitempty"`
	Problem  string `json:"problem,omitempty"`
	Fix      string `json:"fix,omitempty"`
	Proof    string `json:"proof,omitempty"`
	Point    string `json:"point"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// ReviewerFindings is one entry of [OpenFindings]: a reviewer and its findings of the round.
type ReviewerFindings struct {
	Reviewer string    `json:"reviewer"`
	Findings []Finding `json:"findings"`
}

// OpenFindings is open-findings.json: the round's number, from 1, and each reviewer's
// findings of that round, blocking and non-blocking.
type OpenFindings struct {
	Round     int                `json:"round"`
	Reviewers []ReviewerFindings `json:"reviewers"`
}

// Ending is how a reviewer's session ended.
type Ending int

// The endings of a reviewer's session.
const (
	// Finished is a session that ended with no failure: qory reads its report.
	Finished Ending = iota + 1
	// Failed is a session that failed or did not start.
	Failed
	// TimedOut is a session that reached its time limit.
	TimedOut
)

// Reviewer is one reviewer of the round: its agent name, how its session ended, and the
// absolute path of its report file.
type Reviewer struct {
	Name   string
	Ended  Ending
	Report string
}

// SuiteState is how the full suite on the reviewed commit ended.
type SuiteState string

// The states of the full suite, as the verdict's full_suite has them.
const (
	SuitePassed SuiteState = "passed"
	SuiteFailed SuiteState = "failed"
	SuiteNotRun SuiteState = "not_run"
)

// Suite is the result of the full suite on the reviewed commit: its state, the point for
// a state other than passed, one line, and whether the cause is one the agent cannot fix,
// such as a run with no after step. A state that is not passed with AgentCannotFix makes
// the verdict escalate.
type Suite struct {
	State          SuiteState
	Point          string
	AgentCannotFix bool
}

// Round is what [Decide] decides on. Number is the round's number, from 1. NextID is the
// number of the id of the round's first new finding, f<NextID>: qory numbers the findings
// across the run, so the caller keeps the number that the last [Decision] returned. 0 starts
// after the highest id of Previous. Reviewers are in the order that the stage's reviewers
// group lists them. Suite is nil for a round with no result of the suite. Previous is the
// open findings of the round before, nil in round 1.
type Round struct {
	Number    int
	NextID    int
	Reviewers []Reviewer
	Suite     *Suite
	Previous  *OpenFindings
}

// Decision is the round's verdict, its points, the round's open findings, and NextID, the
// number of the next new finding's id in the run.
type Decision struct {
	Verdict string
	Points  []runapi.Point
	Open    OpenFindings
	NextID  int
}

// nameForm is the form of a reviewer's name: lower-case letters, digits and hyphens, from a
// letter or a digit, at most 64 characters.
var nameForm = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// idForm is the form of a finding's id: f and a number from 1, at most 6 digits.
var idForm = regexp.MustCompile(`^f[1-9][0-9]{0,5}$`)

// Decide decides r. The error is for a round that a caller made wrong: a number below 1, no
// reviewer, a reviewer with a name of another form or the name full-suite, a name twice, or
// an ending that is none of [Finished], [Failed] and [TimedOut]. A report that is missing or
// invalid is a finding, never an error. The decision's open findings make an
// open-findings.json of at most [MaxOpenBytes]: past that, the findings that come last in
// the order of the verdict's points stay out of the file, and each reviewer that has fewer
// findings in the file has one more finding of qory's, with how many of its findings are
// out. The verdict's points come from every finding of the round.
func Decide(r Round) (Decision, error) {
	if r.Number < 1 {
		return Decision{}, fmt.Errorf("Number the round from 1: the round's number is %d.", r.Number)
	}
	if len(r.Reviewers) == 0 {
		return Decision{}, errors.New("Give the round at least one reviewer: the round has none.")
	}
	seen := map[string]bool{}
	for _, one := range r.Reviewers {
		if !nameForm.MatchString(one.Name) || one.Name == SuiteEntry {
			return Decision{}, fmt.Errorf("Name each reviewer with lower-case letters, digits and hyphens, not %s.\nThe reviewer %s has another name.", SuiteEntry, strconv.Quote(one.Name))
		}
		if seen[one.Name] {
			return Decision{}, fmt.Errorf("List each reviewer once.\nThe reviewer %s is in the round twice.", one.Name)
		}
		if one.Ended < Finished || one.Ended > TimedOut {
			return Decision{}, fmt.Errorf("Set how the session of each reviewer ended.\nThe ending of reviewer %s is %d.", one.Name, one.Ended)
		}
		seen[one.Name] = true
	}
	ids := newNumbering(r.NextID, r.Previous)
	open := OpenFindings{Round: r.Number, Reviewers: []ReviewerFindings{}}
	suiteFinding, escalate := suiteOutcome(r.Suite)
	if suiteFinding != nil {
		suiteFinding.ID = ids.fresh()
		open.Reviewers = append(open.Reviewers, ReviewerFindings{Reviewer: SuiteEntry, Findings: []Finding{*suiteFinding}})
	}
	for _, one := range r.Reviewers {
		open.Reviewers = append(open.Reviewers, ReviewerFindings{Reviewer: one.Name, Findings: reviewerFindings(one, ids)})
	}
	blocking := false
	for _, entry := range open.Reviewers {
		blocking = blocking || slices.ContainsFunc(entry.Findings, func(f Finding) bool { return f.Severity == Blocking })
	}
	d := Decision{}
	switch {
	case escalate:
		d.Verdict = Escalate
	case blocking:
		d.Verdict = ChangesRequested
	default:
		d.Verdict = Passed
	}
	if d.Verdict != Passed {
		d.Points = points(open)
	}
	cut, err := cutToFit(open, ids)
	if err != nil {
		return Decision{}, err
	}
	d.Open, d.NextID = cut, ids.next
	return d, nil
}

// place is the place of a finding in [OpenFindings]: the entry and the finding's index.
type place struct{ entry, finding int }

// cutToFit is open with as many findings as an open-findings.json of at most
// [MaxOpenBytes] has room for, with the findings in the order of the verdict's points, the
// blocking ones first. Each entry that has fewer findings gets one more finding of qory's,
// blocking when a blocking finding of the entry is out, with an id from ids.
func cutToFit(open OpenFindings, ids *numbering) (OpenFindings, error) {
	var first, later []place
	for e, entry := range open.Reviewers {
		for i, f := range entry.Findings {
			if f.Severity == Blocking {
				first = append(first, place{e, i})
			} else {
				later = append(later, place{e, i})
			}
		}
	}
	order := append(first, later...)
	fits := func(o OpenFindings) (bool, error) {
		data, err := o.JSON()
		return len(data) <= MaxOpenBytes, err
	}
	if ok, err := fits(open); ok || err != nil {
		return open, err
	}
	low, high := 0, len(order)
	if ok, err := fits(keep(open, order[:low])); !ok || err != nil {
		if err == nil {
			err = fmt.Errorf("Give the round fewer reviewers: open-findings.json for %d reviewers has more than %d bytes.", len(open.Reviewers), MaxOpenBytes)
		}
		return OpenFindings{}, err
	}
	for high-low > 1 {
		mid := (low + high) / 2
		ok, err := fits(keep(open, order[:mid]))
		if err != nil {
			return OpenFindings{}, err
		}
		if ok {
			low = mid
		} else {
			high = mid
		}
	}
	cut := keep(open, order[:low])
	for e := range cut.Reviewers {
		for i := range cut.Reviewers[e].Findings {
			if cut.Reviewers[e].Findings[i].ID == measureID {
				cut.Reviewers[e].Findings[i].ID = ids.fresh()
			}
		}
	}
	return cut, nil
}

// keep is open with only the findings at kept, each in its entry's order, and for each entry
// with fewer findings the finding of qory's that states how many are out, with the id
// [measureID].
func keep(open OpenFindings, kept []place) OpenFindings {
	in := map[place]bool{}
	for _, p := range kept {
		in[p] = true
	}
	cut := OpenFindings{Round: open.Round, Reviewers: make([]ReviewerFindings, 0, len(open.Reviewers))}
	for e, entry := range open.Reviewers {
		findings := []Finding{}
		out, severity := 0, NonBlocking
		for i, f := range entry.Findings {
			if in[place{e, i}] {
				findings = append(findings, f)
				continue
			}
			out++
			if f.Severity == Blocking {
				severity = Blocking
			}
		}
		if out > 0 {
			findings = append(findings, Finding{ID: measureID, Severity: severity, Point: fmt.Sprintf(textCut, out, entry.Reviewer)})
		}
		cut.Reviewers = append(cut.Reviewers, ReviewerFindings{Reviewer: entry.Reviewer, Findings: findings})
	}
	return cut
}

// suiteOutcome is the finding for s, nil for a suite that passed, and whether the verdict
// escalates.
func suiteOutcome(s *Suite) (*Finding, bool) {
	if s == nil {
		return &Finding{Severity: Blocking, Point: textNoSuite}, true
	}
	text := plainLine(s.Point, maxPointText)
	switch s.State {
	case SuitePassed:
		return nil, false
	case SuiteFailed:
		if text == "" {
			text = textSuiteFailed
		}
	default:
		if text == "" {
			text = textSuiteNotRun
		}
		if s.State != SuiteNotRun {
			return &Finding{Severity: Blocking, Point: text}, true
		}
	}
	return &Finding{Severity: Blocking, Point: text}, s.AgentCannotFix
}

// reviewerFindings is the findings of one reviewer: its report's, each with its id, or the
// one blocking finding that qory writes for a session that failed or a report that is
// missing or invalid.
func reviewerFindings(one Reviewer, ids *numbering) []Finding {
	written := func(point string) []Finding {
		return []Finding{{ID: ids.fresh(), Severity: Blocking, Point: point}}
	}
	switch one.Ended {
	case Failed:
		return written(fmt.Sprintf(textFailed, one.Name))
	case TimedOut:
		return written(fmt.Sprintf(textTimedOut, one.Name))
	}
	data, err := reportfile.Read(one.Report, reportfile.MaxBytes)
	if err != nil {
		var bad *reportfile.Error
		if errors.As(err, &bad) && bad.Cause == reportfile.Missing {
			return written(fmt.Sprintf(textNoReport, one.Name))
		}
		what := "cannot be read"
		if bad != nil {
			what = bad.What()
		}
		return written(fmt.Sprintf(textBadFile, one.Name, plainLine(what, maxCause)))
	}
	report, point := parseReport(one.Name, data)
	if point != "" {
		return written(point)
	}
	findings := make([]Finding, 0, len(report.Findings))
	for _, f := range report.Findings {
		f.ID = ids.take(f.ID)
		findings = append(findings, f)
	}
	return findings
}

// report is a reviewer's report.json.
type report struct {
	Reviewer string    `json:"reviewer"`
	Findings []Finding `json:"findings"`
}

// parseReport reads data, the report of the reviewer name, against the report format, and
// returns it, or the point of the finding that qory writes for it.
func parseReport(name string, data []byte) (report, string) {
	if !utf8.Valid(data) || bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return report{}, fmt.Sprintf(textNotText, name)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return report{}, fmt.Sprintf(textNotJSON, name, plainLine(err.Error(), maxCause))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return report{}, fmt.Sprintf(textNotJSON, name, "the file continues after the JSON value")
	}
	schema, err := reportSchema()
	if err != nil {
		return report{}, fmt.Sprintf(textNotSchema, name, plainLine(err.Error(), maxCause))
	}
	if err := schema.Validate(doc); err != nil {
		return report{}, fmt.Sprintf(textNotSchema, name, plainLine(firstRule(err), maxCause))
	}
	var r report
	if err := json.Unmarshal(data, &r); err != nil {
		return report{}, fmt.Sprintf(textNotJSON, name, plainLine(err.Error(), maxCause))
	}
	if r.Reviewer != name {
		return report{}, fmt.Sprintf(textOtherName, name, r.Reviewer)
	}
	// The format's \S excludes only the spaces of ASCII. strings.TrimSpace, which
	// ReadOpenFindings uses, also removes a space of Unicode, such as U+00A0.
	for i := range r.Findings {
		r.Findings[i].Point = strings.TrimSpace(r.Findings[i].Point)
		if r.Findings[i].Point == "" {
			return report{}, fmt.Sprintf(textNotSchema, name, fmt.Sprintf(textBlankPoint, i))
		}
	}
	return r, ""
}

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
	one := jsonschema.ValidationError{SchemaURL: broken.SchemaURL, InstanceLocation: broken.InstanceLocation, ErrorKind: broken.ErrorKind}
	return one.Error()
}

// points is the verdict's points for open: one run_again point per finding, the blocking
// ones first, each with the finding's point as its text. With more than the verdict's 20,
// the 20th point states how many more findings are open.
func points(open OpenFindings) []runapi.Point {
	var first, later []runapi.Point
	for _, entry := range open.Reviewers {
		for _, f := range entry.Findings {
			text := plainLine(f.Point, maxPointText)
			if text == "" {
				text = fmt.Sprintf(textNoPoint, entry.Reviewer)
			}
			p := runapi.Point{Text: text, Action: runapi.ActionRunAgain}
			if f.Severity == Blocking {
				first = append(first, p)
			} else {
				later = append(later, p)
			}
		}
	}
	all := append(first, later...)
	if len(all) > maxPoints {
		more := len(all) - (maxPoints - 1)
		all = append(all[:maxPoints-1], runapi.Point{Text: fmt.Sprintf(textMore, more), Action: runapi.ActionRunAgain})
	}
	return all
}

// numbering gives the round's findings their ids: an id of the round before that a reviewer
// repeats, once per round, and f<next> for each other finding.
type numbering struct {
	next     int
	previous map[string]bool
	used     map[string]bool
}

// newNumbering starts the ids at next, or after the highest id of previous when that is
// higher.
func newNumbering(next int, previous *OpenFindings) *numbering {
	n := &numbering{next: max(next, 1), previous: map[string]bool{}, used: map[string]bool{}}
	if previous == nil {
		return n
	}
	for _, entry := range previous.Reviewers {
		for _, f := range entry.Findings {
			n.previous[f.ID] = true
			if number, err := strconv.Atoi(strings.TrimPrefix(f.ID, "f")); err == nil && number >= n.next {
				n.next = number + 1
			}
		}
	}
	return n
}

// take is id when it is an id of the round before that no finding of this round has yet, and
// a fresh id otherwise.
func (n *numbering) take(id string) string {
	if id != "" && n.previous[id] && !n.used[id] {
		n.used[id] = true
		return id
	}
	return n.fresh()
}

// fresh is the next id that no finding of the run has.
func (n *numbering) fresh() string {
	for {
		id := "f" + strconv.Itoa(n.next)
		n.next++
		if !n.previous[id] && !n.used[id] {
			n.used[id] = true
			return id
		}
	}
}

// JSON is open-findings.json for o, with <, > and & as they are, not as \u escapes.
func (o OpenFindings) JSON() ([]byte, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(o); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ReadOpenFindings reads data, an open-findings.json that qory wrote, with the checks that
// the harness's findings open makes. The error starts with the fix.
func ReadOpenFindings(data []byte) (OpenFindings, error) {
	const fix = "Give the open-findings.json that qory wrote for the round before."
	broken := func(detail string, args ...any) (OpenFindings, error) {
		return OpenFindings{}, fmt.Errorf(fix+"\nThe file "+detail+".", args...)
	}
	if !utf8.Valid(data) {
		return broken("is not UTF-8 text")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var o OpenFindings
	if err := decoder.Decode(&o); err != nil {
		return broken("is not one JSON object in the format: %s", plainLine(err.Error(), maxCause))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return broken("continues after the JSON object")
	}
	if o.Round < 1 {
		return broken("has no round that is a whole number of 1 or more")
	}
	if o.Reviewers == nil {
		return broken("has no list reviewers")
	}
	for at, entry := range o.Reviewers {
		if !nameForm.MatchString(entry.Reviewer) {
			return broken("has reviewers %d with no reviewer name of lower-case letters, digits and hyphens", at+1)
		}
		if entry.Findings == nil {
			return broken("has reviewers %d with no list findings", at+1)
		}
		for place, f := range entry.Findings {
			which := fmt.Sprintf("finding %d of %s", place+1, entry.Reviewer)
			switch {
			case !idForm.MatchString(f.ID):
				return broken("has %s with no id of the form f<number>", which)
			case f.Severity != Blocking && f.Severity != NonBlocking:
				return broken("has %s with a severity that is neither blocking nor non-blocking", which)
			case strings.TrimSpace(f.Point) == "":
				return broken("has %s with an empty point", which)
			case f.Line < 0:
				return broken("has %s with a line that is not a whole number of 1 or more", which)
			}
		}
	}
	return o, nil
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
// verdict's line excludes becomes a space, the spaces at the ends go, and a longer text ends
// with "…" at the limit.
func plainLine(text string, limit int) string {
	var b strings.Builder
	for _, r := range text {
		if excluded(r) {
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

// excluded reports whether the line of contracts/harness/v1/verdict.schema.json excludes
// r: a control character, a bidirectional control, a line or paragraph separator, or one
// of the format characters that the line lists.
func excluded(r rune) bool {
	switch {
	case r <= 0x1f, r >= 0x7f && r <= 0x9f:
		return true
	case r == 0x00ad, r == 0x061c, r == 0x180e, r == 0x200b, r == 0x200e, r == 0x200f,
		r == 0x2028, r == 0x2029, r == 0xfeff:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069,
		r >= 0xfff9 && r <= 0xfffb:
		return true
	}
	return false
}
