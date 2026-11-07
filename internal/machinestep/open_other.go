//go:build !unix

package machinestep

// openFlags are no flags on a system without O_NONBLOCK and O_NOCTTY: the check after
// the open still reads a regular file alone.
const openFlags = 0
