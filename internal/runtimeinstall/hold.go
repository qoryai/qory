package runtimeinstall

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The entries of a runtime's folder, <Folder>/<runtime>, other than the versions'
// folders. Each run that uses a version keeps a shared lock on the version's hold file
// until it ends, and takes it while it has the folder's lock: the cleanup, which an
// install runs while it has the folder's lock too, sees every hold of a run that
// started. A version's folder, or a temporary entry of a version, is removed only while
// the cleanup has the version's hold file locked exclusively.
const (
	// lockName is the folder's lock.
	lockName = ".lock"
	// holdPrefix starts the name of a version's hold file, .hold-<version>.
	holdPrefix = ".hold-"
	// downloadPrefix starts a tarball being read, .download-<version>-<number>.
	downloadPrefix = ".download-"
	// installPrefix starts a folder being extracted, .install-<version>-<number>.
	installPrefix = ".install-"
	// installLockPrefix starts the name of a version's install lock, .lock-<version>.
	// The run that installs the version holds it exclusively from before it reads the
	// tarball until the version's folder is there, so a second run that needs the same
	// version waits and then finds the folder. The folder's lock, lockName, is never
	// held during a download.
	installLockPrefix = ".lock-"
)

// installPoll is the pause between two tries to take an install lock that another run
// holds; a test shortens it.
var installPoll = 100 * time.Millisecond

// lockInstall takes the install lock of version, waiting for the run that holds it, as
// long as ctx allows, and returns the lock's file; closing it ends the lock. The cleanup
// removes a lock file that no run holds, so the lock is the file that the path still
// names once it is taken, else the wait starts again.
func (f folder) lockInstall(ctx context.Context, version string) (*os.File, error) {
	path := filepath.Join(f.base, installLockPrefix+version)
	for {
		l, err := openLock(path)
		if err != nil {
			return nil, fmt.Errorf(textState+"%w", err)
		}
		for {
			err := flock(l, true, true)
			if err == nil {
				break
			}
			if !errors.Is(err, errHeld) {
				l.Close()
				return nil, fmt.Errorf(textState+"%w", err)
			}
			select {
			case <-ctx.Done():
				l.Close()
				return nil, fmt.Errorf("Run again with more time\nThe run ended while another run installed %s %s:\n  %w", f.runtime, version, ctx.Err())
			case <-time.After(installPoll):
			}
		}
		held, herr := l.Stat()
		named, nerr := os.Lstat(path)
		if herr == nil && nerr == nil && os.SameFile(held, named) {
			return l, nil
		}
		l.Close()
	}
}

// prepare makes the folder, mode 0755: the wall's user enters the version's folder.
// Forager checks only the version's folder, so prepare refuses a runtime's folder that
// existed before this call, and a runtimes folder, that its group or others can write:
// they could replace a version's folder. A folder that this call makes is set to 0755,
// whatever the umask. It refuses a folder that root and the user of this process do not
// own, and a link at either folder, and checks no folder above
// them: a shared one such as /tmp is group- and other-writable.
func (f folder) prepare() error {
	parent := filepath.Dir(f.base)
	// A link is refused before any MkdirAll, which would make a folder behind it.
	for _, path := range []string{parent, f.base} {
		if i, err := os.Lstat(path); err == nil && i.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("Replace %s with a folder\n%s is a link, and the install checks and binds the folder itself, not what a link leads to", path, path)
		}
	}
	parentNew, baseNew := !exists(parent), !exists(f.base)
	if err := os.MkdirAll(f.base, 0o755); err != nil {
		return fmt.Errorf(textState+"%w", err)
	}
	info, err := os.Lstat(f.base)
	if err != nil {
		return fmt.Errorf(textState+"%w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Remove the runtime's folder, then run again\n%s is not a folder", f.base)
	}
	for _, c := range []struct {
		path string
		made bool
	}{{parent, parentNew}, {f.base, baseNew}} {
		i, err := os.Lstat(c.path)
		if err != nil {
			return fmt.Errorf(textState+"%w", err)
		}
		if ok, uid := ownerOK(i); !ok {
			return fmt.Errorf("Give %s to root or to the user that runs qory, then run again\n%s is owned by the user id %d, who could replace the runtime's program", c.path, c.path, uid)
		}
		if c.made {
			if err := os.Chmod(c.path, 0o755); err != nil {
				return fmt.Errorf(textState+"%w", err)
			}
			continue
		}
		if i.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("Run chmod go-w %s, then run again\n%s has mode %04o: its group or others could replace the runtime's program", c.path, c.path, i.Mode().Perm())
		}
	}
	if err := os.Chmod(f.base, 0o755); err != nil {
		return fmt.Errorf(textState+"%w", err)
	}
	return nil
}

// exists reports whether path is there, without following a link.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// hold takes the hold of version for the run, and returns the hold file, which the run
// keeps open until it ends. While it has the folder's lock, it removes what no run
// holds: [folder.cleanup]. A cleanup that fails stops nothing.
func (f folder) hold(version string) (*os.File, error) {
	if err := f.prepare(); err != nil {
		return nil, err
	}
	lock, err := openLock(filepath.Join(f.base, lockName))
	if err != nil {
		return nil, fmt.Errorf(textState+"%w", err)
	}
	// Closing the lock's file releases its lock.
	defer lock.Close()
	if err := flock(lock, true, false); err != nil {
		return nil, fmt.Errorf(textState+"%w", err)
	}
	h, err := openLock(f.holdPath(version))
	if err != nil {
		return nil, fmt.Errorf(textState+"%w", err)
	}
	if err := flock(h, false, false); err != nil {
		h.Close()
		return nil, fmt.Errorf(textState+"%w", err)
	}
	f.cleanup(version)
	return h, nil
}

// holdPath is the hold file of version.
func (f folder) holdPath(version string) string {
	return filepath.Join(f.base, holdPrefix+version)
}

// openLock opens the lock file at path, made mode 0600 when it is not there, without
// following a link; it must be a regular file.
func openLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|noFollow, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", path)
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// cleanup removes, of the folder's direct entries, each version's folder older than
// newest, each temporary entry, each install lock, and each hold file with no version's
// folder, that no run holds. A version's folder is removed only when it has the shape
// that an install makes, [folder.installed]. It reads entries without following a link,
// and removes no link, and no entry whose name is not a version's or does not start with
// a temporary prefix, installLockPrefix or holdPrefix. The caller has the folder's
// lock.
func (f folder) cleanup(newest string) {
	entries, err := os.ReadDir(f.base)
	if err != nil {
		return
	}
	// The containers' mounts are read once, when a version's folder is to go.
	var mounts []string
	var mountsErr error
	read := false
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(f.base, name)
		switch {
		case strings.HasPrefix(name, downloadPrefix):
			f.unheld(tempVersion(name, downloadPrefix), func() { removeIf(path, fs.FileMode(0), os.Remove) })
		case strings.HasPrefix(name, installPrefix):
			f.unheld(tempVersion(name, installPrefix), func() { removeIf(path, fs.ModeDir, os.RemoveAll) })
		case strings.HasPrefix(name, installLockPrefix):
			f.unlocked(path)
		case strings.HasPrefix(name, holdPrefix):
			version := strings.TrimPrefix(name, holdPrefix)
			if _, err := os.Lstat(filepath.Join(f.base, version)); !versionShape.MatchString(version) || !errors.Is(err, fs.ErrNotExist) {
				continue
			}
			f.unheld(version, func() { removeIf(path, fs.FileMode(0), os.Remove) })
		case versionShape.MatchString(name):
			if c, ok := compareVersions(name, newest); !ok || c >= 0 || !f.installed(path) {
				continue
			}
			if f.mounted != nil {
				if !read {
					mounts, mountsErr = f.mounted()
					read = true
				}
				if mountsErr != nil || mountedIn(mounts, path) {
					continue
				}
			}
			f.unheld(name, func() {
				if removeIf(path, fs.ModeDir, os.RemoveAll) {
					removeIf(f.holdPath(name), fs.FileMode(0), os.Remove)
				}
			})
		}
	}
}

// mountedIn reports whether a running container mounts dir: a mount's source is dir,
// contains it or lies in it, compared by the folders each leads to.
func mountedIn(mounts []string, dir string) bool {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	for _, m := range mounts {
		if resolved, err := filepath.EvalSymlinks(m); err == nil {
			m = resolved
		}
		m = filepath.Clean(m)
		if m == dir || strings.HasPrefix(m, dir+string(filepath.Separator)) || strings.HasPrefix(dir, m+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// installed reports whether the entry at dir has the shape that [folder.install] makes:
// a folder with bin, a folder, with bin/<runtime>, a regular file, and checksumFile, a
// regular file. It follows no link, at dir or below it, so a folder that something else
// put there is not one.
func (f folder) installed(dir string) bool {
	for _, c := range []struct {
		path string
		kind fs.FileMode
	}{{"", fs.ModeDir}, {"bin", fs.ModeDir}, {filepath.Join("bin", f.runtime), 0}, {checksumFile, 0}} {
		info, err := os.Lstat(filepath.Join(dir, c.path))
		if err != nil || info.Mode().Type() != c.kind {
			return false
		}
	}
	return true
}

// unlocked removes the install lock at path when no run holds it, while it has the
// lock exclusively.
func (f folder) unlocked(path string) {
	l, err := os.OpenFile(path, os.O_RDWR|noFollow, 0)
	if err != nil {
		return
	}
	defer l.Close()
	if info, err := l.Stat(); err != nil || !info.Mode().IsRegular() || flock(l, true, true) != nil {
		return
	}
	removeIf(path, fs.FileMode(0), os.Remove)
}

// unheld runs remove when no run holds version: when its hold file is not there, or
// while the cleanup has it locked exclusively. An empty version has no hold.
func (f folder) unheld(version string, remove func()) {
	if version == "" {
		remove()
		return
	}
	h, err := os.OpenFile(f.holdPath(version), os.O_RDWR|noFollow, 0)
	if errors.Is(err, fs.ErrNotExist) {
		remove()
		return
	}
	if err != nil {
		return
	}
	defer h.Close()
	if info, err := h.Stat(); err != nil || !info.Mode().IsRegular() {
		return
	}
	if flock(h, true, true) != nil {
		return
	}
	remove()
}

// removeIf removes path with remove when it is a folder, for kind fs.ModeDir, or a
// regular file, for kind 0, and not a link; it reports whether it removed it.
func removeIf(path string, kind fs.FileMode, remove func(string) error) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Type() != kind {
		return false
	}
	return remove(path) == nil
}

// tempVersion is the version in name, a temporary entry <prefix><version>-<number>;
// empty when name has none.
func tempVersion(name, prefix string) string {
	rest := strings.TrimPrefix(name, prefix)
	i := strings.LastIndex(rest, "-")
	if i < 0 {
		return ""
	}
	if _, err := strconv.ParseUint(rest[i+1:], 10, 64); err != nil || !versionShape.MatchString(rest[:i]) {
		return ""
	}
	return rest[:i]
}

// compareVersions compares a and b as semantic versions, major.minor.patch with any
// number of numeric parts, an optional -prerelease and an optional +build, which does
// not count: -1 when a is older, 0 when they are equal, 1 when a is newer. ok is false
// when either is not such a version, and the two are then not compared.
func compareVersions(a, b string) (c int, ok bool) {
	ac, ap, aok := splitVersion(a)
	bc, bp, bok := splitVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range max(len(ac), len(bc)) {
		x, y := "0", "0"
		if i < len(ac) {
			x = ac[i]
		}
		if i < len(bc) {
			y = bc[i]
		}
		if c := compareNumbers(x, y); c != 0 {
			return c, true
		}
	}
	switch {
	case ap == nil && bp == nil:
		return 0, true
	case ap == nil:
		return 1, true
	case bp == nil:
		return -1, true
	}
	for i := range min(len(ap), len(bp)) {
		x, y := ap[i], bp[i]
		xn, yn := numeric(x), numeric(y)
		var c int
		switch {
		case xn && yn:
			c = compareNumbers(x, y)
		case xn:
			c = -1
		case yn:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c, true
		}
	}
	return compareInts(len(ap), len(bp)), true
}

// splitVersion is v's numeric parts and its prerelease's parts, nil for none; ok is
// false when v is not a semantic version.
func splitVersion(v string) (core, pre []string, ok bool) {
	v, _, _ = strings.Cut(v, "+")
	c, p, hasPre := strings.Cut(v, "-")
	core = strings.Split(c, ".")
	for _, n := range core {
		if !numeric(n) {
			return nil, nil, false
		}
	}
	if hasPre {
		pre = strings.Split(p, ".")
		for _, n := range pre {
			if n == "" {
				return nil, nil, false
			}
		}
	}
	return core, pre, true
}

// numeric reports whether s is one or more ASCII digits.
func numeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// compareNumbers compares two numbers written in digits, of any length.
func compareNumbers(x, y string) int {
	x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
	if c := compareInts(len(x), len(y)); c != 0 {
		return c
	}
	return strings.Compare(x, y)
}

// compareInts is -1, 0 or 1 as x is less than, equal to or greater than y.
func compareInts(x, y int) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}
