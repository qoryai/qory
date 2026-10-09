package cmd

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"
)

// TestParseSubjectsTakesTheTitleLast holds a subject's title to the rest of the value:
// title= first takes all of it, a title that reads like more keys stays the title, an
// empty url= or title= is none, and a URL carries a comma only as %2C, since a raw one
// splits it.
func TestParseSubjectsTakesTheTitleLast(t *testing.T) {
	for _, c := range []struct {
		value string
		want  session.Subject
	}{
		{"type=ticket,ref=7", session.Subject{Type: "ticket", Ref: "7"}},
		{"url=https://tickets.example.com/7,ref=7,type=ticket", session.Subject{Type: "ticket", Ref: "7", URL: "https://tickets.example.com/7"}},
		{"type=ticket,ref=7,title=Crash,ref=9,url=x", session.Subject{Type: "ticket", Ref: "7", Title: "Crash,ref=9,url=x"}},
		{`type=ticket,ref=7,title=a "quoted", title=b`, session.Subject{Type: "ticket", Ref: "7", Title: `a "quoted", title=b`}},
		{"type=ticket,ref=7,url=,title=", session.Subject{Type: "ticket", Ref: "7"}},
		{"type=ticket,ref=7,url=https://tickets.example.com/search?q=a%2Cb", session.Subject{Type: "ticket", Ref: "7", URL: "https://tickets.example.com/search?q=a%2Cb"}},
		{"type=,ref=", session.Subject{}},
	} {
		got, err := parseSubjects([]string{c.value})
		if err != nil || len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: %+v, %v; want %+v", c.value, got, err, c.want)
		}
	}
	for _, c := range []struct {
		value, want string
	}{
		{"title=Crash,type=ticket,ref=7", "--subject title=Crash,type=ticket,ref=7 has no type; title= takes the rest of the value, so it comes last"},
		{"type=ticket,title=Crash,ref=7", "--subject type=ticket,title=Crash,ref=7 has no ref; title= takes the rest of the value, so it comes last"},
		{"type=ticket,title=Crash", "--subject type=ticket,title=Crash has no ref"},
		{"type=ticket,ref=7,url=https://tickets.example.com/search?q=a,b", "--subject type=ticket,ref=7,url=https://tickets.example.com/search?q=a,b: b is not key=value"},
		{"type=ticket,,ref=7", "--subject type=ticket,,ref=7: an empty part is not key=value"},
		{"type=ticket,=7", "--subject type=ticket,=7: =7 is not key=value"},
	} {
		_, err := parseSubjects([]string{c.value})
		if err == nil || err.Error() != c.want || ExitCode(err) != ExitInput {
			t.Errorf("%s: %v (exit %d), want %q", c.value, err, ExitCode(err), c.want)
		}
	}
	values := []string{"type=ticket,ref=7", "type=pull request,ref=7", "type=ticket,ref=8"}
	got, err := parseSubjects(values)
	if want := []session.Subject{{Type: "ticket", Ref: "7"}, {Type: "pull request", Ref: "7"}, {Type: "ticket", Ref: "8"}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("%v: %+v, %v; want %+v in order", values, got, err, want)
	}
}

// TestAboutInputNamesTheFlag maps every field Forager names to the flag that gave it,
// a subject by its value as given, and passes any other error as it is.
func TestAboutInputNamesTheFlag(t *testing.T) {
	subjects := []string{"type=ticket,ref=7", "type=Ticket,ref=8,title=Crash, again"}
	for _, c := range []struct {
		field, reason, want string
	}{
		{"about.kind", "is longer than 64 bytes", "--kind is longer than 64 bytes"},
		{"about.title", "contains a control character", "--title contains a control character"},
		{"about.details", "is not a JSON object", "--details details.json is not a JSON object"},
		{"about.subjects[1].type", "is not words", "--subject type=Ticket,ref=8,title=Crash, again: the type is not words"},
		{"about.subjects[0].ref", "is empty", "--subject type=ticket,ref=7: the ref is empty"},
		{"about.subjects[0].url", "contains a user name or password", "--subject type=ticket,ref=7: the url contains a user name or password"},
		{"about.subjects[1].title", "is longer than 256 bytes", "--subject type=Ticket,ref=8,title=Crash, again: the title is longer than 256 bytes"},
		{"about.subjects[1]", "has the same type and ref as about.subjects[0]", "about.subjects[1] has the same type and ref as about.subjects[0]"},
		{"about.subjects[2].ref", "is empty", "about.subjects[2].ref is empty"},
		{"about.subjects", "has 17 entries; a run carries at most 16", "about.subjects has 17 entries; a run carries at most 16"},
		{"about.other", "is odd", "about.other is odd"},
	} {
		err := aboutInput(&session.AboutError{Field: c.field, Reason: c.reason}, subjects, "details.json")
		if err == nil || err.Error() != c.want || ExitCode(err) != ExitInput {
			t.Errorf("%s: %v (exit %d), want %q", c.field, err, ExitCode(err), c.want)
		}
	}
	plain := errors.New("not an about")
	if err := aboutInput(plain, subjects, "-"); !errors.Is(err, plain) || err.Error() != "not an about" || ExitCode(err) != ExitInput {
		t.Errorf("another error: %v (exit %d)", err, ExitCode(err))
	}
}

// TestReadDetailsRefusesATerminal refuses --details - when stdin is a terminal, before
// it reads anything, and reads a pipe to its end otherwise.
func TestReadDetailsRefusesATerminal(t *testing.T) {
	in := strings.NewReader(`{"queue":"nightly"}`)
	_, err := readDetails("-", in, true)
	if want := "--details - reads stdin, and stdin is a terminal; pass a file, or pipe the details in"; err == nil || err.Error() != want || ExitCode(err) != ExitInput {
		t.Errorf("a terminal: %v, want %q", err, want)
	}
	if in.Len() == 0 {
		t.Error("the terminal was read")
	}
	d, err := readDetails("-", in, false)
	if err != nil || string(d) != `{"queue":"nightly"}` || in.Len() != 0 {
		t.Errorf("a pipe: %s, %v, %d bytes left", d, err, in.Len())
	}
	if d, err := readDetails("", in, true); d != nil || err != nil {
		t.Errorf("no --details: %s, %v", d, err)
	}
}
