package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/secretmask"
)

// machineSecret is a value of the run's mask list that a name of the machine's start
// contains.
const machineSecret = "machine-secret-value-7d3f"

// TestForagerRecordsTheMachineStep is the machine step of a run that Forager opens at a
// gateway in this process, with machineSecret in the run's mask list and in the second
// name: the gateway's record has step 1 of kind machine, start machine, with one run.log
// per entry, the secret replaced with ***, which exits succeeded with -1. No event
// contains the secret.
func TestForagerRecordsTheMachineStep(t *testing.T) {
	ctx := context.Background()
	runs := t.TempDir()
	id := event.NewRunID()
	list := secretmask.New()
	list.Add(machineSecret)
	at, err := openAtGateway(ctx, session.OpenSpec{RunID: id, RunsDir: runs, Report: func(string) {}, Masker: list.Masker()}, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "steps.jsonl")
	content := `{"name":"boot","started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n" +
		`{"name":"start Docker ` + machineSecret + `","started":"2026-11-06T00:31:58.904Z","ended":"2026-11-06T00:32:02.104Z"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var said []string
	writeMachineStep(foragerRun{at.run}, path, func(l string) { said = append(said, l) })
	if _, err := at.run.Close(ctx, session.Outcome{State: event.StateSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := at.g.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if said != nil {
		t.Errorf("said %q", said)
	}
	data, err := os.ReadFile(filepath.Join(runs, id, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
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
				Stream   string `json:"stream"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		d := ev.Data
		switch ev.Type {
		case event.StepStarted:
			got = append(got, fmt.Sprintf("%d started %s: %s", d.Step, d.Kind, d.Name))
		case event.StepExited:
			got = append(got, fmt.Sprintf("%d exited %s %d", d.Step, d.State, d.ExitCode))
		case event.RunLog:
			got = append(got, fmt.Sprintf("%d log %s: %s", d.Step, d.Stream, d.Bytes))
			if strings.Contains(string(d.Bytes), machineSecret) {
				t.Errorf("a line contains the secret: %q", d.Bytes)
			}
		}
	}
	want := []string{"1 started machine: start machine", "1 log stderr: boot  14.8 s\n", "1 log stderr: start Docker ***  3.2 s\n", "1 exited succeeded -1"}
	if !slices.Equal(got, want) {
		t.Errorf("the record:\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(string(data), machineSecret) {
		t.Errorf("events.jsonl contains the secret")
	}
}
