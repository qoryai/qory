//go:build unix

package cmd

import (
	"io/fs"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// openRunCodeFile opens file for reading without blocking, so a named pipe that no
// program writes to returns at once; [readRunCode] then reads only a regular file.
func openRunCodeFile(file string) (*os.File, error) {
	return os.OpenFile(file, os.O_RDONLY|unix.O_NONBLOCK, 0)
}

// closeRunCodeFD closes each descriptor of 3 or above that qory inherited open without
// close-on-exec on info, the file of --run-code-file, by its device and inode, when it
// is a regular file. A starter such as qory run pool passes the run code on one, as
// /dev/fd/3, and without the close every program that qory run starts later inherits
// it and can read the code. The comparison finds the descriptor by any path to the
// file, and the path is not opened again: the path may lead elsewhere by then. Go opens
// every file with close-on-exec, so no file that qory opened is closed. A file that is
// not regular, such as a pipe, closes nothing: unless it is stdin, it stops the run as
// input, see [readRunCode]. A nil info closes nothing.
func closeRunCodeFD(info fs.FileInfo) {
	if info == nil || !info.Mode().IsRegular() {
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	for _, n := range openFDs() {
		var fst syscall.Stat_t
		if n < 3 || syscall.Fstat(n, &fst) != nil || fst.Dev != st.Dev || fst.Ino != st.Ino {
			continue
		}
		if flags, err := unix.FcntlInt(uintptr(n), unix.F_GETFD, 0); err != nil || flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		_ = unix.Close(n)
	}
}

// maxFDs bounds the descriptors that [openFDs] returns when /dev/fd cannot be read.
const maxFDs = 1 << 16

// openFDs is the numbers of the process's open descriptors, as /dev/fd lists them, and
// when the list cannot be read, every number below the soft limit of RLIMIT_NOFILE, at
// most maxFDs. A number may be closed by the time it is used, and the list contains
// the descriptor that read it.
func openFDs() []int {
	var fds []int
	names, err := readDirNames("/dev/fd")
	if err != nil {
		limit := uint64(1024)
		var rl unix.Rlimit
		if unix.Getrlimit(unix.RLIMIT_NOFILE, &rl) == nil {
			limit = min(rl.Cur, maxFDs)
		}
		for n := range int(limit) {
			fds = append(fds, n)
		}
		return fds
	}
	for _, name := range names {
		if n, err := strconv.Atoi(name); err == nil {
			fds = append(fds, n)
		}
	}
	return fds
}

// readDirNames is the names in the folder dir.
func readDirNames(dir string) ([]string, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.Readdirnames(-1)
}
