package reviewround

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/runapi"
)

// The fixture reports and the previous round's open findings in testdata.
const (
	codeReport     = "testdata/code-reviewer.json"
	securityReport = "testdata/security-reviewer.json"
	previousFile   = "testdata/previous.json"
)

// fixture is the absolute path of a file in testdata.
func fixture(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// reportAt writes text as a report file in a new folder and returns its path.
func reportAt(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(realTempDir(t), "report.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// realTempDir is a new folder at a path with no link, as reportfile.Read needs: on macOS
// the default temporary folder is below /var, a link to /private/var.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// previous is the open findings of testdata/previous.json.
func previous(t *testing.T) *OpenFindings {
	t.Helper()
	data, err := os.ReadFile(previousFile)
	if err != nil {
		t.Fatal(err)
	}
	o, err := ReadOpenFindings(data)
	if err != nil {
		t.Fatal(err)
	}
	return &o
}

var green = &Suite{State: SuitePassed}

// decide decides r and checks the decision: the verdict and its points against
// contracts/harness/v1/verdict.schema.json, and open-findings.json as ReadOpenFindings reads it.
func decide(t *testing.T, r Round) Decision {
	t.Helper()
	d, err := Decide(r)
	if err != nil {
		t.Fatal(err)
	}
	body, err := runapi.VerdictBody(d.Verdict, d.Points, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if err := verdictSchema(t).Validate(doc); err != nil {
		t.Errorf("the verdict %s does not match the verdict schema: %v", body, err)
	}
	data, err := d.Open.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > MaxOpenBytes {
		t.Errorf("open-findings.json has %d bytes, more than %d", len(data), MaxOpenBytes)
	}
	back, err := ReadOpenFindings(data)
	if err != nil {
		t.Errorf("open-findings.json does not read back: %v\n%s", err, data)
	}
	if fmt.Sprint(back) != fmt.Sprint(d.Open) {
		t.Errorf("open-findings.json reads back as %v, not %v", back, d.Open)
	}
	return d
}

var (
	verdictOnce     sync.Once
	verdictCompiled *jsonschema.Schema
	verdictErr      error
)

// verdictSchema is qory's contract of the verdict, read from the repository.
func verdictSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	verdictOnce.Do(func() {
		const url = "https://qory.dev/contracts/harness/v1/verdict.schema.json"
		data, err := os.ReadFile("../../contracts/harness/v1/verdict.schema.json")
		if err != nil {
			verdictErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			verdictErr = err
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(url, doc); err != nil {
			verdictErr = err
			return
		}
		verdictCompiled, verdictErr = c.Compile(url)
	})
	if verdictErr != nil {
		t.Fatal(verdictErr)
	}
	return verdictCompiled
}

// ids is each finding's id and severity of open, as "f1 blocking", entry by entry.
func ids(open OpenFindings) map[string][]string {
	got := map[string][]string{}
	for _, entry := range open.Reviewers {
		got[entry.Reviewer] = []string{}
		for _, f := range entry.Findings {
			got[entry.Reviewer] = append(got[entry.Reviewer], f.ID+" "+f.Severity)
		}
	}
	return got
}

func TestABlockingFindingRequestsChangesWithEveryOpenFinding(t *testing.T) {
	d := decide(t, Round{Number: 1, Suite: green, Reviewers: []Reviewer{
		{Name: "code-reviewer", Ended: Finished, Report: fixture(t, codeReport)},
		{Name: "security-reviewer", Ended: Finished, Report: fixture(t, securityReport)},
	}})
	if d.Verdict != ChangesRequested {
		t.Fatalf("the verdict is %s", d.Verdict)
	}
	want := map[string][]string{
		"code-reviewer":     {"f1 blocking", "f2 non-blocking"},
		"security-reviewer": {"f3 non-blocking"},
	}
	if got := ids(d.Open); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the open findings are %v, want %v", got, want)
	}
	texts := []string{}
	for _, p := range d.Points {
		texts = append(texts, p.Text)
		if p.Action != runapi.ActionRunAgain {
			t.Errorf("the point %q has the action %s", p.Text, p.Action)
		}
	}
	wantTexts := []string{"A batch of exactly the limit loses its last item.",
		"The comment of Batch restates its name.", "The token is read on each call."}
	if !slices.Equal(texts, wantTexts) {
		t.Errorf("the points are %q, want %q", texts, wantTexts)
	}
	if d.Open.Round != 1 || d.NextID != 4 {
		t.Errorf("round %d, next id %d", d.Open.Round, d.NextID)
	}
	if data, _ := d.Open.JSON(); !bytes.Contains(data, []byte(`"quote": "if count >= limit {"`)) {
		t.Errorf("open-findings.json escapes the quote:\n%s", data)
	}
	kept := d.Open.Reviewers[0].Findings[0]
	if kept.Quote != "if count >= limit {" || kept.File != "internal/batch/batch.go" || kept.Line != 12 ||
		!strings.HasPrefix(kept.Proof, "failing check:") {
		t.Errorf("the finding lost the reviewer's members: %+v", kept)
	}
}

func TestOnlyNonBlockingFindingsPass(t *testing.T) {
	d := decide(t, Round{Number: 2, Suite: green, Reviewers: []Reviewer{
		{Name: "code-reviewer", Ended: Finished, Report: reportAt(t, `{"reviewer":"code-reviewer","findings":[]}`)},
		{Name: "security-reviewer", Ended: Finished, Report: fixture(t, securityReport)},
	}})
	if d.Verdict != Passed || d.Points != nil {
		t.Fatalf("the verdict is %s with points %v", d.Verdict, d.Points)
	}
	// The non-blocking finding stays in the round's open findings, and is not fixed.
	if got := ids(d.Open); fmt.Sprint(got) != fmt.Sprint(map[string][]string{
		"code-reviewer": {}, "security-reviewer": {"f1 non-blocking"},
	}) {
		t.Errorf("the open findings are %v", got)
	}
}

func TestAReviewerWithNoValidReportIsABlockingFinding(t *testing.T) {
	dir := realTempDir(t)
	link := filepath.Join(dir, "report.json")
	if err := os.Symlink(fixture(t, codeReport), link); err != nil {
		t.Fatal(err)
	}
	finding := `{"severity":"blocking","quote":"q","problem":"p","fix":"f","proof":"failing input: x","point":"p"}`
	cases := []struct {
		name  string
		r     Reviewer
		point string
	}{
		{"failed", Reviewer{Ended: Failed, Report: fixture(t, codeReport)}, "Reviewer code-reviewer wrote no report: its session failed."},
		{"timed out", Reviewer{Ended: TimedOut}, "Reviewer code-reviewer wrote no report: its session reached its time limit."},
		{"missing", Reviewer{Report: filepath.Join(dir, "none.json")}, "Reviewer code-reviewer wrote no report."},
		{"link", Reviewer{Report: link}, "Reviewer code-reviewer wrote no valid report: the report file is a link, or is below a folder that is a link."},
		{"folder", Reviewer{Report: dir}, "Reviewer code-reviewer wrote no valid report: the report file is no regular file."},
		{"not JSON", Reviewer{Report: reportAt(t, `{"reviewer":`)}, "Reviewer code-reviewer wrote a report that is not one JSON object: unexpected EOF."},
		{"two values", Reviewer{Report: reportAt(t, `{"reviewer":"code-reviewer","findings":[]} {}`)}, "Reviewer code-reviewer wrote a report that is not one JSON object: the file continues after the JSON value."},
		{"not UTF-8", Reviewer{Report: reportAt(t, "{\"reviewer\":\"\xff\"}")}, "Reviewer code-reviewer wrote a report that is not UTF-8 text."},
		{"result word", Reviewer{Report: reportAt(t, `{"reviewer":"code-reviewer","findings":[],"result":"clean"}`)}, "Reviewer code-reviewer wrote a report that does not match the report format: "},
		{"bad severity", Reviewer{Report: reportAt(t, `{"reviewer":"code-reviewer","findings":[`+strings.Replace(finding, `"blocking"`, `"minor"`, 1)+`]}`)}, "Reviewer code-reviewer wrote a report that does not match the report format: at '/findings/0/severity'"},
		{"blocking on a broken rule", Reviewer{Report: reportAt(t, `{"reviewer":"code-reviewer","findings":[`+strings.Replace(finding, "failing input", "broken rule", 1)+`]}`)}, "Reviewer code-reviewer wrote a report that does not match the report format: "},
		{"other reviewer", Reviewer{Report: fixture(t, securityReport)}, "Reviewer code-reviewer wrote a report for the reviewer security-reviewer."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.r.Name = "code-reviewer"
			if c.r.Ended == 0 {
				c.r.Ended = Finished
			}
			d := decide(t, Round{Number: 1, Suite: green, Reviewers: []Reviewer{c.r}})
			if d.Verdict != ChangesRequested {
				t.Fatalf("the verdict is %s", d.Verdict)
			}
			got := d.Open.Reviewers[0].Findings
			if len(got) != 1 || got[0].Severity != Blocking || got[0].ID != "f1" {
				t.Fatalf("the findings are %+v", got)
			}
			if !strings.HasPrefix(got[0].Point, c.point) || got[0].Fix != "" {
				t.Errorf("the finding is %+v, want a point that starts %q", got[0], c.point)
			}
			if len(d.Points) != 1 || d.Points[0].Text != got[0].Point {
				t.Errorf("the points are %+v", d.Points)
			}
		})
	}
}

func TestTheSuiteDecidesBesideTheReports(t *testing.T) {
	clean := func(t *testing.T) []Reviewer {
		return []Reviewer{{Name: "code-reviewer", Ended: Finished, Report: reportAt(t, `{"reviewer":"code-reviewer","findings":[]}`)}}
	}
	cases := []struct {
		name    string
		suite   *Suite
		verdict string
		point   string
	}{
		{"red", &Suite{State: SuiteFailed, Point: "The full suite failed: 2 of 140 tests failed."}, ChangesRequested, "The full suite failed: 2 of 140 tests failed."},
		{"red with no point", &Suite{State: SuiteFailed}, ChangesRequested, "The full suite failed."},
		{"not run, the agent can fix", &Suite{State: SuiteNotRun, Point: "The full suite did not start: the build plan has no full row."}, ChangesRequested, "The full suite did not start: the build plan has no full row."},
		{"not run, the agent cannot fix", &Suite{State: SuiteNotRun, Point: "The full suite did not start: git could not read HEAD of the workspace.", AgentCannotFix: true}, Escalate, "The full suite did not start: git could not read HEAD of the workspace."},
		{"red, the agent cannot fix", &Suite{State: SuiteFailed, AgentCannotFix: true}, Escalate, "The full suite failed."},
		{"no result", nil, Escalate, "The full suite has no result for the reviewed commit."},
		{"an unknown state", &Suite{State: "running"}, Escalate, "The full suite did not run."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := decide(t, Round{Number: 1, Suite: c.suite, Reviewers: clean(t)})
			if d.Verdict != c.verdict {
				t.Fatalf("the verdict is %s, want %s", d.Verdict, c.verdict)
			}
			first := d.Open.Reviewers[0]
			if first.Reviewer != SuiteEntry || len(first.Findings) != 1 || first.Findings[0].Severity != Blocking ||
				first.Findings[0].Point != c.point {
				t.Errorf("the first entry is %+v", first)
			}
			if len(d.Points) != 1 || d.Points[0].Text != c.point {
				t.Errorf("the points are %+v", d.Points)
			}
		})
	}
	// A cause the agent cannot fix escalates over a reviewer's blocking finding too, with
	// the suite's point first.
	d := decide(t, Round{Number: 1, Suite: &Suite{State: SuiteNotRun, AgentCannotFix: true},
		Reviewers: []Reviewer{{Name: "code-reviewer", Ended: Finished, Report: fixture(t, codeReport)}}})
	if d.Verdict != Escalate || len(d.Points) != 3 || d.Points[0].Text != "The full suite did not run." {
		t.Errorf("the verdict is %s with %+v", d.Verdict, d.Points)
	}
}

func TestIDsCountAcrossTheRun(t *testing.T) {
	again := func(id string) string {
		return `{"id":"` + id + `","severity":"non-blocking","quote":"q","problem":"p","fix":"f","proof":"broken rule: r","point":"` + id + `"}`
	}
	code := reportAt(t, `{"reviewer":"code-reviewer","findings":[`+again("f1")+`,`+again("f9")+`,`+again("f1")+`]}`)
	security := reportAt(t, `{"reviewer":"security-reviewer","findings":[`+again("f2")+`,`+again("f1")+`]}`)
	d := decide(t, Round{Number: 2, NextID: 5, Suite: green, Previous: previous(t), Reviewers: []Reviewer{
		{Name: "code-reviewer", Ended: Finished, Report: code},
		{Name: "security-reviewer", Ended: Finished, Report: security},
	}})
	// f1 and f2 repeat; f9 is no id of the round before, and the second f1 repeats one this
	// round has, so each gets a new id from NextID on.
	want := map[string][]string{
		"code-reviewer":     {"f1 non-blocking", "f5 non-blocking", "f6 non-blocking"},
		"security-reviewer": {"f2 non-blocking", "f7 non-blocking"},
	}
	if got := ids(d.Open); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the ids are %v, want %v", got, want)
	}
	if d.NextID != 8 || d.Verdict != Passed {
		t.Errorf("next id %d, verdict %s", d.NextID, d.Verdict)
	}
	// With no NextID, the ids start after the highest of the round before.
	d = decide(t, Round{Number: 2, Suite: green, Previous: previous(t), Reviewers: []Reviewer{
		{Name: "code-reviewer", Ended: Finished, Report: fixture(t, codeReport)},
	}})
	if got := ids(d.Open)["code-reviewer"]; !slices.Equal(got, []string{"f3 blocking", "f4 non-blocking"}) {
		t.Errorf("the ids are %v", got)
	}
}

func TestMoreThanTwentyFindingsKeepTheVerdictValid(t *testing.T) {
	var findings []string
	for i := range 25 {
		severity := "non-blocking"
		if i%5 == 0 {
			severity = "blocking"
		}
		findings = append(findings, fmt.Sprintf(`{"severity":%q,"quote":"q","problem":"p","fix":"f","proof":"failing input: x","point":"point %d\twith a tab"}`, severity, i+1))
	}
	report := reportAt(t, `{"reviewer":"code-reviewer","findings":[`+strings.Join(findings, ",")+`]}`)
	d := decide(t, Round{Number: 1, Suite: green, Reviewers: []Reviewer{{Name: "code-reviewer", Ended: Finished, Report: report}}})
	if len(d.Points) != 20 {
		t.Fatalf("the verdict has %d points", len(d.Points))
	}
	// The 5 blocking findings come first, and the last point states the 6 that no point lists.
	if d.Points[0].Text != "point 1 with a tab" || d.Points[4].Text != "point 21 with a tab" {
		t.Errorf("the first points are %+v", d.Points[:5])
	}
	if d.Points[19].Text != "The review has 6 more open findings than these points list." {
		t.Errorf("the last point is %q", d.Points[19].Text)
	}
	if got := len(d.Open.Reviewers[0].Findings); got != 25 {
		t.Errorf("open-findings.json has %d findings", got)
	}
}

func TestDecideStopsOnARoundMadeWrong(t *testing.T) {
	// A name of 64 characters, the most, so that line 1 of each error stays within 100.
	long := strings.Repeat("r", 64)
	good := Reviewer{Name: long, Ended: Finished, Report: "/r/report.json"}
	for name, r := range map[string]Round{
		"round 0":       {Number: 0, Suite: green},
		"no reviewer":   {Number: 1, Suite: green},
		"a bad name":    {Number: 1, Suite: green, Reviewers: []Reviewer{{Name: "Code Reviewer", Ended: Finished}}},
		"the suite's":   {Number: 1, Suite: green, Reviewers: []Reviewer{{Name: SuiteEntry, Ended: Finished}}},
		"a name twice":  {Number: 1, Suite: green, Reviewers: []Reviewer{good, good}},
		"no ending set": {Number: 1, Suite: green, Reviewers: []Reviewer{{Name: long}}},
	} {
		_, err := Decide(r)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if first := strings.SplitN(err.Error(), "\n", 2)[0]; len(first) > 100 {
			t.Errorf("%s: line 1 has %d characters: %q", name, len(first), first)
		}
	}
}

func TestAPointOfSpacesIsNoValidReport(t *testing.T) {
	// The format stops a point of ASCII spaces, and Decide one of Unicode spaces or of
	// characters that the harness's plain() removes.
	const format, trimmed = "does not match pattern", "the point has no character that shows"
	for name, c := range map[string]struct{ point, cause string }{
		"a space": {" ", format}, "a tab": {"\t", format}, "U+00A0": {"\u00a0", trimmed}, "U+00A0 and a space": {"\u00a0 ", trimmed},
		"U+200B": {"\u200b", trimmed}, "U+3164": {"\u3164", trimmed}, "U+034F": {"\u034f", trimmed},
		"U+FEFF": {"\ufeff", trimmed}, "the four": {"\u200b \u3164\u034f\ufeff", trimmed},
	} {
		point := c.point
		t.Run(name, func(t *testing.T) {
			finding, err := json.Marshal(map[string]string{"severity": "non-blocking", "quote": "q", "problem": "p",
				"fix": "f", "proof": "broken rule: r", "point": point})
			if err != nil {
				t.Fatal(err)
			}
			report := reportAt(t, `{"reviewer":"code-reviewer","findings":[`+string(finding)+`]}`)
			d := decide(t, Round{Number: 1, Suite: green, Reviewers: []Reviewer{{Name: "code-reviewer", Ended: Finished, Report: report}}})
			got := d.Open.Reviewers[0].Findings
			want := "Reviewer code-reviewer wrote a report that does not match the report format: at '/findings/0/point'"
			if d.Verdict != ChangesRequested || len(got) != 1 || got[0].Severity != Blocking ||
				!strings.HasPrefix(got[0].Point, want) || !strings.Contains(got[0].Point, c.cause) {
				t.Errorf("the verdict is %s with %+v", d.Verdict, got)
			}
		})
	}
	// The spaces around a point's text go.
	finding := `{"severity":"non-blocking","quote":"q","problem":"p","fix":"f","proof":"broken rule: r","point":"\u00a0 one point\t"}`
	report := reportAt(t, `{"reviewer":"code-reviewer","findings":[`+finding+`]}`)
	d := decide(t, Round{Number: 1, Suite: green, Reviewers: []Reviewer{{Name: "code-reviewer", Ended: Finished, Report: report}}})
	if got := d.Open.Reviewers[0].Findings; len(got) != 1 || got[0].Point != "one point" {
		t.Errorf("the findings are %+v", got)
	}
}

// bigFinding is a finding of a report with each text at its most characters, each of two
// bytes in UTF-8.
func bigFinding(t *testing.T, severity string, number int) string {
	t.Helper()
	long := func(n int) string { return strings.Repeat("ü", n) }
	proof := "failing input: " + long(985)
	data, err := json.Marshal(map[string]string{"severity": severity, "quote": long(2000), "problem": long(2000),
		"fix": long(2000), "proof": proof, "point": fmt.Sprintf("point %d %s", number, long(480))})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestOpenFindingsFitTheHarnessRead(t *testing.T) {
	// code-reviewer has 2 blocking findings and 58 non-blocking ones, and security-reviewer
	// 40 blocking ones: about 1.5 MB of findings, for a file of at most 256 KiB.
	var code, security []string
	for i := range 60 {
		severity := NonBlocking
		if i == 0 || i == 35 {
			severity = Blocking
		}
		code = append(code, bigFinding(t, severity, i+1))
	}
	for i := range 40 {
		security = append(security, bigFinding(t, Blocking, 61+i))
	}
	d := decide(t, Round{Number: 1, Suite: green, Reviewers: []Reviewer{
		{Name: "code-reviewer", Ended: Finished, Report: reportAt(t, `{"reviewer":"code-reviewer","findings":[`+strings.Join(code, ",")+`]}`)},
		{Name: "security-reviewer", Ended: Finished, Report: reportAt(t, `{"reviewer":"security-reviewer","findings":[`+strings.Join(security, ",")+`]}`)},
	}})
	// The verdict's points count every finding: 42 blocking, then 58 non-blocking.
	if d.Verdict != ChangesRequested || len(d.Points) != 20 ||
		d.Points[19].Text != "The review has 81 more open findings than these points list." {
		t.Fatalf("the verdict is %s with %d points", d.Verdict, len(d.Points))
	}
	data, err := d.Open.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < MaxOpenBytes/2 {
		t.Errorf("open-findings.json has %d bytes, far below %d", len(data), MaxOpenBytes)
	}
	// The blocking findings stay first: code-reviewer's 2, then security-reviewer's. Each
	// entry ends with the finding of qory's that states how many of its findings are out,
	// blocking where a blocking finding is out.
	codeKept, securityKept := d.Open.Reviewers[0].Findings, d.Open.Reviewers[1].Findings
	if len(codeKept) != 3 || codeKept[0].ID != "f1" || codeKept[1].ID != "f36" {
		t.Fatalf("code-reviewer's findings are %v", ids(d.Open)["code-reviewer"])
	}
	kept := len(securityKept) - 1
	if kept < 5 {
		t.Fatalf("security-reviewer has %d findings in the file", kept)
	}
	for _, c := range []struct {
		last     Finding
		severity string
		point    string
	}{
		{codeKept[2], NonBlocking, "The review has 58 more open findings of code-reviewer than this file lists."},
		{securityKept[kept], Blocking, fmt.Sprintf("The review has %d more open findings of security-reviewer than this file lists.", 40-kept)},
	} {
		if c.last.Severity != c.severity || c.last.Point != c.point || c.last.Quote != "" {
			t.Errorf("the last finding is %+v, want %s %q", c.last, c.severity, c.point)
		}
	}
	// The findings out keep their ids, so the findings of qory's come after them.
	if codeKept[2].ID != "f101" || securityKept[kept].ID != "f102" || d.NextID != 103 {
		t.Errorf("the ids of qory's findings are %s and %s, next id %d", codeKept[2].ID, securityKept[kept].ID, d.NextID)
	}
}

// TestTheCutKeepsAFileOfTheLimit is the edge of the cut: the harness's findings open reads
// a file of at most MaxOpenBytes, so a file of exactly that size stays whole, and one byte
// more cuts it.
func TestTheCutKeepsAFileOfTheLimit(t *testing.T) {
	sized := func(size int) OpenFindings {
		o := OpenFindings{Round: 1, Reviewers: []ReviewerFindings{{Reviewer: "code-reviewer", Findings: []Finding{
			{ID: "f1", Severity: NonBlocking, Point: "p", Problem: "a"},
		}}}}
		data, err := o.JSON()
		if err != nil {
			t.Fatal(err)
		}
		o.Reviewers[0].Findings[0].Problem = strings.Repeat("a", 1+size-len(data))
		if data, _ := o.JSON(); len(data) != size {
			t.Fatalf("the file has %d bytes, not %d", len(data), size)
		}
		return o
	}
	whole := sized(MaxOpenBytes)
	got, err := cutToFit(whole, newNumbering(2, nil))
	if err != nil || fmt.Sprint(got) != fmt.Sprint(whole) {
		t.Errorf("a file of %d bytes is cut: %v", MaxOpenBytes, err)
	}
	got, err = cutToFit(sized(MaxOpenBytes+1), newNumbering(2, nil))
	if err != nil {
		t.Fatal(err)
	}
	if f := got.Reviewers[0].Findings; len(f) != 1 || f[0].ID != "f2" ||
		f[0].Point != "The review has 1 more open finding of code-reviewer than this file lists." {
		t.Errorf("a file of %d bytes is cut to %+v", MaxOpenBytes+1, f)
	}
}

func TestAHiddenPointHasQorysTextInTheVerdict(t *testing.T) {
	for _, point := range []string{"\u200b", "\u3164", "\u034f", "\ufeff"} {
		got := points(OpenFindings{Round: 1, Reviewers: []ReviewerFindings{{Reviewer: "code-reviewer", Findings: []Finding{
			{ID: "f1", Severity: Blocking, Point: point},
		}}}})
		if len(got) != 1 || got[0].Text != "A finding of code-reviewer has a point with no readable text." {
			t.Errorf("%U: the points are %+v", []rune(point)[0], got)
		}
	}
	// A suite's point of hidden characters alone gets qory's text.
	d := decide(t, Round{Number: 1, Suite: &Suite{State: SuiteFailed, Point: "\u200b\u3164"},
		Reviewers: []Reviewer{{Name: "code-reviewer", Ended: Finished, Report: reportAt(t, `{"reviewer":"code-reviewer","findings":[]}`)}}})
	if p := d.Open.Reviewers[0].Findings[0].Point; p != "The full suite failed." {
		t.Errorf("the suite's point is %q", p)
	}
}

func TestHarnessPlainRemovesWhatTheHarnessRemoves(t *testing.T) {
	for in, want := range map[string]string{
		" a\u200bb\u3164c\u034fd\ufeff ": "abcd", "\u2028x\u2029": "x", "a\tb": "a b",
		"\ufe0f\U000e0041\u00ad\u202e\ufffc": "", "\u00a0point\u00a0": "point",
	} {
		if got := harnessPlain(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestMoreFindingsIsSingularForOne(t *testing.T) {
	for n, want := range map[int]string{1: "1 more open finding", 2: "2 more open findings", 81: "81 more open findings"} {
		if got := moreFindings(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}

func TestReadOpenFindingsChecksTheFile(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{`{"round":0,"reviewers":[]}`, "has no round"},
		{`{"round":1}`, "has no list reviewers"},
		{`{"round":1,"reviewers":[{"reviewer":"Bad","findings":[]}]}`, "no reviewer name"},
		{`{"round":1,"reviewers":[{"reviewer":"r"}]}`, "no list findings"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"x","severity":"blocking","point":"p"}]}]}`, "no id of the form"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"f1","severity":"minor","point":"p"}]}]}`, "a severity"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"f1","severity":"blocking","point":" "}]}]}`, "an empty point"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"f1","severity":"blocking","point":"\u200b"}]}]}`, "an empty point"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"f1","severity":"blocking","point":"\u3164"}]}]}`, "an empty point"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"f1","severity":"blocking","point":"\u034f"}]}]}`, "an empty point"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"f1","severity":"blocking","point":"\ufeff"}]}]}`, "an empty point"},
		{`{"round":1,"reviewers":[],"more":1}`, "is not one JSON object"},
		{`{"round":1,"reviewers":[]} []`, "continues after"},
	} {
		_, err := ReadOpenFindings([]byte(c.text))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: the error is %v, want %q", c.text, err, c.want)
			continue
		}
		if !strings.HasPrefix(err.Error(), "Give the open-findings.json that qory wrote for the round before.\n") {
			t.Errorf("%s: the error does not start with its fix: %v", c.text, err)
		}
	}
}

// TestEmbeddedSchemaIsTheContract keeps the package's copy of the report format equal to
// the contract's file, byte for byte.
func TestEmbeddedSchemaIsTheContract(t *testing.T) {
	const contract = "../../contracts/harness/v1/review-report.schema.json"
	want, err := os.ReadFile(contract)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("review-report.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/reviewround/review-report.schema.json differs from %s", contract)
	}
	if !bytes.Contains(want, []byte(`"$id": "`+reportSchemaURL+`"`)) {
		t.Errorf("%s has no $id %s", contract, reportSchemaURL)
	}
}

// TestTheDecisionLinksNothingOfForager holds the package apart from Forager, so a program
// that runs no Forager session can run the decision: the test binary, which links the
// package and its tests, has no module of Forager.
func TestTheDecisionLinksNothingOfForager(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("the test binary has no build information")
	}
	for _, dep := range info.Deps {
		if strings.Contains(dep.Path, "forager") {
			t.Errorf("the test binary links %s", dep.Path)
		}
	}
}
