package cmd_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/cmd"
	"github.com/qoryai/qory/internal/proxygit/proxygittest"
)

// machineSteps is the file of --machine-steps-file as the machine's start script writes
// it, with two entries, the second's name with an escape sequence and 120 characters.
var machineSteps = `{"name":"boot","started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n" +
	`{"name":"start Docker\u001b[2K` + strings.Repeat("x", 108) + `","started":"2026-11-06T00:31:58.904Z","ended":"2026-11-06T00:32:02.104Z"}` + "\n"

// machineRun runs qory run from a run code whose documents set the target acme/site on
// git.test at its first commit, as [runAtVersion] does, with --machine-steps-file
// path, and returns the output, the error, what the run came to and the events of its
// host steps. stepErr, when not nil, is the run's [cmd.Nowhere.StepErr].
func machineRun(t *testing.T, path string, stepErr func(string) error) (string, error, keyRun, []cmd.StepEvent) {
	t.Helper()
	h, first, _, _ := versionRepository(t)
	g := newCodeRelay(t)
	g.documents = codeDocuments(t, func(d map[string]any) {
		target := map[string]any{"system": proxygittest.Host, "path": "acme/site"}
		d["target"], d["inputs"].(map[string]any)["target"] = target, target
		d["target_version"] = first
		d["harness_hidden"] = false
		d["run_settings"] = map[string]any{"runtime": "claude"}
	})
	t.Cleanup(cmd.SetJoinSession(func(session.SessionSpec) {}))
	t.Setenv("QORY_RUN_KEY", "")
	var steps []cmd.StepEvent
	closes, _, restore := cmd.OpenNowhereWith(cmd.Nowhere{
		System: proxygittest.Host, Repositories: "acme/site",
		Proxy: h.server.Proxy.URL, CABundle: h.server.Proxy.CABundle, Dir: h.runDir,
		OnStep:  func(e cmd.StepEvent) { steps = append(steps, e) },
		StepErr: stepErr,
	})
	defer restore()
	out, err := run(t, "run", "--run-code-file", codeFile(t, g.code()), "--gateway", g.URL, "--machine-steps-file", path)
	return out, err, keyRun{closes: closes(), joined: errors.Is(err, cmd.ErrJoinedNowhere)}, steps
}

// TestMachineStepsFileNeedsARunCode is --machine-steps-file without --run-code-file:
// qory run exits 2 with the fix, before it starts anything, and leaves no record.
func TestMachineStepsFileNeedsARunCode(t *testing.T) {
	root := runKeyCheckout(t)
	t.Setenv("QORY_RUN_KEY", "")
	file := filepath.Join(t.TempDir(), "steps.jsonl")
	writeFile(t, file, machineSteps)
	for _, args := range [][]string{
		{"run", "--machine-steps-file", file},
		{"run", "--local", "--machine-steps-file", file},
		{"run", "claude", "--machine-steps-file", file, "--", "-p", "hi"},
	} {
		_, err := run(t, args...)
		if want := "remove --machine-steps-file: it applies only with --run-code-file"; err == nil || err.Error() != want || cmd.ExitCode(err) != cmd.ExitInput {
			t.Errorf("%q: %v (exit %d), want %q", args, err, cmd.ExitCode(err), want)
		}
	}
	if ids := recorded(t, root); len(ids) != 0 {
		t.Errorf("the runs that exited 2 left records %v", ids)
	}
}

// TestMachineStepsFileIsHidden pins --machine-steps-file as a flag of qory run that its
// help does not show.
func TestMachineStepsFileIsHidden(t *testing.T) {
	emptyDir(t)
	c, _, err := cmd.Root().Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	if f := c.Flags().Lookup("machine-steps-file"); f == nil || !f.Hidden {
		t.Fatalf("the flag is %+v", f)
	}
	out, err := run(t, "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, "machine-steps")
}

// TestARunCodeWritesTheMachineStepBeforeTheFetch is a run from a run code with
// --machine-steps-file: step 1 is the machine step, start machine, with one line per
// entry, the escape sequence removed and the second name cut to 100 characters, and it
// exits succeeded with -1; the fetch is step 2. qory run prints no line of it.
func TestARunCodeWritesTheMachineStepBeforeTheFetch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "steps.jsonl")
	writeFile(t, file, machineSteps)
	out, err, n, steps := machineRun(t, file, nil)
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	got := shownSteps(steps)
	want := []string{
		"1 started machine: start machine",
		"1 log: boot  14.8 s\n",
		"1 log: start Docker[2K" + strings.Repeat("x", 85) + "  3.2 s\n",
		"1 exited succeeded -1",
		"2 started fetch: fetch " + proxygittest.Host + "/acme/site",
	}
	if len(got) < len(want) {
		t.Fatalf("the steps:\n%s", strings.Join(got, "\n"))
	}
	equalLines(t, got[:len(want)], want)
	lacks(t, out, "machine step", "start Docker")
}

// TestARunCodeWithABadMachineStepsFileGoesOn is a run from a run code whose file of
// --machine-steps-file has a second line that is not JSON: the run writes no machine
// step, the fetch is step 1, qory run prints one line with the file and the line, and
// the run joins.
func TestARunCodeWithABadMachineStepsFileGoesOn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "steps.jsonl")
	writeFile(t, file, machineSteps+"start Docker  3.2 s\n")
	out, err, n, steps := machineRun(t, file, nil)
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	if len(steps) == 0 || steps[0].Kind != event.StepFetch {
		t.Errorf("the steps:\n%s\nwant the fetch first", strings.Join(shownSteps(steps), "\n"))
	}
	line := "qory run: the run's record has no machine step: line 3 of " + file + " is not a JSON object with the strings name, started and ended\n"
	if c := strings.Count(out, "machine step"); c != 1 || !strings.Contains(out, line) {
		t.Errorf("want one line %q, got %d lines of the machine step:\n%s", line, c, out)
	}
}

// TestARunCodeWhoseMachineStepForagerDoesNotStartGoesOn is a run from a run code whose
// machine step Forager does not start: qory run prints one line with Forager's error,
// the fetch is step 1, and the run joins.
func TestARunCodeWhoseMachineStepForagerDoesNotStartGoesOn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "steps.jsonl")
	writeFile(t, file, machineSteps)
	out, err, n, steps := machineRun(t, file, func(kind string) error {
		if kind == event.StepMachine {
			return errors.New("start a host step of kind fetch, compose, build or push, not \"machine\"")
		}
		return nil
	})
	if !n.joined {
		t.Fatalf("%v (exit %d), want the join\n%s", err, cmd.ExitCode(err), out)
	}
	if len(steps) == 0 || steps[0].Kind != event.StepFetch || steps[0].Step != 1 {
		t.Errorf("the steps:\n%s\nwant the fetch first", strings.Join(shownSteps(steps), "\n"))
	}
	line := "qory run: the run's record has no machine step: writing the step failed: start a host step of kind fetch, compose, build or push, not \"machine\"\n"
	if c := strings.Count(out, "machine step"); c != 1 || !strings.Contains(out, line) {
		t.Errorf("want one line %q, got %d lines of the machine step:\n%s", line, c, out)
	}
}
