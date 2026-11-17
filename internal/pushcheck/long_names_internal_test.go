package pushcheck

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestAFixWithALongArtifactTypeKeepsItsFirstLineShort is the refusal of a check past its
// limit for an artifact type of 64 characters: line 1 has at most 100 characters, and a
// later line has the whole type.
func TestAFixWithALongArtifactTypeKeepsItsFirstLineShort(t *testing.T) {
	long := "t" + strings.Repeat("y", 63)
	f := failure(fmt.Sprintf(textTimeLimit, long), textTimeCause)
	for what, text := range map[string]string{"message": f.Message, "step": f.Step} {
		lines := strings.Split(text, "\n")
		if n := utf8.RuneCountInString(lines[0]); n > 100 {
			t.Errorf("%s: line 1 has %d characters: %s", what, n, lines[0])
		}
		if strings.Contains(lines[0], long) {
			t.Errorf("%s: line 1 has the whole type: %s", what, lines[0])
		}
		if !strings.Contains(strings.Join(lines[1:], "\n"), long) {
			t.Errorf("%s: no later line has the whole type: %q", what, text)
		}
	}
}
