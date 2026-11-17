package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/runapi"
	"github.com/qoryai/qory/internal/secretmask"
	"github.com/qoryai/qory/internal/suite"
)

func TestSuiteResultFileReadsReviewSuiteResultOfTheInputs(t *testing.T) {
	for _, c := range []struct {
		name, inputs, want string
		fails              bool
	}{
		{"no inputs", "", "", false},
		{"no review", `{"qory":{}}`, "", false},
		{"no member", `{"review":{"commit":"x"}}`, "", false},
		{"a path", `{"review":{"suite_result":"/run/in/suite.json"}}`, "/run/in/suite.json", false},
		{"inputs that are no JSON", `not json`, "", false},
		{"a member that is no string", `{"review":{"suite_result":4}}`, "", false},
		{"a relative path", `{"review":{"suite_result":"suite.json"}}`, "", true},
		{"a path with ..", `{"review":{"suite_result":"/run/../etc/x"}}`, "", true},
		{"an empty path", `{"review":{"suite_result":""}}`, "", true},
		{"a path with a NUL", `{"review":{"suite_result":"/run/a\u0000b"}}`, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := suiteResultFile([]byte(c.inputs))
			if got != c.want || (err != nil) != c.fails {
				t.Errorf("suiteResultFile = %q, %v, want %q, fails %v", got, err, c.want, c.fails)
			}
			if err != nil {
				if first, _, _ := strings.Cut(err.Error(), "\n"); len(first) > 100 {
					t.Errorf("the first line has %d characters: %q", len(first), first)
				}
			}
		})
	}
}

// readResult reads the file as a map.
func readResult(t *testing.T, file string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("the file %q: %v", data, err)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("the file ends %q", data[max(0, len(data)-5):])
	}
	return got
}

// TestTheResultFileHasTheStateOfEveryEnd: qory writes the file for each end of the suite, also for a
// session that did not succeed. A suite that did not pass is one finding in it, and no verdict
// goes out, whatever the end.
func TestTheResultFileHasTheStateOfEveryEnd(t *testing.T) {
	secretmask.Default().Add("s3cret-value-for-the-test")
	one := 1
	finding := func(text string) *runapi.Point { return &runapi.Point{Text: text, Action: "run_again"} }
	for _, c := range []struct {
		name    string
		status  string
		result  string
		output  string
		seconds string
		end     *session.AfterResult
		session session.Outcome
		want    suiteResult
	}{
		{"green", "ran " + suiteSHA, greenResult, "ok\n", "95", &session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded,
			suiteResult{Commit: suiteSHA, State: "passed", Counts: countsOf(2, 0, 0), Minutes: ptr(2), Output: "ok\n"}},
		{"red", "ran " + suiteSHA, redResult, "FAIL\n", "5", &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, succeeded,
			suiteResult{Commit: suiteSHA, State: "failed", Counts: countsOf(2, 1, 0), Minutes: &one, Output: "FAIL\n", Finding: finding("The full suite failed: 1 of 2 tests failed.")}},
		{"a secret in the output", "ran " + suiteSHA, greenResult, "token s3cret-value-for-the-test\n", "5", &session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded,
			suiteResult{Commit: suiteSHA, State: "passed", Counts: countsOf(2, 0, 0), Minutes: &one, Output: "token " + secretmask.Placeholder + "\n"}},
		{"the cap", "ran " + suiteSHA, "", "", "", &session.AfterResult{Step: 2, State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: 143}, succeeded,
			suiteResult{Commit: suiteSHA, State: "timeout", Minutes: ptr(7), Finding: finding("The full suite did not finish: the after step reached its limit of 7 minutes.")}},
		{"a stop", "ran " + suiteSHA, "", "", "", &session.AfterResult{Step: 2, State: event.StateCancelled, ExitCode: -1}, succeeded,
			suiteResult{Commit: suiteSHA, State: "stopped", Finding: finding("The full suite did not finish: the run was stopped.")}},
		{"skipped", "skipped", "", "", "", &session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded,
			suiteResult{State: "skipped"}},
		{"the script never started", "", "", "", "", &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 71}, succeeded,
			suiteResult{State: "failed", Finding: finding("The full suite did not start: git could not clone HEAD of the workspace.")}},
		{"the after step did not run", "", "", "", "", nil, succeeded, suiteResult{State: "did_not_run", Finding: finding("The full suite did not run: the run closed before the after step.")}},
		{"a session that failed", "ran " + suiteSHA, redResult, "FAIL\n", "5", &session.AfterResult{Step: 2, State: event.StateFailed, ExitCode: 1}, session.Outcome{State: event.StateFailed, ExitCode: 3},
			suiteResult{Commit: suiteSHA, State: "failed", Counts: countsOf(2, 1, 0), Minutes: &one, Output: "FAIL\n", Finding: finding("The full suite failed: 1 of 2 tests failed.")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t)
			h.resultFile = filepath.Join(t.TempDir(), "suite.json")
			if c.status != "" {
				h.state(t, c.status, c.result)
				folder := filepath.Join(h.workspace, ".git/qory-suite")
				if c.output != "" {
					os.WriteFile(filepath.Join(folder, "output"), []byte(c.output), 0o644)
				}
				if c.seconds != "" {
					os.WriteFile(filepath.Join(folder, "seconds"), []byte(c.seconds+"\n"), 0o644)
				}
			}
			writeEvents(t, h.runDir, afterStarted(3, 2), afterExited(4, 2))
			h.after(c.end, c.session)
			if posted := h.api.verdicts(); len(posted) != 0 {
				t.Errorf("posted %q with a result file", posted)
			}
			var want map[string]any
			data, _ := json.Marshal(c.want)
			json.Unmarshal(data, &want)
			if got := readResult(t, h.resultFile); !reflect.DeepEqual(got, want) {
				t.Errorf("the file has %v, want %v", got, want)
			}
			if info, _ := os.Stat(h.resultFile); info.Mode().Perm() != 0o644 {
				t.Errorf("mode %v, want 0644", info.Mode().Perm())
			}
		})
	}
}

func ptr(n int) *int { return &n }

func countsOf(tests, failed, skipped int) *suite.Counts {
	return &suite.Counts{Tests: tests, Failed: failed, Skipped: skipped}
}

// TestARunWithNoResultFileWritesNone: with no review.suite_result, qory writes no file and says
// nothing about it.
func TestARunWithNoResultFileWritesNone(t *testing.T) {
	h := newHelper(t)
	h.state(t, "ran "+suiteSHA, greenResult)
	h.after(&session.AfterResult{Step: 2, State: event.StateSucceeded}, succeeded)
	entries, _ := os.ReadDir(h.workspace)
	for _, e := range entries {
		if strings.Contains(e.Name(), "suite") {
			t.Errorf("a file %s", e.Name())
		}
	}
	for _, l := range *h.said {
		if strings.Contains(l, "review.suite_result") {
			t.Errorf("a line about the file: %q", l)
		}
	}
}

// TestAResultFileThatCannotBeWrittenIsALine: a folder that is gone, or a link, gives the line with
// the fix first, writes nothing, posts nothing, and closes a run whose session succeeded
// failed with exit status 1: the run is not left passed.
func TestAResultFileThatCannotBeWrittenIsALine(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]string{
		"a folder that is gone":   filepath.Join(root, "gone", "suite.json"),
		"a folder that is a link": filepath.Join(link, "suite.json"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHelper(t)
			h.resultFile = file
			writeEvents(t, h.runDir, afterStarted(3, 2), afterExited(4, 2))
			got, err := h.after(ranRed(t, h), succeeded)
			if got.State != event.StateFailed || got.ExitCode != 1 || ExitCode(err) != 1 {
				t.Errorf("outcome %+v, error %v, want failed with 1", got, err)
			}
			var line string
			for _, l := range *h.said {
				if strings.HasPrefix(l, "Check the folder of review.suite_result") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("lines %q have no line about the file", *h.said)
			}
			if first, _, _ := strings.Cut(line, "\n"); len(first) > 100 {
				t.Errorf("the first line has %d characters", len(first))
			}
			if entries, _ := os.ReadDir(real); len(entries) != 0 {
				t.Errorf("a file in the folder the link leads to: %v", entries)
			}
			if len(h.api.verdicts()) != 0 {
				t.Errorf("posted %q", h.api.verdicts())
			}
		})
	}
}

// TestAResultPathThatQoryRefusesPostsNoVerdictAndFailsTheRun: inputs that name review.suite_result
// with a relative path print the line at the launch, and the run then writes nothing, posts
// nothing for any end, and closes failed with exit status 1 when its session succeeded.
func TestAResultPathThatQoryRefusesPostsNoVerdictAndFailsTheRun(t *testing.T) {
	for name, end := range map[string]session.AfterResult{
		"green": {Step: 2, State: event.StateSucceeded},
		"red":   {Step: 2, State: event.StateFailed, ExitCode: 1},
		"stop":  {Step: 2, State: event.StateCancelled, ExitCode: -1},
	} {
		t.Run(name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.withPlan(buildPlanText)
			spec := &runSpec{}
			spec.Dir = workspaceRepo(t)
			var said []string
			runDir := t.TempDir()
			r := openSuites(context.Background(), suiteSetup{
				run: apiRun{api: api, dir: runDir}, spec: spec, runID: "r1", walled: true, fullSuite: true,
				inputs: []byte(`{"review":{"suite_result":"suite.json"}}`), said: func(l string) { said = append(said, l) },
			})
			if r == nil || len(said) != 1 || !strings.HasPrefix(said[0], "Set review.suite_result") {
				t.Fatalf("suite %v, lines %q", r != nil, said)
			}
			r.said = func(l string) { said = append(said, l) }
			status := "ran " + suiteSHA
			folder := filepath.Join(spec.Dir, ".git/qory-suite")
			os.MkdirAll(folder, 0o755)
			os.WriteFile(filepath.Join(folder, "status"), []byte(status+"\n"), 0o644)
			e := end
			got, err := r.after(context.Background(), context.Background(), time.Time{}, &session.Result{After: &e}, succeeded)
			if name != "stop" && (got.State != event.StateFailed || got.ExitCode != 1 || ExitCode(err) != 1) {
				t.Errorf("outcome %+v, error %v, want failed with 1", got, err)
			}
			if name == "stop" && (got.State != event.StateCancelled || ExitCode(err) != 1) {
				t.Errorf("outcome %+v, error %v, want cancelled with 1", got, err)
			}
			if posted := api.verdicts(); len(posted) != 0 {
				t.Errorf("posted %q", posted)
			}
		})
	}
}

// TestAStopWithAResultFileClosesTheRunCancelledAndPostsNothing.
func TestAStopWithAResultFileClosesTheRunCancelledAndPostsNothing(t *testing.T) {
	h := newHelper(t)
	h.resultFile = filepath.Join(t.TempDir(), "suite.json")
	h.state(t, "ran "+suiteSHA, "")
	got, err := h.after(&session.AfterResult{Step: 2, State: event.StateCancelled, ExitCode: -1}, succeeded)
	if got.State != event.StateCancelled || got.Reason != event.ReasonInterrupted || ExitCode(err) != 1 {
		t.Errorf("outcome %+v, error %v", got, err)
	}
	if posted := h.api.verdicts(); len(posted) != 0 {
		t.Errorf("posted %q", posted)
	}
	if res := readResult(t, h.resultFile); res["state"] != "stopped" {
		t.Errorf("the file has %v", res)
	}
}

// TestWithAResultFileNoVerdictGoesOutForAnyEnd: green, red, did not start and the cap all write the
// file and post nothing, over any prior verdict of the agent.
func TestWithAResultFileNoVerdictGoesOutForAnyEnd(t *testing.T) {
	for _, agent := range []string{"", `{"verdict":"passed"}`, `{"verdict":"escalate","points":[{"text":"x","action":"run_again"}]}`} {
		for name, end := range map[string]session.AfterResult{
			"green":     {Step: 2, State: event.StateSucceeded},
			"red":       {Step: 2, State: event.StateFailed, ExitCode: 1},
			"not start": {Step: 2, State: event.StateFailed, ExitCode: 71},
			"cap":       {Step: 2, State: event.StateCancelled, Reason: event.ReasonTimeout, ExitCode: 143},
		} {
			h := newHelper(t)
			h.resultFile = filepath.Join(t.TempDir(), "suite.json")
			if agent != "" {
				h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: agent})
			}
			if name != "not start" {
				h.state(t, "ran "+suiteSHA, "")
			}
			e := end
			if got, err := h.after(&e, succeeded); err != nil || got != succeeded {
				t.Errorf("%s: outcome %+v, error %v", name, got, err)
			}
			if posted := h.api.verdicts(); len(posted) != 0 {
				t.Errorf("%s: posted %q", name, posted)
			}
		}
	}
}
