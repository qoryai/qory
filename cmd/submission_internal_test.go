package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/integrations/contracts"
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
// a DEL, a C1 control, a bidirectional override, a zero-width space, a line separator or a tag
// character: the text is dropped with a line that has the position of the character, so the
// program is never given a title that its draft_title line may not return. A title with none
// stays, one with U+200C or an emoji sequence with U+200D included.
func TestAHandoffTitleWithAControlCharacterDropsTheText(t *testing.T) {
	for title, at := range map[string]int{`Add\ta page`: 4, `Add a page\n`: 11, `\u0000Add`: 1, `Add\u007f`: 4, `é\u001f`: 2, `Add\u0085`: 4, `a\u202eb`: 2, `a\u200bb`: 2, `a\u2028b`: 2, `a\udb40\udc41b`: 2, `Add a page é`: 0, `a\u200db`: 0, `a\u200cb`: 0, `\ud83d\udc68\u200d\ud83d\udc69\u200d\ud83d\udc67 home`: 0} {
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

// TestAHandoffDescriptionWithANulOrAMarkerLineDropsTheText is a description with a NUL character,
// or with a line of the block's marker, alone or followed by spaces, tabs or a carriage return:
// the text is dropped with a line that has the position, so the draft's block cannot end early.
// A marker inside a line, or with another character after it, stays.
func TestAHandoffDescriptionWithANulOrAMarkerLineDropsTheText(t *testing.T) {
	for _, c := range []struct {
		name, description string
		want              string
	}{
		{"a NUL", `Fix\u0000it`, fmt.Sprintf(textDroppedNUL, 4)},
		{"the end marker", `A\n<!-- qory:end -->\nB`, fmt.Sprintf(textDroppedMarker, 2)},
		{"the start marker", `<!-- qory:start -->`, fmt.Sprintf(textDroppedMarker, 1)},
		{"a marker with spaces and a carriage return", `A\nB\n<!-- qory:end -->  \t\r\nC`, fmt.Sprintf(textDroppedMarker, 3)},
		{"a marker inside a line", `see <!-- qory:end --> here`, ""},
		{"a marker followed by text", `<!-- qory:end --> more`, ""},
		{"a marker with a space before it", `A\n <!-- qory:end -->`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, err := readHandoff(handoffIn(t, `{"text":{"title":"T","description":"`+c.description+`"}}`))
			if err != nil || h == nil {
				t.Fatalf("%v, %v", h, err)
			}
			if c.want == "" {
				if h.text() == nil || len(h.dropped) != 0 {
					t.Errorf("the text is %v with the lines %v, want it kept", h.text(), h.dropped)
				}
				return
			}
			if h.text() != nil || len(h.dropped) != 1 || h.dropped[0] != c.want {
				t.Errorf("the text is %v with the lines %q, want it dropped with %q", h.text(), h.dropped, c.want)
			}
		})
	}
}

// TestAHandoffStepWithAControlCharacterIsDropped is a step with a tab or a NUL: the step is
// dropped with a line that has the commit, its commit is unreviewed, and a step without one stays.
func TestAHandoffStepWithAControlCharacterIsDropped(t *testing.T) {
	bad, bad2, bad3, bad4, good := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("d", 40), strings.Repeat("e", 40), strings.Repeat("c", 40)
	h, err := readHandoff(handoffIn(t, `{"steps":{"`+bad+`":"plan\tb","`+bad2+`":"x\u0000","`+bad3+`":"a\u202eb","`+bad4+`":"a\u200bb","`+good+`":"plan"}}`))
	if err != nil || h == nil {
		t.Fatalf("%v, %v", h, err)
	}
	want := []string{fmt.Sprintf(textDroppedStepControl, bad), fmt.Sprintf(textDroppedStepControl, bad2), fmt.Sprintf(textDroppedStepControl, bad4), fmt.Sprintf(textDroppedStepControl, bad3)}
	slices.Sort(want)
	got := slices.Clone(h.dropped)
	slices.Sort(got)
	if !slices.Equal(got, want) || h.stepOf(bad3) != stepUnreviewed || h.stepOf(bad4) != stepUnreviewed || h.stepOf(good) != "plan" {
		t.Errorf("the lines are %q, want %q, with the bidirectional and the zero-width steps unreviewed and plan kept", got, want)
	}
}

// TestAHandoffTitleOfSpacesDropsTheText is a title of spaces only, of U+0020, U+00A0 or another
// space of the draft title's rule, or of spaces with U+200C and U+200D, which show nothing: the
// text is dropped with a line and the draft has no title. A title with one other character stays.
func TestAHandoffTitleOfSpacesDropsTheText(t *testing.T) {
	for title, kept := range map[string]bool{`   `: false, `\u00a0\u00a0`: false, ` \u3000\u2009 `: false, ` a `: true,
		`\u200c`: false, `\u200d`: false, ` \u200c\u200d `: false, `\u200c a`: true, `\ud83d\udc68\u200d\ud83d\udc69`: true} {
		h, err := readHandoff(handoffIn(t, `{"text":{"title":"`+title+`","description":"d"}}`))
		if err != nil || h == nil {
			t.Fatalf("%v, %v", h, err)
		}
		if got := h.text() != nil; got != kept || (!kept && !slices.Equal(h.dropped, []string{textDroppedBlank})) || (kept && len(h.dropped) != 0) {
			t.Errorf("title %q: kept %v with the lines %q, want kept %v", title, got, h.dropped, kept)
		}
	}
}

// TestAHandoffTitleOfInvisibleCharactersIsTheKnownDifferenceFromThePinnedContract is a title of
// only spaces, U+200C and U+200D: the hand-off drops it, and the pinned contract (integrations
// e4bf534a) still accepts it as a draft_title line. When the pin moves to a contract that stops
// it, this test fails: move these titles into the agreement table below and delete this test.
func TestAHandoffTitleOfInvisibleCharactersIsTheKnownDifferenceFromThePinnedContract(t *testing.T) {
	for _, title := range []string{"\u200c", "\u200d", " \u200c\u200d "} {
		lines := strings.Join([]string{
			contracts.SubmitKeyDraftNumber + " 1", contracts.SubmitKeyDraftURL + " https://git.example.com/a",
			contracts.SubmitKeyDraftTitle + " " + title, contracts.SubmitKeyDraftState + " draft", contracts.SubmitKeyDraftAction + " opened",
		}, "\n") + "\n"
		if contracts.ParseSubmitLines(lines, "git.example.com", contracts.DraftOpen, nil).Draft == nil {
			t.Errorf("title %q: the pinned contract stops it now; move it into the agreement table", title)
		}
		if visibleTitle(title) {
			t.Errorf("title %q: the hand-off keeps it", title)
		}
	}
}

// TestTheHandoffTitleRuleAgreesWithTheContractOnTheseTitles is a title that the pinned contract
// accepts or does not accept as the draft_title line of the submit program: the hand-off keeps
// each one that the contract accepts and drops each one that it does not, so the program's line
// may return every title that the hand-off keeps. The hand-off also drops the format characters
// that the contract does not list, such as U+0600 and the tag character U+E0041, which this table leaves out.
func TestTheHandoffTitleRuleAgreesWithTheContractOnTheseTitles(t *testing.T) {
	for _, title := range []string{
		"Add a page", "Add \u00e9 \U0001F468\u200d\U0001F469\u200d\U0001F467", "a\u200cb", " a\u00a0", strings.Repeat("a", maxTitle),
		"a\u200bb", "a\u202eb", "a\u2028b", "a\u2029b", "a\u00adb", "a\ufeffb", "a\u061cb", "a\x07b", "a\u0085b",
		"   ", "\u00a0\u3000\u2009", strings.Repeat("a", maxTitle+1),
	} {
		lines := strings.Join([]string{
			contracts.SubmitKeyDraftNumber + " 1", contracts.SubmitKeyDraftURL + " https://git.example.com/a",
			contracts.SubmitKeyDraftTitle + " " + title, contracts.SubmitKeyDraftState + " draft", contracts.SubmitKeyDraftAction + " opened",
		}, "\n") + "\n"
		contractAccepts := contracts.ParseSubmitLines(lines, "git.example.com", contracts.DraftOpen, nil).Draft != nil
		keeps := controlAt(title) == 0 && visibleTitle(title) && utf8.RuneCountInString(title) <= maxTitle
		if contractAccepts != keeps {
			t.Errorf("title %q: the contract accepts it: %v, the hand-off keeps it: %v", title, contractAccepts, keeps)
		}
	}
}
