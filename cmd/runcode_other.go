//go:build !unix

package cmd

import (
	"io/fs"
	"os"
)

// openRunCodeFile opens file for reading; [readRunCode] then reads only a regular file.
func openRunCodeFile(file string) (*os.File, error) { return os.Open(file) }

// closeRunCodeFD does nothing: a system outside unix links no descriptor to a file
// path.
func closeRunCodeFD(fs.FileInfo) {}
