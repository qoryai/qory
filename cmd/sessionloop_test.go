package cmd_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/qoryai/forager/accesskey"
	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/link"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/report"
	rt "github.com/qoryai/qory/internal/runtimeinstall/runtimeinstalltest"
)

// laterAnswer is a next-session answer that starts the workflow hello on runtime with the
// model key credential, for minutes, with no model and no full suite.
func laterAnswer(n int, runtime, credential string, minutes int) string {
	b, _ := json.Marshal(map[string]any{
		"next": "session", "session": n, "workflow": "hello",
		"model_choice": map[string]any{"runtime": runtime}, "model_credential": credential,
		"time_limit_minutes": minutes, "full_suite": false, "inputs": map[string]any{"version": 1},
	})
	return string(b)
}

// loopJoin is one join of a [loopRun]: the session's spec, the route's status for a
// request of the session's number and of the number before it, during the join, and the
// harness report and inputs.json of the run's home then.
type loopJoin struct {
	spec              session.SessionSpec
	served, servedOld int
	report            report.Report
	inputs            string
}

// loopRun is what a run of several sessions came to.
type loopRun struct {
	harnessRun
	joins  []loopJoin
	bodies []string
	home   string
}

// homeReport is the harness report of the run's home, which a run from a run code has in
// the folder given with --home, the zero report when there is none.
func homeReport(home string) report.Report {
	found, _ := filepath.Glob(filepath.Join(home, "*-report.json"))
	if len(found) != 1 {
		return report.Report{}
	}
	rep, _ := report.Read(found[0])
	return rep
}

// loopSetup is the run of [runLoop]. The next-session request after session n gets
// answers[n-1], or the end once answers run out. result, when set, is the result of the
// join of session n; a nil result and error is a session that succeeded. change, when set,
// changes the documents, and nowhere the run's [cmd.Nowhere]. joined, when set, gets the
// spec of session n as it joins. extends, when true, makes the harness document extend the
// stack of the folder examples/hello in place of defining one.
type loopSetup struct {
	extends bool
	answers []string
	joined  func(n int, spec session.SessionSpec)
	result  func(n int) (*session.Result, error)
	change  func(map[string]any)
	nowhere func(*cmd.Nowhere)
}

// runLoop runs qory run from a run code whose documents set shared_run, on claude with a
// read access and 30 minutes, with a harness document that defines a stack of the module
// hello and its workflows hello and edit, read, behind the fake docker's wall. Each join succeeds,
// with the gateway's number from 1, unless s.result returns another result.
func runLoop(t *testing.T, s loopSetup) loopRun {
	t.Helper()
	var r loopRun
	var mu sync.Mutex
	var route http.Handler
	t.Cleanup(cmd.SetJoinResult(func(spec session.SessionSpec) (*session.Result, error) {
		mu.Lock()
		n := len(r.joins) + 1
		j := loopJoin{spec: spec, served: routeStatus(route, n), servedOld: routeStatus(route, n-1), report: homeReport(r.home)}
		if found, _ := filepath.Glob(filepath.Join(r.home, "*", "inputs.json")); len(found) == 1 {
			b, _ := os.ReadFile(found[0])
			j.inputs = string(b)
		}
		r.joins = append(r.joins, j)
		mu.Unlock()
		if spec.OnVariables != nil {
			spec.OnVariables(session.Applied{})
		}
		if s.joined != nil {
			s.joined(n, spec)
		}
		if s.result != nil {
			if res, err := s.result(n); res != nil || err != nil {
				return res, err
			}
		}
		return &session.Result{State: event.StateSucceeded, Session: n}, nil
	}))
	r.home = filepath.Join(tempDir(t), "home")
	defines := map[string]any{"hello": map[string]any{"access": "read"}, "edit": map[string]any{"access": "read"}}
	if s.extends {
		defines = nil
	}
	r.harnessRun = codeHarness{
		defines: defines,
		// Codex starts a workflow by its skill, and the module hello ships a command. The
		// skill workflow-edit is there for a workflow edit that a test adds.
		files: map[string]string{
			"examples/hello/modules/hello/skills/workflow-hello/SKILL.md": "---\nname: workflow-hello\ndescription: Say hello.\n---\n\nSay hello.\n",
			"examples/hello/modules/hello/skills/workflow-edit/SKILL.md":  "---\nname: workflow-edit\ndescription: Edit.\n---\n\nEdit.\n",
		},
		change: func(d map[string]any) {
			d["shared_run"] = true
			d["access"] = "read"
			d["time_limit_minutes"] = 30
			if s.change != nil {
				s.change(d)
			}
		},
		before: func() []string { return []string{"--home", r.home} },
		nowhere: func(n *cmd.Nowhere) {
			open := n.OnOpen
			n.OnOpen = func(o session.OpenSpec) {
				route = o.QoryRoute
				open(o)
			}
			n.NextSession = func(_ context.Context, body []byte) ([]byte, error) {
				mu.Lock()
				defer mu.Unlock()
				r.bodies = append(r.bodies, string(body))
				if i := len(r.bodies) - 1; i < len(s.answers) {
					return []byte(s.answers[i]), nil
				}
				return []byte(`{"next":"end"}`), nil
			}
			if s.nowhere != nil {
				s.nowhere(n)
			}
		},
	}.run(t)
	return r
}

// routeStatus is the status of the route's answer to a request of session n, 0 with no
// route or no session.
func routeStatus(route http.Handler, n int) int {
	if route == nil || n < 1 {
		return 0
	}
	req := httptest.NewRequest(http.MethodGet, "http://qory/", nil)
	req.Header.Set(link.QorySessionHeader, strconv.Itoa(n))
	w := httptest.NewRecorder()
	route.ServeHTTP(w, req)
	return w.Code
}

// TestARunOfSeveralSessionsJoinsOncePerSession is a run whose documents set shared_run,
// on claude, whose next-session answers start session 2 on codex with the workflow edit,
// the model key example-provider-2, 60 minutes and the inputs of attempt 2, and session 3
// on claude with example-provider-3, then end the run. qory joins three times, each join
// with its runtime, workflow, inputs, key and time limit; session 3 has no Codex home; the
// route serves only the session that runs; each request sends the session's number and how
// it ended; the harness is composed for each session's runtime from the run's cache, with
// no second fetch; the report lists each session's runtime; and the run closes succeeded,
// exit 0.
func TestARunOfSeveralSessionsJoinsOncePerSession(t *testing.T) {
	second := strings.Replace(laterAnswer(2, "codex", "example-provider-2", 60), `"workflow":"hello"`, `"workflow":"edit"`, 1)
	second = strings.Replace(second, `"inputs":{"version":1}`, `"inputs":{"run":{"attempt":2},"version":1}`, 1)
	r := runLoop(t, loopSetup{answers: []string{second, laterAnswer(3, "claude", "example-provider-3", 30)}})
	if r.err != nil || len(r.joins) != 3 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	for i, want := range []struct {
		runtime, key string
		minutes      int
	}{{"claude", "", 30}, {"codex", "example-provider-2", 60}, {"claude", "example-provider-3", 30}} {
		j := r.joins[i]
		program := j.spec.Command + " " + strings.Join(j.spec.Args, " ")
		if !strings.Contains(program, "/"+want.runtime+" ") && !strings.HasPrefix(program, want.runtime+" ") {
			t.Errorf("session %d starts %q, want %s", i+1, program, want.runtime)
		}
		if j.spec.ModelCredential != want.key {
			t.Errorf("session %d joins with the key %q, want %q", i+1, j.spec.ModelCredential, want.key)
		}
		if limit := j.spec.Timeout.Minutes(); limit <= float64(want.minutes-1) || limit > float64(want.minutes) {
			t.Errorf("session %d has %v, want %d minutes", i+1, j.spec.Timeout, want.minutes)
		}
		if j.served != http.StatusNotFound || i > 0 && j.servedOld != http.StatusServiceUnavailable {
			t.Errorf("session %d: the route answers the session %d and the session before %d", i+1, j.served, j.servedOld)
		}
	}
	for i, want := range []string{"hello", "edit", "hello"} {
		args := strings.Join(r.joins[i].spec.Args, " ")
		other := map[string]string{"hello": "edit", "edit": "hello"}[want]
		if !strings.Contains(args, want) || strings.Contains(args, other) {
			t.Errorf("session %d starts with the arguments %q, want the workflow %s", i+1, args, want)
		}
	}
	if !strings.Contains(r.joins[1].inputs, `"attempt": 2`) && !strings.Contains(r.joins[1].inputs, `"attempt":2`) || strings.Contains(r.joins[2].inputs, "attempt") {
		t.Errorf("the inputs of session 2 are %q, of session 3 %q", r.joins[1].inputs, r.joins[2].inputs)
	}
	for _, m := range r.joins[2].spec.Mounts {
		if m.Target == "/var/lib/qory-codex-home" {
			t.Errorf("session 3 on claude has the Codex home %s", m.Path)
		}
	}
	if strings.Contains(strings.Join(r.joins[2].spec.LaunchFixed, " "), "CODEX_HOME") {
		t.Errorf("session 3 on claude has %q", r.joins[2].spec.LaunchFixed)
	}
	for i, body := range r.bodies {
		if want := `{"session":` + strconv.Itoa(i+1) + `,"state":"succeeded","reason":null}`; body != want {
			t.Errorf("request %d is %s, want %s", i+1, body, want)
		}
	}
	if len(r.bodies) != 3 {
		t.Errorf("%d next-session requests, want 3", len(r.bodies))
	}
	if n := strings.Count(r.out, "qory run: fetched the module hello at "); n != 1 {
		t.Errorf("the run fetched the module hello %d times, want once:\n%s", n, r.out)
	}
	if n := strings.Count(strings.Join(r.calls, ","), "stack proxy,"); n != 1 {
		t.Errorf("the calls %q open the stack proxy %d times, want once", r.calls, n)
	}
	if n := strings.Count(r.out, "qory run: composed "); n != 3 {
		t.Errorf("the run composed %d times, want 3:\n%s", n, r.out)
	}
	wants(t, r.out, "qory run: session 2 starts the workflow edit on codex\n", "qory run: session 3 starts the workflow hello on claude\n", "Qory Apiary starts no session after session 3, so the run ends\n")
	rep := r.joins[2].report
	if len(rep.Sessions) != 3 || rep.Sessions[0].Runtime != "claude" || rep.Sessions[1].Runtime != "codex" || rep.Sessions[2].Runtime != "claude" || rep.Sessions[2].Session != 3 {
		t.Errorf("the report's sessions are %+v", rep.Sessions)
	}
	for i := 2; i <= 3; i++ {
		if _, err := os.Stat(filepath.Join(r.runDir, "harness-"+strconv.Itoa(i), "harness.yaml")); err != nil {
			t.Errorf("session %d's harness.yaml: %v", i, err)
		}
	}
	r.noAddress(t)
}

// bothNpm is an npm stub of Claude Code and of Codex at one address, as npm serves both:
// a request for Codex's package goes to codex, every other request to claude. It
// publishes claude 2.1.320 and codex 0.170.0 for x64 and arm64, with their tarballs at that
// address; with codexToo false it publishes no codex.
func bothNpm(t *testing.T, claude, codex *rt.Npm, codexToo bool) string {
	t.Helper()
	to := func(raw string) *httputil.ReverseProxy {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return httputil.NewSingleHostReverseProxy(u)
	}
	claudeNpm, codexNpm := to(claude.URL()), to(codex.URL())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "@openai") || strings.Contains(req.URL.EscapedPath(), "%40openai") {
			codexNpm.ServeHTTP(w, req)
			return
		}
		claudeNpm.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	for cpu, arch := range map[string]string{"x64": "amd64", "arm64": "arm64"} {
		b := rt.PlainTarball(t, "2.1.320 "+arch)
		claude.PublishWith(cpu, "2.1.320", b, rt.Integrity(b), srv.URL+"/@anthropic-ai/claude-code-linux-"+cpu+"/-/claude-code-linux-"+cpu+"-2.1.320.tgz")
		if codexToo {
			b = rt.CodexTarball(t, "0.170.0", arch)
			codex.PublishWith(cpu, "0.170.0", b, rt.Integrity(b), srv.URL+"/@openai/codex/-/codex-0.170.0-linux-"+cpu+".tgz")
		}
	}
	claude.SetLatest("2.1.320")
	if codexToo {
		codex.SetLatest("0.170.0")
	}
	return srv.URL
}

// TestARunOfClaudeCodexAndClaudeInstallsEachRuntimeOnce is a walled run of three sessions
// on claude, codex and claude, which installs its runtimes from npm through the run's host
// proxy: qory installs claude before session 1 and codex before session 2, once each, and
// session 3 starts the claude of session 1. Each session's harness is composed for its
// runtime, and the report lists each session's runtime.
func TestARunOfClaudeCodexAndClaudeInstallsEachRuntimeOnce(t *testing.T) {
	defer cmd.SetInstallRuntimes(true)()
	defer cmd.SetRuntimeKeys(rt.Keys())()
	claude, codex, proxy := rt.StartNpm(t), rt.StartCodexNpm(t), rt.StartProxy(t)
	defer cmd.SetNpmRegistry(bothNpm(t, claude, codex, true))()
	r := runLoop(t, loopSetup{
		answers: []string{laterAnswer(2, "codex", "example-provider-2", 30), laterAnswer(3, "claude", "example-provider-3", 30)},
		nowhere: func(n *cmd.Nowhere) {
			n.Proxy = proxy.URL()
			n.PushProxy = "http://qory:nowhere@127.0.0.1:9"
		},
	})
	if r.err != nil || len(r.joins) != 3 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	for line, want := range map[string]int{"qory run: installed claude 2.1.320\n": 1, "qory run: installed codex 0.170.0\n": 1} {
		if n := strings.Count(r.out, line); n != want {
			t.Errorf("the run prints %q %d times:\n%s", line, n, r.out)
		}
	}
	if claude.TarballGets() != 1 || codex.TarballGets() != 1 {
		t.Errorf("npm served %d tarballs of claude and %d of codex, want 1 each", claude.TarballGets(), codex.TarballGets())
	}
	state := filepath.Join(os.Getenv("XDG_STATE_HOME"), "qory", "runtimes")
	for i, want := range []string{
		filepath.Join(state, "claude", "2.1.320", "bin", "claude"),
		filepath.Join(state, "codex", "0.170.0", "bin", "codex"),
		filepath.Join(state, "claude", "2.1.320", "bin", "claude"),
	} {
		if got := startedProgram(r.joins[i].spec); got != want {
			t.Errorf("session %d starts %q, want %q", i+1, got, want)
		}
	}
	if s := r.joins[2].report.Sessions; len(s) != 3 || s[0].Runtime != "claude" || s[1].Runtime != "codex" || s[2].Runtime != "claude" {
		t.Errorf("the report's sessions are %+v", s)
	}
	lacks(t, r.out, rt.ProxyPassword)
}

// TestARunOfSeveralSessionsEndsAsItsAnswersAndSessionsSay is a run whose documents set
// shared_run, in each way it ends after session 1:
//   - an answer of end: one join, one request, the run closes succeeded, exit 0;
//   - a session that paused: one join, no request, exit 1 as for one session;
//   - a session the gateway numbered 2: one join, no request, the run closes failed,
//     exit 1, with the fix first;
//   - Qory Apiary's refusal not_found: one join, one request, failed, exit 1, with its
//     line;
//   - Qory Apiary's refusal run_ended: one request, the run closes succeeded with the
//     end line;
//   - an answer of a runtime qory does not run: failed, exit 1, no second join;
//   - documents of write access whose answer starts the workflow edit of write access
//     after hello of read access: failed, exit 1, no second join.
func TestARunOfSeveralSessionsEndsAsItsAnswersAndSessionsSay(t *testing.T) {
	refused := func(status int, code string) func(*cmd.Nowhere) {
		return func(n *cmd.Nowhere) {
			n.NextSession = func(context.Context, []byte) ([]byte, error) {
				return nil, &accesskey.Refusal{Status: status, Code: code, From: accesskey.FromApiary, Text: "Example text."}
			}
		}
	}
	for name, c := range map[string]struct {
		s        loopSetup
		joins    int
		requests int
		state    string
		exit     int
		line     string
	}{
		"end": {s: loopSetup{answers: []string{`{"next":"end"}`}}, joins: 1, requests: 1, state: event.StateSucceeded, line: "qory run: Qory Apiary starts no session after session 1, so the run ends\n"},
		"paused": {s: loopSetup{result: func(n int) (*session.Result, error) {
			return &session.Result{State: event.StatePaused, Session: n}, nil
		}}, joins: 1, state: event.StatePaused, exit: 1},
		"numbered otherwise": {s: loopSetup{result: func(int) (*session.Result, error) {
			return &session.Result{State: event.StateSucceeded, Session: 2}, nil
		}}, joins: 1, state: event.StateFailed, exit: 1, line: "qory run: update qory or Forager: the gateway numbered the session 2, and qory served session 1 on its route; the code is used\n"},
		"not_found": {s: loopSetup{nowhere: refused(http.StatusNotFound, "not_found")}, joins: 1, state: event.StateFailed, exit: 1,
			line: "qory run: Contact support: the next-session request after session 1 failed.\n  Qory Apiary returned 404: Example text.\n"},
		"run_ended": {s: loopSetup{nowhere: refused(http.StatusConflict, "run_ended")}, joins: 1, state: event.StateSucceeded,
			line: "qory run: Qory Apiary ended this run, so qory pushes nothing more and no later session starts.\n"},
		"another runtime": {s: loopSetup{answers: []string{laterAnswer(2, "example-runtime", "example-provider-2", 30)}}, joins: 1, requests: 1, state: event.StateFailed, exit: 1,
			line: "example-runtime"},
		"another access": {s: loopSetup{answers: []string{strings.Replace(laterAnswer(2, "claude", "example-provider-2", 30), `"hello"`, `"edit"`, 1)}, change: func(d map[string]any) {
			d["access"] = "write"
			d["harness"].(map[string]any)["harness"].(map[string]any)["workflows"].(map[string]any)["edit"] = map[string]any{"access": "write"}
		}}, joins: 1, requests: 1, state: event.StateFailed, exit: 1,
			line: "qory run: set one access for every workflow of the stage in Qory Apiary: the workflow edit has write, and session 1 has read; the code is used\n"},
	} {
		t.Run(name, func(t *testing.T) {
			r := runLoop(t, c.s)
			if len(r.joins) != c.joins {
				t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
			}
			if c.requests != 0 && len(r.bodies) != c.requests || c.s.nowhere == nil && c.requests == 0 && len(r.bodies) != 0 {
				t.Errorf("%d next-session requests, want %d", len(r.bodies), c.requests)
			}
			if len(r.closes) != 1 || r.closes[0].State != c.state {
				t.Errorf("the run closed with %+v, want %s", r.closes, c.state)
			}
			if got := cmd.ExitCode(r.err); got != c.exit {
				t.Errorf("exit %d (%v), want %d\n%s", got, r.err, c.exit, r.out)
			}
			if c.line != "" {
				wants(t, r.out, c.line)
			}
		})
	}
}

// TestEachCodexSessionOfARunGetsANewHome is a walled run of two Codex sessions: each joins
// with a Codex home in the wall, and the file that session 1 wrote in its home is gone when
// session 2 joins.
func TestEachCodexSessionOfARunGetsANewHome(t *testing.T) {
	var homes []string
	left := true
	r := runLoop(t, loopSetup{
		answers: []string{laterAnswer(2, "codex", "example-provider-2", 30)},
		change:  func(d map[string]any) { d["run_settings"] = map[string]any{"runtime": "codex"} },
		joined: func(n int, spec session.SessionSpec) {
			for _, m := range spec.Mounts {
				if m.Target == "/var/lib/qory-codex-home" {
					homes = append(homes, m.Path)
				}
			}
			if len(homes) != n {
				return
			}
			rollout := filepath.Join(homes[0], "sessions", "rollout.jsonl")
			if n == 1 {
				writeFile(t, rollout, "{}\n")
				return
			}
			_, err := os.Stat(rollout)
			left = err == nil
		},
	})
	if r.err != nil || len(r.joins) != 2 || len(homes) != 2 {
		t.Fatalf("%d joins with the Codex homes %q, %v\n%s", len(r.joins), homes, r.err, r.out)
	}
	if left {
		t.Error("session 2 joined with the file of session 1 in its Codex home")
	}
	if info, err := os.Stat(homes[1]); err == nil && info.IsDir() {
		t.Errorf("the Codex home %s is left after the run", homes[1])
	}
}

// TestACodexInstallThatFailsBeforeSession2EndsTheRun is a walled run of claude whose answer
// starts session 2 on codex, which npm does not have: claude is installed, session 1 joins,
// and the install of codex fails, so the run closes failed, exit 1, with the install's
// cause, as a run from a run code shows a failed install of session 1, and session 2 does
// not join.
func TestACodexInstallThatFailsBeforeSession2EndsTheRun(t *testing.T) {
	defer cmd.SetInstallRuntimes(true)()
	defer cmd.SetRuntimeKeys(rt.Keys())()
	claude, codex, proxy := rt.StartNpm(t), rt.StartCodexNpm(t), rt.StartProxy(t)
	defer cmd.SetNpmRegistry(bothNpm(t, claude, codex, false))()
	r := runLoop(t, loopSetup{
		answers: []string{laterAnswer(2, "codex", "example-provider-2", 30)},
		nowhere: func(n *cmd.Nowhere) {
			n.Proxy = proxy.URL()
			n.PushProxy = "http://qory:nowhere@127.0.0.1:9"
		},
	})
	if len(r.joins) != 1 || len(r.bodies) != 1 {
		t.Fatalf("%d joins and %d requests, %v\n%s", len(r.joins), len(r.bodies), r.err, r.out)
	}
	if len(r.closes) != 1 || r.closes[0].State != event.StateFailed || cmd.ExitCode(r.err) != 1 {
		t.Errorf("the run closed with %+v, exit %d (%v)\n%s", r.closes, cmd.ExitCode(r.err), r.err, r.out)
	}
	wants(t, r.out, "qory run: installed claude 2.1.320\n", "qory run: session 2 starts the workflow hello on codex\n",
		"qory run: the dist-tags of @openai/codex returned 404: npm has no such codex release; the code is used\n")
	lacks(t, r.out, "installed codex")
	lacks(t, r.out, rt.ProxyPassword)
}

// TestASignalWhileTheNextSessionRequestWaitsStopsTheRun is a run whose first next-session
// request gets no response, and which gets SIGTERM during the wait before the second send:
// the run closes cancelled with the reason interrupted, exit 1, with the line of a run
// cancelled before its agent started, and no second session joins.
func TestASignalWhileTheNextSessionRequestWaitsStopsTheRun(t *testing.T) {
	r := runLoop(t, loopSetup{nowhere: func(n *cmd.Nowhere) {
		n.NextSession = func(context.Context, []byte) ([]byte, error) {
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
			return nil, errors.New("dial tcp: connection refused")
		}
	}})
	if len(r.joins) != 1 || cmd.ExitCode(r.err) != 1 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	if len(r.closes) != 1 || r.closes[0].State != event.StateCancelled || r.closes[0].Reason != event.ReasonInterrupted {
		t.Errorf("the run closed with %+v, want cancelled with interrupted", r.closes)
	}
	wants(t, r.out, "the run was cancelled: qory run got SIGTERM before claude started")
	lacks(t, r.out, "Check the gateway's connection")
}

// TestALaterSessionComposesTheBaseOfTheHarnessFromTheRunsCache is a run whose harness
// document extends a stack, with session 2 on codex: the base is fetched once, for session
// 1, and session 2's compose for codex reads it from the run's cache.
func TestALaterSessionComposesTheBaseOfTheHarnessFromTheRunsCache(t *testing.T) {
	r := runLoop(t, loopSetup{extends: true, answers: []string{laterAnswer(2, "codex", "example-provider-2", 30)}})
	if r.err != nil || len(r.joins) != 2 {
		t.Fatalf("%d joins, %v (exit %d)\n%s", len(r.joins), r.err, cmd.ExitCode(r.err), r.out)
	}
	if n := strings.Count(r.out, "qory run: fetched "+workspaceBase+" at "); n != 1 {
		t.Errorf("the run fetched the base %d times, want once:\n%s", n, r.out)
	}
	wants(t, r.out, "for codex\n")
	if s := r.joins[1].report.Sessions; len(s) != 2 || s[1].Runtime != "codex" {
		t.Errorf("the report's sessions are %+v", s)
	}
}
