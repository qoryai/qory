package cmd

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestNewReviewerInputsJSON(t *testing.T) {
	spec := reviewSessionSpec{Reviewer: "acceptance-reviewer", Report: "/r/a/report.json"}
	in, err := newReviewerInputs("reviewers", spec, "/r/suite_result.json", "/r/start")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"stage":{"reviewers":"reviewers"},"review":{"report":"/r/a/report.json","suite_result":"/r/suite_result.json"},"start_artifacts":"/r/start"}`
	if string(data) != want {
		t.Errorf("json = %s, want %s", data, want)
	}
	if !slices.Equal(reviewStartArtifactTypes, []string{"acceptance-criteria", "spec", "build-plan"}) {
		t.Errorf("start artifact types = %q", reviewStartArtifactTypes)
	}
}

// The members follow the naming of qoryInputs, which withQoryMember writes, and none
// repeats one of its members.
func TestReviewerInputsNamingMatchesQoryInputs(t *testing.T) {
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
	for _, k := range keys(reviewerInputs{}) {
		if !form.MatchString(k) || slices.Contains(have, k) {
			t.Errorf("member %q is not a new snake_case name beside %q", k, have)
		}
	}
}

func TestNewReviewerInputsRefuses(t *testing.T) {
	ok := reviewSessionSpec{Reviewer: "code-reviewer", Report: "/r/a/report.json"}
	for _, c := range []struct {
		name  string
		role  string
		spec  reviewSessionSpec
		suite string
		start string
		want  string
	}{
		{"empty role", "", ok, "/s", "/t", "group role"},
		{"upper-case role", "Reviewers", ok, "/s", "/t", "group role"},
		{"role the harness reads as no", "none", ok, "/s", "/t", "reads false, no, null, off, none and 0 as no role"},
		{"role 0", "0", ok, "/s", "/t", "group role"},
		{"relative report", "reviewers", reviewSessionSpec{Reviewer: "x", Report: "report.json"}, "/s", "/t", "qory.review.report"},
		{"suite result with ..", "reviewers", ok, "/s/../x", "/t", "qory.review.suite_result"},
		{"start in .git", "reviewers", ok, "/s", "/w/.git/start", "qory.start_artifacts"},
	} {
		_, err := newReviewerInputs(c.role, c.spec, c.suite, c.start)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
			continue
		}
		if first, _, _ := strings.Cut(err.Error(), "\n"); len(first) > 100 {
			t.Errorf("%s: first line has %d characters", c.name, len(first))
		}
	}
}
