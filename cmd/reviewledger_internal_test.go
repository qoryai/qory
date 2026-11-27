package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/reviewround"
	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/suite"
)

var ledgerTarget = contracts.Target{Collection: "R_1001",
	Address: &contracts.TargetAddress{System: "forge", Path: "acme/shop"}}

// ledgerWhere is the target and the stage of the tests.
var ledgerWhere = where{target: ledgerTarget, stage: "build"}

// stored is an event of a finding as a GET lists it, written by the qory actor by.
func stored(kind, by string, round int, reason string) contracts.FindingEvent {
	return contracts.FindingEvent{Kind: kind, Round: round, Stage: "build",
		Actor: contracts.FindingActor{Kind: "qory", Name: by}, Reason: reason}
}

// person is an event of a finding that a person wrote.
func person(kind string) contracts.FindingEvent {
	return contracts.FindingEvent{Kind: kind, Round: 1, Stage: "build", Actor: contracts.FindingActor{Kind: "person"}}
}

// apiaryFinding is a finding of the stage build, as a GET lists it, with the events given
// after the raise by its source.
func apiaryFinding(id, kind, name, state string, events ...contracts.FindingEvent) contracts.Finding {
	return contracts.Finding{ID: id, Target: ledgerTarget, Stage: "build",
		Source:   contracts.FindingSource{Kind: kind, Name: name},
		Severity: "blocking", Place: contracts.FindingPlace{Quote: "quote " + id, File: "a.go", Line: 3},
		Point: "point " + id, Problem: "problem " + id, Fix: "fix " + id, Proof: "proof " + id,
		State: state, Events: append([]contracts.FindingEvent{stored("raised", name, 1, "")}, events...)}
}

// listing is a GET of the findings.
func listing(f ...contracts.Finding) contracts.Findings { return contracts.Findings{Findings: f} }

func TestReviewLedgerStageName(t *testing.T) {
	for _, c := range []struct{ name, id, want string }{
		{"Build", "st_aaa", "build"},
		{"Code review", "st_aaa", "code-review"},
		{"  A  &  B! ", "st_aaa", "a-b"},
		{"Prüfung 2", "st_aaa", "pr-fung-2"},
		{"", "st_ab12", "ab12"},
		{"---", "st_ab12", "ab12"},
		{"日本語", "st_ab12", "ab12"},
		{"", "plain", "plain"},
		{strings.Repeat("a", 70), "st_x", strings.Repeat("a", 64)},
		{strings.Repeat("a", 63) + " b", "st_x", strings.Repeat("a", 63)},
		{strings.Repeat("a", 62) + " bc", "st_x", strings.Repeat("a", 62) + "-b"},
	} {
		if got := stageName(c.name, c.id); got != c.want {
			t.Errorf("stageName(%q, %q) = %q, want %q", c.name, c.id, got, c.want)
		}
	}
}

func TestReviewLedgerExpectedRoles(t *testing.T) {
	other := apiaryFinding("f6", "reviewer", "docs-reviewer", "open")
	other.Stage = "verify"
	found := listing(
		apiaryFinding("f1", "reviewer", "b-reviewer", "open"),
		apiaryFinding("f2", "reviewer", "c-reviewer", "fixed", stored("closed_fixed", "c-reviewer", 1, "r")),
		apiaryFinding("f3", "check", "d-reviewer", "open"),
		apiaryFinding("f4", "reviewer", "e-reviewer", "open"),
		other,
	)
	group := []string{"a-reviewer", "b-reviewer", "c-reviewer", "d-reviewer", "docs-reviewer"}
	got := expectedRoles(group, []string{"a-reviewer"}, found, "build")
	if want := []string{"a-reviewer", "b-reviewer"}; !slices.Equal(got, want) {
		t.Errorf("the expected roles are %v, want %v: a path match and a role with an open finding of the stage, in group order; no closed finding, check, other stage or role outside the group", got, want)
	}
	if got := expectedRoles(group, nil, listing(), "build"); got != nil {
		t.Errorf("with no match and no finding the roles are %v", got)
	}
}

// decodedFile is the file of findings as a test reads it back.
func decodedFile(t *testing.T, data []byte) (int, []map[string]any) {
	t.Helper()
	var doc struct {
		Round    int              `json:"round"`
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the file is no JSON: %v\n%s", err, data)
	}
	return doc.Round, doc.Findings
}

func ids(findings []map[string]any) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f["id"].(string))
	}
	return out
}

func TestReviewLedgerPreviousFindings(t *testing.T) {
	replied := apiaryFinding("f1", "reviewer", "code-reviewer", "open",
		stored("replied_fixed", "implement-changes", 2, "Fixed in a1b2c3."),
		stored("replied_disputed", "implement-changes", 3, "The input is checked."))
	replied.Disputed = true
	verify := apiaryFinding("f7", "reviewer", "code-reviewer", "open")
	verify.Stage = "verify"
	got := listing(
		replied,
		apiaryFinding("f2", "reviewer", "code-reviewer", "fixed", stored("closed_fixed", "code-reviewer", 2, "Now fixed.")),
		apiaryFinding("f3", "reviewer", "code-reviewer", "dismissed", stored("closed_dismissed", "code-reviewer", 2, "No.")),
		apiaryFinding("f4", "reviewer", "code-reviewer", "dismissed", person("dismissed_by_person")),
		apiaryFinding("f5", "reviewer", "code-reviewer", "accepted", person("accepted")),
		apiaryFinding("f6", "reviewer", "security-reviewer", "open"),
		verify,
		apiaryFinding("f8", "check", "suite", "open"),
		apiaryFinding("f9", "reviewer", "docs-reviewer", "open"),
		apiaryFinding("f10", "reviewer", "code-reviewer", "open", stored("closed_fixed", "code-reviewer", 2, "Fixed."), stored("reopened", "code-reviewer", 3, "")),
		apiaryFinding("f11", "reviewer", "code-reviewer", "fixed", person("dismissed_by_person"), stored("closed_fixed", "code-reviewer", 3, "Fixed.")),
		apiaryFinding("f12", "reviewer", "code-reviewer", "fixed", stored("closed_fixed", "code-reviewer", 2, "Fixed."), person("accepted")),
		apiaryFinding("f13", "reviewer", "code-reviewer", "dismissed", stored("closed_dismissed", "code-reviewer", 2, "No."), person("dismissed_by_person")),
		apiaryFinding("f14", "check", "code-reviewer", "open"),
	)
	data, err := previousFindings(4, "build", []string{"code-reviewer", "security-reviewer"}, got)
	if err != nil {
		t.Fatal(err)
	}
	round, findings := decodedFile(t, data)
	if round != 4 {
		t.Errorf("the round is %d", round)
	}
	if want := []string{"f1", "f2", "f3", "f6", "f10", "f11"}; !slices.Equal(ids(findings), want) {
		t.Errorf("the findings are %v, want %v: the roles' open findings and findings that a run closed, of the stage", ids(findings), want)
	}
	one := findings[0]
	wantOne := `{"id":"f1","stage":"build","source":{"kind":"reviewer","name":"code-reviewer"},"severity":"blocking","state":"open",` +
		`"quote":"quote f1","file":"a.go","line":3,"point":"point f1","problem":"problem f1","fix":"fix f1","proof":"proof f1",` +
		`"disputed":true,"replies":[{"kind":"fixed","reason":"Fixed in a1b2c3.","round":2},{"kind":"disputed","reason":"The input is checked.","round":3}]}`
	gotOne, _ := json.Marshal(one)
	var a, b any
	if err := json.Unmarshal(gotOne, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(wantOne), &b); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Errorf("the first finding is %s, want %s", gotOne, wantOne)
	}
	if !strings.Contains(string(data), `"id": "f1",`) || !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("the file is not indented JSON that ends with a line feed:\n%s", data)
	}
	// A finding with no file has no file and no line; a closed finding has no reply.
	if _, has := findings[1]["replies"].([]any); !has || len(findings[1]["replies"].([]any)) != 0 {
		t.Errorf("a finding with no reply has replies %v, want []", findings[1]["replies"])
	}

	if _, err := previousFindings(0, "build", nil, got); err == nil {
		t.Error("round 0 gives a file")
	}
	twice := listing(apiaryFinding("f1", "reviewer", "code-reviewer", "open"), apiaryFinding("f1", "reviewer", "code-reviewer", "open"))
	if _, err := previousFindings(1, "build", []string{"code-reviewer"}, twice); err == nil {
		t.Error("a GET that lists one id twice gives a file")
	}
	empty, err := previousFindings(1, "build", []string{"code-reviewer"}, listing())
	if err != nil || !strings.Contains(string(empty), `"findings": []`) {
		t.Errorf("no finding gives %s and %v, want an empty list", empty, err)
	}
}

func TestReviewLedgerBuilderFindings(t *testing.T) {
	text := apiaryFinding("f1", "reviewer", "code-reviewer", "open")
	text.Point = "a <b> & c"
	verify := apiaryFinding("f3", "check", "suite", "open")
	verify.Stage = "verify"
	nofile := apiaryFinding("f5", "criterion", "AC-1", "open")
	nofile.Place = contracts.FindingPlace{Quote: "q"}
	data, err := builderFindings(2, listing(
		text,
		apiaryFinding("f2", "reviewer", "code-reviewer", "fixed", stored("closed_fixed", "code-reviewer", 1, "Fixed.")),
		verify,
		apiaryFinding("f4", "reviewer", "code-reviewer", "accepted", person("accepted")),
		nofile,
	))
	if err != nil {
		t.Fatal(err)
	}
	round, findings := decodedFile(t, data)
	if round != 2 || !slices.Equal(ids(findings), []string{"f1", "f3", "f5"}) {
		t.Errorf("the round is %d and the findings are %v, want round 2 and the open findings of every stage and source", round, ids(findings))
	}
	if !strings.Contains(string(data), `"point": "a <b> & c"`) {
		t.Errorf("the file escapes <, > or &:\n%s", data)
	}
	if _, has := findings[2]["file"]; has {
		t.Errorf("a finding with no file has the member file: %v", findings[2])
	}
	if _, err := builderFindings(0, listing()); err == nil {
		t.Error("round 0 gives a file")
	}
}

// validateEvents checks events as the body of a findings post against the closed run
// contract, with the tests of qory's other writers.
func validateEvents(t *testing.T, bases []contracts.FindingBase, events []contracts.FindingsPostEvent) {
	t.Helper()
	posts, err := postBatches(bases, events)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := contracts.CompileRunClosed("findings-post.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range posts {
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(doc); err != nil {
			t.Errorf("the post does not match findings-post.schema.json: %v\n%s", err, body)
		}
	}
}

// report is a valid report of the role with the findings and closes given.
func ledgerReport(findings []reviewround.Finding, closes ...reviewround.Close) *reviewround.Report {
	return &reviewround.Report{Reviewer: "code-reviewer", Findings: findings, Closes: closes}
}

func newFinding(point string) reviewround.Finding {
	return reviewround.Finding{Severity: "blocking", Quote: "q " + point, Problem: "p", Fix: "f",
		Proof: "failing check: " + point, Point: point}
}

func TestReviewLedgerReportEvents(t *testing.T) {
	repeat := newFinding("again")
	repeat.ID = "f4"
	placed := newFinding("placed")
	placed.File, placed.Line = "src/a.go", 12
	lineOnly := newFinding("line only")
	lineOnly.Line = 7
	r := &refs{}
	got, err := ledgerWhere.reportsEvents(r, []roleReport{
		{Role: "code-reviewer", Report: ledgerReport(
			[]reviewround.Finding{newFinding("first"), repeat, placed, lineOnly},
			reviewround.Close{ID: "f5", State: reviewround.Fixed, Reason: "Fixed now."},
			reviewround.Close{ID: "f6", State: reviewround.Dismissed, Reason: "Not a defect."})},
		{Role: "security-reviewer", Report: &reviewround.Report{Reviewer: "security-reviewer",
			Findings: []reviewround.Finding{newFinding("second")}}},
	}, listing())
	if err != nil {
		t.Fatal(err)
	}
	target := ledgerTarget
	raise := func(ref, by, quote, file string, line int, point string) contracts.FindingsPostEvent {
		return contracts.FindingsPostEvent{Kind: "raise", By: by, Ref: ref, Target: &target, Stage: "build",
			Source: &contracts.FindingSource{Kind: "reviewer", Name: by}, Severity: "blocking",
			Place: &contracts.FindingPlace{Quote: quote, File: file, Line: line}, Point: point, Problem: "p", Fix: "f",
			Proof: "failing check: " + point}
	}
	want := []contracts.FindingsPostEvent{
		raise("r1", "code-reviewer", "q first", "", 0, "first"),
		{Kind: "repeat", By: "code-reviewer", ID: "f4"},
		raise("r2", "code-reviewer", "q placed", "src/a.go", 12, "placed"),
		raise("r3", "code-reviewer", "q line only", "", 0, "line only"),
		{Kind: "close_fixed", By: "code-reviewer", ID: "f5", Reason: "Fixed now."},
		{Kind: "close_dismissed", By: "code-reviewer", ID: "f6", Reason: "Not a defect."},
		raise("r4", "security-reviewer", "q second", "", 0, "second"),
	}
	g, _ := json.MarshalIndent(got, "", " ")
	w, _ := json.MarshalIndent(want, "", " ")
	if string(g) != string(w) {
		t.Errorf("the events are\n%s\nwant\n%s", g, w)
	}
	validateEvents(t, nil, got)
}

func TestReviewLedgerAMissingOrInvalidReportIsABlockingCheckFinding(t *testing.T) {
	why := "Reviewer code-reviewer wrote no report."
	missing := roleReport{Role: "code-reviewer", Why: why}
	// First round: no finding of the check yet.
	got, err := ledgerWhere.reportsEvents(&refs{}, []roleReport{missing}, listing())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != "raise" || got[0].By != "report-code-reviewer" || got[0].Severity != "blocking" ||
		got[0].Source.Kind != "check" || got[0].Source.Name != "report-code-reviewer" || got[0].Point != why ||
		got[0].Stage != "build" || got[0].Ref != "r1" || *got[0].Target != ledgerTarget {
		t.Errorf("the events are %+v", got)
	}
	validateEvents(t, nil, got)

	// The finding is open: a repeat, whichever other finding of the check there is.
	open := apiaryFinding("f8", "check", "report-code-reviewer", "open")
	closed := apiaryFinding("f7", "check", "report-code-reviewer", "fixed")
	elsewhere := apiaryFinding("f6", "check", "report-code-reviewer", "open")
	elsewhere.Stage = "verify"
	others := apiaryFinding("f5", "check", "report-security-reviewer", "open")
	got, err = ledgerWhere.reportsEvents(&refs{}, []roleReport{missing}, listing(open, closed, elsewhere, others))
	if err != nil {
		t.Fatal(err)
	}
	if want := []contracts.FindingsPostEvent{{Kind: "repeat", By: "report-code-reviewer", ID: "f8"}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the events are %+v, want %+v", got, want)
	}

	// The next valid report closes it, then the events of that report follow.
	valid := roleReport{Role: "code-reviewer", Report: ledgerReport(nil, reviewround.Close{ID: "f3", State: reviewround.Fixed, Reason: "Done."})}
	got, err = ledgerWhere.reportsEvents(&refs{}, []roleReport{valid}, listing(open, others))
	if err != nil {
		t.Fatal(err)
	}
	want := []contracts.FindingsPostEvent{
		{Kind: "close_fixed", By: "report-code-reviewer", ID: "f8", Reason: "The report of code-reviewer is valid."},
		{Kind: "close_fixed", By: "code-reviewer", ID: "f3", Reason: "Done."},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the events are %+v, want %+v", got, want)
	}
	validateEvents(t, nil, got)

	// A valid report with no open finding of the check writes no close.
	got, err = ledgerWhere.reportsEvents(&refs{}, []roleReport{valid}, listing())
	if err != nil || len(got) != 1 {
		t.Errorf("the events are %+v and the error %v", got, err)
	}
	// A cause with line breaks and more than 500 characters is one line of at most 500.
	long := roleReport{Role: "code-reviewer", Why: "a\nb​c " + strings.Repeat("x", 600)}
	got, err = ledgerWhere.reportsEvents(&refs{}, []roleReport{long}, listing())
	if err != nil || len([]rune(got[0].Point)) != 500 || strings.ContainsAny(got[0].Point, "\n​") {
		t.Errorf("the point is %q and the error %v", got[0].Point, err)
	}
	validateEvents(t, nil, got)
	// A cause of nothing still gives a finding with a point.
	got, _ = ledgerWhere.reportsEvents(&refs{}, []roleReport{{Role: "code-reviewer"}}, listing())
	if got[0].Point != "Reviewer code-reviewer wrote no valid report." {
		t.Errorf("the point is %q", got[0].Point)
	}
	if _, err := ledgerWhere.reportsEvents(&refs{}, nil, listing(open, open)); err == nil {
		t.Error("a GET that lists one id twice gives events")
	}
}

func TestReviewLedgerAReportIsCheckedAgainstTheFindingsOfItsRole(t *testing.T) {
	found := listing(
		apiaryFinding("f1", "reviewer", "code-reviewer", "open"),
		apiaryFinding("f2", "reviewer", "code-reviewer", "fixed"),
		apiaryFinding("f3", "reviewer", "security-reviewer", "open"),
	)
	repeat := func(id string) *reviewround.Report {
		f := newFinding("p")
		f.ID = id
		return ledgerReport([]reviewround.Finding{f})
	}
	for _, c := range []struct {
		name   string
		report *reviewround.Report
		err    error
		ok     bool
		why    string
	}{
		{"its open finding", repeat("f1"), nil, true, ""},
		{"its closed finding", repeat("f2"), nil, true, ""},
		{"another role's finding", repeat("f3"), nil, false, "Reviewer code-reviewer's report repeats f3, which is no finding of that reviewer."},
		{"a finding that is not there", ledgerReport(nil, reviewround.Close{ID: "f9", State: "fixed", Reason: "r"}), nil, false,
			"Reviewer code-reviewer's report closes f9, which is no finding of that reviewer."},
		{"one id repeated and closed", func() *reviewround.Report {
			r := repeat("f1")
			r.Closes = []reviewround.Close{{ID: "f1", State: "fixed", Reason: "r"}}
			return r
		}(), nil, false, "Reviewer code-reviewer's report both repeats and closes f1."},
		{"one id repeated twice", func() *reviewround.Report {
			r := repeat("f1")
			r.Findings = append(r.Findings, r.Findings[0])
			return r
		}(), nil, false, "Reviewer code-reviewer's report repeats f1 more than once."},
		{"one id closed twice", ledgerReport(nil, reviewround.Close{ID: "f1", State: "fixed", Reason: "r"}, reviewround.Close{ID: "f1", State: "dismissed", Reason: "r"}), nil, false,
			"Reviewer code-reviewer's report closes f1 more than once."},
		{"no report", &reviewround.Report{}, errors.New("Reviewer code-reviewer wrote no report."), false, "Reviewer code-reviewer wrote no report."},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := readRoleReport("code-reviewer", *c.report, c.err, found, "build")
			if (got.Report != nil) != c.ok || got.Why != c.why || got.Role != "code-reviewer" {
				t.Errorf("the role report is %+v, want ok %v and the cause %q", got, c.ok, c.why)
			}
		})
	}
	// Its finding of another stage is no finding of this round.
	other := apiaryFinding("f1", "reviewer", "code-reviewer", "open")
	other.Stage = "verify"
	if got := readRoleReport("code-reviewer", *repeat("f1"), nil, listing(other), "build"); got.Report != nil {
		t.Error("a repeat of a finding of another stage is valid")
	}
}

func TestReviewLedgerSuiteEvents(t *testing.T) {
	open := apiaryFinding("f4", "check", "suite", "open")
	closed := apiaryFinding("f3", "check", "suite", "fixed")
	accepted := apiaryFinding("f2", "check", "suite", "accepted")
	elsewhere := apiaryFinding("f1", "check", "suite", "open")
	elsewhere.Stage = "verify"
	reviewer := apiaryFinding("f5", "reviewer", "suite", "open")
	tail := "qory-suite-head abc\n--- FAIL: TestBatch\nFAIL"

	for _, state := range []suite.State{suite.Failed, suite.Timeout, suite.Stopped} {
		got, err := ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: state, Point: "2 of 140 tests failed.", Tail: tail}, listing(closed, accepted, elsewhere, reviewer))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Kind != "raise" || got[0].By != "suite" || got[0].Source.Kind != "check" ||
			got[0].Source.Name != "suite" || got[0].Severity != "blocking" || got[0].Point != "2 of 140 tests failed." ||
			got[0].Proof != tail || got[0].Place.Quote != "2 of 140 tests failed." || got[0].Stage != "build" {
			t.Errorf("%s: the events are %+v", state, got)
		}
		validateEvents(t, nil, got)
	}

	// Red again: a repeat of the open finding, no second raise.
	got, err := ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Failed, Point: "p", Tail: tail}, listing(open, closed))
	if err != nil || fmt.Sprint(got) != fmt.Sprint([]contracts.FindingsPostEvent{{Kind: "repeat", By: "suite", ID: "f4"}}) {
		t.Errorf("the events are %+v and the error %v", got, err)
	}
	// Green with one open: close_fixed. Green with none: nothing. Skipped: nothing.
	got, err = ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Passed}, listing(open, closed, elsewhere))
	want := []contracts.FindingsPostEvent{{Kind: "close_fixed", By: "suite", ID: "f4", Reason: "The full suite passed."}}
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the events are %+v and the error %v", got, err)
	}
	validateEvents(t, nil, got)
	if got, err := ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Passed}, listing(closed)); err != nil || got != nil {
		t.Errorf("a green suite with none open gives %+v and %v", got, err)
	}
	if got, err := ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Skipped}, listing(open)); err != nil || got != nil {
		t.Errorf("a skipped suite gives %+v and %v", got, err)
	}

	// The proof is the last 4000 characters of the tail, with no NUL; with no tail it is the
	// point; a point of two lines is one line.
	got, _ = ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Failed, Point: "line one\nline two", Tail: "x\x00" + strings.Repeat("y", 4000)}, listing())
	if got[0].Proof != strings.Repeat("y", 4000) || got[0].Point != "line one line two" {
		t.Errorf("the proof has %d characters, the point is %q", len([]rune(got[0].Proof)), got[0].Point)
	}
	got, _ = ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Failed, Point: "the suite did not start", Tail: " \n"}, listing())
	if got[0].Proof != "the suite did not start" {
		t.Errorf("the proof is %q", got[0].Proof)
	}
	got, _ = ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Failed}, listing())
	if got[0].Point == "" || got[0].Proof == "" || got[0].Place.Quote == "" {
		t.Errorf("a suite with no text gives %+v", got[0])
	}
	validateEvents(t, nil, got)
	if _, err := ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Passed}, listing(open, open)); err == nil {
		t.Error("a GET that lists one id twice gives events")
	}
}

func TestReviewLedgerReplyEvents(t *testing.T) {
	fresh := apiaryFinding("f1", "reviewer", "code-reviewer", "open")
	second := apiaryFinding("f2", "reviewer", "code-reviewer", "open", stored("repeated", "code-reviewer", 2, ""))
	answered := apiaryFinding("f3", "reviewer", "code-reviewer", "open", stored("replied_fixed", "implement-changes", 1, "Fixed."))
	again := apiaryFinding("f4", "reviewer", "code-reviewer", "open", stored("replied_fixed", "implement-changes", 1, "Fixed."), stored("repeated", "code-reviewer", 2, ""))
	reopened := apiaryFinding("f5", "reviewer", "code-reviewer", "open", stored("replied_disputed", "implement-changes", 1, "No."), stored("reopened", "code-reviewer", 2, ""))
	closed := apiaryFinding("f6", "reviewer", "code-reviewer", "fixed", stored("closed_fixed", "code-reviewer", 1, "Done."))
	// An open finding of another stage gets its reply: Qory Apiary accepts a reply to a
	// finding of any stage.
	elsewhere := apiaryFinding("f7", "reviewer", "code-reviewer", "open")
	elsewhere.Stage = "verify"
	found := listing(fresh, second, answered, again, reopened, closed, elsewhere)
	ledger := `{"steps":[
	  {"id":"plan","state":"done"},
	  {"id":"fix:f1","state":"done","reply":{"kind":"fixed","reason":"Fixed in one commit."}},
	  {"id":"fix:f2","state":"done","reply":{"kind":"disputed","reason":"The check is in the caller."}},
	  {"id":"fix:f3","state":"done","reply":{"kind":"fixed","reason":"Again."}},
	  {"id":"fix:f4","state":"done","reply":{"kind":"fixed","reason":"Fixed after the repeat."}},
	  {"id":"fix:f5","state":"done","reply":{"kind":"disputed","reason":"Still no."}},
	  {"id":"fix:f6","state":"done","reply":{"kind":"fixed","reason":"Closed already."}},
	  {"id":"fix:f7","state":"done","reply":{"kind":"fixed","reason":"Fixed in the stage build."}},
	  {"id":"fix:f77","state":"done","reply":{"kind":"fixed","reason":"No such finding."}},
	  {"id":"fix:f8","state":"running"},
	  {"id":"fix:slug-name","state":"done","reply":{"kind":"fixed","reason":"A fix with no finding."}},
	  {"id":"fix:f1","state":"done","pass":2,"reply":{"kind":"disputed","reason":"The last row wins."}},
	  {"id":"deliverable:d-1","state":"done"}
	]}`
	got, _, err := replyEvents([]byte(ledger), "implement-changes", found, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []contracts.FindingsPostEvent{
		{Kind: "reply_disputed", By: "implement-changes", ID: "f1", Reason: "The last row wins."},
		{Kind: "reply_disputed", By: "implement-changes", ID: "f2", Reason: "The check is in the caller."},
		{Kind: "reply_fixed", By: "implement-changes", ID: "f4", Reason: "Fixed after the repeat."},
		{Kind: "reply_disputed", By: "implement-changes", ID: "f5", Reason: "Still no."},
		{Kind: "reply_fixed", By: "implement-changes", ID: "f7", Reason: "Fixed in the stage build."},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the events are %+v, want %+v", got, want)
	}
	validateEvents(t, nil, got)

	none, _, err := replyEvents([]byte(`{"steps":[]}`), "implement-changes", found, nil)
	if err != nil || none != nil {
		t.Errorf("a ledger with no row gives %+v and %v", none, err)
	}
	long := strings.Repeat("r", 2001)
	for name, text := range map[string]string{
		"not JSON":          `{"steps":`,
		"a kind of another": `{"steps":[{"id":"fix:f1","reply":{"kind":"accepted","reason":"x"}}]}`,
		"no reason":         `{"steps":[{"id":"fix:f1","reply":{"kind":"fixed","reason":""}}]}`,
		"a long reason":     `{"steps":[{"id":"fix:f1","reply":{"kind":"fixed","reason":"` + long + `"}}]}`,
		"a NUL":             `{"steps":[{"id":"fix:f1","reply":{"kind":"fixed","reason":"a\u0000b"}}]}`,
	} {
		if got, _, err := replyEvents([]byte(text), "implement-changes", found, nil); err == nil {
			t.Errorf("%s: the events are %+v, want an error", name, got)
		} else if strings.Contains(err.Error(), "\n") || !strings.HasPrefix(err.Error(), "The ") {
			t.Errorf("%s: the error is %q, want one line that starts with the cause", name, err)
		}
	}
	// A bad reply of a row that is not a fix of a finding is not read.
	if _, _, err := replyEvents([]byte(`{"steps":[{"id":"fix:slug","reply":{"kind":"x","reason":""}}]}`), "implement-changes", found, nil); err != nil {
		t.Errorf("a reply of a row that fixes no finding is read: %v", err)
	}
	if _, _, err := replyEvents([]byte(ledger), "implement-changes", listing(fresh, fresh), nil); err == nil {
		t.Error("a GET that lists one id twice gives events")
	}
}

func TestReviewLedgerReplyOfTheHighestPass(t *testing.T) {
	// f3: fixed in pass 1 and replied, then the reviewer repeats it.
	repeated := apiaryFinding("f3", "reviewer", "code-reviewer", "open",
		stored("replied_fixed", "implement-changes", 1, "Fixed."), stored("repeated", "code-reviewer", 2, ""))
	// f4: the same, and the reply of pass 2 is stored.
	replied := apiaryFinding("f4", "reviewer", "code-reviewer", "open",
		stored("replied_fixed", "implement-changes", 1, "Fixed."), stored("repeated", "code-reviewer", 2, ""),
		stored("replied_fixed", "implement-changes", 2, "Fixed again."))
	found := listing(repeated, replied)

	// Both findings had pass 1 before the session that the ledger is of.
	since := map[string]int{"f3": 1, "f4": 1}

	// Pass 2 of fix:f3 failed with no reply: the reply of pass 1 is not about the repeat.
	failed := `{"steps":[
	  {"id":"fix:f3","pass":1,"state":"done","reply":{"kind":"fixed","reason":"Fixed."}},
	  {"id":"fix:f3","pass":2,"state":"failed"}
	]}`
	if got, _, err := replyEvents([]byte(failed), "implement-changes", found, since); err != nil || got != nil {
		t.Errorf("a failed pass 2 after a repeat gives %+v and %v, want none", got, err)
	}

	// Pass 2 ends with a reply: it is posted once, and not again once it is stored. The
	// reply of pass 1 is not read, whatever the order of the rows.
	for name, steps := range map[string]string{
		"in order": `{"id":"fix:f3","pass":1,"reply":{"kind":"fixed","reason":"Fixed."}},
		  {"id":"fix:f3","pass":2,"reply":{"kind":"disputed","reason":"The repeat is wrong."}}`,
		"pass 2 first": `{"id":"fix:f3","pass":2,"reply":{"kind":"disputed","reason":"The repeat is wrong."}},
		  {"id":"fix:f3","pass":1,"reply":{"kind":"fixed","reason":"Fixed."}}`,
	} {
		ledger := `{"steps":[` + steps + `,
		  {"id":"fix:f4","pass":1,"reply":{"kind":"fixed","reason":"Fixed."}},
		  {"id":"fix:f4","pass":2,"reply":{"kind":"fixed","reason":"Fixed again."}}]}`
		got, _, err := replyEvents([]byte(ledger), "implement-changes", found, since)
		want := []contracts.FindingsPostEvent{
			{Kind: "reply_disputed", By: "implement-changes", ID: "f3", Reason: "The repeat is wrong."},
		}
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: the events are %+v and the error %v, want %+v", name, got, err, want)
		}
	}

	// A pass 2 with a bad reply is an error; a bad reply of the pass 1 that pass 2 replaces
	// is not read.
	bad := `{"steps":[{"id":"fix:f3","pass":1,"reply":{"kind":"x","reason":"y"}},
	  {"id":"fix:f3","pass":2,"reply":{"kind":"fixed","reason":"Fixed."}}]}`
	if got, _, err := replyEvents([]byte(bad), "implement-changes", found, since); err != nil || len(got) != 1 {
		t.Errorf("a bad reply of a replaced pass gives %+v and %v, want one event", got, err)
	}
	bad = `{"steps":[{"id":"fix:f3","pass":1,"reply":{"kind":"fixed","reason":"Fixed."}},
	  {"id":"fix:f3","pass":2,"reply":{"kind":"x","reason":"y"}}]}`
	if _, _, err := replyEvents([]byte(bad), "implement-changes", found, since); err == nil {
		t.Error("a bad reply of the highest pass gives no error")
	}

	// With no since, both passes are read: the higher pass replaces the lower, whatever the
	// order of the rows, so the bad kind of pass 1 gives no error and the bad kind of
	// pass 2 gives one.
	const pass1Bad = `{"id":"fix:f3","pass":1,"reply":{"kind":"x","reason":"y"}}`
	const pass2Good = `{"id":"fix:f3","pass":2,"reply":{"kind":"disputed","reason":"The repeat is wrong."}}`
	const pass1Good = `{"id":"fix:f3","pass":1,"reply":{"kind":"fixed","reason":"Fixed."}}`
	const pass2Bad = `{"id":"fix:f3","pass":2,"reply":{"kind":"x","reason":"y"}}`
	for name, steps := range map[string]string{"in order": pass1Bad + "," + pass2Good, "pass 2 first": pass2Good + "," + pass1Bad} {
		got, _, err := replyEvents([]byte(`{"steps":[`+steps+`]}`), "implement-changes", found, nil)
		if err != nil || len(got) != 1 || got[0].Kind != "reply_disputed" {
			t.Errorf("no since, bad pass 1, %s: the events are %+v and the error %v, want the reply of pass 2", name, got, err)
		}
	}
	for name, steps := range map[string]string{"in order": pass1Good + "," + pass2Bad, "pass 2 first": pass2Bad + "," + pass1Good} {
		if _, _, err := replyEvents([]byte(`{"steps":[`+steps+`]}`), "implement-changes", found, nil); err == nil {
			t.Errorf("no since, bad pass 2, %s: no error, want the bad kind of the highest pass", name)
		}
	}
}

func TestReviewLedgerRepliesOfEarlierSessions(t *testing.T) {
	repeated := apiaryFinding("f3", "reviewer", "code-reviewer", "open",
		stored("replied_fixed", "implement-changes", 1, "Fixed."), stored("repeated", "code-reviewer", 2, ""))
	found := listing(repeated)
	const pass1 = `{"id":"fix:f3","pass":1,"reply":{"kind":"fixed","reason":"Fixed."}}`
	const pass2 = `{"id":"fix:f3","pass":2,"reply":{"kind":"disputed","reason":"The repeat is wrong."}}`
	pass2Event := contracts.FindingsPostEvent{Kind: "reply_disputed", By: "implement-changes", ID: "f3", Reason: "The repeat is wrong."}
	pass1Event := contracts.FindingsPostEvent{Kind: "reply_fixed", By: "implement-changes", ID: "f3", Reason: "Fixed."}
	for _, c := range []struct {
		name  string
		steps string
		since map[string]int
		want  []contracts.FindingsPostEvent
	}{
		{"pass 1 only after pass 1 ran", pass1, map[string]int{"f3": 1}, nil},
		{"pass 2 with a reply after pass 1 ran", pass1 + "," + pass2, map[string]int{"f3": 1}, []contracts.FindingsPostEvent{pass2Event}},
		{"pass 2 with no reply after pass 1 ran", pass1 + `,{"id":"fix:f3","pass":2,"state":"failed"}`, map[string]int{"f3": 1}, nil},
		{"pass 2 after pass 2 ran", pass1 + "," + pass2, map[string]int{"f3": 2}, nil},
		{"another finding ran before", pass1, map[string]int{"f4": 1}, []contracts.FindingsPostEvent{pass1Event}},
		{"no pass written before", pass1, nil, []contracts.FindingsPostEvent{pass1Event}},
		{"no pass in the row, one before", `{"id":"fix:f3","reply":{"kind":"fixed","reason":"Fixed."}}`, map[string]int{"f3": 0}, nil},
	} {
		got, _, err := replyEvents([]byte(`{"steps":[`+c.steps+`]}`), "implement-changes", found, c.since)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: the events are %+v and the error %v, want %+v", c.name, got, err, c.want)
		}
	}
}

// TestASignedURLAtTheCutOfTheSuiteTailHasNoPartInThePostedText is a red suite whose tail
// has a signed URL: the last 4000 characters of the unmasked tail start 20 characters
// into its query value, so a cut before the masking leaves the end of the value without
// the name of its query parameter.
func TestASignedURLAtTheCutOfTheSuiteTailHasNoPartInThePostedText(t *testing.T) {
	const value = "Zq7mK2vXr9TnB4wLp8Yc3HdJ6sFg1UeAo5NitQw0Rx"
	head := "FAIL https://blob.example/a?X-Amz-Security-Token="
	rest := value + "&b=2\n"
	tail := head + rest + strings.Repeat("z", 4020-len(rest))
	if len(tail) != 4000+len(head)+20 {
		t.Fatalf("the tail has %d characters, want 4000 and the cut 20 characters into the value", len(tail))
	}
	events, err := ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Failed, Point: "The suite failed.", Tail: tail}, listing())
	if err != nil || len(events) != 1 {
		t.Fatalf("the suite gives %d events and the error %v, want one event", len(events), err)
	}
	posted, mErr := json.Marshal(maskedEvents(events))
	if mErr != nil {
		t.Fatal(mErr)
	}
	for i := 0; i+6 <= len(value); i++ {
		if strings.Contains(string(posted), value[i:i+6]) {
			t.Errorf("the posted text has %q of the query value: %s", value[i:i+6], posted)
		}
	}
}

func TestAKeyInABadProofDoesNotReachThePostedText(t *testing.T) {
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	proof := "nothing: " + strings.Repeat("x", 230) + key
	data := `{"reviewer":"code-reviewer","findings":[{"severity":"non-blocking","quote":"q","problem":"p","fix":"f","proof":"` +
		proof + `","point":"p"}]}`
	report, err := reviewround.Parse("code-reviewer", []byte(data), maskText)
	events, evErr := ledgerWhere.reportsEvents(&refs{}, []roleReport{readRoleReport("code-reviewer", report, err, listing(), "build")}, listing())
	if err == nil || evErr != nil || len(events) != 1 {
		t.Fatalf("the report gives the error %v, and %d events with the error %v, want an error and one event", err, len(events), evErr)
	}
	posted, mErr := json.Marshal(maskedEvents(events))
	if mErr != nil {
		t.Fatal(mErr)
	}
	for _, part := range []string{"sk-ant", "abcdefghij", "xxxxxxxxxx"} {
		if strings.Contains(string(posted), part) {
			t.Errorf("the posted text has %q: %s", part, posted)
		}
	}
}

// TestASecretAcrossTheCutOfAgentTextHasNoPartInThePostedText is a secret of 37 characters
// in the run's masker, in four texts that qory cuts before it posts them: the name of a
// member of a report, which the error of the report quotes, the cause of a report that has
// no valid report, the point of the full suite, and the kind of a reply of the build
// ledger.
func TestASecretAcrossTheCutOfAgentTextHasNoPartInThePostedText(t *testing.T) {
	const secret = "Zq7mK2vXr9TnB4wLp8Yc3HdJ6sFg1UeAo5Nit"
	if len(secret) != 37 {
		t.Fatalf("the secret has %d characters, want 37", len(secret))
	}
	set := secretmask.New()
	set.Add(secret)
	defer secretmask.SetDefault(secretmask.SetDefault(set))
	noPart := func(what, text string) {
		t.Helper()
		for n := 4; n <= len(secret); n++ {
			if strings.Contains(text, secret[:n]) {
				t.Errorf("%s has the first %d characters of the secret: %s", what, n, text)
				return
			}
		}
	}

	member := `{"reviewer":"code-reviewer","findings":[],"` + strings.Repeat("a", 250) + secret + `":1}`
	report, err := reviewround.Parse("code-reviewer", []byte(member), maskText)
	events, evErr := ledgerWhere.reportsEvents(&refs{}, []roleReport{readRoleReport("code-reviewer", report, err, listing(), "build")}, listing())
	if err == nil || evErr != nil || len(events) != 1 {
		t.Fatalf("the report gives the error %v, and %d events with the error %v, want an error and one event", err, len(events), evErr)
	}
	posted, mErr := json.Marshal(maskedEvents(events))
	if mErr != nil {
		t.Fatal(mErr)
	}
	noPart("the posted member name", string(posted))

	// A cause that is not from reviewround, 490 characters before the secret, is cut at 500.
	why := roleReport{Role: "code-reviewer", Why: strings.Repeat("a", 490) + secret}
	events = ledgerWhere.reportEvents(&refs{}, why, listing())
	if posted, err = json.Marshal(maskedEvents(events)); err != nil {
		t.Fatal(err)
	}
	noPart("the posted cause", string(posted))

	// The point of a suite, 490 characters before the secret, is cut at 500.
	events, err = ledgerWhere.suiteEvents(&refs{}, suiteEnding{State: suite.Failed, Point: strings.Repeat("a", 490) + secret}, listing())
	if err != nil || len(events) != 1 {
		t.Fatalf("the suite gives %d events and the error %v, want one event", len(events), err)
	}
	if posted, err = json.Marshal(maskedEvents(events)); err != nil {
		t.Fatal(err)
	}
	noPart("the posted point of the suite", string(posted))

	found := listing(apiaryFinding("f3", "reviewer", "code-reviewer", "open", stored("raised", "code-reviewer", 1, "")))
	_, _, err = replyEvents([]byte(`{"steps":[{"id":"fix:f3","pass":1,"reply":{"kind":"`+strings.Repeat("k", 10)+secret+`","reason":"x"}}]}`),
		"implement-changes", found, nil)
	if err == nil {
		t.Fatal("a reply of another kind gives no error")
	}
	noPart("the error for the kind of a reply", err.Error())
}

func TestReviewLedgerPasses(t *testing.T) {
	got, err := ledgerPasses([]byte(`{"steps":[{"id":"plan","pass":4},{"id":"fix:f3","pass":2},{"id":"fix:f3","pass":1},
	  {"id":"fix:f5"},{"id":"fix:slug","pass":3},{"id":"fix:f12","pass":1,"reply":{"kind":"fixed","reason":"x"}}]}`))
	if want := map[string]int{"f3": 2, "f5": 0, "f12": 1}; err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the passes are %v and the error %v, want %v", got, err, want)
	}
	if got, err := ledgerPasses([]byte(`{"steps":[]}`)); err != nil || len(got) != 0 {
		t.Errorf("a ledger with no row gives %v and %v, want none", got, err)
	}
	// A key is read by its exact name: "Steps" is not "steps".
	if got, err := ledgerPasses([]byte(`{"Steps":[{"id":"fix:f3","pass":2}]}`)); err != nil || len(got) != 0 {
		t.Errorf("a ledger with the key Steps gives %v and %v, want none", got, err)
	}
	if got, _, err := replyEvents([]byte(`{"Steps":[{"id":"fix:f3","pass":1,"reply":{"kind":"fixed","reason":"x"}}]}`), "implement-changes",
		listing(apiaryFinding("f3", "reviewer", "code-reviewer", "open", stored("raised", "code-reviewer", 1, ""))), nil); err != nil || got != nil {
		t.Errorf("a ledger with the key Steps gives the events %v and %v, want none", got, err)
	}
	// A key is read by its exact name: "ID" is not "id".
	if got, err := ledgerPasses([]byte(`{"steps":[{"ID":"fix:f3","Pass":2}]}`)); err != nil || len(got) != 0 {
		t.Errorf("a row with keys of other case gives %v and %v, want none", got, err)
	}
	if _, err := ledgerPasses([]byte(`{"steps":`)); err == nil || !strings.HasPrefix(err.Error(), "The build ledger is not the JSON") {
		t.Errorf("a ledger that is not JSON gives the error %v", err)
	}
}

// TestReviewLedgerPassesOutsideTheHarnessLimit is a row with a pass that the format of
// the build ledger does not allow: 1 to 100, and 0 for no pass in the row. A pass above
// 100 would make every reply of the step read as an earlier session's.
func TestReviewLedgerPassesOutsideTheHarnessLimit(t *testing.T) {
	for _, c := range []struct {
		pass string
		ok   bool
	}{{"0", true}, {"1", true}, {"100", true}, {"101", false}, {"999999999", false}, {"-1", false}} {
		got, err := ledgerPasses([]byte(`{"steps":[{"id":"fix:f1","pass":` + c.pass + `}]}`))
		if (err == nil) != c.ok {
			t.Errorf("the pass %s gives %v and the error %v, want ok %v", c.pass, got, err, c.ok)
		}
	}
	// A row of another kind of step has the limit too: the document is not the harness's.
	if _, err := ledgerPasses([]byte(`{"steps":[{"id":"plan","pass":101}]}`)); err == nil {
		t.Error("a row of the step plan with the pass 101 gives no error")
	}
}

// TestAnUnreadableLedgerFileIsNotReadFromTheServer is a version of the build ledger that
// qory cannot read by its list of files: no file, two files, one over 4 MiB, or a name
// that is no file name. [readLedger] stops it before any download, so it needs no client.
// The check of each case states its cause, and line 1 of each stop has at most 100
// characters with "qory run: ". A bad name is cut at 100 characters in the check and at
// 200 on line 2 of the stop, each with "...".
func TestAnUnreadableLedgerFileIsNotReadFromTheServer(t *testing.T) {
	one := runapi.File{Filename: "build-ledger.json", Size: 10}
	const so = ", so the session did not start"
	const badName = "The newest build ledger has a file name that is not allowed" + so
	for name, c := range map[string]struct {
		v           runapi.Version
		check, stop string
		line2Prefix string
	}{
		"no file": {runapi.Version{ID: "av_1"}, "The newest build ledger has 0 files, and the harness writes one",
			"The newest build ledger has 0 files, and the harness writes one" + so, "A build ledger is one file"},
		"two files": {runapi.Version{ID: "av_1", Files: []runapi.File{one, one}}, "The newest build ledger has 2 files, and the harness writes one",
			"The newest build ledger has 2 files, and the harness writes one" + so, "A build ledger is one file"},
		"too large": {runapi.Version{ID: "av_1", Files: []runapi.File{{Filename: "build-ledger.json", Size: maxLedger + 1}}}, "The newest build ledger file has 4194305 bytes, more than 4 MiB",
			"The newest build ledger file has 4194305 bytes, more than 4 MiB" + so, "A build ledger is one file"},
		"a bad name": {runapi.Version{ID: "av_1", Files: []runapi.File{{Filename: "../x.json", Size: 10}}}, `The newest build ledger has the file name "../x.json", which is not allowed`,
			badName, `The file name is "../x.json", and the version is av_1`},
		"a long bad name": {runapi.Version{ID: "av_1", Files: []runapi.File{{Filename: "../" + strings.Repeat("n", 300), Size: 10}}},
			`The newest build ledger has the file name "../` + strings.Repeat("n", 97) + `...", which is not allowed`,
			badName, `The file name is "../` + strings.Repeat("n", 197) + `...", and the version is av_1`},
	} {
		data, bad, err := readLedger(t.Context(), nil, &c.v, "the session did not start")
		var unread ledgerUnread
		if data != nil || err != nil || !errors.As(bad, &unread) || unread.check.Error() != c.check {
			t.Errorf("%s: readLedger gives %q, %v and %v, want the stop with the check %q", name, data, bad, err, c.check)
			continue
		}
		// reviewStop moves ", so" and what follows to line 2 when line 1 would be over 100.
		lines := strings.Split(bad.Error(), "\n")
		if first := "qory run: " + lines[0]; len(first) > 100 {
			t.Errorf("%s: line 1 has %d characters with qory run:, want at most 100: %q", name, len(first), first)
		}
		shown := strings.Replace(bad.Error(), ",\n  so ", ", so ", 1)
		stop, rest, _ := strings.Cut(shown, "\n")
		if stop != c.stop || !strings.HasPrefix(rest, "  "+c.line2Prefix) {
			t.Errorf("%s: the stop is %q, want %q with line 2 that starts %q", name, shown, c.stop, c.line2Prefix)
		}
	}
}

// TestReviewLedgerHiddenReplies is the number of replies that a since above each pass of
// the ledger keeps out: the ledger of an earlier session has a pass at or above since, so
// a cumulative ledger and a ledger that is read with no since hide nothing.
func TestReviewLedgerHiddenReplies(t *testing.T) {
	open := apiaryFinding("f3", "reviewer", "code-reviewer", "open", stored("raised", "code-reviewer", 1, ""))
	replied := apiaryFinding("f3", "reviewer", "code-reviewer", "open", stored("raised", "code-reviewer", 1, ""),
		stored("replied_fixed", "implement-changes", 1, "Fixed."))
	closed := apiaryFinding("f3", "reviewer", "code-reviewer", "fixed", stored("raised", "code-reviewer", 1, ""))
	const reply1 = `{"id":"fix:f3","pass":1,"reply":{"kind":"fixed","reason":"Fixed."}}`
	const reply2 = `{"id":"fix:f3","pass":2,"reply":{"kind":"fixed","reason":"Fixed."}}`
	for _, c := range []struct {
		name  string
		steps string
		found contracts.Findings
		since map[string]int
		want  int
	}{
		{"a pass 100 before a ledger of pass 1", reply1, listing(open), map[string]int{"f3": 100}, 1},
		{"a row with no pass", `{"id":"fix:f3","reply":{"kind":"fixed","reason":"Fixed."}}`, listing(open), map[string]int{"f3": 5}, 1},
		{"the same pass", reply1, listing(open), map[string]int{"f3": 1}, 0},
		{"a row of a later pass", reply1 + "," + reply2, listing(open), map[string]int{"f3": 1}, 0},
		{"a cumulative ledger after a failed pass", reply1 + `,{"id":"fix:f3","pass":2,"state":"failed"}`, listing(open), map[string]int{"f3": 2}, 0},
		{"a since of another finding", reply1, listing(open), map[string]int{"f4": 100}, 0},
		{"no since", reply1, listing(open), nil, 0},
		{"a finding that a reply follows", reply1, listing(replied), map[string]int{"f3": 100}, 0},
		{"a finding that is not open", reply1, listing(closed), map[string]int{"f3": 100}, 0},
		{"no finding", reply1, listing(), map[string]int{"f3": 100}, 0},
		{"a row with no reply", `{"id":"fix:f3","pass":1}`, listing(open), map[string]int{"f3": 100}, 0},
		{"a ledger that qory could not read", reply1, listing(open), map[string]int{unknownPasses: 0, "f3": 100}, 0},
	} {
		got, hidden, err := replyEvents([]byte(`{"steps":[`+c.steps+`]}`), "implement-changes", c.found, c.since)
		if err != nil || hidden != c.want || len(got) != 0 && c.want != 0 {
			t.Errorf("%s: %d hidden replies, %+v and the error %v, want %d hidden", c.name, hidden, got, err, c.want)
		}
	}
}

// TestReviewLedgerRepliesOfAnUnknownSince is a since for a ledger that qory could not
// read: no reply is posted, whatever the rows are, and the rows are not read.
func TestReviewLedgerRepliesOfAnUnknownSince(t *testing.T) {
	found := listing(apiaryFinding("f3", "reviewer", "code-reviewer", "open", stored("raised", "code-reviewer", 1, "")))
	ledger := `{"steps":[{"id":"fix:f3","pass":1,"reply":{"kind":"fixed","reason":"Fixed."}}]}`
	got, _, err := replyEvents([]byte(ledger), "implement-changes", found, map[string]int{unknownPasses: 0})
	if err != nil || got != nil {
		t.Errorf("an unknown since gives %+v and %v, want none", got, err)
	}
	if got, _, err = replyEvents([]byte(ledger), "implement-changes", found, nil); err != nil || len(got) != 1 {
		t.Errorf("no since gives %+v and %v, want the reply", got, err)
	}
}

func TestReviewLedgerBases(t *testing.T) {
	const base = "0123456789abcdef0123456789abcdef01234567"
	web := preparation.Repository{System: "forge", Path: "acme/shop"}
	elsewhere := preparation.Repository{System: "forge", Path: "acme/other"}
	moved := preparation.Landed{System: "forge", Path: "acme/shop", Commit: base}
	otherLanded := preparation.Landed{System: "forge", Path: "acme/other", Commit: base}
	haveBase := []contracts.FindingBase{{Target: ledgerTarget, Base: base}}
	justCollection := contracts.Target{Collection: "R_2002"}
	withBase := []contracts.FindingBase{{Target: contracts.Target{Collection: "R_2002"}, Base: "x"}}
	otherRepository := []contracts.FindingBase{{Target: contracts.Target{Collection: ledgerTarget.Collection,
		Address: &contracts.TargetAddress{System: "forge", Path: "acme/other"}}, Base: "x"}}
	sameByValue := []contracts.FindingBase{{Target: contracts.Target{Collection: ledgerTarget.Collection,
		Address: &contracts.TargetAddress{System: "forge", Path: "acme/shop"}}, Base: "x"}}
	for _, c := range []struct {
		name    string
		had     []contracts.FindingBase
		target  contracts.Target
		base    string
		version []preparation.Repository
		pushed  []preparation.Landed
		send    bool
	}{
		{"the first run of a take in", nil, ledgerTarget, base, nil, nil, true},
		{"other repositories only", nil, ledgerTarget, base, []preparation.Repository{elsewhere}, []preparation.Landed{otherLanded}, true},
		{"a base is stored", haveBase, ledgerTarget, base, nil, nil, false},
		{"a base of another target is stored", withBase, ledgerTarget, base, nil, nil, true},
		{"a base of another repository of the collection is stored", otherRepository, ledgerTarget, base, nil, nil, true},
		{"a base of the same address, another pointer, is stored", sameByValue, ledgerTarget, base, nil, nil, false},
		{"the run started at a version of the target", nil, ledgerTarget, base, []preparation.Repository{elsewhere, web}, nil, false},
		{"the run started at a pushed commit of the target", nil, ledgerTarget, base, nil, []preparation.Landed{otherLanded, moved}, false},
		{"a Verify round has no base", nil, ledgerTarget, "", nil, nil, false},
		{"a target with no address", nil, justCollection, base, []preparation.Repository{web}, []preparation.Landed{moved}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := basesToSend(c.had, c.target, c.base, c.version, c.pushed)
			if c.send {
				if len(got) != 1 || got[0].Base != c.base || fmt.Sprint(got[0].Target) != fmt.Sprint(c.target) {
					t.Errorf("the bases are %+v, want the base of the target", got)
				}
			} else if got != nil {
				t.Errorf("the bases are %+v, want none", got)
			}
		})
	}
}

// TestFullWindowStopsAtTheMostThatAGetLists: a list of 499 findings is read, and a list of
// 500 stops with the cause first, the caller's end of the line, and the code is used.
func TestFullWindowStopsAtTheMostThatAGetLists(t *testing.T) {
	list := func(n int) contracts.Findings {
		var f []contracts.Finding
		for i := range n {
			f = append(f, apiaryFinding(fmt.Sprintf("f%d", i+1), "reviewer", "code-reviewer", "open"))
		}
		return listing(f...)
	}
	if err := fullWindow(list(499), "the review did not end"); err != nil {
		t.Errorf("499 findings stop the run: %v", err)
	}
	err := fullWindow(list(500), "the review did not end")
	want := "The GET of the findings lists 500, its limit, so the review did not end\n  The work item may have more findings, which the list does not show" + codeUsed
	if err == nil || err.Error() != want {
		t.Errorf("500 findings give %v, want %q", err, want)
	}
}

// TestLedgerBaseIsOfTheWholeTarget: two repositories of one collection have two targets,
// so the base of one is not the base of the other.
func TestLedgerBaseIsOfTheWholeTarget(t *testing.T) {
	other := contracts.Target{Collection: ledgerTarget.Collection, Address: &contracts.TargetAddress{System: "forge", Path: "acme/other"}}
	copyOfTarget := contracts.Target{Collection: ledgerTarget.Collection, Address: &contracts.TargetAddress{System: "forge", Path: "acme/shop"}}
	found := contracts.Findings{Bases: []contracts.FindingBase{{Target: other, Base: "other-base"}}}
	if got := ledgerBase(found, ledgerTarget); got != "" {
		t.Errorf("the base of acme/shop is %q, want none: the work item has the base of acme/other", got)
	}
	found.Bases = append(found.Bases, contracts.FindingBase{Target: copyOfTarget, Base: "shop-base"})
	if got := ledgerBase(found, ledgerTarget); got != "shop-base" {
		t.Errorf("the base of acme/shop is %q, want shop-base", got)
	}
	if got := ledgerBase(found, other); got != "other-base" {
		t.Errorf("the base of acme/other is %q, want other-base", got)
	}
	if got := ledgerBase(found, contracts.Target{Collection: ledgerTarget.Collection}); got != "" {
		t.Errorf("the base of a target with no address is %q, want none", got)
	}
}

func TestReviewLedgerBatches(t *testing.T) {
	event := func(i int) contracts.FindingsPostEvent {
		return contracts.FindingsPostEvent{Kind: "close_fixed", By: "code-reviewer", ID: fmt.Sprintf("f%d", i+1), Reason: "Done."}
	}
	events := func(n int) []contracts.FindingsPostEvent {
		var out []contracts.FindingsPostEvent
		for i := range n {
			out = append(out, event(i))
		}
		return out
	}
	bases := []contracts.FindingBase{{Target: ledgerTarget, Base: "0123456789abcdef0123456789abcdef01234567"}}

	posts, err := postBatches(bases, events(250))
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	var all []contracts.FindingsPostEvent
	for i, p := range posts {
		sizes = append(sizes, len(p.Events))
		all = append(all, p.Events...)
		if (len(p.Bases) == 1) != (i == 0) {
			t.Errorf("batch %d has %d bases, want them in the first batch only", i+1, len(p.Bases))
		}
	}
	if !slices.Equal(sizes, []int{100, 100, 50}) || fmt.Sprint(all) != fmt.Sprint(events(250)) {
		t.Errorf("the batches have %v events, in order %v", sizes, fmt.Sprint(all) == fmt.Sprint(events(250)))
	}
	if posts, err := postBatches(nil, events(100)); err != nil || len(posts) != 1 {
		t.Errorf("100 events give %d batches and %v", len(posts), err)
	}
	if posts, err := postBatches(bases, nil); err != nil || posts != nil {
		t.Errorf("no event gives %v and %v", posts, err)
	}

	// Body size: events of 1 MiB each go 7 to a batch, whatever their count.
	big := func(i int) contracts.FindingsPostEvent {
		e := event(i)
		e.Reason = strings.Repeat("r", 1<<20)
		return e
	}
	var bigEvents []contracts.FindingsPostEvent
	for i := range 20 {
		bigEvents = append(bigEvents, big(i))
	}
	posts, err = postBatches(bases, bigEvents)
	if err != nil {
		t.Fatal(err)
	}
	var counts []int
	for _, p := range posts {
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) >= 8<<20 {
			t.Errorf("a batch has %d bytes", len(body))
		}
		counts = append(counts, len(p.Events))
	}
	if !slices.Equal(counts, []int{7, 7, 6}) {
		t.Errorf("the batches have %v events, want [7 7 6]", counts)
	}
	huge := event(0)
	huge.Reason = strings.Repeat("r", 9<<20)
	if _, err := postBatches(nil, []contracts.FindingsPostEvent{huge}); !errors.Is(err, errBodyTooLarge) {
		t.Errorf("an event of 9 MiB gives %v", err)
	}
	// A ref twice in one batch is the contract's refusal; in two batches it is allowed.
	twice := []contracts.FindingsPostEvent{{Kind: "raise", By: "a", Ref: "r1"}, {Kind: "raise", By: "a", Ref: "r1"}}
	if _, err := postBatches(nil, twice); err == nil {
		t.Error("a ref twice in a batch is accepted")
	}
}

// batchOf is a post with the events.
func batchOf(events ...contracts.FindingsPostEvent) contracts.FindingsPost {
	return contracts.FindingsPost{Events: events}
}

func TestReviewLedgerFindingsStored(t *testing.T) {
	raise := contracts.FindingsPostEvent{Kind: "raise", By: "code-reviewer", Ref: "r1", Point: "point f9", Proof: "proof f9",
		Place: &contracts.FindingPlace{Quote: "quote f9", File: "a.go", Line: 3}}
	raised := func(id string) contracts.Finding {
		f := apiaryFinding(id, "reviewer", "code-reviewer", "open")
		f.Point, f.Proof = "point f9", "proof f9"
		f.Place = contracts.FindingPlace{Quote: "quote f9", File: "a.go", Line: 3}
		return f
	}
	base := listing(apiaryFinding("f1", "reviewer", "code-reviewer", "open", stored("repeated", "code-reviewer", 1, "")))
	repeat := contracts.FindingsPostEvent{Kind: "repeat", By: "code-reviewer", ID: "f1"}

	var full []contracts.Finding
	for i := range 500 {
		full = append(full, apiaryFinding(fmt.Sprintf("f%d", i+1), "reviewer", "code-reviewer", "open"))
	}
	hundred := apiaryFinding("f1", "reviewer", "code-reviewer", "open")
	for range 99 {
		hundred.Events = append(hundred.Events, stored("replied_fixed", "implement-changes", 1, "r"))
	}
	hundredMore := hundred
	hundredMore.Events = append(slices.Clone(hundred.Events[1:]), stored("repeated", "code-reviewer", 2, ""))

	plain := func(id, kind, by string, events ...contracts.FindingEvent) contracts.Finding {
		return apiaryFinding(id, kind, by, "open", events...)
	}
	for _, c := range []struct {
		name          string
		batch         contracts.FindingsPost
		before, after contracts.Findings
		stored        bool
		stop          bool
	}{
		{"a raise that is stored", batchOf(raise), base, listing(base.Findings[0], raised("f2")), true, false},
		{"a raise that is not stored", batchOf(raise), base, base, false, false},
		{"a raise whose finding is another's", batchOf(raise), base,
			listing(base.Findings[0], func() contracts.Finding { f := raised("f2"); f.Point = "another"; return f }()), false, false},
		{"a raise by another actor", batchOf(raise), base,
			listing(base.Findings[0], func() contracts.Finding {
				f := raised("f2")
				f.Events = []contracts.FindingEvent{stored("raised", "security-reviewer", 1, "")}
				return f
			}()), false, false},
		{"a raise of another place", batchOf(raise), base,
			listing(base.Findings[0], func() contracts.Finding { f := raised("f2"); f.Place.Line = 4; return f }()), false, false},
		{"a raise of another proof", batchOf(raise), base,
			listing(base.Findings[0], func() contracts.Finding { f := raised("f2"); f.Proof = "another"; return f }()), false, false},
		{"a raise of another quote", batchOf(raise), base,
			listing(base.Findings[0], func() contracts.Finding { f := raised("f2"); f.Place.Quote = "another"; return f }()), false, false},
		{"a raise of another file", batchOf(raise), base,
			listing(base.Findings[0], func() contracts.Finding { f := raised("f2"); f.Place.File = "b.go"; return f }()), false, false},
		{"a finding that was there before", batchOf(raise), listing(raised("f2")), listing(raised("f2")), false, false},
		{"two identical raises, both stored", batchOf(raise, raise), base, listing(base.Findings[0], raised("f2"), raised("f3")), true, false},
		{"a repeat that is stored", batchOf(repeat), base,
			listing(plain("f1", "reviewer", "code-reviewer", stored("repeated", "code-reviewer", 1, ""), stored("repeated", "code-reviewer", 2, ""))), true, false},
		{"a repeat that is not stored, though one is there from the round before", batchOf(repeat), base, base, false, false},
		{"a repeat that reopened the finding", batchOf(repeat), base,
			listing(plain("f1", "reviewer", "code-reviewer", stored("repeated", "code-reviewer", 1, ""), stored("reopened", "code-reviewer", 2, ""))), true, false},
		{"a repeat that a person's close kept closed", batchOf(repeat), listing(apiaryFinding("f1", "reviewer", "code-reviewer", "accepted", person("accepted"))),
			listing(apiaryFinding("f1", "reviewer", "code-reviewer", "accepted", person("accepted"), stored("repeated_after_person_close", "code-reviewer", 2, ""))), true, false},
		{"a repeat by another actor", batchOf(repeat), base,
			listing(plain("f1", "reviewer", "code-reviewer", stored("repeated", "code-reviewer", 1, ""), stored("repeated", "security-reviewer", 2, ""))), false, false},
		{"a reply that is stored", batchOf(contracts.FindingsPostEvent{Kind: "reply_fixed", By: "implement-changes", ID: "f1"}), base,
			listing(plain("f1", "reviewer", "code-reviewer", stored("repeated", "code-reviewer", 1, ""), stored("replied_fixed", "implement-changes", 2, "r"))), true, false},
		{"a reply of another kind", batchOf(contracts.FindingsPostEvent{Kind: "reply_disputed", By: "implement-changes", ID: "f1"}), base,
			listing(plain("f1", "reviewer", "code-reviewer", stored("replied_fixed", "implement-changes", 2, "r"))), false, false},
		{"a close of a finding that a person closed", batchOf(contracts.FindingsPostEvent{Kind: "close_fixed", By: "code-reviewer", ID: "f1"}),
			listing(apiaryFinding("f1", "reviewer", "code-reviewer", "accepted", person("accepted"))),
			listing(apiaryFinding("f1", "reviewer", "code-reviewer", "accepted", person("accepted"), stored("closed_fixed", "code-reviewer", 2, "r"))), true, false},
		{"a dismissal that is stored", batchOf(contracts.FindingsPostEvent{Kind: "close_dismissed", By: "code-reviewer", ID: "f1"}), base,
			listing(plain("f1", "reviewer", "code-reviewer", stored("closed_dismissed", "code-reviewer", 2, "r"))), true, false},
		{"one event of the batch is proven", batchOf(repeat, contracts.FindingsPostEvent{Kind: "close_fixed", By: "code-reviewer", ID: "f9"}), base,
			listing(plain("f1", "reviewer", "code-reviewer", stored("repeated", "code-reviewer", 1, ""), stored("repeated", "code-reviewer", 2, ""))), true, false},
		{"the base of the post proves nothing", contracts.FindingsPost{Bases: []contracts.FindingBase{{Target: ledgerTarget, Base: "b"}}, Events: []contracts.FindingsPostEvent{repeat}},
			listing(), listing(), false, false},
		{"500 findings before and a raise not seen", batchOf(raise), listing(full...), listing(full...), false, true},
		{"500 findings before and a raise seen", batchOf(raise), listing(full...), listing(append(slices.Clone(full), raised("f501"))...), true, false},
		{"a finding of the batch with 100 events and no new one", batchOf(contracts.FindingsPostEvent{Kind: "reply_fixed", By: "implement-changes", ID: "f1"}),
			listing(hundred), listing(hundred), false, true},
		{"a finding of the batch with 100 events, the event seen", batchOf(repeat), listing(hundred), listing(hundredMore), true, false},
		{"a finding with 100 events, another event proven", batchOf(repeat, raise), listing(hundred),
			listing(hundred, raised("f2")), true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := findingsStored(c.batch, c.before, c.after)
			if c.stop {
				if !errors.Is(err, errNotKnown) || got {
					t.Errorf("the answer is %v and %v, want the stop", got, err)
				}
				return
			}
			if err != nil || got != c.stored {
				t.Errorf("the answer is %v and %v, want %v", got, err, c.stored)
			}
		})
	}
	twice := listing(apiaryFinding("f1", "reviewer", "code-reviewer", "open"), apiaryFinding("f1", "reviewer", "code-reviewer", "open"))
	if _, err := findingsStored(batchOf(repeat), twice, base); err == nil {
		t.Error("a GET before that lists one id twice is read")
	}
	if _, err := findingsStored(batchOf(repeat), base, twice); err == nil {
		t.Error("a GET after that lists one id twice is read")
	}
}

func TestReviewLedgerVerdict(t *testing.T) {
	answer := func(counts ...contracts.FindingsOpenBlocking) *contracts.FindingsPostAnswer {
		return &contracts.FindingsPostAnswer{OpenBlocking: counts}
	}
	soft := apiaryFinding("f2", "reviewer", "code-reviewer", "open")
	soft.Severity = "non-blocking"
	closed := apiaryFinding("f3", "reviewer", "code-reviewer", "fixed")
	elsewhere := apiaryFinding("f4", "reviewer", "code-reviewer", "open")
	elsewhere.Stage = "verify"
	for _, c := range []struct {
		name   string
		answer *contracts.FindingsPostAnswer
		got    contracts.Findings
		want   string
	}{
		{"no count of the stage", answer(contracts.FindingsOpenBlocking{Stage: "verify", Count: 3}), listing(apiaryFinding("f1", "reviewer", "x", "open")), runapi.VerdictPassed},
		{"a count of 0", answer(contracts.FindingsOpenBlocking{Stage: "build", Count: 0}), listing(), runapi.VerdictPassed},
		{"a count of 2", answer(contracts.FindingsOpenBlocking{Stage: "verify", Count: 0}, contracts.FindingsOpenBlocking{Stage: "build", Count: 2}), listing(), runapi.VerdictChangesRequested},
		{"no answer, no finding", nil, listing(), runapi.VerdictPassed},
		{"no answer, only non-blocking, closed and other stages", nil, listing(soft, closed, elsewhere), runapi.VerdictPassed},
		{"no answer, an open blocking finding", nil, listing(soft, closed, elsewhere, apiaryFinding("f1", "check", "suite", "open")), runapi.VerdictChangesRequested},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := roundVerdict("build", c.answer, c.got)
			if err != nil || got != c.want {
				t.Errorf("the verdict is %q and %v, want %q", got, err, c.want)
			}
		})
	}
	twice := answer(contracts.FindingsOpenBlocking{Stage: "build"}, contracts.FindingsOpenBlocking{Stage: "build"})
	if _, err := roundVerdict("build", twice, listing()); err == nil {
		t.Error("an answer that lists a stage twice gives a verdict")
	}
	dup := listing(apiaryFinding("f1", "reviewer", "x", "open"), apiaryFinding("f1", "reviewer", "x", "open"))
	if _, err := roundVerdict("build", nil, dup); err == nil {
		t.Error("a GET that lists one id twice gives a verdict")
	}
}

func TestReviewLedgerCutAndOneLine(t *testing.T) {
	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"a text that fits", cut("abc", 3), "abc"},
		{"a text that is cut keeps n characters with the ellipsis", cut("abcdef", 4), "abc\u2026"},
		{"characters, not bytes", cut("\u00e4\u00f6\u00fc\u00df\u00e4", 3), "\u00e4\u00f6\u2026"},
		{"a NUL is dropped", cut("a\x00b", 5), "ab"},
		{"the last characters", lastChars("abcdef", 3), "def"},
		{"a line break and a run of spaces become one space", oneLine("a \n\t b\u2028c", 20), "a b c"},
		{"a long line is cut", oneLine("abcdef ghi", 5), "abcd\u2026"},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestReviewLedgerStaleReportCloses(t *testing.T) {
	group := []string{"code-reviewer", "docs-reviewer"}
	check := func(role string) contracts.Finding {
		f := apiaryFinding("f9", sourceCheck, reportCheck+role, stateOpen)
		f.Stage = "build"
		return f
	}
	reviewer := apiaryFinding("f8", sourceReviewer, "docs-reviewer", stateOpen)
	reviewer.Stage = "build"
	reason := "No file of the change needs a report of docs-reviewer."
	closeOf := contracts.FindingsPostEvent{Kind: eventCloseFixed, By: "report-docs-reviewer", ID: "f9", Reason: reason}
	for _, c := range []struct {
		name               string
		found              []contracts.Finding
		byPath             []string
		fromLedger, listed bool
		want               []contracts.FindingsPostEvent
	}{
		{"files that no path of the role matches", []contracts.Finding{check("docs-reviewer")}, []string{"code-reviewer"}, true, true,
			[]contracts.FindingsPostEvent{closeOf}},
		{"files that match the role", []contracts.Finding{check("docs-reviewer")}, group, true, true, nil},
		{"no base of the work item", []contracts.Finding{check("docs-reviewer")}, []string{"code-reviewer"}, false, true, nil},
		{"a list of files that was not read", []contracts.Finding{check("docs-reviewer")}, []string{"code-reviewer"}, true, false, nil},
		{"an open finding of the role", []contracts.Finding{check("docs-reviewer"), reviewer}, []string{"code-reviewer"}, true, true, nil},
		{"no open check finding", nil, []string{"code-reviewer"}, true, true, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ledgerWhere.staleReportCloses(group, c.byPath, listing(c.found...), c.fromLedger, c.listed)
			if !slices.Equal(got, c.want) {
				t.Errorf("the events are %+v, want %+v", got, c.want)
			}
		})
	}
}
