//go:build !unix

package cmd

// canWrite is true where no read run happens: a read run needs a wall, and a wall needs
// a Unix host.
func canWrite(string) bool { return true }
