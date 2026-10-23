package prebuild

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"syscall"
)

// The most bytes read of a file of the fetch's repository: HEAD and a loose ref hold one
// line each, and packed-refs one line per ref of a fetch at depth one.
const (
	maxRefFile    = 4 << 10
	maxPackedRefs = 1 << 20
)

// commitShape is a commit id of a SHA-1 repository: 40 lower-case hexadecimal digits.
var commitShape = regexp.MustCompile(`^[0-9a-f]{40}$`)

// refSegment is one segment of a ref's name as this reader takes it: no dot first, so
// no . or .., and no character a ref's name cannot hold.
var refSegment = regexp.MustCompile(`^[A-Za-z0-9_+=,@-][A-Za-z0-9._+=,@-]*$`)

// errNotCommit is every refusal of [Commit]: the reason is in its text after it.
var errNotCommit = errors.New("the fetched repository names no commit")

// Commit is the commit that HEAD of the git working tree work is set to, directly or
// through a ref: what the fetch checked out, read from the files of work/.git alone,
// with no git run. HEAD is a commit id, or "ref: " and a ref under refs/, which is
// read as a loose ref, work/.git/<ref>, or else from work/.git/packed-refs. Every file and
// folder on the way, .git among them, must be what it is, not a link, and stay inside
// work; each file is read up to a bound; and the id must be 40 lower-case hexadecimal
// digits. A ref that names another ref is refused.
func Commit(work string) (string, error) {
	if fi, err := os.Lstat(work); err != nil || !fi.IsDir() {
		return "", refuse("the working folder is not a folder")
	}
	root, err := os.OpenRoot(work)
	if err != nil {
		return "", refuse("the working folder could not be opened: %v", err)
	}
	defer root.Close()
	if err := isDir(root, ".git"); err != nil {
		return "", err
	}
	head, err := readFile(root, ".git/HEAD", maxRefFile)
	if err != nil {
		return "", err
	}
	line, err := oneLine(head, ".git/HEAD")
	if err != nil {
		return "", err
	}
	if commitShape.MatchString(line) {
		return line, nil
	}
	ref, ok := strings.CutPrefix(line, "ref: ")
	if !ok || !refName(ref) {
		return "", refuse(".git/HEAD is neither a commit id nor a ref under refs/")
	}
	loose := ".git/" + ref
	switch b, err := readFile(root, loose, maxRefFile); {
	case err == nil:
		id, err := oneLine(b, loose)
		if err != nil {
			return "", err
		}
		if !commitShape.MatchString(id) {
			return "", refuse("%s is not a commit id", loose)
		}
		return id, nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	packed, err := readFile(root, ".git/packed-refs", maxPackedRefs)
	if errors.Is(err, fs.ErrNotExist) {
		return "", refuse("%s is in neither .git/%s nor .git/packed-refs", ref, ref)
	}
	if err != nil {
		return "", err
	}
	return fromPacked(packed, ref)
}

// fromPacked is the id packed-refs gives ref: the line "<id> <ref>", once. Comments and
// the peeled lines after an annotated tag, ^<id>, are skipped.
func fromPacked(b []byte, ref string) (string, error) {
	found := ""
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		id, name, ok := strings.Cut(line, " ")
		if !ok || name != ref {
			continue
		}
		if found != "" {
			return "", refuse(".git/packed-refs names %s twice", ref)
		}
		if !commitShape.MatchString(id) {
			return "", refuse(".git/packed-refs gives %s no commit id", ref)
		}
		found = id
	}
	if found == "" {
		return "", refuse("%s is in neither .git/%s nor .git/packed-refs", ref, ref)
	}
	return found, nil
}

// refName reports whether ref is a ref this reader looks up: refs/ and segments of
// [refSegment], none ending in .lock, at most 255 bytes.
func refName(ref string) bool {
	if len(ref) > 255 || !strings.HasPrefix(ref, "refs/") {
		return false
	}
	for _, s := range strings.Split(ref, "/") {
		if !refSegment.MatchString(s) || strings.HasSuffix(s, ".lock") {
			return false
		}
	}
	return true
}

// oneLine is b as one line: its text, with one newline after it or none.
func oneLine(b []byte, name string) (string, error) {
	s := strings.TrimSuffix(string(b), "\n")
	if strings.ContainsAny(s, "\n\r\x00") || s == "" {
		return "", refuse("%s is not one line", name)
	}
	return s, nil
}

// isDir refuses name, under root, unless every segment of it is a folder, none a link.
func isDir(root *os.Root, name string) error {
	at := ""
	for _, s := range strings.Split(name, "/") {
		at = path.Join(at, s)
		fi, err := root.Lstat(at)
		if err != nil {
			return notRead(at, err)
		}
		if !fi.IsDir() {
			return refuse("%s is not a folder", at)
		}
	}
	return nil
}

// readFile reads name, under root, up to limit bytes: each folder on the way is one,
// not a link, and the file is a regular file, not a link, opened without following
// one, and the same file it was when it was looked at. A file that is not there is
// fs.ErrNotExist, unwrapped by errors.Is.
func readFile(root *os.Root, name string, limit int64) ([]byte, error) {
	if dir := path.Dir(name); dir != "." {
		if err := isDir(root, dir); err != nil {
			return nil, err
		}
	}
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, notRead(name, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, refuse("%s is not a regular file", name)
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, notRead(name, err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, notRead(name, err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return nil, refuse("%s changed while it was read", name)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, notRead(name, err)
	}
	if int64(len(b)) > limit {
		return nil, refuse("%s is larger than %d bytes", name, limit)
	}
	return b, nil
}

// notRead is a file or folder that could not be looked at: fs.ErrNotExist stays one.
func notRead(name string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s is not there: %w", errNotCommit, name, fs.ErrNotExist)
	}
	return refuse("%s could not be read", name)
}

// refuse is [errNotCommit] with its reason.
func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errNotCommit}, args...)...)
}
