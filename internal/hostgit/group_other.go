//go:build !unix

package hostgit

import "os/exec"

// Group leaves cmd as it is on a system without process groups: the context's end
// kills git.
func Group(*exec.Cmd) {}

// StopGroup does nothing on a system without process groups.
func StopGroup(*exec.Cmd) {}
