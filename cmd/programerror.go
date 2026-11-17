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
