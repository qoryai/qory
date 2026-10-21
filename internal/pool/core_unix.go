//go:build unix

package pool

import (
	"fmt"
	"syscall"
)

// NoCoreDumps sets the process's core file size limit, RLIMIT_CORE, to 0, soft and
// hard, so a crash leaves no image of its memory, which may hold a run key.
func NoCoreDumps() error {
	if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
		return fmt.Errorf("core dumps could not be turned off: %w", err)
	}
	return nil
}
