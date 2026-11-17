package cmd

import (
	"strings"

	"github.com/qoryai/qory/internal/exports"
)

// programError is the refusal of the program of an adapter or a tool, which starts with
// the fix and unwraps to err, the error [config.Program] returned.
type programError struct {
	refusal *exports.Refusal
	err     error
}

func (e *programError) Error() string { return e.refusal.Error() }
func (e *programError) Unwrap() error { return e.err }

// refuseProgram is err, the error of [config.Program] for the program of the key at in
// the file at file: the fix, then the cause, then the place. err's text is as the caller
// hides it.
func refuseProgram(fix, at, file string, err error) error {
	cause := strings.TrimSuffix(err.Error(), ".") + "."
	return &programError{exports.Refuse(fix, cause, at+" is in "+file+"."), err}
}

// refuseFlag is err, the refusal of the value of flag, with a last line that names the
// flag and line 1 as err has it: the fix alone. It unwraps to err. err's text is as the
// caller hides it.
func refuseFlag(flag string, err error) error {
	lines := strings.Split(err.Error(), "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = strings.TrimPrefix(lines[i], "  ")
	}
	return &programError{exports.Refuse(lines[0], append(lines[1:], "The value is from "+flag+".")...), err}
}
