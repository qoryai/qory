package child

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// SessionMembers is each process in the session id, with its start stamp: each process
// that the sysctl kern.proc.all lists whose session, as getsid returns it, is id, and
// whose start stamp is the same in that list and in a read after getsid. A process
// whose id passed to another one between the reads has two stamps, and is left out,
// as is a process that ends while the list is read.
func SessionMembers(id int) ([]Member, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	var members []Member
	for _, kp := range procs {
		pid := int(kp.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		before := fmt.Sprintf("%d.%06d", kp.Proc.P_starttime.Sec, kp.Proc.P_starttime.Usec)
		if sid, err := unix.Getsid(pid); err != nil || sid != id {
			continue
		}
		if after, err := startStamp(pid); err == nil && after == before {
			members = append(members, Member{ID: pid, Stamp: before})
		}
	}
	return members, nil
}
