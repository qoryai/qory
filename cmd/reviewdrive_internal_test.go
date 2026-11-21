package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/reviewcopy"
	"github.com/qoryai/qory/internal/reviewround"
	"github.com/qoryai/qory/internal/runapi"
)

// cleanReport is the report of a reviewer with no finding.
const cleanReport = `{"reviewer":%q,"findings":[]}`

// fakeRound is a [reviewRun] whose steps record what they were asked, with the reviewers
// that crash listed in crash and the ones that wrote a blocking finding in blocking.
type fakeRound struct {
	t        *testing.T
	run      reviewRun
	crash    map[string]bool
	blocking map[string]bool
	joins    []reviewJoin
	copies   []string
	verdicts []string
	points   [][]runapi.Point
	nexts    []int
	order    []string
}

func newFakeRound(t *testing.T, names ...string) *fakeRound {
	t.Helper()
	dir := t.TempDir()
	f := &fakeRound{t: t, crash: map[string]bool{}, blocking: map[string]bool{}}
	agents := make([]reviewerAgent, len(names))
	for i, n := range names {
		agents[i] = reviewerAgent{Name: n, Tier: "light"}
	}
	f.run = reviewRun{
		Reviewers: agents, Number: 1, BuildSession: 4, Suite: &reviewround.Suite{State: reviewround.SuitePassed}, OpenFindingsPath: filepath.Join(dir, "open-findings.json"),
		Copy: func(_ context.Context, name string) (reviewcopy.Folder, error) {
			f.copies = append(f.copies, name)
			return reviewcopy.New(filepath.Join(dir, "copy-"+name))
		},
		ReportRoot: func(_ context.Context, name string) (string, error) {
			root := filepath.Join(dir, "report-"+name)
			return root, os.Mkdir(root, 0o700)
		},
		Join: func(_ context.Context, j reviewJoin) (reviewJoined, error) {
			f.joins = append(f.joins, j)
			f.order = append(f.order, j.Spec.Reviewer)
			if f.crash[j.Spec.Reviewer] {
				return reviewJoined{Session: j.Number, Ended: reviewround.Failed}, nil
			}
			text := strings.Replace(cleanReport, "%q", `"`+j.Spec.Reviewer+`"`, 1)
			if f.blocking[j.Spec.Reviewer] {
				text = `{"reviewer":"` + j.Spec.Reviewer + `","findings":[{"severity":"blocking","quote":"q","problem":"p","fix":"f","proof":"broken rule: r","point":"The code loses data."}]}`
			}
			if err := os.WriteFile(j.Spec.Report, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			return reviewJoined{Session: j.Number, Ended: reviewround.Finished}, nil
		},
		SendVerdict: func(_ context.Context, verdict string, points []runapi.Point) error {
			f.verdicts = append(f.verdicts, verdict)
			f.points = append(f.points, points)
			return nil
		},
		SendNext: func(_ context.Context, session int) error {
			f.nexts = append(f.nexts, session)
			return nil
		},
	}
	return f
}

func TestChangesRequestedFromTheSessionStartsNoReviewer(t *testing.T) {
	for _, c := range []struct {
		name     string
		recorded string
		review   bool
	}{
		{"changes-requested, with no points", runapi.VerdictChangesRequested, false},
		{"no verdict", "", true},
	} {
		f := newFakeRound(t, "code-reviewer", "test-reviewer")
		f.run.Recorded = c.recorded
		out, err := runReviewRound(context.Background(), f.run)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if out.Ran != c.review || (len(f.joins) > 0) != c.review {
			t.Errorf("%s: ran = %t with %d joins, want a round: %t", c.name, out.Ran, len(f.joins), c.review)
		}
		if !c.review {
			if len(f.copies) != 0 || len(f.verdicts) != 0 || len(f.nexts) != 0 || out.Session != 4 {
				t.Errorf("%s: copies %v, verdicts %v, requests %v, session %d; want none of the first three and session 4", c.name, f.copies, f.verdicts, f.nexts, out.Session)
			}
			if _, err := os.Stat(f.run.OpenFindingsPath); !os.IsNotExist(err) {
				t.Errorf("%s: open-findings.json was written (%v)", c.name, err)
			}
		}
	}
}

func TestRoundOfTwoReviewersSendsOneRequestWithTheLastNumber(t *testing.T) {
	f := newFakeRound(t, "code-reviewer", "test-reviewer")
	out, err := runReviewRound(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.nexts) != 1 || f.nexts[0] != 6 || out.Session != 6 {
		t.Errorf("requests = %v, session %d; want one request with 6 (the Build session 4 and two joins)", f.nexts, out.Session)
	}
	if len(f.joins) != 2 || f.joins[0].Number != 5 || f.joins[1].Number != 6 || strings.Join(f.order, ",") != "code-reviewer,test-reviewer" {
		t.Errorf("joins = %+v, want code-reviewer as 5 and test-reviewer as 6, in the group's order", f.joins)
	}
	if len(f.verdicts) != 1 {
		t.Errorf("verdicts = %v, want one", f.verdicts)
	}
}

func TestCleanRoundIsPassedAndWritesOpenFindings(t *testing.T) {
	f := newFakeRound(t, "code-reviewer", "test-reviewer")
	out, err := runReviewRound(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.verdicts) != 1 || f.verdicts[0] != runapi.VerdictPassed || len(f.points[0]) != 0 || out.Decision.Verdict != runapi.VerdictPassed {
		t.Errorf("verdicts = %v, points = %v; want passed with no points", f.verdicts, f.points)
	}
	data, err := os.ReadFile(f.run.OpenFindingsPath)
	if err != nil {
		t.Fatal(err)
	}
	open, err := reviewround.ReadOpenFindings(data)
	if err != nil || open.Round != 1 || len(open.Reviewers) != 2 {
		t.Errorf("open-findings.json = %s (%v)", data, err)
	}
	if info, _ := os.Stat(f.run.OpenFindingsPath); info.Mode().Perm() != openFindingsMode {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}

func TestCrashedReviewerIsAFinding(t *testing.T) {
	f := newFakeRound(t, "code-reviewer", "test-reviewer")
	f.crash["test-reviewer"] = true
	if _, err := runReviewRound(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	if len(f.verdicts) != 1 || f.verdicts[0] != runapi.VerdictChangesRequested || len(f.points[0]) != 1 ||
		!strings.Contains(f.points[0][0].Text, "Reviewer test-reviewer wrote no report: its session failed.") {
		t.Errorf("verdicts = %v, points = %+v", f.verdicts, f.points)
	}
	if len(f.nexts) != 1 || f.nexts[0] != 6 {
		t.Errorf("requests = %v, want one with 6", f.nexts)
	}
}

func TestBlockingFindingAndPreviousFindingsReachTheNextRound(t *testing.T) {
	f := newFakeRound(t, "code-reviewer")
	f.blocking["code-reviewer"] = true
	if _, err := runReviewRound(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	if f.verdicts[0] != runapi.VerdictChangesRequested {
		t.Fatalf("verdict = %q", f.verdicts[0])
	}
	data, err := os.ReadFile(f.run.OpenFindingsPath)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := reviewround.ReadOpenFindings(data)
	if err != nil {
		t.Fatal(err)
	}
	g := newFakeRound(t, "code-reviewer")
	g.run.Number, g.run.Previous, g.run.BuildSession = 2, &previous, 7
	if _, err := runReviewRound(context.Background(), g.run); err != nil {
		t.Fatal(err)
	}
	if len(g.joins) != 1 || g.joins[0].Previous != &previous || g.joins[0].Round != 2 || g.joins[0].Number != 8 || g.nexts[0] != 8 {
		t.Errorf("joins = %+v, requests = %v", g.joins, g.nexts)
	}
}

func TestFailedSuiteIsAFindingOfTheRound(t *testing.T) {
	f := newFakeRound(t, "code-reviewer")
	f.run.Suite = &reviewround.Suite{State: reviewround.SuiteFailed}
	if _, err := runReviewRound(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	if f.verdicts[0] != runapi.VerdictChangesRequested || len(f.points[0]) != 1 || f.points[0][0].Text != "The full suite failed." {
		t.Errorf("verdicts = %v, points = %+v", f.verdicts, f.points)
	}
}

func TestRoundStopsOnAnErrorAndSendsNothingAfter(t *testing.T) {
	boom := errors.New("boom")
	for _, c := range []struct {
		name   string
		change func(*fakeRound)
		want   string
		joins  int
	}{
		{"join error", func(f *fakeRound) {
			f.run.Join = func(context.Context, reviewJoin) (reviewJoined, error) { return reviewJoined{}, boom }
		}, "Check the join of the reviewer", 0},
		{"another session number", func(f *fakeRound) {
			f.run.Join = func(_ context.Context, j reviewJoin) (reviewJoined, error) {
				f.joins = append(f.joins, j)
				return reviewJoined{Session: j.Number + 1, Ended: reviewround.Finished}, nil
			}
		}, "another session number than qory predicted", 1},
		{"copy error", func(f *fakeRound) {
			f.run.Copy = func(context.Context, string) (reviewcopy.Folder, error) { return reviewcopy.Folder{}, boom }
		}, "Check the reviewer's copy", 0},
		{"report folder error", func(f *fakeRound) {
			f.run.ReportRoot = func(context.Context, string) (string, error) { return "", boom }
		}, "Check the reviewer's report folder", 0},
		{"bad session", func(f *fakeRound) {
			f.run.ReportRoot = func(context.Context, string) (string, error) { return "relative", nil }
		}, "absolute, clean report folder", 0},
		{"no reviewer", func(f *fakeRound) { f.run.Reviewers = nil }, "at least one reviewer", 0},
		{"reviewer twice", func(f *fakeRound) {
			f.run.Reviewers = []reviewerAgent{{Name: "a"}, {Name: "a"}}
		}, "List each reviewer once", 0},
		{"reviewer named like the suite", func(f *fakeRound) {
			f.run.Reviewers = []reviewerAgent{{Name: "full-suite"}}
		}, "not full-suite", 0},
		{"verdict error", func(f *fakeRound) {
			f.run.SendVerdict = func(context.Context, string, []runapi.Point) error { return boom }
		}, "boom", 2},
		{"cancelled", func(f *fakeRound) {}, "context canceled", 0},
	} {
		f := newFakeRound(t, "code-reviewer", "test-reviewer")
		c.change(f)
		ctx := context.Background()
		if c.name == "cancelled" {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		_, err := runReviewRound(ctx, f.run)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
			continue
		}
		if first, _, _ := strings.Cut(err.Error(), "\n"); len(first) > 100 {
			t.Errorf("%s: first line has %d characters", c.name, len(first))
		}
		if len(f.nexts) != 0 {
			t.Errorf("%s: sent the requests %v after the error", c.name, f.nexts)
		}
		if c.joins > 0 && len(f.joins) != c.joins {
			t.Errorf("%s: %d joins, want %d", c.name, len(f.joins), c.joins)
		}
	}
}

func TestOpenFindingsReplacesTheOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open-findings.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenFindings(path, reviewround.OpenFindings{Round: 2, Reviewers: []reviewround.ReviewerFindings{}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"round": 2`) {
		t.Errorf("file = %s", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("folder has %d files, want only open-findings.json", len(entries))
	}
	if err := writeOpenFindings(filepath.Join(path, "x", "y"), reviewround.OpenFindings{Round: 1}); err == nil || !strings.HasPrefix(err.Error(), "Give the round a folder") {
		t.Errorf("a missing folder returned %v", err)
	}
}
