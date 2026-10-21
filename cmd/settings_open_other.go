//go:build !unix

package cmd

import (
	"os"
	"path/filepath"
	"strings"
)

// openUnder opens the regular file rel under the directory home, following no link: the
// home and each path below it are read as they are, without following one, before the
// file is opened. A link is [errLinked], anything but a regular file [errNotRegular].
func openUnder(home, rel string) (*os.File, error) {
	path := home
	parts := append([]string{""}, strings.Split(filepath.ToSlash(rel), "/")...)
	for i, part := range parts {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errLinked
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() || i < len(parts)-1 && !info.IsDir() {
			return nil, errNotRegular
		}
	}
	return os.Open(path)
}
