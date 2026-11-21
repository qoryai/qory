package cmd

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/reviewcopy"
	"github.com/qoryai/qory/internal/stack"
)

// reviewCopy makes a reviewer copy folder in a short temporary folder.
func reviewCopy(t *testing.T) reviewcopy.Folder {
	t.Helper()
	f, err := reviewcopy.New(filepath.Join(t.TempDir(), "copy"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNewReviewSessionBuildsTheMountsAndTheModel(t *testing.T) {
	copyDir := reviewCopy(t)
	reports := filepath.Join(t.TempDir(), "code-reviewer")
	tiers := map[string]stack.Tier{"balanced": {Model: "model-b", Effort: "high"}, "light": {Model: "model-l"}}
	for _, c := range []struct {
		name, tier, model, effort string
	}{
		{"code-reviewer", "balanced", "model-b", "high"},
		{"code-reviewer", "light", "model-l", ""},
		{"code-reviewer", "thinking", "", ""}, // a tier with no map: the main model
		{"code-reviewer", "", "", ""},
	} {
		spec, err := newReviewSession(reviewSessionInput{
			Reviewer: reviewerAgent{c.name, c.tier}, Copy: copyDir, ReportRoot: reports,
			Provider: "anthropic", Tiers: tiers, TimeLimit: 20 * time.Minute,
		})
		if err != nil {
			t.Fatalf("tier %q: %v", c.tier, err)
		}
		if spec.Model != c.model || spec.Effort != c.effort || spec.Provider != "anthropic" || spec.TimeLimit != 20*time.Minute {
			t.Errorf("tier %q: spec = %+v", c.tier, spec)
		}
		if spec.Report != filepath.Join(reports, "report.json") || spec.ReportRoot != reports {
			t.Errorf("report = %q in %q", spec.Report, spec.ReportRoot)
		}
		writable := 0
		for _, m := range spec.Mounts {
			if !m.ReadOnly {
				writable++
				if m.Path != reports {
					t.Errorf("writable mount %q, want the report folder", m.Path)
				}
				continue
			}
			if m.Path != copyDir.Commit() && m.Path != copyDir.Diff() {
				t.Errorf("read-only mount %q is not the commit folder or the diff", m.Path)
			}
		}
		if writable != 1 || len(spec.Mounts) != 3 {
			t.Errorf("mounts = %+v, want the commit and the diff read-only and the report folder writable", spec.Mounts)
		}
	}
}

func TestNewReviewSessionRefuses(t *testing.T) {
	copyDir := reviewCopy(t)
	reports := filepath.Join(t.TempDir(), "r")
	ok := reviewSessionInput{Reviewer: reviewerAgent{"code-reviewer", "light"}, Copy: copyDir, ReportRoot: reports}
	for _, c := range []struct {
		name   string
		change func(*reviewSessionInput)
		want   string
	}{
		{"name", func(in *reviewSessionInput) { in.Reviewer.Name = "Code Reviewer" }, "Name each reviewer"},
		{"empty name", func(in *reviewSessionInput) { in.Reviewer.Name = "" }, "Name each reviewer"},
		{"tier", func(in *reviewSessionInput) { in.Reviewer.Tier = "heavy" }, "a tier of light, balanced, thinking"},
		{"limit", func(in *reviewSessionInput) { in.TimeLimit = -time.Minute }, "time limit of 0 or more"},
		{"no copy", func(in *reviewSessionInput) { in.Copy = reviewcopy.Folder{} }, "the copy that reviewcopy made"},
		{"relative report folder", func(in *reviewSessionInput) { in.ReportRoot = "reports" }, "absolute, clean report folder"},
		{"unclean report folder", func(in *reviewSessionInput) { in.ReportRoot = reports + "/../r" }, "absolute, clean report folder"},
		{"report folder is the copy", func(in *reviewSessionInput) { in.ReportRoot = copyDir.Root() }, "apart from its copy"},
		{"report folder in the copy", func(in *reviewSessionInput) { in.ReportRoot = filepath.Join(copyDir.Root(), "out") }, "apart from its copy"},
		{"copy in the report folder", func(in *reviewSessionInput) { in.ReportRoot = filepath.Dir(copyDir.Root()) }, "apart from its copy"},
		{"report in .git", func(in *reviewSessionInput) { in.ReportRoot = filepath.Join(reports, ".git") }, "tool guard denies a write"},
		{"long report path", func(in *reviewSessionInput) { in.ReportRoot = filepath.Join(reports, strings.Repeat("a", 4100)) }, "more than 4096"},
	} {
		in := ok
		c.change(&in)
		_, err := newReviewSession(in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
}

func TestReviewPathProblem(t *testing.T) {
	for path, want := range map[string]string{
		"/r/report.json":      "",
		"r/report.json":       "is not absolute",
		"/r/./report.json":    "has a . part",
		"/r/../report.json":   "has a .. part",
		"/r//report.json":     "has a double or trailing slash",
		"/r/report.json/":     "has a double or trailing slash",
		"/r/a\x00b":           "has a NUL",
		"/r/.claude/rep.json": "has a .claude part, where the harness's tool guard denies a write",
		"/r/.gitignore/x":     "",
	} {
		if got := reviewPathProblem(path); got != want {
			t.Errorf("reviewPathProblem(%q) = %q, want %q", path, got, want)
		}
	}
}
