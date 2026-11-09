//go:build unix && !linux && !darwin

package child

import (
	"errors"
	"time"
)

// startTime reads no start time on a system that qory is not built for.
func startTime(int) (time.Time, error) {
	return time.Time{}, errors.New("this system has no start time of a process that qory reads")
}
