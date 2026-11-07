// Package fixline marks an error whose text is a line that starts with its fix, so a
// caller that adds a fix to a line with only a cause does not add a second one.
package fixline

import "errors"

// markedError is an error whose text starts with its fix.
type markedError struct{ err error }

func (e *markedError) Error() string { return e.err.Error() }
func (e *markedError) Unwrap() error { return e.err }

// Mark marks err, whose text starts with its fix. Its text does not change.
func Mark(err error) error { return &markedError{err} }

// First reports whether err's text is a line that [Mark] marked: no wrap put another text
// in front of the line.
func First(err error) bool {
	var marked *markedError
	return errors.As(err, &marked) && marked.Error() == err.Error()
}
