package exports

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
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

// NameMax is the longest name, in characters, that [Fit] leaves whole.
const NameMax = 24

// shortHead and shortTail are the characters [Short] keeps of the start and the end of a
// name. With the "…" between them, the result has 20 characters, so a name that Short
// shortens loses at least 5.
const (
	shortHead = 9
	shortTail = 10
)

// Short is name with its middle replaced by "…" when it has more than [NameMax]
// characters: its first 9 characters, "…", and its last 10. It returns a shorter name
// as it is.
func Short(name string) string { return shorten(name, 0) }

// shorten is [Short] with extra more characters kept at each end.
func shorten(name string, extra int) string {
	r := []rune(name)
	if len(r) <= NameMax {
		return name
	}
	head, tail := shortHead+extra, shortTail+extra
	if head+1+tail >= len(r) {
		return name
	}
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// shortened maps each of names, which are different, to its [Short] form, keeping more
// characters at each end until no two names have the same form.
func shortened(names []string) map[string]string {
	for extra := 0; ; extra++ {
		forms := make(map[string]string, len(names))
		seen := make(map[string]bool, len(names))
		distinct := true
		for _, name := range names {
			form := shorten(name, extra)
			forms[name] = form
			if seen[form] {
				distinct = false
			}
			seen[form] = true
		}
		if distinct {
			return forms
		}
	}
}

// longName matches, in a refusal's first line, a short quoted value, which stays as it is
// and is matched first so that its closing quote does not open the next match; a quoted
// value of more than 24 characters between its quotes, whole from its opening quote to its closing quote,
// spaces included; or a run of letters, digits, dots, hyphens and underscores that does
// not end with a dot, so the full stop of a sentence stays. A slash or a space ends a
// run, so "agents/" in front of a role name stays.
var longName = regexp.MustCompile(`"[^"\n]{0,24}"|"(?:[^"\\\n]|\\.){25,}"|[\p{L}\p{N}._-]{24,}[\p{L}\p{N}_-]`)

// keyPath matches the keys that start a dotted name in a refusal, such as the "bind." and
// "agents." of bind.agents.<role>, or the "gateway.credentials." of
// gateway.credentials.<name>. [Fit] leaves them whole and shortens the name after them.
var keyPath = regexp.MustCompile(`^(?:(?:gateway\.(?:credentials|tools|integrations|egress\.paths|server\.via)|wall\.images|harness\.(?:launch|tiers)|workflows|exports\.(?:stacks|modules)|extending\.target\.(?:runtime|model))\.)?(?:(?:bind|provides|roles)\.)?(?:(?:agents|commands|skills|output-styles|hooks|mcp|files)\.)?`)

// Fit is text, one refusal or several, in which each refusal whose first line, an
// unindented line, has more than 100 characters has the names in that line of more than
// [NameMax] characters printed by [Short]. A name is a quoted value, or a run of letters,
// digits, dots, hyphens and underscores after the keys of its path, such as "bind." and
// "agents.". When two names would have one form, each keeps more characters, until the
// forms differ. When no line after the first line has the whole name, a last line of the
// refusal has it: "The full name of <short> is <name>." A first line of at most 100
// characters stays as it is. Fit shortens names only: a first line that is still over 100
// characters has more than names in it.
func Fit(text string) string {
	var out []string
	var full []string
	block := func() {
		out = append(out, full...)
		full = nil
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
	// nameOf is the name in a match: the match after its leading keys.
	nameOf := func(match string) (keys, name string) {
		if strings.HasPrefix(match, `"`) {
			return "", match
		}
		keys = keyPath.FindString(match)
		return keys, match[len(keys):]
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, " ") {
			block()
			if utf8.RuneCountInString(line) <= 100 {
				out = append(out, line)
				continue
			}
			var names []string
			for _, match := range longName.FindAllString(line, -1) {
				if _, name := nameOf(match); long(name) && !slices.Contains(names, name) {
					names = append(names, name)
				}
			}
			forms := shortened(names)
			after := rest(i)
			for _, name := range names {
				if !hasName(after, name) && forms[name] != name {
					full = append(full, "  The full name of "+forms[name]+" is "+name+".")
				}
			}
			line = longName.ReplaceAllStringFunc(line, func(match string) string {
				keys, name := nameOf(match)
				if form, ok := forms[name]; ok {
					return keys + form
				}
				return match
			})
		}
		out = append(out, line)
	}
	block()
	return strings.Join(out, "\n")
}

// long reports whether name, a match of [longName] without its leading keys, has more than
// [NameMax] characters, not counting the quotes of a quoted value.
func long(name string) bool {
	n := utf8.RuneCountInString(name)
	if strings.HasPrefix(name, `"`) {
		n -= 2
	}
	return n > NameMax
}

// nameChar reports whether r can be a character of a name that [longName] matches.
func nameChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-'
}

// hasName reports whether text has name as a whole name: the character before and the one
// after it are not characters of a name.
func hasName(text, name string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], name)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(name)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, size := utf8.DecodeRuneInString(text[end:])
		if after == '.' {
			// A full stop ends the name, as it ends a sentence, unless a name character follows.
			next, _ := utf8.DecodeRuneInString(text[end+size:])
			if end+size == len(text) || !nameChar(next) {
				after = ' '
			}
		}
		if (start == 0 || !nameChar(before) || strings.HasPrefix(name, `"`)) && (end == len(text) || !nameChar(after) || strings.HasPrefix(name, `"`)) {
			return true
		}
		from = start + 1
	}
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
	if d, ok := err.(*decodeRefusal); ok {
		return &decodeRefusal{Prefixed(prefix, d.Refusal).(*Refusal), d.cause}
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// AtPath is err, an error about the file at path, with path in front, or, when err
// itself is an [InFile], with path inside its text. An InFile under a wrapper gets the
// path in front, for the reason [Prefixed] gives.
func AtPath(path string, err error) error {
	path = HidePath(path)
	if in, ok := err.(InFile); ok {
		return in.In(path)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// decodeRefusal is the refusal of a value the decoder could not read, which unwraps to
// the decoder's error, so errors.Is and errors.As still match it, a *yaml.TypeError
// among them. Its text has the decoder's words, and only when they contain no access key
// secret or run key. It is an [InFile] as a [Refusal] is.
type decodeRefusal struct {
	*Refusal
	cause error
}

// Unwrap returns the decoder's error.
func (d *decodeRefusal) Unwrap() error { return d.cause }

// In is the refusal with file at [FilePlace], and the same cause.
func (d *decodeRefusal) In(file string) error {
	return &decodeRefusal{d.Refusal.In(file).(*Refusal), d.cause}
}
