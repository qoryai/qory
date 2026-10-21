//go:build unix

package render_test

import (
	"os"
	"syscall"
)

// hardLinks is how many names the file of info has.
func hardLinks(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
