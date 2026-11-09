//go:build !unix

package child

import (
	"errors"
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

// ProcessGroup is -1 on a system without process groups: no id is a group's.
func ProcessGroup() int { return -1 }

// StartStamp reads no start on a system without process groups.
func StartStamp(int) (string, error) {
	return "", errors.New("this system has no process groups")
}

// CheckLeader fails on a system without process groups, so no group is signalled.
func CheckLeader(int, string) error {
	return errors.New("this system has no process groups")
}
