package foragerdir

import (
	"errors"
	"fmt"
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
