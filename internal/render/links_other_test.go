//go:build !unix

package render_test

import "os"

// hardLinks is one on a system whose file information has no link count.
func hardLinks(os.FileInfo) uint64 { return 1 }
