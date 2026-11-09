//go:build unix && !linux

package child

import (
	"os"

	"golang.org/x/sys/unix"
)

// signalMember sends sig to the process id while it is in the session session, as
// getsid returns it just before kill.
func signalMember(id, session int, sig unix.Signal) {
	if sid, err := unix.Getsid(id); err == nil && sid == session {
		_ = unix.Kill(id, sig)
	}
}

// HasOpen lists no process on a system whose open files of a process qory does not
// read: [OpenFilesRead] is false.
func HasOpen([]int, os.FileInfo) []int { return nil }

// OpenFilesRead reports whether [HasOpen] reads the open files of processes on this
// system.
const OpenFilesRead = false
