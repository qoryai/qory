package foragerdir

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// RunKeysDir is the directory beside forager.yaml, mode 0700, of the run keys qory keeps
// on a machine with its own access key whose runs go through a separate gateway: one
// file per run, named by the run's id, mode 0600. A run's key is never in its run
// directory, which a walled agent sees, and a wall's mounts never reach forager.yaml's
// directory.
const RunKeysDir = "run-keys"

// maxRunKeyFile is the most of a kept run key that is read: a run key is at most 2048
// characters.
const maxRunKeyFile = 4 << 10

// ErrNoRunKey is the error of a run whose run key the directory does not keep.
var ErrNoRunKey = errors.New("no run key is kept for the run")

// runKeyFile is how a kept run key is checked before it is read.
var runKeyFile = Private{Holds: "a run's run key", Replace: "the run key, by ending the run,", DirOf: "forager.yaml"}

// runKeyPath is where the run key of the run runID, a run id the caller checked, is
// kept.
func (d Dir) runKeyPath(runID string) string {
	return filepath.Join(string(d), RunKeysDir, filepath.Base(runID))
}

// WriteRunKey keeps the run key of the run runID, whose id the caller checked: in
// run-keys, made mode 0700 when it is not there and refused when it is a link or
// another user's, a file mode 0600 created with O_EXCL and O_NOFOLLOW, in place of one
// an earlier attempt left. No error contains the key.
func (d Dir) WriteRunKey(runID, key string) error {
	dir := filepath.Join(string(d), RunKeysDir)
	if err := MakePrivateDir(dir, "the directory of the run keys"); err != nil {
		return err
	}
	path := d.runKeyPath(runID)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	b := []byte(key + "\n")
	defer clear(b)
	if err := create(path, 0o600, b); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ReadRunKey reads the run key kept for the run runID, whose id the caller checked:
// from a regular file, not a link, that the effective user owns and that grants nothing
// to the group or to others, in a directory that is the user's alone. A run whose key
// is not kept is [ErrNoRunKey]. No error contains the key.
func (d Dir) ReadRunKey(runID string) (string, error) {
	path := d.runKeyPath(runID)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return "", ErrNoRunKey
	}
	if err := runKeyFile.checkDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow|nonBlock, 0)
	if err != nil {
		if isLink(path) {
			return "", fmt.Errorf("%s is a symbolic link; it must be the file itself", path)
		}
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if err := runKeyFile.checkFile(path, info); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRunKeyFile+1))
	defer clear(b)
	if err != nil {
		return "", err
	}
	if len(b) > maxRunKeyFile {
		return "", fmt.Errorf("%s is larger than a run key", path)
	}
	return string(b), nil
}

// RemoveRunKey removes the run key kept for the run runID, whose id the caller checked;
// one that is not kept is no error.
func (d Dir) RemoveRunKey(runID string) error {
	if err := os.Remove(d.runKeyPath(runID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// RunKeyPath is where the run key of the run runID is kept, for a test and a line.
func (d Dir) RunKeyPath(runID string) string { return d.runKeyPath(runID) }
