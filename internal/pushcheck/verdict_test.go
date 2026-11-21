package pushcheck

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
)

func TestSessionVerdict(t *testing.T) {
	for _, c := range []struct {
		name    string
		path    string
		body    string
		refused string // text the error has, "" for no error
	}{
		{"escalate is stored", "/verdict", `{"verdict":"escalate","points":[{"text":"x"}]}`, ""},
		{"passed is refused", "/verdict", `{"verdict":"passed"}`, `The session sent the verdict "passed".`},
		{"changes-requested is refused", "/verdict", `{"verdict":"changes-requested"}`, `"changes-requested"`},
		{"another case is refused", "/verdict", `{"verdict":"Escalate"}`, "Escalate"},
		{"a verdict twice is refused", "/verdict", `{"verdict":"escalate","verdict":"passed"}`, "a member twice"},
		{"a name in another case is refused", "/verdict", `{"verdict":"escalate","VERDICT":"passed"}`, "differs only in case"},
		{"no verdict is refused", "/verdict", `{"points":[]}`, "missing or not a string"},
		{"a number is refused", "/verdict", `{"verdict":1}`, "missing or not a string"},
		{"not an object is refused", "/verdict", `[]`, "The body is no verdict"},
		{"not JSON is refused", "/verdict", `nope`, "The body is no verdict"},
		{"another path is not checked", "/artifacts", `{"verdict":"passed"}`, ""},
		{"a push is not checked", "/pushed-commits", `nope`, ""},
	} {
		err := SessionVerdict(false, session.RunAPIRequest{Path: c.path, Body: []byte(c.body)})
		if c.refused == "" {
			if err != nil {
				t.Errorf("%s: error %v, want none", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: no error", c.name)
			continue
		}
		first, _, _ := strings.Cut(err.Error(), "\n")
		if first != "Record escalate or no verdict: the next workflow in the loop judges this work." || len(first) > 100 {
			t.Errorf("%s: first line %q (%d characters) is not the fix text", c.name, first, len(first))
		}
		if !strings.Contains(err.Error(), c.refused) {
			t.Errorf("%s: error %q has no %q", c.name, err, c.refused)
		}
	}
}

func TestSessionVerdictCutsALongVerdict(t *testing.T) {
	err := SessionVerdict(false, session.RunAPIRequest{Path: "/verdict", Body: []byte(`{"verdict":"` + strings.Repeat("a\\u0007", 5000) + `"}`)})
	if err == nil || len(err.Error()) > 1500 || strings.ContainsRune(err.Error(), 7) {
		t.Errorf("error is %d bytes: %.80q", len(err.Error()), err)
	}
}

// verdictPush is the body of a POST of /artifacts of artifactType with one file.
func verdictPush(t *testing.T, artifactType, content string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"type": artifactType, "name": "main", "files": []map[string]any{
		{"filename": "verdict.json", "content_type": "application/json", "content_base64": []byte(content)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestSessionVerdictChecksAPushOfTheTypeVerdict(t *testing.T) {
	for _, c := range []struct {
		name    string
		body    []byte
		refused bool
	}{
		{"escalate pushed", verdictPush(t, "verdict", `{"verdict":"escalate","points":[{"text":"x"}]}`), false},
		{"passed pushed", verdictPush(t, "verdict", `{"verdict":"passed"}`), true},
		{"changes-requested pushed", verdictPush(t, "verdict", `{"verdict":"changes-requested"}`), true},
		{"a file that is no verdict", verdictPush(t, "verdict", `nope`), true},
		{"another type is not checked", verdictPush(t, "spec", `{"verdict":"passed"}`), false},
		{"a push in no form is left to the run API", []byte(`nope`), false},
		{"a withdrawal has no file", []byte(`{"type":"verdict","name":"main","withdraw":true}`), false},
	} {
		err := SessionVerdict(false, session.RunAPIRequest{Path: "/artifacts", Body: c.body})
		if (err != nil) != c.refused {
			t.Errorf("%s: error %v, refused want %t", c.name, err, c.refused)
		}
	}
}

// A workflow that judges, or that sets no judges, may record any verdict; the cmd package
// reads an unset judges as true, see workflowJudges.
func TestSessionVerdictByJudges(t *testing.T) {
	verdicts := []string{"passed", "changes-requested", "escalate"}
	for _, c := range []struct {
		name    string
		judges  bool
		refused map[string]bool
	}{
		{"judges true", true, map[string]bool{}},
		{"judges false", false, map[string]bool{"passed": true, "changes-requested": true}},
	} {
		for _, v := range verdicts {
			for _, req := range []session.RunAPIRequest{
				{Path: "/verdict", Body: []byte(`{"verdict":"` + v + `"}`)},
				{Path: "/artifacts", Body: verdictPush(t, "verdict", `{"verdict":"`+v+`"}`)},
			} {
				err := SessionVerdict(c.judges, req)
				if (err != nil) != c.refused[v] {
					t.Errorf("%s, %s on %s: error %v, refused want %t", c.name, v, req.Path, err, c.refused[v])
				}
			}
		}
	}
}
