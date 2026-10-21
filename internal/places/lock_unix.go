//go:build unix

package places

import (
	"os"
	"syscall"
)

// noFollow keeps a lock file from being opened through a link.
const noFollow = syscall.O_NOFOLLOW

// lock takes the lock of an open file, shared or exclusive, waiting for it unless
// nowait; nowait on a lock another holds is errHeld.
func lock(f *os.File, exclusive, nowait bool) error {
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

// unlock releases the lock of an open file.
func unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
