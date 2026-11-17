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
	posted        []string
	indexCalls    int
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

// helperRun is a suiteRun with a registry that runs run, the lines it said, and the api.
type helperRun struct {
	*suiteRun
	api  *fakeAPI
	said *[]string
}

// newHelper makes the suites of a run whose session exited, with a fake run API and a fake
// container, and the lines the helper said. mutate changes the configuration of the registry.
func newHelper(t *testing.T, run suite.Runner, mutate ...func(*suite.Config)) helperRun {
	t.Helper()
	api := newFakeAPI(t)
	var said []string
	var mu sync.Mutex
	r := &suiteRun{
		run: apiRun{api: api, dir: t.TempDir()}, runID: "r1", limit: time.Minute, minutes: 1, timeout: time.Hour,
		said: func(line string) { mu.Lock(); said = append(said, line); mu.Unlock() },
	}
	cfg := suite.Config{
		Source: "source", Root: t.TempDir(), Full: suite.Full{Command: "make test", ResultFile: "junit.xml"},
		Limit: r.limit, Clone: suiteClone, Run: run, Ended: r.ended,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	r.reg = suite.New(context.Background(), cfg)
	t.Cleanup(r.close)
	return helperRun{r, api, &said}
}

// succeeded is the outcome of a session that succeeded.
var succeeded = session.Outcome{State: event.StateSucceeded}

// start starts a suite of commit and returns when it runs.
func (h helperRun) start(t *testing.T, commit string) {
	t.Helper()
	if _, _, err := h.reg.Start(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
}

// after runs the helper as run.go does, with no stop and the limit given.
func (h helperRun) after(ctx context.Context, limit time.Time, out session.Outcome) (session.Outcome, error) {
	return h.suiteRun.after(ctx, context.Background(), limit, func() stopRead { return stopRead{stopped: ctx.Err() != nil} }, out)
}

const (
	redResult   = `<testsuite><testcase name="a"/><testcase name="b"><failure/></testcase></testsuite>`
	greenResult = `<testsuite><testcase name="a"/><testcase name="b"/></testsuite>`
	bodyChanges = `{"verdict":"changes-requested","points":[{"text":"The full suite failed on 0123456789ab: 1 of 2 tests failed.","action":"run_again"}]}`
)

// TestARedSuitePostsChangesRequestedAfterTheSessionEnded is the helper's work after the agent
// exited: a suite that was started and that ends red after the session, posts one
// changes-requested verdict with one point, over a passed verdict and over none.
func TestARedSuitePostsChangesRequestedAfterTheSessionEnded(t *testing.T) {
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
			release := make(chan struct{})
			h := newHelper(t, func(_ context.Context, job suite.Job, out io.Writer) (int, error) {
				<-release
				return suiteWrites(redResult, 0)(context.Background(), job, out)
			})
			for _, v := range c.agent {
				h.api.add(v)
			}
			h.start(t, suiteCommit)
			// The suite outlasts the session: it still runs when the helper starts.
			time.AfterFunc(50*time.Millisecond, func() { close(release) })
			got, err := h.after(context.Background(), time.Time{}, succeeded)
			if err != nil || got != succeeded {
				t.Errorf("outcome %+v, error %v, want the session's outcome", got, err)
			}
			if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != bodyChanges {
				t.Errorf("posted %q, want one %s", posted, bodyChanges)
			}
		})
	}
}

// TestACommandThatExitsOtherThanZeroPostsTheCountsOfItsResultFile: a suite whose command wrote a
// result file and exited 1 is red, and its point has the counts of the file.
func TestACommandThatExitsOtherThanZeroPostsTheCountsOfItsResultFile(t *testing.T) {
	h := newHelper(t, suiteWrites(redResult, 1))
	h.start(t, suiteCommit)
	if got, err := h.after(context.Background(), time.Time{}, succeeded); err != nil || got != succeeded {
		t.Errorf("outcome %+v, error %v", got, err)
	}
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != bodyChanges {
		t.Errorf("posted %q, want one %s", posted, bodyChanges)
	}
}

// TestAGreenSuitePostsNothingAndTheAgentsVerdictStands: a suite that passes posts no verdict, and
// neither does a run that started no suite or a session that did not succeed.
func TestAGreenSuitePostsNothingAndTheAgentsVerdictStands(t *testing.T) {
	h := newHelper(t, suiteWrites(greenResult, 0))
	h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: `{"verdict":"escalate","points":[{"text":"Pick one.","action":"answer","question":"q1"}]}`})
	h.start(t, suiteCommit)
	if got, err := h.after(context.Background(), time.Time{}, succeeded); err != nil || got != succeeded {
		t.Errorf("outcome %+v, error %v", got, err)
	}
	if posted := h.api.verdicts(); len(posted) != 0 {
		t.Errorf("posted %q for a green suite", posted)
	}
	if h.api.indexCalls != 0 {
		t.Errorf("the helper read the artifact index %d times for a green suite", h.api.indexCalls)
	}
	none := newHelper(t, suiteWrites(redResult, 0))
	if got, err := none.after(context.Background(), time.Time{}, succeeded); err != nil || got != succeeded || len(none.api.verdicts()) != 0 {
		t.Errorf("no suite: %+v, %v, posted %q", got, err, none.api.verdicts())
	}
	failedSession := session.Outcome{State: event.StateFailed, ExitCode: 3}
	red := newHelper(t, suiteWrites(redResult, 0))
	red.start(t, suiteCommit)
	if got, err := red.after(context.Background(), time.Time{}, failedSession); err != nil || got != failedSession || len(red.api.verdicts()) != 0 {
		t.Errorf("a failed session: %+v, %v, posted %q", got, err, red.api.verdicts())
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
	suitePointText := `{"text":"The full suite failed on 0123456789ab: 1 of 2 tests failed.","action":"run_again"}`
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
			h := newHelper(t, suiteWrites(redResult, 0))
			h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: c.agent})
			h.start(t, suiteCommit)
			if got, err := h.after(context.Background(), time.Time{}, succeeded); err != nil || got != succeeded {
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
		{"the verdict file is no JSON", func(h helperRun) {
			h.api.add(fakeVersion{artifact: "verdict", name: "main", number: 1, runID: "r1", file: "verdict.json", content: "passed"})
		}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHelper(t, suiteWrites(redResult, 0))
			c.setup(h)
			h.start(t, suiteCommit)
			got, err := h.after(context.Background(), time.Time{}, succeeded)
			if got.State == event.StateSucceeded || got.State != event.StateFailed || got.ExitCode != 1 {
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

// TestTheTimeLimitAndAStopEndTheWait: a suite that outlasts the run's time limit, or that a stop
// reaches, is stopped and red, the verdict goes out, and the run ends cancelled as a cut push
// step ends it.
func TestTheTimeLimitAndAStopEndTheWait(t *testing.T) {
	was := pushTimes
	pushTimes.limit, pushTimes.signal = 5*time.Second, 5*time.Second
	t.Cleanup(func() { pushTimes = was })
	block := func(ctx context.Context, _ suite.Job, _ io.Writer) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	t.Run("the time limit", func(t *testing.T) {
		h := newHelper(t, block)
		h.start(t, suiteCommit)
		begin := time.Now()
		got, err := h.after(context.Background(), time.Now().Add(60*time.Millisecond), succeeded)
		if got.State != event.StateCancelled || got.Reason != event.ReasonTimeout || got.ExitCode != exitTimeout || ExitCode(err) != exitTimeout {
			t.Errorf("outcome %+v, error %v, want cancelled with timeout", got, err)
		}
		if took := time.Since(begin); took > 3*time.Second {
			t.Errorf("the wait took %s", took)
		}
		posted := h.api.verdicts()
		if len(posted) != 1 || !strings.Contains(posted[0], `"changes-requested"`) || !strings.Contains(posted[0], "was stopped before it finished") {
			t.Errorf("posted %q, want changes-requested for a stopped suite", posted)
		}
	})
	t.Run("a stop", func(t *testing.T) {
		h := newHelper(t, block)
		h.start(t, suiteCommit)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(60*time.Millisecond, cancel)
		begin := time.Now()
		got, err := h.after(ctx, time.Time{}, succeeded)
		if got.State != event.StateCancelled || got.Reason != event.ReasonInterrupted || ExitCode(err) != 1 {
			t.Errorf("outcome %+v, error %v, want cancelled with interrupted", got, err)
		}
		if took := time.Since(begin); took > 3*time.Second {
			t.Errorf("the wait took %s", took)
		}
		if posted := h.api.verdicts(); len(posted) != 1 || !strings.Contains(posted[0], `"changes-requested"`) {
			t.Errorf("posted %q", posted)
		}
	})
}

// TestASuiteThatEndedBeforeTheLimitIsNotCut: a red suite that ended before the run's time limit
// passed is judged as it ended, and the run's outcome stays the session's.
func TestASuiteThatEndedBeforeTheLimitIsNotCut(t *testing.T) {
	h := newHelper(t, suiteWrites(redResult, 0))
	h.start(t, suiteCommit)
	if _, ok, err := h.reg.Wait(context.Background()); !ok || err != nil {
		t.Fatalf("Wait = %v, %v", ok, err)
	}
	got, err := h.after(context.Background(), time.Now().Add(-time.Second), succeeded)
	if err != nil || got != succeeded {
		t.Errorf("outcome %+v, error %v, want the session's outcome", got, err)
	}
	if posted := h.api.verdicts(); len(posted) != 1 || posted[0] != bodyChanges {
		t.Errorf("posted %q, want one %s", posted, bodyChanges)
	}
}

// TestCloseRemovesTheEmptyFolderOfTheSuites: the run's folder has no suites folder once the run's
// suites are closed.
func TestCloseRemovesTheEmptyFolderOfTheSuites(t *testing.T) {
	api := newFakeAPI(t)
	api.withPlan(buildPlanText)
	source, commit := suiteSource(t)
	was := runGitContainer
	runGitContainer = func(context.Context, gitContainer) (int, error) { return 0, nil }
	t.Cleanup(func() { runGitContainer = was })
	run := apiRun{api: api, dir: t.TempDir()}
	route := newQoryRoute()
	suites := openSuites(context.Background(), suiteSetup{run: run, spec: &runSpec{}, route: route, runID: "r1", walled: true, source: source, said: func(string) {}})
	closeRoute := route.open(1)
	status, got, _ := suiteAsk(t, route, http.MethodPost, "/v1/suites", `{"commit":"`+commit+`"}`)
	closeRoute()
	if status != http.StatusCreated {
		t.Fatalf("POST: %d %v", status, got)
	}
	if _, err := os.Stat(filepath.Join(run.dir, suitesDir)); err != nil {
		t.Fatalf("the suites folder is not there while the suite exists: %v", err)
	}
	suites.close()
	if _, err := os.Stat(filepath.Join(run.dir, suitesDir)); !os.IsNotExist(err) {
		t.Errorf("the suites folder is still there after close: %v", err)
	}
}

// TestASuiteOverItsCapIsStoppedAndRed: a suite past its cap ends timeout, and the post says so
// with the cap's minutes.
func TestASuiteOverItsCapIsStoppedAndRed(t *testing.T) {
	h := newHelper(t, func(ctx context.Context, _ suite.Job, _ io.Writer) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	}, func(c *suite.Config) { c.Limit = 40 * time.Millisecond })
	h.minutes = 7
	h.start(t, suiteCommit)
	if got, err := h.after(context.Background(), time.Time{}, succeeded); err != nil || got != succeeded {
		t.Errorf("outcome %+v, error %v", got, err)
	}
	posted := h.api.verdicts()
	if len(posted) != 1 || !strings.Contains(posted[0], "did not finish on 0123456789ab within 7 minutes") {
		t.Errorf("posted %q", posted)
	}
}

// suiteSource is a git repository with one commit, and the commit's id.
func suiteSource(t *testing.T) (dir, commit string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(home))
	dir = filepath.Join(home, "source")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "commit.gpgsign=false"}, args...)...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	git("add", ".")
	git("commit", "--quiet", "-m", "one")
	return dir, git("rev-parse", "HEAD")
}

// TestTheSuiteRunsTheCommandOfTheLaunchBuildPlan: the full row that openSuites reads when the run
// starts is what a request runs, in a container with the clone's folder, also after the agent pushed
// a newer build plan with another command.
func TestTheSuiteRunsTheCommandOfTheLaunchBuildPlan(t *testing.T) {
	api := newFakeAPI(t)
	api.withPlan(buildPlanText)
	source, commit := suiteSource(t)
	var mu sync.Mutex
	var ran []string
	was := runGitContainer
	runGitContainer = func(_ context.Context, c gitContainer) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, c.Session.Args[1])
		return 0, nil
	}
	t.Cleanup(func() { runGitContainer = was })
	route := newQoryRoute()
	run := apiRun{api: api, dir: t.TempDir()}
	suites := openSuites(context.Background(), suiteSetup{run: run, spec: &runSpec{}, route: route, runID: "r1", walled: true, source: source, said: func(string) {}})
	t.Cleanup(suites.close)
	// The agent pushes a newer build plan with another full command during the session.
	api.add(fakeVersion{artifact: "build-plan", name: "main", number: 2, runID: "r1", file: "build-plan.md", content: strings.Replace(buildPlanText, "make test", "true", 1)})
	closeRoute := route.open(1)
	defer closeRoute()
	status, got, _ := suiteAsk(t, route, http.MethodPost, "/v1/suites", `{"commit":"`+commit+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST: %d %v", status, got)
	}
	suiteEnded(t, route, got["suite"].(string))
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 1 || ran[0] != "make test" {
		t.Errorf("the suite ran %q, want the launch plan's full command make test", ran)
	}
}

// TestARunWithNoWallOrNoFullCommandHasNoSuite: no wall gives no suite and a route that serves no
// path; a walled run whose launch plan has no full row, or no plan, answers each request with the
// reason, and its helper does nothing.
func TestARunWithNoWallOrNoFullCommandHasNoSuite(t *testing.T) {
	api := newFakeAPI(t)
	run := apiRun{api: api, dir: t.TempDir()}
	route := newQoryRoute()
	if suites := openSuites(context.Background(), suiteSetup{run: run, spec: &runSpec{}, route: route, runID: "r1", walled: false, said: func(string) {}}); suites != nil {
		t.Error("a run with no wall has suites")
	}
	closeRoute := route.open(1)
	if status, got, _ := suiteAsk(t, route, http.MethodPost, "/v1/suites", `{"commit":"`+suiteCommit+`"}`); status != http.StatusNotFound || got["error"] != routeNotFound {
		t.Errorf("no wall: %d %v, want 404 not_found", status, got)
	}
	closeRoute()
	for _, c := range []struct{ name, plan, code string }{
		{"no plan", "", routeNoBuildPlan},
		{"no full row", "## Commands\n\n| Kind | Command | Result file | Source |\n|---|---|---|---|\n| targeted | `x` | `r.xml` | s |\n", routeNoFullCommand},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := newFakeAPI(t)
			if c.plan != "" {
				api.withPlan(c.plan)
			}
			var said []string
			route := newQoryRoute()
			suites := openSuites(context.Background(), suiteSetup{run: apiRun{api: api, dir: t.TempDir()}, spec: &runSpec{}, route: route, runID: "r1", walled: true, said: func(l string) { said = append(said, l) }})
			if suites == nil || suites.reg != nil {
				t.Error("a run with no usable plan has a registry, or no record of why not")
			}
			closeRoute := route.open(1)
			defer closeRoute()
			for range 2 {
				if status, got, _ := suiteAsk(t, route, http.MethodPost, "/v1/suites", `{"commit":"`+suiteCommit+`"}`); status != http.StatusConflict || got["error"] != c.code {
					t.Errorf("%d %v, want 409 %s", status, got, c.code)
				}
			}
			if got, err := suites.after(context.Background(), context.Background(), time.Time{}, nil, succeeded); err != nil || got != succeeded {
				t.Errorf("the helper changed the outcome: %+v, %v", got, err)
			}
			if len(said) != 1 || !strings.HasPrefix(said[0], "Add a full row to the build plan and take the work item in again: no suite started.\n  ") {
				t.Errorf("lines %q, want one line for two requests", said)
			}
		})
	}
}

// TestAWriteToTheCheckoutAfterTheRequestLeavesTheSuitesFilesUnchanged: the commit is cloned before
// the request returns, so the agent that rewrites the checkout, and its git folder, while the suite
// runs changes nothing the suite sees.
func TestAWriteToTheCheckoutAfterTheRequestLeavesTheSuitesFilesUnchanged(t *testing.T) {
	source, commit := suiteSource(t)
	release := make(chan struct{})
	seen := make(chan string, 1)
	run := func(_ context.Context, job suite.Job, _ io.Writer) (int, error) {
		<-release
		got, err := os.ReadFile(filepath.Join(job.Dir, "main.go"))
		if err != nil {
			seen <- err.Error()
			return 1, nil
		}
		seen <- string(got)
		return 0, nil
	}
	q, _, _ := suiteRoute(t, context.Background(), run, func(c *suite.Config) { c.Source, c.Clone = source, nil })
	status, got, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", `{"commit":"`+commit+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("POST: %d %v", status, got)
	}
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(source, ".git")); err != nil {
		t.Fatal(err)
	}
	close(release)
	if content := <-seen; content != "package main\n" {
		t.Errorf("the suite read %q, want the commit's file", content)
	}
}

// TestTheHelperJudgesTheNewestSuiteAndNotOneThatANewerCommitStopped: the older suite is red because
// a newer commit replaced it, and the newer one is green: no verdict goes out.
func TestTheHelperJudgesTheNewestSuiteAndNotOneThatANewerCommitStopped(t *testing.T) {
	started := make(chan string, 2)
	h := newHelper(t, func(ctx context.Context, job suite.Job, out io.Writer) (int, error) {
		started <- job.Commit
		if job.Commit == suiteCommit {
			<-ctx.Done()
			return -1, ctx.Err()
		}
		return suiteWrites(greenResult, 0)(ctx, job, out)
	})
	h.start(t, suiteCommit)
	<-started
	h.start(t, suiteCommit2)
	got, err := h.after(context.Background(), time.Time{}, succeeded)
	if err != nil || got != succeeded {
		t.Errorf("outcome %+v, error %v", got, err)
	}
	if posted := h.api.verdicts(); len(posted) != 0 {
		t.Errorf("posted %q, want none: the newest suite passed", posted)
	}
}

// TestASecondSignalEndsTheWaitAtOnce: the second signal cuts the post too, so the run closes
// without a verdict, cancelled and interrupted.
func TestASecondSignalEndsTheWaitAtOnce(t *testing.T) {
	h := newHelper(t, func(ctx context.Context, _ suite.Job, _ io.Writer) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	})
	h.start(t, suiteCommit)
	ctx, cancel := context.WithCancel(context.Background())
	again, cancelAgain := context.WithCancel(context.Background())
	cancel()
	cancelAgain()
	got, err := h.suiteRun.after(ctx, again, time.Time{}, func() stopRead { return stopRead{stopped: true, again: true, got: "interrupt"} }, succeeded)
	if got.State != event.StateCancelled || got.Reason != event.ReasonInterrupted || ExitCode(err) != 1 {
		t.Errorf("outcome %+v, error %v", got, err)
	}
	var said []string
	said = append(said, *h.said...)
	if !slices.Contains(said, textSuiteCutSecond) {
		t.Errorf("lines %q have no %q", said, textSuiteCutSecond)
	}
}

// TestAPostAfterACutHasOneTry: a wait that the time limit cut posts its verdict once, even when the
// run API answers 503, so the push step keeps its time.
func TestAPostAfterACutHasOneTry(t *testing.T) {
	was := branchWait
	branchWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { branchWait = was })
	wasTimes := pushTimes
	pushTimes.limit, pushTimes.signal = 5*time.Second, 5*time.Second
	t.Cleanup(func() { pushTimes = wasTimes })
	h := newHelper(t, func(ctx context.Context, _ suite.Job, _ io.Writer) (int, error) {
		<-ctx.Done()
		return -1, ctx.Err()
	})
	h.api.verdictStatus = http.StatusServiceUnavailable
	h.start(t, suiteCommit)
	got, err := h.after(context.Background(), time.Now().Add(50*time.Millisecond), succeeded)
	if got.Reason != event.ReasonTimeout || ExitCode(err) != exitTimeout {
		t.Errorf("outcome %+v, error %v, want the time limit's", got, err)
	}
	if n := len(h.api.verdicts()); n != 1 {
		t.Errorf("%d posts after a cut, want 1", n)
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

// TestTheLinesOfTheSuiteStartWithTheFixInAtMost100Characters holds the first line of each line of qory
// run about the full suite to 100 characters, with the longest values a line shows.
func TestTheLinesOfTheSuiteStartWithTheFixInAtMost100Characters(t *testing.T) {
	id, commit, state := "su_0123abcd", "0123456789ab", suite.Timeout
	for _, line := range []string{
		fmt.Sprintf(textSuiteNotStarted, "x"), fmt.Sprintf(textSuiteNoFolder, "x"), fmt.Sprintf(textSuiteStarted, id, commit),
		fmt.Sprintf(textSuiteWaiting, id, commit), fmt.Sprintf(textSuiteEnded, id, commit, state, ", 100000 of 100000 tests failed"),
		textSuiteGreen, fmt.Sprintf(textSuiteStands, "changes-requested"), fmt.Sprintf(textSuitePosted, "changes-requested"), textSuiteNoRoom,
		fmt.Sprintf(textSuiteNoPost, "x"), fmt.Sprintf(textSuiteCutLimit, 1000*time.Hour), fmt.Sprintf(textSuiteCutStop, "SIGTERM"), textSuiteCutSecond,
	} {
		first, _, _ := strings.Cut(line, "\n")
		if len(first) > 100 {
			t.Errorf("the first line %q has %d characters", first, len(first))
		}
	}
}
