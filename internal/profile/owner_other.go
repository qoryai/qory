//go:build !unix

package profile

import "os"

// ownedByCurrentUser reports every file as the current user's on a system that has no file
// owner to compare against.
func ownedByCurrentUser(os.FileInfo) bool { return true }
