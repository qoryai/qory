package cmd

import (
	"encoding/json"
	"fmt"
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

// handoffIn writes content as the hand-off file of a new checkout and returns the checkout.
func handoffIn(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(handoffPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handoffPath(root), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestTheHandoffIsReadFromARegularFile is the hand-off file in each state: no path and no
// file are no hand-off, a file with a title and steps is read, and a link, a directory, a
// file of invalid JSON and a file over the limit are errors that leave no hand-off.
func TestTheHandoffIsReadFromARegularFile(t *testing.T) {
	if h, err := readHandoff(""); h != nil || err != nil {
		t.Errorf("no path: %v, %v", h, err)
	}
	if h, err := readHandoff(t.TempDir()); h != nil || err != nil {
		t.Errorf("no file: %v, %v", h, err)
	}
	good := `{"text":{"title":"Add a page","description":"The page lists orders."},"steps":{"` + commitA + `":"implement"}}`
	root := handoffIn(t, good)
	h, err := readHandoff(root)
	if err != nil || h == nil || h.Text.Title != "Add a page" || h.Steps[commitA] != "implement" || h.dropped != nil {
		t.Errorf("the file: %+v, %v", h, err)
	}
	// A link as the file, though the target is in the checkout.
	linked := handoffIn(t, "{}")
	if err := os.Remove(handoffPath(linked)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "real.json"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(linked, "real.json"), handoffPath(linked)); err != nil {
		t.Fatal(err)
	}
	if h, err := readHandoff(linked); h != nil || err == nil {
		t.Errorf("a link: %v, %v", h, err)
	}
	dir := handoffIn(t, "{}")
	if err := os.Remove(handoffPath(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(handoffPath(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if h, err := readHandoff(dir); h != nil || err == nil {
		t.Errorf("a directory: %v, %v", h, err)
	}
	if h, err := readHandoff(handoffIn(t, "{")); h != nil || err == nil {
		t.Errorf("invalid JSON: %v, %v", h, err)
	}
	big := handoffIn(t, `{"text":{"title":"`+strings.Repeat("x", handoffMax)+`"}}`)
	if h, err := readHandoff(big); h != nil || err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("a file over the limit: %v, %v", h, err)
	}
	// A file that is exactly the limit passes the size check and is read.
	body := `{"text":{"title":"T","description":""},"steps":{}}`
	exact := handoffIn(t, body+strings.Repeat(" ", handoffMax-len(body)))
	if h, err := readHandoff(exact); err != nil || h == nil {
		t.Errorf("a file of %d bytes: %v, %v", handoffMax, h, err)
	}
}

// TestALinkedFolderOfTheHandoffIsNotFollowed is a .qory/handoff that the agent made a link to
// a folder of the host, and a .qory that is one: the host's file with the right name in that
// folder is not read.
func TestALinkedFolderOfTheHandoffIsNotFollowed(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "submission.json"), []byte(`{"text":{"title":"From the host"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, link := range map[string]string{"handoff": filepath.Join(".qory", "handoff"), ".qory": ".qory"} {
		root := t.TempDir()
		target := outside
		if name == ".qory" {
			// The folder outside has handoff/submission.json.
			target = t.TempDir()
			if err := os.MkdirAll(filepath.Join(target, "handoff"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "handoff", "submission.json"), []byte(`{"text":{"title":"From the host"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.MkdirAll(filepath.Join(root, ".qory"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
		h, err := readHandoff(root)
		if h != nil || err == nil {
			t.Errorf("a linked %s: %+v, %v, want an error and no hand-off", name, h, err)
		}
	}
}

// TestAHandoffPartOverTheFormatsLimitIsDropped is a title, a description and a step over the
// limits of the submission format: the part is dropped, the others stay, and the document
// that is built from it passes the format's check.
func TestAHandoffPartOverTheFormatsLimitIsDropped(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) }
	cases := []struct {
		name, file string
		text       bool
		steps      map[string]string
		dropped    int
	}{
		{"a title of 256 characters", `{"text":{"title":"` + long(256) + `","description":"d"},"steps":{"` + commitA + `":"` + long(128) + `"}}`, true, map[string]string{commitA: long(128)}, 0},
		{"a title of 257", `{"text":{"title":"` + long(257) + `","description":"d"},"steps":{"` + commitA + `":"ok"}}`, false, map[string]string{commitA: "ok"}, 1},
		{"a description of 20001", `{"text":{"title":"t","description":"` + long(20001) + `"}}`, false, nil, 1},
		{"a step of 129", `{"text":{"title":"t","description":"d"},"steps":{"` + commitA + `":"` + long(129) + `","` + commitB + `":"ok"}}`, true, map[string]string{commitB: "ok"}, 1},
		{"both", `{"text":{"title":"` + long(300) + `"},"steps":{"` + commitA + `":"` + long(200) + `"}}`, false, map[string]string{}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := readHandoff(handoffIn(t, c.file))
			if err != nil || h == nil {
				t.Fatalf("%v, %v", h, err)
			}
			if (h.text() != nil) != c.text || len(h.dropped) != c.dropped {
				t.Errorf("the text is %v and %d parts are dropped, want %v and %d: %v", h.text() != nil, len(h.dropped), c.text, c.dropped, h.dropped)
			}
			for commit, want := range c.steps {
				if h.Steps[commit] != want {
					t.Errorf("the step of %s is %q", commit, h.Steps[commit])
				}
			}
			if len(h.Steps) != len(c.steps) {
				t.Errorf("the steps are %v, want %d", h.Steps, len(c.steps))
			}
			branch := &preparation.WorkBranch{Ref: "qory/wi_1"}
			repositories := []submission.Repository{{System: "git.example.com", Path: "acme/shop", Commit: commitB, Base: &submission.Base{Commit: commitA}, Commits: []submission.Commit{{Commit: commitA}, {Commit: commitB}}}}
			raw, err := json.Marshal(assembleSubmission(branch, repositories, submission.VerdictPassed, h))
			if err != nil {
				t.Fatal(err)
			}
			if err := submission.Check(raw); err != nil {
				t.Errorf("%v\n%s", err, raw)
			}
		})
	}
}

// TestAStepOfACommitOutsideTheRangeIsNotUsed is a hand-off that gives a step for a commit the
// run did not make: the document has the steps of the run's commits alone.
func TestAStepOfACommitOutsideTheRangeIsNotUsed(t *testing.T) {
	branch := &preparation.WorkBranch{Ref: "qory/wi_1"}
	repositories := []submission.Repository{{System: "git.example.com", Path: "acme/shop", Commit: commitB, Base: &submission.Base{Commit: commitA}, Commits: []submission.Commit{{Commit: commitB}}}}
	h := &handoff{Steps: map[string]string{commitA: "base", commitC: "later", commitB: "implement"}}
	raw, err := json.Marshal(assembleSubmission(branch, repositories, submission.VerdictNone, h))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"step":"base"`) || strings.Contains(string(raw), `"step":"later"`) || !strings.Contains(string(raw), `"step":"implement"`) {
		t.Errorf("the document is %s", raw)
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
	doc := assembleSubmission(branch, repositories, submission.VerdictPassed, h)
	got := doc.Repositories[0].Commits
	if got[0].Step != "implement" || got[1].Step != stepUnreviewed {
		t.Errorf("the steps are %+v", got)
	}
	for _, c := range assembleSubmission(branch, repositories, submission.VerdictNone, nil).Repositories[0].Commits {
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
			doc := assembleSubmission(branch, repositories, c.verdict, c.h)
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

// TestAHandoffStepKeyIsAFullLowerCaseCommitID is the key of a step: 40 or 64 lower-case
// hexadecimal digits, as the format's commit has them. Any other key is dropped, counted in one
// line with no content of the key, and the commit with that id has the step unreviewed.
func TestAHandoffStepKeyIsAFullLowerCaseCommitID(t *testing.T) {
	sha256 := strings.Repeat("ab12cd34", 8)
	cases := []struct {
		name, key string
		kept      bool
	}{
		{"40 digits", commitA, true},
		{"64 digits", sha256, true},
		{"upper case, 40", strings.ToUpper(commitA), false},
		{"upper case, 64", strings.ToUpper(sha256), false},
		{"a short id", commitA[:12], false},
		{"41 digits", commitA + "a", false},
		{"not hexadecimal", strings.Repeat("g", 40), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, err := readHandoff(handoffIn(t, `{"steps":{"`+c.key+`":"implement"}}`))
			if err != nil || h == nil {
				t.Fatalf("%v, %v", h, err)
			}
			if _, kept := h.Steps[c.key]; kept != c.kept {
				t.Errorf("the step is kept: %v, want %v", kept, c.kept)
			}
			if got, want := len(h.dropped), map[bool]int{true: 0, false: 1}[c.kept]; got != want {
				t.Errorf("%d lines are dropped, want %d: %v", got, want, h.dropped)
			}
			if !c.kept && strings.Contains(h.dropped[0], c.key) {
				t.Errorf("the line has the key: %q", h.dropped[0])
			}
		})
	}
}

// TestAHandoffTitleWithAControlCharacterDropsTheText is a title with a tab, a line end, a NUL,
// a DEL, a C1 control, a bidirectional override, a zero-width space or a line separator, which
// the format's title pattern or the contract's draft_title refuses: the text is dropped with a
// line that has the position of the character, so the program is never given a title that its
// draft_title line may not carry back. A title with none stays.
func TestAHandoffTitleWithAControlCharacterDropsTheText(t *testing.T) {
	for title, at := range map[string]int{`Add\ta page`: 4, `Add a page\n`: 11, `\u0000Add`: 1, `Add\u007f`: 4, `é\u001f`: 2, `Add\u0085`: 4, `a\u202eb`: 2, `a\u200bb`: 2, `a\u2028b`: 2, `Add a page é`: 0} {
		t.Run(title, func(t *testing.T) {
			h, err := readHandoff(handoffIn(t, `{"text":{"title":"`+title+`","description":"d"}}`))
			if err != nil || h == nil {
				t.Fatalf("%v, %v", h, err)
			}
			if at == 0 {
				if h.text() == nil || len(h.dropped) != 0 {
					t.Errorf("the text is %v with the lines %v, want it kept", h.text(), h.dropped)
				}
				return
			}
			if want := fmt.Sprintf(textDroppedControl, at); h.text() != nil || len(h.dropped) != 1 || h.dropped[0] != want {
				t.Errorf("the text is %v with the lines %q, want it dropped with %q", h.text(), h.dropped, want)
			}
		})
	}
}

// TestAHandoffThatIsNotJSONNamesNoContent is a file whose text is not JSON, or has a member of
// the wrong type: the cause says where or which member, and has no character, member name or
// value of the file.
func TestAHandoffThatIsNotJSONNamesNoContent(t *testing.T) {
	for name, c := range map[string]struct{ file, want string }{
		"text":           {`Secret-token-1234 {`, "the file is not JSON: syntax error at byte 1"},
		"cut":            {`{"text":{"title":"x"`, "the file is not JSON: syntax error at byte 20"},
		"a string text":  {`{"text":"secret-title"}`, "the member text of the file has the wrong type"},
		"a number steps": {`{"steps":{"` + commitA + `":12345}}`, "the member steps of the file has the wrong type"},
		"an array":       {`["secret"]`, "the file is not JSON of the form of the hand-off"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readHandoff(handoffIn(t, c.file))
			if err == nil || err.Error() != c.want {
				t.Fatalf("the error is %v, want %q", err, c.want)
			}
			for _, content := range []string{"Secret", "secret", "invalid character", "12345", commitA} {
				if strings.Contains(err.Error(), content) {
					t.Errorf("the cause has %q: %v", content, err)
				}
			}
		})
	}
}
