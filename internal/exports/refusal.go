package exports

import (
	"fmt"
	"strings"
)

// FilePlace and ModulePlace mark, in the text of a [Refusal], the place of the file and
// of the module it is about, which [Refusal.In] and [Prefixed] fill in.
const (
	FilePlace   = "\x00"
	ModulePlace = "\x01"
)

// Refusal is a refusal written fix first: the first line is the step the reader takes,
// at most 100 characters, and each line after it, indented by two spaces, is the cause
// or a value. The text names the file and the module it is about inside it, so a caller
// that would put a path or a module in front of an error fills in [FilePlace] and
// [ModulePlace] instead, see [Prefixed].
type Refusal struct {
	text         string
	file, module string
}

// Refuse is a [Refusal] of fix and the lines after it, each indented by two spaces.
func Refuse(fix string, lines ...string) *Refusal {
	text := fix
	for _, l := range lines {
		text += "\n  " + l
	}
	return &Refusal{text: text}
}

// NameMax is the most characters of a role, module or entry name that a refusal prints
// on its first line. A longer name is printed by [Short].
const NameMax = 30

// Short is name, a role, module or entry name as a refusal prints it, with its middle
// replaced by an ellipsis when it has more than [NameMax] characters: its first 14
// characters, "…", and its last 15. A refusal that prints Short on its first line prints
// the whole name on a later line, so a reader can tell two long names apart.
func Short(name string) string {
	r := []rune(name)
	if len(r) <= NameMax || secretPhrase(name) {
		return name
	}
	return string(r[:14]) + "…" + string(r[len(r)-15:])
}

// secretPhrase reports whether s is the phrase [accesskey.ShownName] prints for a name
// that contains a secret, such as "(a name that contains an access key secret)": the
// phrase has a fixed length and hides a secret, so it is printed whole.
func secretPhrase(s string) bool {
	return strings.HasSuffix(s, ")") && (strings.HasPrefix(s, "(a name that contains ") || strings.HasPrefix(s, "(a value that contains "))
}

// Error is the text, with "the file" and "the module" at the places no caller filled in.
// The first line has the module as [Short] prints it. When that shortens the module and
// no later line has it, a last line has the whole text.
func (r *Refusal) Error() string {
	file, module := r.file, r.module
	if file == "" {
		file = "the file"
	}
	if module == "" {
		module = "the module"
	}
	first, rest, more := strings.Cut(r.text, "\n")
	short := Short(module)
	out := strings.NewReplacer(FilePlace, file, ModulePlace, short).Replace(first)
	if more {
		rest = strings.NewReplacer(FilePlace, file, ModulePlace, module).Replace(rest)
		out += "\n" + rest
	}
	if short != module && strings.Contains(first, ModulePlace) && !strings.Contains(rest, module) {
		out += "\n  The module is " + module + "."
	}
	return out
}

// In is the refusal with file as the path of the file it is about, see [InFile].
func (r *Refusal) In(file string) error {
	return &Refusal{text: r.text, file: file, module: r.module}
}

// Prefixed is err with prefix in front, as "<prefix>: <err>", or, when err itself is a
// [Refusal], the refusal with prefix, such as "module core", at [ModulePlace]. A
// Refusal under a wrapper, such as the one [HideValueError] returns, is prefixed as any
// other error: rebuilding it from its text would undo what the wrapper hides.
func Prefixed(prefix string, err error) error {
	if r, ok := err.(*Refusal); ok {
		return &Refusal{text: r.text, file: r.file, module: prefix}
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// AtPath is err, an error about the file at path, with path in front, or, when err
// itself is an [InFile], with path inside its text. An InFile under a wrapper gets the
// path in front, for the reason [Prefixed] gives.
func AtPath(path string, err error) error {
	if in, ok := err.(InFile); ok {
		return in.In(path)
	}
	return fmt.Errorf("%s: %w", path, err)
}
