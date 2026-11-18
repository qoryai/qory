package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/qory/internal/submission"
)

const (
	baseID   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	middleID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	headID   = "cccccccccccccccccccccccccccccccccccccccc"
)

// programScript writes a shell script with body in a new folder and returns its path.
func programScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "open-draft")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// passedDocument is a submission of a passed round with a text and one repository on
// git.example.com with two commits, whose draft is the placeholder.
func passedDocument() *submission.Submission {
	return &submission.Submission{
		Ref: "qory/wi_1", Verdict: submission.VerdictPassed,
		Text: &submission.Text{Title: "Add a page", Description: "The page lists orders.\nIt has a second line."},
		Repositories: []submission.Repository{{
			System: "git.example.com", Path: "acme/shop", Commit: headID,
			Base:    &submission.Base{Commit: baseID, Branch: "main"},
			Commits: []submission.Commit{{Commit: middleID, Step: "implement"}, {Commit: headID, Step: "unreviewed"}},
			Draft:   placeholder,
		}},
	}
}

// commitLink is the address of a commit of acme/shop on git.example.com.
func commitLink(commit string) string { return "https://git.example.com/acme/shop/commit/" + commit }

// fillScript is a program that prints a whole draft and an address for the base, the middle
// commit and the head.
const fillScript = `echo "draft_number 7"
echo "draft_url https://git.example.com/acme/shop/pull/7"
echo "draft_title $QORY_SUBMIT_TITLE"
echo "draft_state draft"
echo "draft_action opened"
for c in $QORY_SUBMIT_BASE_COMMIT $QORY_SUBMIT_COMMITS; do echo "commit_url $c https://git.example.com/acme/shop/commit/$c"; done
`

// runProgram is [submitProgram.apply] of a program with command on document, with the lines it printed.
func runProgram(t *testing.T, p *submitProgram, document *submission.Submission) (*submission.Submission, string) {
	t.Helper()
	var out bytes.Buffer
	got, err := p.apply(context.Background(), &out, document)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDocument(got); err != nil {
		t.Errorf("the stored document is not a submission: %v", err)
	}
	return got, out.String()
}

// checkDocument checks document against the submission format.
func checkDocument(document *submission.Submission) error {
	content, err := json.Marshal(document)
	if err != nil {
		return err
	}
	return submission.Check(content)
}

// TestTheProgramRunsOutsideTheWallWithThePersonsEnvironment is a program on the person's
// machine: it gets the contract's inputs, the person's variables, closed standard input and the
// description in a file, and its draft and addresses are in the stored document.
func TestTheProgramRunsOutsideTheWallWithThePersonsEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "person-login")
	t.Setenv("QORY_SUBMIT_TITLE", "from the shell")
	t.Setenv("QORY_SUBMIT_DRAFT_NUMBER", "99")
	seen := filepath.Join(t.TempDir(), "seen")
	program := &submitProgram{command: []string{programScript(t, `
{
echo "gh=$GH_TOKEN"
echo "system=$QORY_SUBMIT_SYSTEM path=$QORY_SUBMIT_PATH branch=$QORY_SUBMIT_BRANCH"
echo "base=$QORY_SUBMIT_BASE_BRANCH@$QORY_SUBMIT_BASE_COMMIT"
echo "commits=$QORY_SUBMIT_COMMITS"
echo "draft=$QORY_SUBMIT_DRAFT number=${QORY_SUBMIT_DRAFT_NUMBER-unset} previous=${QORY_SUBMIT_PREVIOUS_TITLE-unset}"
echo "title=$QORY_SUBMIT_TITLE"
cat "$QORY_SUBMIT_DESCRIPTION_FILE"
echo "stdin=$(cat | wc -c | tr -d ' ')"
} > `+seen+"\n"+fillScript)}}
	got, printed := runProgram(t, program, passedDocument())
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	want := "gh=person-login\nsystem=git.example.com path=acme/shop branch=qory/wi_1\n" +
		"base=main@" + baseID + "\ncommits=" + middleID + " " + headID + "\ndraft=open number=unset previous=unset\n" +
		"title=Add a page\nThe page lists orders.\nIt has a second line.stdin=0\n"
	if string(data) != want {
		t.Errorf("the program saw:\n%s\nwant:\n%s", data, want)
	}
	r := got.Repositories[0]
	if r.CommitURL != commitLink(headID) || r.Base.CommitURL != commitLink(baseID) || r.Commits[0].CommitURL != commitLink(middleID) || r.Commits[1].CommitURL != commitLink(headID) {
		t.Errorf("the addresses are %+v", r)
	}
	if want := `{"number":7,"url":"https://git.example.com/acme/shop/pull/7","title":"Add a page","state":"draft","action":"opened"}`; string(r.Draft) != want {
		t.Errorf("the draft is %s, want %s", r.Draft, want)
	}
	if printed != "" {
		t.Errorf("the step printed %q, want nothing for a program that printed no error line", printed)
	}
}

// TestTheProgramUpdatesTheDraftOfThePreviousVersion is a work item whose previous version has
// a draft: the program gets its number and the previous title, and the draft is an update.
func TestTheProgramUpdatesTheDraftOfThePreviousVersion(t *testing.T) {
	previous := passedDocument()
	previous.Text.Title = "Add a first page"
	previous.Repositories[0].Draft = json.RawMessage(`{"number":7,"url":"https://git.example.com/acme/shop/pull/7","title":"Add a first page","state":"draft","action":"opened"}`)
	seen := filepath.Join(t.TempDir(), "seen")
	program := &submitProgram{previous: previous, command: []string{programScript(t,
		`echo "$QORY_SUBMIT_DRAFT $QORY_SUBMIT_DRAFT_NUMBER $QORY_SUBMIT_PREVIOUS_TITLE" > `+seen+"\n")}}
	got, _ := runProgram(t, program, passedDocument())
	data, _ := os.ReadFile(seen)
	if string(data) != "update 7 Add a first page\n" {
		t.Errorf("the program saw %q", data)
	}
	if string(got.Repositories[0].Draft) != `{"none":"program-failed","exit":1}` {
		t.Errorf("a program that printed no draft left the draft %s", got.Repositories[0].Draft)
	}
}

// TestARoundThatDidNotPassGetsTheAddressesAndNoDraft is a repository whose draft is not the
// placeholder: the program gets the draft none and no title, its draft lines are not read, and
// the addresses are stored.
func TestARoundThatDidNotPassGetsTheAddressesAndNoDraft(t *testing.T) {
	document := passedDocument()
	document.Verdict = submission.VerdictChangesRequested
	document.Repositories[0].Draft = submission.None(submission.NoneNotPassed, 0)
	seen := filepath.Join(t.TempDir(), "seen")
	program := &submitProgram{command: []string{programScript(t,
		`echo "$QORY_SUBMIT_DRAFT title=${QORY_SUBMIT_TITLE-unset} file=${QORY_SUBMIT_DESCRIPTION_FILE-unset}" > `+seen+"\n"+fillScript)}}
	got, _ := runProgram(t, program, document)
	data, _ := os.ReadFile(seen)
	if string(data) != "none title=unset file=unset\n" {
		t.Errorf("the program saw %q", data)
	}
	r := got.Repositories[0]
	if string(r.Draft) != `{"none":"not-passed"}` || r.CommitURL != commitLink(headID) || r.Base.CommitURL != commitLink(baseID) {
		t.Errorf("the repository is %+v, want the addresses and not-passed", r)
	}
}

// TestAProgramByHowItEnded is each end of a program for the repository whose draft is the
// placeholder: the draft and the lines the step prints.
func TestAProgramByHowItEnded(t *testing.T) {
	for _, c := range []struct {
		name, body, draft string
		urls              bool
		printed           []string
	}{
		{"finished with no draft", `echo done >&2`, `{"none":"program-failed","exit":1}`, false,
			[]string{"qory run: submit program: done", "qory run: The submit program printed no whole draft, so a repository has no draft.", "  Exit code: 0.", "  Draft of the repository: program-failed."}},
		{"closed draft", `echo "draft_number 7"
echo "draft_url https://git.example.com/acme/shop/pull/7"
echo "draft_title T"
echo "draft_state merged"
echo "draft_action kept"
`, `{"none":"program-failed","exit":1}`, false,
			[]string{"qory run: The draft is closed or merged, so the submission has no draft for it.", "  Draft state: merged.", "  Draft of the repository: program-failed."}},
		{"failed call after its lines", fillScript + "echo 'no permission' >&2\nexit 1", `{"number":7,"url":"https://git.example.com/acme/shop/pull/7","title":"Add a page","state":"draft","action":"opened"}`, true,
			[]string{"qory run: submit program: no permission"}},
		{"failed call, no draft", "echo 'no permission' >&2\nexit 1", `{"none":"program-failed","exit":1}`, false,
			[]string{"qory run: submit program: no permission", "qory run: The submit program failed, so a repository it did not finish has no draft.", "  Exit code: 1.", "  Draft of the repository: program-failed."}},
		{"bad input", "exit 2", `{"none":"program-failed","exit":2}`, false,
			[]string{"qory run: The submit program failed, so a repository it did not finish has no draft.", "  Exit code: 2.", "  Draft of the repository: program-failed."}},
		{"another status", fillScript + "exit 137", `{"none":"program-failed","exit":1}`, false,
			[]string{"qory run: The submit program failed, so a repository it did not finish has no draft.", "  Exit code: 137.", "  Draft of the repository: program-failed."}},
		{"no login", "echo 'Run `gh auth login`, then `qory run` again.' >&2\nexit 1", `{"none":"program-failed","exit":1}`, false,
			[]string{"qory run: submit program: Run `gh auth login`, then `qory run` again.", "qory run: The submit program failed, so a repository it did not finish has no draft.", "  Exit code: 1.", "  Draft of the repository: program-failed."}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, printed := runProgram(t, &submitProgram{command: []string{programScript(t, c.body)}}, passedDocument())
			r := got.Repositories[0]
			if string(r.Draft) != c.draft {
				t.Errorf("the draft is %s, want %s", r.Draft, c.draft)
			}
			if (r.CommitURL != "") != c.urls {
				t.Errorf("the address of the head is %q, want one: %v", r.CommitURL, c.urls)
			}
			if lines := strings.Split(strings.TrimSuffix(printed, "\n"), "\n"); strings.Join(lines, "\n") != strings.Join(c.printed, "\n") {
				t.Errorf("the step printed:\n%s\nwant:\n%s", printed, strings.Join(c.printed, "\n"))
			}
		})
	}
}

// TestAProgramPastItsLimitHasNoDraft is a program that sleeps past the limit: it is stopped,
// the draft is program-timeout, and the step prints the limit.
func TestAProgramPastItsLimitHasNoDraft(t *testing.T) {
	program := &submitProgram{command: []string{programScript(t, "exec sleep 30\n")}, limit: 300 * time.Millisecond}
	started := time.Now()
	got, printed := runProgram(t, program, passedDocument())
	if time.Since(started) > 20*time.Second {
		t.Errorf("the program ran %s", time.Since(started))
	}
	if string(got.Repositories[0].Draft) != `{"none":"program-timeout"}` {
		t.Errorf("the draft is %s", got.Repositories[0].Draft)
	}
	if !strings.Contains(printed, "Limit: 300ms.") {
		t.Errorf("the step printed %q", printed)
	}
}

// TestAProgramThatDoesNotStartHasNoDraft is a command that is not there.
func TestAProgramThatDoesNotStartHasNoDraft(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	got, printed := runProgram(t, &submitProgram{command: []string{missing}}, passedDocument())
	if string(got.Repositories[0].Draft) != `{"none":"program-failed","exit":1}` {
		t.Errorf("the draft is %s", got.Repositories[0].Draft)
	}
	if !strings.HasPrefix(printed, "qory run: The submit program did not start, so a repository has no draft.\n  Cause: ") {
		t.Errorf("the step printed %q", printed)
	}
}

// TestALineThatQoryCannotUseIsLeftOutAndListed is a program that prints a draft whose number is
// wrong, an address on another host and a good address: the draft is not stored, the good
// address is, and the step lists the lines that qory left out.
func TestALineThatQoryCannotUseIsLeftOutAndListed(t *testing.T) {
	body := `echo "draft_number 0"
echo "draft_url https://git.example.com/acme/shop/pull/7"
echo "commit_url $QORY_SUBMIT_BASE_COMMIT https://other.example.com/acme/shop/commit/x"
echo "commit_url $QORY_SUBMIT_BASE_COMMIT https://git.example.com/acme/shop/commit/$QORY_SUBMIT_BASE_COMMIT"
`
	got, printed := runProgram(t, &submitProgram{command: []string{programScript(t, body)}}, passedDocument())
	r := got.Repositories[0]
	if string(r.Draft) != `{"none":"program-failed","exit":1}` || r.Base.CommitURL != commitLink(baseID) {
		t.Errorf("the repository is %+v", r)
	}
	for _, want := range []string{
		"qory run: The submit program printed a line that qory cannot use, so qory left it out.\n  Line 1: the number is not a whole number of 1 to 9 digits, 1 or more.",
		"  Line 2: the draft needs five lines, and draft_number, draft_title, draft_state, draft_action is missing; the line is left out.",
		"  Line 3: the address is not on the repository's system.",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("the step did not print %q:\n%s", want, printed)
		}
	}
}

// TestARepositoryWithTooManyCommitsGetsNoProgram is 101 commits, over the contract's 100.
func TestARepositoryWithTooManyCommitsGetsNoProgram(t *testing.T) {
	document := passedDocument()
	r := &document.Repositories[0]
	r.Commits = nil
	for range maxSubmitCommits + 1 {
		r.Commits = append(r.Commits, submission.Commit{Commit: headID, Step: "unreviewed"})
	}
	started := filepath.Join(t.TempDir(), "started")
	got, printed := runProgram(t, &submitProgram{command: []string{programScript(t, "touch "+started+"\n")}}, document)
	if _, err := os.Stat(started); err == nil {
		t.Error("the program started")
	}
	if string(got.Repositories[0].Draft) != `{"none":"program-failed","exit":2}` || !strings.Contains(printed, "the repository has 101 commits, and the program takes at most 100.") {
		t.Errorf("the draft is %s and the step printed %q", got.Repositories[0].Draft, printed)
	}
}

// TestARepositoryThatTheRunDidNotPushGetsNoProgram is an entry with its commit alone.
func TestARepositoryThatTheRunDidNotPushGetsNoProgram(t *testing.T) {
	document := passedDocument()
	document.Repositories = append(document.Repositories, submission.Repository{System: "git.example.com", Path: "acme/lib", Commit: baseID, Draft: placeholder})
	started := filepath.Join(t.TempDir(), "count")
	_, _ = runProgram(t, &submitProgram{command: []string{programScript(t, "echo x >> "+started+"\n")}}, document)
	data, _ := os.ReadFile(started)
	if string(data) != "x\n" {
		t.Errorf("the program ran %q times for 2 entries, 1 of them pushed", data)
	}
}

// TestTheProgramsErrorLinesHideASecret is a program that prints a registered secret.
func TestTheProgramsErrorLinesHideASecret(t *testing.T) {
	got, printed := runProgram(t, &submitProgram{command: []string{programScript(t, "echo 'token ghp_abcdefghijklmnopqrstuvwxyz0123456789 end' >&2\n")}}, passedDocument())
	_ = got
	if strings.Contains(printed, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("the step printed a token: %q", printed)
	}
}

// missing is the reason of each of the n draft lines that are there when one of the five is
// not.
func missing(key string, n int) []string {
	var out []string
	for range n {
		out = append(out, "the draft needs five lines, and "+key+" is missing; the line is left out")
	}
	return out
}

// around is the reasons for a draft whose line at position (1 to 5) is wrong for reason: the
// reason in its place, and the other four lines left out.
func around(key string, position int, reason string) []string {
	out := missing(key, 4)
	return append(out[:position-1], append([]string{reason}, out[position-1:]...)...)
}

// TestParseSubmitLines is the contract's rules for the lines of the program: a whole draft, an
// address for a commit that qory gave, and the lines that qory leaves out with the reason.
func TestParseSubmitLines(t *testing.T) {
	commits := []string{baseID, headID}
	good := "draft_number 34\ndraft_url https://git.example.com/acme/shop/pull/34\ndraft_title A title\ndraft_state open\ndraft_action kept\n"
	for _, c := range []struct {
		name, out string
		draft     bool
		urls      int
		reasons   []string
	}{
		{"whole", good + "commit_url " + headID + " https://git.example.com/acme/shop/commit/" + headID + "\n", true, 1, nil},
		{"blank line", "\n" + good, true, 0, nil},
		{"a draft key twice", good + "draft_state draft\n", true, 0, []string{"the key is there already"}},
		{"four draft keys", strings.Replace(good, "draft_action kept\n", "", 1), false, 0, missing("draft_action", 4)},
		{"no line feed at the end", good + "commit_url " + headID, true, 0, []string{"the line has no line feed at its end"}},
		{"control character", "draft_title a\tb\n", false, 0, []string{"the line has a control character"}},
		{"no value", "draft_url\n", false, 0, []string{"the line is not a key and a value"}},
		{"unknown key", "other 1\n", false, 0, []string{"the key is not one of the contract's"}},
		{"unknown commit", "commit_url " + middleID + " https://git.example.com/a/b\n", false, 0, []string{"the commit is not one that qory gave the program"}},
		{"commit twice", "commit_url " + headID + " https://git.example.com/a/b\ncommit_url " + headID + " https://git.example.com/a/c\n", false, 1, []string{"the commit has an address already"}},
		{"no address", "commit_url " + headID + "\n", false, 0, []string{"the line has no address"}},
		{"http", "draft_url http://git.example.com/a/b\n", false, 0, []string{"the address is not an https URL as definitions.schema.json describes it"}},
		{"port", "draft_url https://git.example.com:8443/a/b\n", false, 0, []string{"the address has a port"}},
		{"user", "draft_url https://u@git.example.com/a/b\n", false, 0, []string{"the address is not an https URL as definitions.schema.json describes it"}},
		{"query", "draft_url https://git.example.com/a/b?x=1\n", false, 0, []string{"the address has a query or a fragment"}},
		{"no path", "draft_url https://git.example.com/\n", false, 0, []string{"the address has no path"}},
		{"state", "draft_state archived\n", false, 0, []string{"the state is none of draft, open, closed and merged"}},
		{"a draft line alone", "draft_state draft\n", false, 0, []string{"the draft needs five lines, and draft_number, draft_url, draft_title, draft_action is missing; the line is left out"}},
		{"closed with the action updated", strings.Replace(strings.Replace(good, "draft_state open", "draft_state closed", 1), "draft_action kept", "draft_action updated", 1), false, 0, []string{"a draft that is closed or merged has the action kept"}},
		{"merged and kept", strings.Replace(good, "draft_state open", "draft_state merged", 1), true, 0, nil},
		{"carriage returns", strings.ReplaceAll(good, "\n", "\r\n"), true, 0, nil},
		{"a title of spaces", strings.Replace(good, "A title", "\u00a0 \u3000", 1), false, 0, around("draft_title", 3, "the title has a control, bidirectional or invisible character, or only spaces")},
		{"a title with a bidirectional control", strings.Replace(good, "A title", "A\u202etitle", 1), false, 0, around("draft_title", 3, "the title has a control, bidirectional or invisible character, or only spaces")},
		{"a title that is not UTF-8", strings.Replace(good, "A title", "A\xfftitle", 1), false, 0, around("draft_title", 3, "the title is not UTF-8")},
		{"an upper-case scheme", strings.Replace(good, "https://git", "HTTPS://git", 1), false, 0, around("draft_url", 2, "the address is not an https URL as definitions.schema.json describes it")},
		{"a quote in an address", "commit_url " + headID + " https://git.example.com/acme/shop/commit/\"x\n", false, 0, []string{"the address is not an https URL as definitions.schema.json describes it"}},
		{"a non-ASCII address", "commit_url " + headID + " https://git.example.com/acme/shop/commit/\u00e9\n", false, 0, []string{"the address is not an https URL as definitions.schema.json describes it"}},
		{"action", "draft_action closed\n", false, 0, []string{"the action is none of opened, updated and kept"}},
		{"number with a zero", "draft_number 07\n", false, 0, []string{"the number is not a whole number of 1 to 9 digits, 1 or more"}},
		{"number of 10 digits", "draft_number 1234567890\n", false, 0, []string{"the number is not a whole number of 1 to 9 digits, 1 or more"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := parseSubmitLines(c.out, "git.example.com", submitDraftOpen, commits)
			if (got.Draft != nil) != c.draft || len(got.CommitURLs) != c.urls {
				t.Errorf("draft %v and %d addresses, want %v and %d", got.Draft, len(got.CommitURLs), c.draft, c.urls)
			}
			var reasons []string
			for _, bad := range got.Invalid {
				reasons = append(reasons, bad.Reason)
			}
			if strings.Join(reasons, "|") != strings.Join(c.reasons, "|") {
				t.Errorf("the lines left out are %q, want %q", reasons, c.reasons)
			}
		})
	}
}
