package foragerdir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// LabelsDir is the directory beside forager.yaml, mode 0700, of the labels qory keeps
// per checkout: the labels a checkout's origin remote gave at the checkout's first run
// the server accepted. One file per checkout, named by the SHA-256 of the checkout's
// real path, mode 0600. The server chooses a run's policy by its labels, so a later run
// whose origin remote gives others does not start until it is run with --relabel.
const LabelsDir = "labels"

// maxLabelsFile is the most of a checkout's labels file that is read: two labels of at
// most 256 bytes each, in JSON.
const maxLabelsFile = 4 << 10

// Labels are a checkout's kept labels, by key. A key that is absent is not kept; an
// empty value is a key the origin remote gave none of.
type Labels map[string]string

// ErrNoLabels is the error of a checkout whose labels the directory does not keep.
var ErrNoLabels = errors.New("no labels are kept for the checkout")

// LabelsPath is where the labels of the checkout at root are kept: in labels, named by
// the lower-case hex SHA-256 of root's real path, its links resolved, so every path
// that leads to the checkout names the same file.
func (d Dir) LabelsPath(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(resolved))
	return filepath.Join(string(d), LabelsDir, hex.EncodeToString(sum[:])+".json"), nil
}

// ReadLabels reads the labels kept for the checkout at root, from a regular file, not a
// link, in a labels directory that is not a link: one is refused, as [Dir.WriteLabels]
// refuses it. A checkout whose labels are not kept is [ErrNoLabels].
func (d Dir) ReadLabels(root string) (Labels, error) {
	path, err := d.LabelsPath(root)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if info, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoLabels
	} else if err != nil {
		return nil, err
	} else if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link; it must be the directory itself", dir)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow|nonBlock, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoLabels
	}
	if err != nil {
		if isLink(path) {
			return nil, fmt.Errorf("%s is a symbolic link; it must be the file itself", path)
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxLabelsFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxLabelsFile {
		return nil, fmt.Errorf("%s is larger than a checkout's labels", path)
	}
	var l Labels
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if l == nil {
		l = Labels{}
	}
	return l, nil
}

// WriteLabels keeps l as the labels of the checkout at root: in labels, made mode 0700
// when it is not there and refused when it is a link or another user's, a file mode 0600
// written beside it and renamed over the one there, in one step.
func (d Dir) WriteLabels(root string, l Labels) error {
	path, err := d.LabelsPath(root)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := MakePrivateDir(dir, "the directory of the checkouts' labels"); err != nil {
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".labels-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return Dir(dir).sync()
}
