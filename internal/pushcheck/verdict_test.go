package pushcheck

import (
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
)

func TestReviewStageVerdict(t *testing.T) {
	for _, c := range []struct {
		name    string
		path    string
		body    string
		refused string // text the error has, "" for no error
	}{
		{"changes-requested is stored", "/verdict", `{"verdict":"changes-requested"}`, ""},
		{"changes-requested with points", "/verdict", `{"verdict":"changes-requested","points":[{"text":"x","action":"run_again"}]}`, ""},
		{"passed is refused", "/verdict", `{"verdict":"passed"}`, `The session sent the verdict "passed".`},
		{"escalate is refused", "/verdict", `{"verdict":"escalate","points":[{"text":"x"}]}`, `"escalate"`},
		{"another case is refused", "/verdict", `{"verdict":"Changes-Requested"}`, "Changes-Requested"},
		{"a verdict twice is refused", "/verdict", `{"verdict":"changes-requested","verdict":"passed"}`, "a member twice"},
		{"a name in another case is refused", "/verdict", `{"verdict":"changes-requested","VERDICT":"passed"}`, "differs only in case"},
		{"no verdict is refused", "/verdict", `{"points":[]}`, "missing or not a string"},
		{"a number is refused", "/verdict", `{"verdict":1}`, "missing or not a string"},
		{"not an object is refused", "/verdict", `[]`, "The body is no verdict"},
		{"not JSON is refused", "/verdict", `nope`, "The body is no verdict"},
		{"another path is not checked", "/artifacts", `{"verdict":"passed"}`, ""},
		{"a push is not checked", "/pushed-commits", `nope`, ""},
	} {
		err := ReviewStageVerdict(session.RunAPIRequest{Path: c.path, Body: []byte(c.body)})
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
		if !strings.HasPrefix(first, "Record only changes-requested") || len(first) > 100 {
			t.Errorf("%s: first line %q (%d characters) is not the fix within 100", c.name, first, len(first))
		}
		if !strings.Contains(err.Error(), c.refused) {
			t.Errorf("%s: error %q has no %q", c.name, err, c.refused)
		}
	}
}

func TestReviewStageVerdictCutsALongVerdict(t *testing.T) {
	err := ReviewStageVerdict(session.RunAPIRequest{Path: "/verdict", Body: []byte(`{"verdict":"` + strings.Repeat("a\\u0007", 5000) + `"}`)})
	if err == nil || len(err.Error()) > 1000 || strings.ContainsRune(err.Error(), 7) {
		t.Errorf("error is %d bytes: %.80q", len(err.Error()), err)
	}
}
