//go:build unix

package preparation

import (
	"io/fs"
	"syscall"
)

// linkCount is how many names the file of info has, from its st_nlink; 1 when the
// system gives no count.
func linkCount(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
