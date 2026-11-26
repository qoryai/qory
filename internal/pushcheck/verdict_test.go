package pushcheck

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/runapi"
)

const (
	fixText          = "Record escalate or no verdict: the next workflow in the loop judges this work."
	noSessionFixText = "Post the verdict from a session of the run."
	pathFix          = "Send the POST to the run's /verdict or /artifacts path."
)

// wantRefusal checks that r is the 422 refusal of a push check with text in its message,
// or, for text "", that r is nil. A fix that is not "" starts the message.
func wantRefusal(t *testing.T, name string, r *session.RunAPIRefusal, fix, text string) {
	t.Helper()
	if text == "" {
		if r != nil {
			t.Errorf("%s: refusal %q, want none", name, r.Message)
		}
		return
	}
	if r == nil {
		t.Errorf("%s: no refusal", name)
		return
	}
	if r.Status != http.StatusUnprocessableEntity || r.Code != runapi.CodePushCheckFailed {
		t.Errorf("%s: status %d code %q, want 422 %q", name, r.Status, r.Code, runapi.CodePushCheckFailed)
	}
	first, _, _ := strings.Cut(r.Message, "\n")
	if (fix != "" && !strings.HasPrefix(first, fix)) || len(first) > 100 {
		t.Errorf("%s: first line %q (%d characters) is not %q", name, first, len(first), fix)
	}
	if !strings.Contains(r.Message, text) {
		t.Errorf("%s: message %q has no %q", name, r.Message, text)
	}
}

func TestSessionVerdict(t *testing.T) {
	for _, c := range []struct {
		name    string
		path    string
		body    string
		refused string // text the message has, "" for no refusal
	}{
		{"escalate is stored", "/verdict", `{"verdict":"escalate","points":[{"text":"x"}]}`, ""},
		{"passed is refused", "/verdict", `{"verdict":"passed"}`, `The session sent the verdict "passed".`},
		{"changes-requested is refused", "/verdict", `{"verdict":"changes-requested"}`, `"changes-requested"`},
		{"another case is refused", "/verdict", `{"verdict":"Escalate"}`, "Escalate"},
		{"a verdict twice is refused", "/verdict", `{"verdict":"escalate","verdict":"passed"}`, "a member twice"},
		{"a verdict twice, passed first", "/verdict", `{"verdict":"passed","verdict":"escalate"}`, "a member twice"},
		{"a name in another case is refused", "/verdict", `{"verdict":"escalate","VERDICT":"passed"}`, "differs only in case"},
		{"no verdict is refused", "/verdict", `{"points":[]}`, "missing or not a string"},
		{"a number is refused", "/verdict", `{"verdict":1}`, "missing or not a string"},
		{"not an object is refused", "/verdict", `[]`, "The body is no verdict"},
		{"not JSON is refused", "/verdict", `nope`, "The body is no verdict"},
		{"another POST is not checked", "/pushed-commits", `nope`, ""},
		{"next-session is not checked", "/next-session", `{"verdict":"passed"}`, ""},
	} {
		r := SessionVerdict(false, session.RunAPIRequest{Path: c.path, Body: []byte(c.body)})
		wantRefusal(t, c.name, r, fixText, c.refused)
	}
}

func TestSessionVerdictCutsALongVerdict(t *testing.T) {
	r := SessionVerdict(false, session.RunAPIRequest{Path: "/verdict", Body: []byte(`{"verdict":"` + strings.Repeat("a\\u0007", 5000) + `"}`)})
	if r == nil || len(r.Message) > 1500 || strings.ContainsRune(r.Message, 7) {
		t.Errorf("refusal %+v", r)
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
		refused string
	}{
		{"escalate pushed", verdictPush(t, "verdict", `{"verdict":"escalate","points":[{"text":"x"}]}`), ""},
		{"passed pushed", verdictPush(t, "verdict", `{"verdict":"passed"}`), `"passed"`},
		{"changes-requested pushed", verdictPush(t, "verdict", `{"verdict":"changes-requested"}`), `"changes-requested"`},
		{"a file that is no verdict", verdictPush(t, "verdict", `nope`), "The body is no verdict"},
		{"another type is not read as a verdict", verdictPush(t, "spec", `{"verdict":"passed"}`), ""},
		{"a type in another case is not read as a verdict", verdictPush(t, "Verdict", `{"verdict":"passed"}`), ""},
		{"a withdrawal has no file", []byte(`{"type":"verdict","name":"main","withdraw":true}`), ""},
	} {
		r := SessionVerdict(false, session.RunAPIRequest{Path: "/artifacts", Body: c.body})
		wantRefusal(t, c.name, r, fixText, c.refused)
	}
}

// A push of the type verdict is a verdict under every name.
func TestSessionVerdictIgnoresTheNameOfAPush(t *testing.T) {
	for _, name := range []string{"main", "other", ""} {
		body, err := json.Marshal(map[string]any{"type": "verdict", "name": name, "files": []map[string]any{
			{"filename": "verdict.json", "content_base64": []byte(`{"verdict":"passed"}`)},
		}})
		if err != nil {
			t.Fatal(err)
		}
		wantRefusal(t, "name "+name, SessionVerdict(false, session.RunAPIRequest{Path: "/artifacts", Body: body}), fixText, `"passed"`)
	}
}

// A body that the run API and a JSON reader that takes the last member could read in two
// ways is stopped, for a push and for a verdict, with the member in both orders.
func TestSessionVerdictRefusesAnAmbiguousPOST(t *testing.T) {
	file := `[{"filename":"v.json","content_base64":"` + "eyJ2ZXJkaWN0IjoicGFzc2VkIn0=" + `"}]`
	for _, c := range []struct {
		name, path, body, text string
	}{
		{"type verdict first", "/artifacts", `{"type":"verdict","type":"note","files":` + file + `}`, "Send each member of the push once"},
		{"type verdict last", "/artifacts", `{"type":"note","type":"verdict","files":` + file + `}`, "Send each member of the push once"},
		{"a name in another case", "/artifacts", `{"type":"note","TYPE":"verdict","files":` + file + `}`, "Send each member of the push once"},
		{"files twice", "/artifacts", `{"type":"note","files":[],"files":` + file + `}`, "Send each member of the push once"},
		{"a file member twice", "/artifacts", `{"type":"verdict","files":[{"filename":"a","filename":"b","content_base64":""}]}`, "Send each member of the push once"},
		{"not a push", "/artifacts", `nope`, "Send the push as qory artifact push sends it"},
		{"verdict first", "/verdict", `{"verdict":"escalate","verdict":"passed"}`, "a member twice"},
		{"verdict last", "/verdict", `{"verdict":"passed","verdict":"escalate"}`, "a member twice"},
	} {
		req := session.RunAPIRequest{Path: c.path, Body: []byte(c.body)}
		wantRefusal(t, c.name+", no judges", SessionVerdict(false, req), "", c.text)
		noSession := c.text
		if c.path == "/verdict" {
			noSession = noSessionFixText
		}
		wantRefusal(t, c.name+", no session", NoSessionVerdict(req), "", noSession)
		wantRefusal(t, c.name+", judges", SessionVerdict(true, req), "", "")
	}
}

// The paths of the table are other spellings of /verdict and /artifacts, and the check
// stops each. Forager cuts the query and decodes the escapes before it passes the path, so
// only the slash spellings reach the check in a run; the other rows keep the check safe
// for a caller that passes the path as it is. The gateway sends on as a verdict or a push
// only the exact /verdict and /artifacts.
func TestSessionVerdictRefusesAPathSpelling(t *testing.T) {
	passed := []byte(`{"verdict":"passed"}`)
	push := verdictPush(t, "verdict", `{"verdict":"passed"}`)
	for _, c := range []struct {
		path string
		body []byte
	}{
		{"/verdict/", passed}, {"//verdict", passed}, {"/verdict//", passed}, {"/verdict?x=1", passed},
		{"/verdict#x", passed}, {"/%76erdict", passed},
		{"/artifacts/", push}, {"//artifacts", push}, {"/artifacts?x=1", push}, {"/%61rtifacts", push},
	} {
		req := session.RunAPIRequest{Path: c.path, Body: c.body}
		text := "The path of the POST is another spelling of it."
		wantRefusal(t, c.path+", no judges", SessionVerdict(false, req), pathFix, text)
		wantRefusal(t, c.path+", no session", NoSessionVerdict(req), pathFix, text)
		wantRefusal(t, c.path+", judges", SessionVerdict(true, req), "", "")
	}
	// Spellings that are other paths: the gateway reads each as no verdict and no push, since
	// it reads no empty segment, no dot segment, and no segment with a slash.
	for _, path := range []string{"/Verdict", "/VERDICT", "/verdicts", "/artifact", "/Artifacts", "/verdict/x", "/%zz",
		"/verdict%2f", "/./verdict", "/../verdict"} {
		req := session.RunAPIRequest{Path: path, Body: passed}
		wantRefusal(t, path, SessionVerdict(false, req), "", "")
		wantRefusal(t, path+", no session", NoSessionVerdict(req), "", "")
	}
}

// Without a session, every verdict is stopped, escalate too.
func TestNoSessionVerdict(t *testing.T) {
	for _, v := range []string{"passed", "changes-requested", "escalate"} {
		for _, req := range []session.RunAPIRequest{
			{Path: "/verdict", Body: []byte(`{"verdict":"` + v + `"}`)},
			{Path: "/artifacts", Body: verdictPush(t, "verdict", `{"verdict":"`+v+`"}`)},
		} {
			wantRefusal(t, v+" on "+req.Path, NoSessionVerdict(req), noSessionFixText, "The run takes no verdict between its sessions or from a setup command.")
		}
	}
	// A verdict that cannot be read has the same refusal.
	for _, req := range []session.RunAPIRequest{
		{Path: "/verdict", Body: []byte(`nope`)},
		{Path: "/verdict", Body: []byte(`{}`)},
		{Path: "/artifacts", Body: verdictPush(t, "verdict", `nope`)},
	} {
		wantRefusal(t, string(req.Body)+" on "+req.Path, NoSessionVerdict(req), noSessionFixText, "The run takes no verdict between its sessions or from a setup command.")
	}
	for _, req := range []session.RunAPIRequest{
		{Path: "/pushed-commits", Body: []byte(`nope`)},
		{Path: "/next-session", Body: []byte(`{}`)},
		{Path: "/artifacts", Body: verdictPush(t, "note", `{"verdict":"passed"}`)},
	} {
		wantRefusal(t, req.Path, NoSessionVerdict(req), "", "")
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
				r := SessionVerdict(c.judges, req)
				if (r != nil) != c.refused[v] {
					t.Errorf("%s, %s on %s: refusal %v, refused want %t", c.name, v, req.Path, r, c.refused[v])
				}
			}
		}
	}
}
