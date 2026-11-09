//go:build unix && !linux && !darwin

package child

import "errors"

// startStamp reads no start of a process on a system that qory is not built for.
func startStamp(int) (string, error) {
	return "", errors.New("this system has no start of a process that qory reads")
}
