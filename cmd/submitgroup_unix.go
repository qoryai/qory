//go:build unix

package cmd

import (
	"os/exec"
	"syscall"
)

// inOwnGroup starts cmd in a new session, which has no controlling terminal and is a process
// group of its own, and has the context's end kill the whole group, so a helper that the program
// started stops with it.
func inOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// killGroup kills what is left of cmd's process group once cmd has exited: a helper that the
// program started and left running.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
