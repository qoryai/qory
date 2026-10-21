//go:build unix

package hostgit

import (
	"os"
	"strconv"
	"syscall"
)

// openFlags keep a file the check hashes from being opened through a link at its path,
// waiting on a pipe there, or becoming the terminal of the process.
const openFlags = syscall.O_NOFOLLOW | noWaitFlags

// noWaitFlags keep a file the check reads from waiting on a pipe at its path, or
// becoming the terminal of the process.
const noWaitFlags = syscall.O_NONBLOCK | syscall.O_NOCTTY

// searchable reports whether this process may search the folder at path, as git asks
// with access(2) and X_OK before it takes a folder for a git directory's objects or
// refs.
func searchable(path string) bool { return syscall.Access(path, 1) == nil }

// owner is the user id of the owner of path, without following a link at path, as git
// 2.55 reads it with lstat; "-" when there is nothing.
func owner(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "-"
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(st.Uid), 10)
	}
	return "?"
}
