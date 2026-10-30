//go:build !unix

package statusfd

import (
	"errors"
	"os"
)

// open returns the error of a system on which qory run writes to no descriptor.
func open(int) (*os.File, error) {
	return nil, errors.New("qory run writes its lines to a descriptor on Linux and macOS only")
}
