//go:build unix

package runtimeinstall

import (
	"errors"
	"os"
	"syscall"
)

// errHeld is the error of a lock that another open file holds, when flock does not wait.
var errHeld = errors.New("the lock is held")

// flock takes the lock of f, shared or exclusive, waiting for it unless nowait; nowait
// on a lock another open file holds is errHeld.
func flock(f *os.File, exclusive, nowait bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if nowait {
		how |= syscall.LOCK_NB
	}
	for {
		err := syscall.Flock(int(f.Fd()), how)
		switch err {
		case syscall.EINTR:
			continue
		case syscall.EWOULDBLOCK:
			return errHeld
		}
		return err
	}
}

// noFollow is the flag that opens a file without following a link.
const noFollow = syscall.O_NOFOLLOW
