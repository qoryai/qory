//go:build !unix

package integration

import "os/exec"

// group leaves cmd as it is on a system without process groups: the context's end
// kills the program.
func group(*exec.Cmd) {}

// stopGroup does nothing on a system without process groups.
func stopGroup(*exec.Cmd) {}
