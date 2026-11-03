//go:build unix

package cmd

import "golang.org/x/sys/unix"

// canWrite reports whether this process may create files in dir. A read-only mount fails
// the check for every user, root included.
func canWrite(dir string) bool {
	return unix.Access(dir, unix.W_OK) == nil
}
