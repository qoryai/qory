package child

import "golang.org/x/sys/unix"

// SessionMembers is the id of each process in the session id: each process that the
// sysctl kern.proc.all lists whose session, as getsid returns it, is id. A process
// that ends while the list is read is left out.
func SessionMembers(id int) ([]int, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, kp := range procs {
		pid := int(kp.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		if sid, err := unix.Getsid(pid); err == nil && sid == id {
			ids = append(ids, pid)
		}
	}
	return ids, nil
}
