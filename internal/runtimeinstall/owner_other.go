//go:build !unix

package runtimeinstall

import "io/fs"

// ownerOK is true on a system with no user ids in its file information.
func ownerOK(fs.FileInfo) (bool, int) { return true, 0 }

// currentUID is 0 on a system with no user ids; a test replaces it.
var currentUID = func() int { return 0 }
