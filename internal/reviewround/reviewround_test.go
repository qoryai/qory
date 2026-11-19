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
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
	dir := t.TempDir()
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
		{"link", Reviewer{Report: link}, "Reviewer code-reviewer wrote no valid report: the report file is a link, or is in a folder that is a link."},
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
	good := Reviewer{Name: "code-reviewer", Ended: Finished, Report: "/r/report.json"}
	for name, r := range map[string]Round{
		"round 0":       {Number: 0, Suite: green},
		"a bad name":    {Number: 1, Suite: green, Reviewers: []Reviewer{{Name: "Code Reviewer", Ended: Finished}}},
		"the suite's":   {Number: 1, Suite: green, Reviewers: []Reviewer{{Name: SuiteEntry, Ended: Finished}}},
		"a name twice":  {Number: 1, Suite: green, Reviewers: []Reviewer{good, good}},
		"no ending set": {Number: 1, Suite: green, Reviewers: []Reviewer{{Name: "code-reviewer"}}},
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

func TestReadOpenFindingsChecksTheFile(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{`{"round":0,"reviewers":[]}`, "has no round"},
		{`{"round":1}`, "has no list reviewers"},
		{`{"round":1,"reviewers":[{"reviewer":"Bad","findings":[]}]}`, "no reviewer name"},
		{`{"round":1,"reviewers":[{"reviewer":"r"}]}`, "no list findings"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"x","severity":"blocking","point":"p"}]}]}`, "no id of the form"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"f1","severity":"minor","point":"p"}]}]}`, "a severity"},
		{`{"round":1,"reviewers":[{"reviewer":"r","findings":[{"id":"f1","severity":"blocking","point":" "}]}]}`, "an empty point"},
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
