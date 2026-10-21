//go:build unix

package render

import (
	"io/fs"
	"syscall"
)

// openFlags keep the exclude file from being opened through a link at its path, waiting
// on a pipe there, or becoming the terminal of the process.
const openFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_NOCTTY

// copyFlags keep a copy from waiting on a pipe that took a file's place, or becoming the
// terminal of the process.
const copyFlags = syscall.O_NONBLOCK | syscall.O_NOCTTY

// fileKey is a file by its device and inode.
type fileKey struct{ dev, ino uint64 }

// keyOf is the file of info by its device and inode.
func keyOf(info fs.FileInfo) (fileKey, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileKey{}, false
	}
	return fileKey{uint64(st.Dev), uint64(st.Ino)}, true
}
