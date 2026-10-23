package pool

import (
	"fmt"
	"strings"
	"testing"
)

// TestTheOutputTailKeepsTheLastLines adds lines past twice keptLog, so the buffer is cut
// back while lines come, and then a few more: the text is the last lines, at most
// keptLog bytes, from the start of a line, each followed by a line feed, and marked
// cut. An output within keptLog is kept whole and not marked.
func TestTheOutputTailKeepsTheLastLines(t *testing.T) {
	var o outputTail
	var all strings.Builder
	for i := 0; all.Len() < 3*keptLog; i++ {
		line := fmt.Sprintf("line %07d %s", i, strings.Repeat("x", 90))
		o.add(line)
		all.WriteString(line + "\n")
	}
	got, cut := o.text()
	out := all.String()
	switch {
	case !cut:
		t.Error("the output is not marked cut")
	case len(got) > keptLog || len(got) < keptLog-200:
		t.Errorf("%d bytes kept, want at most %d", len(got), keptLog)
	case !strings.HasSuffix(out, got) || !strings.HasSuffix(out[:len(out)-len(got)], "\n") || !strings.HasPrefix(got, "line "):
		t.Errorf("the text kept is not the last lines: %.120q", got)
	}

	var small outputTail
	small.add("one")
	small.add("")
	small.add("three")
	if got, cut := small.text(); got != "one\n\nthree\n" || cut {
		t.Errorf("a short output is %q, cut %v", got, cut)
	}
	var none outputTail
	if got, cut := none.text(); got != "" || cut {
		t.Errorf("no output is %q, cut %v", got, cut)
	}
}
