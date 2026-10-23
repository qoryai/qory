package cmd_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// runSteps is [hostFetch.runWith] that also returns the events of the run's host
// steps, in the order qory run wrote them.
func (h *hostFetch) runSteps(t *testing.T, repositories string, proxyErr error) (string, error, keyRun, []cmd.StepEvent) {
	t.Helper()
	gw := newKeyedGateway(t)
	key := mintRunKey(t, keyRunID, gw.url())
	t.Setenv("QORY_RUN_KEY", "")
	var mu sync.Mutex
	var steps []cmd.StepEvent
	closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: repositories,
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		ProxyErr: proxyErr,
		OnStep: func(e cmd.StepEvent) {
			mu.Lock()
			defer mu.Unlock()
			steps = append(steps, e)
		},
	})
	defer restore()
	out, err := run(t, "run", "claude", "--run-key-fd", descriptor(t, key))
	mu.Lock()
	defer mu.Unlock()
	return out, err, keyRun{closes: closes(), joined: errors.Is(err, cmd.ErrJoinedNowhere)}, steps
}

// shownSteps is each step event as one line, the times of the fetch lines as X:
// "1 started fetch: fetch git.test/acme/shop", "1 log: qory run: fetched ...", "1 exited
// succeeded 0".
func shownSteps(steps []cmd.StepEvent) []string {
	var lines []string
	for _, e := range steps {
		switch e.Type {
		case "step_started":
			lines = append(lines, fmt.Sprintf("%d started %s: %s", e.Step, e.Kind, e.Name))
		case "log":
			lines = append(lines, fmt.Sprintf("%d log: %s", e.Step, fetchTimes.ReplaceAllString(e.Line, " in X\n")))
		case "step_exited":
			lines = append(lines, fmt.Sprintf("%d exited %s %d", e.Step, e.State, e.ExitCode))
		}
	}
	return lines
}

// equalLines fails the test when got is not want, showing both.
func equalLines(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the steps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// lineOf is the line of out that starts with prefix, with its line break, or "".
func lineOf(out, prefix string) string {
	for _, l := range strings.SplitAfter(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// TestARunWritesItsFetchAndComposeSteps is a run given a run key whose run
// configuration lists acme/shop, with the fake runtime: qory run writes step 1, the
// fetch of git.test/acme/shop, with its fetch line, then step 2, the compose for claude,
// with the line of the module's git source and the composed line, each succeeded with 0
// and each line on stderr as before.
func TestARunWritesItsFetchAndComposeSteps(t *testing.T) {
	h := newHostFetch(t, "", "main")
	out, err, n, steps := h.runSteps(t, "acme/shop", nil)
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	shop := proxygittest.Git(t, filepath.Join(h.server.Root, "acme", "shop.git"), "rev-parse", "HEAD")
	harness := proxygittest.Git(t, filepath.Join(h.server.Root, "acme", "harness.git"), "rev-parse", "HEAD")
	equalLines(t, shownSteps(steps), []string{
		"1 started fetch: fetch git.test/acme/shop",
		"1 log: qory run: fetched acme/shop at " + shop[:12] + " in X\n",
		"1 exited succeeded 0",
		"2 started compose: compose the harness for claude",
		"2 log: qory run: fetched " + h.server.URL("acme/harness") + "#main at " + harness[:12] + " in X\n",
		"2 log: qory run: composed 0 entries from 1 module for claude\n",
		"2 exited succeeded 0",
	})
	wants(t, fetchTimes.ReplaceAllString(out, " in X\n"), "qory run: fetched acme/shop at "+shop[:12]+" in X\n", "qory run: composed 0 entries from 1 module for claude\n")
	h.noPassword(t, out)
}

// TestAFailedFetchStepHasTheErrorLine is a run given a run key whose repository the
// server lacks, or whose host proxy does not open: the fetch step ends failed with the
// status qory run exits with, its last line is the error line qory run prints on
// stderr, and no compose step starts.
func TestAFailedFetchStepHasTheErrorLine(t *testing.T) {
	for _, c := range []struct {
		name     string
		proxyErr error
		want     string
	}{
		{"repository", nil, "qory run: add a credential for git.test that reads acme/missing to the run configuration, then run again."},
		{"host proxy", errors.New("the run's session is joining"), "qory run: run again: the run's host proxy, through which qory fetches the run's code, did not open: the run's session is joining\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHostFetch(t, "", "main")
			out, err, n, steps := h.runSteps(t, "acme/missing", c.proxyErr)
			if cmd.ExitCode(err) != 1 || !n.stoppedBeforeJoin() {
				t.Fatalf("%v (exit %d), want exit 1 before the join\n%s", err, cmd.ExitCode(err), out)
			}
			line := lineOf(out, c.want)
			if line == "" {
				t.Fatalf("no line %q on stderr:\n%s", c.want, out)
			}
			equalLines(t, shownSteps(steps), []string{
				"1 started fetch: fetch git.test/acme/missing",
				"1 log: " + line,
				"1 exited failed 1",
			})
		})
	}
}

// TestAFailedComposeStepHasTheErrorLine is a run given a run key whose module's git
// source lacks the ref the stack lists: the fetch step succeeds, and the compose step
// ends failed with 2, the status qory run exits with, after the error line qory run
// prints on stderr.
func TestAFailedComposeStepHasTheErrorLine(t *testing.T) {
	h := newHostFetch(t, "", "gone")
	out, err, n, steps := h.runSteps(t, "acme/shop", nil)
	if cmd.ExitCode(err) != 2 || !n.stoppedBeforeJoin() {
		t.Fatalf("%v (exit %d), want exit 2 before the join\n%s", err, cmd.ExitCode(err), out)
	}
	line := "qory run: check that " + h.server.URL("acme/harness") + " has the branch, tag or commit gone, that the run's policy allows git.test and, for a private repository, that the run configuration has a credential that reads it. The fetch failed: " + h.server.URL("acme/harness") + " has no branch or tag gone\n"
	wants(t, out, "\n"+line)
	got := shownSteps(steps)
	if len(got) < 5 || got[0] != "1 started fetch: fetch git.test/acme/shop" || got[2] != "1 exited succeeded 0" || got[3] != "2 started compose: compose the harness for claude" {
		t.Fatalf("the steps:\n%s\nwant the fetch, then the compose", strings.Join(got, "\n"))
	}
	if tail := got[len(got)-2:]; tail[0] != "2 log: "+line || tail[1] != "2 exited failed 2" {
		t.Errorf("the compose step ends:\n%s\nwant the line %q, then failed 2", strings.Join(tail, "\n"), line)
	}
}

// recordedSteps is each step event and each host line of the one run recorded under
// root, in the record's order, as [shownSteps] shows them, and a session's step_started
// as "<n> started <kind>: <name>" too.
func recordedSteps(t *testing.T, root string) []string {
	t.Helper()
	dir, _ := events(t, root)
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev struct {
			Type string `json:"type"`
			Data struct {
				Step     int    `json:"step"`
				Name     string `json:"name"`
				Kind     string `json:"kind"`
				State    string `json:"state"`
				ExitCode int    `json:"exit_code"`
				Bytes    []byte `json:"bytes"`
				Session  int    `json:"session"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		d := ev.Data
		switch ev.Type {
		case "dev.qory.run.step_started":
			lines = append(lines, fmt.Sprintf("%d started %s: %s", d.Step, d.Kind, d.Name))
		case "dev.qory.run.step_exited":
			lines = append(lines, fmt.Sprintf("%d exited %s %d", d.Step, d.State, d.ExitCode))
		case "dev.qory.run.log":
			if d.Session == 0 {
				lines = append(lines, fmt.Sprintf("%d log: %s", d.Step, d.Bytes))
			}
		}
	}
	return lines
}

// TestARunRecordsItsComposeStepBeforeTheJoinStep is a person's run through Forager with
// the fake runtime: the record has step 1, the compose, with the composed line, then
// step 2, Forager's join step of session 1.
func TestARunRecordsItsComposeStepBeforeTheJoinStep(t *testing.T) {
	root := runKeyCheckout(t)
	t.Setenv("QORY_RUN_KEY", "")
	out, err := run(t, "run", "claude")
	if cmd.ExitCode(err) != 0 {
		t.Fatalf("%v (exit %d)\n%s", err, cmd.ExitCode(err), out)
	}
	equalLines(t, recordedSteps(t, root), []string{
		"1 started compose: compose the harness for claude",
		"1 log: qory run: composed 8 entries from 2 modules for claude\n",
		"1 exited succeeded 0",
		"2 started join: join session 1",
		"2 exited succeeded -1",
	})
}

// TestARunRecordsItsFailedComposeStep is a person's run through Forager whose stack
// lists a module that is not there: the record has step 1, the compose, failed with 2,
// the status qory run exits with, after the error line qory run prints, and no join
// step.
func TestARunRecordsItsFailedComposeStep(t *testing.T) {
	root := runKeyCheckout(t)
	t.Setenv("QORY_RUN_KEY", "")
	if err := os.RemoveAll(filepath.Join(root, "modules", "nextjs")); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "run")
	if cmd.ExitCode(err) != 2 {
		t.Fatalf("%v (exit %d), want 2\n%s", err, cmd.ExitCode(err), out)
	}
	got := recordedSteps(t, root)
	if len(got) != 3 || got[0] != "1 started compose: compose the harness" || got[2] != "1 exited failed 2" {
		t.Fatalf("the steps:\n%s\nwant step 1, the compose, failed 2", strings.Join(got, "\n"))
	}
	text := strings.TrimPrefix(got[1], "1 log: qory run: ")
	first, _, _ := strings.Cut(text, "\n")
	if text == got[1] || !strings.Contains(out, first) {
		t.Errorf("the compose step's line %q is not the error qory run printed:\n%s", got[1], out)
	}
}
