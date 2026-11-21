package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/reviewround"
	"github.com/qoryai/qory/internal/runapi"
)

// reportOf is the report of reviewer with one finding of severity, or with none for "".
func reportOf(reviewer, severity string) string {
	if severity == "" {
		return `{"reviewer":"` + reviewer + `","findings":[]}`
	}
	return `{"reviewer":"` + reviewer + `","findings":[{"severity":"` + severity +
		`","quote":"q","problem":"p","fix":"f","proof":"failing input: x","point":"Point of ` + reviewer + `"}]}`
}

// checkFor writes reports (reviewer to report text) into a new folder and returns the
// check of the round with those reviewers expected, a passing suite and recorded verdict.
func checkFor(t *testing.T, expected []string, reports map[string]string, recorded string) reviewCheck {
	t.Helper()
	dir := t.TempDir()
	for name, text := range reports {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return reviewCheck{
		Expected:     expected,
		Reports:      dir,
		Suite:        &reviewround.Suite{State: reviewround.SuitePassed},
		Recorded:     recorded,
		OpenFindings: filepath.Join(t.TempDir(), "open-findings.json"),
	}
}

func TestReviewCheckRefusesPassedWithABlockingFinding(t *testing.T) {
	c := checkFor(t, []string{"code-reviewer", "test-reviewer"}, map[string]string{
		"code-reviewer": reportOf("code-reviewer", "blocking"),
		"test-reviewer": reportOf("test-reviewer", ""),
	}, runapi.VerdictPassed)
	res, err := checkReviewRound(c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stands || res.Decision.Verdict != reviewround.ChangesRequested {
		t.Errorf("stands %v, verdict %q", res.Stands, res.Decision.Verdict)
	}
	first, _, _ := strings.Cut(res.Refusal, "\n")
	if !strings.HasPrefix(first, "Record changes-requested, not passed") || len(first) > 100 || !strings.Contains(res.Refusal, "code-reviewer (1)") {
		t.Errorf("refusal %q", res.Refusal)
	}
}

func TestReviewCheckRefusesPassedWithAMissingReport(t *testing.T) {
	c := checkFor(t, []string{"code-reviewer", "test-reviewer"}, map[string]string{
		"code-reviewer": reportOf("code-reviewer", ""),
	}, runapi.VerdictPassed)
	res, err := checkReviewRound(c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stands || res.Refusal == "" || !strings.Contains(res.Refusal, "test-reviewer (1)") {
		t.Errorf("stands %v, refusal %q", res.Stands, res.Refusal)
	}
	// A report that is no file at all reads as missing too, and is no crash.
	c = checkFor(t, []string{"code-reviewer"}, nil, runapi.VerdictPassed)
	if err := os.Mkdir(filepath.Join(c.Reports, "code-reviewer.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if res, err := checkReviewRound(c); err != nil || res.Stands {
		t.Errorf("a folder as report: stands %v, error %v", res.Stands, err)
	}
}

func TestReviewCheckWritesEveryReportsBlockingFindings(t *testing.T) {
	c := checkFor(t, []string{"code-reviewer", "test-reviewer"}, map[string]string{
		"code-reviewer": reportOf("code-reviewer", "blocking"),
		"test-reviewer": reportOf("test-reviewer", "blocking"),
	}, runapi.VerdictChangesRequested)
	res, err := checkReviewRound(c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stands || res.Refusal != "" {
		t.Errorf("a recorded changes-requested: stands %v, refusal %q", res.Stands, res.Refusal)
	}
	data, err := os.ReadFile(c.OpenFindings)
	if err != nil {
		t.Fatal(err)
	}
	open, err := reviewround.ReadOpenFindings(data)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range open.Reviewers {
		for _, f := range entry.Findings {
			if f.Severity == reviewround.Blocking {
				got = append(got, entry.Reviewer+": "+f.Point)
			}
		}
	}
	if want := "code-reviewer: Point of code-reviewer test-reviewer: Point of test-reviewer"; strings.Join(got, " ") != want {
		t.Errorf("blocking findings %q, want %q", got, want)
	}
}

func TestReviewCheckLetsPassedStand(t *testing.T) {
	c := checkFor(t, []string{"code-reviewer", "test-reviewer"}, map[string]string{
		"code-reviewer": reportOf("code-reviewer", "non-blocking"),
		"test-reviewer": reportOf("test-reviewer", ""),
	}, runapi.VerdictPassed)
	res, err := checkReviewRound(c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stands || res.Refusal != "" || res.Decision.Verdict != reviewround.Passed {
		t.Errorf("stands %v, refusal %q, verdict %q", res.Stands, res.Refusal, res.Decision.Verdict)
	}
	if _, err := os.Stat(c.OpenFindings); err != nil {
		t.Errorf("open-findings.json: %v", err)
	}
	// The next round numbers from the one before.
	c.Previous = &res.Decision.Open
	if next, err := checkReviewRound(c); err != nil || next.Decision.Open.Round != 2 {
		t.Errorf("next round %d, error %v", next.Decision.Open.Round, err)
	}
	// A round with no expected reviewer is a wrong round, not a pass.
	c.Expected = nil
	if _, err := checkReviewRound(c); err == nil || !strings.HasPrefix(err.Error(), "Give the round at least one reviewer") {
		t.Errorf("no reviewer: %v", err)
	}
}

// While rounds are left, the refusal names changes-requested; in the last round of the
// loop, and when the suite has a cause that the agent cannot fix, it names escalate.
func TestReviewCheckRefusalNamesTheVerdictToRecord(t *testing.T) {
	for _, c := range []struct {
		name  string
		last  bool
		suite reviewround.Suite
		want  string
	}{
		{"rounds left", false, reviewround.Suite{State: reviewround.SuitePassed}, "Record changes-requested, not passed"},
		{"last round", true, reviewround.Suite{State: reviewround.SuitePassed}, "Record escalate, not passed"},
		{"suite the agent cannot fix", false, reviewround.Suite{State: reviewround.SuiteNotRun, AgentCannotFix: true}, "Record escalate, not passed"},
	} {
		in := checkFor(t, []string{"code-reviewer"}, map[string]string{
			"code-reviewer": reportOf("code-reviewer", "blocking"),
		}, runapi.VerdictPassed)
		in.LastRound = c.last
		in.Suite = &c.suite
		res, err := checkReviewRound(in)
		if err != nil {
			t.Fatal(err)
		}
		if res.Stands || !strings.HasPrefix(res.Refusal, c.want) {
			t.Errorf("%s: stands %v, refusal %q", c.name, res.Stands, res.Refusal)
		}
	}
}
