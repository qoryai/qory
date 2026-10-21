package main

import "golang.org/x/sys/unix"

// isLinux is true on Linux, where the process has a dumpable flag.
const isLinux = true

// dumpable is PR_GET_DUMPABLE for this process, or -1 when reading it fails.
func dumpable() int {
	v, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		return -1
	}
	return v
}
