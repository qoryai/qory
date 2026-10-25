//go:build !unix

package cmd

import (
	"errors"
	"io/fs"
	"os"
)

// openEngineProxyFile stops: a system outside unix has no way to open a file without
// following a link, which [readEngineProxy] needs.
func openEngineProxyFile(string) (*os.File, error) {
	return nil, errors.New("wall.engine_proxy_file needs a unix system")
}

// engineProxyLink reports false: [openEngineProxyFile] opens nothing.
func engineProxyLink(error) bool { return false }

// fileOwner is false: a file outside unix has no uid.
func fileOwner(fs.FileInfo) (int, bool) { return 0, false }
