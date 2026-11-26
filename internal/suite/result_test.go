package suite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
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
	if strings.Contains(r.OutputTail, "first") || !strings.HasSuffix(r.OutputTail, "\nlast line") {
		t.Errorf("OutputTail starts %q and ends %q, want the end of the output only", r.OutputTail[:10], r.OutputTail[len(r.OutputTail)-12:])
	}
	if len(r.OutputTail) > MaxOutputBytes {
		t.Errorf("OutputTail has %d bytes, want at most %d", len(r.OutputTail), MaxOutputBytes)
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

func TestLastBytesStartsAtAWholeCharacter(t *testing.T) {
	text := []byte("aé€\U0001F600z")
	for n := 1; n <= len(text); n++ {
		got := lastBytes(text, n)
		if !utf8.Valid(got) && utf8.Valid(text) {
			t.Errorf("lastBytes(%d) = %q, want valid UTF-8", n, got)
		}
		if len(got) > n || !strings.HasSuffix(string(text), string(got)) {
			t.Errorf("lastBytes(%d) = %q, want at most %d bytes from the end", n, got, n)
		}
	}
	if got := string(lastBytes([]byte("x€"), 2)); got != "" {
		t.Errorf("a cut inside a 3-byte character = %q, want it moved past the character", got)
	}
}
