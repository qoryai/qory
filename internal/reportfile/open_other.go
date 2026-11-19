//go:build !unix

package reportfile

import "io/fs"

// openFlags are no flags on a system without O_NOFOLLOW and O_NONBLOCK: the checks before
// and after the open still read a regular file alone.
const openFlags = 0

// isLink reports no link: without O_NOFOLLOW, the check before the open finds one.
func isLink(error) bool { return false }

// hardLinks is 1: a system without a link count in fs.FileInfo leaves the check out.
func hardLinks(fs.FileInfo) uint64 { return 1 }
