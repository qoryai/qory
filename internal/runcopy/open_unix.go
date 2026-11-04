//go:build unix

package runcopy

import (
	"os"
	"syscall"
)

// noFollow keeps an open from following a link at the path's last element.
const noFollow = syscall.O_NOFOLLOW

// openNoFollow opens the file at path to read, without following a link at its last
// element, waiting on a pipe there, or making it the terminal of the process.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}
