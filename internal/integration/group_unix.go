//go:build unix

package integration

import (
	"os/exec"
	"syscall"
)

// group starts cmd in a process group of its own, and has the context's end kill the
// whole group, so a process the program started stops with it.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// stopGroup kills what is left of cmd's process group once cmd has exited: a process
// the program started and left running.
func stopGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
