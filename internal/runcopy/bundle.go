package runcopy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/qoryai/qory/internal/checkout"
)

// MaxBundle is the largest bundle qory reads back from a run.
// 1 GiB is far above an agent's change, and keeps a planted file from filling the disk.
const MaxBundle = 1 << 30

// maxBundleHeader is the largest header of a bundle, its refs and prerequisites, that
// qory reads when it checks the pack itself.
const maxBundleHeader = 1 << 20

// ErrNoBundle is a git session that recorded, for every repository, that the run made
// no commit past its base, so nothing comes back.
var ErrNoBundle = errors.New("no action needed: the run made no commit past its base commit")

// OpenBundle opens the bundle file at path to read: only when it is a regular file of
// at most [MaxBundle] bytes, as lstat reads it, and when the file opened, without
// following a link, is that same file by device and inode.
func OpenBundle(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf(textBundleOpen, shown(path), err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf(textBundleKind, shown(path), kind(before.Mode()))
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf(textBundleOpen, shown(path), err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf(textBundleOpen, shown(path), err)
	}
	if !os.SameFile(before, info) || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf(textBundleMoved, shown(path))
	}
	if info.Size() > MaxBundle {
		f.Close()
		return nil, fmt.Errorf(textBundleSize, shown(path), int64(MaxBundle), info.Size())
	}
	return f, nil
}

// kind is the kind of file mode is, as a line prints it.
func kind(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "folder"
	case mode&fs.ModeSymlink != 0:
		return "link"
	case mode&fs.ModeNamedPipe != 0:
		return "named pipe"
	case mode&fs.ModeSocket != 0:
		return "socket"
	case mode&fs.ModeDevice != 0:
		return "device"
	}
	return "file of another kind"
}

// Quarantine is the bare repository that one repository's bundle is fetched into, with
// the bare source of that repository as its alternates.
type Quarantine struct {
	// Path is the quarantine repository.
	Path string
	// Repo is the repository of the copy, as [Dir.Clone] returned it.
	Repo Repository
	// Branch is the run's branch, which the fetch wrote in the quarantine.
	Branch string
	// Head is the branch's commit.
	Head string
}

// Quarantine checks the bundle the git session wrote for each repository of repos, as
// [Dir.Clone] returned them, copies it into the run's folder, and fetches branch from it
// into a new bare repository whose alternates are the bare source of that repository,
// which no run wrote. Call it once the run's containers are gone; ctx sets how long it
// may take. It returns a quarantine per repository, nil for one whose git session
// recorded no commit past its base, and [ErrNoBundle] when that is true for all of them.
//
// The fetch checks every object it receives: by transfer.fsckObjects from git 2.48 on,
// and by git index-pack --strict on an older git, whose fetch from a bundle checks
// none. A head that adds an entry at the top of the repository named .git or .qory, in
// any letter case, does not reach the quarantine's caller. A second call makes the
// quarantines again.
func (d Dir) Quarantine(ctx context.Context, branch string, repos []Repository) ([]*Quarantine, error) {
	if err := checkBranch(ctx, branch); err != nil {
		return nil, err
	}
	top := filepath.Join(d.root, quarantineName)
	if err := os.RemoveAll(top); err != nil {
		return nil, fileErr(top, err)
	}
	if err := os.Mkdir(top, 0o700); err != nil {
		return nil, fileErr(top, err)
	}
	out := make([]*Quarantine, len(repos))
	found := false
	for i, r := range repos {
		rel, err := repositoryRel(r)
		if err != nil {
			return nil, err
		}
		none := filepath.Join(d.Bundle(), strconv.Itoa(i)+".none")
		if info, err := os.Lstat(none); err == nil && info.Mode().IsRegular() {
			continue
		}
		bundle := filepath.Join(d.Bundle(), strconv.Itoa(i)+".bundle")
		if _, err := os.Lstat(bundle); errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf(textNoBundle, shown(rel))
		}
		q, err := d.quarantineOne(ctx, i, r, rel, branch, bundle)
		if err != nil {
			return nil, err
		}
		out[i], found = q, true
	}
	if !found {
		return nil, ErrNoBundle
	}
	return out, nil
}

// quarantineOne is [Dir.Quarantine] for the repository r, number i, at rel in the copy.
func (d Dir) quarantineOne(ctx context.Context, i int, r Repository, rel, branch, bundle string) (*Quarantine, error) {
	checked := filepath.Join(d.root, quarantineName, strconv.Itoa(i)+".bundle")
	if err := copyBundle(bundle, checked); err != nil {
		return nil, err
	}
	source := d.source(r.index)
	format, err := git(ctx, source, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, fmt.Errorf(textGitInRepo, shown(source), err)
	}
	q := d.quarantine(r.index)
	if _, err := git(ctx, d.root, "init", "--quiet", "--bare", "--template=", "--object-format="+strings.TrimSpace(format), "--", q); err != nil {
		return nil, fmt.Errorf(textQuarantine, shown(q), err)
	}
	if err := writeNew(filepath.Join(q, "objects", "info", "alternates"), filepath.Join(source, "objects")+"\n"); err != nil {
		return nil, fileErr(q, err)
	}
	ref := "refs/heads/" + branch
	if bundleFetchChecks(ctx) {
		_, err = git(ctx, q, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true", "-c", "fetch.fsck.hasDotgit=error",
			"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
			"--", checked, ref+":"+ref)
	} else {
		err = indexBundle(ctx, q, checked, ref)
	}
	if err != nil {
		return nil, fmt.Errorf(textBundleChecks, shown(d.Work()), shown(rel), err)
	}
	head, err := commitOf(ctx, q, ref)
	if err != nil {
		return nil, fmt.Errorf(textBundleChecks, shown(d.Work()), shown(rel), err)
	}
	quarantine := &Quarantine{Path: q, Repo: r, Branch: branch, Head: head}
	if err := quarantine.checkTop(ctx, d.Work(), rel); err != nil {
		return nil, err
	}
	return quarantine, nil
}

// checkTop returns an error when the head's tree has an entry at the top whose name is
// .git or .qory in any letter case, other than the same entry at the base commit.
func (q *Quarantine) checkTop(ctx context.Context, work, rel string) error {
	head, err := q.treeEntries(ctx, q.Head+"^{tree}", "")
	if err != nil {
		return fmt.Errorf(textBundleChecks, shown(work), shown(rel), err)
	}
	base, err := q.treeEntries(ctx, q.Repo.Base+"^{tree}", "")
	if err != nil {
		return fmt.Errorf(textBundleChecks, shown(work), shown(rel), err)
	}
	for _, e := range head {
		if !folds(e.name, ".git") && !folds(e.name, checkout.Dir) {
			continue
		}
		kept := false
		for _, b := range base {
			kept = kept || b == e
		}
		if !kept {
			return fmt.Errorf(textTopEntry, shown(work), q.Head, shown(rel), shown(e.name))
		}
	}
	return nil
}

// indexBundle fetches the branch ref from the bundle at path into the bare repository
// q for a git whose fetch from a bundle checks no object: it reads the bundle's header,
// passes its pack to git index-pack --strict --fsck-objects, which checks every object
// and fails on a broken one, sets ref, and checks with git rev-list that every object
// the branch reaches past the bundle's prerequisites is there.
func indexBundle(ctx context.Context, q, path, ref string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head, prereqs, offset, err := bundleHeader(f, ref)
	if err != nil {
		return err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	if _, err := gitWith(ctx, q, nil, f, "index-pack", "--stdin", "--fix-thin", "--strict", "--fsck-objects"); err != nil {
		return err
	}
	if _, err := git(ctx, q, "update-ref", ref, head); err != nil {
		return err
	}
	args := []string{"rev-list", "--objects", "--quiet", head}
	if len(prereqs) > 0 {
		args = append(append(args, "--not"), prereqs...)
	}
	_, err = git(ctx, q, args...)
	return err
}

// bundleHeader reads the header of the bundle r: the commit of ref, the prerequisite
// commits, and the offset where the pack starts.
func bundleHeader(r io.Reader, ref string) (string, []string, int64, error) {
	br := bufio.NewReader(io.LimitReader(r, maxBundleHeader))
	var offset int64
	line := func() (string, error) {
		s, err := br.ReadString('\n')
		offset += int64(len(s))
		if err != nil {
			return "", errBundleHeader
		}
		return strings.TrimSuffix(s, "\n"), nil
	}
	first, err := line()
	if err != nil || first != "# v2 git bundle" && first != "# v3 git bundle" {
		return "", nil, 0, errBundleHeader
	}
	head := ""
	var prereqs []string
	for {
		s, err := line()
		if err != nil {
			return "", nil, 0, err
		}
		switch {
		case s == "":
			if head == "" {
				return "", nil, 0, errBundleHeader
			}
			return head, prereqs, offset, nil
		case strings.HasPrefix(s, "@"):
			// A capability: the object format, or a filter, which a partial bundle has.
			if !strings.HasPrefix(s, "@object-format=") {
				return "", nil, 0, errBundleHeader
			}
		case strings.HasPrefix(s, "-"):
			id, _, _ := strings.Cut(s[1:], " ")
			if !objectID.MatchString(id) {
				return "", nil, 0, errBundleHeader
			}
			prereqs = append(prereqs, id)
		default:
			id, name, _ := strings.Cut(s, " ")
			if !objectID.MatchString(id) {
				return "", nil, 0, errBundleHeader
			}
			if name == ref {
				head = id
			}
		}
	}
}

// errBundleHeader is a bundle whose header is not a git bundle's.
var errBundleHeader = errors.New("the bundle's header is not a git bundle's, or it has no ref of the run's branch")

// copyBundle copies the bundle at src, opened by [OpenBundle], to dest, a new file in a
// folder no run binds, and reads at most [MaxBundle] bytes.
func copyBundle(src, dest string) error {
	in, err := OpenBundle(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return fileErr(dest, err)
	}
	n, err := io.Copy(out, io.LimitReader(in, MaxBundle+1))
	if err == nil && n > MaxBundle {
		err = fmt.Errorf(textBundleGrew, shown(src), int64(MaxBundle))
	} else if err != nil {
		err = fileErr(dest, err)
	}
	if cerr := out.Close(); err == nil && cerr != nil {
		err = fileErr(dest, cerr)
	}
	return err
}

// writeNew writes text to path, a file that must not exist.
func writeNew(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// FetchInto fetches the run's branch from the quarantine into the person's repository
// at checkout, as ref, a full ref name such as refs/heads/qory/run-<run id>. ctx sets
// how long it may take. The fetch checks every object, writes no tag and no submodule,
// and moves ref only forward. git stops the fetch when ref is a branch checked out in a
// working tree of the repository: the person's files never change to what the run
// wrote. When the fetch fails, the quarantine stays, and the error contains its path.
func (q *Quarantine) FetchInto(ctx context.Context, checkout, ref string) error {
	if err := checkRef(ctx, ref); err != nil {
		return err
	}
	from := "refs/heads/" + q.Branch
	_, err := git(ctx, checkout, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true",
		"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
		"--", q.Path, from+":"+ref)
	if err == nil {
		return nil
	}
	if at := checkedOut(ctx, checkout, ref); at != "" {
		return fmt.Errorf(textFetchCheckedOut, shown(ref), shown(at), from, shown(q.Path), err)
	}
	if _, verr := git(ctx, checkout, "rev-parse", "--verify", "--quiet", "--end-of-options", ref); verr == nil {
		return fmt.Errorf(textFetchExists, shown(q.Path), shown(ref), shown(checkout), from, err)
	}
	return fmt.Errorf(textFetchFailed, shown(q.Path), shown(ref), shown(checkout), from, err)
}

// checkedOut is the working tree of the repository at checkout that has ref checked
// out, by git worktree list; "" for none or when the list fails.
func checkedOut(ctx context.Context, checkout, ref string) string {
	out, err := git(ctx, checkout, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return ""
	}
	tree := ""
	for field := range strings.SplitSeq(out, "\x00") {
		if p, ok := strings.CutPrefix(field, "worktree "); ok {
			tree = p
		}
		if field == "branch "+ref {
			return tree
		}
	}
	return ""
}
