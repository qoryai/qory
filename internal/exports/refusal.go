package exports

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
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
const NameMax = 20

// Short is name, a role, module or entry name as a refusal prints it on its first line,
// with its middle replaced by an ellipsis when it has more than [NameMax] characters: its
// first 9 characters, "…", and its last 10.
func Short(name string) string {
	r := []rune(name)
	if len(r) <= NameMax {
		return name
	}
	return string(r[:9]) + "…" + string(r[len(r)-10:])
}

// longName matches a name of more than [NameMax] characters in a refusal's first line: a
// run of letters, digits, hyphens and underscores, so that the "agents/" and "bind." in
// front of a role name stay.
var longName = regexp.MustCompile(`[\p{L}\p{N}_-]{21,}`)

// Fit is text, one refusal or several, with each name of more than [NameMax] characters
// in the first line of a refusal, an unindented line of more than 100 characters,
// printed by [Short]. A first line of at most 100 characters stays as it is. When no line
// after that first line has the whole name, a last line of the refusal does:
// "The full name of <short> is <name>." So the first line stays within 100 characters
// whatever the names, and the reader still finds the whole name.
func Fit(text string) string {
	var out []string
	var long []string
	block := func() {
		for _, name := range long {
			out = append(out, "  The full name of "+Short(name)+" is "+name+".")
		}
		long = nil
	}
	lines := strings.Split(text, "\n")
	rest := func(i int) string {
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if !strings.HasPrefix(lines[j], " ") {
				end = j
				break
			}
		}
		return strings.Join(lines[i+1:end], "\n")
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, " ") {
			block()
			if utf8.RuneCountInString(line) <= 100 {
				out = append(out, line)
				continue
			}
			after := rest(i)
			for _, name := range longName.FindAllString(line, -1) {
				if !strings.Contains(after, name) && !slices.Contains(long, name) {
					long = append(long, name)
				}
			}
			line = longName.ReplaceAllStringFunc(line, Short)
		}
		out = append(out, line)
	}
	block()
	return strings.Join(out, "\n")
}

// Errorf is an error of the formatted text, which [Fit] has fitted. A refusal written
// fix first uses it in place of [fmt.Errorf] when its text has a role, module or entry
// name. It wraps no error.
func Errorf(format string, args ...any) error {
	return errors.New(Fit(fmt.Sprintf(format, args...)))
}

// Error is the text, with "the file" and "the module" at the places no caller filled in,
// as [Fit] fits it.
func (r *Refusal) Error() string {
	file, module := r.file, r.module
	if file == "" {
		file = "the file"
	}
	if module == "" {
		module = "the module"
	}
	return Fit(strings.NewReplacer(FilePlace, file, ModulePlace, module).Replace(r.text))
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
