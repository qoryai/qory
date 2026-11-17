package cmd_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/link"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// suitePlan is a build plan with a full row whose result file is junit.xml.
const suitePlan = "## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n| full | `make test` | `junit.xml` | s |\n"

// suiteAPI is a run API for a work item with the build plan suitePlan and no verdict: it
// answers the artifact index and the plan's file, and POST verdict with status, and keeps each
// verdict it got.
type suiteAPI struct {
	srv    *httptest.Server
	status int

	mu       sync.Mutex
	verdicts []string
}

func newSuiteAPI(t *testing.T, status int) *suiteAPI {
	t.Helper()
	a := &suiteAPI{status: status}
	sum := sha256.Sum256([]byte(suitePlan))
	index := fmt.Sprintf(`{"version":1,"artifacts":[{"type":"build-plan","name":"main","newest_version":1,"versions":[{"id":"bp1","number":1,"writer":{"kind":"run","run_id":"earlier"},"files":[{"filename":"build-plan.md","size":%d,"sha256":%q}]}]}]}`, len(suitePlan), hex.EncodeToString(sum[:]))
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/artifacts"):
			io.WriteString(w, index)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/files/build-plan.md"):
			io.WriteString(w, suitePlan)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/verdict"):
			body, _ := io.ReadAll(r.Body)
			a.mu.Lock()
			a.verdicts = append(a.verdicts, string(body))
			a.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(a.status)
			if a.status == http.StatusCreated {
				io.WriteString(w, `{"version":1,"artifact":{"type":"verdict","name":"main","version":{"id":"v1","number":1}}}`)
			} else {
				io.WriteString(w, `{"version":1,"error":"unknown_run","message":"no such run"}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *suiteAPI) posted() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.verdicts...)
}

// suiteRun runs qory run behind the fake wall in a checkout whose agent asks the route for the full
// suite of its commit; the container of the suite writes result and exits with exit. It returns
// the output and error of qory run, and the outcome the run closed with.
func suiteRun(t *testing.T, api *suiteAPI, result string, exit int) (string, error, session.Outcome) {
	t.Helper()
	return suiteRunWith(t, api, "", func(_ context.Context, spec session.SessionSpec) (*session.Result, error) {
		if result != "" {
			if err := os.WriteFile(filepath.Join(spec.Dir, "junit.xml"), []byte(result), 0o644); err != nil {
				return nil, err
			}
		}
		return &session.Result{ExitCode: exit}, nil
	})
}

// suiteRunWith is [suiteRun] with the container of the suite as suite, and extensions, lines
// under harness.extensions in the checkout's qory.yaml, each indented 4 spaces or more.
func suiteRunWith(t *testing.T, api *suiteAPI, extensions string, suite func(context.Context, session.SessionSpec) (*session.Result, error)) (string, error, session.Outcome) {
	t.Helper()
	root := copyCheckout(t)
	if extensions != "" {
		file := filepath.Join(root, "qory.yaml")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, file, string(data)+"  extensions:\n"+extensions)
		runGit(t, root, "commit", "--quiet", "-am", "extensions")
	}
	t.Chdir(root)
	var route http.Handler
	outcomes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		RunAPI: api.srv.URL + "/v1/runs/r1", OnOpen: func(spec session.OpenSpec) { route = spec.QoryRoute },
	})
	t.Cleanup(restore)
	t.Cleanup(cmd.SetGitSession(func(ctx context.Context, spec session.SessionSpec) (*session.Result, error) {
		if spec.Command != "sh" {
			return cmd.GitSessionOnHost(ctx, spec)
		}
		return suite(ctx, spec)
	}))
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		// The agent commits in its checkout, which is a copy: the commit is in no other folder.
		gitOutput(t, spec.Dir, "-c", "user.name=Agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "--allow-empty", "-m", "agent")
		commit := strings.TrimSpace(gitOutput(t, spec.Dir, "rev-parse", "HEAD"))
		r := httptest.NewRequest(http.MethodPost, link.QoryHostURL+"/v1/suites", strings.NewReader(`{"commit":"`+commit+`"}`))
		r.Header.Set(link.QorySessionHeader, "1")
		w := httptest.NewRecorder()
		route.ServeHTTP(w, r)
		if w.Code != http.StatusCreated {
			t.Errorf("POST /v1/suites: %d %s", w.Code, w.Body)
		}
		return &session.Result{State: event.StateSucceeded}, nil
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	got := outcomes()
	if len(got) != 1 {
		t.Fatalf("%d closes, want 1\n%s", len(got), out)
	}
	return out, err, got[0]
}

const (
	suiteRed   = `<testsuite><testcase name="a"/><testcase name="b"><failure/></testcase></testsuite>`
	suiteGreen = `<testsuite><testcase name="a"/><testcase name="b"/></testsuite>`
)

// TestARunWaitsForTheSuiteAfterItsSessionAndPostsTheVerdict is qory run with a session whose
// script started the full suite: the suite is red after the session, qory posts one
// changes-requested, and the run closes as its session did, succeeded.
func TestARunWaitsForTheSuiteAfterItsSessionAndPostsTheVerdict(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	out, err, closed := suiteRun(t, api, suiteRed, 0)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if closed.State != event.StateSucceeded {
		t.Errorf("the run closed %+v, want succeeded", closed)
	}
	posted := api.posted()
	if len(posted) != 1 || !strings.HasPrefix(posted[0], `{"verdict":"changes-requested","points":[{"text":"The full suite failed on `) {
		t.Errorf("posted %q, want one changes-requested", posted)
	}
	wants(t, out, "started the full suite su_", "is failed, 1 of 2 tests failed")
}

// TestAGreenSuitePostsNothingAndTheRunSucceeds is the same run with a suite that passes.
func TestAGreenSuitePostsNothingAndTheRunSucceeds(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	out, err, closed := suiteRun(t, api, suiteGreen, 0)
	if err != nil || closed.State != event.StateSucceeded {
		t.Fatalf("%v, closed %+v\n%s", err, closed, out)
	}
	if posted := api.posted(); len(posted) != 0 {
		t.Errorf("posted %q for a green suite", posted)
	}
	wants(t, out, "the full suite passed, so qory posts no verdict")
}

// TestARunWhoseVerdictCannotBePostedDoesNotEndPassed: the run API refuses the verdict of the red
// suite, so the run closes failed with exit status 1, and the output has the fix line first.
func TestARunWhoseVerdictCannotBePostedDoesNotEndPassed(t *testing.T) {
	api := newSuiteAPI(t, http.StatusNotFound)
	out, err, closed := suiteRun(t, api, suiteRed, 0)
	if closed.State != event.StateFailed || closed.ExitCode != 1 {
		t.Errorf("the run closed %+v, want failed with exit code 1", closed)
	}
	if err == nil || cmd.ExitCode(err) != 1 {
		t.Errorf("qory run returned %v, exit status %d, want 1", err, cmd.ExitCode(err))
	}
	wants(t, out, "Take the work item in again in Qory Apiary: qory could not post the verdict of the red full suite.")
}

// TestTheRepositorysLimitReplacesSixtyMinutes is a suite that never ends: with no setting it is
// stopped at 60 minutes, with command_limits_minutes.full_suite 2 at 2 minutes, in a minute that
// the test makes 20 milliseconds long, and the verdict says which.
func TestTheRepositorysLimitReplacesSixtyMinutes(t *testing.T) {
	t.Cleanup(cmd.SetSuiteMinute(20 * time.Millisecond))
	forever := func(ctx context.Context, _ session.SessionSpec) (*session.Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	for _, c := range []struct{ name, extensions, minutes string }{
		{"no setting", "", "within 60 minutes"},
		{"a setting", "    software-factory:\n      command_limits_minutes: {full_suite: 2}\n", "within 2 minutes"},
		{"a setting above 60", "    software-factory:\n      command_limits_minutes: {full_suite: 90}\n", "within 60 minutes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := newSuiteAPI(t, http.StatusCreated)
			out, err, closed := suiteRunWith(t, api, c.extensions, forever)
			if err != nil || closed.State != event.StateSucceeded {
				t.Fatalf("%v, closed %+v\n%s", err, closed, out)
			}
			posted := api.posted()
			if len(posted) != 1 || !strings.Contains(posted[0], c.minutes+", and qory stopped it.") {
				t.Errorf("posted %q, want a point with %q", posted, c.minutes)
			}
			wants(t, out, "is timeout")
		})
	}
}
