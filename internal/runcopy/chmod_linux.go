package runcopy

import (
	"errors"
	"strconv"

	"golang.org/x/sys/unix"
)

// chmodFolder sets the folder name in dir to mode 0700, and follows no link. A kernel with no
// fchmodat2 takes the mode through a descriptor opened with O_PATH.
func chmodFolder(dir int, name string) error {
	err := unix.Fchmodat(dir, name, 0o700, unix.AT_SYMLINK_NOFOLLOW)
	if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOSYS) {
		return err
	}
	fd, err := unix.Openat(dir, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fchmodat(unix.AT_FDCWD, "/proc/self/fd/"+strconv.Itoa(fd), 0o700, 0)
}
