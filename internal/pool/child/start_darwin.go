package child

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// startStamp is when the process id started, as the kernel keeps it: p_starttime of
// the kinfo_proc that the sysctl kern.proc.pid returns, as seconds and microseconds,
// sec.usec. It is the same for as long as the process runs. The sysctl returns no
// kinfo_proc for an id that no process has, which x/sys returns as EIO: that is
// [ErrNoProcess].
func startStamp(id int) (string, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", id)
	if errors.Is(err, unix.EIO) {
		return "", fmt.Errorf("process %d does not run: %w", id, ErrNoProcess)
	}
	if err != nil {
		return "", err
	}
	t := kp.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", t.Sec, t.Usec), nil
}
