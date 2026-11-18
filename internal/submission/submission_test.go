package submission_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/submission"
)

// contract is the schema's file in the contract.
const contract = "../../contracts/harness/v1/submission.schema.json"

// sha1, sha256 are full commit ids of each kind.
var (
	sha1   = strings.Repeat("5d6e7f8a", 5)
	sha256 = strings.Repeat("5d6e7f8a", 8)
	other  = strings.Repeat("0a1b2c3d", 5)
)

// valid is a document with one repository, which passes.
func valid() string {
	return `{"ref": "qory/wi_7k2m9pq4xt3a8b1c", "verdict": "passed", "text": {"title": "Add a page", "description": "Adds it."},
 "repositories": [{"system": "github.com", "path": "acme/web", "commit": "` + sha1 + `", "commit_url": "https://github.com/acme/web/commit/` + sha1 + `",
 "base": {"commit": "` + other + `"}, "commits": [{"commit": "` + sha1 + `", "step": "deliverable:d-1"}],
 "draft": {"number": 3, "url": "https://github.com/acme/web/pull/3", "title": "Add a page", "state": "draft", "action": "opened"}}]}`
}

// TestEmbeddedSchemaIsTheContract keeps the package's copy of the schema equal to the
// contract's file, byte for byte.
func TestEmbeddedSchemaIsTheContract(t *testing.T) {
	want, err := os.ReadFile(contract)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("submission.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/submission/submission.schema.json differs from %s", contract)
	}
}

// TestName is the branch of a work item: qory/ and the id.
func TestName(t *testing.T) {
	if got := submission.Name("wi_7k2m9pq4xt3a8b1c"); got != "qory/wi_7k2m9pq4xt3a8b1c" {
		t.Errorf("Name = %s", got)
	}
}

// TestSubmissionIsTheFormat is a [submission.Submission] written as JSON: the document has
// the format's members, passes [submission.Check] and reads back the same.
func TestSubmissionIsTheFormat(t *testing.T) {
	s := submission.Submission{
		Ref:     submission.Name("wi_7k2m9pq4xt3a8b1c"),
		Verdict: submission.VerdictChangesRequested,
		Repositories: []submission.Repository{{
			System: "github.com", Path: "acme/web", Commit: sha1,
			Base:    &submission.Base{Commit: other},
			Commits: []submission.Commit{{Commit: sha1, Step: "unreviewed"}},
			Draft:   submission.None(submission.NoneNotPassed, 0),
		}},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ref":"qory/wi_7k2m9pq4xt3a8b1c","verdict":"changes-requested","repositories":[{"system":"github.com","path":"acme/web","commit":"` + sha1 + `","base":{"commit":"` + other + `"},"commits":[{"commit":"` + sha1 + `","step":"unreviewed"}],"draft":{"none":"not-passed"}}]}`
	if string(data) != want {
		t.Errorf("%s\nwant %s", data, want)
	}
	if err := submission.Check(data); err != nil {
		t.Error(err)
	}
	var back submission.Submission
	if err := json.Unmarshal([]byte(valid()), &back); err != nil {
		t.Fatal(err)
	}
	if back.Ref != s.Ref || back.Verdict != "passed" || back.Text == nil || len(back.Repositories) != 1 || back.Repositories[0].Commits[0].Step != "deliverable:d-1" {
		t.Errorf("read back %+v", back)
	}
}

// TestNone writes the draft of a repository that has none, with the exit status for a
// program that failed.
func TestNone(t *testing.T) {
	if got := string(submission.None(submission.NoneNoProgram, 0)); got != `{"none":"no-program"}` {
		t.Errorf("None = %s", got)
	}
	for reason, want := range map[string]string{
		submission.NoneProgramFailed:  `{"none":"program-failed","exit":1}`,
		submission.NoneLoginNeeded:    `{"none":"login-needed","exit":1}`,
		submission.NoneProgramTimeout: `{"none":"program-timeout"}`,
	} {
		if got := string(submission.None(reason, 1)); got != want {
			t.Errorf("None(%s, 1) = %s, want %s", reason, got, want)
		}
	}
	if got := string(submission.None(submission.NoneProgramFailed, 0)); got != `{"none":"program-failed","exit":0}` {
		t.Errorf("None = %s", got)
	}
}

// TestCheck keeps [submission.Check] and the contract's file in step: each document is
// accepted or refused by both.
func TestCheck(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(contract)
	if err != nil {
		t.Fatal(err)
	}
	replace := func(from, to string) string { return strings.Replace(valid(), from, to, 1) }
	draft := `"draft": {"number": 3, "url": "https://github.com/acme/web/pull/3", "title": "Add a page", "state": "draft", "action": "opened"}`
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{valid(), true},
		{replace(draft, `"draft": {"none": "no-text"}`), true},
		{replace(draft, `"draft": {"none": "program-failed", "exit": 2}`), true},
		{replace(draft, `"draft": {"none": "program-failed"}`), false},
		{replace(draft, `"draft": {"none": "no-text", "exit": 1}`), false},
		{replace(draft, `"draft": {"none": "other"}`), false},
		{replace(draft, `"draft": {"none": "program-timeout"}`), true},
		{replace(draft, `"draft": {}`), false},
		{replace(`"state": "draft"`, `"state": "closed"`), true},
		{replace(`"state": "draft"`, `"state": "merged"`), true},
		{replace(`"state": "draft"`, `"state": "archived"`), false},
		{replace(draft, `"draft": {"none": "program-failed", "exit": 0}`), true},
		{replace(draft, `"draft": {"none": "program-failed", "exit": 3}`), false},
		{replace(draft, `"draft": {"none": "login-needed", "exit": 3}`), true},
		{replace(draft, `"draft": {"none": "login-needed", "exit": 1}`), false},
		{replace(draft, `"draft": {"none": "login-needed"}`), false},
		{replace(draft, `"draft": {"none": "program-timeout", "exit": 1}`), false},
		{replace(`"action": "opened"`, `"action": "kept"`), true},
		{replace(",\n "+draft, ""), false},
		{replace(`"verdict": "passed"`, `"verdict": "none"`), true},
		{replace(`"verdict": "passed"`, `"verdict": "approved"`), false},
		{replace(`"verdict": "passed", `, ``), false},
		{replace(`"text": {"title": "Add a page", "description": "Adds it."},`, ``), true},
		{replace(`"title": "Add a page", "description"`, `"description"`), false},
		{replace(`"base": {"commit": "`+other+`"}, `, ``), true},
		{replace(`"commit": "`+other+`"}, "commits"`, `"commit": "abc"}, "commits"`), false},
		{replace(`"step": "deliverable:d-1"`, `"step": ""`), false},
		{replace(`"step": "deliverable:d-1"`, `"step": "a", "owner": "x"`), false},
		{replace("wi_7k2m9pq4xt3a8b1c", "wi_"+strings.Repeat("a", 65)), false},
		{replace("github.com", "a"), true},
		{replace("github.com", strings.Repeat("a", 254)), false},
		{replace("acme/web", "acme/.git"), false},
		{replace("acme/web", "acme/.github"), true},
		{`{}`, false},
		{`[]`, false},
		{replace(`"repositories": [`, `"owner": "acme", "repositories": [`), false},
		{replace(`"system":`, `"owner": "acme", "system":`), false},
		{replace(sha1+`", "commit_url`, sha1[:39]+`", "commit_url`), false},
		{replace(sha1+`", "commit_url`, sha256+`", "commit_url`), true},
		{replace(sha1+`", "commit_url`, strings.ToUpper(sha1)+`", "commit_url`), false},
		{replace(`"commit": "`+sha1+`", "step"`, `"commit": "`+sha256+`", "step"`), true},
		{replace(`"commit": "`+sha1+`", "step"`, `"commit": "`+strings.ToUpper(sha256)+`", "step"`), false},
		{replace(`"commit": "`+sha1+`", "step"`, `"commit": "`+strings.ToUpper(sha1)+`", "step"`), false},
		{replace(`"title": "Add a page", "description"`, `"title": "Add\ta page", "description"`), false},
		{replace(`"title": "Add a page", "description"`, `"title": "Add\u0001 page", "description"`), false},
		{replace(`"title": "Add a page", "description"`, `"title": "Add\u007f page", "description"`), false},
		{replace(`"title": "Add a page", "description"`, `"title": "Add a page\n", "description"`), false},
		{replace(`"title": "Add a page", "description"`, `"title": "Ajouter une pagé é", "description"`), true},
		{replace(`"description": "Adds it."`, `"description": "Adds it.\nTwo lines."`), true},
		{`{"ref": "qory/wi_1", "verdict": "passed", "repositories": []}`, false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.doc), &doc); err != nil {
			t.Fatalf("%s: %v", c.doc, err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("contract: %s valid=%v err=%v", c.doc, c.valid, err)
		}
		if err := submission.Check([]byte(c.doc)); (err == nil) != c.valid {
			t.Errorf("Check: %s valid=%v err=%v", c.doc, c.valid, err)
		}
	}
}

// TestCheckStartsWithTheFix is the error of a document the format does not take, and of
// one that is not JSON: the fix first, and then each rule with its place.
func TestCheckStartsWithTheFix(t *testing.T) {
	err := submission.Check([]byte(`{"ref": "main", "verdict": "passed", "repositories": []}`))
	const want = "fix the submission to match contracts/harness/v1/submission.schema.json: at '/"
	if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "\n") {
		t.Fatalf("%v\nwant %s…", err, want)
	}
	for _, doc := range []string{"", "{", `{} {}`} {
		err := submission.Check([]byte(doc))
		if err == nil || !strings.HasPrefix(err.Error(), "write the submission as one JSON object, the format of contracts/harness/v1/submission.schema.json: ") {
			t.Errorf("%q: %v", doc, err)
		}
	}
}

// TestCheckHidesASecret is an access key secret in a value: the error does not print it.
func TestCheckHidesASecret(t *testing.T) {
	const secret = "qak_" + "0123456789abcdef"
	err := submission.Check([]byte(strings.Replace(valid(), "acme/web", "acme web "+secret, 1)))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("%v", err)
	}
}

// TestCommitID is the commit id of the format: 40 or 64 lower-case hexadecimal digits, as the
// schema's pattern has it.
func TestCommitID(t *testing.T) {
	for id, want := range map[string]bool{
		sha1: true, sha256: true, strings.ToUpper(sha1): false, strings.ToUpper(sha256): false,
		sha1[:39]: false, sha1 + "0": false, "": false, strings.Repeat("g", 40): false, "abc": false,
	} {
		if got := submission.CommitID(id); got != want {
			t.Errorf("CommitID(%q) = %v, want %v", id, got, want)
		}
	}
}
