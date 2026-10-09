package foragerdir

import "os"

// SetDirOwnedByMe makes MakePrivateDir judge a directory's owner by f, and returns what
// restores the default.
func SetDirOwnedByMe(f func(os.FileInfo) bool) func() {
	dirOwnedByMe = f
	return func() { dirOwnedByMe = ownedByMe }
}
