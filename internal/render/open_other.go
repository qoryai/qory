//go:build !unix

package render

import "io/fs"

// openFlags are no flags on a system without O_NOFOLLOW and O_NONBLOCK: the check of the
// path before the open stands alone.
const openFlags = 0

// copyFlags are no flags on a system without O_NONBLOCK: the check of the entry before
// the open stands alone.
const copyFlags = 0

// fileKey is a file by its device and inode, which this system does not report.
type fileKey struct{}

// keyOf reports no key: every directory counts as one not read yet.
func keyOf(fs.FileInfo) (fileKey, bool) { return fileKey{}, false }
