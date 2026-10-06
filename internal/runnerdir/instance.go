package runnerdir

import (
	"errors"
	"io/fs"
	"os"

	"github.com/qoryai/runner/accesskey"
)

// maxInstanceFile is the most of instance-id that is read: two short lines.
const maxInstanceFile = 1024

// readInstance reads instance-id, never through a link, and returns its id when it is
// this machine's.
func (d Dir) readInstance(machineID []byte) (string, bool) {
	f, err := os.OpenFile(d.Path(InstanceFile), os.O_RDONLY|noFollow, 0)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	b := make([]byte, maxInstanceFile+1)
	n, _ := f.Read(b)
	if n > maxInstanceFile {
		return "", false
	}
	return accesskey.ReadInstanceFile(b[:n], machineID)
}

// ReadInstanceID returns the id instance-id holds when it is this machine's, without
// writing anything.
func (d Dir) ReadInstanceID(machineID []byte) (string, bool) { return d.readInstance(machineID) }

// InstanceID returns this instance's id: the one instance-id holds when its id fits the
// pattern and its hash is this machine's identity's, else a new one written there with
// O_CREAT|O_EXCL|O_NOFOLLOW, mode 0600, read again from the file another process
// created first. When the directory cannot be written, the new id is this process's
// alone, and kept is false.
func (d Dir) InstanceID(machineID []byte) (id string, kept bool, err error) {
	if id, ok := d.readInstance(machineID); ok {
		return id, true, nil
	}
	id, err = accesskey.NewInstanceID()
	if err != nil {
		return "", false, err
	}
	path := d.Path(InstanceFile)
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		if readOnly(err) {
			return id, false, nil
		}
		return "", false, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		if readOnly(err) {
			return id, false, nil
		}
		return "", false, err
	}
	err = create(path, 0o600, accesskey.InstanceFile(id, machineID))
	switch {
	case err == nil:
		return id, true, nil
	case errors.Is(err, fs.ErrExist):
		if other, ok := d.readInstance(machineID); ok {
			return other, true, nil
		}
		return id, false, nil
	case readOnly(err):
		return id, false, nil
	}
	return "", false, err
}
