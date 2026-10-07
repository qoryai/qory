// Package runnerdir is the runner file's directory, everything qory keeps on this
// machine for its server beside runner.yaml: the access key's secret, the moved-aside
// secrets, the instance id, the stored-secrets marker, the pending enrolment and the
// lock files. The directory is the user's configuration directory, $XDG_CONFIG_HOME/qory
// or ~/.config/qory, mode 0700.
//
// Every file is created with O_CREAT|O_EXCL|O_NOFOLLOW and an exact mode, and the secret
// is read only from a regular file the effective user owns that grants nothing to the
// group or to others, in a directory the effective user owns that grants nothing to
// them either: the owner and the mode are compared before a byte is read. No error of
// this package contains a secret.
package runnerdir

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/qoryai/runner/accesskey"
)

// The files of the directory.
const (
	// SecretFile holds the access key's secret, one line, mode 0600.
	SecretFile = "access-key-secret"
	// OldPrefix starts the name of a secret moved aside, followed by the Unix time.
	OldPrefix = SecretFile + ".old."
	// MarkerFile is the stored-secrets marker: while it exists, every run needs a wall.
	MarkerFile = "stored-secrets"
	// PendingFile records an enrolment that has not been answered yet: the SHA-256 of
	// its code, the fingerprint of the public key made for it and the time.
	PendingFile = "enrolment-pending"
	// InstanceFile holds this machine's instance id and the hash of its identity.
	InstanceFile = "instance-id"
	// LocksDir holds the key lock and one lock file per run.
	LocksDir = "locks"
	// KeyLock is the lock every command that generates a key takes exclusively, and
	// every run takes shared while it starts.
	KeyLock = "key.lock"
)

// PendingFor is how long an enrolment code stays usable: a retry reuses the secret
// only while the pending enrolment is younger than this.
const PendingFor = 15 * time.Minute

// maxSecretFile is the most of access-key-secret that is read: a secret is one line of
// 47 characters.
const maxSecretFile = 4 << 10

// Dir is the runner file's directory.
type Dir string

// Path returns the path of a file of the directory.
func (d Dir) Path(name string) string { return filepath.Join(string(d), name) }

// Ensure creates the directory with mode 0700, or makes an existing one the effective
// user owns mode 0700, and reports whether it changed the mode. A directory another user
// owns is refused.
func (d Dir) Ensure() (changed bool, err error) {
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return false, err
	}
	info, err := os.Stat(string(d))
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s is not a directory", d)
	}
	if !ownedByMe(info) {
		return false, fmt.Errorf("%s belongs to another user; the runner file's directory is yours alone", d)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(string(d), 0o700); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// Check refuses a directory another user owns, or one that grants anything to the group
// or to others: the secret is read only from a directory that is the user's alone.
func (d Dir) Check() error {
	info, err := os.Stat(string(d))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", d)
	}
	if !ownedByMe(info) {
		return fmt.Errorf("%s belongs to another user; the runner file's directory is yours alone", d)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is mode %04o, which grants access to the group or others; it holds the access key's secret: chmod 700 %s", d, info.Mode().Perm(), d)
	}
	return nil
}

// ErrNoSecret is the error of a directory that holds no access-key-secret.
var ErrNoSecret = errors.New("no " + SecretFile)

// ReadSecret reads access-key-secret: a regular file, not a link, that the effective
// user owns and that grants nothing to the group or to others, in a directory [Dir.Check]
// passes, holding one secret and at most one line ending after it. The published
// fixture key is refused. A directory with no such file is [ErrNoSecret].
func (d Dir) ReadSecret() (*accesskey.Key, error) {
	path := d.Path(SecretFile)
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoSecret
	}
	if err := d.Check(); err != nil {
		return nil, err
	}
	b, err := readPrivate(path, maxSecretFile)
	if err != nil {
		return nil, err
	}
	k, err := ParseSecret(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return k, nil
}

// ParseSecret reads an access key secret as a file, a variable or a file descriptor
// holds it: the secret, and at most one line ending after it. The published fixture key
// is refused. No error contains the value.
func ParseSecret(b []byte) (*accesskey.Key, error) {
	s := string(b)
	if t, ok := strings.CutSuffix(s, "\n"); ok {
		s = strings.TrimSuffix(t, "\r")
	}
	k, err := accesskey.ParseSecret(s)
	if err != nil {
		return nil, errors.New("not an access key secret: one line, " + accesskey.SecretPrefix + " and 43 characters of base64url")
	}
	if k.PublicKey().Fixture() {
		return nil, errors.New("it holds the runner contract's published fixture key, whose secret anyone can read; generate a key of your own with qory access-key enrol or qory access-key create")
	}
	return k, nil
}

// readPrivate reads a file the effective user owns, regular and not a link, that grants
// nothing to the group or to others, comparing the owner and the mode before it reads,
// and reading at most limit bytes.
func readPrivate(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow|nonBlock, 0)
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
	if !ownedByMe(info) {
		return nil, fmt.Errorf("%s belongs to another user; the access key's secret is yours alone", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is mode %04o, which grants access to the group or others: chmod 600 %s, and replace the key if anyone else could read it", path, info.Mode().Perm(), path)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than an access key secret", path)
	}
	return b, nil
}

// isLink reports whether path is a symbolic link.
func isLink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&fs.ModeSymlink != 0
}

// create creates a file that must not exist, not following a link, with exactly mode,
// and writes b into it.
func create(path string, mode os.FileMode, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, mode)
	if err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// WriteSecret writes a new access-key-secret, mode 0600, which must not exist.
func (d Dir) WriteSecret(k *accesskey.Key) error {
	if err := create(d.Path(SecretFile), 0o600, []byte(k.Secret()+"\n")); err != nil {
		return fmt.Errorf("write %s: %w", d.Path(SecretFile), err)
	}
	return nil
}

// HasSecret reports whether access-key-secret exists, as a file or as anything else.
func (d Dir) HasSecret() bool {
	_, err := os.Lstat(d.Path(SecretFile))
	return err == nil
}

// MoveAside renames access-key-secret to access-key-secret.old.<Unix time>, under a
// name no existing file has, and returns the new name. A directory with no secret
// moves nothing and returns "".
func (d Dir) MoveAside(now time.Time) (string, error) {
	src := d.Path(SecretFile)
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	for t := now.Unix(); ; t++ {
		dst := d.Path(OldPrefix + strconv.FormatInt(t, 10))
		// A link fails when the name exists, where a rename would replace the file.
		err := os.Link(src, dst)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("move %s aside: %w", src, err)
		}
		if err := os.Remove(src); err != nil {
			return "", fmt.Errorf("move %s aside: %w", src, err)
		}
		return dst, nil
	}
}

// OldSecrets returns the names of the secrets moved aside.
func (d Dir) OldSecrets() ([]string, error) {
	entries, err := os.ReadDir(string(d))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), OldPrefix) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// DeleteOldSecrets deletes every secret moved aside.
func (d Dir) DeleteOldSecrets() error {
	old, err := d.OldSecrets()
	if err != nil {
		return err
	}
	for _, name := range old {
		if err := os.Remove(d.Path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// HasMarker reports whether the stored-secrets marker exists. A marker that cannot be
// looked at is an error, which a caller treats as a marker.
func (d Dir) HasMarker() (bool, error) {
	_, err := os.Lstat(d.Path(MarkerFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return true, nil
}

// markerText is what the marker holds, for a person who finds it.
const markerText = "While this file exists, qory runs every session behind a wall: this machine's access key may receive stored secrets.\n"

// WriteMarker writes the stored-secrets marker, mode 0600; one that exists stays.
func (d Dir) WriteMarker() error {
	err := create(d.Path(MarkerFile), 0o600, []byte(markerText))
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("write the stored-secrets marker %s: %w", d.Path(MarkerFile), err)
	}
	return nil
}

// RemoveMarker removes the stored-secrets marker.
func (d Dir) RemoveMarker() error {
	if err := os.Remove(d.Path(MarkerFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// codeHash is the lower-case hex SHA-256 of a normalised enrolment code.
func codeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// WritePending records an enrolment of code with the key whose public key is key at
// now in enrolment-pending, mode 0600, replacing a record there. The record holds the
// key's fingerprint, never its secret.
func (d Dir) WritePending(code string, key accesskey.PublicKey, now time.Time) error {
	if err := d.RemovePending(); err != nil {
		return err
	}
	if err := create(d.Path(PendingFile), 0o600, []byte(codeHash(code)+"\n"+key.Fingerprint()+"\n"+strconv.FormatInt(now.Unix(), 10)+"\n")); err != nil {
		return fmt.Errorf("write %s: %w", d.Path(PendingFile), err)
	}
	return nil
}

// Pending reports whether enrolment-pending records code with the key whose public key
// is key, and is younger than [PendingFor] at now. A record of another code or another
// key, or one without a key's fingerprint, is none.
func (d Dir) Pending(code string, key accesskey.PublicKey, now time.Time) bool {
	b, err := readPrivate(d.Path(PendingFile), 1024)
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 3 || lines[0] != codeHash(code) || lines[1] != key.Fingerprint() {
		return false
	}
	t, err := strconv.ParseInt(lines[2], 10, 64)
	if err != nil {
		return false
	}
	age := now.Sub(time.Unix(t, 0))
	return age >= 0 && age < PendingFor
}

// RemovePending removes enrolment-pending.
func (d Dir) RemovePending() error {
	if err := os.Remove(d.Path(PendingFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SameSecret reports whether access-key-secret still holds the secret of k.
func (d Dir) SameSecret(k *accesskey.Key) bool {
	held, err := d.ReadSecret()
	return err == nil && held.PublicKey() == k.PublicKey()
}

// readOnly reports whether err says the directory cannot be written: a read-only file
// system, or no permission.
func readOnly(err error) bool {
	return errors.Is(err, syscall.EROFS) || errors.Is(err, fs.ErrPermission)
}
