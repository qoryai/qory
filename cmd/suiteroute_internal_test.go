package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/link"

	"github.com/qoryai/qory/internal/suite"
)

const (
	suiteCommit  = "0123456789abcdef0123456789abcdef01234567"
	suiteCommit2 = "89abcdef0123456789abcdef0123456789abcdef"
)

// suiteWrites is a [suite.Runner] that writes result to the result file and ends with exit.
func suiteWrites(result string, exit int) suite.Runner {
	return func(_ context.Context, job suite.Job, out io.Writer) (int, error) {
		fmt.Fprintln(out, "ran "+job.Full.Command)
		if result != "" {
			if err := os.WriteFile(filepath.Join(job.Dir, job.Full.ResultFile), []byte(result), 0o644); err != nil {
				return -1, err
			}
		}
		return exit, nil
	}
}

// suiteBlocks is a [suite.Runner] that runs until its context ends, and sends each commit it
// starts on started.
func suiteBlocks(started chan<- string) suite.Runner {
	return func(ctx context.Context, job suite.Job, _ io.Writer) (int, error) {
		started <- job.Commit
		<-ctx.Done()
		return -1, ctx.Err()
	}
}

// suiteClone is a clone that makes the folder of the suite and writes nothing in it.
func suiteClone(_ context.Context, _, _, dest string) error { return os.Mkdir(dest, 0o700) }

// suiteRoute is a route open to session 1 with a registry that runs run, and the lines the host
// said.
func suiteRoute(t *testing.T, ctx context.Context, run suite.Runner, mutate ...func(*suite.Config)) (*qoryRoute, *suite.Registry, *[]string) {
	t.Helper()
	cfg := suite.Config{
		Source: "source", Root: t.TempDir(), Full: suite.Full{Command: "make test", ResultFile: "junit.xml"},
		Limit: time.Minute, Clone: suiteClone, Run: run,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	reg := suite.New(ctx, cfg)
	t.Cleanup(func() { _ = reg.Close() })
	var said []string
	q := newQoryRoute()
	q.useSuites(&suiteHost{reg: reg, said: func(line string) { said = append(said, line) }})
	closeRoute := q.open(1)
	t.Cleanup(closeRoute)
	return q, reg, &said
}

// suiteAsk sends the route a request with body and returns the status and the body.
func suiteAsk(t *testing.T, q *qoryRoute, method, path, body string) (int, map[string]any, http.Header) {
	t.Helper()
	r := httptest.NewRequest(method, link.QoryHostURL+path, strings.NewReader(body))
	r.Header.Set(link.QorySessionHeader, "1")
	w := httptest.NewRecorder()
	q.ServeHTTP(w, r)
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%s %s: the body %q: %v", method, path, w.Body.String(), err)
	}
	return w.Code, got, w.Header()
}

// suiteEnded reads suite id until it is not running.
func suiteEnded(t *testing.T, q *qoryRoute, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, got, _ := suiteAsk(t, q, http.MethodGet, "/v1/suites/"+id, "")
		if status != http.StatusOK {
			t.Fatalf("GET %s: %d %v", id, status, got)
		}
		if got["state"] != "running" {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("suite %s still runs", id)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestASuiteRequestStartsASuiteAndAnswersAtOnce is POST /v1/suites with a commit: 201 with the
// suite's id, the commit and the state started, while the suite runs; the same commit gets the
// same suite; GET reads the state, the counts and the output.
func TestASuiteRequestStartsASuiteAndAnswersAtOnce(t *testing.T) {
	started := make(chan string, 1)
	q, reg, said := suiteRoute(t, context.Background(), suiteBlocks(started))
	status, got, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", `{"commit":"`+suiteCommit+`"}`)
	id, _ := got["suite"].(string)
	if status != http.StatusCreated || !strings.HasPrefix(id, "su_") || got["commit"] != suiteCommit || got["state"] != "started" {
		t.Fatalf("POST: %d %v, want 201 with a suite, the commit and started", status, got)
	}
	<-started
	status, again, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", ` {"commit": "`+suiteCommit+`"} `)
	if status != http.StatusOK || again["suite"] != id || again["state"] != "running" {
		t.Errorf("the same commit again: %d %v, want 200 with suite %s running", status, again, id)
	}
	status, read, _ := suiteAsk(t, q, http.MethodGet, "/v1/suites/"+id+"?x=1", "")
	if status != http.StatusOK || read["state"] != "running" || read["counts"] != nil || read["commit"] != suiteCommit || read["suite"] != id {
		t.Errorf("GET: %d %v", status, read)
	}
	if _, has := read["minutes"].(float64); !has || read["output"] != "" {
		t.Errorf("GET has minutes %v and output %q", read["minutes"], read["output"])
	}
	if len(*said) != 1 || !strings.Contains((*said)[0], id) || !strings.Contains((*said)[0], suiteCommit[:12]) {
		t.Errorf("lines %q, want one that has the suite and the short commit", *said)
	}
	reg.Stop()
}

// TestASuiteRequestWithAnyFieldButCommitIsRefused is a request with a ref, a branch, a command,
// or any other field: 400 field_not_allowed with the text of the contract, naming the first such
// field of the body, cut so the first line keeps 100 characters; no suite starts.
func TestASuiteRequestWithAnyFieldButCommitIsRefused(t *testing.T) {
	started := make(chan string, 1)
	q, reg, _ := suiteRoute(t, context.Background(), suiteBlocks(started))
	long := strings.Repeat("x", 80)
	for _, c := range []struct{ body, field string }{
		{`{"commit":"` + suiteCommit + `","ref":"refs/heads/main"}`, "ref"},
		{`{"branch":"main","commit":"` + suiteCommit + `"}`, "branch"},
		{`{"commit":"` + suiteCommit + `","command":"rm -rf /"}`, "command"},
		{`{"cap":5}`, "cap"},
		{`{"zeta":1,"alpha":2}`, "zeta"},
		{`{"` + long + `":1}`, long[:33] + "..."},
		{`{"a\nb\u0007":1}`, "a?b?"},
		{`{"Commit":"` + suiteCommit + `"}`, "Commit"},
	} {
		status, got, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", c.body)
		want := "Send only commit in the suite request: field " + c.field + " is not allowed."
		if status != http.StatusBadRequest || got["error"] != "field_not_allowed" || got["message"] != want {
			t.Errorf("%s: %d %v, want 400 field_not_allowed %q", c.body, status, got, want)
		}
		if line := got["message"].(string); len(line) > 100 {
			t.Errorf("%s: a first line of %d characters", c.body, len(line))
		}
	}
	if _, ok := reg.Newest(); ok {
		t.Error("a refused request started a suite")
	}
}

// TestASuiteRequestThatIsNoRequestIsRefusedWithTheFix: each fault of the body or the commit gets
// its code and a text that starts with the fix.
func TestASuiteRequestThatIsNoRequestIsRefusedWithTheFix(t *testing.T) {
	q, reg, _ := suiteRoute(t, context.Background(), suiteWrites("", 0))
	for _, c := range []struct {
		name, body, code string
		status           int
	}{
		{"no body", ``, routeBadBody, 400},
		{"not JSON", `commit=` + suiteCommit, routeBadBody, 400},
		{"an array", `["` + suiteCommit + `"]`, routeBadBody, 400},
		{"cut", `{"commit":"` + suiteCommit + `"`, routeBadBody, 400},
		{"data after the object", `{"commit":"` + suiteCommit + `"} {}`, routeBadBody, 400},
		{"no commit", `{}`, routeCommitMissing, 400},
		{"a commit that is no text", `{"commit":5}`, routeBadCommit, 400},
		{"a ref", `{"commit":"HEAD"}`, routeBadCommit, 400},
		{"a short commit", `{"commit":"0123456"}`, routeBadCommit, 400},
		{"an upper-case commit", `{"commit":"` + strings.ToUpper(suiteCommit) + `"}`, routeBadCommit, 400},
		{"the commit twice", `{"commit":"` + suiteCommit + `","commit":"` + suiteCommit + `"}`, routeBadCommit, 400},
	} {
		status, got, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", c.body)
		if status != c.status || got["error"] != c.code {
			t.Errorf("%s: %d %v, want %d %s", c.name, status, got, c.status, c.code)
		}
		if line := got["message"].(string); strings.Contains(line, "\n") || len(line) > 100 {
			t.Errorf("%s: the text %q is not one line of 100 characters", c.name, line)
		}
	}
	if _, ok := reg.Newest(); ok {
		t.Error("a refused request started a suite")
	}
}

// TestTheSuitePathsRefuseWhatTheyDoNotServe: another method on a suite path gets 405 with Allow,
// another path 404, an unknown suite id 404 unknown_suite with the text of the contract.
func TestTheSuitePathsRefuseWhatTheyDoNotServe(t *testing.T) {
	q, _, _ := suiteRoute(t, context.Background(), suiteWrites("", 0))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/suites"}, {http.MethodDelete, "/v1/suites"}, {http.MethodPost, "/v1/suites/su_1"}, {http.MethodDelete, "/v1/suites/su_1"},
	} {
		status, got, header := suiteAsk(t, q, c.method, c.path, "")
		if status != http.StatusMethodNotAllowed || got["error"] != routeMethod || header.Get("Allow") != "GET, POST" {
			t.Errorf("%s %s: %d %v Allow %q", c.method, c.path, status, got, header.Get("Allow"))
		}
	}
	for _, path := range []string{"/", "/v1", "/v1/suites/", "/v1/suites/su_1/output", "/v1/runs"} {
		if status, got, _ := suiteAsk(t, q, http.MethodGet, path, ""); status != http.StatusNotFound || got["error"] != routeNotFound || got["message"] != textRouteNotFoundSuites {
			t.Errorf("GET %s: %d %v, want 404 not_found", path, status, got)
		}
	}
	status, got, _ := suiteAsk(t, q, http.MethodGet, "/v1/suites/su_0000", "")
	if status != http.StatusNotFound || got["error"] != "unknown_suite" || got["message"] != "Use the suite id that POST /v1/suites returned: su_0000 is unknown." {
		t.Errorf("an unknown suite: %d %v", status, got)
	}
	long := "su_" + strings.Repeat("z", 90)
	_, got, _ = suiteAsk(t, q, http.MethodGet, "/v1/suites/"+long, "")
	if line := got["message"].(string); len(line) > 100 || !strings.Contains(line, long[:30]) {
		t.Errorf("the text for a long id is %d characters: %q", len(line), line)
	}
}

// TestTheSuiteGetBodyHasTheStateTheCountsAndTheOutput reads a suite that ended: passed with the
// counts of its result file, failed with a failed test, and the output.
func TestTheSuiteGetBodyHasTheStateTheCountsAndTheOutput(t *testing.T) {
	result := `<testsuite><testcase name="a"/><testcase name="b"><failure/></testcase><testcase name="c"><skipped/></testcase></testsuite>`
	q, _, _ := suiteRoute(t, context.Background(), suiteWrites(result, 0))
	_, got, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", `{"commit":"`+suiteCommit+`"}`)
	read := suiteEnded(t, q, got["suite"].(string))
	counts, _ := read["counts"].(map[string]any)
	if read["state"] != "failed" || counts["tests"] != float64(3) || counts["failed"] != float64(1) || counts["skipped"] != float64(1) || len(counts) != 3 ||
		read["minutes"] != float64(1) || read["output"] != "ran make test\n" {
		t.Errorf("GET: %v", read)
	}
}

// TestASuiteForANewerCommitStopsTheOlderOne: the older suite ends stopped, the newer runs.
func TestASuiteForANewerCommitStopsTheOlderOne(t *testing.T) {
	started := make(chan string, 2)
	q, _, _ := suiteRoute(t, context.Background(), suiteBlocks(started))
	_, first, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", `{"commit":"`+suiteCommit+`"}`)
	<-started
	_, second, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", `{"commit":"`+suiteCommit2+`"}`)
	<-started
	if first["suite"] == second["suite"] {
		t.Fatal("two commits got one suite")
	}
	if old := suiteEnded(t, q, first["suite"].(string)); old["state"] != "stopped" {
		t.Errorf("the older suite is %v, want stopped", old["state"])
	}
	if _, now, _ := suiteAsk(t, q, http.MethodGet, "/v1/suites/"+second["suite"].(string), ""); now["state"] != "running" {
		t.Errorf("the newer suite is %v, want running", now["state"])
	}
}

// TestASuiteWithNoCommandIsRefusedBeforeItStarts: a run whose launch build plan has no full row, or
// none, answers 409 with the fix, and a commit that the clone cannot take answers 422.
func TestASuiteWithNoCommandIsRefusedBeforeItStarts(t *testing.T) {
	for _, c := range []struct {
		name string
		none error
		code string
		text string
	}{
		{"no build plan", errNoBuildPlan, routeNoBuildPlan, textRouteNoBuildPlan},
		{"no table", suite.ErrNoCommands, routeNoFullCommand, textRouteNoFullCommand},
		{"no full row", suite.ErrNoFull, routeNoFullCommand, textRouteNoFullCommand},
		{"no command", suite.ErrNoCommand, routeNoFullCommand, textRouteNoFullCommand},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newQoryRoute()
			q.useSuites(&suiteHost{none: c.none, said: func(string) {}})
			closeRoute := q.open(1)
			defer closeRoute()
			status, got, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", `{"commit":"`+suiteCommit+`"}`)
			if status != http.StatusConflict || got["error"] != c.code || got["message"] != c.text {
				t.Errorf("%d %v, want 409 %s", status, got, c.code)
			}
			if len(c.text) > 100 {
				t.Errorf("the text has %d characters", len(c.text))
			}
			if status, got, _ = suiteAsk(t, q, http.MethodGet, "/v1/suites/su_1", ""); status != http.StatusNotFound || got["error"] != routeUnknownSuite {
				t.Errorf("GET: %d %v", status, got)
			}
			if status, got, _ = suiteAsk(t, q, http.MethodPost, "/v1/suites", `{"ref":"x"}`); status != http.StatusBadRequest || got["error"] != routeFieldNotAllowed {
				t.Errorf("a bad body with no command: %d %v, want the 400 first", status, got)
			}
		})
	}
	t.Run("a commit the clone cannot take", func(t *testing.T) {
		q, _, said := suiteRoute(t, context.Background(), suiteWrites("", 0), func(c *suite.Config) {
			c.Clone = func(context.Context, string, string, string) error { return fmt.Errorf("git fetch: not our ref") }
		})
		status, got, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", `{"commit":"`+suiteCommit+`"}`)
		if status != http.StatusUnprocessableEntity || got["error"] != routeCommitNotTaken || got["message"] != textRouteCommitNotTaken {
			t.Errorf("%d %v, want 422 %s", status, got, routeCommitNotTaken)
		}
		if len(*said) != 1 || !strings.Contains((*said)[0], "not our ref") {
			t.Errorf("lines %q, want one with git's reason", *said)
		}
	})
	t.Run("a run that ends", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		q, _, _ := suiteRoute(t, ctx, suiteWrites("", 0))
		cancel()
		status, got, _ := suiteAsk(t, q, http.MethodPost, "/v1/suites", `{"commit":"`+suiteCommit+`"}`)
		if status != http.StatusServiceUnavailable || got["error"] != routeRunEnding {
			t.Errorf("%d %v, want 503 %s", status, got, routeRunEnding)
		}
	})
}

// TestTheSuiteTextsStartWithTheFix holds each text of the suite paths to one line of at most 100
// characters, with the longest name a text shows.
func TestTheSuiteTextsStartWithTheFix(t *testing.T) {
	name := strings.Repeat("n", suiteNameMax)
	for _, text := range []string{
		textRouteMethod, textRouteNotFoundSuites, textRouteBadBody, fmt.Sprintf(textRouteField, name), textRouteCommitMissing,
		textRouteBadCommit, textRouteCommitNotTaken, fmt.Sprintf(textRouteUnknownSuite, name), textRouteNoBuildPlan, textRouteNoFullCommand, textRouteRunEnding, textRouteSuiteFolder,
	} {
		if strings.Contains(text, "\n") || len(text) > 100 {
			t.Errorf("%q is more than one line of 100 characters", text)
		}
	}
}
