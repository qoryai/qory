//go:build unix

package runtimeinstall

import (
	"io/fs"
	"os"
	"syscall"
)

// currentUID is the user id of this process; a test replaces it.
var currentUID = os.Getuid

// ownerOK reports whether info is a file of root or of the user of this process, and
// the owner's user id.
func ownerOK(info fs.FileInfo) (bool, int) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true, 0
	}
	uid := int(st.Uid)
	return uid == 0 || uid == currentUID(), uid
}
