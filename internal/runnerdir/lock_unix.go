//go:build unix

package runnerdir

import (
	"os"
	"syscall"
)

// ownedByMe reports whether the effective user owns the file.
func ownedByMe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// flock takes the lock of an open file, shared or exclusive, waiting for it unless
// nowait; nowait on a lock another holds is errWouldBlock.
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
			return errWouldBlock
		}
		return err
	}
}

// funlock releases the lock of an open file.
func funlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// The flags that open a file without following a link, and without waiting on a pipe.
const (
	noFollow = syscall.O_NOFOLLOW
	nonBlock = syscall.O_NONBLOCK
)
