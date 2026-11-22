//go:build !unix

package cmd

import "os"

// lockedRecord opens the record of qory destination clone at path for reading and
// appending, mode 0600. A system that is not Unix has no wall, so no call runs beside
// another, and the record has no lock.
func lockedRecord(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
}
