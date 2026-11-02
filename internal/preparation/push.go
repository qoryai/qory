package preparation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/qoryai/qory/internal/branchformat"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/render"
)

// The words of the push step of a write run. Each is one format string.
const (
	textNoNewCommits     = "qory run: no new commits to push"
	textPushed           = "qory run: pushed %s to %s at %s"
	textNotSafe          = "qory run: take the work item in again in Qory Apiary: after the agent exited, the check of repository %s failed, so qory committed and pushed nothing: %s; the code is used"
	textNoCommitLeft     = "qory run: take the work item in again in Qory Apiary: the commit of what the agent left in %s failed: %s; the code is used"
	textNotFastForward   = "qory run: take the work item in again in Qory Apiary, and keep the run's commits on top of commit %[3]s: HEAD of %[1]s is at %[2]s, which does not descend from it, so a push would not be a fast-forward; the code is used"
	textBranchMoved      = "qory run: add a new version of the artifact branch in Qory Apiary with the head of %[1]s on %[2]s, or delete that branch on %[3]s, then take the work item in again: the branch moved since the run fetched it, and the push failed: %[4]s; the code is used"
	textPushRefused      = "qory run: change the rule on %[3]s that stops a push to %[1]s of %[2]s, then take the work item in again: the push failed: %[4]s; the code is used"
	textPushForbidden    = "qory run: add /%[1]s.git/git-receive-pack to the path rules of the run's policy for %[2]s, and give the run's credential for %[2]s write access to %[1]s, then take the work item in again: the push through the run's gateway returned 403: %[3]s; the code is used"
	textPushNoCredential = "qory run: give the run's credential for %[2]s write access to %[1]s, then take the work item in again: the push through the run's gateway failed: %[3]s; the code is used"
	textPushNotReached   = "qory run: check that the run's policy allows %[2]s and that %[2]s is reachable, then take the work item in again: the push of %[1]s through the run's gateway failed: %[3]s; the code is used"
)

// commitName and commitEmail are the author and the committer of a commit that [Push]
// makes, as qory's squash of a worktree has them: git reads no person's configuration
// here, and a repository's configuration may not set them.
const (
	commitName  = "qory"
	commitEmail = "qory@localhost"
)

// pushForbidden, pushMoved and pushRejected match git's lines for a push: a host or a
// gateway that returned 403, a branch on the remote that is not behind the commit, and a
// remote that stopped the update.
var (
	pushForbidden = regexp.MustCompile(`(?i)returned error: 403|HTTP 403`)
	pushMoved     = regexp.MustCompile(`\[rejected\]`)
	pushRejected  = regexp.MustCompile(`\[remote rejected\]`)
)

// Push commits what the agent left in each repository of b, in dir, the working folder,
// and pushes each repository whose HEAD is past its [WorkRepository.Commit] to the branch
// b.Ref as a fast-forward, by the URL qory builds, through the run's host proxy p. It
// returns the branch document of the new version, nil when no repository has new
// commits: [WorkBranch.Version] with the commit of each repository pushed replaced, or
// added for one with no entry. It prints a line on report for each repository pushed.
//
// Before any git writes, [safeAfterRun] checks the target and [safeFurther] each further
// repository, since the agent could write each .git, and the hold on the working folder
// lasts until Push returns. Every git runs with the environment [proxygit.Env] makes, so
// it reads no system or global configuration file and no credential helper of one runs,
// and with [proxygit.Hardened], so a hook, a filter or another program that .git names
// does not run. The two exceptions are the checks of a recorded place in
// [hostgit.Hold], git config --file and git rev-parse, which use the flags of hostgit
// and reach no remote. The commits of every repository come before the first push, so
// a failed commit pushes nothing.
func Push(ctx context.Context, dir string, base []string, b *WorkBranch, message string, p proxygit.Proxy, report io.Writer) (*branchformat.Branch, error) {
	ctx = hostgit.WithBase(ctx, proxygit.Env(base, nil))
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, local(err)
	}
	folders := make([]string, len(b.Repositories))
	for i, r := range b.Repositories {
		folders[i] = filepath.Join(dir, filepath.FromSlash(r.Dir))
		if i > 0 {
			if err := safeFurther(ctx, folders[i]); err != nil {
				return nil, notSafe(r, err)
			}
			continue
		}
		hold, err := safeAfterRun(ctx, folders[i])
		if err != nil {
			return nil, notSafe(r, err)
		}
		defer hold.Release()
	}
	heads := make([]string, len(b.Repositories))
	for i, r := range b.Repositories {
		if err := commitLeft(ctx, &p, folders[i], message); err != nil {
			return nil, fmt.Errorf(textNoCommitLeft, shownPath(r.Path), integration.Printable(exports.HideURLError(err).Error()))
		}
		if heads[i], err = headPast(ctx, hostRunner(&p), folders[i], r); err != nil {
			return nil, err
		}
	}
	version := slices.Clone(b.Version)
	pushed := false
	for i, r := range b.Repositories {
		if heads[i] == "" {
			continue
		}
		if err := pushTo(ctx, hostRunner(&p), folders[i], b, r, heads[i]); err != nil {
			return nil, err
		}
		fmt.Fprintf(report, textPushed+"\n", b.Ref, shownPath(r.Path), shortCommit(heads[i]))
		version = withCommit(version, b.system, r.Path, heads[i])
		pushed = true
	}
	if !pushed {
		fmt.Fprintln(report, textNoNewCommits)
		return nil, nil
	}
	return &branchformat.Branch{Ref: b.Ref, Repositories: version}, nil
}

// notSafe is [textNotSafe] for repository r and err, a stop of [safeAfterRun].
func notSafe(r WorkRepository, err error) error {
	cause := strings.TrimPrefix(err.Error(), "qory run: ")
	var unsafe *unsafeError
	if errors.As(err, &unsafe) {
		cause = unsafe.cause
	}
	return fmt.Errorf(textNotSafe, shownPath(r.Path), cause)
}

// shownPath is path as a line shows a repository.
func shownPath(path string) string { return integration.Printable(exports.Value(path, false)) }

// commitLeft commits what the agent left in the repository at dir, with message, as
// [commitName]. The commit has each path git status lists, by git's rules for untracked
// and ignored files, but [leftOut]'s: the index starts from HEAD, so what the agent
// staged counts only through the file it left. With no such path, or none that differs
// from HEAD once git added it, nothing is committed.
func commitLeft(ctx context.Context, p *proxygit.Proxy, dir, message string) error {
	git := hostRunner(p)
	status, err := git(ctx, dir, "status", "--porcelain", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return err
	}
	var paths strings.Builder
	for _, entry := range strings.Split(status, "\x00") {
		if len(entry) < 4 {
			continue
		}
		path := entry[3:]
		out, err := leftOut(dir, path)
		if err != nil {
			return err
		}
		if !out {
			paths.WriteString(":(top,literal)" + path + "\x00")
		}
	}
	if paths.Len() == 0 {
		return nil
	}
	if _, err := git(ctx, dir, "reset", "--quiet"); err != nil {
		return err
	}
	add := hostRunnerWith(p, paths.String())
	if _, err := add(ctx, dir, "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
		return err
	}
	if _, err := git(ctx, dir, "diff", "--cached", "--quiet", "--no-ext-diff"); err == nil {
		return nil
	} else if ge := (*gitError)(nil); !errors.As(err, &ge) || ge.exit != 1 {
		return err
	}
	commit := hostRunnerWith(p, "", "GIT_AUTHOR_NAME="+commitName, "GIT_AUTHOR_EMAIL="+commitEmail, "GIT_COMMITTER_NAME="+commitName, "GIT_COMMITTER_EMAIL="+commitEmail)
	_, err = commit(ctx, dir, "commit", "--quiet", "--no-verify", "--no-gpg-sign", "-m", message)
	return err
}

// leftOut reports whether path, relative to the repository at dir, stays out of a commit:
// it is [checkout.Dir] or under it, or a link that qory wrote. A run's branch stops every
// later run of its work item when its commit has another .qory than the target version,
// or a path where the compose writes a link.
func leftOut(dir, path string) (bool, error) {
	if path = strings.TrimSuffix(path, "/"); path == checkout.Dir || strings.HasPrefix(path, checkout.Dir+"/") {
		return true, nil
	}
	return render.OwnLink(dir, filepath.Join(dir, filepath.FromSlash(path)))
}

// headPast is HEAD of the repository r at dir when it is past [WorkRepository.Commit], ""
// when it is at that commit. A HEAD that does not descend from that commit stops with
// [textNotFastForward]: a branch that the remote does not have yet takes any push.
func headPast(ctx context.Context, git gitRunner, dir string, r WorkRepository) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf(textNoCommitLeft, shownPath(r.Path), integration.Printable(exports.HideURLError(err).Error()))
	}
	head := strings.TrimSpace(out)
	if head == r.Commit {
		return "", nil
	}
	if _, err := git(ctx, dir, "merge-base", "--is-ancestor", r.Commit, head); err != nil {
		return "", fmt.Errorf(textNotFastForward, shownPath(r.Path), shortCommit(head), shortCommit(r.Commit))
	}
	return head, nil
}

// pushTo pushes head of the repository r at dir to the branch of b, as a fast-forward.
func pushTo(ctx context.Context, git gitRunner, dir string, b *WorkBranch, r WorkRepository, head string) error {
	_, err := git(ctx, dir, "push", "--quiet", "--no-verify", "--no-recurse-submodules", "--", repoURL(b.system, r.Path), head+":refs/heads/"+b.Ref)
	if err != nil {
		return pushFailed(b, r.Path, err)
	}
	return nil
}

// pushFailed is the line of a push of the repository path that failed with err: by what
// git wrote, a branch that moved, a remote that stopped the update, a 403, a credential,
// and any other failure.
func pushFailed(b *WorkBranch, path string, err error) error {
	shown, host := shownPath(path), shownPath(b.system)
	var ge *gitError
	output := err.Error()
	if errors.As(err, &ge) && ge.output != "" {
		output = ge.output
	}
	cause := pushCause(output)
	switch {
	case pushMoved.MatchString(output):
		return fmt.Errorf(textBranchMoved, b.Ref, shown, host, cause)
	case pushRejected.MatchString(output):
		return fmt.Errorf(textPushRefused, b.Ref, shown, host, cause)
	case pushForbidden.MatchString(output):
		return fmt.Errorf(textPushForbidden, shown, host, cause)
	case answered.MatchString(output):
		return fmt.Errorf(textPushNoCredential, shown, host, cause)
	}
	return fmt.Errorf(textPushNotReached, shown, host, cause)
}

// pushCause is the line of git's output that says why a push failed, made printable: the
// line of the ref, else the first error, else the first line. The first line of a
// rejected push is the remote's address.
func pushCause(output string) string {
	lines := strings.Split(output, "\n")
	found := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "!") })
	if found < 0 {
		found = slices.IndexFunc(lines, func(line string) bool {
			return strings.HasPrefix(line, "error:") || strings.HasPrefix(line, "fatal:")
		})
	}
	line := firstLine(output)
	if found >= 0 {
		line = strings.TrimSpace(lines[found])
	}
	return integration.Printable(exports.HideURLError(errors.New(line)).Error())
}

// withCommit is version with the commit of the repository path on system set to commit,
// or with a new entry when it has none. The entries compare as [WorkBranch.CommitOf]
// compares them.
func withCommit(version []branchformat.Repository, system, path, commit string) []branchformat.Repository {
	for i := range version {
		if version[i].System == system && sameRepository(system == ForgeGitHub, version[i].Path, path) {
			version[i].Commit = commit
			return version
		}
	}
	return append(version, branchformat.Repository{System: system, Path: path, Commit: commit})
}
