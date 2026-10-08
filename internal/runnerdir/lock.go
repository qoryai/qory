package runnerdir

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
)

// errWouldBlock is a lock another process holds, asked for without waiting.
var errWouldBlock = errors.New("the lock is held")

// Lock is a held flock on a file of the locks directory.
type Lock struct {
	f *os.File
}

// Release drops the lock and closes its file. A nil Lock releases nothing.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	funlock(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}

// locks creates the locks directory, mode 0700.
func (d Dir) locks() (string, error) {
	path := d.Path(LocksDir)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// openLock opens a lock file, creating it with mode 0600, never through a link.
func openLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|noFollow, 0o600)
}

// ErrReadOnly is a directory in which no lock file can be created: a read-only file
// system, or one the user may not write. No key command can write there either.
var ErrReadOnly = errors.New("the runner file's directory cannot be written")

// LockKey takes locks/key.lock, exclusively for a command that generates a key, shared
// for a run that starts, waiting while another process holds it the other way. A
// directory that cannot be written is [ErrReadOnly].
func (d Dir) LockKey(exclusive bool) (*Lock, error) {
	dir, err := d.locks()
	if err != nil {
		if readOnly(err) {
			return nil, ErrReadOnly
		}
		return nil, err
	}
	f, err := openLock(dir + string(os.PathSeparator) + KeyLock)
	if err != nil {
		if readOnly(err) {
			return nil, ErrReadOnly
		}
		return nil, err
	}
	if err := flock(f, exclusive, false); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return &Lock{f: f}, nil
}

// The words a run's lock file holds, naming whether the run is walled.
const (
	walledWord   = "walled"
	unwalledWord = "unwalled"
)

// LockRun creates locks/<run id>.lock, naming whether the run is walled, and holds it
// exclusively for the run's life. A run takes it while it holds the key lock shared,
// so no key command sees a run lock file half written.
func (d Dir) LockRun(runID string, walled bool) (*Lock, error) {
	dir, err := d.locks()
	if err != nil {
		return nil, err
	}
	f, err := openLock(dir + string(os.PathSeparator) + runID + ".lock")
	if err != nil {
		return nil, err
	}
	if err := flock(f, true, true); err != nil {
		f.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, fmt.Errorf("a run with the id %s is running", runID)
		}
		return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	word := unwalledWord
	if walled {
		word = walledWord
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.WriteAt([]byte(word+"\n"), 0); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Sweep is for a key command that holds the key lock exclusively: it removes every run
// lock file whose lock it can take, since that run has ended, and returns the ids of
// the runs still live without a wall. A lock file that does not say walled counts as
// unwalled.
func (d Dir) Sweep() (unwalled []string, err error) {
	dir := d.Path(LocksDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		id, ok := strings.CutSuffix(name, ".lock")
		if !ok || name == KeyLock || !e.Type().IsRegular() {
			continue
		}
		path := dir + string(os.PathSeparator) + name
		f, err := os.OpenFile(path, os.O_RDWR|noFollow, 0)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if err := flock(f, true, true); err == nil {
			os.Remove(path)
			funlock(f)
			f.Close()
			continue
		} else if !errors.Is(err, errWouldBlock) {
			f.Close()
			return nil, err
		}
		b, _ := io.ReadAll(io.LimitReader(f, 64))
		f.Close()
		if strings.TrimSpace(string(b)) != walledWord {
			unwalled = append(unwalled, id)
		}
	}
	sort.Strings(unwalled)
	return unwalled, nil
}
