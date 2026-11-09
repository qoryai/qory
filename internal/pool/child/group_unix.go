//go:build unix

package child

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// group starts cmd in a new session, and so in a new process group of the same id: a
// signal sent to the program from its terminal reaches the command only through the
// program, which stops it with SIGTERM. Every program that the command starts stays in
// the session, also one in another process group, unless it starts a new session.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// kill sends SIGKILL to cmd's process group, so what it started in the group stops
// with it.
func kill(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// SignalEach sends SIGTERM, or SIGKILL with kill, to each process id of ids, as
// [SessionMembers] lists them. A process that ended since then gets nothing.
func SignalEach(ids []int, kill bool) {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	for _, id := range ids {
		_ = syscall.Kill(id, sig)
	}
}

// Session is the id of this process's session.
func Session() int {
	id, err := unix.Getsid(0)
	if err != nil {
		return -1
	}
	return id
}

// StartStamp is when the process id started, as the kernel keeps it: on Linux, field 22
// of /proc/<id>/stat in clock ticks, and on macOS p_starttime as sec.usec. It is the
// same for as long as the process runs, and another process that gets the id later
// has another one. A process that does not run is [ErrNoProcess].
func StartStamp(id int) (string, error) { return startStamp(id) }

// CheckLeader checks that the process id leads the session id and has the start stamp
// start, as [StartStamp] read it when the process started: a process with another
// stamp, or one that leads no session of that id, got the id after the session that
// start recorded ended. A process that does not run is [ErrNoProcess].
func CheckLeader(id int, start string) error {
	stamp, err := startStamp(id)
	if err != nil {
		return err
	}
	if stamp != start {
		return fmt.Errorf("process %d started at %s, not at %s as recorded, so its id passed to another process", id, stamp, start)
	}
	sid, err := unix.Getsid(id)
	if errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("process %d does not run: %w", id, ErrNoProcess)
	}
	if err != nil {
		return fmt.Errorf("the session of process %d could not be read: %w", id, err)
	}
	if sid != id {
		return fmt.Errorf("process %d is in session %d, so it leads no session %d", id, sid, id)
	}
	return nil
}
