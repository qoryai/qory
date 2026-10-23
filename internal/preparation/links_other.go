//go:build !unix

package preparation

import "io/fs"

// linkCount is 1 on a system whose file information gives no count of names.
func linkCount(fs.FileInfo) uint64 { return 1 }
