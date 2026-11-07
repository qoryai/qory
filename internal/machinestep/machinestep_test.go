package machinestep

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/qoryai/forager/event"
)

// file writes content to a new file, steps.jsonl, mode 0644, and returns its path.
func file(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "steps.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// entry is one line of the file, with its line break.
func entry(name, started, ended string) string {
	b, err := json.Marshal(map[string]string{"name": name, "started": started, "ended": ended})
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// after is an entry named name that ended d after it started.
func after(name string, d time.Duration) string {
	from := time.Date(2026, 11, 6, 0, 31, 58, 904e6, time.UTC)
	return entry(name, from.Format(time.RFC3339Nano), from.Add(d).Format(time.RFC3339Nano))
}

// TestLinesShowEachEntry is a file of four entries, written as the machine's start script
// writes them and with spaces, a further member and an offset: each line is the name,
// two spaces and the seconds from started to ended to one decimal. A file with no line
// break after its last entry reads the same.
func TestLinesShowEachEntry(t *testing.T) {
	content := `{"name":"boot","started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n" +
		`{"name":"start Docker","started":"2026-11-06T00:31:58.904Z","ended":"2026-11-06T00:32:02.104Z"}` + "\n" +
		`{ "name" : "firewall" , "started" : "2026-11-06T00:32:02.104Z" , "ended" : "2026-11-06T00:32:02.104Z", "kind": "x" }` + "\n" +
		`{"name":"pull image","started":"2026-11-06T01:32:02.000+01:00","ended":"2026-11-06T00:33:03.050Z"}` + "\n"
	want := []string{"boot  14.8 s\n", "start Docker  3.2 s\n", "firewall  0.0 s\n", "pull image  61.1 s\n"}
	for _, c := range []string{content, strings.TrimSuffix(content, "\n")} {
		got, err := Lines(file(t, c))
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%q, %v, want %q", got, err, want)
		}
	}
}

// TestLinesRoundHalfUp is the seconds of entries whose time falls on half a tenth, and
// around it: each rounds half up, to one decimal, and 24 hours exactly is a whole line.
func TestLinesRoundHalfUp(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0.0"}, {49 * time.Millisecond, "0.0"}, {49999 * time.Microsecond, "0.0"},
		{50 * time.Millisecond, "0.1"}, {150 * time.Millisecond, "0.2"}, {250 * time.Millisecond, "0.3"},
		{350 * time.Millisecond, "0.4"}, {450 * time.Millisecond, "0.5"}, {3250 * time.Millisecond, "3.3"},
		{3249 * time.Millisecond, "3.2"}, {59950 * time.Millisecond, "60.0"}, {24 * time.Hour, "86400.0"},
	} {
		got, err := Lines(file(t, after("x", c.d)))
		if want := []string{"x  " + c.want + " s\n"}; err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: %q, %v, want %q", c.d, got, err, want)
		}
	}
}

// TestLinesCleanTheName is a name with a terminal escape sequence, which is removed
// whole; with characters of category Cc, C0, DEL and C1; with characters of category
// Cf, a right-to-left override, a zero-width space and a byte order mark; and with
// U+2028 and U+2029: each is removed. A name of 150 characters, two bytes each, is cut
// to 100 characters.
func TestLinesCleanTheName(t *testing.T) {
	long := strings.Repeat("é", 150)
	content := entry("start Docker\x1b[2K", "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z") +
		entry("\x1b[1;31mfire\x1b[0mwall\r\n\t\x7f\u0085\u202e\u200b\ufeff\u2028\u2029", "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z") +
		entry(long, "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z")
	got, err := Lines(file(t, content))
	want := []string{"start Docker  3.2 s\n", "firewall  3.2 s\n", strings.Repeat("é", 100) + "  3.2 s\n"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("%q, %v, want %q", got, err, want)
	}
}

// TestLinesReadTheLimits is a file of 64 KiB exactly and a file of 64 entries: both are
// read.
func TestLinesReadTheLimits(t *testing.T) {
	short := entry("", "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z")
	name := strings.Repeat("a", 64<<10-len(short))
	got, err := Lines(file(t, entry(name, "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z")))
	if err != nil || len(got) != 1 || got[0] != strings.Repeat("a", 100)+"  3.2 s\n" {
		t.Errorf("a file of 64 KiB: %q, %v", got, err)
	}
	var b strings.Builder
	for i := range 64 {
		b.WriteString(after(fmt.Sprintf("step %d", i+1), 3200*time.Millisecond))
	}
	got, err = Lines(file(t, b.String()))
	if err != nil || len(got) != 64 || got[0] != "step 1  3.2 s\n" || got[63] != "step 64  3.2 s\n" {
		t.Errorf("64 entries: %d lines, %v", len(got), err)
	}
}

// TestLinesOfABadFile is each file that gets no step, with the text of its error:
// missing, a folder, one this process may not read, larger than 64 KiB, empty, of more
// than 64 lines, and with a line that is not a JSON object with the three strings, a
// time that does not parse, an entry that ended before it started or more than 24 hours
// after, also when the times are further apart than a Duration holds.
func TestLinesOfABadFile(t *testing.T) {
	good := entry("boot", "2026-11-06T00:31:44.104Z", "2026-11-06T00:31:58.904Z")
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.jsonl")
	if _, err := Lines(missing); err == nil || err.Error() != missing+" cannot be read: no such file or directory" {
		t.Errorf("a missing file: %v", err)
	}
	if _, err := Lines(dir); err == nil || err.Error() != dir+" is not a regular file" {
		t.Errorf("a folder: %v", err)
	}
	if os.Getuid() != 0 {
		locked := file(t, good)
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := Lines(locked); err == nil || err.Error() != locked+" cannot be read: permission denied" {
			t.Errorf("a file this process may not read: %v", err)
		}
	}
	const notObject = "line %d of %s is not a JSON object with the strings name, started and ended"
	for _, c := range []struct {
		name, content, want string
		line                int
	}{
		{"larger than 64 KiB", strings.Repeat(" ", 64<<10+1), "%[2]s is larger than 64 KiB", 0},
		{"empty", "", "%[2]s has no steps", 0},
		{"65 lines", strings.Repeat(good, 65), "%[2]s has more than 64 lines", 0},
		{"65 lines, the last one bad", strings.Repeat(good, 64) + "{}\n", "%[2]s has more than 64 lines", 0},
		{"not JSON", good + "start Docker 3.2 s\n", notObject, 2},
		{"a line break alone", "\n", notObject, 1},
		{"an array", "[]\n", notObject, 1},
		{"null", "null\n", notObject, 1},
		{"two objects on a line", strings.TrimSuffix(good, "\n") + good, notObject, 1},
		{"an empty line", good + "\n" + good, notObject, 2},
		{"no name", `{"started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n", notObject, 1},
		{"a name that is null", `{"name":null,"started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n", notObject, 1},
		{"a name in capitals", `{"NAME":"boot","started":"2026-11-06T00:31:44.104Z","ended":"2026-11-06T00:31:58.904Z"}` + "\n", notObject, 1},
		{"a number as ended", `{"name":"boot","started":"2026-11-06T00:31:44.104Z","ended":1762389118}` + "\n", notObject, 1},
		{"a started that does not parse", good + entry("boot", "2026-11-06 00:31:44", "2026-11-06T00:31:58.904Z"), "line %d of %s: started is not an RFC 3339 time", 2},
		{"an ended that does not parse", entry("boot", "2026-11-06T00:31:44.104Z", "later"), "line %d of %s: ended is not an RFC 3339 time", 1},
		{"ended before started", good + good + after("boot", -time.Millisecond), "line %d of %s: ended is before started", 3},
		{"ended a millisecond past 24 hours", after("boot", 24*time.Hour+time.Millisecond), "line %d of %s: ended is more than 24 hours after started", 1},
		{"times 9998 years apart", entry("boot", "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999Z"), "line %d of %s: ended is more than 24 hours after started", 1},
	} {
		path := file(t, c.content)
		got, err := Lines(path)
		if want := fmt.Sprintf(c.want, c.line, path); err == nil || err.Error() != want || got != nil {
			t.Errorf("%s: %q, %v, want %q", c.name, got, err, want)
		}
	}
}

// startedStep starts a recorded step, or returns err, and records the kind and name it
// got.
type startedStep struct {
	err        error
	step       *recordedStep
	kind, name string
}

func (s *startedStep) start(kind, name string) (Step, error) {
	s.kind, s.name = kind, name
	if s.err != nil {
		return nil, s.err
	}
	return s.step, nil
}

// recordedStep is a [Step] that records its lines and its exit, whose Log returns logErr
// and whose Exit returns exitErr.
type recordedStep struct {
	lines           []string
	exit            string
	logErr, exitErr error
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
	return s.exitErr
}

// write runs Write with s and the file of content, and returns the lines it said.
func write(t *testing.T, s *startedStep, content string) (path string, said []string) {
	t.Helper()
	path = file(t, content)
	Write(s.start, path, func(l string) { said = append(said, l) })
	return path, said
}

// TestWriteWritesOneStep is the machine step of a file of two entries: one step of kind
// machine named start machine, one Log per entry and the exit succeeded with -1, and no
// line said.
func TestWriteWritesOneStep(t *testing.T) {
	s := &startedStep{step: &recordedStep{}}
	_, said := write(t, s, entry("boot", "2026-11-06T00:31:44.104Z", "2026-11-06T00:31:58.904Z")+entry("start Docker", "2026-11-06T00:31:58.904Z", "2026-11-06T00:32:02.104Z"))
	if s.kind != event.StepMachine || s.name != "start machine" || !slices.Equal(s.step.lines, []string{"boot  14.8 s\n", "start Docker  3.2 s\n"}) || s.step.exit != "succeeded -1" || said != nil {
		t.Errorf("the step %s %q, lines %q, exit %q; said %q", s.kind, s.name, s.step.lines, s.step.exit, said)
	}
}

// TestWriteOfABadFileWritesNone is a file whose second line is not JSON, and an empty
// file: no step starts, and one line says that the record has no machine step and why.
func TestWriteOfABadFileWritesNone(t *testing.T) {
	for _, c := range []struct{ content, want string }{
		{entry("boot", "2026-11-06T00:31:44.104Z", "2026-11-06T00:31:58.904Z") + "boot\n", "the run's record has no machine step: line 2 of %s is not a JSON object with the strings name, started and ended"},
		{"", "the run's record has no machine step: %s has no steps"},
	} {
		s := &startedStep{step: &recordedStep{}}
		path, said := write(t, s, c.content)
		if want := []string{fmt.Sprintf(c.want, path)}; s.kind != "" || !slices.Equal(said, want) {
			t.Errorf("the step %q; said %q, want %q", s.kind, said, want)
		}
	}
}

// TestWriteWhoseWriteFails is a step that does not start, which leaves no step; a step
// whose Log fails, whose Exit fails, and whose Log and Exit both fail, each of which
// still exits: each says exactly one line, of one line, with the Log's error when there
// is one.
func TestWriteWhoseWriteFails(t *testing.T) {
	content := entry("boot", "2026-11-06T00:31:44.104Z", "2026-11-06T00:31:58.904Z")
	logErr, exitErr := errors.New("the run is closed"), errors.New("the step exited already")
	_, said := write(t, &startedStep{err: logErr}, content)
	if want := []string{"the run's record has no machine step: writing the step failed: the run is closed"}; !slices.Equal(said, want) {
		t.Errorf("a step that does not start: said %q, want %q", said, want)
	}
	for _, c := range []struct {
		name            string
		logErr, exitErr error
		want            string
	}{
		{"the Log", logErr, nil, "the run is closed"},
		{"the Exit", nil, exitErr, "the step exited already"},
		{"both", logErr, exitErr, "the run is closed"},
	} {
		s := &startedStep{step: &recordedStep{logErr: c.logErr, exitErr: c.exitErr}}
		_, said := write(t, s, content)
		want := []string{"the machine step in the run's record is incomplete: writing it failed: " + c.want}
		if !slices.Equal(said, want) || strings.Contains(said[0], "\n") || s.step.exit != "succeeded -1" {
			t.Errorf("%s fails: said %q, want %q; the exit %q", c.name, said, want, s.step.exit)
		}
	}
}
