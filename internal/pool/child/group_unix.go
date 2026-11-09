//go:build unix

package child

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
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

// LeaderSince checks that the process id leads the process group id and started no
// later than since: a process that started later, or that leads no group of that id,
// took the id over from a group that is gone. Its error is for each failed check, and
// for a start time that cannot be read.
func LeaderSince(id int, since time.Time) error {
	g, err := syscall.Getpgid(id)
	if err != nil {
		return fmt.Errorf("process %d does not run: %w", id, err)
	}
	if g != id {
		return fmt.Errorf("process %d is in process group %d, so it leads no group %d", id, g, id)
	}
	started, err := startTime(id)
	if err != nil {
		return fmt.Errorf("the start time of process %d could not be read: %w", id, err)
	}
	if started.After(since) {
		return fmt.Errorf("process %d started at %s, after %s, so it is not the one recorded", id, started.UTC().Format(time.RFC3339Nano), since.UTC().Format(time.RFC3339Nano))
	}
	return nil
}
