package child

import (
	"time"

	"golang.org/x/sys/unix"
)

// startTime is when the process id started, p_starttime of the kinfo_proc that the
// sysctl kern.proc.pid returns.
func startTime(id int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", id)
	if err != nil {
		return time.Time{}, err
	}
	t := kp.Proc.P_starttime
	return time.Unix(int64(t.Sec), int64(t.Usec)*1000), nil
}
