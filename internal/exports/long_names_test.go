package exports_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/exports"
)

// TestRefusalsKeepTheFixLineShortForLongNames is each refusal of the exports section that
// prints a name in its first line, with names of 24 and of 64 characters: the first line
// has at most 100 characters, and some line has the whole name.
func TestRefusalsKeepTheFixLineShortForLongNames(t *testing.T) {
	for _, n := range []int{24, 64} {
		name := "n" + strings.Repeat("x", n-1)
		for what, body := range map[string]string{
			"a module listed twice": "exports: {modules: [" + name + ", " + name + "]}\n",
			"a stack listed twice":  "exports: {stacks: [" + name + ", " + name + "]}\n",
			"a dir key":             "exports: {dir: {stacks: ./s, " + name + ": ./m}}\n",
		} {
			_, err := exports.Read(write(t, body))
			if err == nil {
				t.Errorf("%s: no refusal", what)
				continue
			}
			lines := strings.Split(err.Error(), "\n")
			if chars := utf8.RuneCountInString(lines[0]); chars > 100 {
				t.Errorf("%s, %d characters: line 1 has %d characters: %s", what, n, chars, lines[0])
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("%s, %d characters: no line has the whole name: %v", what, n, err)
			}
		}
	}
}
