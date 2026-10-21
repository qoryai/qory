//go:build !unix

package pool

// NoCoreDumps does nothing where there is no RLIMIT_CORE.
func NoCoreDumps() error { return nil }
