//go:build unix

package child

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// group starts cmd in a new process group: a signal sent to the program from its
// terminal reaches the command only through the program, which stops it with SIGTERM.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// kill sends SIGKILL to cmd's process group, so what it started stops with it.
func kill(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// SignalGroup sends sig to the process group id, as [Command.Group] reported it: to the
// command and every program it started in its group. kill sends SIGKILL, and SIGTERM
// otherwise.
func SignalGroup(id int, kill bool) error {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	return syscall.Kill(-id, sig)
}

// ProcessGroup is the id of this process's process group.
func ProcessGroup() int { return syscall.Getpgrp() }

// StartStamp is when the process id started, as the kernel keeps it: on Linux, field 22
// of /proc/<id>/stat in clock ticks, and on macOS p_starttime as sec.usec. It is the
// same for as long as the process runs, and another process that gets the id later
// has another one. A process that does not run is [ErrNoProcess].
func StartStamp(id int) (string, error) { return startStamp(id) }

// CheckLeader checks that the process id leads the process group id and has the start
// stamp start, as [StartStamp] read it when the process started: a process with
// another stamp, or one that leads no group of that id, got the id after the group
// that start recorded ended. A process that does not run is [ErrNoProcess].
func CheckLeader(id int, start string) error {
	g, err := syscall.Getpgid(id)
	if errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("process %d does not run: %w", id, ErrNoProcess)
	}
	if err != nil {
		return fmt.Errorf("the process group of process %d could not be read: %w", id, err)
	}
	if g != id {
		return fmt.Errorf("process %d is in process group %d, so it leads no group %d", id, g, id)
	}
	stamp, err := startStamp(id)
	if err != nil {
		return err
	}
	if stamp != start {
		return fmt.Errorf("process %d started at %s, not at %s as recorded, so its id passed to another process", id, stamp, start)
	}
	return nil
}
