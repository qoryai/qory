//go:build !unix

package foragerdir

import (
	"errors"
	"os"
)

// ownedByMe reports every file as the user's on a system with no owner to compare.
func ownedByMe(os.FileInfo) bool { return true }

// ownedByRoot reports no file as root's on a system with no owner to compare.
func ownedByRoot(os.FileInfo) bool { return false }

// errNoLocks is the error of a system without flock.
var errNoLocks = errors.New("this system has no file locks, which the directory of forager.yaml needs")

func flock(*os.File, bool, bool) error { return errNoLocks }

func funlock(*os.File) error { return nil }

const (
	noFollow = 0
	nonBlock = 0
)
