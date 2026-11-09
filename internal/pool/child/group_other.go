//go:build !unix

package child

import (
	"os"
	"os/exec"
)

// group leaves cmd as it is on a system without process groups.
func group(*exec.Cmd) {}

// kill kills cmd on a system without process groups.
func kill(cmd *exec.Cmd) { _ = cmd.Process.Kill() }

// SignalGroup kills the process id on a system without process groups, when kill is
// set; without kill it sends nothing, since such a system has no SIGTERM to send.
func SignalGroup(id int, kill bool) error {
	if !kill {
		return nil
	}
	p, err := os.FindProcess(id)
	if err != nil {
		return err
	}
	return p.Kill()
}
