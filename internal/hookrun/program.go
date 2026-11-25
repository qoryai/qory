package hookrun

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qoryai/qory/internal/hostgit"
)

// MaxProgramBytes is the most bytes of a hook's program that qory reads, and of each tree on
// its path.
const MaxProgramBytes = 64 << 20

// The causes of a program that qory does not read, each one line: the path's, as a [*Refused],
// and the objects'.
const (
	textLink      = "%s is a link in git at %s"
	textOnLink    = "%s passes through the link %s in git at %s"
	textFolder    = "%s is a folder in git at %s"
	textSubmodule = "%s is a submodule in git at %s"
	textOther     = "%s has the mode %s in git at %s, not a file's"
	textMissing   = "%s has no entry in git at %s"
	textBadPath   = "%s is not a path from the checkout's root"
	textBadObject = "the %s %s in .git does not match its id"
	textTooLarge  = "the %s %s has more than %d bytes"
)

// Refused is the error of a program that qory does not read: Cause is one line.
type Refused struct{ Cause string }

func (e *Refused) Error() string { return e.Cause }

// TooLarge is the error of a program, or a tree on its path, of more than [MaxProgramBytes]
// bytes: Cause is one line.
type TooLarge struct{ Cause string }

func (e *TooLarge) Error() string { return e.Cause }

// Program writes the program at path, from the root of the repository at repo, as the commit
// has it, to the file dest, mode 0555. It reads the commit, each tree on the path and the file
// with git cat-file, and checks each object's bytes against its id, so an object that a
// process wrote into .git/objects under another object's id is an error. A path that passes a
// link, or whose entry is a link, a folder, a submodule or no file, is refused with a
// [*Refused], and a program over [MaxProgramBytes] with a [*TooLarge]. After any error dest is
// not written. git runs with a private git directory of the checkout, see [private], in a
// folder next to dest.
// Every program a hook runs comes from here, so a change
// of where the program comes from is a change of this function alone.
func Program(ctx context.Context, repo, commit, path, dest string) error {
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return &Refused{fmt.Sprintf(textBadPath, path)}
		}
	}
	// git here reads the checkout's .git, which an agent may have written.
	_, env, done, err := private(ctx, repo, filepath.Dir(dest), commit)
	if err != nil {
		return err
	}
	defer done()
	at := shortID(commit)
	data, err := readObject(ctx, repo, env, commit, "commit")
	if err != nil {
		return err
	}
	tree, ok := treeOf(data)
	if !ok {
		return fmt.Errorf("the commit %s has no tree", at)
	}
	for i, name := range parts {
		data, err := readObject(ctx, repo, env, tree, "tree")
		if err != nil {
			return err
		}
		mode, id, found := entryOf(data, name, len(commit)/2)
		sofar := strings.Join(parts[:i+1], "/")
		last := i == len(parts)-1
		switch {
		case !found:
			return &Refused{fmt.Sprintf(textMissing, path, at)}
		case mode == "120000" && last:
			return &Refused{fmt.Sprintf(textLink, path, at)}
		case mode == "120000":
			return &Refused{fmt.Sprintf(textOnLink, path, sofar, at)}
		case mode == "160000":
			return &Refused{fmt.Sprintf(textSubmodule, sofar, at)}
		case mode == "40000" && !last:
			tree = id
			continue
		case mode == "40000":
			return &Refused{fmt.Sprintf(textFolder, path, at)}
		case !last:
			return &Refused{fmt.Sprintf(textMissing, path, at)}
		case last && (mode == "100644" || mode == "100755"):
			blob, err := readObject(ctx, repo, env, id, "blob")
			if err != nil {
				return err
			}
			return writeProgram(dest, blob)
		default:
			return &Refused{fmt.Sprintf(textOther, sofar, mode, at)}
		}
	}
	return &Refused{fmt.Sprintf(textMissing, path, at)}
}

// writeProgram writes data to a new file dest, mode 0555, which every user may read and run.
func writeProgram(dest string, data []byte) error {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// The wall's user may not be this process's, so the file is readable by every user; it
	// has the bytes of a committed file, and no user may write it.
	return os.Chmod(dest, 0o555)
}

// treeOf is the tree of a commit object's bytes.
func treeOf(commit []byte) (string, bool) {
	line, _, _ := bytes.Cut(commit, []byte("\n"))
	id, ok := bytes.CutPrefix(line, []byte("tree "))
	return string(id), ok
}

// entryOf is the mode and id of the entry name of a tree object's bytes, whose ids have size
// bytes.
func entryOf(tree []byte, name string, size int) (mode, id string, found bool) {
	for len(tree) > 0 {
		sp := bytes.IndexByte(tree, ' ')
		nul := bytes.IndexByte(tree, 0)
		if sp < 0 || nul < sp || nul+1+size > len(tree) {
			return "", "", false
		}
		m, n := string(tree[:sp]), string(tree[sp+1:nul])
		raw := tree[nul+1 : nul+1+size]
		tree = tree[nul+1+size:]
		if n == name {
			return m, hex.EncodeToString(raw), true
		}
	}
	return "", "", false
}

// shortID is the first 12 characters of an object id, for a line.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// readObject is the bytes of the object id of the type kind in the repository at repo, read
// with git cat-file with git's variables env and checked against id: sha1 for an id of 40 hex
// digits, sha256 for one of 64.
func readObject(ctx context.Context, repo string, env []string, id, kind string) ([]byte, error) {
	var h hash.Hash
	switch len(id) {
	case 40:
		h = sha1.New()
	case 64:
		h = sha256.New()
	default:
		return nil, fmt.Errorf("%q is not an object id", id)
	}
	ctx, cancel := context.WithTimeout(ctx, gitLimit)
	defer cancel()
	cmd, err := gitCommand(ctx, repo, env, "cat-file", kind, id)
	if err != nil {
		return nil, err
	}
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = &stderr
	hostgit.Group(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer hostgit.StopGroup(cmd)
	data, readErr := io.ReadAll(io.LimitReader(out, MaxProgramBytes+1))
	if len(data) > MaxProgramBytes {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	switch {
	case len(data) > MaxProgramBytes:
		return nil, &TooLarge{fmt.Sprintf(textTooLarge, kind, shortID(id), MaxProgramBytes)}
	case readErr != nil:
		return nil, readErr
	case waitErr != nil:
		line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		return nil, fmt.Errorf("git cat-file %s %s failed: %s", kind, shortID(id), line)
	}
	fmt.Fprintf(h, "%s %d\x00", kind, len(data))
	h.Write(data)
	if hex.EncodeToString(h.Sum(nil)) != id {
		return nil, fmt.Errorf(textBadObject, kind, shortID(id))
	}
	return data, nil
}
