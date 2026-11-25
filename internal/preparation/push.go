package preparation

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/qoryai/integrations/contracts"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/render"
)

// The words of the push step of a write run. Each is one format string.
const (
	textNoNewCommits = "qory run: no new commits to push"
	textPushed       = "qory run: pushed %s to %s at %s"
	// textReadAhead is the cause alone: the push goes on without the read target's commits.
	textReadAhead        = "qory run: The commits in a read-only repository were not kept: a run changes only its target.\n  The repository is %s."
	textForgeLine        = "qory run: remote: %s"
	textNestedRepository = "qory run: have the agent leave plain files in %s of %s to push them: that folder is a repository, so qory left it out of the commit, and what it has stays in the working folder"
	textLockRemoved      = "qory run: removed .git/index.lock of %s, which the agent left when it stopped"
	textLockUnproven     = "qory run: take the work item in again in Qory Apiary: repository %s has .git/index.lock and the stop of the agent is not proven, so qory committed and pushed nothing; the code is used"
	textLockNotFile      = "qory run: take the work item in again in Qory Apiary: .git/index.lock of repository %s is not a regular file, so qory committed and pushed nothing; the code is used"
	textLockNotRemoved   = "qory run: take the work item in again in Qory Apiary: the removal of .git/index.lock of repository %s failed, so qory committed and pushed nothing: %s; the code is used"
	textNotPushed        = "qory run: not pushed from %s: %s %s"
	textFurtherLines     = "qory run: %d further lines left out"
	textSetupNotSafe     = "qory run: take the work item in again in Qory Apiary: after the setup, the check of repository %s failed, so the agent did not start: %s; the code is used"
	textNoSetupRecord    = "qory run: take the work item in again in Qory Apiary: the record of what the setup changed in %s failed: %s; the code is used"
	textNotSafe          = "qory run: take the work item in again in Qory Apiary: after the agent exited, the check of repository %s failed, so qory committed and pushed nothing: %s; the code is used"
	textNoCommitLeft     = "qory run: take the work item in again in Qory Apiary: the commit of what the agent left in %s failed: %s; the code is used"
	textNoHistory        = "qory run: take the work item in again in Qory Apiary: the check that HEAD of %s descends from commit %s failed: %s; the code is used"
	textNotFastForward   = "qory run: take the work item in again in Qory Apiary, and keep the run's commits on top of commit %[3]s: HEAD of %[1]s is at %[2]s, which does not descend from it, so a push would not be a fast-forward; the code is used"
	textDeleteBranch     = "delete branch %[1]s of %[2]s on %[3]s"
	textListFailed       = "qory run: Start the run again: the list of the run's commits failed.\n  Listing the commits of %s from commit %s to %s failed: %s. qory pushed nothing."
	textPushRefused      = "qory run: change the rule on %[3]s that stops a push to %[1]s of %[2]s, then take the work item in again: the push failed: %[4]s; the code is used"
	textPushForbidden    = "qory run: The push was refused (403).\n  One of these most likely stopped it:\n  1. The run's push proxy refuses a push that is not a plain push to the push_ref of the push credential (for example a signed push or a push with push options), and qory pushes to refs/heads/%[3]s (wall:push-ref).\n  2. The run's push credential has no item for /%[1]s.git/git-receive-pack on %[2]s, and the push proxy refuses every other host and path.\n  3. The forge stopped the push: its permission for the push credential, or a branch rule.\n  The push of %[1]s to %[2]s returned 403: %[4]s; the code is used"
	textPushNoCredential = "qory run: give the run's credential for %[2]s write access to %[1]s, then take the work item in again: the push through the run's gateway failed: %[3]s; the code is used"
	textPushNotReached   = "qory run: Let the run's gateway reach %[2]s, then take the work item in again.\n  The push of %[1]s through the run's push proxy failed: %[3]s; the code is used"
)

// textListFailedFromCode is [textListFailed] for a run from a run code.
const textListFailedFromCode = textListFailed + " The run code is used up."

// maxFailedPushLines is the most lines of the forge, and the most commits, that a failed
// push prints for one repository.
const maxFailedPushLines = 50

// commitName and commitEmail are the author and the committer of a commit that [Push]
// makes, as qory's squash of a worktree has them: git reads no person's configuration
// here, and a repository's configuration may not set them.
const (
	gitlinkMode = "160000"
	commitName  = "qory"
	commitEmail = "qory@localhost"
)

// urlLogin matches the user part of a URL. pushForbidden, pushMoved and pushRejected
// match git's lines for a push: a host or a gateway that returned 403, a branch on the
// remote that is not behind the commit, and a remote that stopped the update.
var (
	urlLogin      = regexp.MustCompile(`://[^/\s@]+@`)
	pushForbidden = regexp.MustCompile(`(?i)returned error: 403|HTTP 403`)
	pushMoved     = regexp.MustCompile(`\[rejected\]`)
	pushRejected  = regexp.MustCompile(`\[remote rejected\]`)
)

// noRemote is the proxy of a git of the push step that reaches no remote: it has the
// environment of a git with a proxy, with no password in it.
var noRemote = &proxygit.Proxy{}

// noMaintenance are the options of a git of the push step: git commit would otherwise
// start maintenance in the background on this machine, which outlives the step.
var noMaintenance = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}

// stepGit runs a git of the push step, as [hostRunnerWith] does, with [noMaintenance] and
// with GIT_CEILING_DIRECTORIES at the parent of the repository's folder, so git takes no
// folder above it for the repository.
func stepGit(p *proxygit.Proxy, input string, env ...string) gitRunner {
	return func(ctx context.Context, dir string, args ...string) (string, error) {
		return hostRunnerOf(p, hasRemote(p), noMaintenance, input, append(slices.Clone(env), ceiling(dir)))(ctx, dir, args...)
	}
}

// ceiling is the variable that keeps git from a folder above dir.
func ceiling(dir string) string { return "GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir) }

// Push commits what the agent left in the run's target, the first repository of b, in dir,
// the working folder, and pushes it, when its HEAD is past its [WorkRepository.Commit], to
// the branch b.Ref as a fast-forward, by the URL qory builds, through the run's push proxy
// p. Before the push, it reads the head of the branch at the forge through the read proxy
// and compares it with [WorkRepository.Commit], see [checkHead]: a head that is not the
// expected one stops the push with a [*HeadChanged], and a head that cancelled runs left
// is replaced with a lease on it. It returns the repositories of the new version, nil when
// the target has no new commits: [WorkBranch.Version] with the target's commit replaced,
// or added when it has no entry, and its base and commits from [WorkRepository.Start]. It
// prints a line on report when it pushed.
// Each push that lands sets the repository's [WorkRepository.Commit] and its entry of
// [WorkBranch.Version] to the commit pushed, so a later Push of the same run, after a
// later session, pushes only the commits past it, and pushes nothing when there are none.
// [WorkBranch.PushedPaths] lists the repositories of the last Push alone.
//
// A further repository, a read target, [WorkRepository.ReadOnly], is skipped in each step:
// it is not checked, committed or pushed, and the version has no entry of it. A target that
// is a read target, in a run with no target, is skipped too. [readsAhead] prints a line for
// each read target with commits past its [WorkRepository.Start]. Before any git writes,
// [safeAfterRun] checks the target, since the agent could write its .git. Every git runs with the environment
// [proxygit.Env] makes, so it reads no system or global configuration file, and with
// [proxygit.Hardened], which turns off the hooks in .git, fsmonitor and credential
// helpers. No option turns a filter off: the check stops on each key that git init and
// the fetch do not write, filter keys among them, so a filter stays out only while no
// process writes .git/config after the check. A path that [RecordSetup]
// recorded and that is as the setup left it stays out of the commit.
//
// An agent that was killed can leave .git/index.lock, which would stop the commit. After
// the checks, [clearIndexLocks] removes that one file of the target when
// agentStopped is true, and stops the push when a lock is there and it is false.
//
// After the commit and before the head check, check, when not nil, gets the version that
// the push leaves when the target lands, [WorkBranch.Version] when it has no new commits.
// An error of check stops the push and is Push's error: the commits stay local.
func Push(ctx context.Context, dir string, base []string, b *WorkBranch, message string, read, p proxygit.Proxy, agentStopped bool, check func(version []Repository) error, report io.Writer) ([]Repository, error) {
	// A Push that stops before its first push leaves no repository of an earlier Push listed.
	b.landed, b.pushed = nil, nil
	ctx = hostgit.WithBase(ctx, proxygit.Env(base, nil))
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, local(err)
	}
	// work is the index in b.Repositories of the repository the push may move: the target,
	// the first, unless it is a read target. A run changes only its target, so a further
	// repository is checked, committed and pushed in no step here.
	var work []int
	if len(b.Repositories) > 0 && !b.Repositories[0].ReadOnly {
		work = []int{0}
	}
	readsAhead(ctx, dir, b, report)
	folders := make([]string, len(b.Repositories))
	for _, i := range work {
		r := b.Repositories[i]
		folders[i] = filepath.Join(dir, filepath.FromSlash(r.Dir))
		if err := safeAfterRun(ctx, folders[i]); err != nil {
			return nil, notSafe(r, err)
		}
	}
	repositories, workFolders := make([]WorkRepository, len(work)), make([]string, len(work))
	for n, i := range work {
		repositories[n], workFolders[n] = b.Repositories[i], folders[i]
	}
	if err := clearIndexLocks(repositories, workFolders, agentStopped, report); err != nil {
		return nil, err
	}
	heads := make([]string, len(b.Repositories))
	for _, i := range work {
		r := b.Repositories[i]
		if err := commitLeft(ctx, folders[i], r, b.setupChanges[r.Dir], message, report); err != nil {
			return nil, fmt.Errorf(textNoCommitLeft, shownPath(r.Path), integration.Printable(exports.HideURLError(err).Error()))
		}
		if heads[i], err = headPast(ctx, stepGit(noRemote, ""), folders[i], r); err != nil {
			return nil, err
		}
	}
	// No repository is pushed when a commit of one has a path of the outbox.
	for i, r := range b.Repositories {
		if heads[i] == "" {
			continue
		}
		if err := outboxCommitted(ctx, stepGit(noRemote, ""), folders[i], b.system, r, r.Start, heads[i], true); err != nil {
			return nil, err
		}
	}
	// The target's commits are listed before the push, so a list that fails pushes nothing.
	commits := make([][]Commit, len(b.Repositories))
	version := b.withoutReads(b.Version)
	for i, r := range b.Repositories {
		if heads[i] == "" {
			continue
		}
		if commits[i], err = runCommits(ctx, stepGit(noRemote, ""), folders[i], r.Start, heads[i]); err != nil {
			return nil, listFailed(true, r, r.Start, heads[i], err)
		}
		version = withCommit(version, b.system, r.Path, heads[i], r.Start, baseBranch(r.DefaultBranch), commits[i])
	}
	if check != nil {
		if err := check(slices.Clone(version)); err != nil {
			return nil, err
		}
	}
	leases := make([]string, len(b.Repositories))
	readGit := stepGit(&read, "")
	for i, r := range b.Repositories {
		if heads[i] == "" {
			continue
		}
		check, err := checkHead(ctx, readGit, folders[i], b, r)
		if err != nil {
			return nil, err
		}
		leases[i] = check.lease
	}
	for i, r := range b.Repositories {
		if heads[i] == "" {
			continue
		}
		if err := pushTo(ctx, stepGit(&p, ""), readGit, folders[i], b, r, heads[i], leases[i], report); err != nil {
			return nil, err
		}
		b.pushed = append(b.pushed, shownPath(r.Path))
		b.landed = append(b.landed, Landed{System: b.system, Path: r.Path, Commit: heads[i]})
		fmt.Fprintf(report, textPushed+"\n", b.Ref, shownPath(r.Path), shortCommit(heads[i]))
		// The branch is at heads[i] now, so a later Push of the run starts there.
		b.Repositories[i].Commit = heads[i]
		b.Version = withCommit(slices.Clone(b.Version), b.system, r.Path, heads[i], r.Start, baseBranch(r.DefaultBranch), commits[i])
	}
	if len(b.pushed) == 0 {
		fmt.Fprintln(report, textNoNewCommits)
		return nil, nil
	}
	return b.withoutReads(b.Version), nil
}

// withoutReads is a copy of version with no entry of a repository of b that is a read
// target, [WorkRepository.ReadOnly]: the version that [Push] leaves covers the
// repositories it may push alone.
func (b *WorkBranch) withoutReads(version []Repository) []Repository {
	return slices.DeleteFunc(slices.Clone(version), func(v Repository) bool {
		return v.System == b.system && slices.ContainsFunc(b.Repositories, func(r WorkRepository) bool {
			return r.ReadOnly && sameRepository(b.system == ForgeGitHub, r.Path, v.Path)
		})
	})
}

// listFailed is the error of a list of the commits of the repository r from start to head
// that failed with err, before any push: [textListFailedFromCode] for a run from a run code,
// fromCode, and [textListFailed] for a local run.
func listFailed(fromCode bool, r WorkRepository, start, head string, err error) error {
	text := textListFailed
	if fromCode {
		text = textListFailedFromCode
	}
	return fmt.Errorf(text, shownPath(r.Path), shortCommit(start), shortCommit(head), integration.Printable(exports.HideURLError(err).Error()))
}

// runCommits lists the commits from start, not included, to head, included, of the
// repository at dir, oldest first: the commits that the run pushed.
func runCommits(ctx context.Context, git gitRunner, dir, start, head string) ([]Commit, error) {
	out, err := git(ctx, dir, "rev-list", "--reverse", start+".."+head)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, c := range strings.Fields(out) {
		commits = append(commits, Commit{Commit: c})
	}
	return commits, nil
}

// clearIndexLocks removes .git/index.lock of each repository, in folders by the order of
// repositories, and prints a line on report for each. It removes nothing until every
// repository passed: with agentStopped false a lock stops the push, as does one that is
// not a regular file. A .git that is not a directory has no such file, and the commit
// reports what git finds. No other lock is touched.
func clearIndexLocks(repositories []WorkRepository, folders []string, agentStopped bool, report io.Writer) error {
	var locks []int
	for i, r := range repositories {
		info, err := os.Lstat(filepath.Join(folders[i], ".git"))
		if err != nil || !info.IsDir() {
			continue
		}
		lock, err := os.Lstat(indexLock(folders[i]))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return fmt.Errorf(textLockNotRemoved, shownPath(r.Path), integration.Printable(err.Error()))
		case !lock.Mode().IsRegular():
			return fmt.Errorf(textLockNotFile, shownPath(r.Path))
		case !agentStopped:
			return fmt.Errorf(textLockUnproven, shownPath(r.Path))
		}
		locks = append(locks, i)
	}
	for _, i := range locks {
		path := shownPath(repositories[i].Path)
		if err := os.Remove(indexLock(folders[i])); err != nil {
			return fmt.Errorf(textLockNotRemoved, path, integration.Printable(err.Error()))
		}
		fmt.Fprintf(report, textLockRemoved+"\n", path)
	}
	return nil
}

// indexLock is the lock file of the index of the repository at folder.
func indexLock(folder string) string { return filepath.Join(folder, ".git", "index.lock") }

// notSafe is [textNotSafe] for repository r and err, a stop of [safeAfterRun].
func notSafe(r WorkRepository, err error) error { return checkFailed(textNotSafe, r, err) }

// checkFailed is the line text for the failed check of r, with what the folder has.
func checkFailed(text string, r WorkRepository, err error) error {
	cause := strings.TrimPrefix(err.Error(), "qory run: ")
	var unsafe *unsafeError
	if errors.As(err, &unsafe) {
		cause = unsafe.cause
	}
	return fmt.Errorf(text, shownPath(r.Path), cause)
}

// shownPath is path as a line shows a repository.
func shownPath(path string) string { return integration.Printable(exports.Value(path, false)) }

// ShownPath is [shownPath] for a caller outside the preparation.
func ShownPath(path string) string { return shownPath(path) }

// commitLeft commits what the agent left in r, the repository at dir, with message, as
// [commitName]. The index starts from HEAD, so what the agent staged counts only through
// the file it left. The commit has each path git status lists, by git's rules for
// untracked and ignored files, but [leftOut]'s and each path in a repository in the
// repository, see [repositoryOf], for which a line goes to report, once; such a path keeps
// what HEAD has there. A path of [isReplaced] goes out of the index by its name, since git
// add fails on a path behind a link. A path of [leftOut] that a commit of the agent changed
// goes back to what r.Commit has, because a branch with another .qory stops the later runs
// of its work item. With no change from HEAD, nothing is committed.
func commitLeft(ctx context.Context, dir string, r WorkRepository, setup map[string]string, message string, report io.Writer) error {
	git := stepGit(noRemote, "")
	if _, err := git(ctx, dir, "reset", "--quiet"); err != nil {
		return err
	}
	changed, err := changedPaths(ctx, git, dir)
	if err != nil {
		return err
	}
	printed := map[string]bool{}
	line := func(path string) {
		if !printed[path] {
			fmt.Fprintf(report, textNestedRepository+"\n", shownText(path), shownPath(r.Path))
		}
		printed[path] = true
	}
	var paths, gone []string
	for _, path := range changed {
		out, err := leftOut(dir, path, setup)
		var repository string
		if err == nil && !out {
			repository, err = repositoryOf(dir, path)
		}
		switch {
		case err != nil:
			return err
		case out:
		case repository != "":
			line(repository)
		default:
			replaced, stays, err := isReplaced(dir, path, setup)
			switch {
			case err != nil:
				return err
			case !replaced:
				paths = append(paths, path)
			case !stays:
				gone = append(gone, path)
			}
		}
	}
	if err := reportGitlinks(ctx, git, dir, line); err != nil {
		return err
	}
	if len(gone) > 0 {
		if _, err := stepGit(noRemote, strings.Join(gone, "\x00")+"\x00")(ctx, dir, "update-index", "--force-remove", "-z", "--stdin"); err != nil {
			return err
		}
	}
	if len(paths) > 0 {
		added, err := addLeft(ctx, dir, paths)
		if err != nil {
			return err
		}
		for _, path := range added {
			line(path)
		}
		if len(added) > 0 {
			if err := resetPaths(ctx, dir, "HEAD", added); err != nil {
				return err
			}
		}
	}
	if err := restoreLeftOut(ctx, git, dir, r.Commit); err != nil {
		return err
	}
	// hostgit sets diff.ignoreSubmodules=all, which would hide a repository in the repository.
	if _, err := git(ctx, dir, "diff", "--cached", "--quiet", "--no-ext-diff", "--ignore-submodules=none"); err == nil {
		return nil
	} else if ge := (*gitError)(nil); !errors.As(err, &ge) || ge.exit != 1 {
		return err
	}
	commit := stepGit(noRemote, "", "GIT_AUTHOR_NAME="+commitName, "GIT_AUTHOR_EMAIL="+commitEmail, "GIT_COMMITTER_NAME="+commitName, "GIT_COMMITTER_EMAIL="+commitEmail)
	// The diff found a change, and git commit counts none in a gitlink alone.
	_, err = commit(ctx, dir, "commit", "--quiet", "--allow-empty", "--no-verify", "--no-gpg-sign", "-m", message)
	return err
}

// resetPaths sets paths in the index of the repository at dir to what commit has.
func resetPaths(ctx context.Context, dir, commit string, paths []string) error {
	_, err := stepGit(noRemote, pathspecs(paths))(ctx, dir, "reset", "--quiet", "--pathspec-from-file=-", "--pathspec-file-nul", commit, "--")
	return err
}

// addLeft adds paths to the index of the repository at dir as [hostgit.AddAll] does, with the
// git of [stepGit] and within the limit of one git call, and returns the gitlinks it made.
func addLeft(ctx context.Context, dir string, paths []string) ([]string, error) {
	limit := limitOf("add")
	call, cancel := context.WithTimeoutCause(ctx, limit, errLimit)
	defer cancel()
	command := func(ctx context.Context, dir string, env []string, args ...string) (*exec.Cmd, error) {
		return hostCommand(ctx, noRemote, false, dir, noMaintenance, append(slices.Clone(env), ceiling(dir)), args)
	}
	added, err := hostgit.AddAll(call, command, dir, nil, paths)
	if err == nil {
		return added, nil
	}
	var ae *hostgit.AddError
	switch {
	case errors.Is(context.Cause(call), errLimit):
		err = &gitError{line: fmt.Sprintf(textStopped, "add", limit), exit: -1}
	case ctx.Err() != nil:
		err = &gitError{line: fmt.Sprintf(textCut, "add"), exit: -1}
	case errors.As(err, &ae) && firstLine(ae.Stderr) != "":
		err = &gitError{line: firstLine(ae.Stderr), exit: -1, output: ae.Stderr}
	}
	return nil, err
}

// pathspecs is paths as git reads them with --pathspec-file-nul: each from the top of the
// repository, with no pattern in it.
func pathspecs(paths []string) string {
	var list strings.Builder
	for _, path := range paths {
		list.WriteString(":(top,literal)" + path + "\x00")
	}
	return list.String()
}

// repositoryOf is the topmost of path and the folders above it, relative to the repository at
// dir, that has an entry named .git of any kind, a link too, and "" for none. The walk goes from
// the top and stops at the first folder above path that is missing or is not a folder, such as a
// link, since git has no entry below it; at path itself, a link is followed. git status lists the
// files below a folder whose .git git does not read as a repository, such as a .git file with no
// gitdir line or an empty .git folder.
func repositoryOf(dir, path string) (string, error) {
	path = strings.TrimSuffix(path, "/")
	hasGit := func(folder string) bool {
		_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(folder), ".git"))
		return err == nil
	}
	for i := range len(path) {
		if path[i] != '/' {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path[:i])))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", nil
		case err != nil:
			return "", err
		case !info.IsDir():
			return "", nil
		case hasGit(path[:i]):
			return path[:i], nil
		}
	}
	if hasGit(path) {
		return path, nil
	}
	return "", nil
}

// notFolder is the first path above path, relative to the repository at dir, whose entry is
// not a folder, such as a file or a link, and "" for none. Below one, git has no entry: no
// code reads path through a link, which can lead out of the repository.
func notFolder(dir, path string) (string, error) {
	path = strings.TrimSuffix(path, "/")
	for i := range len(path) {
		if path[i] != '/' {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path[:i])))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return "", nil
		case err != nil:
			return "", err
		case !info.IsDir():
			return path[:i], nil
		}
	}
	return "", nil
}

// isReplaced reports whether path, which git status lists in dir, is a tracked file that a
// link took the place of: the link is at a folder above it, see [notFolder]. It also reports
// whether path stays as it is, since that link is one of [leftOut]. A tracked file below a
// file, or at a folder now, needs no case: git add -u stages its deletion.
func isReplaced(dir, path string, setup map[string]string) (replaced, stays bool, err error) {
	above, err := notFolder(dir, path)
	if err != nil || above == "" {
		return false, false, err
	}
	info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(above)))
	if err != nil || info.Mode()&fs.ModeSymlink == 0 {
		return false, false, err
	}
	stays, err = leftOut(dir, above, setup)
	return true, stays, err
}

// reportGitlinks calls report, for each gitlink in the index of the repository at dir, with
// what [repositoryOf] returns for its path, unless that is "": git status with
// --ignore-submodules=all does not list the gitlink, and no git runs in that repository. A
// folder that Lstat cannot read counts as one with no .git entry.
func reportGitlinks(ctx context.Context, git gitRunner, dir string, report func(path string)) error {
	entries, err := git(ctx, dir, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(entries, "\x00") {
		about, path, ok := strings.Cut(entry, "\t")
		if !ok || !strings.HasPrefix(about, gitlinkMode+" ") {
			continue
		}
		if repository, err := repositoryOf(dir, path); err == nil && repository != "" {
			report(repository)
		}
	}
	return nil
}

// restoreLeftOut sets each path of [leftOut] in the index of the repository at dir to what
// the commit start has, when it differs there, so the next commit undoes a commit of the
// agent. The file on disk stays.
func restoreLeftOut(ctx context.Context, git gitRunner, dir, start string) error {
	differs, err := git(ctx, dir, "diff", "--cached", "--name-only", "-z", "--no-renames", start)
	if err != nil {
		return err
	}
	var paths []string
	for path := range strings.SplitSeq(differs, "\x00") {
		if path == "" {
			continue
		}
		out, err := leftOut(dir, path, nil)
		if err != nil {
			return err
		}
		if out {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return resetPaths(ctx, dir, start, paths)
}

// changedPaths is each path git status lists in dir, with no rename, so a moved file is
// two paths. With a gitlink, .gitmodules can make git status run git status in the
// repository there, and with it a program set in that repository's configuration, which
// no check read: --ignore-submodules=all wins over .gitmodules.
func changedPaths(ctx context.Context, git gitRunner, dir string) ([]string, error) {
	status, err := git(ctx, dir, "status", "--porcelain", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range strings.Split(status, "\x00") {
		if len(entry) >= 4 {
			paths = append(paths, entry[3:])
		}
	}
	return paths, nil
}

// leftOut reports whether path, relative to the repository at dir, stays out of a commit:
// it is [checkout.Dir] or under it, a link that qory wrote, or an entry of setup as the
// setup left it. A path below an entry that is not a folder, see [notFolder], is missing on
// the disk, and so is no link. A run's branch stops every later run of its work item when its
// commit has another .qory than the target version, or a path where the compose writes a link.
func leftOut(dir, path string, setup map[string]string) (bool, error) {
	trimmed := strings.TrimSuffix(path, "/")
	if trimmed == checkout.Dir || strings.HasPrefix(trimmed, checkout.Dir+"/") {
		return true, nil
	}
	above, err := notFolder(dir, trimmed)
	if err != nil {
		return false, err
	}
	if above == "" {
		if link, err := render.OwnLink(dir, filepath.Join(dir, filepath.FromSlash(trimmed))); link || err != nil {
			return link, err
		}
	}
	recorded, ok := setup[path]
	if !ok {
		return false, nil
	}
	now, err := entryDigest(dir, path)
	return now == recorded, err
}

// RecordSetup records what the setup of a run that can push left changed in each
// repository of b, in dir, the working folder, for [Push]. A caller runs it after the
// setup and before the session joins. The setup container is still up, and git status
// runs a filter that .git/config has, so a process in that container could add one after
// any check of the live file. git status therefore reads a copy that no process in the
// container can write, see [privateGitDir]. git runs as in [Push], so both read the same
// list of ignored files.
func RecordSetup(ctx context.Context, dir string, base []string, b *WorkBranch) error {
	return recordWith(ctx, dir, base, b, func(env ...string) gitRunner { return stepGit(noRemote, "", env...) })
}

// recordWith is [RecordSetup], with status as the [stepGit] of its git status.
func recordWith(ctx context.Context, dir string, base []string, b *WorkBranch, status func(env ...string) gitRunner) error {
	ctx = hostgit.WithBase(ctx, proxygit.Env(base, nil))
	dir, err := filepath.Abs(dir)
	if err != nil {
		return local(err)
	}
	b.setupChanges = make(map[string]map[string]string, len(b.Repositories))
	for _, r := range b.Repositories {
		folder := filepath.Join(dir, filepath.FromSlash(r.Dir))
		paths, err := setupPaths(ctx, dir, folder, r, status)
		if err != nil {
			return err
		}
		digests := make(map[string]string, len(paths))
		for _, path := range paths {
			if digests[path], err = entryDigest(folder, path); err != nil {
				return fmt.Errorf(textNoSetupRecord, shownPath(r.Path), integration.Printable(exports.HideError(err).Error()))
			}
		}
		b.setupChanges[r.Dir] = digests
	}
	return nil
}

// setupPaths is each path [changedPaths] lists for the repository r at folder in the
// working folder root, read through a [privateGitDir] that exists for this call alone.
// That folder is in this machine's temporary folder, which a run's container does not
// mount unless a mount of the run lists it.
func setupPaths(ctx context.Context, root, folder string, r WorkRepository, status func(env ...string) gitRunner) ([]string, error) {
	private, err := os.MkdirTemp("", "qory-record-")
	if err != nil {
		return nil, checkFailed(textSetupNotSafe, r, local(err))
	}
	defer os.RemoveAll(private)
	if err := privateGitDir(ctx, root, folder, private, r.Commit); err != nil {
		return nil, checkFailed(textSetupNotSafe, r, err)
	}
	git := status("GIT_DIR="+private, "GIT_WORK_TREE="+folder, "GIT_OBJECT_DIRECTORY="+filepath.Join(folder, ".git", "objects"))
	paths, err := changedPaths(ctx, git, folder)
	if err != nil {
		return nil, fmt.Errorf(textNoSetupRecord, shownPath(r.Path), integration.Printable(exports.HideURLError(err).Error()))
	}
	return paths, nil
}

// entryDigest is the SHA-256 sum of the entry path in dir as it is on disk, read with no
// git, so no filter runs: a missing entry, a link's target, a file's content and whether
// it may be run, or each entry of a folder, as git status lists a repository in a
// repository. Any other kind of file has its type alone.
func entryDigest(dir, path string) (string, error) {
	sum := sha256.New()
	above, err := notFolder(dir, path)
	if err != nil {
		return "", err
	}
	if above != "" {
		fmt.Fprint(sum, "missing\x00")
		return hex.EncodeToString(sum.Sum(nil)), nil
	}
	if err := addEntry(sum, filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(path, "/")))); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// addEntry adds the entry at path to sum, for [entryDigest].
func addEntry(sum io.Writer, path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		_, err = io.WriteString(sum, "missing\x00")
		return err
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(sum, "link %q\x00", target)
	case info.IsDir():
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			fmt.Fprintf(sum, "entry %q\x00", entry.Name())
			if err := addEntry(sum, filepath.Join(path, entry.Name())); err != nil {
				return err
			}
		}
		fmt.Fprint(sum, "end\x00")
	case info.Mode().IsRegular():
		f, err := os.OpenFile(path, os.O_RDONLY|render.OpenFlags, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		fmt.Fprintf(sum, "file %o\x00", info.Mode().Perm()&0o111)
		_, err = io.Copy(sum, f)
		return err
	default:
		fmt.Fprintf(sum, "other %s\x00", info.Mode().Type())
	}
	return nil
}

// readsAhead prints [textReadAhead] on report for each read target of b, in the working
// folder dir, whose HEAD has a commit that its [WorkRepository.Start] does not have, once per
// run: b records each read target it printed. git runs there only after [safeFurther] passed.
// shortcut: a read target that fails that check, or whose git fails, gets no line; check it
// in a container if a run must report those too.
func readsAhead(ctx context.Context, dir string, b *WorkBranch, report io.Writer) {
	for _, r := range b.Repositories {
		if !r.ReadOnly || b.readsShown[r.Dir] {
			continue
		}
		folder := filepath.Join(dir, filepath.FromSlash(r.Dir))
		if safeFurther(ctx, dir, folder, true) != nil {
			continue
		}
		out, err := stepGit(noRemote, "")(ctx, folder, "rev-list", "--max-count=1", "HEAD", "^"+r.Start)
		if err != nil || strings.TrimSpace(out) == "" {
			continue
		}
		if b.readsShown == nil {
			b.readsShown = map[string]bool{}
		}
		b.readsShown[r.Dir] = true
		fmt.Fprintf(report, textReadAhead+"\n", shownPath(r.Path))
	}
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
	_, err = git(ctx, dir, "merge-base", "--is-ancestor", r.Commit, head)
	if ge := (*gitError)(nil); errors.As(err, &ge) && ge.exit == 1 {
		return "", fmt.Errorf(textNotFastForward, shownPath(r.Path), shortCommit(head), shortCommit(r.Commit))
	}
	if err != nil {
		return "", fmt.Errorf(textNoHistory, shownPath(r.Path), shortCommit(r.Commit), integration.Printable(exports.HideURLError(err).Error()))
	}
	return head, nil
}

// pushTo pushes head of the repository r at dir to the branch of b, as a fast-forward, or,
// with lease set, as a push that replaces the branch only while it is at lease. A push that
// the forge rejects as a branch that moved is a [*HeadChanged] with the head read again
// through read; with the read failing, the head is lease, or the commit r expected.
func pushTo(ctx context.Context, git, read gitRunner, dir string, b *WorkBranch, r WorkRepository, head, lease string, report io.Writer) error {
	args := []string{"push", "--quiet", "--no-verify", "--no-recurse-submodules"}
	if lease != "" {
		args = append(args, "--force-with-lease=refs/heads/"+b.Ref+":"+lease)
	}
	args = append(args, "--", repoURL(b.system, r.Path), head+":refs/heads/"+b.Ref)
	_, err := git(ctx, dir, args...)
	if err == nil {
		return nil
	}
	var ge *gitError
	if errors.As(err, &ge) {
		printFailedPush(ctx, stepGit(noRemote, ""), dir, r, head, ge.output, report)
		if pushMoved.MatchString(ge.output) {
			now, readErr := remoteHead(ctx, read, dir, b.system, r.Path, b.Ref)
			if readErr != nil {
				now = cmp.Or(lease, r.Commit)
			}
			return &HeadChanged{Branch: b.Ref, System: b.system, Path: r.Path, Head: now, Expected: r.Commit, LastPush: r.Commit != r.Start}
		}
	}
	return pushFailed(b, r.Path, err)
}

// printFailedPush prints on report, the step's log, what the error's one line leaves out:
// each remote: line that git printed in output, and the commits from r to head, which did
// not reach the branch. Each shows no URL's login or password. A failed read of the
// commits prints none, since the error that follows is the one the run ends with.
func printFailedPush(ctx context.Context, git gitRunner, dir string, r WorkRepository, head, output string, report io.Writer) {
	var forge []string
	for line := range strings.Lines(output) {
		if text, ok := strings.CutPrefix(strings.TrimSpace(line), "remote:"); ok && strings.TrimSpace(text) != "" {
			forge = append(forge, shownText(strings.TrimSpace(text)))
		}
	}
	printAtMost(forge, report, func(line string) { fmt.Fprintf(report, textForgeLine+"\n", line) })
	log, err := git(ctx, dir, "log", "--format=%H %s", r.Commit+".."+head)
	if err != nil {
		return
	}
	printAtMost(strings.Split(strings.TrimSpace(log), "\n"), report, func(line string) {
		commit, subject, _ := strings.Cut(line, " ")
		fmt.Fprintf(report, textNotPushed+"\n", shownPath(r.Path), shortCommit(commit), shownText(subject))
	})
}

// printAtMost calls print for the first [maxFailedPushLines] lines, and prints how many
// it left out.
func printAtMost(lines []string, report io.Writer, print func(string)) {
	for _, line := range lines[:min(len(lines), maxFailedPushLines)] {
		print(line)
	}
	if left := len(lines) - maxFailedPushLines; left > 0 {
		fmt.Fprintf(report, textFurtherLines+"\n", left)
	}
}

// shownText is text from the forge or a commit as a line shows it: printable, with the
// login and the password of each URL hidden, since [exports.HideURLs] keeps the login.
func shownText(text string) string {
	hidden := urlLogin.ReplaceAllString(exports.HideURLError(errors.New(text)).Error(), "://(hidden)@")
	return integration.Printable(hidden)
}

// pushFailed is the line of a push of the repository path that failed with err: by what
// git wrote, a remote that stopped the update, a 403, a credential,
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
	case pushRejected.MatchString(output):
		return fmt.Errorf(textPushRefused, b.Ref, shown, host, cause)
	case pushForbidden.MatchString(output):
		return &PushRefusedError{fmt.Sprintf(textPushForbidden, shown, host, b.Ref, cause)}
	case answered.MatchString(output):
		return fmt.Errorf(textPushNoCredential, shown, host, cause)
	}
	return fmt.Errorf(textPushNotReached, shown, host, cause)
}

// PushRefusedError is the error of a push that got 403: its text, and the end code
// push_refused that a run closes with.
type PushRefusedError struct{ text string }

// Error is the text of the push's failure.
func (e *PushRefusedError) Error() string { return e.text }

// EndCode is push_refused.
func (e *PushRefusedError) EndCode() contracts.EndCode { return contracts.EndCodePushRefused }

// pushCause is the line of git's output with the reason a push failed, made printable: the
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
	return shownText(line)
}

// maxBaseBranch is the most characters of the default branch name that the push records.
const maxBaseBranch = 255

// baseBranch is the default branch name for base.branch: name, or "" when it has more than
// [maxBaseBranch] characters, since a version whose schema stops a longer name would be
// stopped after the push landed.
func baseBranch(name string) string {
	if utf8.RuneCountInString(name) > maxBaseBranch {
		return ""
	}
	return name
}

// withCommit is version with the repository path on system set to head, with the run's
// base and commits, or with a new entry when it has none. The entries compare as
// [WorkBranch.CommitOf] compares them. A set entry keeps no program output of an earlier
// version: the commit URLs and the draft are written again at the submit step.
func withCommit(version []Repository, system, path, head, base, branch string, commits []Commit) []Repository {
	entry := Repository{System: system, Path: path, Commit: head, Base: &Base{Commit: base, Branch: branch}, Commits: commits}
	for i := range version {
		if version[i].System == system && sameRepository(system == ForgeGitHub, version[i].Path, path) {
			entry.Path = version[i].Path
			version[i] = entry
			return version
		}
	}
	return append(version, entry)
}
