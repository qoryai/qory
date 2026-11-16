//go:build !unix

package runtimeinstall

import (
	"errors"
	"os"
)

// errHeld is the error of a lock that another open file holds.
var errHeld = errors.New("the lock is held")

// flock fails on a system without flock: qory installs a runtime on Linux and macOS.
func flock(*os.File, bool, bool) error {
	return errors.New("This system has no file locks, which the install of a runtime needs")
}

// noFollow is no flag on a system without O_NOFOLLOW.
const noFollow = 0
