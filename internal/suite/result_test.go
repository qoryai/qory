package suite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	return b.String()
}

func TestTail(t *testing.T) {
	for _, c := range []struct {
		name   string
		output string
		n      int
		want   string
	}{
		{"empty", "", 3, ""},
		{"only a break", "\n", 3, ""},
		{"fewer lines", "a\nb\n", 3, "a\nb"},
		{"no final break", "a\nb", 3, "a\nb"},
		{"exactly n", "a\nb\nc\n", 3, "a\nb\nc"},
		{"more than n", "a\nb\nc\nd\n", 2, "c\nd"},
		{"blank line kept", "a\n\nb\n", 2, "\nb"},
		{"n of 1", "a\nb\nc", 1, "c"},
		{"n of 0", "a", 0, ""},
	} {
		if got := Tail([]byte(c.output), c.n); got != c.want {
			t.Errorf("%s: Tail = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNewResultKeepsTheLast200Lines(t *testing.T) {
	r := NewResult(Counts{Tests: 300, Failed: 2, Skipped: 1}, []byte(lines(500)))
	got := strings.Split(r.OutputTail, "\n")
	if len(got) != MaxTailLines || got[0] != "line 301" || got[len(got)-1] != "line 500" {
		t.Errorf("tail has %d lines from %q to %q", len(got), got[0], got[len(got)-1])
	}
}

func TestNewResultLooksAtTheLastBytesOnly(t *testing.T) {
	output := []byte("first line\n" + strings.Repeat("x", MaxOutputBytes) + "\nlast line\n")
	r := NewResult(Counts{}, output)
	// The cap cuts inside the line of x, which is dropped whole.
	if r.OutputTail != "last line" {
		t.Errorf("OutputTail = %q, want the line after the cut only", r.OutputTail)
	}
	if len(r.OutputTail) > MaxOutputBytes {
		t.Errorf("OutputTail has %d bytes, want at most %d", len(r.OutputTail), MaxOutputBytes)
	}
}

func TestNewResultOfOutputWithNoTextLineInTheCapHasTheLine(t *testing.T) {
	const line = "No line with text starts in the last 256 KiB of the suite's output, so qory shows none of those bytes."
	for name, output := range map[string]string{
		"one long line":            strings.Repeat("x", 2*MaxOutputBytes),
		"a break before the cap":   "first\n" + strings.Repeat("x", MaxOutputBytes+1),
		"one long line and \\n":    strings.Repeat("x", 2*MaxOutputBytes) + "\n",
		"one long line and \\r\\n": strings.Repeat("x", 2*MaxOutputBytes) + "\r\n",
		"one long line and \\n\\n": strings.Repeat("x", 2*MaxOutputBytes) + "\n\n",
		"one long line and \\n\\r": strings.Repeat("x", 2*MaxOutputBytes) + "\n\r",
		"\\n alone":                strings.Repeat("\n", 2*MaxOutputBytes),
		"\\r\\n alone":             strings.Repeat("\r\n", 2*MaxOutputBytes),
	} {
		if got := NewResult(Counts{}, []byte(output)).OutputTail; got != line {
			t.Errorf("%s: OutputTail = %q, want %q", name, got, line)
		}
	}
	// Output of the cap or less is never cut, and no output has no tail.
	if got := NewResult(Counts{}, []byte(strings.Repeat("x", MaxOutputBytes))).OutputTail; len(got) != MaxOutputBytes {
		t.Errorf("OutputTail of output at the cap has %d bytes, want %d", len(got), MaxOutputBytes)
	}
	if got := NewResult(Counts{}, nil).OutputTail; got != "" {
		t.Errorf("OutputTail of no output = %q, want none", got)
	}
}

// TestNoTextLineHasTheCap: the text of NoTextLine contains 256 KiB, the value of
// MaxOutputBytes.
func TestNoTextLineHasTheCap(t *testing.T) {
	if MaxOutputBytes != 256<<10 {
		t.Errorf("MaxOutputBytes = %d, want %d", MaxOutputBytes, 256<<10)
	}
	if !strings.Contains(NoTextLine, "256 KiB") {
		t.Errorf("NoTextLine = %q, want it to contain 256 KiB", NoTextLine)
	}
}

func TestNewResultOfAGreenSuiteHasAShortTail(t *testing.T) {
	r := NewResult(Counts{Tests: 2}, []byte("ok\n"))
	if r.OutputTail != "ok" || r.Tests != 2 {
		t.Errorf("result %+v, want the counts and the one line", r)
	}
}

func TestWriteResultMembers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suite_result.json")
	want := NewResult(Counts{Tests: 3, Failed: 1, Skipped: 0}, []byte("FAIL TestX\n"))
	if err := WriteResult(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got["tests"] != 3.0 || got["failed"] != 1.0 || got["skipped"] != 0.0 || got["output_tail"] != "FAIL TestX" {
		t.Errorf("members = %v", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	if err := WriteResult(path, want); err == nil || !strings.HasPrefix(err.Error(), "The suite result file ") || !strings.Contains(err.Error(), " exists or could not be made: ") {
		t.Errorf("a second write returned %v, want the error of a file that exists", err)
	}
}

func TestNewResultEscapesHiddenCharacters(t *testing.T) {
	r := NewResult(Counts{}, []byte("ok\n\u202e<evil>\x1b[2J\n"))
	if want := "ok\n<U+202E><evil><U+001B>[2J"; r.OutputTail != want {
		t.Errorf("OutputTail = %q, want %q", r.OutputTail, want)
	}
}

func TestLastBytesStartsAtALine(t *testing.T) {
	text := []byte("one\ntwo €\nthree\n")
	for _, c := range []struct {
		n    int
		want string
	}{
		{len(text), string(text)},
		{len(text) + 5, string(text)},
		// A cut inside a line drops that line's part, and a cut inside a character too.
		{len(text) - 1, "two €\nthree\n"},
		{len("two €\nthree\n") + 2, "two €\nthree\n"},
		{len("three\n") + 1, "three\n"},
		// A cut right after a line break keeps the line.
		{len("two €\nthree\n"), "two €\nthree\n"},
		{len("three\n"), "three\n"},
		// The last n bytes have no line break.
		{len("three"), ""},
		{0, ""},
	} {
		if got := string(lastBytes(text, c.n)); got != c.want {
			t.Errorf("lastBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	// The last line may have no break after it.
	for n, want := range map[int]string{3: "bc", 2: "bc", 1: ""} {
		if got := string(lastBytes([]byte("a\nbc"), n)); got != want {
			t.Errorf("lastBytes(%d) of a text with an unfinished last line = %q, want %q", n, got, want)
		}
	}
}
