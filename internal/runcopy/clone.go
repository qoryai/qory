package runcopy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/checkout"
)

// repositories is the folder, relative to the checkout, that the further repositories
// of a run are cloned under, as preparation.Repositories sets it.
const repositories = checkout.Dir + "/repositories"

// Repository is one repository of the copy and the commit it starts at.
type Repository struct {
	// Path is the repository's folder relative to the copy, with forward slashes: "" for
	// the person's repository, and .qory/repositories/<path> for a further one.
	Path string
	// Base is the commit the copy checks out: the one the person's checkout has at HEAD.
	Base string
	// index is the repository's number: 0 for the person's, and from 1 for the further
	// ones, which is in the name of its bare source and of its quarantine.
	index int
	// filtered are the files a filter changed at the checkout, such as a Git LFS file,
	// with what the checkout wrote: the git session runs no filter, so it leaves each
	// out while the copy still has what the checkout wrote.
	filtered []SetupPath
}

// Clone copies the person's repository at checkout, the top of its working tree, and
// each repository under .qory/repositories in it, for a run that works on branch. It
// returns the repositories, the person's first.
//
// For each, git clone --no-local first clones the repository into a bare repository
// under the run's folder that no run binds, and fetches HEAD into it as branch. git
// clone --no-local then clones that bare repository into the copy, [Dir.Work] or the
// same path under it, and checks out branch with the person's git environment, so a
// filter such as Git LFS applies; no run has written the copy yet. --no-local copies
// the objects and links none of them, and no clone uses --shared or --reference. Each
// submodule of the person's repository whose repository is under its .git/modules is
// cloned the same way at the commit its gitlink records. The copy has commits alone:
// no uncommitted, untracked or ignored file of the checkout.
func (d Dir) Clone(ctx context.Context, checkout, branch string) ([]Repository, error) {
	if err := checkBranch(ctx, branch); err != nil {
		return nil, err
	}
	checkout, err := filepath.Abs(checkout)
	if err != nil {
		return nil, fileErr(checkout, err)
	}
	for _, p := range []string{d.Work(), filepath.Join(d.root, sourcesName)} {
		if _, err := os.Lstat(p); err == nil {
			return nil, fmt.Errorf(textExists, shown(p))
		}
	}
	if err := os.Mkdir(filepath.Join(d.root, sourcesName), 0o700); err != nil {
		return nil, fileErr(d.root, err)
	}
	first, err := d.cloneOne(ctx, 0, checkout, d.Work(), branch)
	if err != nil {
		return nil, err
	}
	if err := d.cloneSubmodules(ctx, checkout, d.Work(), first.Base); err != nil {
		return nil, err
	}
	repos := []Repository{first}
	further, err := furtherRepositories(checkout)
	if err != nil {
		return nil, err
	}
	for i, rel := range further {
		src := filepath.Join(checkout, filepath.FromSlash(rel))
		dest := filepath.Join(d.Work(), filepath.FromSlash(rel))
		// The commit can have a link at .qory or below it, which a clone would write through.
		if through, err := linkOnPath(d.Work(), rel); err != nil {
			return nil, fileErr(dest, err)
		} else if through {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, fileErr(filepath.Dir(dest), err)
		}
		r, err := d.cloneOne(ctx, i+1, src, dest, branch)
		if err != nil {
			return nil, err
		}
		r.Path = rel
		repos = append(repos, r)
	}
	return repos, nil
}

// cloneOne copies the repository at src, as [Dir.Clone] describes, through the bare
// repository number i, into dest, a path that does not exist, and returns it with the
// commit it checks out, the one src has at HEAD, and its filtered files.
func (d Dir) cloneOne(ctx context.Context, i int, src, dest, branch string) (Repository, error) {
	base, err := d.cloneAt(ctx, i, src, dest, branch)
	if err != nil {
		return Repository{}, err
	}
	filtered, err := d.filteredFiles(ctx, i, dest)
	if err != nil {
		return Repository{}, fmt.Errorf(textNoCheckout, base, shown(src), err)
	}
	return Repository{Base: base, index: i, filtered: filtered}, nil
}

// cloneAt is [Dir.cloneOne] without the filtered files.
func (d Dir) cloneAt(ctx context.Context, i int, src, dest, branch string) (string, error) {
	bare := d.source(i)
	if _, err := git(ctx, d.root, "clone", "--quiet", "--bare", "--no-local", "--no-hardlinks", "--", src, bare); err != nil {
		return "", fmt.Errorf(textNoClone, shown(src), err)
	}
	// HEAD by its name, as the branch of the run: the clone copies the branches alone,
	// and a HEAD on no branch would not be among them.
	ref := "refs/heads/" + branch
	if _, err := git(ctx, bare, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", src, "+HEAD:"+ref); err != nil {
		return "", fmt.Errorf(textNoHead, shown(src), err)
	}
	base, err := commitOf(ctx, bare, ref)
	if err != nil {
		return "", fmt.Errorf(textNoHead, shown(src), err)
	}
	if _, err := git(ctx, d.root, "clone", "--quiet", "--no-local", "--no-hardlinks", "--no-checkout", "--no-recurse-submodules", "--branch", branch, "--", bare, dest); err != nil {
		return "", fmt.Errorf(textNoClone, shown(src), err)
	}
	if _, err := personGit(ctx, dest, "checkout", "--quiet", "--force", "--no-recurse-submodules", "-B", branch, base); err != nil {
		return "", fmt.Errorf(textNoCheckout, base, shown(src), err)
	}
	return base, nil
}

// filteredFiles is each file of the copy at dest, the repository number i just checked
// out, that .gitattributes sets a filter for, with what the checkout wrote there. git
// ls-files and git check-attr read the copy before any run writes it.
func (d Dir) filteredFiles(ctx context.Context, i int, dest string) ([]SetupPath, error) {
	files, err := git(ctx, dest, "ls-files", "-z")
	if err != nil || files == "" {
		return nil, err
	}
	out, err := gitWith(ctx, dest, nil, strings.NewReader(files), "check-attr", "-z", "--stdin", "filter")
	if err != nil {
		return nil, err
	}
	var paths []string
	fields := strings.Split(out, "\x00")
	for j := 0; j+2 < len(fields); j += 3 {
		if v := fields[j+2]; v != "unspecified" && v != "unset" && cleanRel(fields[j]) {
			paths = append(paths, fields[j])
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	return d.setupStatesAt(ctx, dest, i, paths)
}

// cloneSubmodules clones, into the copy at dest, each submodule that .gitmodules at
// commit base lists, whose repository the person's checkout at src has under its git
// folder's modules, at the commit its gitlink in base records. A submodule with no
// such repository stays an empty folder, as git leaves one that is not set up.
func (d Dir) cloneSubmodules(ctx context.Context, src, dest, base string) error {
	listed, err := git(ctx, dest, "ls-tree", "-z", "--full-tree", base, "--", ".gitmodules")
	if err != nil {
		return fmt.Errorf(textSubmodules, base, shown(src), err)
	}
	if listed == "" {
		return nil
	}
	out, err := git(ctx, dest, "config", "--blob", base+":.gitmodules", "-z", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		if exitOf(err) == 1 {
			return nil
		}
		return fmt.Errorf(textSubmodules, base, shown(src), err)
	}
	gitDir, err := git(ctx, src, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return fmt.Errorf(textGitInRepo, shown(src), err)
	}
	modules := filepath.Join(strings.TrimSpace(gitDir), "modules")
	for entry := range strings.SplitSeq(out, "\x00") {
		key, sub, ok := strings.Cut(entry, "\n")
		if !ok {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "submodule."), ".path")
		if !cleanRel(name) || !cleanRel(sub) {
			continue
		}
		commit, ok, err := gitlinkAt(ctx, dest, base, sub)
		if err != nil {
			return fmt.Errorf(textSubmodules, base, shown(src), err)
		}
		from := filepath.Join(modules, filepath.FromSlash(name))
		if info, err := os.Lstat(from); !ok || err != nil || !info.IsDir() {
			continue
		}
		// The commit can have a link on the way, which a clone would write through.
		if through, err := linkOnPath(dest, sub); err != nil {
			return fmt.Errorf(textNoSubmodule, shown(sub), shown(src), shown(from), err)
		} else if through {
			continue
		}
		to := filepath.Join(dest, filepath.FromSlash(sub))
		if _, err := git(ctx, d.root, "clone", "--quiet", "--no-local", "--no-hardlinks", "--no-checkout", "--no-recurse-submodules", "--", from, to); err != nil {
			return fmt.Errorf(textNoSubmodule, shown(sub), shown(src), shown(from), err)
		}
		if _, err := git(ctx, to, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--", from, commit); err != nil {
			return fmt.Errorf(textNoSubmodule, shown(sub), shown(src), shown(from), err)
		}
		if _, err := personGit(ctx, to, "checkout", "--quiet", "--force", "--detach", commit); err != nil {
			return fmt.Errorf(textNoSubmodule, shown(sub), shown(src), shown(from), err)
		}
	}
	return nil
}

// linkOnPath reports whether a part of rel, with forward slashes, below the folder top is a
// link, part by part with lstat. It stops at the first part that does not exist.
func linkOnPath(top, rel string) (bool, error) {
	at := top
	for part := range strings.SplitSeq(rel, "/") {
		at = filepath.Join(at, part)
		info, err := os.Lstat(at)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return false, nil
		case err != nil:
			return false, err
		case info.Mode()&fs.ModeSymlink != 0:
			return true, nil
		}
	}
	return false, nil
}

// gitlinkAt is the commit the gitlink at rel in commit records, and whether there is
// one.
func gitlinkAt(ctx context.Context, dir, commit, rel string) (string, bool, error) {
	out, err := gitWith(ctx, dir, []string{"GIT_LITERAL_PATHSPECS=1"}, nil, "ls-tree", "-z", "--full-tree", commit, "--", rel)
	if err != nil {
		return "", false, err
	}
	about, name, _ := strings.Cut(strings.TrimSuffix(out, "\x00"), "\t")
	fields := strings.Fields(about)
	if len(fields) != 3 || fields[0] != "160000" || name != rel || !objectID.MatchString(fields[2]) {
		return "", false, nil
	}
	return fields[2], true, nil
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
			return nil, fileErr(p, err)
		}
	}
	var out []string
	err := filepath.WalkDir(top, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() || p == top {
			return nil
		}
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			rel, err := filepath.Rel(checkout, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fileErr(top, err)
	}
	return out, nil
}

// cleanRel reports whether p is a clean relative path with forward slashes that stays
// in the folder it is relative to, with no . or .. part.
func cleanRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || strings.ContainsRune(p, 0) {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
