package child

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
)

// startStamp is when the process id started, as the kernel keeps it: starttime, field
// 22 of /proc/<id>/stat, in clock ticks since boot, as written there. It is the same
// for as long as the process runs, and no clock step changes it. A process that does
// not run is [ErrNoProcess].
func startStamp(id int) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(id) + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("process %d does not run: %w", id, ErrNoProcess)
	}
	if err != nil {
		return "", err
	}
	return statField(b, 22)
}
