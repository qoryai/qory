package cmd

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestAProgramRefusalKeepsItsFirstLineShortForLongNames is the refusal of the program of
// an adapter and of a tool with a name of 64 characters: line 1 has at most 100
// characters, some line has the whole name, and errors.Is matches the cause.
func TestAProgramRefusalKeepsItsFirstLineShortForLongNames(t *testing.T) {
	name := "n" + strings.Repeat("x", 63)
	cause := errors.New("a is not on the PATH; install it there, or give its absolute path")
	for what, at := range map[string]string{"adapter": "gateway.credentials." + name, "command": "gateway.tools." + name} {
		err := refuseProgram("Fix the "+what+" of "+at+".", at+"."+what, "forager.yaml", cause)
		lines := strings.Split(err.Error(), "\n")
		if n := utf8.RuneCountInString(lines[0]); n > 100 {
			t.Errorf("%s: line 1 has %d characters: %s", what, n, lines[0])
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s: no line has the whole name: %v", what, err)
		}
		if !errors.Is(err, cause) {
			t.Errorf("%s: errors.Is does not match the cause", what)
		}
	}
}
