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
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/proxygit"
	"github.com/qoryai/qory/internal/render"
	"github.com/qoryai/qory/internal/submission"
)

// The words of the push step of a write run. Each is one format string.
const (
	textNoNewCommits     = "qory run: no new commits to push"
	textPushed           = "qory run: pushed %s to %s at %s"
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
	textPushedNoVersion  = "qory run: %s: qory pushed %s and stored no version, because the push of the next repository failed: %s"
	textPushRefused      = "qory run: change the rule on %[3]s that stops a push to %[1]s of %[2]s, then take the work item in again: the push failed: %[4]s; the code is used"
	textPushForbidden    = "qory run: Contact support: the push was refused (403).\n  One of these most likely stopped it:\n  1. The run's push proxy refuses a push that is not a plain push to the push_ref of the push credential (for example a signed push or a push with push options), and qory pushes to refs/heads/%[3]s (wall:push-ref).\n  2. The run's push credential has no item for /%[1]s.git/git-receive-pack on %[2]s, and the push proxy refuses every other host and path.\n  3. The forge stopped the push: its permission for the push credential, or a branch rule.\n  The push of %[1]s to %[2]s returned 403: %[4]s; the code is used"
	textPushNoCredential = "qory run: give the run's credential for %[2]s write access to %[1]s, then take the work item in again: the push through the run's gateway failed: %[3]s; the code is used"
	textPushNotReached   = "qory run: Let the run's gateway reach %[2]s, then take the work item in again.\n  The push of %[1]s through the run's push proxy failed: %[3]s; the code is used"
)

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
		ceiling := "GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir)
		return hostRunnerOf(p, noMaintenance, input, append(slices.Clone(env), ceiling))(ctx, dir, args...)
	}
}

// Push commits what the agent left in each repository of b, in dir, the working folder,
// and pushes each repository whose HEAD is past its [WorkRepository.Commit] to the branch
// b.Ref as a fast-forward, by the URL qory builds, through the run's push proxy p. Before
// the first push, it reads the head of the branch at the forge of each such repository
// through the read proxy and compares it with [WorkRepository.Commit], see [checkHead]: a
// head that is not the expected one stops every push with a [*HeadChanged], and a head
// that cancelled runs left is replaced with a lease on it. It returns the repositories of
// the new version, nil when no repository has new commits: [WorkBranch.Version] with the
// commit of each repository pushed replaced, or added for one with no entry, and its base
// and commits from [WorkRepository.Start]. It prints a line on report for each repository
// pushed.
// Each push that lands sets the repository's [WorkRepository.Commit] and its entry of
// [WorkBranch.Version] to the commit pushed, so a later Push of the same run, after a
// later session, pushes only the commits past it, and pushes nothing when there are none.
// [WorkBranch.PushedPaths] lists the repositories of the last Push alone.
//
// Before any git writes, [safeAfterRun] checks the target and [safeFurther] each further
// repository, since the agent could write each .git. Every git runs with the environment
// [proxygit.Env] makes, so it reads no system or global configuration file, and with
// [proxygit.Hardened], which turns off the hooks in .git, fsmonitor and credential
// helpers. No option turns a filter off: the check stops on each key that git init and
// the fetch do not write, filter keys among them, so a filter stays out only while no
// process writes .git/config after the check. The commits of every repository come
// before the first push, so a failed commit pushes nothing. A path that [RecordSetup]
// recorded and that is as the setup left it stays out of the commit.
//
// An agent that was killed can leave .git/index.lock, which would stop the commit. After
// the checks, [clearIndexLocks] removes that one file of each repository when
// agentStopped is true, and stops the push when a lock is there and it is false.
func Push(ctx context.Context, dir string, base []string, b *WorkBranch, message string, read, p proxygit.Proxy, agentStopped bool, report io.Writer) ([]submission.Repository, error) {
	b.landed = nil
	ctx = hostgit.WithBase(ctx, proxygit.Env(base, nil))
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, local(err)
	}
	folders := make([]string, len(b.Repositories))
	for i, r := range b.Repositories {
		folders[i] = filepath.Join(dir, filepath.FromSlash(r.Dir))
		if i > 0 {
			if err := safeFurther(ctx, dir, folders[i], true); err != nil {
				return nil, notSafe(r, err)
			}
			continue
		}
		if err := safeAfterRun(ctx, folders[i]); err != nil {
			return nil, notSafe(r, err)
		}
	}
	if err := clearIndexLocks(b.Repositories, folders, agentStopped, report); err != nil {
		return nil, err
	}
	heads := make([]string, len(b.Repositories))
	for i, r := range b.Repositories {
		if err := commitLeft(ctx, folders[i], r, b.setupChanges[r.Dir], message, report); err != nil {
			return nil, fmt.Errorf(textNoCommitLeft, shownPath(r.Path), integration.Printable(exports.HideURLError(err).Error()))
		}
		if heads[i], err = headPast(ctx, stepGit(noRemote, ""), folders[i], r); err != nil {
			return nil, err
		}
	}
	// No repository is pushed when a commit of one has a path of the hand-off folder.
	for i, r := range b.Repositories {
		if heads[i] == "" {
			continue
		}
		if err := handoffCommitted(ctx, stepGit(noRemote, ""), folders[i], b.system, r, r.Start, heads[i]); err != nil {
			return nil, err
		}
	}
	b.pushed = nil
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
			if len(b.pushed) > 0 {
				return nil, &pushedNoVersion{text: fmt.Sprintf(textPushedNoVersion, b.DeleteBranch(), b.PushedPaths(), strings.TrimPrefix(err.Error(), "qory run: ")), err: err}
			}
			return nil, err
		}
		b.pushed = append(b.pushed, shownPath(r.Path))
		b.landed = append(b.landed, Landed{System: b.system, Path: r.Path, Commit: heads[i]})
		fmt.Fprintf(report, textPushed+"\n", b.Ref, shownPath(r.Path), shortCommit(heads[i]))
		// The branch is at heads[i] now, so a later Push of the run starts there.
		b.Repositories[i].Commit = heads[i]
		commits, err := runCommits(ctx, stepGit(noRemote, ""), folders[i], r.Start, heads[i])
		if err != nil {
			return nil, err
		}
		b.Version = withCommit(slices.Clone(b.Version), b.system, r.Path, heads[i], r.Start, baseBranch(r.DefaultBranch), commits)
	}
	if len(b.pushed) == 0 {
		fmt.Fprintln(report, textNoNewCommits)
		return nil, nil
	}
	return slices.Clone(b.Version), nil
}

// pushedNoVersion is the error of a push that failed after an earlier repository landed: its
// text is [textPushedNoVersion], and it unwraps to the push's error, so a [*HeadChanged] of
// the later repository is still found.
type pushedNoVersion struct {
	text string
	err  error
}

func (e *pushedNoVersion) Error() string { return e.text }

func (e *pushedNoVersion) Unwrap() error { return e.err }

// runCommits lists the commits from start, not included, to head, included, of the
// repository at dir, oldest first, as the commits of a submission with no step: the
// caller sets each step.
func runCommits(ctx context.Context, git gitRunner, dir, start, head string) ([]submission.Commit, error) {
	out, err := git(ctx, dir, "rev-list", "--reverse", start+".."+head)
	if err != nil {
		return nil, fmt.Errorf(textNoHistory, shownPath(dir), shortCommit(start), integration.Printable(exports.HideURLError(err).Error()))
	}
	var commits []submission.Commit
	for _, c := range strings.Fields(out) {
		commits = append(commits, submission.Commit{Commit: c})
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
// untracked and ignored files, but [leftOut]'s and a repository in the repository, for
// which a line goes to report. A path of [leftOut] that a commit of the agent changed
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
	var paths []string
	for _, path := range changed {
		out, err := leftOut(dir, path, setup)
		switch {
		case err != nil:
			return err
		case out:
		case isRepository(dir, strings.TrimSuffix(path, "/")):
			fmt.Fprintf(report, textNestedRepository+"\n", shownText(strings.TrimSuffix(path, "/")), shownPath(r.Path))
		default:
			paths = append(paths, path)
		}
	}
	if err := reportGitlinks(ctx, git, dir, r, report); err != nil {
		return err
	}
	if len(paths) > 0 {
		add := stepGit(noRemote, pathspecs(paths))
		if _, err := add(ctx, dir, "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return err
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
	_, err = commit(ctx, dir, "commit", "--quiet", "--no-verify", "--no-gpg-sign", "-m", message)
	return err
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

// isRepository reports whether path, relative to the repository at dir, has a .git entry.
func isRepository(dir, path string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path), ".git"))
	return err == nil
}

// reportGitlinks prints [textNestedRepository] for each gitlink in the index of the
// repository at dir that has a repository in its folder: git status with
// --ignore-submodules=all does not list it, and no git runs in that repository.
func reportGitlinks(ctx context.Context, git gitRunner, dir string, r WorkRepository, report io.Writer) error {
	entries, err := git(ctx, dir, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(entries, "\x00") {
		about, path, ok := strings.Cut(entry, "\t")
		if ok && strings.HasPrefix(about, gitlinkMode+" ") && isRepository(dir, path) {
			fmt.Fprintf(report, textNestedRepository+"\n", shownText(path), shownPath(r.Path))
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
	_, err = stepGit(noRemote, pathspecs(paths))(ctx, dir, "reset", "--quiet", start, "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
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
// setup left it. A run's branch stops every later run of its work item when its commit
// has another .qory than the target version, or a path where the compose writes a link.
func leftOut(dir, path string, setup map[string]string) (bool, error) {
	trimmed := strings.TrimSuffix(path, "/")
	if trimmed == checkout.Dir || strings.HasPrefix(trimmed, checkout.Dir+"/") {
		return true, nil
	}
	if link, err := render.OwnLink(dir, filepath.Join(dir, filepath.FromSlash(trimmed))); link || err != nil {
		return link, err
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
		return fmt.Errorf(textPushForbidden, shown, host, b.Ref, cause)
	case answered.MatchString(output):
		return fmt.Errorf(textPushNoCredential, shown, host, cause)
	}
	return fmt.Errorf(textPushNotReached, shown, host, cause)
}

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

// maxBaseBranch is the most characters of base.branch that the submission format takes.
const maxBaseBranch = 255

// baseBranch is the default branch name for base.branch: name, or "" when it has more than
// [maxBaseBranch] characters, since a version that the format refuses would be refused after
// the push landed.
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
func withCommit(version []submission.Repository, system, path, head, base, branch string, commits []submission.Commit) []submission.Repository {
	entry := submission.Repository{System: system, Path: path, Commit: head, Base: &submission.Base{Commit: base, Branch: branch}, Commits: commits}
	for i := range version {
		if version[i].System == system && sameRepository(system == ForgeGitHub, version[i].Path, path) {
			entry.Path = version[i].Path
			version[i] = entry
			return version
		}
	}
	return append(version, entry)
}
