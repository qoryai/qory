//go:build unix

package runcopy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
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

// disarmedSuffix goes after the name of what [Dir.Disarm] renames.
const disarmedSuffix = "-from-run"

// Disarm renames each .git in the copy and in the bundle folder to .git-from-run, mode 0700,
// and each entry that makes a folder a git folder, as [gitDirLike] reads it, the same way, so
// a git in a copy that stays finds no repository and runs nothing the run planted. It follows
// no link: it opens each folder from the one above it with O_NOFOLLOW, and a link named .git
// is renamed as a link. Call it once the run's containers are gone.
func (d Dir) Disarm() error {
	for _, top := range []string{d.Work(), d.Bundle()} {
		fd, err := unix.Open(top, dirFlags, 0)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return fileErr(top, err)
		}
		err = disarm(fd, top, 0)
		unix.Close(fd)
		if err != nil {
			return err
		}
	}
	return nil
}

// disarm is [Dir.Disarm] of the folder dir at path.
func disarm(dir int, path string, depth int) error {
	if depth > maxOpenFolders {
		return fmt.Errorf(textTooDeep, shown(path), maxOpenFolders)
	}
	names, err := readNames(dir)
	if err != nil {
		return fileErr(path, err)
	}
	like := gitDirLike(names)
	for _, name := range names {
		at := path + "/" + name
		switch f := folded(name); {
		case f == folded(".git"):
			if err := renameOut(dir, name, at); err != nil {
				return err
			}
			continue
		case like && (f == "head" || f == "commondir"):
			if err := renameOut(dir, name, at); err != nil {
				return err
			}
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		sub, err := unix.Openat(dir, name, dirFlags, 0)
		if errors.Is(err, unix.EACCES) {
			if err := chmodFolder(dir, name); err != nil {
				return fileErr(at, err)
			}
			sub, err = unix.Openat(dir, name, dirFlags, 0)
		}
		if err != nil {
			return fileErr(at, err)
		}
		err = disarm(sub, at, depth+1)
		unix.Close(sub)
		if err != nil {
			return err
		}
	}
	return nil
}

// renameOut renames the entry name in dir to a free name with [disarmedSuffix], and sets a
// folder to mode 0700. It follows no link.
func renameOut(dir int, name, at string) error {
	to := strings.ToLower(name) + disarmedSuffix
	for i := 1; ; i++ {
		var st unix.Stat_t
		if err := unix.Fstatat(dir, to, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
			break
		}
		to = strings.ToLower(name) + disarmedSuffix + "-" + strconv.Itoa(i)
	}
	if err := unix.Renameat(dir, name, dir, to); err != nil {
		return fileErr(at, err)
	}
	var st unix.Stat_t
	if err := unix.Fstatat(dir, to, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR {
		if err := chmodFolder(dir, to); err != nil {
			return fileErr(at, err)
		}
	}
	return nil
}
