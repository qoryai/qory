//go:build !unix

package config

// ownersOnly accepts every path on a system that has no file owner to compare against.
func ownersOnly([]string) error { return nil }
