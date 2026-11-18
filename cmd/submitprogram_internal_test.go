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
	pr := `{"number":7,"url":"https://git.example.com/acme/shop/pull/7","title":"Add a page","state":"draft","action":"opened"}`
	doc := programDocument(t, placeholder)
	linked := programFile(t, doc, `"commit":"`+commitC+`","base"`, `"commit":"`+commitC+`","commit_url":"https://git.example.com/acme/shop/commit/`+commitC+`","base"`)
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
	pr := `{"number":7,"url":"https://git.example.com/acme/shop/pull/7","title":"Add a page","state":"draft","action":"opened"}`
	if got, why := afterProgram(doc, pushcheck.ProgramEnd{Submission: programFile(t, doc, notPassed, pr)}); !strings.Contains(why, "the draft") || string(got.Repositories[0].Draft) != notPassed {
		t.Errorf("a program that opened a draft where none was due: %s, %q, want the draft kept and the cause", got.Repositories[0].Draft, why)
	}
}

// TestTheValuesThatTheProgramLeavesAreChecked is each link and draft value that the program
// may write: an https link on the repository's system at the path of the commit or the pull
// request, a plain number, and a title with no control character. A value that breaks a rule
// discards the program's file, with a cause that names the field and none of its content.
func TestTheValuesThatTheProgramLeavesAreChecked(t *testing.T) {
	placeholder := string(submission.None(submission.NoneNoProgram, 0))
	doc := programDocument(t, placeholder)
	site := "https://git.example.com/acme/shop"
	draftOf := func(fields string) string { return `{` + fields + `}` }
	const tail = `,"title":"Add a page","state":"draft","action":"opened"`
	pull := func(url string) string { return `"number":7,"url":"` + url + `"` + tail }
	commit := func(link string) []byte {
		return programFile(t, doc, `"commit":"`+commitC+`","base"`, `"commit":"`+commitC+`","commit_url":"`+link+`","base"`)
	}
	cases := []struct {
		name string
		file []byte
		ok   bool
		why  string
	}{
		{"a good commit link", commit(site + "/commit/" + commitC), true, ""},
		{"the path in another letter case", commit("https://git.example.com/ACME/Shop/commit/" + commitC), true, ""},
		{"the link of another commit", commit(site + "/commit/" + commitB), false, "commit_url"},
		{"javascript:", commit("javascript:alert(1)"), false, "commit_url"},
		{"file:", commit("file:///etc/passwd"), false, "commit_url"},
		{"http", commit("http://git.example.com/acme/shop/commit/" + commitC), false, "commit_url"},
		{"another host", commit("https://evil.example/acme/shop/commit/" + commitC), false, "commit_url"},
		{"a user in the link", commit("https://user:pw@git.example.com/acme/shop/commit/" + commitC), false, "commit_url"},
		{"a port", commit("https://git.example.com:8443/acme/shop/commit/" + commitC), false, "commit_url"},
		{"a query", commit(site + "/commit/" + commitC + "?x=1"), false, "commit_url"},
		{"a fragment", commit(site + "/commit/" + commitC + "#x"), false, "commit_url"},
		{"another repository", commit("https://git.example.com/acme/other/commit/" + commitC), false, "commit_url"},
		{"a control character", commit(site + "/commit/" + commitC + `\u0007`), false, "commit_url"},
		{"a base link of another commit", programFile(t, doc, `"base":{"commit":"`+commitA+`"}`, `"base":{"commit":"`+commitA+`","commit_url":"`+site+`/commit/`+commitC+`"}`), false, "commit_url"},
		{"a good draft", programFile(t, doc, placeholder, draftOf(pull(site+"/pull/7"))), true, ""},
		{"a draft with javascript:", programFile(t, doc, placeholder, draftOf(pull("javascript:alert(1)"))), false, "url"},
		{"a draft with file:", programFile(t, doc, placeholder, draftOf(pull("file:///etc/passwd"))), false, "url"},
		{"a draft on another host", programFile(t, doc, placeholder, draftOf(pull("https://evil.example/acme/shop/pull/7"))), false, "url"},
		{"a draft with another number", programFile(t, doc, placeholder, draftOf(pull(site+"/pull/8"))), false, "url"},
		{"a draft with a control character in the title", programFile(t, doc, placeholder, draftOf(`"number":7,"url":"`+site+`/pull/7","title":"Add\u0007 page","state":"draft","action":"opened"`)), false, "title"},
		{"a draft with a tab in the title", programFile(t, doc, placeholder, draftOf(`"number":7,"url":"`+site+`/pull/7","title":"Add\tpage","state":"draft","action":"opened"`)), false, "title"},
		{"a number written 7.0", programFile(t, doc, placeholder, draftOf(`"number":7.0,"url":"`+site+`/pull/7","title":"t","state":"draft","action":"opened"`)), false, "number"},
		{"a number written 1e0", programFile(t, doc, placeholder, draftOf(`"number":1e0,"url":"`+site+`/pull/1","title":"t","state":"draft","action":"opened"`)), false, "number"},
		{"a number of 0", programFile(t, doc, placeholder, draftOf(`"number":0,"url":"`+site+`/pull/0","title":"t","state":"draft","action":"opened"`)), false, "number"},
		{"a number that is too large", programFile(t, doc, placeholder, draftOf(`"number":1234567890123456789,"url":"`+site+`/pull/1234567890123456789","title":"t","state":"draft","action":"opened"`)), false, "number"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := afterProgram(doc, pushcheck.ProgramEnd{Submission: c.file})
			if c.ok != (why == "") {
				t.Fatalf("the cause is %q, want a good file: %v", why, c.ok)
			}
			if c.ok {
				return
			}
			if !strings.Contains(why, c.why) || strings.Contains(why, "evil.example") || strings.Contains(why, "javascript") {
				t.Errorf("the cause is %q, want the field %q and no value", why, c.why)
			}
			if string(got.Repositories[0].Draft) != `{"none":"program-failed","exit":1}` || got.Repositories[0].CommitURL != "" {
				t.Errorf("the program's file is kept: %+v", got.Repositories[0])
			}
		})
	}
}

// TestTheDraftIsWrittenAgainAndAMemberTwiceDoesNotSurvive is a draft with a member twice, in
// each order: the draft that is stored is the one that qory writes from the five members, so
// a second url never reaches the stored version, and the first one is not hidden from the check.
func TestTheDraftIsWrittenAgainAndAMemberTwiceDoesNotSurvive(t *testing.T) {
	placeholder := string(submission.None(submission.NoneNoProgram, 0))
	doc := programDocument(t, placeholder)
	site := "https://git.example.com/acme/shop"
	good := `"url":"` + site + `/pull/7"`
	bad := `"url":"javascript:alert(1)"`
	for name, members := range map[string]string{"good last": bad + `,` + good, "bad last": good + `,` + bad} {
		t.Run(name, func(t *testing.T) {
			file := programFile(t, doc, placeholder, `{"number":7,`+members+`,"title":"Add a page","state":"draft","action":"opened"}`)
			got, why := afterProgram(doc, pushcheck.ProgramEnd{Submission: file})
			draft := string(got.Repositories[0].Draft)
			if strings.Count(draft, `"url"`) > 1 || strings.Contains(draft, "javascript") {
				t.Errorf("the stored draft is %s", draft)
			}
			if name == "bad last" && why == "" {
				t.Errorf("the draft with a last url of javascript: is taken")
			}
			if name == "good last" && (why != "" || draft != `{"number":7,"url":"`+site+`/pull/7","title":"Add a page","state":"draft","action":"opened"}`) {
				t.Errorf("the draft is %s with the cause %q, want the one that qory writes", draft, why)
			}
		})
	}
}
