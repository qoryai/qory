//go:build !unix

package preparation

import "os"

// openDir opens the directory at path: the agent's home is made in a Linux container,
// and elsewhere only the tests make one.
func openDir(path string) (*os.File, error) { return os.Open(path) }
