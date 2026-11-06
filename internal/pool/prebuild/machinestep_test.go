package prebuild_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/proxygit"
)

// machineSteps is a file of the machine's start with two entries.
const machineSteps = `{"name":"boot","started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n" +
	`{"name":"start Docker","started":"2026-11-06T00:31:58.904Z","ended":"2026-11-06T00:32:02.104Z"}` + "\n"

// machineFile writes content to a new file, steps.jsonl, and returns its path.
func machineFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "steps.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLaunchWritesTheMachineStepBeforeTheFetch is a prebuild with MachineSteps, through a
// gateway in this process: the run's step 1 is the machine step, start machine, with one
// run.log per entry, which exits succeeded with -1, and the run's record has it when
// the fetch starts. The prebuild succeeds and logs no line of the step.
func TestLaunchWritesTheMachineStepBeforeTheFetch(t *testing.T) {
	e := newEnv(t)
	evs, opened := throughGateway(t, e)
	e.settings.MachineSteps = machineFile(t, machineSteps)
	var atFetch string
	fetch := e.settings.Fetch
	e.settings.Fetch = func(ctx context.Context, dir string, base []string, getenv func(string) string, p proxygit.Proxy, out io.Writer) (bool, error) {
		b, _ := os.ReadFile(filepath.Join(opened().Dir(), "session.jsonl"))
		atFetch = string(b)
		return fetch(ctx, dir, base, getenv, p, out)
	}
	if _, err := e.launch(t, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(atFetch, `"kind":"machine"`) {
		t.Errorf("the run's record had no machine step when the fetch started:\n%s", atFetch)
	}
	started := evs.of(t, "dev.qory.run.step_started")
	if len(started) == 0 || started[0]["step"] != float64(1) || started[0]["kind"] != "machine" || started[0]["name"] != "start machine" {
		t.Fatalf("the steps started: %v", started)
	}
	var lines []string
	for _, l := range evs.of(t, "dev.qory.run.log") {
		if l["step"] == float64(1) {
			lines = append(lines, l["bytes"].(string))
		}
	}
	// The bytes of a run.log are base64: "boot  14.8 s\n" and "start Docker  3.2 s\n".
	if want := []string{"Ym9vdCAgMTQuOCBzCg==", "c3RhcnQgRG9ja2VyICAzLjIgcwo="}; !slices.Equal(lines, want) {
		t.Errorf("the machine step's lines %q, want %q", lines, want)
	}
	exited := evs.of(t, "dev.qory.run.step_exited")
	if len(exited) == 0 || exited[0]["step"] != float64(1) || exited[0]["state"] != "succeeded" || exited[0]["exit_code"] != float64(-1) {
		t.Errorf("the steps exited: %v", exited)
	}
	for _, l := range e.lines() {
		if strings.Contains(l, "machine step") {
			t.Errorf("a line of the machine step: %q", l)
		}
	}
}

// TestLaunchWithABadMachineStepsFileGoesOn is a prebuild whose MachineSteps file has a
// second line that is not JSON: the prebuild logs one line, with the run id, the path
// and the line, fetches and succeeds.
func TestLaunchWithABadMachineStepsFileGoesOn(t *testing.T) {
	e := newEnv(t)
	e.settings.MachineSteps = machineFile(t, strings.SplitAfter(machineSteps, "\n")[0]+"start Docker  3.2 s\n")
	if _, err := e.launch(t, false); err != nil {
		t.Fatal(err)
	}
	want := testRunID + ": the run's record has no machine step: line 2 of " + e.settings.MachineSteps + " is not a JSON object with the strings name, started and ended"
	var got []string
	for _, l := range e.lines() {
		if strings.Contains(l, "machine step") {
			got = append(got, l)
		}
	}
	if !slices.Equal(got, []string{want}) {
		t.Errorf("the lines of the machine step %q, want %q", got, want)
	}
	if len(e.fetched) != 1 {
		t.Errorf("the prebuild fetched %d times, want once", len(e.fetched))
	}
}
