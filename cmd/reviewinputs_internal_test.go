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
	in, err := newReviewSessionInputs(head, "/r/change.diff", "/r/reports", "/r/suite_result.json", "/r/start")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"review_session":{"commit":"0123456789abcdef0123456789abcdef01234567","diff":"/r/change.diff","reports":"/r/reports","suite_result":"/r/suite_result.json"},"start_artifacts":"/r/start"}`
	if string(data) != want {
		t.Errorf("json = %s, want %s", data, want)
	}
	if in.ReviewSession.Commit != head {
		t.Errorf("commit = %q, want the fresh copy's head %q", in.ReviewSession.Commit, head)
	}
	if !slices.Equal(reviewStartArtifactTypes, []string{"acceptance-criteria", "spec", "build-plan"}) {
		t.Errorf("start artifact types = %q", reviewStartArtifactTypes)
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
		diff   string
		report string
		suite  string
		start  string
		want   string
	}{
		{"short commit", head[:7], "/d", ok, "/s", "/t", "qory.review_session.commit"},
		{"upper-case commit", strings.ToUpper(head), "/d", ok, "/s", "/t", "qory.review_session.commit"},
		{"relative diff", head, "change.diff", ok, "/s", "/t", "qory.review_session.diff"},
		{"relative reports", head, "/d", "reports", "/s", "/t", "qory.review_session.reports"},
		{"suite result with ..", head, "/d", ok, "/s/../x", "/t", "qory.review_session.suite_result"},
		{"suite result below .git is fine to read", head, "/d", ok, "/w/.git/s", "/t", ""},
		{"relative start", head, "/d", ok, "/s", "start", "qory.start_artifacts"},
		{"reports in .claude", head, "/d", "/w/.claude/reports", "/s", "/t", "qory.review_session.reports"},
	} {
		_, err := newReviewSessionInputs(c.commit, c.diff, c.report, c.suite, c.start)
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
