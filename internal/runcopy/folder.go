package runcopy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/qoryai/forager/session"

	"github.com/qoryai/qory/internal/preparation"
)

// fileState is what a regular file has: its size, whether the owner may run it, and
// the SHA-256 sum of its content.
type fileState struct {
	Size   int64  `json:"size"`
	Exec   bool   `json:"exec"`
	SHA256 string `json:"sha256"`
}

// folderRecord is the record of the regular files [Dir.CopyFolder] copied, by path
// relative to the folder with forward slashes.
type folderRecord struct {
	Files map[string]fileState `json:"files"`
	// Links is the target of each link the copy has.
	Links map[string]string `json:"links"`
}

// The most [Dir.CopyFolder] copies of a folder: a person's folder can be larger than what a
// run brings back, and the copy has to fit on this machine's disk.
const (
	maxCopyFolderEntries = 1_000_000
	maxCopyFolderBytes   = 64 << 30
)

// folderCopy is the state of one [Dir.CopyFolder].
type folderCopy struct {
	ctx      context.Context
	from, to *os.Root
	rec      folderRecord
	// leave is the path left out, "" for none.
	leave string
	// maxEntries and maxBytes are the budget, entries and bytes what it used.
	maxEntries int
	maxBytes   int64
	entries    int
	bytes      int64
}

// errFolderBudget is a folder past the budget of [Dir.CopyFolder].
var errFolderBudget = errors.New("past the budget")

// CopyFolder copies the folder src, which is not a repository, into [Dir.Work]: its
// folders, its regular files with their permission bits and times, and its links as
// links, never followed. Any other kind of file, such as a pipe or a socket, stays out.
// It records the size, the owner's execute bit and a hash of each regular file in the
// run's folder, for [Dir.CopyBack]. src must not contain the run's folder.
func (d Dir) CopyFolder(ctx context.Context, src string) error {
	return d.copyFolderOf(ctx, src, "", maxCopyFolderEntries, maxCopyFolderBytes)
}

// CopyFolderWithoutGit is [Dir.CopyFolder] of a folder that git init set up and that has no
// commit, for a run that has no repository: the .git at its top stays out of the copy.
func (d Dir) CopyFolderWithoutGit(ctx context.Context, src string) error {
	return d.copyFolderOf(ctx, src, gitDirName, maxCopyFolderEntries, maxCopyFolderBytes)
}

// gitDirName is the git folder of a working tree.
const gitDirName = ".git"

// NoCommits reports whether the repository at dir has no commit on any ref, as after git
// init. A failure of git is no such repository: [Dir.Clone] then prints what is wrong.
func NoCommits(ctx context.Context, dir string) bool {
	out, err := git(ctx, dir, "rev-list", "-n", "1", "--all")
	return err == nil && strings.TrimSpace(out) == ""
}

// copyFolderOf is [Dir.CopyFolder] with the path leave, "" for none, left out, and a budget of
// maxEntries entries and maxBytes bytes.
func (d Dir) copyFolderOf(ctx context.Context, src, leave string, maxEntries int, maxBytes int64) error {
	src, err := filepath.Abs(src)
	if err != nil {
		return fileErr(src, err)
	}
	info, err := os.Lstat(src)
	if err != nil {
		return fileErr(src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(textFolderOnly, shown(src))
	}
	if session.Overlap(src, d.root) != "" {
		return fmt.Errorf(textFolderHolds, shown(d.root))
	}
	work := d.Work()
	if err := os.Mkdir(work, info.Mode().Perm()|0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf(textExists, shown(work))
		}
		return fileErr(work, err)
	}
	from, err := os.OpenRoot(src)
	if err != nil {
		return fmt.Errorf(textNoFolderCopy, shown(src), err)
	}
	defer from.Close()
	to, err := os.OpenRoot(work)
	if err != nil {
		return fileErr(work, err)
	}
	defer to.Close()
	rec := folderRecord{Files: map[string]fileState{}, Links: map[string]string{}}
	c := &folderCopy{ctx: ctx, from: from, to: to, rec: rec, leave: leave, maxEntries: maxEntries, maxBytes: maxBytes}
	if err := c.folder("."); err != nil {
		if errors.Is(err, errFolderBudget) {
			return fmt.Errorf(textFolderLimit, maxEntries, maxBytes, shown(src))
		}
		return fmt.Errorf(textNoFolderCopy, shown(src), err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	p := filepath.Join(d.root, recordName)
	if err := writeNew(p, string(data)); err != nil {
		return fileErr(p, err)
	}
	return nil
}

// folder copies the entries of the folder rel into the copy, and records each regular file and
// each link.
func (c *folderCopy) folder(rel string) error {
	f, err := c.from.Open(rel)
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		p := path.Join(rel, e.Name())
		if p == c.leave {
			continue
		}
		if err := c.ctx.Err(); err != nil {
			return err
		}
		fi, err := c.from.Lstat(p)
		if err != nil {
			return err
		}
		c.entries++
		if c.entries > c.maxEntries || c.bytes+fi.Size() > c.maxBytes {
			return errFolderBudget
		}
		switch mode := fi.Mode(); {
		case mode&fs.ModeSymlink != 0:
			target, err := c.from.Readlink(p)
			if err != nil {
				return err
			}
			if err := c.to.Symlink(target, p); err != nil {
				return err
			}
			c.rec.Links[p] = target
		case mode.IsDir():
			if err := c.to.Mkdir(p, 0o700); err != nil {
				return err
			}
			if err := c.folder(p); err != nil {
				return err
			}
			// The owner keeps write access, so the run can write in every folder.
			if err := c.to.Chmod(p, mode.Perm()|0o700); err != nil {
				return err
			}
		case mode.IsRegular():
			c.bytes += fi.Size()
			if err := preparation.CopyFile(c.from, c.to, p); err != nil {
				return err
			}
			if err := preparation.KeepMode(c.to, p, fi); err != nil {
				return err
			}
			state, err := stateOf(c.to, p, mode)
			if err != nil {
				return err
			}
			c.rec.Files[p] = state
		}
	}
	return nil
}

// stateOf is the state of the regular file name in root, whose mode is mode.
func stateOf(root *os.Root, name string, mode fs.FileMode) (fileState, error) {
	f, err := root.Open(name)
	if err != nil {
		return fileState{}, err
	}
	defer f.Close()
	return hashState(f, mode.Perm()&0o100 != 0)
}

// hashState is the state of the content r has, with exec as the owner's execute bit.
func hashState(r io.Reader, exec bool) (fileState, error) {
	sum := sha256.New()
	n, err := io.Copy(sum, r)
	if err != nil {
		return fileState{}, err
	}
	return fileState{Size: n, Exec: exec, SHA256: hex.EncodeToString(sum.Sum(nil))}, nil
}

// readRecord reads the record [Dir.CopyFolder] wrote.
func (d Dir) readRecord() (folderRecord, error) {
	p := filepath.Join(d.root, recordName)
	data, err := os.ReadFile(p)
	if err != nil {
		return folderRecord{}, fmt.Errorf(textRecord, shown(p), err)
	}
	var rec folderRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return folderRecord{}, fmt.Errorf(textRecord, shown(p), err)
	}
	if rec.Files == nil {
		rec.Files = map[string]fileState{}
	}
	return rec, nil
}
