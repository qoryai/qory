//go:build !unix

package cmd

import "os/exec"

// inOwnGroup leaves cmd as it is on a system without process groups: the context's end kills
// the program.
func inOwnGroup(*exec.Cmd) {}

// killGroup does nothing on a system without process groups.
func killGroup(*exec.Cmd) {}
