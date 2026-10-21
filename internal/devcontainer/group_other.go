//go:build !unix

package devcontainer

import (
	"os/exec"
	"time"
)

// group leaves cmd as it is on a system without process groups: the context's end
// kills the command's process.
func group(*exec.Cmd) {}

// endGroup does nothing on a system without process groups.
func endGroup(*exec.Cmd, time.Time) {}
