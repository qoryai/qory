package cmd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
)

// suitePlan is a build plan with a full row whose command writes the variable SUITE_RESULT to
// its result file junit.xml and exits with SUITE_EXIT.
const suitePlan = "## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n| full | `printf '%s' \"$SUITE_RESULT\" > junit.xml; exit ${SUITE_EXIT:-0}` | `junit.xml` | s |\n"

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

// suiteEnd is what the fake session and its after step do.
type suiteEnd struct {
	// result is what the suite's command writes to its result file, exit its exit status.
	result string
	exit   int
	// stay is whether the agent leaves HEAD where it started.
	stay bool
	// extensions are lines under harness.extensions in the checkout's qory.yaml, each
	// indented 4 spaces or more.
	extensions string
	// noFullSuite is whether the session runs no full suite: SetFullSuite(true) is left out.
	noFullSuite bool
	// after, when not nil, is the end of the after step in place of the script's.
	after *session.AfterResult
	// cutAfterStart runs the script, so the command starts, and then ends the step as this result
	// says, as the cap does for a command that never ends.
	cutAfterStart *session.AfterResult
	// events are lines that the run's events file has, after the after step's start and before its exit.
	events []string
	// seen is what the fake join sees of the spec: After and AfterTimeout.
	seen *session.SessionSpec
}

// suiteRun runs qory run behind the fake wall in a checkout whose agent commits and ends. The after
// step runs on this machine as the script in spec.After says, with the environment that Forager
// gives it, and the command it runs writes result and exits with exit. It returns the output and
// error of qory run, and the outcome the run closed with.
func suiteRun(t *testing.T, api *suiteAPI, e suiteEnd) (string, error, session.Outcome) {
	t.Helper()
	root := copyCheckout(t)
	if e.extensions != "" {
		file := filepath.Join(root, "qory.yaml")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, file, string(data)+"  extensions:\n"+e.extensions)
		runGit(t, root, "commit", "--quiet", "-am", "extensions")
	}
	t.Chdir(root)
	runDir := t.TempDir()
	outcomes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{RunAPI: api.srv.URL + "/v1/runs/r1", Dir: runDir})
	t.Cleanup(restore)
	if !e.noFullSuite {
		t.Cleanup(cmd.SetFullSuite(true))
	}
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		if e.seen != nil {
			*e.seen = spec
		}
		if !e.stay {
			gitOutput(t, spec.Dir, "-c", "user.name=Agent", "-c", "user.email=agent@example.com", "commit", "--quiet", "--allow-empty", "-m", "agent")
		}
		res := &session.Result{State: event.StateSucceeded}
		if len(spec.After) == 0 {
			return res, nil
		}
		res.After = e.after
		if res.After == nil {
			res.After = runAfter(t, spec, e)
			if e.cutAfterStart != nil {
				res.After = e.cutAfterStart
			}
		}
		lines := append([]string{afterLine("step_started", 3, "after")}, e.events...)
		lines = append(lines, afterLine("step_exited", 90, ""))
		writeFile(t, filepath.Join(runDir, "events.jsonl"), strings.Join(lines, "\n")+"\n")
		return res, nil
	}))
	out, err := run(t, "run", "claude", "--", "-p", "hi")
	got := outcomes()
	if len(got) != 1 {
		t.Fatalf("%d closes, want 1\n%s", len(got), out)
	}
	return out, err, got[0]
}

// afterLine is a step event of the after step 2, with the sequence seq.
func afterLine(typ string, seq int, kind string) string {
	return fmt.Sprintf(`{"type":"dev.qory.run.%s","sequence":"%010d","data":{"step":2,"name":"after","kind":"after"}}`, typ, seq)
}

// runAfter runs spec.After in the workspace as Forager's after step does, and returns its end.
func runAfter(t *testing.T, spec session.SessionSpec, e suiteEnd) *session.AfterResult {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(home, 0o500); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(spec.After[0], spec.After[1:]...)
	c.Dir = spec.Dir
	c.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null",
		"GIT_CONFIG_KEY_1=protocol.allow", "GIT_CONFIG_VALUE_1=never",
		"SUITE_RESULT=" + e.result, fmt.Sprintf("SUITE_EXIT=%d", e.exit),
	}
	out, err := c.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	state := event.StateSucceeded
	if code != 0 {
		state = event.StateFailed
	}
	return &session.AfterResult{Step: 2, State: state, ExitCode: code}
}

const (
	suiteRed   = `<testsuite><testcase name="a"/><testcase name="b"><failure/></testcase></testsuite>`
	suiteGreen = `<testsuite><testcase name="a"/><testcase name="b"/></testsuite>`
)

const changesRequested = `{"verdict":"changes-requested","points":[{"text":"The full suite failed: 1 of 2 tests failed.","action":"run_again"}]}`

// TestARedSuiteAfterTheSessionPostsChangesRequestedAndTheRunSucceeds is qory run with a session that
// moved HEAD: the suite runs in the after step, is red, qory posts one changes-requested, and the
// run closes as its session did, succeeded, with exit status 0. The run does not end failed.
func TestARedSuiteAfterTheSessionPostsChangesRequestedAndTheRunSucceeds(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	var seen session.SessionSpec
	out, err, closed := suiteRun(t, api, suiteEnd{result: suiteRed, exit: 1, seen: &seen})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if closed.State != event.StateSucceeded || closed.ExitCode != 0 {
		t.Errorf("the run closed %+v, want succeeded", closed)
	}
	if posted := api.posted(); len(posted) != 1 || posted[0] != changesRequested {
		t.Errorf("posted %q, want one changes-requested", posted)
	}
	if seen.AfterTimeout != time.Hour {
		t.Errorf("AfterTimeout = %v, want 60 minutes", seen.AfterTimeout)
	}
	wants(t, out, "the full suite failed on ", ": 1 of 2 tests failed")
}

// TestAGreenSuitePostsNothingAndTheRunSucceeds is the same run with a suite that passes, one
// that skips tests, and one with an unreadable result file.
func TestAGreenSuitePostsNothingAndTheRunSucceeds(t *testing.T) {
	for name, result := range map[string]string{
		"green":          suiteGreen,
		"skipped tests":  `<testsuite><testcase name="a"><skipped/></testcase><testcase name="b"/></testsuite>`,
		"unreadable":     "tests: 4 passed",
		"no result file": "",
	} {
		t.Run(name, func(t *testing.T) {
			api := newSuiteAPI(t, http.StatusCreated)
			out, err, closed := suiteRun(t, api, suiteEnd{result: result})
			if err != nil || closed.State != event.StateSucceeded {
				t.Fatalf("%v, closed %+v\n%s", err, closed, out)
			}
			if posted := api.posted(); len(posted) != 0 {
				t.Errorf("posted %q for a green suite", posted)
			}
			wants(t, out, "the full suite passed, so qory posts no verdict")
		})
	}
}

// TestASuiteThatRanNoTestsIsFailed: a result file with 0 tests proves nothing.
func TestASuiteThatRanNoTestsIsFailed(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	out, err, closed := suiteRun(t, api, suiteEnd{result: `<testsuite tests="0"/>`})
	if err != nil || closed.State != event.StateSucceeded {
		t.Fatalf("%v, closed %+v\n%s", err, closed, out)
	}
	if posted := api.posted(); len(posted) != 1 || !strings.Contains(posted[0], "The full suite ran 0 tests, so it proves nothing.") {
		t.Errorf("posted %q", posted)
	}
}

// TestASessionThatLeftHeadWhereItStartedPostsNothing is the skipped end: the script ends 0, no
// suite runs, no verdict goes out and the run succeeds.
func TestASessionThatLeftHeadWhereItStartedPostsNothing(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	out, err, closed := suiteRun(t, api, suiteEnd{result: suiteRed, exit: 1, stay: true})
	if err != nil || closed.State != event.StateSucceeded {
		t.Fatalf("%v, closed %+v\n%s", err, closed, out)
	}
	if posted := api.posted(); len(posted) != 0 {
		t.Errorf("posted %q for a session that changed nothing", posted)
	}
	wants(t, out, "the full suite did not run: the session left HEAD at the commit it started at")
}

// TestARunWithFullSuiteFalseRunsNoSuite: with full_suite false, the session has no after step.
func TestARunWithFullSuiteFalseRunsNoSuite(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	var seen session.SessionSpec
	out, err, closed := suiteRun(t, api, suiteEnd{result: suiteRed, exit: 1, noFullSuite: true, seen: &seen})
	if err != nil || closed.State != event.StateSucceeded {
		t.Fatalf("%v, closed %+v\n%s", err, closed, out)
	}
	if len(seen.After) != 0 || len(api.posted()) != 0 {
		t.Errorf("After %q, posted %q", seen.After, api.posted())
	}
}

// TestARunWhoseVerdictCannotBePostedDoesNotEndPassed: the run API refuses the verdict of the red
// suite, so the run closes failed with exit status 1, and the output has the fix line first.
func TestARunWhoseVerdictCannotBePostedDoesNotEndPassed(t *testing.T) {
	api := newSuiteAPI(t, http.StatusNotFound)
	out, err, closed := suiteRun(t, api, suiteEnd{result: suiteRed, exit: 1})
	if closed.State != event.StateFailed || closed.ExitCode != 1 {
		t.Errorf("the run closed %+v, want failed with exit code 1", closed)
	}
	if err == nil || cmd.ExitCode(err) != 1 {
		t.Errorf("qory run returned %v, exit status %d, want 1", err, cmd.ExitCode(err))
	}
	wants(t, out, "Take the work item in again in Qory Apiary: qory could not post the suite's verdict.")
}

// TestTheRepositorysCapReplacesSixtyMinutes: the after step's time limit is the repository's
// cap, and a step that the cap cut is red, with the cap in the point.
func TestTheRepositorysCapReplacesSixtyMinutes(t *testing.T) {
	for _, c := range []struct {
		name, extensions string
		limit            time.Duration
		minutes          string
	}{
		{"no setting", "", 60 * time.Minute, "its limit of 60 minutes."},
		{"a setting", "    software-factory:\n      command_limits_minutes: {full_suite: 2}\n", 2 * time.Minute, "its limit of 2 minutes."},
		{"a setting above 60", "    software-factory:\n      command_limits_minutes: {full_suite: 90}\n", 60 * time.Minute, "its limit of 60 minutes."},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := newSuiteAPI(t, http.StatusCreated)
			var seen session.SessionSpec
			cut := &session.AfterResult{Step: 2, State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: 143}
			out, err, closed := suiteRun(t, api, suiteEnd{extensions: c.extensions, cutAfterStart: cut, seen: &seen})
			if err != nil || closed.State != event.StateSucceeded {
				t.Fatalf("%v, closed %+v\n%s", err, closed, out)
			}
			if seen.AfterTimeout != c.limit {
				t.Errorf("AfterTimeout = %v, want %v", seen.AfterTimeout, c.limit)
			}
			posted := api.posted()
			if len(posted) != 1 || !strings.Contains(posted[0], c.minutes) {
				t.Errorf("posted %q, want a point with %q", posted, c.minutes)
			}
			wants(t, out, "the full suite timed out")
		})
	}
}

// TestARedSuiteThatFollowsARefusedHostNamesTheHost: the point's first line has the host that the
// proxy refused while the after step ran, and no host that it refused before or after.
func TestARedSuiteThatFollowsARefusedHostNamesTheHost(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	events := []string{
		`{"type":"dev.qory.run.egress","sequence":"0000000004","data":{"host":"registry.example","port":443,"method":"CONNECT","decision":"denied"}}`,
		`{"type":"dev.qory.run.egress","sequence":"0000000005","data":{"host":"cdn.example","port":443,"method":"CONNECT","decision":"denied"}}`,
	}
	out, err, closed := suiteRun(t, api, suiteEnd{result: suiteRed, exit: 1, events: events})
	if err != nil || closed.State != event.StateSucceeded {
		t.Fatalf("%v, closed %+v\n%s", err, closed, out)
	}
	want := `{"verdict":"changes-requested","points":[{"text":"Allow registry... and 1 more in the run's policy, or change the suite: it failed after the refusals.","action":"run_again","fix":"Refused connections: the run's Connections tab, Denied."}]}`
	if posted := api.posted(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestTheCapBeforeTheCommandPostsAnEscalate: a step cut at the cap before the script wrote `ran`
// never started the command: qory posts escalate with the point "did not start", and the run
// closes as its session did.
func TestTheCapBeforeTheCommandPostsAnEscalate(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	cut := &session.AfterResult{Step: 2, State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: 143}
	out, err, closed := suiteRun(t, api, suiteEnd{after: cut})
	if err != nil || closed.State != event.StateSucceeded {
		t.Fatalf("%v, closed %+v\n%s", err, closed, out)
	}
	want := `{"verdict":"escalate","points":[{"text":"The full suite did not start: the after step used its 60 minutes before the command.","action":"run_again"}]}`
	if posted := api.posted(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestTheCapBeforeTheCommandNamesTheRepositorysLimit: with command_limits_minutes.full_suite 7, the
// point for a step cut at the cap before the command says 7 minutes.
func TestTheCapBeforeTheCommandNamesTheRepositorysLimit(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	cut := &session.AfterResult{Step: 2, State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: 143}
	var seen session.SessionSpec
	extensions := "    software-factory:\n      command_limits_minutes: {full_suite: 7}\n"
	out, err, closed := suiteRun(t, api, suiteEnd{after: cut, extensions: extensions, seen: &seen})
	if err != nil || closed.State != event.StateSucceeded {
		t.Fatalf("%v, closed %+v\n%s", err, closed, out)
	}
	if seen.AfterTimeout != 7*time.Minute {
		t.Errorf("AfterTimeout = %v, want 7 minutes", seen.AfterTimeout)
	}
	want := `{"verdict":"escalate","points":[{"text":"The full suite did not start: the after step used its 7 minutes before the command.","action":"run_again"}]}`
	if posted := api.posted(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}

// TestAStopDuringTheAfterStepDoesNotLeaveTheRunSucceeded: a stop cuts the after step of a session that
// succeeded. qory posts the verdict of the red suite and closes the run cancelled with the
// reason interrupted, with exit status 1.
func TestAStopDuringTheAfterStepDoesNotLeaveTheRunSucceeded(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	cut := &session.AfterResult{Step: 2, State: event.StateCancelled, ExitCode: -1}
	out, err, closed := suiteRun(t, api, suiteEnd{after: cut})
	if closed.State != event.StateCancelled || closed.Reason != event.ReasonInterrupted {
		t.Errorf("the run closed %+v, want cancelled with the reason interrupted\n%s", closed, out)
	}
	if cmd.ExitCode(err) != 1 {
		t.Errorf("qory run returned %v, exit status %d, want 1", err, cmd.ExitCode(err))
	}
	if posted := api.posted(); len(posted) != 1 || !strings.Contains(posted[0], "The full suite did not finish: the run was stopped.") {
		t.Errorf("posted %q", posted)
	}
}

// TestAFullSuiteThatCannotStartPostsAnEscalate: a script that ends before the command ran gives
// the verdict escalate with the point, and the run still closes as its session did.
func TestAFullSuiteThatCannotStartPostsAnEscalate(t *testing.T) {
	api := newSuiteAPI(t, http.StatusCreated)
	t.Cleanup(cmd.SetGOOS("linux"))
	failed := &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: -1}
	out, err, closed := suiteRun(t, api, suiteEnd{after: failed})
	if err != nil || closed.State != event.StateSucceeded {
		t.Fatalf("%v, closed %+v\n%s", err, closed, out)
	}
	want := `{"verdict":"escalate","points":[{"text":"Contact support: the full suite did not start, because its container did not start.","action":"run_again"}]}`
	if posted := api.posted(); len(posted) != 1 || posted[0] != want {
		t.Errorf("posted %q, want %s", posted, want)
	}
}
