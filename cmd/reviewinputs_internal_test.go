package cmd

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// head is the fresh copy's pushed head in these tests.
const head = "0123456789abcdef0123456789abcdef01234567"

func TestNewReviewSessionInputsJSON(t *testing.T) {
	in, err := newReviewSessionInputs(head, "/r/commit", "/r/change.diff", "/r/reports", "/r/suite_result.json", "", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"review_session":{"commit":"0123456789abcdef0123456789abcdef01234567","commit_folder":"/r/commit","diff":"/r/change.diff","reports":"/r/reports","suite_result":"/r/suite_result.json"}}`
	if string(data) != want {
		t.Errorf("json = %s, want %s", data, want)
	}
	later, err := newReviewSessionInputs(head, "/r/commit", "/r/change.diff", "/r/reports", "/r/suite_result.json", "/r/open-findings.json", "/r/build-report.md")
	if err != nil {
		t.Fatal(err)
	}
	if later.ReviewSession.PreviousFindings != "/r/open-findings.json" || later.ReviewSession.BuildReport != "/r/build-report.md" {
		t.Errorf("later round: %+v", later.ReviewSession)
	}
	verify, err := newReviewSessionInputs(head, "/r/commit", "/r/change.diff", "/r/reports", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(verify)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "suite_result") {
		t.Errorf("write-no-push session json = %s, want no suite_result", data)
	}
	if in.ReviewSession.Commit != head {
		t.Errorf("commit = %q, want the fresh copy's head %q", in.ReviewSession.Commit, head)
	}
}

// The members follow the naming of qoryInputs, which withQoryMember writes, and none
// repeats one of its members.
func TestReviewSessionInputsNamingMatchesQoryInputs(t *testing.T) {
	keys := func(v any) []string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		return slices.Collect(maps.Keys(m))
	}
	form := regexp.MustCompile(`^[a-z]+(_[a-z]+)*$`)
	have := keys(qoryInputs{})
	for _, k := range keys(reviewSessionInputs{}) {
		if !form.MatchString(k) || slices.Contains(have, k) {
			t.Errorf("member %q is not a new snake_case name beside %q", k, have)
		}
	}
}

func TestNewReviewSessionInputsRefuses(t *testing.T) {
	const ok = "/r/reports"
	for _, c := range []struct {
		name   string
		commit string
		folder string
		diff   string
		report string
		suite  string
		want   string
		// previous is the path of the round before's open findings, "" for none.
		previous string
	}{
		{"short commit", head[:7], "/r/commit", "/d", ok, "/s", "qory.review_session.commit", ""},
		{"upper-case commit", strings.ToUpper(head), "/r/commit", "/d", ok, "/s", "qory.review_session.commit", ""},
		{"relative copy folder", head, "commit", "/d", ok, "/s", "qory.review_session.commit_folder", ""},
		{"copy folder with ..", head, "/r/../commit", "/d", ok, "/s", "qory.review_session.commit_folder", ""},
		{"relative diff", head, "/r/commit", "change.diff", ok, "/s", "qory.review_session.diff", ""},
		{"relative reports", head, "/r/commit", "/d", "reports", "/s", "qory.review_session.reports", ""},
		{"suite result with ..", head, "/r/commit", "/d", ok, "/s/../x", "qory.review_session.suite_result", ""},
		{"suite result below .git is fine to read", head, "/r/commit", "/d", ok, "/w/.git/s", "", ""},
		{"reports in .claude", head, "/r/commit", "/d", "/w/.claude/reports", "/s", "qory.review_session.reports", ""},
		{"relative previous findings", head, "/r/commit", "/d", ok, "/s", "qory.review_session.previous_findings", "open-findings.json"},
		{"previous findings of a later round", head, "/r/commit", "/d", ok, "/s", "", "/r/open-findings.json"},
		{"no suite result for a write-no-push session", head, "/r/commit", "/d", ok, "", "", ""},
	} {
		_, err := newReviewSessionInputs(c.commit, c.folder, c.diff, c.report, c.suite, c.previous, "")
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
			continue
		}
		if first, _, _ := strings.Cut(err.Error(), "\n"); len(first) > 100 {
			t.Errorf("%s: first line has %d characters", c.name, len(first))
		}
	}
}

func TestReviewPathProblem(t *testing.T) {
	for path, want := range map[string]string{
		"/r/report.json":      "",
		"r/report.json":       "is not absolute",
		"/r/./report.json":    "has a . part",
		"/r/../report.json":   "has a .. part",
		"/r//report.json":     "has a double or trailing slash",
		"/r/report.json/":     "has a double or trailing slash",
		"/r/a\x00b":           "has a NUL",
		"/r/.claude/rep.json": "has a .claude part, where the harness's tool guard denies a write",
		"/r/.gitignore/x":     "",
	} {
		if got := reviewPathProblem(path); got != want {
			t.Errorf("reviewPathProblem(%q) = %q, want %q", path, got, want)
		}
	}
}
