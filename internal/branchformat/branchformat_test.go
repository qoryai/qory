package branchformat_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/qoryai/qory/internal/branchformat"
)

// contract is the schema's file in the contract.
const contract = "../../contracts/harness/v1/branch.schema.json"

// sha1, sha256 are full commit ids of each kind.
var (
	sha1   = strings.Repeat("5d6e7f8a", 5)
	sha256 = strings.Repeat("5d6e7f8a", 8)
)

// valid is a document with one repository, which passes.
func valid() string {
	return `{"ref": "qory/wi_7k2m9pq4xt3a8b1c", "repositories": [{"system": "github.com", "path": "acme/web", "commit": "` + sha1 + `"}]}`
}

// TestEmbeddedSchemaIsTheContract keeps the package's copy of the schema equal to the
// contract's file, byte for byte.
func TestEmbeddedSchemaIsTheContract(t *testing.T) {
	want, err := os.ReadFile(contract)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("branch.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/branchformat/branch.schema.json differs from %s", contract)
	}
}

// TestName is the branch of a work item: qory/ and the id.
func TestName(t *testing.T) {
	if got := branchformat.Name("wi_7k2m9pq4xt3a8b1c"); got != "qory/wi_7k2m9pq4xt3a8b1c" {
		t.Errorf("Name = %s", got)
	}
}

// TestBranchIsTheFormat is a [branchformat.Branch] written as JSON: the document has the
// format's members, passes [branchformat.Check] and reads back the same.
func TestBranchIsTheFormat(t *testing.T) {
	b := branchformat.Branch{
		Ref:          branchformat.Name("wi_7k2m9pq4xt3a8b1c"),
		Repositories: []branchformat.Repository{{System: "github.com", Path: "acme/web", Commit: sha1}},
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ref":"qory/wi_7k2m9pq4xt3a8b1c","repositories":[{"system":"github.com","path":"acme/web","commit":"` + sha1 + `"}]}`
	if string(data) != want {
		t.Errorf("%s\nwant %s", data, want)
	}
	if err := branchformat.Check(data); err != nil {
		t.Error(err)
	}
	var back branchformat.Branch
	if err := json.Unmarshal([]byte(valid()), &back); err != nil {
		t.Fatal(err)
	}
	if back.Ref != b.Ref || len(back.Repositories) != 1 || back.Repositories[0] != b.Repositories[0] {
		t.Errorf("read back %+v", back)
	}
}

// TestCheck keeps [branchformat.Check] and the contract's file in step: each document is
// accepted or refused by both.
func TestCheck(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(contract)
	if err != nil {
		t.Fatal(err)
	}
	replace := func(from, to string) string { return strings.Replace(valid(), from, to, 1) }
	two := `{"system": "git.example", "path": "a/b/c", "commit": "` + sha256 + `"}`
	for _, c := range []struct {
		doc   string
		valid bool
	}{
		{valid(), true},
		{replace(`"commit": "`+sha1+`"}`, `"commit": "`+sha1+`"}, `+two), true},
		{replace("wi_7k2m9pq4xt3a8b1c", "wi_"+strings.Repeat("a", 64)), true},
		{replace("wi_7k2m9pq4xt3a8b1c", "wi_"+strings.Repeat("a", 65)), false},
		{replace("github.com", "a"), true},
		{replace("github.com", strings.Repeat("a", 253)), true},
		{replace("github.com", strings.Repeat("a", 254)), false},
		{replace("acme/web", strings.Repeat("a", 200)), true},
		{replace("acme/web", strings.Repeat("a", 201)), false},
		{replace("acme/web", "acme/.github"), true},
		{`{}`, false},
		{`[]`, false},
		{`{"ref": "qory/wi_1"}`, false},
		{`{"repositories": [{"system": "github.com", "path": "acme/web", "commit": "` + sha1 + `"}]}`, false},
		{replace(`"ref": "qory/wi_7k2m9pq4xt3a8b1c"`, `"ref": "wi_7k2m9pq4xt3a8b1c"`), false},
		{replace("qory/wi_7k2m9pq4xt3a8b1c", "qory/"), false},
		{replace("qory/wi_7k2m9pq4xt3a8b1c", "qory/wi_1/x"), false},
		{replace("qory/wi_7k2m9pq4xt3a8b1c", `qory/wi_1\n`), false},
		{replace("qory/wi_7k2m9pq4xt3a8b1c", "qory/ws_1"), false},
		{`{"ref": "qory/wi_1", "repositories": []}`, false},
		{`{"ref": "qory/wi_1", "repositories": {}}`, false},
		{replace(`"ref":`, `"owner": "acme", "ref":`), false},
		{replace(`"system":`, `"owner": "acme", "system":`), false},
		{replace(`"system": "github.com", `, ""), false},
		{replace(`"path": "acme/web", `, ""), false},
		{replace(`, "commit": "`+sha1+`"`, ""), false},
		{replace(sha1, sha1[:39]), false},
		{replace(sha1, sha1+"5"), false},
		{replace(sha1, sha256+"5"), false},
		{replace(sha1, sha256), true},
		{replace(sha1, strings.ToUpper(sha1)), false},
		{replace(sha1, "g"+sha1[1:]), false},
		{replace(sha1, sha1+`\n`), false},
		{replace("github.com", "GitHub.com"), false},
		{replace("github.com", "github.com."), false},
		{replace("github.com", ""), false},
		{replace("acme/web", ""), false},
		{replace("acme/web", "/acme/web"), false},
		{replace("acme/web", "acme/web/"), false},
		{replace("acme/web", "acme//web"), false},
		{replace("acme/web", "acme/.."), false},
		{replace("acme/web", "./acme"), false},
		{replace("acme/web", "acme/.git"), false},
		{replace("acme/web", ".GIT/web"), false},
		{replace("acme/web", "acme/w b"), false},
		{replace("acme/web", `acme/web\n`), false},
	} {
		var doc any
		if err := json.Unmarshal([]byte(c.doc), &doc); err != nil {
			t.Fatalf("%s: %v", c.doc, err)
		}
		if err := schema.Validate(doc); (err == nil) != c.valid {
			t.Errorf("contract: %s valid=%v err=%v", c.doc, c.valid, err)
		}
		if err := branchformat.Check([]byte(c.doc)); (err == nil) != c.valid {
			t.Errorf("Check: %s valid=%v err=%v", c.doc, c.valid, err)
		}
	}
}

// TestCheckStartsWithTheFix is the error of a document the format does not take, and of
// one that is not JSON: the fix first, and then each rule with its place.
func TestCheckStartsWithTheFix(t *testing.T) {
	err := branchformat.Check([]byte(`{"ref": "main", "repositories": []}`))
	const want = "fix the branch document to match contracts/harness/v1/branch.schema.json: at '/"
	if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "\n") {
		t.Fatalf("%v\nwant %s…", err, want)
	}
	// The library returns the rules in no fixed order.
	for _, rule := range []string{"at '/ref': ", "at '/repositories': "} {
		if !strings.Contains(err.Error(), rule) {
			t.Errorf("%v\nwant %s", err, rule)
		}
	}
	for _, doc := range []string{"", "{", `{} {}`} {
		err := branchformat.Check([]byte(doc))
		if err == nil || !strings.HasPrefix(err.Error(), "write the branch document as one JSON object, the format of contracts/harness/v1/branch.schema.json: ") {
			t.Errorf("%q: %v", doc, err)
		}
	}
}

// TestCheckPrintsTheSegmentRule is a path with a segment the format does not take. The
// schema has that rule under a not, which has no rule under it in the library's error,
// and the error has the place and the not.
func TestCheckPrintsTheSegmentRule(t *testing.T) {
	const want = "fix the branch document to match contracts/harness/v1/branch.schema.json: at '/repositories/0/path': 'not' failed"
	for _, path := range []string{"acme/.git", "../web", "acme/../web", "acme/.GIT/x", "./web"} {
		err := branchformat.Check([]byte(strings.Replace(valid(), "acme/web", path, 1)))
		if err == nil || err.Error() != want {
			t.Errorf("%s: %v\nwant %s", path, err, want)
		}
	}
}

// TestCheckHidesASecret is an access key secret in a value: the error does not print it.
func TestCheckHidesASecret(t *testing.T) {
	const secret = "qak_" + "0123456789abcdef"
	err := branchformat.Check([]byte(strings.Replace(valid(), "acme/web", "acme web "+secret, 1)))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("%v", err)
	}
}
