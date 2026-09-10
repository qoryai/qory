//go:build !unix

package profile

import "os"

// OwnedByCurrentUser reports every file as the current user's on a system that has no file
// owner to compare against.
func OwnedByCurrentUser(os.FileInfo) bool { return true }
