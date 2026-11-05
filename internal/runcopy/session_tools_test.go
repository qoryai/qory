package runcopy

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/qoryai/qory/internal/image"
)

// shellWords are the shell's keywords and builtins that the script of the git session uses.
var shellWords = []string{
	"case", "cd", "command", "do", "done", "elif", "else", "esac", "exit", "export", "fi", "for", "if", "in",
	"printf", "read", "return", "set", "shift", "test", "then", "trap", "umask", "unset", "while", "echo",
	"continue", "exec",
}

var (
	singleQuoted = regexp.MustCompile(`'[^']*'`)
	doubleQuoted = regexp.MustCompile(`"[^"]*"`)
	arithmetic   = regexp.MustCompile(`\$\(\([^)]*\)\)`)
	shellComment = regexp.MustCompile(`(^|\s)#.*$`)
	commandStart = regexp.MustCompile(`[;|&({!]|\$\(|\b(?:then|do|else|if|elif|while)\s`)
	aProgram     = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	functionName = regexp.MustCompile(`(?m)^\s*([a-z_][a-z0-9_]*)\(\)`)
)

// scriptPrograms is each word at the start of a command of the sh script, with no keyword, builtin
// or function of the script. It reads line by line, so it also reads the lines of a sh -c '...'.
func scriptPrograms(script string) []string {
	functions := functionName.FindAllStringSubmatch(script, -1)
	var words []string
	for line := range strings.Lines(script) {
		line = shellComment.ReplaceAllString(strings.TrimRight(line, "\n"), "$1")
		line = singleQuoted.ReplaceAllString(line, "X")
		line = arithmetic.ReplaceAllString(line, "X")
		// A command in a quoted $(...) stays, and the quotes inside it end up unpaired.
		line = doubleQuoted.ReplaceAllStringFunc(line, func(q string) string {
			if strings.Contains(q, "$(") {
				return q
			}
			return "X"
		})
		for _, command := range commandStart.Split(line, -1) {
			fields := strings.Fields(command)
			for len(fields) > 0 && strings.Contains(fields[0], "=") {
				fields = fields[1:]
			}
			if len(fields) == 0 {
				continue
			}
			word := strings.TrimRight(fields[0], ")'")
			if !aProgram.MatchString(word) || slices.Contains(shellWords, word) || slices.Contains(words, word) ||
				slices.ContainsFunc(functions, func(f []string) bool { return f[1] == word }) {
				continue
			}
			words = append(words, word)
		}
	}
	slices.Sort(words)
	return words
}

// TestSessionToolsListsEveryProgramOfTheScript fails when the script of the git session runs a
// program that qory image check does not test for.
func TestSessionToolsListsEveryProgramOfTheScript(t *testing.T) {
	tools := append(image.SessionTools(), "git")
	for _, word := range scriptPrograms(sessionScript) {
		if !slices.Contains(tools, word) {
			t.Errorf("the script runs %s, which image.SessionTools does not list", word)
		}
	}
	for _, tool := range tools {
		if !slices.Contains(scriptPrograms(sessionScript), tool) {
			t.Errorf("image.SessionTools lists %s, which the script does not run", tool)
		}
	}
}

// TestScriptProgramsFindsANewProgram is the scan of the test above on a script with programs in
// each place a command starts.
func TestScriptProgramsFindsANewProgram(t *testing.T) {
	got := scriptPrograms(`# a comment with words
f() { one x; }
if two 'y'; then three; fi
z=$(four | five)
! six
x="$(seven "$y")"
find . -exec sh -c '
	eight $1
	test -r "$p" && nine' sh {} +
i=$((i + 1))
printf 'ten; eleven (twelve)\n'
`)
	want := []string{"eight", "find", "five", "four", "nine", "one", "seven", "six", "three", "two"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
