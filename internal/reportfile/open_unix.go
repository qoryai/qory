//go:build unix

package reportfile

import (
	"errors"
	"io/fs"
	"syscall"
)

// openFlags keep the open from following a link at the file's path, from waiting on a FIFO
// or a device there, and a terminal there from becoming the process's.
const openFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_NOCTTY

// isLink reports whether err is the error of an open with O_NOFOLLOW at a link.
func isLink(err error) bool { return errors.Is(err, syscall.ELOOP) }

// hardLinks is the number of hard links of the file of info, 1 when info has none.
func hardLinks(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
