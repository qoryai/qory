//go:build unix

package child

import (
	"fmt"
	"os"
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
// [SessionMembers] listed them for the session session. Just before each signal, the
// process's session is read again, so a process that ended since the list, and one
// that got its id since then in another session, gets nothing. It sends nothing for a
// session of 1 or less or the session of this process, and nothing to an id of 1 or
// less or this process's id.
func SignalEach(ids []int, session int, kill bool) {
	if session <= 1 || session == Session() {
		return
	}
	sig := unix.SIGTERM
	if kill {
		sig = unix.SIGKILL
	}
	for _, id := range ids {
		if id <= 1 || id == os.Getpid() {
			continue
		}
		signalMember(id, session, sig)
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

// CheckLeader checks that the process id has the start stamp start, as [StartStamp]
// read it when the process started as the leader of the session id: a process with
// another stamp got the id after that session ended. A session's leader cannot leave
// the session, so a process with that stamp still leads it. A process that does not run
// is [ErrNoProcess].
func CheckLeader(id int, start string) error {
	stamp, err := startStamp(id)
	if err != nil {
		return err
	}
	if stamp != start {
		return fmt.Errorf("process %d started at %s, not at %s as recorded, so its id passed to another process", id, stamp, start)
	}
	return nil
}
