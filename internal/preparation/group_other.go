//go:build !unix

package preparation

import "os/exec"

// group leaves cmd as it is on a system without process groups: the context's end
// kills git.
func group(*exec.Cmd) {}

// stopGroup does nothing on a system without process groups.
func stopGroup(*exec.Cmd) {}
