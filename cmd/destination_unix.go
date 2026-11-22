//go:build unix

package cmd

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockedRecord opens the record of qory destination clone at path for reading and
// appending, mode 0600, never through a link, and waits for its exclusive lock, which
// the file's close releases.
func lockedRecord(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
