//go:build unix

package cmd

import (
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// canWrite reports whether this process may create files in dir. A read-only mount fails
// the check for every user, root included.
func canWrite(dir string) bool {
	return unix.Access(dir, unix.W_OK) == nil
}

// folderOwner returns the user that owns the folder of info, and this process's user.
func folderOwner(info fs.FileInfo) (owner, user int) {
	user = os.Geteuid()
	owner = user
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		owner = int(st.Uid)
	}

	return owner, user
}
