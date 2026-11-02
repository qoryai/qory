package preparation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/qoryai/qory/internal/branchformat"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
)

// The words of the checkout of a work item's branch. Each is one format string.
const (
	textOnBranch       = "qory run: on branch %s at %s"
	textQoryInBranch   = "qory run: make .qory in commit %[2]s of the branch %[1]s the same as in the target version %[3]s, then run the round again: the branch has another .qory, and qory composes the harness there from the target version alone; the code is used"
	textBranchCollides = "qory run: remove the harness's links from commit %[2]s of the branch %[1]s, then run the round again. The checkout failed: %[3]s; the code is used"
)

// WorkBranch is the branch of a run's work item. [RunAt] fetches it with [Target.Branch]
// set, and [Switch] checks it out. A repository with no entry in Version gets the branch
// at the commit the fetch took.
type WorkBranch struct {
	// Ref is the branch's name, the same in each repository: qory/<work item id>.
	Ref string
	// Version is the entries of the branch version the run read, nil with none. It may
	// have repositories that the run does not list, and those are not fetched.
	Version []branchformat.Repository
	// Repositories is every repository of the run, the target first, set by [RunAt] and
	// [Switch].
	Repositories []WorkRepository

	// target is the commit of the version for the target, "" for none.
	target string
	// system is the host of the run's repositories.
	system string
}

// WorkRepository is one repository of a run on its [WorkBranch].
type WorkRepository struct {
	// Path is the repository's path on the run's system.
	Path string
	// Dir is the repository's folder relative to the working folder: "." for the target.
	Dir string
	// Commit is HEAD after the checkout: a run's new commits are the ones past it.
	Commit string
}

// CommitOf is the commit of the repository path on system in the branch's version, ""
// for none. The entry's system and path compare as [sameRepository] compares them.
func (b *WorkBranch) CommitOf(system, path string) string {
	for _, r := range b.Version {
		if r.System == system && sameRepository(system == ForgeGitHub, r.Path, path) {
			return r.Commit
		}
	}
	return ""
}

// fetchBranch fetches the target's commit of b into dir, under the hold of the fetch, and
// leaves the working folder at the target version head: the compose reads that version,
// and [Switch] moves to the branch after it. The commit is at FETCH_HEAD alone.
func fetchBranch(ctx context.Context, git gitRunner, dir, system, path, head string, b *WorkBranch) error {
	b.system, b.target = system, b.CommitOf(system, path)
	if b.target != "" {
		if _, err := git(ctx, dir, "fetch", "--quiet", "--depth", "1", "origin", "--", b.target); err != nil {
			return notAtCommit(path, system, b.target, err)
		}
	}
	b.Repositories = []WorkRepository{{Path: path, Dir: ".", Commit: head}}
	return nil
}

// branchFurther checks out b in dest, the clone of the further repository path: at the
// commit of its entry in the version, fetched by id at depth one, or at the clone's head.
func branchFurther(ctx context.Context, git gitRunner, dest, system, path string, b *WorkBranch) error {
	args := []string{"checkout", "--quiet", "-b", b.Ref}
	if commit := b.CommitOf(system, path); commit != "" {
		if _, err := git(ctx, dest, "fetch", "--quiet", "--depth", "1", "origin", "--", commit); err != nil {
			return notAtCommit(path, system, commit, err)
		}
		args = append(args, commit)
	}
	if _, err := git(ctx, dest, args...); err != nil {
		return notFetched(path, system, err)
	}
	head, err := git(ctx, dest, "rev-parse", "HEAD")
	if err != nil {
		return notFetched(path, system, err)
	}
	dir := filepath.ToSlash(filepath.Join(filepath.FromSlash(Repositories), filepath.FromSlash(path)))
	b.Repositories = append(b.Repositories, WorkRepository{Path: path, Dir: dir, Commit: strings.TrimSpace(head)})
	return nil
}

// Switch checks out b in dir, the working folder, once the compose has read the target
// version: at the commit [RunAt] fetched, or at the target version when the version has
// no entry for the target. No git here reaches a remote. A branch with another .qory
// stops with [textQoryInBranch], so a run cannot add to the composed harness, and a
// path that the compose linked stops with [textBranchCollides], since git leaves the
// link and every ignored file in place. On success it prints the branch and the commit
// on report, and sets the target's [WorkRepository.Commit].
func Switch(ctx context.Context, dir string, base []string, b *WorkBranch, report io.Writer) error {
	ctx = hostgit.WithBase(ctx, base)
	dir, err := filepath.Abs(dir)
	if err != nil {
		return local(err)
	}
	hold, err := hostgit.Hold(ctx, dir)
	if err != nil {
		if hostgit.Refused(err) {
			return fmt.Errorf(textPlaceRefused, integration.Printable(exports.HideError(err).Error()))
		}
		return gitFailed(err)
	}
	defer hold.Release()
	git := hostRunner(nil)
	args := []string{"checkout", "--quiet", "--no-overwrite-ignore", "-b", b.Ref}
	if b.target != "" {
		if err := sameQory(ctx, git, dir, b); err != nil {
			return err
		}
		args = append(args, b.target)
	}
	if _, err := git(ctx, dir, args...); err != nil {
		var ge *gitError
		if errors.As(err, &ge) && ge.exit == 1 {
			return fmt.Errorf(textBranchCollides, b.Ref, shortCommit(b.target), integration.Printable(exports.HideURLError(err).Error()))
		}
		return gitFailed(err)
	}
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return gitFailed(err)
	}
	b.Repositories[0].Commit = strings.TrimSpace(head)
	fmt.Fprintf(report, textOnBranch+"\n", b.Ref, shortCommit(b.Repositories[0].Commit))
	return nil
}

// sameQory stops on a commit of b whose .qory entry is not the one of HEAD, the target
// version.
func sameQory(ctx context.Context, git gitRunner, dir string, b *WorkBranch) error {
	var entries [2]string
	for i, commit := range []string{"HEAD", b.target} {
		out, err := git(ctx, dir, "ls-tree", commit, "--", checkout.Dir)
		if err != nil {
			return gitFailed(err)
		}
		entries[i] = out
	}
	if entries[0] != entries[1] {
		return fmt.Errorf(textQoryInBranch, b.Ref, shortCommit(b.target), shortCommit(b.Repositories[0].Commit))
	}
	return nil
}

// shortCommit is the first twelve characters of commit, as the fetch's line shows one.
func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
