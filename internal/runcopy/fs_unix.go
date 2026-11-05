//go:build unix

package runcopy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// dirFlags open a folder without following a link at its name.
const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// fileFlags open a file to read without following a link at its name, waiting on a pipe
// there, or making it the terminal of the process.
const fileFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_CLOEXEC

// maxOpenFolders is how deep a walk of a run's copy goes, with two folders open per
// level. 64 levels keep the open files far below the limit of a process.
const maxOpenFolders = 64

// openNoFollow opens the file at path to read, as [fileFlags] has it.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, fileFlags, 0)
}

// openFileAt opens the file name in the folder dir to read, as [fileFlags] has it.
func openFileAt(dir int, name, rel string) (*os.File, error) {
	fd, err := unix.Openat(dir, name, fileFlags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: rel, Err: err}
	}
	return os.NewFile(uintptr(fd), rel), nil
}

// lookup is what the path rel, with forward slashes, is under the folder root: its
// folder open, the name in it, and errNoEntry when a part of it does not exist or a
// part above its name is a link or no folder. It follows no link. The caller closes
// the folder.
func lookup(root int, rel string) (int, string, error) {
	parts := strings.Split(rel, "/")
	dir, err := unix.Dup(root)
	if err != nil {
		return -1, "", err
	}
	for _, name := range parts[:len(parts)-1] {
		next, err := unix.Openat(dir, name, dirFlags, 0)
		unix.Close(dir)
		switch {
		case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
			return -1, "", errNoEntry
		case err != nil:
			return -1, "", &os.PathError{Op: "open", Path: rel, Err: err}
		}
		dir = next
	}
	return dir, parts[len(parts)-1], nil
}

// errNoEntry is a path [lookup] does not reach.
var errNoEntry = errors.New("no entry")

// readNames is the names in the folder dir, sorted. dir stays open.
func readNames(dir int) ([]string, error) {
	fd, err := unix.Dup(dir)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "")
	defer f.Close()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	names, err := f.Readdirnames(-1)
	slices.Sort(names)
	return names, err
}

// Remove deletes the run's folder: the copy, the bundle, the bare sources and the
// quarantines. A run can leave folders without the write bit, so when the first pass
// fails, Remove opens each folder with O_NOFOLLOW, sets it to mode 0700 with fchmod,
// and deletes again. It follows no link.
func (d Dir) Remove() error {
	if d.root == "" {
		return nil
	}
	if err := os.RemoveAll(d.root); err == nil {
		return nil
	}
	fd, err := unix.Open(d.root, dirFlags, 0)
	if err != nil {
		return fileErr(d.root, err)
	}
	err = writable(fd, d.root, 0)
	unix.Close(fd)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(d.root); err != nil {
		return fileErr(d.root, err)
	}
	return nil
}

// writable sets the folder dir, at path, and every folder below it to mode 0700.
func writable(dir int, path string, depth int) error {
	if depth > maxOpenFolders {
		return fmt.Errorf(textTooDeep, shown(path), maxOpenFolders)
	}
	if err := unix.Fchmod(dir, 0o700); err != nil {
		return fileErr(path, err)
	}
	names, err := readNames(dir)
	if err != nil {
		return fileErr(path, err)
	}
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		sub, err := unix.Openat(dir, name, dirFlags, 0)
		if errors.Is(err, unix.EACCES) {
			// A folder with no read bit: the open needs the bit, and the chmod follows no link.
			if err := chmodFolder(dir, name); err != nil {
				return fileErr(path+"/"+name, err)
			}
			sub, err = unix.Openat(dir, name, dirFlags, 0)
		}
		if err != nil {
			return fileErr(path+"/"+name, err)
		}
		err = writable(sub, path+"/"+name, depth+1)
		unix.Close(sub)
		if err != nil {
			return err
		}
	}
	return nil
}
