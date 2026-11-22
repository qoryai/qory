package codex

import (
	"bytes"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// onDiskCase is p, an existing path with no link in it, with each name in the case that
// the volume has. Codex resolves CODEX_HOME with std::fs::canonicalize, whose realpath(3)
// returns that case on macOS, and filepath.EvalSymlinks keeps the case of p. It is p when
// p cannot be opened.
func onDiskCase(p string) string {
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return p
	}
	defer unix.Close(fd)
	// The pin keeps buf on the heap, at one address, while fcntl writes the path into it.
	buf := make([]byte, unix.PathMax)
	var pin runtime.Pinner
	pin.Pin(&buf[0])
	_, err = unix.FcntlInt(uintptr(fd), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&buf[0]))))
	pin.Unpin()
	if n := bytes.IndexByte(buf, 0); err == nil && n > 0 {
		return string(buf[:n])
	}
	return p
}
