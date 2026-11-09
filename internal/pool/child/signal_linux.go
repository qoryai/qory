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
// through the pidfd. On a kernel without pidfd_open, ENOSYS, or where a filter blocks
// it, EPERM, the session is read again just before kill.
func signalMember(id, session int, sig unix.Signal) {
	fd, err := unix.PidfdOpen(id, 0)
	if err == nil {
		defer unix.Close(fd)
		if inSession(id, session) {
			_ = unix.PidfdSendSignal(fd, sig, nil, 0)
		}
		return
	}
	if (errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM)) && inSession(id, session) {
		_ = unix.Kill(id, sig)
	}
}

// inSession reports whether field 6 of /proc/<id>/stat, the session, is session.
func inSession(id, session int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(id) + "/stat")
	if err != nil {
		return false
	}
	sid, err := statField(b, 6)
	return err == nil && sid == strconv.Itoa(session)
}

// HasOpen reports which process ids of ids have the file info open, as the links in
// /proc/<pid>/fd lead to it, matched by device and inode. A process whose descriptors
// cannot be read, or that ended, is left out.
func HasOpen(ids []int, info os.FileInfo) []int {
	var open []int
	for _, id := range ids {
		dir := "/proc/" + strconv.Itoa(id) + "/fd"
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if fi, err := os.Stat(dir + "/" + fd.Name()); err == nil && os.SameFile(fi, info) {
				open = append(open, id)
				break
			}
		}
	}
	return open
}

// OpenFilesRead reports whether [HasOpen] reads the open files of processes on this
// system.
const OpenFilesRead = true
