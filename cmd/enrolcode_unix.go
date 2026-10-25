//go:build unix

package cmd

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// openEnrolmentCode opens file for reading without following a link and without
// blocking, so a named pipe that no program writes to returns at once.
func openEnrolmentCode(file string) (*os.File, error) {
	return os.OpenFile(file, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}

// linkRefused reports whether err is the open of a link that [openEnrolmentCode] does
// not follow.
func linkRefused(err error) bool { return errors.Is(err, unix.ELOOP) }
