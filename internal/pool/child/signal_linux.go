package child

import (
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// signalMember sends sig to the process id while it is in the session session. A pidfd
// of the process, which pidfd_open opens, keeps referring to that process even when
// its id passes to another, so the session is read after the open and the signal goes
// through the pidfd. When pidfd_open fails with another error than ESRCH, as on a
// kernel without it or where a filter blocks it, the session is read again just
// before kill.
func signalMember(id, session int, sig unix.Signal) {
	fd, err := unix.PidfdOpen(id, 0)
	if err == nil {
		defer unix.Close(fd)
		if _, ok := statInSession(id, session); ok {
			_ = unix.PidfdSendSignal(fd, sig, nil, 0)
		}
		return
	}
	if errors.Is(err, unix.ESRCH) {
		return
	}
	if _, ok := statInSession(id, session); ok {
		_ = unix.Kill(id, sig)
	}
}

// HasOpen is the process id of each member of members, of the session session, that
// has the file info open, as the links in /proc/<pid>/fd lead to it, matched by device
// and inode. After the match, the process's session and start stamp are read again,
// and a process with another session or stamp than the member's is left out, as is a
// process whose descriptors cannot be read.
func HasOpen(members []Member, session int, info os.FileInfo) []int {
	var open []int
	for _, m := range members {
		dir := "/proc/" + strconv.Itoa(m.ID) + "/fd"
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if fi, err := os.Stat(dir + "/" + fd.Name()); err == nil && os.SameFile(fi, info) {
				if stamp, ok := statInSession(m.ID, session); ok && stamp == m.Stamp {
					open = append(open, m.ID)
				}
				break
			}
		}
	}
	return open
}

// OpenFilesRead reports whether [HasOpen] reads the open files of processes on this
// system.
const OpenFilesRead = true
