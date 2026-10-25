//go:build unix

package cmd

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// openEngineProxyFile opens path for reading without following a link at path and
// without blocking, so a named pipe that no program writes to returns at once;
// [readEngineProxy] then reads only a regular file.
func openEngineProxyFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

// engineProxyLink reports whether err is the open's refusal of a link at the path.
func engineProxyLink(err error) bool { return errors.Is(err, unix.ELOOP) }

// fileOwner is the uid of the file that info describes, false when info has none.
func fileOwner(info fs.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
