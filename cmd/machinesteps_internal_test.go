package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/forager/event"
	"github.com/qoryai/forager/session"
)

// machineFile writes content to a new file, steps.jsonl, mode 0644, and returns its path.
func machineFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "steps.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// machineEntry is one line of the file of --machine-steps-file, with its line break.
func machineEntry(name, started, ended string) string {
	b, err := json.Marshal(map[string]string{"name": name, "started": started, "ended": ended})
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// TestMachineStepLinesShowEachEntry is a file of four entries, written as the machine's
// start script writes them and with spaces, a further member and an offset: each line
// is the name, two spaces and the seconds from started to ended to one decimal. A file
// with no line break after its last entry reads the same.
func TestMachineStepLinesShowEachEntry(t *testing.T) {
	content := `{"name":"boot","started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n" +
		`{"name":"start Docker","started":"2026-11-06T00:31:58.904Z","ended":"2026-11-06T00:32:02.104Z"}` + "\n" +
		`{ "name" : "firewall" , "started" : "2026-11-06T00:32:02.104Z" , "ended" : "2026-11-06T00:32:02.104Z", "kind": "x" }` + "\n" +
		`{"name":"pull image","started":"2026-11-06T01:32:02.000+01:00","ended":"2026-11-06T00:33:03.050Z"}` + "\n"
	want := []string{"boot  14.8 s\n", "start Docker  3.2 s\n", "firewall  0.0 s\n", "pull image  61.0 s\n"}
	for _, c := range []string{content, strings.TrimSuffix(content, "\n")} {
		got, err := machineStepLines(machineFile(t, c))
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q, %v, want %q", got, err, want)
		}
	}
}

// TestMachineStepLinesCleanTheName is a name with control characters of category Cc,
// C0, DEL and C1, and with U+2028 and U+2029, which are removed, and a name of 150
// characters, two bytes each, which is cut to 100 characters.
func TestMachineStepLinesCleanTheName(t *testing.T) {
	long := strings.Repeat("é", 150)
	content := machineEntry("start\x1b[31m Docker\r\n\t\x7f\u0085  ", "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z") +
		machineEntry(long, "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z")
	got, err := machineStepLines(machineFile(t, content))
	want := []string{"start[31m Docker  3.2 s\n", strings.Repeat("é", 100) + "  3.2 s\n"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("%q, %v, want %q", got, err, want)
	}
}

// TestMachineStepLinesKeepSixtyFourEntries is a file of 70 entries: the lines are the
// first 64, and nothing marks the rest.
func TestMachineStepLinesKeepSixtyFourEntries(t *testing.T) {
	var b strings.Builder
	for i := range 70 {
		b.WriteString(machineEntry(fmt.Sprintf("step %d", i+1), "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z"))
	}
	got, err := machineStepLines(machineFile(t, b.String()))
	if err != nil || len(got) != 64 || got[0] != "step 1  3.2 s\n" || got[63] != "step 64  3.2 s\n" {
		t.Errorf("%d lines, %v: %q", len(got), err, got)
	}
}

// TestMachineStepLinesReadSixtyFourKiB is a file of 64 KiB exactly, which is read, and
// an empty file, which has no lines.
func TestMachineStepLinesReadSixtyFourKiB(t *testing.T) {
	entry := machineEntry("", "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z")
	name := strings.Repeat("a", 64<<10-len(entry))
	got, err := machineStepLines(machineFile(t, machineEntry(name, "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z")))
	if err != nil || len(got) != 1 || got[0] != strings.Repeat("a", 100)+"  3.2 s\n" {
		t.Errorf("a file of 64 KiB: %q, %v", got, err)
	}
	if got, err := machineStepLines(machineFile(t, "")); err != nil || got != nil {
		t.Errorf("an empty file: %q, %v", got, err)
	}
}

// TestMachineStepLinesOfABadFile is each file that qory reads no line of, with the text
// of its error: missing, not a regular file, larger than 64 KiB, a line that is not a
// JSON object with the three strings, a time that does not parse, and an entry that
// ended before it started. A bad line after good ones, past the 64th entry too, counts
// as well.
func TestMachineStepLinesOfABadFile(t *testing.T) {
	good := machineEntry("boot", "2026-11-06T00:31:44.104Z", "2026-11-06T00:31:58.904Z")
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.jsonl")
	if _, err := machineStepLines(missing); err == nil || err.Error() != missing+" cannot be read: no such file or directory" {
		t.Errorf("a missing file: %v", err)
	}
	if _, err := machineStepLines(dir); err == nil || err.Error() != dir+" is not a regular file" {
		t.Errorf("a folder: %v", err)
	}
	if os.Getuid() != 0 {
		locked := machineFile(t, good)
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := machineStepLines(locked); err == nil || err.Error() != locked+" cannot be read: permission denied" {
			t.Errorf("a file qory may not read: %v", err)
		}
	}
	for _, c := range []struct {
		name, content, want string
	}{
		{"larger than 64 KiB", strings.Repeat(" ", 64<<10+1), "%s is larger than 64 KiB"},
		{"not JSON", good + "start Docker 3.2 s\n", "line 2 of %s is not a JSON object with the strings name, started and ended"},
		{"an array", "[]\n", "line 1 of %s is not a JSON object with the strings name, started and ended"},
		{"null", "null\n", "line 1 of %s is not a JSON object with the strings name, started and ended"},
		{"two objects on a line", strings.TrimSuffix(good, "\n") + good, "line 1 of %s is not a JSON object with the strings name, started and ended"},
		{"an empty line", good + "\n" + good, "line 2 of %s is not a JSON object with the strings name, started and ended"},
		{"no name", `{"started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n", "line 1 of %s is not a JSON object with the strings name, started and ended"},
		{"a name that is null", `{"name":null,"started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n", "line 1 of %s is not a JSON object with the strings name, started and ended"},
		{"a name in capitals", `{"NAME":"boot","started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n", "line 1 of %s is not a JSON object with the strings name, started and ended"},
		{"a number as ended", `{"name":"boot","started":"2026-11-06T00:31:44.104Z","ended":1762389118}` + "\n", "line 1 of %s is not a JSON object with the strings name, started and ended"},
		{"a started that does not parse", good + machineEntry("boot", "2026-11-06 00:31:44", "2026-11-06T00:31:58.904Z"), "line 2 of %s: started is not an RFC 3339 time"},
		{"an ended that does not parse", machineEntry("boot", "2026-11-06T00:31:44.104Z", "later"), "line 1 of %s: ended is not an RFC 3339 time"},
		{"ended before started", good + good + machineEntry("boot", "2026-11-06T00:31:58.904Z", "2026-11-06T00:31:58.903Z"), "line 3 of %s: ended is before started"},
		{"a bad line past the 64th", strings.Repeat(good, 70) + "{}\n", "line 71 of %s is not a JSON object with the strings name, started and ended"},
	} {
		path := machineFile(t, c.content)
		got, err := machineStepLines(path)
		if want := fmt.Sprintf(c.want, path); err == nil || err.Error() != want || got != nil {
			t.Errorf("%s: %q, %v, want %q", c.name, got, err, want)
		}
	}
}

// stepRun is an [openedRun] whose StartStep returns its step, or err, and records the
// kind and name it got; every other call panics.
type stepRun struct {
	openedRun
	err        error
	step       *recordedStep
	kind, name string
}

func (r *stepRun) StartStep(kind, name string) (hostStep, error) {
	r.kind, r.name = kind, name
	if r.err != nil {
		return nil, r.err
	}
	return r.step, nil
}

// recordedStep is a [hostStep] that records its lines and its exit, and whose Log
// returns logErr.
type recordedStep struct {
	lines  []string
	exit   string
	logErr error
}

func (s *recordedStep) Log(b []byte) error {
	if s.logErr != nil {
		return s.logErr
	}
	s.lines = append(s.lines, string(b))
	return nil
}

func (s *recordedStep) Exit(state string, exitCode int) error {
	s.exit = fmt.Sprintf("%s %d", state, exitCode)
	return nil
}

// TestWriteMachineStepWritesOneStep is the machine step of a file of two entries: one
// step of kind machine named start machine, one Log per entry and the exit succeeded
// with -1, and no line from qory run. An empty file writes the step with no lines.
func TestWriteMachineStepWritesOneStep(t *testing.T) {
	for _, c := range []struct {
		content string
		lines   []string
	}{
		{machineEntry("boot", "2026-11-06T00:31:44.104Z", "2026-11-06T00:31:58.904Z") + machineEntry("start Docker", "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z"), []string{"boot  14.8 s\n", "start Docker  3.2 s\n"}},
		{"", nil},
	} {
		r := &stepRun{step: &recordedStep{}}
		var said []string
		writeMachineStep(r, machineFile(t, c.content), func(l string) { said = append(said, l) })
		if r.kind != event.StepMachine || r.name != "start machine" || !slices.Equal(r.step.lines, c.lines) || r.step.exit != "succeeded -1" || said != nil {
			t.Errorf("%q: the step %s %q, lines %q, exit %q; said %q", c.content, r.kind, r.name, r.step.lines, r.step.exit, said)
		}
	}
}

// TestWriteMachineStepOfABadFileWritesNone is a file whose second line is not JSON: no
// step starts, and qory run says, in one line, that the record has no machine step and
// which line of which file is bad.
func TestWriteMachineStepOfABadFileWritesNone(t *testing.T) {
	path := machineFile(t, machineEntry("boot", "2026-11-06T00:31:44.104Z", "2026-11-06T00:31:58.904Z")+"boot\n")
	r := &stepRun{step: &recordedStep{}}
	var said []string
	writeMachineStep(r, path, func(l string) { said = append(said, l) })
	want := []string{"the run's record has no machine step: line 2 of " + path + " is not a JSON object with the strings name, started and ended"}
	if r.kind != "" || !slices.Equal(said, want) {
		t.Errorf("the step %q; said %q, want %q", r.kind, said, want)
	}
}

// TestWriteMachineStepWhoseWriteFails is a step Forager does not start, which leaves no
// step, and a step whose line Forager does not write, which still exits: each gets one
// line from qory run with Forager's error.
func TestWriteMachineStepWhoseWriteFails(t *testing.T) {
	path := machineFile(t, machineEntry("boot", "2026-11-06T00:31:44.104Z", "2026-11-06T00:31:58.904Z"))
	r := &stepRun{err: errors.New("open a new run for the host work: the run is closed")}
	var said []string
	writeMachineStep(r, path, func(l string) { said = append(said, l) })
	if want := []string{"the run's record has no machine step: writing the step failed: open a new run for the host work: the run is closed"}; !slices.Equal(said, want) {
		t.Errorf("said %q, want %q", said, want)
	}
	r = &stepRun{step: &recordedStep{logErr: errors.New("start a new step for these lines: step 1 exited")}}
	said = nil
	writeMachineStep(r, path, func(l string) { said = append(said, l) })
	if want := []string{"the machine step in the run's record is incomplete: writing it failed: start a new step for these lines: step 1 exited"}; !slices.Equal(said, want) || r.step.exit != "succeeded -1" {
		t.Errorf("said %q, want %q; the exit %q", said, want, r.step.exit)
	}
}

// TestForagerRecordsTheMachineStep is the machine step of a run that Forager opens at a
// gateway in this process: the gateway's record has step 1 of kind machine, start
// machine, with one run.log per entry, which exits succeeded with -1.
func TestForagerRecordsTheMachineStep(t *testing.T) {
	ctx := context.Background()
	runs := t.TempDir()
	id := event.NewRunID()
	at, err := openAtGateway(ctx, session.OpenSpec{RunID: id, RunsDir: runs, Report: func(string) {}}, "")
	if err != nil {
		t.Fatal(err)
	}
	path := machineFile(t, machineEntry("boot", "2026-11-06T00:31:44.104Z", "2026-11-06T00:31:58.904Z")+machineEntry("start Docker", "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z"))
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
		}
	}
	want := []string{"1 started machine: start machine", "1 log stderr: boot  14.8 s\n", "1 log stderr: start Docker  3.2 s\n", "1 exited succeeded -1"}
	if !slices.Equal(got, want) {
		t.Errorf("the record:\n%q\nwant\n%q", got, want)
	}
}
