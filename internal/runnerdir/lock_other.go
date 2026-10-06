//go:build !unix

package runnerdir

import (
	"errors"
	"os"
)

// ownedByMe reports every file as the user's on a system with no owner to compare.
func ownedByMe(os.FileInfo) bool { return true }

// errNoLocks is the error of a system without flock.
var errNoLocks = errors.New("this system has no file locks, which the runner file's directory needs")

func flock(*os.File, bool, bool) error { return errNoLocks }

func funlock(*os.File) error { return nil }

const (
	noFollow = 0
	nonBlock = 0
)
