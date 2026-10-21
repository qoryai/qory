//go:build !unix

package child

import "os/exec"

// group leaves cmd as it is on a system without process groups.
func group(*exec.Cmd) {}

// kill kills cmd on a system without process groups.
func kill(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
