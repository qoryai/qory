//go:build unix

package machinestep

import "syscall"

// openFlags keep the open of a FIFO or a device at the file's path from waiting, and
// keep a terminal there from becoming the process's.
const openFlags = syscall.O_NONBLOCK | syscall.O_NOCTTY
