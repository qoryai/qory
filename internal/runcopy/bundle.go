package runcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxBundle is the largest bundle qory reads back from a run.
// 1 GiB is far above an agent's change, and keeps a planted file from filling the disk.
const MaxBundle = 1 << 30

// ErrNoBundle is a git session that wrote no bundle: the run's branch has no commit past
// its base, so nothing comes back.
var ErrNoBundle = errors.New("no action needed: the run made no commit past its base commit, so the git session wrote no bundle")

// OpenBundle opens the bundle file at path to read: without following a link at path,
// and only when it is a regular file of at most [MaxBundle] bytes. A missing file is
// [ErrNoBundle].
func OpenBundle(path string) (*os.File, error) {
	f, err := openNoFollow(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoBundle
	}
	if err != nil {
		return nil, fmt.Errorf("look at %s: qory reads the run's bundle there only as a regular file, and the open failed, as it does for a link: %w", shown(path), err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("look at %s: qory reads the run's bundle only from a regular file, and it is a %s", shown(path), kind(info.Mode()))
	}
	if info.Size() > MaxBundle {
		f.Close()
		return nil, fmt.Errorf("look at %s: qory reads a bundle of at most %d bytes from a run, and it has %d", shown(path), int64(MaxBundle), info.Size())
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

// Quarantine is the bare repository that a run's bundle is fetched into, with the
// person's objects as its alternates.
type Quarantine struct {
	// Path is the repository.
	Path string
	// Branch is the run's branch, which the fetch wrote there.
	Branch string
	// Head is the branch's commit.
	Head string
}

// Quarantine checks the bundle the git session wrote in [Dir.Bundle], copies it into
// the run's folder, and fetches branch from it into a new bare repository,
// [Dir.QuarantinePath]. Call it once the run's containers are gone. checkout is the
// person's repository: the quarantine takes its objects as alternates, which the run
// never wrote, and its object format. The fetch checks every object it receives,
// transfer.fsckObjects, and writes no tag and no submodule. A bundle that is missing is
// [ErrNoBundle].
func (d Dir) Quarantine(ctx context.Context, checkout, branch string) (*Quarantine, error) {
	if err := checkBranch(ctx, branch); err != nil {
		return nil, err
	}
	checked := filepath.Join(d.root, bundleFile)
	if err := copyBundle(filepath.Join(d.Bundle(), bundleFile), checked); err != nil {
		return nil, err
	}
	common, err := git(ctx, checkout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("check that %s is a git repository: qory fetches the run's commits into it, and git rev-parse failed: %w", shown(checkout), err)
	}
	format, err := git(ctx, checkout, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, err
	}
	q := d.QuarantinePath()
	if _, err := git(ctx, d.root, "init", "--quiet", "--bare", "--template=", "--object-format="+strings.TrimSpace(format), "--", q); err != nil {
		return nil, err
	}
	objects := filepath.Join(strings.TrimSpace(common), "objects")
	if err := writeNew(filepath.Join(q, "objects", "info", "alternates"), objects+"\n"); err != nil {
		return nil, err
	}
	ref := "refs/heads/" + branch
	if _, err := git(ctx, q, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true",
		"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
		"--", checked, ref+":"+ref); err != nil {
		return nil, fmt.Errorf("look at the run's copy in %s: the run's bundle failed git's checks, so no commit of the run reached your repository: %w", shown(d.Work()), err)
	}
	head, err := commitOf(ctx, q, ref)
	if err != nil {
		return nil, err
	}
	return &Quarantine{Path: q, Branch: branch, Head: head}, nil
}

// copyBundle copies the bundle at src, opened by [OpenBundle], to dest, a new file in a
// folder no run binds, and reads at most [MaxBundle] bytes.
func copyBundle(src, dest string) error {
	in, err := OpenBundle(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, MaxBundle+1))
	if err == nil && n > MaxBundle {
		err = fmt.Errorf("look at %s: qory reads a bundle of at most %d bytes from a run, and it grew past that while qory read it", shown(src), int64(MaxBundle))
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeNew writes text to path, a file that must not exist.
func writeNew(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0o600)
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
// at checkout, as ref, a full ref name such as refs/heads/<name>. The fetch checks every
// object, writes no tag and no submodule, and moves ref only forward. git stops the
// fetch when ref is a branch checked out in a working tree of the repository: the
// person's files never change to what the run wrote.
func (q *Quarantine) FetchInto(ctx context.Context, checkout, ref string) error {
	if err := checkRef(ctx, ref); err != nil {
		return err
	}
	_, err := git(ctx, checkout, "-c", "transfer.fsckObjects=true", "-c", "fetch.fsckObjects=true",
		"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
		"--", q.Path, "refs/heads/"+q.Branch+":"+ref)
	if err != nil {
		return fmt.Errorf("fetch %s from %s by hand, or check out another branch: qory fetches the run's commits into %s, and git fetch failed: %w", "refs/heads/"+q.Branch, shown(q.Path), shown(ref), err)
	}
	return nil
}
