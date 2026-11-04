//go:build unix

package runcopy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// SetupStates reads what the copy has at each of paths, the paths the setup changed as
// RecordSetup lists them, for [LeftOut.Setup]. Call it after the setup and before the
// agent's session. It opens each folder on the way from the one above it with
// O_NOFOLLOW, so it follows no link, and hashes a file's content, or a link's target,
// with git hash-object --stdin --no-filters in checkout, the person's repository, which
// sets the object format. No git opens the copy.
func (d Dir) SetupStates(ctx context.Context, checkout string, paths []string) ([]SetupPath, error) {
	out := make([]SetupPath, 0, len(paths))
	for _, p := range paths {
		if !cleanRel(p) {
			return nil, fmt.Errorf("give each setup path relative to the top of the copy: %s is not such a path", shown(p))
		}
		state, err := d.setupState(ctx, checkout, p)
		if err != nil {
			return nil, err
		}
		out = append(out, SetupPath{Path: p, State: state})
	}
	return out, nil
}

// setupState is what the copy has at rel, in the form of [SetupPath.State].
func (d Dir) setupState(ctx context.Context, checkout, rel string) (string, error) {
	dir, err := unix.Open(d.Work(), dirFlags, 0)
	if err != nil {
		return "", &os.PathError{Op: "open", Path: d.Work(), Err: err}
	}
	parts := strings.Split(rel, "/")
	for _, name := range parts[:len(parts)-1] {
		next, err := unix.Openat(dir, name, dirFlags, 0)
		unix.Close(dir)
		switch {
		case errors.Is(err, unix.ENOENT):
			return "missing", nil
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
			// A link or a file where a folder was: the path's file is not there.
			return "missing", nil
		case err != nil:
			return "", &os.PathError{Op: "open", Path: rel, Err: err}
		}
		dir = next
	}
	defer unix.Close(dir)
	name := parts[len(parts)-1]
	var st unix.Stat_t
	err = unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return "missing", nil
	}
	if err != nil {
		return "", &os.PathError{Op: "lstat", Path: rel, Err: err}
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		buf := make([]byte, unix.PathMax)
		n, err := unix.Readlinkat(dir, name, buf)
		if err != nil {
			return "", &os.PathError{Op: "readlink", Path: rel, Err: err}
		}
		id, err := blobID(ctx, checkout, strings.NewReader(string(buf[:n])))
		return "link:" + id, err
	case unix.S_IFREG:
		f, err := openFileAt(dir, name, rel)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
			return "other", err
		}
		kind := "file:"
		if st.Mode&0o100 != 0 {
			kind = "exec:"
		}
		id, err := blobID(ctx, checkout, f)
		return kind + id, err
	}
	return "other", nil
}
