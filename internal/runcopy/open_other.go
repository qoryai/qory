//go:build !unix

package runcopy

import (
	"fmt"
	"os"
)

// noFollow is no flag on a system without O_NOFOLLOW.
const noFollow = 0

// openNoFollow opens the file at path to read after a check that it is no link. On a
// system without O_NOFOLLOW the check and the open are two steps.
func openNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a link", shown(path))
	}
	return os.Open(path)
}
