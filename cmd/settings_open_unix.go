//go:build unix

package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openUnder opens the regular file rel under the directory home, following no link: the
// home itself, then each folder below it and the file, are each opened from the one
// above with O_NOFOLLOW, so a link anywhere from the home down is [errLinked], and what
// is opened is what was checked. Anything but a regular file is [errNotRegular].
func openUnder(home, rel string) (*os.File, error) {
	dir, err := unix.Open(filepath.Dir(home), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Dir(home), Err: err}
	}
	parts := append([]string{filepath.Base(home)}, strings.Split(filepath.ToSlash(rel), "/")...)
	for i, part := range parts {
		last := i == len(parts)-1
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if !last {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK
		}
		fd, err := unix.Openat(dir, part, flags, 0)
		unix.Close(dir)
		switch {
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EMLINK):
			return nil, errLinked
		case errors.Is(err, unix.ENOTDIR):
			// A link to a folder, opened with O_NOFOLLOW and O_DIRECTORY, is no folder.
			var st unix.Stat_t
			if unix.Lstat(filepath.Join(append([]string{filepath.Dir(home)}, parts[:i+1]...)...), &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
				return nil, errLinked
			}
			return nil, errNotRegular
		case err != nil:
			return nil, &os.PathError{Op: "open", Path: filepath.Join(home, rel), Err: err}
		}
		dir = fd
	}
	var st unix.Stat_t
	if err := unix.Fstat(dir, &st); err != nil {
		unix.Close(dir)
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(dir)
		return nil, errNotRegular
	}
	return os.NewFile(uintptr(dir), filepath.Join(home, rel)), nil
}
