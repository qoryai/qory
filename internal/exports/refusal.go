package exports

import (
	"errors"
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

// Error is the text, with "the file" and "the module" at the places no caller filled in.
func (r *Refusal) Error() string {
	file, module := r.file, r.module
	if file == "" {
		file = "the file"
	}
	if module == "" {
		module = "the module"
	}
	return strings.NewReplacer(FilePlace, file, ModulePlace, module).Replace(r.text)
}

// In is the refusal with file as the path of the file it is about, see [InFile].
func (r *Refusal) In(file string) error {
	return &Refusal{text: r.text, file: file, module: r.module}
}

// Prefixed is err with prefix in front, as "<prefix>: <err>", or, for a [Refusal], the
// refusal with prefix, such as "module core", at [ModulePlace].
func Prefixed(prefix string, err error) error {
	var r *Refusal
	if errors.As(err, &r) {
		return &Refusal{text: r.text, file: r.file, module: prefix}
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// AtPath is err, an error about the file at path, with path in front, or, for an
// [InFile], with path inside its text.
func AtPath(path string, err error) error {
	var in InFile
	if errors.As(err, &in) {
		return in.In(path)
	}
	return fmt.Errorf("%s: %w", path, err)
}
