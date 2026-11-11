//go:build unix

package runcopy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/qoryai/qory/internal/checkout"
)

// maxCopyBackFiles is the most files [Dir.CopyBack] writes from one run.
// 100,000 files is more than a person reviews from one run; past it, the run wrote a flood.
const maxCopyBackFiles = 100_000

// maxCopyBackBytes is the most bytes [Dir.CopyBack] writes from one run.
// 8 GiB keeps a run from filling the person's disk with files nobody asked for.
const maxCopyBackBytes = 8 << 30

// generatedFolders are the names of folders that a build or a package manager writes. What a
// run writes in such a folder, at any depth, counts nothing toward the budget of [Dir.CopyBack],
// and counts toward [MaxGeneratedFiles] and [MaxGeneratedBytes] instead. A regular file with
// such a name counts toward the budget.
var generatedFolders = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true, "target": true,
	"build": true, "dist": true, ".gradle": true, ".next": true,
}

// MaxGeneratedFiles and MaxGeneratedBytes are the most regular files and bytes, in all folders of
// [generatedFolders] at any depth, that [Dir.CopyBack] writes from one run. Past either, it
// leaves out every such folder, in [CopyBackResult.Generated]. A test sets smaller ones.
var (
	MaxGeneratedFiles       = 1_000_000
	MaxGeneratedBytes int64 = 32 << 30
)

// generated reports whether the folder rel, with forward slashes, is a folder of
// [generatedFolders] or lies in one. For a file, pass the folder it is in.
func generated(rel string) bool {
	for part := range strings.SplitSeq(rel, "/") {
		if generatedFolders[part] {
			return true
		}
	}
	return false
}

// errGeneratedOver stops the count of [generatedOver] past a limit.
var errGeneratedOver = errors.New("past the limit of generated folders")

// generatedOver reports whether the regular files in folders of [generatedFolders] of the folder
// work, at any depth, are more than [MaxGeneratedFiles] or have more than [MaxGeneratedBytes]. It
// reads each entry with lstat and follows no link. It leaves out .git and .qory, as the copy back
// does, and each entry it cannot read, which the copy back lists as failed.
func generatedOver(ctx context.Context, work string) (bool, error) {
	files, bytes := 0, int64(0)
	err := filepath.WalkDir(work, func(p string, e fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case err != nil && e != nil && e.IsDir() && p != work:
			return fs.SkipDir
		case err != nil:
			return nil
		case e.IsDir():
			if p != work && leftOutName(e.Name()) {
				return fs.SkipDir
			}
			return nil
		case !e.Type().IsRegular():
			return nil
		}
		rel, err := filepath.Rel(work, filepath.Dir(p))
		if err != nil || !generated(filepath.ToSlash(rel)) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		files++
		bytes += info.Size()
		if files > MaxGeneratedFiles || bytes > MaxGeneratedBytes {
			return errGeneratedOver
		}
		return nil
	})
	if errors.Is(err, errGeneratedOver) {
		return true, nil
	}
	return false, err
}

// CopyBackResult is what [Dir.CopyBack] did, each path relative to the folder with
// forward slashes, in order.
type CopyBackResult struct {
	// Copied are the new and changed regular files it wrote.
	Copied []string
	// Failed are the entries it left out because a read or a write of them failed. Nothing
	// below such a folder is deleted.
	Failed []string
	// Deleted are the files the run deleted that it deleted in the folder too.
	Deleted []string
	// Conflicts are the files that the run changed and the person changed or deleted since
	// the copy, or that both added with other content. The person's file stays, and the
	// run's version is beside it, at the name with [disarmedSuffix], or with a number after
	// that when the name is taken. [Dir.CopyArtifactsIn] writes no such file.
	Conflicts []string
	// Generated are the folders of [generatedFolders] it left out, each with all below it,
	// because their files pass [MaxGeneratedFiles] or [MaxGeneratedBytes]. Nothing below such a
	// folder is deleted.
	Generated []string
	// NotDeleted are the files that the run deleted and the person changed since the copy.
	// The person's file stays.
	NotDeleted []string
	// Skipped are the entries of the copy it left out: a link, a .git, a .qory, a file
	// that is not regular, a folder that git would take as a git folder, a folder too
	// deep, and a path whose folder in the destination is a link or no folder.
	Skipped []string
	// Acting are the acting keys of the qory files the run changed. Each such file
	// stays as the person has it.
	Acting []ActingChange
}

// copyBack is the state of one [Dir.CopyBack].
type copyBack struct {
	ctx context.Context
	// maxFiles and maxBytes are the budget: [maxCopyBackFiles] and [maxCopyBackBytes].
	maxFiles int
	maxBytes int64
	rec      folderRecord
	result   *CopyBackResult
	qory     map[string]bool
	skipped  map[string]bool
	// in is true for [Dir.CopyArtifactsIn]: a failed read or write stops the walk, and a
	// conflict writes no file beside the other.
	in bool
	// noGenerated is whether the walk leaves out each folder of [generatedFolders], as
	// [generatedOver] found them past their limit.
	noGenerated bool
	// bytes, files and folders are what it wrote and made in the destination outside
	// [generatedFolders], which the budget counts.
	bytes   int64
	files   int
	folders int
}

// overBudget reports whether one more entry of size bytes passes the budget.
func (c *copyBack) overBudget(size int64) bool {
	return c.files+c.folders+1 > c.maxFiles || c.bytes+size > c.maxBytes
}

// fail lists rel among the failed entries and returns nil, so the walk goes on, and nothing at
// or below rel is deleted. It returns err, the failure, when ctx ended, and for
// [Dir.CopyArtifactsIn].
func (c *copyBack) fail(rel string, err error) error {
	if c.in || c.ctx.Err() != nil {
		return err
	}
	c.result.Failed = append(c.result.Failed, rel)
	c.skipped[rel] = true
	return nil
}

// ctxReader stops a read of r once ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// boundedRead is what a copy reads of in: at most size bytes, the file's size when the walk
// hashed it, so a file that grows after that adds nothing, until ctx ends.
func boundedRead(ctx context.Context, in io.Reader, size int64) io.Reader {
	return ctxReader{ctx, io.LimitReader(in, size)}
}

// artifactsFolder is the folder under .qory where qory artifact push keeps versions outside
// a control plane.
const artifactsFolder = "artifacts"

// workItemFile is the file under .qory where qory work-item show reads the work item outside
// a control plane.
const workItemFile = "work-item.json"

// errBudget is a copy-back past [maxCopyBackFiles] or [maxCopyBackBytes].
var errBudget = errors.New("past the budget")

// CopyBack brings the run's changes to the copy of a plain folder, [Dir.Work], into
// dest, the person's folder. Call it once the run's containers are gone.
//
//   - It copies a regular file that the run added or changed since [Dir.CopyFolder]
//     recorded it, when the person did not change it since then. When the person did,
//     the person's file stays, the run's version goes beside it, at the name with
//     [disarmedSuffix] or a free name after that, and the file is listed as a conflict.
//   - It deletes a regular file that was in the folder at the copy, that the person did
//     not change since, and that is gone from the copy. A file that the person changed
//     stays, and is listed as not deleted.
//   - It leaves out every link, every .git and .qory at any depth, every file that is
//     not regular, and every folder that git would take as a git folder, in the copy or
//     once copied.
//   - It compares the acting keys of the qory files at the top, and of the stack files
//     they extend by a path, between the folder and the copy, and leaves out each file
//     whose acting keys changed.
//
// It follows no link on any part of a path, in the copy or in dest: it opens each
// folder from the one above it with O_NOFOLLOW, and keeps at most [maxOpenFolders]
// levels open. It stops past [maxCopyBackFiles] files or [maxCopyBackBytes] bytes, with
// an error that lists what it copied; what it writes in a folder of [generatedFolders] counts
// nothing toward them. When the files in those folders pass [MaxGeneratedFiles] or
// [MaxGeneratedBytes], it leaves each such folder out, in [CopyBackResult.Generated]. A read or a write of an entry that fails lists the entry as failed,
// and the walk goes on. A changed file keeps the permission bits it has
// in dest, with the run's change to the owner's execute bit, and a new one gets the
// process's umask, with the execute bits when the copy has the owner's.
func (d Dir) CopyBack(ctx context.Context, dest string) (CopyBackResult, error) {
	return d.copyBackWith(ctx, dest, maxCopyBackFiles, maxCopyBackBytes)
}

// copyBackWith is [Dir.CopyBack] with a budget of maxFiles files and maxBytes bytes.
func (d Dir) copyBackWith(ctx context.Context, dest string, maxFiles int, maxBytes int64) (CopyBackResult, error) {
	var result CopyBackResult
	rec, err := d.readRecord()
	if err != nil {
		return result, err
	}
	src, err := openRoot(d.Work())
	if err != nil {
		return result, fileErr(d.Work(), err)
	}
	defer unix.Close(src)
	dst, err := unix.Open(dest, dirFlags, 0)
	if err != nil {
		return result, fmt.Errorf(textCopyBackOpen, shown(dest), err)
	}
	defer unix.Close(dst)
	c := &copyBack{ctx: ctx, maxFiles: maxFiles, maxBytes: maxBytes, rec: rec, result: &result, qory: map[string]bool{}, skipped: map[string]bool{}}
	if err := c.compareQory(src, dst); err != nil {
		return result, err
	}
	if c.noGenerated, err = generatedOver(ctx, d.Work()); err != nil {
		return result, fmt.Errorf(textCopyBackCut, shown(d.Work()), "nothing")
	}
	err = c.dir(src, dst, "", 0)
	if errors.Is(err, errBudget) {
		return result, fmt.Errorf(textCopyBackLimit, shown(d.Work()), maxFiles, maxBytes, strings.Join(result.Copied, ", "))
	}
	if ctx.Err() != nil && err != nil {
		return result, fmt.Errorf(textCopyBackCut, shown(d.Work()), strings.Join(result.Copied, ", "))
	}
	if err != nil {
		return result, err
	}
	if err := c.deletions(src, dst); err != nil {
		return result, err
	}
	return result, nil
}

// CopyArtifactsBack copies the regular files of .qory/artifacts in [Dir.Work], where qory
// artifact push keeps versions outside a control plane, into .qory/artifacts of dest, the
// person's checkout or folder. The git session leaves .qory out of its commits, so this
// runs with or without a commit, once the run's containers are gone and before
// [Dir.Remove]. The folder has no qory setting, so no acting key applies.
//
// The rules of [Dir.CopyBack] apply: it follows no link on any part of a path, in the copy
// or in dest, leaves out each link, each .git and .qory at any depth, each file that is not
// regular and each folder that git would take as a git folder, and stops at the same budget.
// A file that the person has with other content stays, the run's version goes beside it, and
// the file is listed as a conflict. It deletes nothing.
func (d Dir) CopyArtifactsBack(ctx context.Context, dest string) (CopyBackResult, error) {
	return d.artifactsBackWith(ctx, dest, maxCopyBackFiles, maxCopyBackBytes)
}

// CopyArtifactsIn copies src's .qory/artifacts and .qory/work-item.json, which qory wrote and
// no run did, into the copy that [Dir.Clone] made of src, before the run starts. A run then
// reads the same artifacts and work item as the person, and numbers its versions after theirs.
// The rules of [Dir.CopyArtifactsBack] apply: a file of the commit with other content stays
// and is listed as a conflict. src itself may be a link, which the person gave.
func (d Dir) CopyArtifactsIn(ctx context.Context, src string) (CopyBackResult, error) {
	return d.artifactsInWith(ctx, src, maxCopyBackFiles, maxCopyBackBytes)
}

// artifactsInWith is [Dir.CopyArtifactsIn] with a budget of maxFiles files and maxBytes bytes.
func (d Dir) artifactsInWith(ctx context.Context, src string, maxFiles int, maxBytes int64) (CopyBackResult, error) {
	var result CopyBackResult
	top, err := unix.Open(src, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return result, fileErr(src, err)
	}
	defer unix.Close(top)
	work, err := openRoot(d.Work())
	if err != nil {
		return result, fileErr(d.Work(), err)
	}
	defer unix.Close(work)
	c := &copyBack{ctx: ctx, maxFiles: maxFiles, maxBytes: maxBytes, rec: folderRecord{}, result: &result, qory: map[string]bool{}, skipped: map[string]bool{}, in: true}
	err = c.artifactsIn(top, work)
	if errors.Is(err, errBudget) {
		return result, fmt.Errorf(textCopyInLimit, shown(filepath.Join(src, checkout.Dir, artifactsFolder)), maxFiles, maxBytes)
	}
	return result, err
}

// artifactsIn is the walk of [Dir.CopyArtifactsIn] from the folder top to the folder work.
func (c *copyBack) artifactsIn(top, work int) error {
	from, to, ok, err := c.enter(top, work, checkout.Dir, checkout.Dir)
	if err != nil || !ok {
		return err
	}
	defer unix.Close(from)
	defer unix.Close(to)
	rel := path.Join(checkout.Dir, workItemFile)
	var st unix.Stat_t
	err = unix.Fstatat(from, workItemFile, &st, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case errors.Is(err, unix.ENOENT):
	case err != nil:
		return fileErr(rel, err)
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		c.skip(rel, false)
	default:
		if err := c.file(from, to, workItemFile, rel, st.Mode&0o100 != 0); err != nil {
			return err
		}
	}
	rel = path.Join(checkout.Dir, artifactsFolder)
	subFrom, subTo, ok, err := c.enter(from, to, artifactsFolder, rel)
	if err != nil || !ok {
		return err
	}
	defer unix.Close(subFrom)
	defer unix.Close(subTo)
	return c.dir(subFrom, subTo, rel, 0)
}

// enter opens the folder name in src and in dst, at rel, and makes it in dst when there is
// none. ok is false when src has none, and when a link or a file stands there in either,
// which it lists as skipped. It follows no link. The caller closes both folders.
func (c *copyBack) enter(src, dst int, name, rel string) (from, to int, ok bool, err error) {
	from, err = unix.Openat(src, name, dirFlags, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return -1, -1, false, nil
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		c.skip(rel, true)
		return -1, -1, false, nil
	case err != nil:
		return -1, -1, false, fileErr(rel, err)
	}
	if err := unix.Mkdirat(dst, name, 0o777); err != nil && !errors.Is(err, unix.EEXIST) {
		unix.Close(from)
		return -1, -1, false, fileErr(rel, err)
	}
	to, err = unix.Openat(dst, name, dirFlags, 0)
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		unix.Close(from)
		c.skip(rel, true)
		return -1, -1, false, nil
	case err != nil:
		unix.Close(from)
		return -1, -1, false, fileErr(rel, err)
	}
	return from, to, true, nil
}

// hasArtifacts reports whether the folder src has .qory/artifacts, of any kind: with none,
// the copy back writes no .qory in the person's folder. It is true when the check fails, so
// [copyBack.enter] reports the failure.
func hasArtifacts(src int) bool {
	qory, err := unix.Openat(src, checkout.Dir, dirFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		return false
	}
	if err != nil {
		return true
	}
	defer unix.Close(qory)
	var st unix.Stat_t
	return !errors.Is(unix.Fstatat(qory, artifactsFolder, &st, unix.AT_SYMLINK_NOFOLLOW), unix.ENOENT)
}

// artifactsBackWith is [Dir.CopyArtifactsBack] with a budget of maxFiles files and maxBytes
// bytes.
func (d Dir) artifactsBackWith(ctx context.Context, dest string, maxFiles int, maxBytes int64) (CopyBackResult, error) {
	var result CopyBackResult
	rec, err := d.readRecord()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A clone has no record: only a plain folder's copy started with the person's files.
		rec = folderRecord{}
	case err != nil:
		return result, err
	}
	src, err := openRoot(d.Work())
	if err != nil {
		return result, fileErr(d.Work(), err)
	}
	defer unix.Close(src)
	dst, err := unix.Open(dest, dirFlags, 0)
	if err != nil {
		return result, fmt.Errorf(textCopyBackOpen, shown(dest), err)
	}
	defer unix.Close(dst)
	c := &copyBack{ctx: ctx, maxFiles: maxFiles, maxBytes: maxBytes, rec: rec, result: &result, qory: map[string]bool{}, skipped: map[string]bool{}}
	if !hasArtifacts(src) {
		return result, nil
	}
	from, to, ok, err := c.enter(src, dst, checkout.Dir, checkout.Dir)
	if err != nil || !ok {
		return result, err
	}
	defer unix.Close(from)
	defer unix.Close(to)
	rel := path.Join(checkout.Dir, artifactsFolder)
	subFrom, subTo, ok, err := c.enter(from, to, artifactsFolder, rel)
	if err != nil || !ok {
		return result, err
	}
	defer unix.Close(subFrom)
	defer unix.Close(subTo)
	from, to = subFrom, subTo
	err = c.dir(from, to, rel, 0)
	if errors.Is(err, errBudget) {
		return result, fmt.Errorf(textCopyBackLimit, shown(d.Work()), maxFiles, maxBytes, strings.Join(result.Copied, ", "))
	}
	if ctx.Err() != nil && err != nil {
		return result, fmt.Errorf(textCopyBackCut, shown(d.Work()), strings.Join(result.Copied, ", "))
	}
	return result, err
}

// compareQory lists the acting changes of the qory files between dst, the person's
// folder, and src, the copy, and marks each changed file to leave out.
func (c *copyBack) compareQory(src, dst int) error {
	srcTop, err := readNames(src)
	if err != nil {
		return err
	}
	dstTop, err := readNames(dst)
	if err != nil {
		return err
	}
	changes, err := qoryChanges(dstTop, folderReader(dst), srcTop, folderReader(src))
	if err != nil {
		return err
	}
	c.result.Acting = changes
	for _, ch := range changes {
		c.qory[folded(ch.File)] = true
	}
	return nil
}

// folderReader reads a qory file in the folder root, following no link.
func folderReader(root int) readQoryFile {
	return func(rel string) (qoryFile, []byte, error) {
		dir, name, err := lookup(root, rel)
		if errors.Is(err, errNoEntry) {
			return qoryFile{shape: shapeMissing}, nil, nil
		}
		if err != nil {
			return qoryFile{}, nil, err
		}
		defer unix.Close(dir)
		var st unix.Stat_t
		err = unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			return qoryFile{shape: shapeMissing}, nil, nil
		}
		if err != nil {
			return qoryFile{}, nil, &os.PathError{Op: "lstat", Path: rel, Err: err}
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFLNK:
			buf := make([]byte, unix.PathMax)
			n, err := unix.Readlinkat(dir, name, buf)
			if err != nil {
				return qoryFile{}, nil, &os.PathError{Op: "readlink", Path: rel, Err: err}
			}
			return qoryFile{shape: shapeOther, id: "link " + string(buf[:n])}, nil, nil
		case unix.S_IFREG:
			f, err := openFileAt(dir, name, rel)
			if err != nil {
				return qoryFile{}, nil, err
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, maxQoryFile+1))
			if err != nil {
				return qoryFile{}, nil, err
			}
			sum := sha256.Sum256(data)
			id := hex.EncodeToString(sum[:])
			if len(data) > maxQoryFile {
				return qoryFile{shape: shapeOther, id: "large " + id}, nil, nil
			}
			return qoryFile{shape: shapeYAML, id: id}, data, nil
		}
		return qoryFile{shape: shapeOther, id: "other"}, nil, nil
	}
}

// dir copies back the folder src, at rel, "" for the top, into the folder dst.
func (c *copyBack) dir(src, dst int, rel string, depth int) error {
	shownRel := rel
	if shownRel == "" {
		shownRel = "."
	}
	if depth > maxOpenFolders {
		c.skip(shownRel, true)
		return nil
	}
	names, err := readNames(src)
	if err != nil {
		return c.fail(shownRel, fileErr(shownRel, err))
	}
	have, err := readNames(dst)
	if err != nil {
		return c.fail(shownRel, fileErr(shownRel, err))
	}
	if gitDirLike(names) || gitDirLike(append(slices.Clone(names), have...)) {
		c.skip(shownRel, true)
		return nil
	}
	for _, name := range names {
		if err := c.ctx.Err(); err != nil {
			return err
		}
		at := path.Join(rel, name)
		if leftOutName(name) {
			c.skip(at, true)
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstatat(src, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if err := c.fail(at, fileErr(at, err)); err != nil {
				return err
			}
			continue
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if err := c.sub(src, dst, name, at, depth); err != nil {
				return err
			}
		case unix.S_IFREG:
			if c.qory[folded(at)] {
				continue
			}
			if err := c.file(src, dst, name, at, st.Mode&0o100 != 0); err != nil {
				return err
			}
		case unix.S_IFLNK:
			// A link the folder had at the copy, with the same target, is no change of the run.
			if !c.sameLink(src, name, at) {
				c.skip(at, false)
			}
		default:
			c.skip(at, false)
		}
	}
	return nil
}

// sameLink reports whether the link name in src, at rel, has the target the record has for
// rel.
func (c *copyBack) sameLink(src int, name, rel string) bool {
	was, recorded := c.rec.Links[rel]
	if !recorded {
		return false
	}
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(src, name, buf)
	return err == nil && string(buf[:n]) == was
}

// skip lists rel among the skipped entries. When folder is true, nothing below rel is
// deleted.
func (c *copyBack) skip(rel string, folder bool) {
	c.result.Skipped = append(c.result.Skipped, rel)
	if folder {
		c.skipped[rel] = true
	}
}

// sub copies back the folder name in src, at rel, into the folder of that name in dst,
// which it makes when there is none. A folder of [generatedFolders] counts nothing toward the
// budget, and stays out with [copyBack.noGenerated].
func (c *copyBack) sub(src, dst int, name, rel string, depth int) error {
	if c.noGenerated && generatedFolders[name] {
		c.result.Generated = append(c.result.Generated, rel)
		c.skipped[rel] = true
		return nil
	}
	from, err := unix.Openat(src, name, dirFlags, 0)
	if err != nil {
		return c.fail(rel, fileErr(rel, err))
	}
	defer unix.Close(from)
	names, err := readNames(from)
	if err != nil {
		return c.fail(rel, fileErr(rel, err))
	}
	if gitDirLike(names) {
		// Before the folder is made in dst, so not even an empty one is.
		c.skip(rel, true)
		return nil
	}
	switch err := unix.Mkdirat(dst, name, 0o777); {
	case err == nil:
		if !generated(rel) {
			c.folders++
			if c.overBudget(0) {
				return errBudget
			}
		}
	case !errors.Is(err, unix.EEXIST):
		return c.fail(rel, fileErr(rel, err))
	}
	to, err := unix.Openat(dst, name, dirFlags, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		// A link or a file of the person's stands there.
		c.skip(rel, true)
		return nil
	}
	if err != nil {
		return c.fail(rel, fileErr(rel, err))
	}
	defer unix.Close(to)
	return c.dir(from, to, rel, depth+1)
}

// file copies back the regular file name in src, at rel, whose owner may run it when
// exec is true, into dst, by the rules of [Dir.CopyBack]. A file in a folder of
// [generatedFolders] counts nothing toward the budget.
func (c *copyBack) file(src, dst int, name, rel string, exec bool) error {
	in, err := openFileAt(src, name, rel)
	if err != nil {
		return c.fail(rel, fileErr(rel, err))
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() {
		c.skip(rel, false)
		return nil
	}
	was, recorded := c.rec.Files[rel]
	counted := !generated(path.Dir(rel))
	// A size past the budget stops the copy before any hash reads the file.
	if counted && !(recorded && info.Size() == was.Size) && c.overBudget(info.Size()) {
		return errBudget
	}
	now, err := hashState(boundedRead(c.ctx, in, info.Size()), exec)
	if err != nil {
		return c.fail(rel, fileErr(rel, err))
	}
	if recorded && now == was {
		return nil
	}
	have, perm, err := destState(dst, name, rel)
	switch {
	case errors.Is(err, errNotRegular):
		c.skip(rel, false)
		return nil
	case err != nil:
		return c.fail(rel, fileErr(rel, err))
	case have != nil && *have == now:
		return nil
	}
	conflict := recorded && (have == nil || *have != was) || !recorded && have != nil
	if conflict && c.in {
		c.result.Conflicts = append(c.result.Conflicts, rel)
		return nil
	}
	if counted && c.overBudget(now.Size) {
		return errBudget
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return c.fail(rel, fileErr(rel, err))
	}
	to, mode, replaces := name, uint32(0o666), have != nil && !conflict
	if exec {
		mode = 0o777
	}
	if conflict {
		// The run's version goes beside the person's, at a name that neither folder has.
		if to, err = fromRunName(name, rel, dst, src); err != nil {
			return c.fail(rel, err)
		}
	}
	if replaces {
		mode = perm
		switch {
		case now.Exec && !was.Exec:
			mode |= (mode & 0o444) >> 2
		case !now.Exec && was.Exec:
			mode &^= 0o111
		}
	}
	// At most what the hash read, which the budget counted.
	if err := writeAt(dst, to, rel, boundedRead(c.ctx, in, now.Size), mode, replaces); err != nil {
		return c.fail(rel, err)
	}
	if counted {
		c.bytes += now.Size
		c.files++
	}
	if conflict {
		c.result.Conflicts = append(c.result.Conflicts, rel)
	} else {
		c.result.Copied = append(c.result.Copied, rel)
	}
	return nil
}

// errNotRegular is an entry in the destination that is no regular file.
var errNotRegular = errors.New("no regular file")

// destState is the state of the entry name in dst and its permission bits: nil when
// there is none, and [errNotRegular] when it is no regular file.
func destState(dst int, name, rel string) (*fileState, uint32, error) {
	var st unix.Stat_t
	err := unix.Fstatat(dst, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil, 0, nil
	case err != nil:
		return nil, 0, err
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return nil, 0, errNotRegular
	}
	f, err := openFileAt(dst, name, rel)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	state, err := hashState(f, st.Mode&0o100 != 0)
	if err != nil {
		return nil, 0, err
	}
	return &state, uint32(st.Mode) & 0o7777, nil
}

// deletions deletes in dst each recorded file that is gone from src, by the rules of
// [Dir.CopyBack]. A read or a delete of an entry that fails lists the entry as failed, and the
// deletions go on.
func (c *copyBack) deletions(src, dst int) error {
	paths := make([]string, 0, len(c.rec.Files))
	for p := range c.rec.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		if !cleanRel(rel) || c.leftOut(rel) {
			continue
		}
		if err := c.deletion(src, dst, rel); err != nil {
			return err
		}
	}
	sort.Strings(c.result.Conflicts)
	return nil
}

// deletion deletes the recorded file rel in dst when it is gone from src, by the rules of
// [Dir.CopyBack]. It returns an error only as [copyBack.fail] does.
func (c *copyBack) deletion(src, dst int, rel string) error {
	gone, err := missingAt(src, rel)
	if err != nil {
		return c.fail(rel, fileErr(rel, err))
	}
	if !gone {
		return nil
	}
	dir, name, err := lookup(dst, rel)
	if errors.Is(err, errNoEntry) {
		return nil
	}
	if err != nil {
		return c.fail(rel, fileErr(rel, err))
	}
	defer unix.Close(dir)
	have, _, err := destState(dir, name, rel)
	switch {
	case errors.Is(err, errNotRegular) || err == nil && have != nil && *have != c.rec.Files[rel]:
		c.result.NotDeleted = append(c.result.NotDeleted, rel)
	case err != nil:
		return c.fail(rel, fileErr(rel, err))
	case have != nil:
		if err := unix.Unlinkat(dir, name, 0); err != nil {
			return c.fail(rel, fileErr(rel, err))
		}
		c.result.Deleted = append(c.result.Deleted, rel)
	}
	return nil
}

// leftOut reports whether rel, or a folder above it, is left out of the copy-back: a
// .git or .qory part, a skipped folder, or a qory file whose acting keys changed.
func (c *copyBack) leftOut(rel string) bool {
	if c.qory[folded(rel)] {
		return true
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if leftOutName(part) || c.skipped[strings.Join(parts[:i+1], "/")] {
			return true
		}
	}
	return c.skipped["."]
}

// missingAt reports whether root has no entry at rel, following no link.
func missingAt(root int, rel string) (bool, error) {
	dir, name, err := lookup(root, rel)
	if errors.Is(err, errNoEntry) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer unix.Close(dir)
	var st unix.Stat_t
	err = unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	return false, err
}

// writeAt writes what in has to a new file in dst and renames it to name, over what
// is there. A file that replaces another gets perm as it is; a new one gets perm under
// the umask.
func writeAt(dst int, name, rel string, in io.Reader, perm uint32, replaces bool) error {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	tmp := ".qory-copy-" + hex.EncodeToString(b)
	fd, err := unix.Openat(dst, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, perm)
	if err != nil {
		return fileErr(rel, err)
	}
	out := os.NewFile(uintptr(fd), rel)
	_, err = io.Copy(out, in)
	if err == nil && replaces {
		err = out.Chmod(os.FileMode(perm & 0o777))
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = unix.Renameat(dst, tmp, dst, name)
	}
	if err != nil {
		_ = unix.Unlinkat(dst, tmp, 0)
		return fileErr(rel, err)
	}
	return nil
}
