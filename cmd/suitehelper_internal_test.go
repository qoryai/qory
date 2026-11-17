package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/suite"
	"github.com/qoryai/qory/internal/verdictformat"
)

const buildPlanText = "# Build plan\n\n## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n" +
	"| targeted | `go test ./a` | `t.xml` | s |\n| full | `make test` | `junit.xml` | s |\n"

// fakeAPI is a run API for a run r1: the artifact index and the files of the versions it has,
// and the verdicts that were posted to it.
type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	versions []fakeVersion
	// verdictStatus is the status POST verdict returns, 201 when 0.
	verdictStatus int
	// failFiles is whether a request for the file of a version answers 500.
	failFiles  bool
	posted     []string
	indexCalls int
}

type fakeVersion struct {
	artifact, name string
	number         int
	runID          string
	cancelled      bool
	withdrawn      bool
	file, content  string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) base() string { return f.srv.URL + "/v1/runs/r1" }

// add makes a version that run runID wrote.
func (f *fakeAPI) add(v fakeVersion) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.versions = append(f.versions, v)
}

// withPlan adds the build plan with text, as the work item's launch plan.
func (f *fakeAPI) withPlan(text string) {
	f.add(fakeVersion{artifact: "build-plan", name: "main", number: 1, runID: "r0", file: "build-plan.md", content: text})
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v1/runs/r1")
	switch {
	case r.Method == http.MethodGet && path == "/artifacts":
		f.indexCalls++
		type file struct {
			Filename string `json:"filename"`
			Size     int    `json:"size"`
			SHA256   string `json:"sha256"`
		}
		type version struct {
			ID        string        `json:"id"`
			Number    int           `json:"number"`
			Writer    runapi.Writer `json:"writer"`
			Cancelled bool          `json:"cancelled"`
			Withdrawn bool          `json:"withdrawn"`
			Files     []file        `json:"files"`
		}
		type artifact struct {
			Type     string    `json:"type"`
			Name     string    `json:"name"`
			Newest   int       `json:"newest_version"`
			Versions []version `json:"versions"`
		}
		var order []string
		by := map[string]*artifact{}
		for _, v := range f.versions {
			key := v.artifact + "/" + v.name
			a := by[key]
			if a == nil {
				a = &artifact{Type: v.artifact, Name: v.name}
				by[key] = a
				order = append(order, key)
			}
			sum := sha256.Sum256([]byte(v.content))
			a.Versions = append(a.Versions, version{
				ID: fmt.Sprintf("%s-%s-%d", v.artifact, v.name, v.number), Number: v.number,
				Writer:    runapi.Writer{Kind: "run", RunID: v.runID},
				Cancelled: v.cancelled, Withdrawn: v.withdrawn,
				Files: []file{{v.file, len(v.content), hex.EncodeToString(sum[:])}},
			})
			a.Newest = max(a.Newest, v.number)
		}
		out := struct {
			Version   int         `json:"version"`
			Artifacts []*artifact `json:"artifacts"`
		}{Version: 1}
		for _, key := range order {
			out.Artifacts = append(out.Artifacts, by[key])
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet && strings.Contains(path, "/files/") && f.failFiles:
		http.Error(w, "down", http.StatusInternalServerError)
	case r.Method == http.MethodGet && strings.Contains(path, "/files/"):
		for _, v := range f.versions {
			if strings.HasPrefix(path, fmt.Sprintf("/artifacts/%s-%s-%d/files/", v.artifact, v.name, v.number)) {
				_, _ = io.WriteString(w, v.content)
				return
			}
		}
		http.NotFound(w, r)
	case r.Method == http.MethodPost && path == "/verdict":
		body, _ := io.ReadAll(r.Body)
		f.posted = append(f.posted, string(body))
		if f.verdictStatus != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.verdictStatus)
			_, _ = io.WriteString(w, `{"version":1,"error":"unavailable","message":"try later"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"version":1,"artifact":{"type":"verdict","name":"main","version":{"id":"v","number":9}}}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAPI) verdicts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posted...)
}

// apiRun is an opened run whose run API is api, with the folder dir.
type apiRun struct {
	openedRun
	api *fakeAPI
	dir string
}

func (r apiRun) RunAPI() (*http.Client, string, error) { return http.DefaultClient, r.api.base(), nil }
func (r apiRun) Dir() string                           { return r.dir }
func (r apiRun) Image() session.Image                  { return session.Image{Ref: "example.test/build:1"} }

// helperRun is a suiteRun for a workspace and a run folder, the lines it said, and the api.
type helperRun struct {
	*suiteRun
	api       *fakeAPI
	said      *[]string
	workspace string
	runDir    string
}

const (
	suiteSHA    = "0123456789abcdef0123456789abcdef01234567"
	redResult   = `<testsuite><testcase name="a"/><testcase name="b"><failure/></testcase></testsuite>`
	greenResult = `<testsuite><testcase name="a"/><testcase name="b"/></testsuite>`
	bodyChanges = `{"verdict":"changes-requested","points":[{"text":"The full suite failed: 1 of 2 tests failed.","action":"run_again"}]}`
)

// newHelper is the suiteRun of a run whose session exited, with a fake run API.
func newHelper(t *testing.T) helperRun {
	t.Helper()
	api := newFakeAPI(t)
	var said []string
	var mu sync.Mutex
	dir, runDir := t.TempDir(), t.TempDir()
	r := &suiteRun{
		run: apiRun{api: api, dir: runDir}, runID: "r1", minutes: 7, dir: dir,
		full: suite.Full{Command: "make test", ResultFile: "junit.xml"},
		said: func(line string) { mu.Lock(); said = append(said, line); mu.Unlock() },
	}
	return helperRun{r, api, &said, dir, runDir}
}

// state writes the files of the script's end to the workspace: the status file and, when
// result is not "", the result copy.
func (h helperRun) state(t *testing.T, status, result string) {
	t.Helper()
	folder := filepath.Join(h.workspace, suite.StateFolder)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "status"), []byte(status+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if result != "" {
		if err := os.WriteFile(filepath.Join(folder, "result"), []byte(result), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// after runs the helper as run.go does with the after step's end, no stop and no limit.
func (h helperRun) after(end *session.AfterResult, out session.Outcome) (session.Outcome, error) {
	var res *session.Result
	if end != nil {
		res = &session.Result{After: end}
	}
	return h.suiteRun.after(context.Background(), context.Background(), time.Time{}, res, out)
}

// succeeded is the outcome of a session that succeeded.
var succeeded = session.Outcome{State: event.StateSucceeded}

// ranRed is the end of a suite whose command ran and wrote red counts and exited 1.
func ranRed(t *testing.T, h helperRun) *session.AfterResult {
	h.state(t, "ran "+suiteSHA, redResult)
	return &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}
}

// TestARedSuitePostsChangesRequestedAfterTheSessionAndTheRunSucceeds: a suite that ends red
// posts one changes-requested verdict with one point, over a passed verdict and over none, and
// the run closes as its session did: it does not end failed, and qory run exits 0.
func TestARedSuitePostsChangesRequestedAfterTheSessionAndTheRunSucceeds(t *testing.T) {
	for _, c := range []struct {
		name  string
		agent []fakeVersion
	}{
		{"no verdict", nil},
		{"a passed verdict", []fakeVersion{{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: `{"verdict":"passed"}`}}},
		{"an escalate of an earlier run only", []fakeVersion{
			{artifact: "verdict", name: "main", number: 1, runID: "r0", file: "verdict.json", content: `{"verdict":"escalate","points":[{"text":"x","action":"answer","question":"q1"}]}`},
		}},
		{"a cancelled escalate of this run", []fakeVersion{
			{artifact: "verdict", name: "main", number: 1, runID: "r1", cancelled: true, file: "verdict.json", content: `{"verdict":"escalate","points":[{"text":"x","action":"run_again"}]}`},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			for _, v := range c.agent {
				h.api.add(v)
			}
			// The run's events are local and the step had no refusal: the point has one line.
			writeEvents(t, h.runDir, afterStarted(3, 2), afterExited(4, 2))
			got, err := h.after(ranRed(t, h), succeeded)
			if err != nil || got != succeeded {
				t.Errorf("outcome %+v, error %v, want the session's outcome", got, err)
			}
			if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != bodyChanges {
				t.Errorf("posted %q, want one %s", posted, bodyChanges)
			}
		})
	}
}

// TestAGreenSuiteAndASkippedSuitePostNothing: neither reads the artifact index, and a suite
// that did not run and a session that did not succeed post nothing either.
func TestAGreenSuiteAndASkippedSuitePostNothing(t *testing.T) {
	for _, c := range []struct {
		name    string
		status  string
		result  string
		end     *session.AfterResult
		session session.Outcome
	}{
		{"green", "ran " + suiteSHA, greenResult, &session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded},
		{"skipped", "skipped", "", &session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded},
		{"skipped tests", "ran " + suiteSHA, `<testsuite><testcase name="a"><skipped/></testcase></testsuite>`, &session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded},
		{"a failed session", "ran " + suiteSHA, redResult, &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, session.Outcome{State: event.StateFailed, ExitCode: 3}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: `{"verdict":"escalate","points":[{"text":"Pick one.","action":"answer","question":"q1"}]}`})
			if c.status != "" {
				h.state(t, c.status, c.result)
			}
			got, err := h.after(c.end, c.session)
			if err != nil || got != c.session {
				t.Errorf("outcome %+v, error %v", got, err)
			}
			if posted := h.api.verdicts(); len(posted) != 0 {
				t.Errorf("posted %q", posted)
			}
			if h.api.indexCalls != 0 {
				t.Errorf("the helper read the artifact index %d times", h.api.indexCalls)
			}
		})
	}
}

// TestTheFirstLineOfThePointFollowsHowTheSuiteEnded: each end of a red suite has its first line:
// 0 tests, unreadable counts, totals that do not match, the cap and a script that never started.
func TestTheFirstLineOfThePointFollowsHowTheSuiteEnded(t *testing.T) {
	for _, c := range []struct {
		name, status, result string
		end                  session.AfterResult
		want                 string
	}{
		{"0 tests", "ran " + suiteSHA, `<testsuite tests="0"/>`, session.AfterResult{State: event.StateSucceeded}, "The full suite ran 0 tests, so it proves nothing."},
		{"unreadable counts", "ran " + suiteSHA, "tests: 4 passed", session.AfterResult{State: event.StateFailed, ExitCode: 2}, "The full suite failed: its command exited with status 2."},
		{"no result file", "ran " + suiteSHA, "", session.AfterResult{State: event.StateFailed, ExitCode: 5}, "The full suite failed: its command exited with status 5."},
		{"totals that do not match", "ran " + suiteSHA, `<testsuite tests="9"><testcase/></testsuite>`, session.AfterResult{State: event.StateSucceeded}, "The full suite failed: the totals of its result file do not match its testcases."},
		{"the cap", "ran " + suiteSHA, "", session.AfterResult{State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: 143}, "The full suite did not finish: the after step reached its limit of 7 minutes."},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			if c.status != "" {
				h.state(t, c.status, c.result)
			}
			end := c.end
			end.Step = 2
			writeEvents(t, h.runDir, afterStarted(3, 2), afterExited(4, 2))
			got, err := h.after(&end, succeeded)
			if err != nil || got != succeeded {
				t.Errorf("outcome %+v, error %v, want the session's outcome", got, err)
			}
			want := `{"verdict":"changes-requested","points":[{"text":"` + c.want + `","action":"run_again"}]}`
			if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
				t.Errorf("posted %q, want %s", posted, want)
			}
		})
	}
}

// afterStarted and afterExited are the lines of the events file for the after step number n.
func afterStarted(seq, n int) string {
	return fmt.Sprintf(`{"type":"dev.qory.run.step_started","sequence":"%010d","data":{"step":%d,"name":"after","kind":"after"}}`, seq, n)
}

func afterExited(seq, n int) string {
	return fmt.Sprintf(`{"type":"dev.qory.run.step_exited","sequence":"%010d","data":{"step":%d,"state":"failed","exit_code":1}}`, seq, n)
}

func refusedEvent(seq int, host, decision string) string {
	return fmt.Sprintf(`{"type":"dev.qory.run.egress","sequence":"%010d","data":{"host":%q,"port":443,"method":"CONNECT","decision":%q}}`, seq, host, decision)
}

func writeEvents(t *testing.T, runDir string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(runDir, "events.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestARefusedHostIsNamedInThePoint: the first line of the point of a red suite has the refused
// host when the run's events have one in the after step, and the fix is the second line when
// the step had a denial or the events are not local.
func TestARefusedHostIsNamedInThePoint(t *testing.T) {
	for _, c := range []struct {
		name   string
		events []string
		text   string
		fix    string
	}{
		{"one host", []string{afterStarted(3, 2), refusedEvent(4, "registry.example", "denied"), afterExited(5, 2)},
			"Allow registry.example in the run's policy, or change the suite: it failed after the refusal.", textPointDenied},
		{"three hosts", []string{afterStarted(3, 2), refusedEvent(4, "a.example", "denied"), refusedEvent(5, "b.example", "denied"), refusedEvent(6, "c.example", "denied"), afterExited(7, 2)},
			"Allow a.example and 2 more in the run's policy, or change the suite: it failed after the refusals.", textPointDenied},
		{"a refusal before the step", []string{refusedEvent(1, "early.example", "denied"), afterStarted(3, 2), afterExited(5, 2)},
			"The full suite failed: 1 of 2 tests failed.", ""},
		{"a refusal after the step", []string{afterStarted(3, 2), afterExited(5, 2), refusedEvent(6, "late.example", "denied")},
			"The full suite failed: 1 of 2 tests failed.", ""},
		{"an allowed connection", []string{afterStarted(3, 2), refusedEvent(4, "ok.example", "allowed"), afterExited(5, 2)},
			"The full suite failed: 1 of 2 tests failed.", ""},
		{"a denial of a host with no valid name", []string{afterStarted(3, 2), refusedEvent(4, "bad host", "denied"), afterExited(5, 2)},
			"The full suite failed: 1 of 2 tests failed.", textPointDenied},
		{"events that are not local", nil,
			"The full suite failed: 1 of 2 tests failed.", textPointDenied},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			if c.events != nil {
				writeEvents(t, h.runDir, c.events...)
			}
			if _, err := h.after(ranRed(t, h), succeeded); err != nil {
				t.Fatal(err)
			}
			var v struct {
				Points []runapi.Point `json:"points"`
			}
			posted := h.api.verdicts()
			if len(posted) != 1 {
				t.Fatalf("posted %q", posted)
			}
			if err := json.Unmarshal([]byte(posted[0]), &v); err != nil || len(v.Points) != 1 {
				t.Fatalf("posted %q: %v", posted, err)
			}
			if v.Points[0].Text != c.text || v.Points[0].Fix != c.fix {
				t.Errorf("point %+v, want text %q, fix %q", v.Points[0], c.text, c.fix)
			}
		})
	}
}

// TestTheHostLineHasAtMost100CharactersAndAValidVerdict: a host of 253 characters is cut, with and
// without "and N more", and the verdict passes the verdict format.
func TestTheHostLineHasAtMost100CharactersAndAValidVerdict(t *testing.T) {
	long := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + ".example"
	if len(long) < 250 || len(long) > 253 {
		t.Fatalf("the test host has %d characters", len(long))
	}
	for _, hosts := range [][]string{{long}, {long, "b.example"}, slices.Repeat([]string{long}, 12), {"a.example"}} {
		line := hostsLine(hosts)
		if len(line) > 100 {
			t.Errorf("%d hosts: the line has %d characters: %q", len(hosts), len(line), line)
		}
		if !strings.HasPrefix(line, "Allow ") || !strings.Contains(line, "in the run's policy") {
			t.Errorf("the line %q lost its words", line)
		}
		if len(hosts) > 1 && !strings.Contains(line, " and ") {
			t.Errorf("the line %q has no count of the other hosts", line)
		}
		body, err := runapi.VerdictBody(runapi.VerdictChangesRequested, []runapi.Point{{Text: line, Action: "run_again", Fix: textPointDenied}})
		if err != nil {
			t.Fatal(err)
		}
		if err := verdictformat.Check(body); err != nil {
			t.Errorf("the verdict for %q: %v", line, err)
		}
	}
	if got := hostsLine([]string{long}); !strings.Contains(got, "...") || len(got) != 100 {
		t.Errorf("a long host is not shown cut to a line of 100 characters: %q", got)
	}
	// The words around the host take 77 characters, so a host of 23 fits whole and one of 24 or
	// 30 is cut; with "and 1 more" and the plural the host has 11.
	whole := strings.Repeat("a", 15) + ".example"
	if got := hostsLine([]string{whole}); !strings.Contains(got, " "+whole+" ") || len(got) != 100 {
		t.Errorf("a host of %d characters is cut: %q", len(whole), got)
	}
	for _, host := range []string{whole + "x", strings.Repeat("a", 22) + ".example"} {
		if got := hostsLine([]string{host}); !strings.Contains(got, "...") || len(got) != 100 || strings.Contains(got, host) {
			t.Errorf("a host of %d characters is not cut to 100: %q", len(host), got)
		}
	}
	if got := hostsLine([]string{"a.example", "b", "c"}); got != "Allow a.example and 2 more in the run's policy, or change the suite: it failed after the refusals." {
		t.Errorf("line %q", got)
	}
}

// TestASuiteThatCannotStartPostsAnEscalate: a suite whose command never ran is a setup fault that
// no change to the code mends. Over no verdict and over passed, qory posts escalate with the one
// point; over an escalate of the agent it adds the point; over a changes-requested of the agent
// it posts escalate with the agent's points and track and the point; over 20 points it posts
// nothing. The run closes as its session did.
func TestASuiteThatCannotStartPostsAnEscalate(t *testing.T) {
	defer func(was string) { goos = was }(goos)
	goos = "linux"
	point := func(i int) string { return fmt.Sprintf(`{"text":"Point %d.","action":"run_again"}`, i) }
	twenty := make([]string, 20)
	for i := range twenty {
		twenty[i] = point(i + 1)
	}
	for _, c := range []struct {
		name  string
		agent string
		end   session.AfterResult
		want  string
	}{
		{"the after step could not run", "", session.AfterResult{State: event.StateFailed, ExitCode: -1}, `{"verdict":"escalate","points":[{"text":"Contact support: the full suite did not start, because its container did not start.","action":"run_again"}]}`},
		{"no folder", "", session.AfterResult{State: event.StateFailed, ExitCode: 70}, `{"verdict":"escalate","points":[{"text":"The full suite did not start: the script could not make a folder it needs.","action":"run_again"}]}`},
		{"no clone", "", session.AfterResult{State: event.StateFailed, ExitCode: 71}, `{"verdict":"escalate","points":[{"text":"The full suite did not start: git could not clone HEAD of the workspace.","action":"run_again"}]}`},
		{"no HEAD", "", session.AfterResult{State: event.StateFailed, ExitCode: 72}, `{"verdict":"escalate","points":[{"text":"The full suite did not start: git could not read HEAD of the workspace.","action":"run_again"}]}`},
		{"no shell", "", session.AfterResult{State: event.StateFailed, ExitCode: 127}, `{"verdict":"escalate","points":[{"text":"The full suite did not start: the image may lack sh or git (exit 127).","action":"run_again"}]}`},
		{"over passed", `{"verdict":"passed"}`, session.AfterResult{State: event.StateFailed, ExitCode: 71}, `{"verdict":"escalate","points":[{"text":"The full suite did not start: git could not clone HEAD of the workspace.","action":"run_again"}]}`},
		{"over an escalate", `{"verdict":"escalate","points":[{"text":"Pick one.","action":"answer","question":"q1"}]}`, session.AfterResult{State: event.StateFailed, ExitCode: 71},
			`{"verdict":"escalate","points":[{"text":"Pick one.","action":"answer","question":"q1"},{"text":"The full suite did not start: git could not clone HEAD of the workspace.","action":"run_again"}]}`},
		{"over a changes-requested", `{"verdict":"changes-requested","points":[` + point(1) + `],"track":{"id":"small","reason":"One file."}}`, session.AfterResult{State: event.StateFailed, ExitCode: 71},
			`{"verdict":"escalate","points":[` + point(1) + `,{"text":"The full suite did not start: git could not clone HEAD of the workspace.","action":"run_again"}],"track":{"id":"small","reason":"One file."}}`},
		{"over a changes-requested with 20 points", `{"verdict":"changes-requested","points":[` + strings.Join(twenty, ",") + `]}`, session.AfterResult{State: event.StateFailed, ExitCode: 71}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			if c.agent != "" {
				h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: c.agent})
			}
			end := c.end
			end.Step = 2
			got, err := h.after(&end, succeeded)
			if err != nil || got != succeeded {
				t.Errorf("outcome %+v, error %v, want the session's outcome", got, err)
			}
			posted := h.api.verdicts()
			switch {
			case c.want == "" && len(posted) != 0:
				t.Errorf("posted %q, want nothing", posted)
			case c.want != "" && (len(posted) != 1 || posted[0] != c.want):
				t.Errorf("posted %q, want %s", posted, c.want)
			}
		})
	}
}

// TestAPassedVerdictKeepsItsTrackUnderARedSuite: the verdict that qory posts over a passed verdict
// of the agent has the agent's track, for a red suite and for one that did not start.
func TestAPassedVerdictKeepsItsTrackUnderARedSuite(t *testing.T) {
	const track = `"track":{"id":"small","reason":"One file."}`
	for _, c := range []struct {
		name string
		end  session.AfterResult
		want string
	}{
		{"red", session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, `{"verdict":"changes-requested","points":[{"text":"The full suite failed: 1 of 2 tests failed.","action":"run_again"}],` + track + `}`},
		{"not started", session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 71}, `{"verdict":"escalate","points":[{"text":"The full suite did not start: git could not clone HEAD of the workspace.","action":"run_again"}],` + track + `}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: `{"verdict":"passed",` + track + `}`})
			if c.name == "red" {
				h.state(t, "ran "+suiteSHA, redResult)
			}
			writeEvents(t, h.runDir, afterStarted(3, 2), afterExited(4, 2))
			end := c.end
			if _, err := h.after(&end, succeeded); err != nil {
				t.Fatal(err)
			}
			if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != c.want {
				t.Errorf("posted %q, want %s", posted, c.want)
			}
		})
	}
}

// TestASuiteThatStartsAndFailsPostsChangesRequested: a command that ran and exited other than 0,
// as `npm ci` does when it fails, is red, not a setup fault: it gets changes-requested.
func TestASuiteThatStartsAndFailsPostsChangesRequested(t *testing.T) {
	h := newHelper(t)
	h.state(t, "ran "+suiteSHA, "")
	writeEvents(t, h.runDir, afterStarted(3, 2), afterExited(4, 2))
	if _, err := h.after(&session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, succeeded); err != nil {
		t.Fatal(err)
	}
	want := `{"verdict":"changes-requested","points":[{"text":"The full suite failed: its command exited with status 1.","action":"run_again"}]}`
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestAHostTextThatTheVerdictFormatRefusesIsLeftOut: a host text that makes the verdict invalid
// is dropped from the line, and the point stays valid, so the post does not fail.
func TestAHostTextThatTheVerdictFormatRefusesIsLeftOut(t *testing.T) {
	got := suite.Outcome{State: suite.Failed, Started: true, ExitCode: 1, Counts: &suite.Counts{Tests: 2, Failed: 1}}
	for _, host := range []string{"fe80::1%a\nb", "<b>x</b>\n", "x\u202ey", "a\tb"} {
		p := suitePoint(got, suite.Refused{Local: true, Denied: 1, Hosts: []string{host}}, 60)
		if strings.Contains(p.Text, "Allow") || p.Text != "The full suite failed: 1 of 2 tests failed." || !validPoint(p) {
			t.Errorf("host %q: point %+v", host, p)
		}
	}
	if p := suitePoint(got, suite.Refused{Local: true, Denied: 1, Hosts: []string{"registry.example"}}, 60); !strings.HasPrefix(p.Text, "Allow registry.example") {
		t.Errorf("a valid host is left out: %+v", p)
	}
}

// TestAnAgentsVerdictStandsOverARedSuite: a changes-requested stands as it is; an escalate stands
// with the red suite as one more point, with its points and its track kept in order; an escalate
// with 20 points stands unchanged.
func TestAnAgentsVerdictStandsOverARedSuite(t *testing.T) {
	point := func(i int) string { return fmt.Sprintf(`{"text":"Point %d.","action":"run_again"}`, i) }
	points := func(n int) string {
		var p []string
		for i := 1; i <= n; i++ {
			p = append(p, point(i))
		}
		return strings.Join(p, ",")
	}
	suitePointText := `{"text":"The full suite failed: 1 of 2 tests failed.","action":"run_again"}`
	for _, c := range []struct {
		name, agent, want string
	}{
		{"changes-requested", `{"verdict":"changes-requested","points":[` + point(1) + `]}`, ""},
		{"escalate", `{"verdict":"escalate","points":[{"text":"Pick one.","action":"answer","question":"q1"}]}`,
			`{"verdict":"escalate","points":[{"text":"Pick one.","action":"answer","question":"q1"},` + suitePointText + `]}`},
		{"escalate with a track", `{"verdict":"escalate","points":[` + point(1) + `],"track":{"id":"small","reason":"One file."}}`,
			`{"verdict":"escalate","points":[` + point(1) + `,` + suitePointText + `],"track":{"id":"small","reason":"One file."}}`},
		{"escalate with 19 points", `{"verdict":"escalate","points":[` + points(19) + `]}`, `{"verdict":"escalate","points":[` + points(19) + `,` + suitePointText + `]}`},
		{"escalate with 20 points", `{"verdict":"escalate","points":[` + points(20) + `]}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: c.agent})
			writeEvents(t, h.runDir, afterStarted(3, 2), afterExited(4, 2))
			if got, err := h.after(ranRed(t, h), succeeded); err != nil || got != succeeded {
				t.Errorf("outcome %+v, error %v", got, err)
			}
			posted := h.api.verdicts()
			switch {
			case c.want == "" && len(posted) != 0:
				t.Errorf("posted %q, want nothing", posted)
			case c.want != "" && (len(posted) != 1 || posted[0] != c.want):
				t.Errorf("posted %q, want %s", posted, c.want)
			}
		})
	}
}

// TestAFailedPostDoesNotEndTheRunPassed: the run API refuses the verdict, or the agent's verdict
// cannot be read: the run closes failed with exit status 1, and the line has the fix first.
func TestAFailedPostDoesNotEndTheRunPassed(t *testing.T) {
	was := branchWait
	branchWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { branchWait = was })
	for _, c := range []struct {
		name  string
		setup func(h helperRun)
		posts int
	}{
		{"the run API answers 503", func(h helperRun) { h.api.verdictStatus = http.StatusServiceUnavailable }, maxBranchTries},
		{"the run API answers 409", func(h helperRun) { h.api.verdictStatus = http.StatusConflict }, 1},
		{"the agent's verdict is no valid verdict", func(h helperRun) {
			h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: `{"verdict":"escalate","points":[{"text":"` + strings.Repeat("a", 600) + `","action":"run_again"}]}`})
		}, 0},
		{"the verdict file is no JSON", func(h helperRun) {
			h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: "passed"})
		}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			c.setup(h)
			got, err := h.after(ranRed(t, h), succeeded)
			if got.State != event.StateFailed || got.ExitCode != 1 {
				t.Errorf("outcome %+v, want failed with 1", got)
			}
			if ExitCode(err) != 1 {
				t.Errorf("error %v, exit status %d, want 1", err, ExitCode(err))
			}
			if n := len(h.api.verdicts()); n != c.posts {
				t.Errorf("%d posts, want %d", n, c.posts)
			}
			var line string
			for _, l := range *h.said {
				if strings.HasPrefix(l, "Take the work item in again") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("lines %q have no fix line", *h.said)
			}
			if first, _, _ := strings.Cut(line, "\n"); len(first) > 100 {
				t.Errorf("the first line has %d characters: %q", len(first), first)
			}
		})
	}
}

// TestAStopDuringTheAfterStepClosesTheRunCancelled: a stop of qory run cuts the after step of a
// session that succeeded. The run does not close succeeded over a suite that did not finish: qory
// posts the verdict of the red suite with one try, and closes the run cancelled with the reason
// interrupted and exit status 1, also when that post fails.
func TestAStopDuringTheAfterStepClosesTheRunCancelled(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		posts  int
	}{
		{"the post goes through", 0, 1},
		{"the post fails", http.StatusServiceUnavailable, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			h.api.verdictStatus = c.status
			h.state(t, "ran "+suiteSHA, "")
			got, err := h.after(&session.AfterResult{Step: 2, State: event.StateCancelled, ExitCode: -1}, succeeded)
			if got.State != event.StateCancelled || got.Reason != event.ReasonInterrupted || got.ExitCode != -1 {
				t.Errorf("outcome %+v, want cancelled, interrupted, -1", got)
			}
			if ExitCode(err) != 1 {
				t.Errorf("exit status %d, want 1", ExitCode(err))
			}
			posted := h.api.verdicts()
			want := `{"verdict":"changes-requested","points":[{"text":"The full suite did not finish: the run was stopped.","action":"run_again"}]}`
			if len(posted) != c.posts || posted[0] != want {
				t.Errorf("posted %q, want %d of %s", posted, c.posts, want)
			}
		})
	}
}

// TestTheStateFolderIsRemovedAfterEveryEnd: after the helper judged, nothing of the script's
// files stays in the workspace for the copy step and the push step, whatever the end was.
func TestTheStateFolderIsRemovedAfterEveryEnd(t *testing.T) {
	for _, c := range []struct {
		name string
		end  *session.AfterResult
		out  session.Outcome
	}{
		{"red", &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, succeeded},
		{"green", &session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded},
		{"no after step", nil, succeeded},
		{"a failed session", &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, session.Outcome{State: event.StateFailed}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			h.state(t, "ran "+suiteSHA, redResult)
			h.after(c.end, c.out)
			if _, err := os.Lstat(filepath.Join(h.workspace, suite.StateFolder)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the state folder stays: %v", err)
			}
		})
	}
}

// TestTheSuiteCapIsTheTimeLimitOfTheAfterStep: the cap of the repository, or 60 minutes, is the
// after step's time limit.
func TestTheSuiteCapIsTheTimeLimitOfTheAfterStep(t *testing.T) {
	for _, c := range []struct {
		name       string
		extensions map[string]any
		want       time.Duration
	}{
		{"no setting", nil, 60 * time.Minute},
		{"a setting", map[string]any{"software-factory": map[string]any{"command_limits_minutes": map[string]any{"full_suite": 2}}}, 2 * time.Minute},
		{"a setting above 60", map[string]any{"software-factory": map[string]any{"command_limits_minutes": map[string]any{"full_suite": 90}}}, 60 * time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := workspaceRepo(t)
			api := newFakeAPI(t)
			api.withPlan(buildPlanText)
			spec := &runSpec{}
			spec.Dir = dir
			var said []string
			r := openSuites(context.Background(), suiteSetup{
				run: apiRun{api: api, dir: t.TempDir()}, spec: spec, runID: "r1", walled: true, fullSuite: true,
				extensions: c.extensions, said: func(l string) { said = append(said, l) },
			})
			if r == nil {
				t.Fatalf("no suite: %q", said)
			}
			if spec.AfterTimeout != c.want {
				t.Errorf("AfterTimeout = %v, want %v", spec.AfterTimeout, c.want)
			}
		})
	}
}

// workspaceRepo is a folder with a git repository with one commit.
func workspaceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet"}, {"-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// TestOpenSuitesSetsTheAfterStepOnlyWhereTheSessionRunsTheSuite: with full_suite true, a walled
// write run with a full row and a git folder gets After; each other case gets none.
func TestOpenSuitesSetsTheAfterStepOnlyWhereTheSessionRunsTheSuite(t *testing.T) {
	for _, c := range []struct {
		name      string
		fullSuite bool
		walled    bool
		readOnly  bool
		plan      string
		noGit     bool
		wantAfter bool
		fault     bool
		line      string
	}{
		{"full_suite is true", true, true, false, buildPlanText, false, true, false, ""},
		{"full_suite is false", false, true, false, buildPlanText, false, false, false, ""},
		{"no wall", true, false, false, buildPlanText, false, false, false, ""},
		{"a read run", true, true, true, buildPlanText, false, false, false, ""},
		{"no full row", true, true, false, "# Plan\n\n## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n| fast | `a` | none | s |\n", false, false, true, "Add a row of kind full to Commands in the build plan.\n  The full suite did not start: the build plan has no full row."},
		{"no build plan", true, true, false, "", false, false, true, "Write a build plan with a full row in Commands, then push it.\n  The full suite did not start: the work item has no usable build plan."},
		{"no git folder", true, true, false, buildPlanText, true, false, true, "The full suite did not start: the session's folder has no .git folder."},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := workspaceRepo(t)
			if c.noGit {
				if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
					t.Fatal(err)
				}
			}
			api := newFakeAPI(t)
			if c.plan != "" {
				api.withPlan(c.plan)
			}
			spec := &runSpec{}
			spec.Dir = dir
			var said []string
			r := openSuites(context.Background(), suiteSetup{
				run: apiRun{api: api, dir: t.TempDir()}, spec: spec, runID: "r1", walled: c.walled, readOnly: c.readOnly, fullSuite: c.fullSuite,
				said: func(l string) { said = append(said, l) },
			})
			if (r != nil) != (c.wantAfter || c.fault) || (len(spec.After) > 0) != c.wantAfter || (r != nil && r.fault != nil) != c.fault {
				t.Fatalf("suite %v, After %q, want After %v and fault %v", r != nil, spec.After, c.wantAfter, c.fault)
			}
			if c.line == "" && len(said) != 0 || c.line != "" && (len(said) != 1 || !strings.HasPrefix(said[0], c.line)) {
				t.Errorf("lines %q, want one that starts %q", said, c.line)
			}
			if c.wantAfter {
				if spec.After[4] != "make test" || spec.After[5] != "junit.xml" || len(spec.After[6]) != 40 {
					t.Errorf("After = %q", spec.After)
				}
			}
		})
	}
}

// TestTheAfterCommandHasTheLaunchPlansCommandNotAPushedOne: the plan is read once at the launch;
// a plan that the work item gets after it changes no word of After.
func TestTheAfterCommandHasTheLaunchPlansCommandNotAPushedOne(t *testing.T) {
	api := newFakeAPI(t)
	api.withPlan(buildPlanText)
	spec := &runSpec{}
	spec.Dir = workspaceRepo(t)
	r := openSuites(context.Background(), suiteSetup{
		run: apiRun{api: api, dir: t.TempDir()}, spec: spec, runID: "r1", walled: true, fullSuite: true, said: func(string) {},
	})
	if r == nil {
		t.Fatal("no suite")
	}
	before := slices.Clone(spec.After)
	// The agent pushes a plan with another command during the session.
	api.add(fakeVersion{artifact: "build-plan", name: "main", number: 2, runID: "r1", file: "build-plan.md", content: strings.Replace(buildPlanText, "make test", "true", 1)})
	if !slices.Equal(before, spec.After) || before[4] != "make test" {
		t.Errorf("After = %q, want the launch plan's command", spec.After)
	}
	if r.full.Command != "make test" {
		t.Errorf("the judged row is %q", r.full.Command)
	}
	if api.indexCalls != 1 {
		t.Errorf("the plan was read %d times, want 1", api.indexCalls)
	}
}

// TestLaunchFullReadsTheNewestUsableVersionOfTheBuildPlan: the plan is the artifact build-plan
// named main, or its one name; a version that a person cancelled is passed over, and a withdrawn name,
// a name that is not main among several, and a version with no plan file give none.
func TestLaunchFullReadsTheNewestUsableVersionOfTheBuildPlan(t *testing.T) {
	other := strings.Replace(buildPlanText, "make test", "make other", 1)
	third := strings.Replace(buildPlanText, "make test", "make third", 1)
	plan := func(name string, number int, text string) fakeVersion {
		return fakeVersion{artifact: "build-plan", name: name, number: number, runID: "r0", file: "build-plan.md", content: text}
	}
	for _, c := range []struct {
		name     string
		versions []fakeVersion
		want     string
		none     bool
	}{
		{"the newest version", []fakeVersion{plan("main", 1, other), plan("main", 2, buildPlanText)}, "make test", false},
		{"a cancelled newest version", []fakeVersion{plan("main", 1, other), {artifact: "build-plan", name: "main", number: 2, runID: "r0", cancelled: true, file: "build-plan.md", content: third}}, "make other", false},
		{"one name that is not main", []fakeVersion{plan("draft", 1, buildPlanText)}, "make test", false},
		{"two names and no main", []fakeVersion{plan("a", 1, buildPlanText), plan("b", 1, other)}, "", true},
		{"main among several", []fakeVersion{plan("a", 1, other), plan("main", 1, buildPlanText)}, "make test", false},
		{"a withdrawn name", []fakeVersion{plan("main", 1, buildPlanText), {artifact: "build-plan", name: "main", number: 2, runID: "r0", withdrawn: true, file: "x"}}, "", true},
		{"no plan file", []fakeVersion{{artifact: "build-plan", name: "main", number: 1, runID: "r0", file: "notes.md", content: buildPlanText}}, "", true},
		{"no build plan", []fakeVersion{{artifact: "verdict", name: "main", number: 1, runID: "r0", file: "verdict.json", content: "{}"}}, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := newFakeAPI(t)
			for _, v := range c.versions {
				api.add(v)
			}
			full, err := launchFull(context.Background(), apiRun{api: api, dir: t.TempDir()})
			if c.none != (err != nil) || full.Command != c.want && !c.none {
				t.Errorf("launchFull = %+v, %v, want command %q, none %v", full, err, c.want, c.none)
			}
			if c.none && !errors.Is(err, errNoBuildPlan) {
				t.Errorf("error %v is no errNoBuildPlan", err)
			}
		})
	}
}

// TestThePointTextsHaveAtMost100Characters: each first line of a point that qory posts has at most
// 100 characters, passes the verdict format, and does not end with two full stops.
func TestThePointTextsHaveAtMost100Characters(t *testing.T) {
	for _, text := range []string{
		fmt.Sprintf(textPointFailed, 100000, 100000), fmt.Sprintf(textPointExit, -1), textPointNoTests, textPointMismatch,
		fmt.Sprintf(textPointNoStartExit, 125), textPointNoStartShell, textPointNoStartMac, textPointNoStartNone, textPointNoStartFolder,
		textPointNoStartClone, textPointNoStartHead, fmt.Sprintf(textPointNoStartCap, 60), fmt.Sprintf(textPointTimeout, 60), textPointStopped,
		textPointDenied,
	} {
		if len(text) > 100 || !validPoint(runapi.Point{Text: text, Action: "run_again"}) || strings.HasSuffix(text, "..") {
			t.Errorf("the point text %q has %d characters, valid %v", text, len(text), validPoint(runapi.Point{Text: text, Action: "run_again"}))
		}
	}
	for _, f := range allFaults() {
		if len(f.point.Text) > 100 || !validPoint(f.point) || f.fixable != (f.point.Fix != "") {
			t.Errorf("fault %+v: text of %d characters, valid %v", f, len(f.point.Text), validPoint(f.point))
		}
	}
}

func allFaults() []*suiteFault {
	return []*suiteFault{
		faultOf(suite.ErrNoCommands), faultOf(suite.ErrNoFull), faultOf(suite.ErrNoCommand), faultOf(suite.ErrBadCommand),
		faultOf(suite.ErrBadResultFile), faultOf(errNoBuildPlan), faultOf(errBuildPlanTooBig), faultOf(suite.ErrBadStart), faultOf(errors.New("x")),
		noGitFault(), noAfterFault(), noRunAPIFault(),
	}
}

// TestTheLinesOfQoryRunStartWithTheFixInAtMost100Characters: qory run prints each line after "qory run: ",
// so the first line of each, without the prefix "qory run: ", has at most 90 characters. A fault with a fix prints the fix
// first and the cause on the next line, and a line shortens a point text that is longer.
func TestTheLinesOfQoryRunStartWithTheFixInAtMost100Characters(t *testing.T) {
	lines := []string{
		textSuiteSkipped, textSuiteGreen, textSuiteCut, fmt.Sprintf(textSuiteStands, "changes-requested"),
		fmt.Sprintf(textSuitePosted, "changes-requested"), textSuiteNoRoom, fmt.Sprintf(textSuiteNoPost, "x"), fmt.Sprintf(textSuiteNoClear, "x"),
		endedLine(suite.Outcome{State: suite.Timeout, Commit: suiteSHA, Counts: &suite.Counts{Tests: 100000, Failed: 100000}}),
		endedLine(suite.Outcome{State: suite.Passed, Commit: suiteSHA, Counts: &suite.Counts{Tests: 100000}}),
	}
	for _, f := range allFaults() {
		lines = append(lines, f.line(), shownLine(f.point.Text))
		if f.fixable && !strings.HasPrefix(f.line(), f.point.Fix+"\n") {
			t.Errorf("the line of the fault %q does not start with its fix: %q", f.point.Text, f.line())
		}
	}
	for _, text := range []string{
		fmt.Sprintf(textPointNoStartExit, 125), fmt.Sprintf(textPointNoStartExit, 100000), textPointNoStartMac, textPointNoStartNone,
		textPointNoStartShell, textPointNoStartFolder, textPointNoStartClone, textPointNoStartHead, fmt.Sprintf(textPointNoStartCap, 60),
	} {
		lines = append(lines, shownLine(text))
	}
	for _, line := range lines {
		first, _, _ := strings.Cut(line, "\n")
		if len(first) > 90 {
			t.Errorf("the first line %q has %d characters, want at most 90 before \"qory run: \"", first, len(first))
		}
		if strings.HasSuffix(strings.TrimSpace(line), "..") && !strings.HasSuffix(strings.TrimSpace(line), "...") || strings.Contains(line, ".. ") {
			t.Errorf("the line %q has two full stops", line)
		}
	}
	if got := shownLine(textPointNoStartMac); got == textPointNoStartMac || !strings.Contains(got, "Linux") {
		t.Errorf("the macOS line is %q", got)
	}
}

// TestTheTextForAContainerThatDidNotStart: on macOS the point says that the full suite needs a Linux
// machine; elsewhere it gives the exit status of the container, or says that it did not start. Each
// first line has at most 100 characters, and no line ends with two full stops.
func TestTheTextForAContainerThatDidNotStart(t *testing.T) {
	defer func(was string) { goos = was }(goos)
	for _, c := range []struct {
		goos string
		exit int
		want string
	}{
		{"darwin", -1, textPointNoStartMac},
		{"darwin", 125, textPointNoStartMac},
		{"linux", -1, "Contact support: the full suite did not start, because its container did not start."},
		{"linux", 125, "Contact support: the full suite did not start, because its container exited with status 125."},
		{"linux", 3, "Contact support: the full suite did not start, because its container exited with status 3."},
		{"darwin", 71, textPointNoStartClone},
		{"linux", 127, textPointNoStartShell},
	} {
		goos = c.goos
		got := notStartedLine(c.exit)
		if got != c.want || len(got) > 100 || strings.HasSuffix(got, "..") {
			t.Errorf("%s, exit %d: %q, want %q", c.goos, c.exit, got, c.want)
		}
	}
	if textPointNoStartMac != "Contact support: the full suite needs a Linux machine, and this run's machine runs Docker on macOS." {
		t.Errorf("the macOS text is %q", textPointNoStartMac)
	}
	// The line at the end of the run has the same cause in fewer words, to fit after "qory run: ".
	h := newHelper(t)
	goos = "darwin"
	h.after(&session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 125}, succeeded)
	if len(*h.said) == 0 || (*h.said)[0] != textShownMac {
		t.Errorf("lines %q", *h.said)
	}
}

// TestTheHostLineAtTheBoundaryOfAndNMore: with "and N more" the host has 11 characters for N of one
// digit and 10 for N of two digits; a host that fits stays whole, one character more is cut, and a cut
// host never ends in "." or "-" before "...".
func TestTheHostLineAtTheBoundaryOfAndNMore(t *testing.T) {
	for _, c := range []struct {
		more  int
		fit   int
		wants string
	}{{1, 11, "and 1 more"}, {12, 10, "and 12 more"}} {
		hosts := make([]string, c.more+1)
		for i := range hosts {
			hosts[i] = "other.example"
		}
		hosts[0] = strings.Repeat("a", c.fit)
		whole := hostsLine(hosts)
		if len(whole) != 100 || !strings.Contains(whole, " "+hosts[0]+" "+c.wants+" in the run's policy") {
			t.Errorf("%d more, a host of %d characters: %q (%d)", c.more, c.fit, whole, len(whole))
		}
		hosts[0] += "a"
		cut := hostsLine(hosts)
		if len(cut) > 100 || !strings.Contains(cut, strings.Repeat("a", c.fit-3)+"... "+c.wants) {
			t.Errorf("%d more, a host of %d characters: %q (%d)", c.more, c.fit+1, cut, len(cut))
		}
	}
	for _, host := range []string{"a.example.com.example", "abcdefghijk-lm-nop.example"} {
		got := hostsLine([]string{host})
		if strings.Contains(got, ".... ") || strings.Contains(got, "-... ") || strings.Contains(got, ".... ") {
			t.Errorf("the cut host in %q ends in a dot or a hyphen", got)
		}
	}
	if got := cutHost("abc.def.example", 7); got != "abc..." {
		t.Errorf("cutHost = %q", got)
	}
	if got := cutHost("abcd.efg.example", 8); got != "abcd..." {
		t.Errorf("cutHost = %q", got)
	}
}

// TestTheProgressLinesAreOneSentence: the line of a suite that ended names its state, the short commit
// and the counts.
func TestTheProgressLinesAreOneSentence(t *testing.T) {
	for _, c := range []struct {
		got  suite.Outcome
		want string
	}{
		{suite.Outcome{State: suite.Passed, Commit: suiteSHA, Counts: &suite.Counts{Tests: 2}}, "the full suite passed on 0123456789ab: 2 tests"},
		{suite.Outcome{State: suite.Failed, Commit: suiteSHA, Counts: &suite.Counts{Tests: 2, Failed: 1}}, "the full suite failed on 0123456789ab: 1 of 2 tests failed"},
		{suite.Outcome{State: suite.Timeout, Commit: suiteSHA}, "the full suite timed out on 0123456789ab"},
		{suite.Outcome{State: suite.Failed}, "the full suite failed"},
	} {
		if got := endedLine(c.got); got != c.want {
			t.Errorf("endedLine = %q, want %q", got, c.want)
		}
	}
}
