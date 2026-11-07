//go:build unix && !linux

package runcopy

import "golang.org/x/sys/unix"

// chmodFolder sets the folder name in dir to mode 0700, and follows no link.
func chmodFolder(dir int, name string) error {
	return unix.Fchmodat(dir, name, 0o700, unix.AT_SYMLINK_NOFOLLOW)
}
