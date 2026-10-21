//go:build unix

package preparation

import (
	"os"
	"syscall"
)

// openDir opens the directory at path without following a link at its last name.
func openDir(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
}
