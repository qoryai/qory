package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/pushcheck"
	"github.com/qoryai/qory/internal/submission"
)

// programDocument is the document the step gives the program: two commits in one repository,
// the text of the session, and the draft [placeholder], which the program may fill.
func programDocument(t *testing.T, draft string) *submission.Submission {
	t.Helper()
	raw := `{"ref":"qory/wi_1","verdict":"passed","text":{"title":"Add a page","description":"The page lists orders."},"repositories":[` +
		`{"system":"git.example.com","path":"acme/shop","commit":"` + commitC + `","base":{"commit":"` + commitA + `"},` +
		`"commits":[{"commit":"` + commitB + `","step":"implement"},{"commit":"` + commitC + `","step":"unreviewed"}],"draft":` + draft + `}]}`
	if err := submission.Check([]byte(raw)); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	var doc submission.Submission
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	return &doc
}

// programFile is the document as a file with replace applied to its text: each pair of
// replace is a string and what takes its place.
func programFile(t *testing.T, doc *submission.Submission, replace ...string) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for i := 0; i < len(replace); i += 2 {
		if !strings.Contains(text, replace[i]) {
			t.Fatalf("the document has no %q:\n%s", replace[i], text)
		}
		text = strings.Replace(text, replace[i], replace[i+1], 1)
	}
	return []byte(text)
}

// TestAfterProgramByHowTheProgramEnded is the document that the step stores for each way the
// submit program ends and each file it leaves: its links and a draft are taken, a file that
// changes more than the links and the draft is discarded, and a failure or a timeout turns the
// draft none no-program into its reason, with the links the program wrote when its file is
// good.
func TestAfterProgramByHowTheProgramEnded(t *testing.T) {
	placeholder := string(submission.None(submission.NoneNoProgram, 0))
	pr := `{"number":7,"url":"https://forge.example/acme/shop/pull/7","title":"Add a page","state":"draft","action":"opened"}`
	doc := programDocument(t, placeholder)
	linked := programFile(t, doc, `"commit":"`+commitC+`","base"`, `"commit":"`+commitC+`","commit_url":"https://forge.example/c","base"`)
	drafted := programFile(t, doc, placeholder, pr)
	for _, c := range []struct {
		name   string
		end    pushcheck.ProgramEnd
		draft  string // the draft of the repository in the result, compact JSON
		linked bool   // whether the result has the program's commit_url
		whyHas string // a part of the cause, "" for none
	}{
		{"links only, exit 0", pushcheck.ProgramEnd{Submission: linked}, placeholder, true, ""},
		{"a draft, exit 0", pushcheck.ProgramEnd{Submission: drafted}, pr, false, ""},
		{"links, exit 1", pushcheck.ProgramEnd{Submission: linked, Exit: 1}, `{"none":"program-failed","exit":1}`, true, ""},
		{"no file, exit 2", pushcheck.ProgramEnd{FileErr: "the file is not there", Exit: 2}, `{"none":"program-failed","exit":2}`, false, "left no file"},
		{"a crash, exit 137", pushcheck.ProgramEnd{Submission: linked, Exit: 137}, `{"none":"program-failed","exit":1}`, true, ""},
		{"the engine could not start it", pushcheck.ProgramEnd{StartErr: "no such file"}, `{"none":"program-failed","exit":1}`, false, "left no file"},
		{"a timeout", pushcheck.ProgramEnd{TimedOut: true, Submission: linked}, `{"none":"program-timeout"}`, true, ""},
		{"a timeout with no file", pushcheck.ProgramEnd{TimedOut: true, FileErr: "the file is not there"}, `{"none":"program-timeout"}`, false, "left no file"},
		{"another commit, exit 0", pushcheck.ProgramEnd{Submission: programFile(t, doc, `"commit":"`+commitC+`","base"`, `"commit":"`+commitB+`","base"`)}, `{"none":"program-failed","exit":1}`, false, "system, the path or the commit"},
		{"another title, exit 0", pushcheck.ProgramEnd{Submission: programFile(t, doc, `"title":"Add a page"`, `"title":"Another"`)}, `{"none":"program-failed","exit":1}`, false, "the text"},
		{"another step, exit 0", pushcheck.ProgramEnd{Submission: programFile(t, doc, `"step":"implement"`, `"step":"review"`)}, `{"none":"program-failed","exit":1}`, false, "the commits"},
		{"another verdict, exit 0", pushcheck.ProgramEnd{Submission: programFile(t, doc, `"verdict":"passed"`, `"verdict":"escalate"`)}, `{"none":"program-failed","exit":1}`, false, "the verdict"},
		{"a reason it chose, exit 0", pushcheck.ProgramEnd{Submission: programFile(t, doc, placeholder, `{"none":"escalated"}`)}, `{"none":"program-failed","exit":1}`, false, "the draft"},
		{"not JSON, exit 0", pushcheck.ProgramEnd{Submission: []byte("{")}, `{"none":"program-failed","exit":1}`, false, "not a submission"},
		{"an unknown field, exit 0", pushcheck.ProgramEnd{Submission: programFile(t, doc, `"ref":`, `"extra":1,"ref":`)}, `{"none":"program-failed","exit":1}`, false, "not a submission"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, why := afterProgram(doc, c.end)
			if got == nil || len(got.Repositories) != 1 {
				t.Fatalf("the result is %+v", got)
			}
			var want, have any
			json.Unmarshal([]byte(c.draft), &want)
			json.Unmarshal(got.Repositories[0].Draft, &have)
			if !equalJSON(want, have) {
				t.Errorf("the draft is %s, want %s", got.Repositories[0].Draft, c.draft)
			}
			if has := got.Repositories[0].CommitURL != ""; has != c.linked {
				t.Errorf("the result has the program's link: %v, want %v", has, c.linked)
			}
			if (c.whyHas == "") != (why == "") || !strings.Contains(why, c.whyHas) {
				t.Errorf("the cause is %q, want one with %q", why, c.whyHas)
			}
			// The part of the document that the program may not change is the document's.
			if got.Ref != doc.Ref || got.Verdict != doc.Verdict || got.Text == nil || got.Text.Title != "Add a page" || len(got.Repositories[0].Commits) != 2 {
				t.Errorf("the result changed what the program may not: %+v", got)
			}
			if err := checkDocument(got); err != nil {
				t.Errorf("the result fails the format's check: %v", err)
			}
		})
	}
}

// checkDocument checks doc against the submission format.
func checkDocument(doc *submission.Submission) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return submission.Check(raw)
}

// equalJSON reports whether a and b, each decoded JSON, are equal.
func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// TestADraftThatIsNotThePlaceholderStaysWhatItWas is a repository whose draft is a reason
// other than no-program: the program may leave it and may not replace it, and a failure of
// the program does not change it.
func TestADraftThatIsNotThePlaceholderStaysWhatItWas(t *testing.T) {
	notPassed := string(submission.None(submission.NoneNotPassed, 0))
	doc := programDocument(t, notPassed)
	same := programFile(t, doc)
	if got, why := afterProgram(doc, pushcheck.ProgramEnd{Submission: same, Exit: 1}); why != "" || string(got.Repositories[0].Draft) != notPassed {
		t.Errorf("a program that failed: %s, %q, want the draft kept", got.Repositories[0].Draft, why)
	}
	pr := `{"number":7,"url":"https://forge.example/acme/shop/pull/7","title":"Add a page","state":"draft","action":"opened"}`
	if got, why := afterProgram(doc, pushcheck.ProgramEnd{Submission: programFile(t, doc, notPassed, pr)}); !strings.Contains(why, "the draft") || string(got.Repositories[0].Draft) != notPassed {
		t.Errorf("a program that opened a draft where none was due: %s, %q, want the draft kept and the cause", got.Repositories[0].Draft, why)
	}
}
