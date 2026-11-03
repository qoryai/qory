//go:build !unix

package cmd

import "io/fs"

// canWrite is true where no read run happens: a read run needs a wall, and a wall needs
// a Unix host.
func canWrite(string) bool { return true }

// folderOwner returns one user twice: no read run happens here.
func folderOwner(fs.FileInfo) (owner, user int) { return 0, 0 }
