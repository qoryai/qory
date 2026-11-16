package foragerdir

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrLocked is a lock another process holds, which [LockFile] does not wait for.
var ErrLocked = errWouldBlock

// LockFile takes the lock of the file at path exclusively, creating the file mode 0600,
// never through a link, and holds it until Release: for a program of which one runs at a
// time. It does not wait: a lock another process holds is [ErrLocked].
func LockFile(path string) (*Lock, error) {
	f, err := openLock(path)
	if err != nil {
		return nil, err
	}
	if err := flock(f, true, true); err != nil {
		f.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// LockFileText is [LockFile] that also writes text into the file, for the process that
// finds the lock held to read, see [LockText]. The text replaces what an earlier holder
// wrote.
func LockFileText(path, text string) (*Lock, error) {
	l, err := LockFile(path)
	if err != nil {
		return nil, err
	}
	if err := l.SetText(text); err != nil {
		l.Release()
		return nil, err
	}
	return l, nil
}

// SetText replaces the text of the lock file with text, for the holder. A nil Lock sets
// nothing.
func (l *Lock) SetText(text string) error {
	if l == nil || l.f == nil {
		return nil
	}
	// The new text goes first and the file is cut to its length after, so a reader never finds
	// the file empty.
	if _, err := l.f.WriteAt([]byte(text), 0); err != nil {
		return err
	}
	return l.f.Truncate(int64(len(text)))
}

// lockTextMax is the most of a lock file's text that [LockText] reads.
const lockTextMax = 512

// LockText is the text of the lock file at path that [LockFileText] wrote, at most 512
// bytes, "" when the file is missing, empty or cannot be read. It takes no lock, and never
// reads through a link.
func LockText(path string) string {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow|nonBlock, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, lockTextMax))
	return string(b)
}
