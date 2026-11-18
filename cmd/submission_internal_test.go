package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/submission"
)

const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	commitC = "cccccccccccccccccccccccccccccccccccccccc"
)

// TestTheHandoffIsReadFromARegularFile is the hand-off file in each state: no path and no
// file are no hand-off, a file with a title and steps is read, and a link, a directory, a
// file of invalid JSON and a file over the limit are errors that leave no hand-off.
func TestTheHandoffIsReadFromARegularFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if h, err := readHandoff(""); h != nil || err != nil {
		t.Errorf("no path: %v, %v", h, err)
	}
	if h, err := readHandoff(filepath.Join(dir, "missing.json")); h != nil || err != nil {
		t.Errorf("no file: %v, %v", h, err)
	}
	good := write("good.json", `{"text":{"title":"Add a page","description":"The page lists orders."},"steps":{"`+commitA+`":"implement"}}`)
	h, err := readHandoff(good)
	if err != nil || h == nil || h.Text.Title != "Add a page" || h.Steps[commitA] != "implement" {
		t.Errorf("the file: %+v, %v", h, err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if h, err := readHandoff(link); h != nil || err == nil {
		t.Errorf("a link: %v, %v", h, err)
	}
	if h, err := readHandoff(dir); h != nil || err == nil {
		t.Errorf("a directory: %v, %v", h, err)
	}
	if h, err := readHandoff(write("bad.json", "{")); h != nil || err == nil {
		t.Errorf("invalid JSON: %v, %v", h, err)
	}
	big := write("big.json", `{"text":{"title":"`+strings.Repeat("x", handoffMax)+`"}}`)
	if h, err := readHandoff(big); h != nil || err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("a file over the limit: %v, %v", h, err)
	}
}

// TestACommitWithNoStepIsUnreviewed is the ledger of a push of 3 commits where the hand-off
// lists the step of one: the other two are unreviewed, and so is each commit with no
// hand-off at all.
func TestACommitWithNoStepIsUnreviewed(t *testing.T) {
	branch := &preparation.WorkBranch{Ref: "qory/wi_1"}
	repositories := []submission.Repository{{
		System: "git.example.com", Path: "acme/shop", Commit: commitC, Base: &submission.Base{Commit: commitA},
		Commits: []submission.Commit{{Commit: commitB}, {Commit: commitC}},
	}}
	h := &handoff{Steps: map[string]string{commitB: "implement"}}
	doc := assembleSubmission(branch, repositories, submission.VerdictPassed, h, false)
	got := doc.Repositories[0].Commits
	if got[0].Step != "implement" || got[1].Step != stepUnreviewed {
		t.Errorf("the steps are %+v", got)
	}
	for _, c := range assembleSubmission(branch, repositories, submission.VerdictNone, nil, false).Repositories[0].Commits {
		if c.Step != stepUnreviewed {
			t.Errorf("commit %s has the step %q with no hand-off", c.Commit, c.Step)
		}
	}
}

// TestTheDraftIsNoneWithItsReason is each reason a repository has no draft, by the verdict
// and the text: escalate, a verdict that is not passed, passed with no text, and passed with
// text and no program running; and the document of each passes the contract's schema.
func TestTheDraftIsNoneWithItsReason(t *testing.T) {
	branch := &preparation.WorkBranch{Ref: "qory/wi_1"}
	repositories := []submission.Repository{{System: "git.example.com", Path: "acme/shop", Commit: commitC, Base: &submission.Base{Commit: commitA}, Commits: []submission.Commit{{Commit: commitC}}}}
	text := &handoff{Text: &submission.Text{Title: "Add a page"}}
	for name, c := range map[string]struct {
		verdict string
		h       *handoff
		want    string
	}{
		"escalate":       {submission.VerdictEscalate, text, submission.NoneEscalated},
		"changes":        {submission.VerdictChangesRequested, text, submission.NoneNotPassed},
		"no verdict":     {submission.VerdictNone, text, submission.NoneNotPassed},
		"passed no text": {submission.VerdictPassed, nil, submission.NoneNoText},
		"passed, text":   {submission.VerdictPassed, text, submission.NoneNoProgram},
	} {
		t.Run(name, func(t *testing.T) {
			doc := assembleSubmission(branch, repositories, c.verdict, c.h, false)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := submission.Check(raw); err != nil {
				t.Fatalf("%v\n%s", err, raw)
			}
			if !strings.Contains(string(raw), `"`+c.want+`"`) {
				t.Errorf("the document has no reason %q:\n%s", c.want, raw)
			}
		})
	}
}
