//go:build unix

package runcopy

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"

	"golang.org/x/sys/unix"
)

// dirFlags open a folder without following a link at its name.
const dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// fileFlags open a file to read without following a link at its name, waiting on a pipe
// there, or making it the terminal of the process.
const fileFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_CLOEXEC

// CopyBack copies the new and changed regular files of the copy of a plain folder,
// [Dir.Work], into dest, the person's folder. Call it once the run's containers are
// gone. It leaves out every link, every .git and .qory at any depth, and every file
// that is not regular, and follows no link on any part of a path, in the copy or in
// dest: it opens each folder from the one above it with O_NOFOLLOW. A folder that is a
// link or no folder in dest is left out with everything in it. It deletes nothing in
// dest. A changed file keeps the permission bits it has in dest, and a new one gets the
// process's umask, with the execute bits when the copy has one.
func (d Dir) CopyBack(dest string) (CopyBackResult, error) {
	src, err := unix.Open(d.Work(), dirFlags, 0)
	if err != nil {
		return CopyBackResult{}, &os.PathError{Op: "open", Path: d.Work(), Err: err}
	}
	defer unix.Close(src)
	dst, err := unix.Open(dest, dirFlags, 0)
	if err != nil {
		return CopyBackResult{}, fmt.Errorf("give the folder the run started in by a path with no link at its end: qory copies the run's files back there, and the open of %s failed: %w", shown(dest), err)
	}
	defer unix.Close(dst)
	var r CopyBackResult
	err = copyBackDir(src, dst, "", &r)
	return r, err
}

// copyBackDir copies back the entries of the folder src, at rel, into the folder dst.
func copyBackDir(src, dst int, rel string, r *CopyBackResult) error {
	names, err := readNames(src)
	if err != nil {
		return err
	}
	for _, name := range names {
		at := path.Join(rel, name)
		if leftOutName(name) {
			r.Skipped = append(r.Skipped, at)
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(src, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return &os.PathError{Op: "lstat", Path: at, Err: err}
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := copyBackSub(src, dst, name, at, r); err != nil {
				return err
			}
		case unix.S_IFREG:
			if err := copyBackFile(src, dst, name, at, uint32(st.Mode), r); err != nil {
				return err
			}
		default:
			r.Skipped = append(r.Skipped, at)
		}
	}
	return nil
}

// copyBackSub copies back the folder name in src, at rel, into the folder of that name
// in dst, which it makes when there is none.
func copyBackSub(src, dst int, name, rel string, r *CopyBackResult) error {
	sub, err := unix.Openat(src, name, dirFlags, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: rel, Err: err}
	}
	defer unix.Close(sub)
	if err := unix.Mkdirat(dst, name, 0o777); err != nil && !errors.Is(err, unix.EEXIST) {
		return &os.PathError{Op: "mkdir", Path: rel, Err: err}
	}
	to, err := unix.Openat(dst, name, dirFlags, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		// A link or a file of the person's stands there.
		r.Skipped = append(r.Skipped, rel)
		return nil
	}
	if err != nil {
		return &os.PathError{Op: "open", Path: rel, Err: err}
	}
	defer unix.Close(to)
	return copyBackDir(sub, to, rel, r)
}

// copyBackFile copies the regular file name in src, at rel, with mode, into dst, when
// dst has no entry of that name or a regular file with other content.
func copyBackFile(src, dst int, name, rel string, mode uint32, r *CopyBackResult) error {
	in, err := openFileAt(src, name, rel)
	if err != nil {
		return err
	}
	defer in.Close()
	if info, err := in.Stat(); err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		r.Skipped = append(r.Skipped, rel)
		return nil
	}
	perm := uint32(0o666)
	if mode&0o111 != 0 {
		perm = 0o777
	}
	var st unix.Stat_t
	err = unix.Fstatat(dst, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	replaces := err == nil
	switch {
	case errors.Is(err, unix.ENOENT):
	case err != nil:
		return &os.PathError{Op: "lstat", Path: rel, Err: err}
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		r.Skipped = append(r.Skipped, rel)
		return nil
	default:
		same, err := sameContent(in, dst, name, rel)
		if err != nil || same {
			return err
		}
		if _, err := in.Seek(0, io.SeekStart); err != nil {
			return err
		}
		perm = uint32(st.Mode) & 0o7777
	}
	if err := writeAt(dst, name, rel, in, perm, replaces); err != nil {
		return err
	}
	r.Copied = append(r.Copied, rel)
	return nil
}

// writeAt writes what in has to a new file in dst and renames it to name, over what
// is there. A file that replaces another gets perm as it is; a new one gets perm under
// the umask.
func writeAt(dst int, name, rel string, in io.Reader, perm uint32, replaces bool) error {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	tmp := ".qory-copy-" + hex.EncodeToString(b)
	fd, err := unix.Openat(dst, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, perm)
	if err != nil {
		return &os.PathError{Op: "create", Path: rel, Err: err}
	}
	out := os.NewFile(uintptr(fd), rel)
	_, err = io.Copy(out, in)
	if err == nil && replaces {
		err = out.Chmod(os.FileMode(perm & 0o777))
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = unix.Renameat(dst, tmp, dst, name)
	}
	if err != nil {
		_ = unix.Unlinkat(dst, tmp, 0)
		return &os.PathError{Op: "write", Path: rel, Err: err}
	}
	return nil
}

// sameContent reports whether in, read from its start, has what the regular file name
// in dst has.
func sameContent(in *os.File, dst int, name, rel string) (bool, error) {
	other, err := openFileAt(dst, name, rel)
	if err != nil {
		return false, err
	}
	defer other.Close()
	a, b := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		n, errA := io.ReadFull(in, a)
		m, errB := io.ReadFull(other, b)
		if n != m || !bytes.Equal(a[:n], b[:m]) {
			return false, nil
		}
		endA := errA == io.EOF || errA == io.ErrUnexpectedEOF
		endB := errB == io.EOF || errB == io.ErrUnexpectedEOF
		switch {
		case errA != nil && !endA:
			return false, errA
		case errB != nil && !endB:
			return false, errB
		case endA || endB:
			return endA && endB, nil
		}
	}
}

// openFileAt opens the file name in the folder dir to read, as [fileFlags] has it.
func openFileAt(dir int, name, rel string) (*os.File, error) {
	fd, err := unix.Openat(dir, name, fileFlags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: rel, Err: err}
	}
	return os.NewFile(uintptr(fd), rel), nil
}

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
