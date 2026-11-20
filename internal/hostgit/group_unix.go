//go:build unix

package hostgit

import (
	"os/exec"
	"syscall"
)

// Group starts cmd in a new process group, and has the context's end kill the
// whole group, so a helper git started stops with it.
func Group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// StopGroup kills what is left of cmd's process group once cmd has exited: a helper
// git started and left running.
func StopGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
