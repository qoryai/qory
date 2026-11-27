package suite

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// logLine is a dev.qory.run.log line of the step step with text as its output.
func logLine(step int, text string) string { return streamLine(step, "stdout", text) }

// streamLine is a dev.qory.run.log line of the step step on the stream stream.
func streamLine(step int, stream, text string) string {
	return fmt.Sprintf(`{"type":"dev.qory.run.log","sequence":"1","data":{"stream":%q,"bytes":%q,"step":%d}}`,
		stream, base64.StdEncoding.EncodeToString([]byte(text)), step)
}

func writeEventsFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadOutputJoinsTheChunksOfTheStep(t *testing.T) {
	path := writeEventsFile(t,
		logLine(1, "other step\n"),
		`{"type":"dev.qory.run.log","sequence":"2","data":{"stream":"stdout","bytes":"c2Vzc2lvbgo="}}`,
		logLine(2, "first\nsec"),
		`{"type":"dev.qory.run.egress","sequence":"3","data":{"step":2}}`,
		logLine(2, "ond\n"),
		`{"type":"dev.qory.run.log","sequence":"4","data":{"step":2,"bytes":"!not base64"}}`,
	)
	if got := string(ReadOutput(path, 2)); got != "first\nsecond\n" {
		t.Errorf("ReadOutput = %q, want the two chunks of step 2", got)
	}
}

func TestReadOutputKeepsTheLastBytesOfALongOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, logLine(2, strings.Repeat(string(rune('a'+i)), MaxOutputBytes-1)+"\n"))
	}
	got := ReadOutput(writeEventsFile(t, lines...), 2)
	if len(got) != MaxOutputBytes || got[0] != 'e' || got[len(got)-1] != '\n' {
		t.Errorf("ReadOutput has %d bytes, want the %d bytes of the last chunk", len(got), MaxOutputBytes)
	}
}

func TestReadOutputStartsAtALineWhenTheCapCutsALine(t *testing.T) {
	// The cap cuts inside the line of c, in the middle of the read and at its end.
	var lines []string
	for _, text := range []string{"a\n", strings.Repeat("b", MaxOutputBytes) + "\n", strings.Repeat("c", MaxOutputBytes) + "\n", "d\n", "e\n"} {
		lines = append(lines, logLine(2, text))
	}
	if got := string(ReadOutput(writeEventsFile(t, lines...), 2)); got != "d\ne\n" {
		t.Errorf("ReadOutput = %q, want the lines after the cut line", got[max(0, len(got)-10):])
	}
	// A line that is cut in the middle of the read goes on in the next chunk.
	long := logLine(2, strings.Repeat("x", 3*MaxOutputBytes))
	if got := string(ReadOutput(writeEventsFile(t, long, logLine(2, "end of the line\nlast\n")), 2)); got != "last\n" {
		t.Errorf("ReadOutput = %q, want the line after the one that the cap cut", got)
	}
	// One line of more than the cap has no line break to start at.
	if got := string(ReadOutput(writeEventsFile(t, logLine(2, strings.Repeat("x", 3*MaxOutputBytes))), 2)); got != NoTextLine {
		t.Errorf("ReadOutput of one long line = %q, want %q", got, NoTextLine)
	}
}

func TestReadOutputOfNothingIsNil(t *testing.T) {
	path := writeEventsFile(t, logLine(3, "x"))
	for name, got := range map[string][]byte{
		"a step with no output": ReadOutput(path, 2),
		"step 0":                ReadOutput(path, 0),
		"a missing file":        ReadOutput(filepath.Join(t.TempDir(), "none"), 2),
	} {
		if got != nil {
			t.Errorf("%s: ReadOutput = %q, want nil", name, got)
		}
	}
}

// TestReadHeadIsTheFirstLineOfTheStepsStandardOutput is a marker that is split over two
// chunks and has a CR LF line end, with a forged marker later in the output, on standard
// error before it, and in another step: the first line of stdout of the step counts.
func TestReadHeadIsTheFirstLineOfTheStepsStandardOutput(t *testing.T) {
	const real = "0123456789abcdef0123456789abcdef01234567"
	const forged = "89abcdef0123456789abcdef0123456789abcdef"
	path := writeEventsFile(t,
		logLine(1, HeadMarker+forged+"\n"),
		streamLine(2, "stderr", HeadMarker+forged+"\n"),
		logLine(2, HeadMarker+real[:10]),
		streamLine(2, "stderr", HeadMarker+forged+"\n"),
		logLine(2, real[10:]+"\r\nmake test\n"+HeadMarker+forged+"\n"),
	)
	if got := ReadHead(path, 2); got != real {
		t.Errorf("ReadHead = %q, want the marker on the first line of stdout of step 2, %q", got, real)
	}
}

// TestReadHeadOfNoMarkerLineIsEmpty is each output with no marker as its first line of
// standard output: a marker on standard error only, after another line of stdout, after an
// empty line, with a short commit, with no line end, past the longest first line, and in
// another step or a missing file.
func TestReadHeadOfNoMarkerLineIsEmpty(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	marker := HeadMarker + commit + "\n"
	for name, path := range map[string]string{
		"no output":                writeEventsFile(t, logLine(3, marker)),
		"a marker on stderr":       writeEventsFile(t, streamLine(2, "stderr", marker)),
		"a marker on a terminal":   writeEventsFile(t, streamLine(2, "terminal", marker)),
		"a marker on stderr first": writeEventsFile(t, streamLine(2, "stderr", marker), logLine(2, "make test\n"+marker)),
		"a marker on line 2":       writeEventsFile(t, logLine(2, "warning\n"+marker)),
		"a marker on line 2 of 2":  writeEventsFile(t, logLine(2, "warning\n"), logLine(2, marker)),
		"an empty first line":      writeEventsFile(t, logLine(2, "\n"+marker)),
		"a marker after a space":   writeEventsFile(t, logLine(2, " "+marker)),
		"a short commit":           writeEventsFile(t, logLine(2, HeadMarker+commit[:39]+"\n"+marker)),
		"no line break":            writeEventsFile(t, logLine(2, HeadMarker+commit)),
		"a first line past 16K":    writeEventsFile(t, logLine(2, strings.Repeat("x", maxHeadScan)+"\n"), logLine(2, marker)),
		"a missing file":           filepath.Join(t.TempDir(), "none"),
	} {
		if got := ReadHead(path, 2); got != "" {
			t.Errorf("%s: ReadHead = %q, want none", name, got)
		}
	}
	if got := ReadHead(writeEventsFile(t, logLine(2, marker)), 0); got != "" {
		t.Errorf("step 0: ReadHead = %q, want none", got)
	}
}
