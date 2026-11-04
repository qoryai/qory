package runcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/qoryai/qory/internal/checkout"
)

// repositories is the folder, relative to the checkout, that the further repositories
// of a run are cloned under, as preparation.Repositories sets it. This package does not
// import preparation, whose push step may come to use it.
const repositories = checkout.Dir + "/repositories"

// Repository is one repository of the copy and the commit it starts at.
type Repository struct {
	// Path is the repository's folder relative to the copy, with forward slashes: "" for
	// the person's repository, and .qory/repositories/<path> for a further one.
	Path string
	// Base is the commit the copy checks out: the one the person's checkout has at HEAD.
	Base string
}

// Clone clones the person's repository at checkout, the top of its working tree, into
// [Dir.Work], and checks out branch there at the commit the checkout has at HEAD. It
// clones each repository under .qory/repositories in the checkout the same way, into
// the same path in the copy, at that repository's HEAD. It returns the repositories,
// the person's first.
//
// Each clone is git clone --no-local, which copies the objects and links none of them
// into the person's .git, and never --shared or --reference; git runs as [git] runs it,
// with hooks off. The copy has commits alone: no uncommitted, untracked or ignored file
// of the checkout, and no submodule checked out.
func (d Dir) Clone(ctx context.Context, checkout, branch string) ([]Repository, error) {
	if err := checkBranch(ctx, branch); err != nil {
		return nil, err
	}
	checkout, err := filepath.Abs(checkout)
	if err != nil {
		return nil, err
	}
	main, err := cloneOne(ctx, d.root, checkout, d.Work(), branch)
	if err != nil {
		return nil, err
	}
	repos := []Repository{{Base: main}}
	further, err := furtherRepositories(checkout)
	if err != nil {
		return nil, err
	}
	for _, rel := range further {
		src := filepath.Join(checkout, filepath.FromSlash(rel))
		dest := filepath.Join(d.Work(), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, err
		}
		base, err := cloneOne(ctx, d.root, src, dest, branch)
		if err != nil {
			return nil, err
		}
		repos = append(repos, Repository{Path: rel, Base: base})
	}
	return repos, nil
}

// cloneOne clones the repository at src into dest, a path that does not exist, and
// checks out branch at the commit src has at HEAD, which it returns. git clone runs in
// root, which is in no repository.
func cloneOne(ctx context.Context, root, src, dest, branch string) (string, error) {
	base, err := commitOf(ctx, src, "HEAD")
	if err != nil {
		return "", fmt.Errorf("commit in %s, or start the run in a repository with a commit at HEAD: the run works in a copy of the commit at HEAD, and git found none: %w", shown(src), err)
	}
	if _, err := git(ctx, root, "clone", "--quiet", "--no-local", "--no-hardlinks", "--no-checkout", "--no-recurse-submodules", "--", src, dest); err != nil {
		return "", fmt.Errorf("check that git can clone %s: qory copies it for the run with git clone, which failed: %w", shown(src), err)
	}
	if _, err := git(ctx, dest, "cat-file", "-e", base+"^{commit}"); err != nil {
		// A HEAD on no branch: git clone copies the branches, so fetch the commit by it.
		if _, err := git(ctx, dest, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", src, "HEAD"); err != nil {
			return "", fmt.Errorf("check that git can fetch HEAD from %s: qory copies it for the run, and the fetch of HEAD failed: %w", shown(src), err)
		}
		if _, err := git(ctx, dest, "cat-file", "-e", base+"^{commit}"); err != nil {
			return "", fmt.Errorf("start the run again: HEAD of %s moved while qory copied it, and the copy has no commit %s", shown(src), base)
		}
	}
	if _, err := git(ctx, dest, "checkout", "--quiet", "--force", "--no-recurse-submodules", "-B", branch, base); err != nil {
		return "", fmt.Errorf("check that git can check out %s in a clone of %s: the run works on that commit, and the checkout failed: %w", base, shown(src), err)
	}
	return base, nil
}

// furtherRepositories is the path of each repository under .qory/repositories in the
// checkout, relative to the checkout with forward slashes, in order: each folder with a
// .git, below which no other is looked for. A link is not followed, and a .qory or a
// .qory/repositories that is no folder has none.
func furtherRepositories(checkout string) ([]string, error) {
	top := filepath.Join(checkout, filepath.FromSlash(repositories))
	for _, p := range []string{filepath.Dir(top), top} {
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) || err == nil && !info.IsDir() {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	var out []string
	err := filepath.WalkDir(top, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() || path == top {
			return nil
		}
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			rel, err := filepath.Rel(checkout, path)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
			return fs.SkipDir
		}
		return nil
	})
	return out, err
}

// CopyFolder copies the folder src, which is not a repository, into [Dir.Work]: its
// folders, its regular files with their permission bits, and its links as links, never
// followed. Any other kind of file, such as a pipe or a socket, stays out.
func (d Dir) CopyFolder(src string) error {
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("start the run in a folder: %s is no folder", shown(src))
	}
	dest := d.Work()
	return filepath.WalkDir(src, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dest, rel)
		info, err := e.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			// The owner keeps write access, so the run can write in every folder.
			return os.Mkdir(to, mode.Perm()|0o700)
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case mode.IsRegular():
			return copyFile(path, to, mode.Perm())
		}
		return nil
	})
}

// copyFile copies the regular file at src, opened without following a link, to dest, a
// path that does not exist, with permission bits perm.
func copyFile(src, dest string, perm fs.FileMode) error {
	in, err := openNoFollow(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if info, err := in.Stat(); err != nil {
		return err
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("%s changed while qory copied it: it is no regular file now", shown(src))
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
