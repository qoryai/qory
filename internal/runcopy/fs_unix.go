//go:build unix

package runcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// maxRenameTries is how many names [renameOut] tries in one folder.
const maxRenameTries = 100

// maxDisarmErrors is how many errors [Dir.Disarm] returns, so the line it goes in stays short.
const maxDisarmErrors = 5

// Disarm renames each .git in the copy and in the bundle folder to .git-from-run, and each
// entry that makes a folder a git folder, as [gitDirLike] reads it, the same way. It sets every
// folder it enters to mode 0700, and enters what it renamed. A git in such a folder finds no
// repository and runs nothing the run planted, as long as no container of the run is left: a
// live container can write a new .git. It follows no link: it opens each folder from the one
// above it with O_NOFOLLOW, and a link named .git is renamed as a link. An error does not end
// the walk, so a folder it cannot enter leaves the rest renamed. ctx ends the walk. Call it once
// the run's containers are gone.
func (d Dir) Disarm(ctx context.Context) error {
	var errs []error
	for _, top := range []string{d.Work(), d.Bundle()} {
		fd, err := openTop(top)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			errs = append(errs, fileErr(top, err))
			continue
		}
		disarm(ctx, fd, top, 0, false, &errs)
		unix.Close(fd)
	}
	return errors.Join(errs[:min(len(errs), maxDisarmErrors)]...)
}

// openTop opens the folder at top, which the run could chmod. A folder with no read bit is set to
// the mode that [disarm] sets on a folder below it.
func openTop(top string) (int, error) {
	fd, err := unix.Open(top, dirFlags, 0)
	if !errors.Is(err, unix.EACCES) {
		return fd, err
	}
	parent, err := unix.Open(filepath.Dir(top), dirFlags, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(parent)
	if err := chmodFolder(parent, filepath.Base(top)); err != nil {
		return -1, err
	}
	return unix.Openat(parent, filepath.Base(top), dirFlags, 0)
}

// disarm is the walk of [Dir.Disarm] in the folder dir, at path. gitDir is whether the folder is
// a .git that the walk renamed, where HEAD and commondir go whatever else the folder has. It
// adds each error to errs.
func disarm(ctx context.Context, dir int, path string, depth int, gitDir bool, errs *[]error) {
	if err := ctx.Err(); err != nil {
		if !slices.ContainsFunc(*errs, func(e error) bool { return errors.Is(e, err) }) {
			*errs = append(*errs, err)
		}
		return
	}
	if depth > maxOpenFolders {
		*errs = append(*errs, fmt.Errorf(textTooDeep, shown(path), maxOpenFolders))
		return
	}
	// A failed chmod leaves the folder as it was: the reads below show what that stops.
	if err := unix.Fchmod(dir, 0o700); err != nil {
		*errs = append(*errs, fileErr(path, err))
	}
	names, err := readNames(dir)
	if err != nil {
		*errs = append(*errs, fileErr(path, err))
		return
	}
	like := gitDir || gitDirLike(names)
	for _, name := range names {
		at := path + "/" + name
		entry, isGit := name, false
		switch f := folded(name); {
		case f == ".git":
			isGit = true
			fallthrough
		case like && (f == "head" || f == "commondir"):
			if entry, err = renameOut(dir, name, f, at); err != nil {
				*errs = append(*errs, err)
				continue
			}
		}
		var st unix.Stat_t
		if err := unix.Fstatat(dir, entry, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			*errs = append(*errs, fileErr(at, err))
			continue
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		sub, err := unix.Openat(dir, entry, dirFlags, 0)
		if errors.Is(err, unix.EACCES) {
			// A folder with no read bit: the open needs the bit, and the chmod follows no link.
			if err := chmodFolder(dir, entry); err != nil {
				*errs = append(*errs, fileErr(at, err))
				continue
			}
			sub, err = unix.Openat(dir, entry, dirFlags, 0)
		}
		if err != nil {
			*errs = append(*errs, fileErr(at, err))
			continue
		}
		disarm(ctx, sub, path+"/"+entry, depth+1, isGit, errs)
		unix.Close(sub)
	}
}

// renameOut renames the entry name of the folder dir, at at, to base with [disarmedSuffix], or
// with a number after that when the name is taken, and returns the new name. base is the plain
// form of name, as [folded] has it, so a long name that folds to .git fits.
func renameOut(dir int, name, base, at string) (string, error) {
	base += disarmedSuffix
	to := base
	for i := 1; ; i++ {
		var st unix.Stat_t
		err := unix.Fstatat(dir, to, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			break
		}
		if err != nil {
			return "", fileErr(at, err)
		}
		if i > maxRenameTries {
			return "", fmt.Errorf(textNoFreeName, shown(at), maxRenameTries+1)
		}
		to = base + "-" + strconv.Itoa(i)
	}
	if err := unix.Renameat(dir, name, dir, to); err != nil {
		return "", fileErr(at, err)
	}
	return to, nil
}
