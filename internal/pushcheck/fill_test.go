package pushcheck

import (
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/exports"
)

// fillTypes are two artifact types of a composed process.yaml: pull-request, whose members
// base and head are marked, and notes, which marks none.
var fillTypes = []exports.ProcessType{
	{Name: "notes"},
	{Name: "pull-request", Marks: map[string]string{"head": "pushed-head", "base": "pushed-base"}},
}

// TestFilledMemberRefusesAMarkedMember is a push of a pull-request version that sets a
// marked member: the refusal is the 422 whose line 1 is the text of the outbox, with the
// member's name in it, for the first member in name order.
func TestFilledMemberRefusesAMarkedMember(t *testing.T) {
	for _, c := range []struct{ name, content, text string }{
		{"base", `{"title":"A","base":{"collection":"c"}}`, "Remove base from the pull-request output: qory fills it at the push."},
		{"head", `{"title":"A","head":null}`, "Remove head from the pull-request output: qory fills it at the push."},
		{"both, in name order", `{"head":1,"base":2}`, "Remove base from the pull-request output: qory fills it at the push."},
	} {
		r := FilledMember(fillTypes, session.RunAPIRequest{Path: "/artifacts", Body: verdictPush(t, "pull-request", c.content)})
		wantRefusal(t, c.name, r, c.text, c.text)
		if r != nil && r.Message != c.text {
			t.Errorf("%s: message %q, want %q alone", c.name, r.Message, c.text)
		}
	}
	if TextFilledMember != "Remove %s from the %s output: qory fills it at the push." {
		t.Errorf("the text is %q", TextFilledMember)
	}
}

// TestFilledMemberPassesWhatSetsNone covers each POST that no rule stops: a version with
// no marked member, a type that marks none, a type the run does not have, a file that is
// no JSON object, a withdrawal with no files, a POST of another path, and a run whose
// types mark nothing, whatever the body.
func TestFilledMemberPassesWhatSetsNone(t *testing.T) {
	artifacts := func(body []byte) session.RunAPIRequest { return session.RunAPIRequest{Path: "/artifacts", Body: body} }
	for _, c := range []struct {
		name  string
		types []exports.ProcessType
		req   session.RunAPIRequest
	}{
		{"no marked member", fillTypes, artifacts(verdictPush(t, "pull-request", `{"title":"A","text":"base"}`))},
		{"a type that marks none", fillTypes, artifacts(verdictPush(t, "notes", `{"base":1}`))},
		{"a type the run lacks", fillTypes, artifacts(verdictPush(t, "other", `{"base":1}`))},
		{"a file that is no object", fillTypes, artifacts(verdictPush(t, "pull-request", `[{"base":1}]`))},
		{"a file that is no JSON", fillTypes, artifacts(verdictPush(t, "pull-request", `base`))},
		{"a withdrawal", fillTypes, artifacts([]byte(`{"type":"pull-request","name":"main","withdraw":true}`))},
		{"another path", fillTypes, session.RunAPIRequest{Path: "/verdict", Body: []byte(`{"base":1}`)}},
		{"no marks in the run", []exports.ProcessType{{Name: "pull-request"}}, artifacts(verdictPush(t, "pull-request", `{"base":1}`))},
		{"an unread body, no marks in the run", nil, artifacts([]byte(`nope`))},
	} {
		wantRefusal(t, c.name, FilledMember(c.types, c.req), "", "")
	}
}

// TestFilledMemberStopsAPushThatCannotBeRead is a run whose types mark a member: a body
// that is no push, a member twice, and a path in another spelling get the refusals of
// every rule that reads the push, and so does a file that has the marked member twice or
// in another case.
func TestFilledMemberStopsAPushThatCannotBeRead(t *testing.T) {
	for _, c := range []struct{ name, path, body, text string }{
		{"no push", "/artifacts", `nope`, "Send the push as qory artifact push sends it"},
		{"a type twice", "/artifacts", `{"type":"notes","type":"pull-request","files":[]}`, "Send each member of the push once"},
		{"another spelling", "/artifacts/", `{"type":"notes","files":[]}`, "Send the POST to the run's /verdict or /artifacts path."},
		{"the member twice", "/artifacts", string(verdictPush(t, "pull-request", `{"base":1,"base":2}`)), "Send each member of the push once"},
		{"the member in another case", "/artifacts", string(verdictPush(t, "pull-request", `{"Base":1}`)), "Send each member of the push once"},
	} {
		r := FilledMember(fillTypes, session.RunAPIRequest{Path: c.path, Body: []byte(c.body)})
		wantRefusal(t, c.name, r, c.text, c.text)
	}
}

// TestFilledMemberCutsALongName is a type and a member whose names are long: line 1 of the
// refusal has at most 100 characters.
func TestFilledMemberCutsALongName(t *testing.T) {
	long := strings.Repeat("m", 90)
	types := []exports.ProcessType{{Name: strings.Repeat("t", 90), Marks: map[string]string{long: "pushed-base"}}}
	r := FilledMember(types, session.RunAPIRequest{Path: "/artifacts", Body: verdictPush(t, types[0].Name, `{"`+long+`":1}`)})
	wantRefusal(t, "long names", r, "Remove ", "qory fills it at the push.")
}
