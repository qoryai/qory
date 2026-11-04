//go:build !unix

package runcopy

import (
	"context"
	"errors"
)

// SetupStates is not built on a system without openat(2) and O_NOFOLLOW.
func (d Dir) SetupStates(ctx context.Context, checkout string, paths []string) ([]SetupPath, error) {
	return nil, errors.New("run qory on Linux or macOS: the record of the setup's paths needs openat(2) and O_NOFOLLOW")
}
