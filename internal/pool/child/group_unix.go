//go:build unix

package child

import (
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
