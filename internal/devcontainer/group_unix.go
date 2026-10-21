//go:build unix

package devcontainer

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// group starts cmd in a new process group, and has the context's end send SIGTERM to
// the whole group, so a process the command started is asked to stop with it. This is
// group of internal/preparation, with SIGTERM in place of SIGKILL: [endGroup] kills
// what is left.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

// endGroup waits until deadline for cmd's process group to hold no process, then
// kills what is left of it.
func endGroup(cmd *exec.Cmd, deadline time.Time) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
