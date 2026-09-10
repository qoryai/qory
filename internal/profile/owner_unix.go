//go:build unix

package profile

import (
	"os"
	"syscall"
)

// OwnedByCurrentUser reports whether the file belongs to the user running qory. A stat a
// caller cannot read counts as another user's, so [Discover] and the configuration's
// discovery skip the file.
func OwnedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == os.Getuid()
}
