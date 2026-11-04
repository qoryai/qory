//go:build !unix

package runcopy

import "errors"

// CopyBack is not built on a system without openat(2) and O_NOFOLLOW.
func (d Dir) CopyBack(dest string) (CopyBackResult, error) {
	return CopyBackResult{}, errors.New("run qory on Linux or macOS: the copy-back of a plain folder needs openat(2) and O_NOFOLLOW")
}
