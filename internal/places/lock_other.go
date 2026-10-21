//go:build !unix

package places

import (
	"errors"
	"os"
)

// noFollow is no flag on a system without O_NOFOLLOW.
const noFollow = 0

// lock fails on a system without flock: the record needs a lock.
func lock(*os.File, bool, bool) error {
	return errors.New("this system has no file locks, which the record of a walled run's places needs")
}

// unlock releases nothing on a system without flock.
func unlock(*os.File) {}
