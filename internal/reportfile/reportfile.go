// Package reportfile reads a file that a session wrote for qory, such as a reviewer's
// report, after that session ended: as a regular file with one hard link, at most a given
// size, and without following a link at the file's path or at any folder above it. A link,
// a second hard link, a FIFO, a folder, a device and an oversize file are each an [Error]
// with its [Cause], so the caller can state why it read nothing.
package reportfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// MaxBytes is the most bytes of a reviewer's report that qory reads: 1 MiB.
const MaxBytes = 1 << 20

// Cause is why [Read] read no file.
type Cause int

// The causes of an [Error].
const (
	// Missing is a path with no file.
	Missing Cause = iota + 1
	// Link is a symbolic link at the path, or at a folder above it.
	Link
	// NotRegular is a FIFO, a folder, a socket or a device at the path.
	NotRegular
	// TooLarge is a regular file of more bytes than the limit.
	TooLarge
	// Unreadable is a file that the open or the read failed on.
	Unreadable
	// BadPath is a path that is not absolute and clean.
	BadPath
	// HardLink is a regular file with more than one hard link.
	HardLink
)

// Error is why [Read] read no file: its path, its cause and, for [Unreadable], the error of
// the open or the read.
type Error struct {
	Path  string
	Cause Cause
	Limit int64
	Err   error
}

// What is the cause as a clause that follows the file's name: "is a link".
func (e *Error) What() string {
	switch e.Cause {
	case Missing:
		return "is missing"
	case Link:
		return "is a link, or is below a folder that is a link"
	case NotRegular:
		return "is no regular file"
	case TooLarge:
		return "has more than " + strconv.FormatInt(e.Limit, 10) + " bytes"
	case BadPath:
		return "is no absolute, clean path"
	case HardLink:
		return "has another hard link"
	default:
		if e.Err != nil {
			// The cause alone: a *fs.PathError's text has the path, which a point that
			// leaves this machine must not have.
			cause := e.Err
			var pathErr *fs.PathError
			if errors.As(cause, &pathErr) {
				cause = pathErr.Err
			}
			return "cannot be read: " + firstLine(cause.Error())
		}
		return "cannot be read"
	}
}

// Error is the fix on line 1, then the path and the cause on line 2.
func (e *Error) Error() string {
	return fmt.Sprintf("Write the report as one regular file of at most %d bytes.\n%s %s.",
		e.Limit, strconv.Quote(e.Path), e.What())
}

// Unwrap is the error of the open or the read, nil for any other cause.
func (e *Error) Unwrap() error { return e.Err }

// Read reads the file at path, an absolute and clean path, of at most limit bytes. It
// follows no link: a link at path, or at any folder from path's folder up to the root, is
// an [Error] with [Link], so the caller gives a path with no link in it, such as one that
// filepath.EvalSymlinks returns. A file with a second hard link is [HardLink]. A file that
// is not regular is [NotRegular], also when it turns into one between the check and the
// open, and the open does not wait on a FIFO.
func Read(path string, limit int64) ([]byte, error) {
	fail := func(c Cause, err error) ([]byte, error) {
		return nil, &Error{Path: path, Cause: c, Limit: limit, Err: err}
	}
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fail(BadPath, nil)
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if info, err := os.Lstat(dir); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return fail(Link, nil)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fail(Missing, nil)
	case err != nil:
		return fail(Unreadable, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return fail(Link, nil)
	case !info.Mode().IsRegular():
		return fail(NotRegular, nil)
	case hardLinks(info) != 1:
		return fail(HardLink, nil)
	case info.Size() > limit:
		return fail(TooLarge, nil)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openFlags, 0)
	if err != nil {
		if isLink(err) {
			return fail(Link, nil)
		}
		if errors.Is(err, fs.ErrNotExist) {
			return fail(Missing, nil)
		}
		return fail(Unreadable, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return fail(Unreadable, err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return fail(NotRegular, nil)
	}
	if hardLinks(opened) != 1 {
		return fail(HardLink, nil)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return fail(Unreadable, err)
	}
	if int64(len(data)) > limit {
		return fail(TooLarge, nil)
	}
	return data, nil
}

// firstLine is text up to its first line break.
func firstLine(text string) string {
	for i, r := range text {
		if r == '\n' || r == '\r' {
			return text[:i]
		}
	}
	return text
}
