//go:build unix

package testenv

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
	"time"
)

// Gone reports whether process pid ends within 2 seconds, see [Ended].
func Gone(pid int) bool {
	for range 40 {
		if Ended(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// Ended reports whether pid has no process, or only a zombie's entry. On Linux, the new
// parent of an orphan, such as PID 1 of a container, may leave it a zombie for a while,
// and kill(pid, 0) succeeds on a zombie; /proc/<pid>/stat has its state, Z. Where
// /proc has no stat file, as on macOS, kill(pid, 0) alone decides.
func Ended(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the program's name, in parentheses that may contain ") ".
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) {
		return false
	}
	return stat[i+2] == 'Z' || stat[i+2] == 'X'
}
