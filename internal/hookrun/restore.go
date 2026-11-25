// Package hookrun reads HEAD of a checkout that a run can write, with no git.
package hookrun

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/qoryai/qory/internal/render"
)

// Head is the commit id that HEAD of the checkout at dir points at, read from the files of
// its .git with no git, as [headOf] reads it, so no configuration of the checkout takes effect.
func Head(dir string) (string, error) { return headOf(filepath.Join(dir, ".git")) }

// sha1ID matches an object id of sha1.
var sha1ID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// maxRefFile is the most bytes of a ref file, and of packed-refs, that [headOf] reads.
const maxRefFile = 64 << 20

// headOf is the commit id that HEAD of the git directory gitDir points at, read from its files
// with no git, so that no configuration in gitDir takes effect: HEAD, then the refs it names, up
// to 5 in a chain, from a loose ref file, or from packed-refs for a ref with no loose file or
// with a folder in its place, as git reads it. A link, a name outside refs/ or a ref of another
// format is an error. These are errors too, since git can read another commit from them: refs in
// other files, see [filesRefs], a HEAD or a loose ref of another form than git writes, see
// [refLine], and such a packed-refs, see [packedRef].
func headOf(gitDir string) (string, error) {
	if err := filesRefs(gitDir); err != nil {
		return "", err
	}
	name := "HEAD"
	for range 5 {
		data, err := readRef(filepath.Join(gitDir, filepath.FromSlash(name)))
		if name != "HEAD" && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EISDIR)) {
			return packedRef(gitDir, name)
		}
		if err != nil {
			return "", err
		}
		if !refLine(string(data)) {
			return "", fmt.Errorf("%s in .git is not one line of a ref or a commit id", name)
		}
		// TrimSpace would remove characters that git reads as part of a ref name, such as U+00A0.
		text := strings.TrimSuffix(string(data), "\n")
		ref, isRef := strings.CutPrefix(text, "ref: ")
		if !isRef {
			return text, nil
		}
		if !strings.HasPrefix(ref, "refs/") {
			return "", fmt.Errorf("%s in .git points at %q, which is not a ref", name, ref)
		}
		name = ref
	}
	return "", errors.New("HEAD in .git points at a chain of more than 5 refs")
}

// The keys of a configuration that [filesRefs] looks for, in any case and section. git reads a key
// at the start of a line or after the ] of a section's header, and spaces, tabs and carriage
// returns before it. A key has letters, digits and -.
var (
	otherRefs  = regexp.MustCompile(`(?im)(?:^|\])[ \t\r]*(?:refstorage|(?:compat)?objectformat)(?:[^a-z0-9-]|$)`)
	formatKey  = regexp.MustCompile(`(?im)(?:^|\])[ \t\r]*repositoryformatversion\b`)
	knownValue = regexp.MustCompile(`^[ \t]*=[ \t]*[01][ \t]*(?:\n|$)`)
)

// filesRefs returns an error when git can read the refs of gitDir from other files than HEAD,
// refs/ and packed-refs, or with ids of another length than sha1's. These are: a commondir file,
// which git follows to another git directory; a reftable folder; refs or objects that is not a
// folder that qory can search, since git then takes gitDir for no repository and can read one at
// the top of the checkout; and these keys in the config, see [otherRefs]: refStorage, which can
// select reftable, objectFormat, compatObjectFormat, and repositoryFormatVersion with another
// value than 0 or 1, which git does not read. A config that git init writes for a sha1 repository
// has none. The text of a value or comment is an error too where it looks like such a key: after
// a ], or on a line that continues a value.
//
// shortcut: a SHA-256 repository and one with reftable fail the read, so the full suite of a
// session runs there also when HEAD did not move. Replace it when qory parses .git/config as git
// does.
func filesRefs(gitDir string) error {
	for _, name := range []string{"commondir", "reftable"} {
		_, err := os.Lstat(filepath.Join(gitDir, name))
		if err == nil {
			return fmt.Errorf("the .git has %s, so git reads refs from other files", name)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	for _, name := range []string{"refs", "objects"} {
		folder := filepath.Join(gitDir, name)
		info, err := os.Lstat(folder)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s in .git is not a folder", name)
		}
		// The path with /. needs the permission to search the folder, as git does.
		if _, err := os.Lstat(folder + string(filepath.Separator) + "."); err != nil {
			return err
		}
	}
	data, err := readRef(filepath.Join(gitDir, "config"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if otherRefs.Match(data) {
		return errors.New("the .git/config has refStorage or objectFormat, so git can read other refs or ids")
	}
	for _, key := range formatKey.FindAllIndex(data, -1) {
		if !knownValue.Match(data[key[1]:]) {
			return errors.New("the .git/config has a repositoryFormatVersion that is not 0 or 1")
		}
	}
	return nil
}

// refLine reports whether data is the form of HEAD and of a loose ref that git writes: one line,
// ended by a line feed, of "ref: " and a ref name that git accepts, see [refName], or of an
// object id of sha1. Neither has a carriage return or a line feed.
func refLine(data string) bool {
	line, ok := strings.CutSuffix(data, "\n")
	if !ok {
		return false
	}
	if ref, isRef := strings.CutPrefix(line, "ref: "); isRef {
		return refName(ref)
	}
	return sha1ID.MatchString(line)
}

// refName reports whether name is a ref name that git accepts, as git check-ref-format checks
// it: parts between slashes that are not empty and do not start with a dot or end with .lock,
// no end with a slash or a dot, and no .., @{, control character, space, ~, ^, :, ?, *, [ or
// \.
func refName(name string) bool {
	if name == "" || strings.HasSuffix(name, ".") || strings.Contains(name, "..") ||
		strings.Contains(name, "@{") ||
		strings.ContainsFunc(name, func(r rune) bool {
			return r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r)
		}) {
		return false
	}
	for part := range strings.SplitSeq(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// readRef is the file at path, a regular file of at most [maxRefFile] bytes. A folder is the
// error [syscall.EISDIR]. The open follows no link at the last part of path; a link at a folder above it, such as refs/heads, is
// followed.
func readRef(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|render.OpenFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, &fs.PathError{Op: "read", Path: path, Err: syscall.EISDIR}
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s in .git is not a file", filepath.Base(path))
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRefFile+1))
	if err == nil && len(data) > maxRefFile {
		err = fmt.Errorf("%s in .git has more than %d bytes", filepath.Base(path), maxRefFile)
	}
	return data, err
}

// packedRef is the id of the ref name in packed-refs of gitDir, when packed-refs has the form
// that git writes, see [parsePackedRefs].
func packedRef(gitDir, name string) (string, error) {
	data, err := readRef(filepath.Join(gitDir, "packed-refs"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return parsePackedRefs(string(data), name)
}

// parsePackedRefs is the id of the ref name in data, a packed-refs file, when data has the form
// that git writes: lines ended by a line feed, with no carriage return; an optional first line
// "# pack-refs with:"; then lines of an object id of sha1 and a ref name, each optionally followed
// by one line of ^ and an id. Each ref name comes after the one before it in byte order, since git
// searches by halves and can find another ref or none in a file with another order. Any other
// line or order is an error.
func parsePackedRefs(data, name string) (string, error) {
	bad := errors.New("packed-refs in .git has a line of another form than git writes")
	if strings.Contains(data, "\r") || (data != "" && !strings.HasSuffix(data, "\n")) {
		return "", bad
	}
	if data == "" {
		return "", fmt.Errorf("%s in .git has no commit", name)
	}
	id, last, afterRef := "", "", false
	for i, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		peeled, isPeeled := strings.CutPrefix(line, "^")
		switch {
		case i == 0 && strings.HasPrefix(line, "# pack-refs with:"):
			continue
		case isPeeled:
			if !afterRef || !sha1ID.MatchString(peeled) {
				return "", bad
			}
			afterRef = false
			continue
		}
		// A line with no space has no ref name, which refName does not accept.
		hex, ref, _ := strings.Cut(line, " ")
		if !sha1ID.MatchString(hex) || !refName(ref) {
			return "", bad
		}
		if ref <= last {
			return "", fmt.Errorf("packed-refs in .git has %s not in order", ref)
		}
		last, afterRef = ref, true
		if ref == name {
			id = hex
		}
	}
	if id == "" {
		return "", fmt.Errorf("%s in .git has no commit", name)
	}
	return id, nil
}
