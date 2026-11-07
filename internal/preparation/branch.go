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
	textOnBranch        = "qory run: on branch %s at %s"
	textBranchNoCommit  = "qory run: add a new version of the artifact branch in Qory Apiary with a commit of %[2]s, then take the work item in again: the version has %[1]s, which is not a commit on %[2]s on %[3]s. The fetch through the run's gateway failed: %[4]s; the code is used"
	textBranchNotCommit = "qory run: add a new version of the artifact branch in Qory Apiary with a commit of %[2]s, then take the work item in again: the version has %[1]s, which is not a commit; the code is used"
	textQoryInBranch    = "qory run: add a new version of the artifact branch in Qory Apiary whose commit has .qory as the target version %[3]s has it, then take the work item in again: commit %[2]s of %[1]s has another .qory, where qory keeps the files of the run; the code is used"
	textNoCheckout      = "qory run: add a new version of the artifact branch in Qory Apiary with a commit that git can check out, then take the work item in again. The checkout of %[1]s at %[2]s in %[3]s failed: %[4]s; the code is used"
)

// TargetRef is the ref that [Switch] leaves at the target version: [fromSeed] reads which
// files a branch removed from it.
const TargetRef = "refs/qory/target-version"

// WorkBranch is the branch of a run's work item. [RunAt] fetches it with [Target.Branch]
// set, and [Switch] checks it out. A repository with no entry in Version
// gets the branch at the commit the fetch took.
type WorkBranch struct {
	// Ref is the branch's name, the same in each repository: qory/<work item id>.
	Ref string
	// Version is the entries of the branch version the run read, nil with none. It may
	// have repositories that the run does not list, and those are not fetched.
	Version []branchformat.Repository
	// Repositories is every repository of the run, the target first, set by [RunAt] and
	// [Switch].
	Repositories []WorkRepository

	// system is the host of the run's repositories.
	system string
	// pushed is each repository [Push] pushed, as a line shows it.
	pushed []string
}

// VersionWays is the fix for a branch that [Push] pushed with no version stored: the next
// run starts at the last version, so its push fails as a branch that moved.
func (b *WorkBranch) VersionWays() string {
	return fmt.Sprintf(textVersionWays, b.Ref, b.PushedPaths(), shownPath(b.system))
}

// PushedPaths is each repository [Push] pushed, as one phrase of a line.
func (b *WorkBranch) PushedPaths() string { return strings.Join(b.pushed, " and ") }

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

// fetchBranchCommit fetches the commit of the repository path in b, if its version has
// one, into dest, by its id at depth one, and leaves dest where it is: the commit is at
// FETCH_HEAD alone until [Switch]. It runs under the hold of the fetch, because the check
// before a fetch stops on the remote's key that the fetch writes.
func fetchBranchCommit(ctx context.Context, git gitRunner, dest, system, path string, b *WorkBranch) error {
	commit := b.CommitOf(system, path)
	if commit == "" {
		return nil
	}
	if _, err := git(ctx, dest, "fetch", "--quiet", "--depth", "1", "origin", "--", commit); err != nil {
		var ge *gitError
		if errors.As(err, &ge) && notOurRef.MatchString(ge.line) {
			return branchNoCommit(path, system, commit, ge.line)
		}
		return notAtCommit(path, system, commit, err)
	}
	if _, err := git(ctx, dest, "rev-parse", "--verify", "--quiet", commit+"^{commit}"); err != nil {
		return fmt.Errorf(textBranchNotCommit, shortCommit(commit), integration.Printable(exports.Value(path, false)))
	}
	return nil
}

// branchNoCommit is [textBranchNoCommit] for commit of path on system, with git's line.
func branchNoCommit(path, system, commit, line string) error {
	shown := integration.Printable(exports.Value(path, false))
	host := integration.Printable(exports.Value(system, false))
	return fmt.Errorf(textBranchNoCommit, shortCommit(commit), shown, host, integration.Printable(exports.HideURLError(errors.New(line)).Error()))
}

// Switch checks out b in dir, the working folder, and in each further repository: at the
// commit of the version that [RunAt] fetched, or where the repository is when the version
// has no entry for it. A caller runs it after the compose, so no file of the branch
// reaches the harness. No git here reaches a remote. A commit with another .qory stops
// with [textQoryInBranch], and a commit that git does not check out with
// [textNoCheckout]. On success it prints the branch and the target's commit on report,
// and sets each [WorkRepository.Commit]. A work branch comes only from a run code, so a stop
// has the line of [Target.stop] for a run from a run code.
func Switch(ctx context.Context, dir string, base []string, b *WorkBranch, report io.Writer) error {
	return Target{FromCode: true}.stop(checkOutBranch(ctx, dir, base, b, report))
}

// checkOutBranch is [Switch] with the lines of a run from no run code.
func checkOutBranch(ctx context.Context, dir string, base []string, b *WorkBranch, report io.Writer) error {
	ctx = hostgit.WithBase(ctx, base)
	dir, err := filepath.Abs(dir)
	if err != nil {
		return local(err)
	}
	git := hostRunner(nil)
	if _, err := git(ctx, dir, "update-ref", TargetRef, "HEAD"); err != nil {
		return noCheckout(b, b.Repositories[0], err)
	}
	for i := range b.Repositories {
		r := &b.Repositories[i]
		at := filepath.Join(dir, filepath.FromSlash(r.Dir))
		commit := b.CommitOf(b.system, r.Path)
		args := []string{"checkout", "--quiet", "--no-overwrite-ignore", "-b", b.Ref}
		if commit != "" {
			args = append(args, commit)
		}
		if i == 0 && commit != "" {
			if err := sameQory(ctx, git, at, b.Ref, commit, r.Commit); err != nil {
				return err
			}
		}
		if _, err := git(ctx, at, args...); err != nil {
			return noCheckout(b, *r, err)
		}
		head, err := git(ctx, at, "rev-parse", "HEAD")
		if err != nil {
			return noCheckout(b, *r, err)
		}
		r.Commit = strings.TrimSpace(head)
	}
	fmt.Fprintf(report, textOnBranch+"\n", b.Ref, shortCommit(b.Repositories[0].Commit))
	return nil
}

// noCheckout is [textNoCheckout] for repository r, with git's line in err.
func noCheckout(b *WorkBranch, r WorkRepository, err error) error {
	shown := integration.Printable(exports.Value(r.Path, false))
	commit := b.CommitOf(b.system, r.Path)
	if commit == "" {
		commit = r.Commit
	}
	return fmt.Errorf(textNoCheckout, b.Ref, shortCommit(commit), shown, integration.Printable(exports.HideURLError(err).Error()))
}

// sameQory stops on a commit whose .qory entry is not the one of the target version.
func sameQory(ctx context.Context, git gitRunner, dir, ref, commit, version string) error {
	var entries [2]string
	for i, at := range []string{version, commit} {
		out, err := git(ctx, dir, "ls-tree", at, "--", checkout.Dir)
		if err != nil {
			return gitFailed(err)
		}
		entries[i] = out
	}
	if entries[0] != entries[1] {
		return fmt.Errorf(textQoryInBranch, ref, shortCommit(commit), shortCommit(version))
	}
	return nil
}

// removedByBranch is the set of paths, with forward slashes, that the commit at HEAD does
// not have and the target version has: nil outside a branch. The image's copy, made at
// or before the target version, may still have those files.
func removedByBranch(ctx context.Context, dir string) (map[string]bool, error) {
	if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", TargetRef); err != nil {
		var ge *gitError
		if errors.As(err, &ge) && ge.exit == 1 {
			return nil, nil
		}
		return nil, gitFailedIn(err)
	}
	out, err := git(ctx, dir, "diff", "--name-only", "-z", "--no-renames", "--diff-filter=D", TargetRef, "HEAD", "--")
	if err != nil {
		return nil, gitFailedIn(err)
	}
	removed := map[string]bool{}
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			removed[path] = true
		}
	}
	return removed, nil
}

// shortCommit is the first twelve characters of commit, as the fetch's line shows one.
func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
