//go:build unix

package statusfd

import (
	"os"
	"syscall"
)

// open returns descriptor n as a file, marked close-on-exec, or the error of the check
// that it is open.
func open(n int) (*os.File, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(n, &st); err != nil {
		return nil, err
	}
	syscall.CloseOnExec(n)
	return os.NewFile(uintptr(n), Env), nil
}
