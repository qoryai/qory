// Package preparation fetches the code of a run into its working folder, and prepares
// the working folder inside the wall before the agent starts. [EnvSystem] says the host
// of the code. A run from a run code takes its repositories from its documents, the
// target's first and then the read targets', see [Target.Repositories]; any other run
// takes them from [EnvRepositories], the run's repository first.
//
// [RunAt] fetches on this machine, as qory run does once the run is open: the run's
// repository into the working folder, an empty folder, in which it runs git init, or an
// empty git working tree, at the head of its default branch, or at a commit with [RunAt],
// and each further one under .qory/repositories, every request through the run's host
// proxy, which adds what a host needs, so no token is on the machine or in git's
// configuration. Every fetch is at depth one, over https alone, with no credential helper
// and no prompt, as package [proxygit] sets git up.
//
// In a container of the run's image, the setup container or the agent's, qory run prepare
// calls [Prepare], which adds to the working folder the files of the image's copy at
// [Seed] that the fetched commit does not track, when a prebuilt image has one, and
// [Start], which runs the start commands of the repository's development container
// configuration. qory run agent-env calls [FreshHome] last before the agent starts.
//
// Every git the preparation runs has hooks off, through core.hooksPath, and the file
// system monitor off; it reads the configuration of the working folder's repository
// alone, none of the user's, the system's or the environment's, and no replace object.
package preparation

import (
	"bufio"
	"bytes"
	"context"
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
	"time"

	"github.com/qoryai/integrations/contracts"
	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/proxygit"
)

// The variables that say where a run's code is. Their names are each in one constant,
// so a change of name is a change here alone.
const (
	// EnvSystem names the host the repositories are on, such as github.com.
	EnvSystem = "QORY_RUN_SYSTEM"
	// EnvRepositories names the repositories, comma-separated with no space, the run's
	// repository first. A run from a run code does not read it, see [Target.Repositories].
	EnvRepositories = "QORY_RUN_REPOSITORIES"
)

// ForgeGitHub is the one forge whose repository form qory knows: github.com.
const ForgeGitHub = "github.com"

// The lines of a run from a run code for the fetch's lines that contain "run again". The code
// is redeemed before the fetch, so the next run needs a new take-in. The steps on this
// machine are for the person with a shell on it.
const (
	textNotFetchedFromCode    = "qory run: add a credential for %s that reads %s to the run configuration, then take the work item in again in Qory Apiary: the fetch of %s through the run's gateway failed: %s; the code is used"
	textNotReachedFromCode    = "qory run: check that the run's policy allows %[1]s and that %[1]s is reachable, then take the work item in again in Qory Apiary: the fetch of %[2]s through the run's gateway failed: %[3]s; the code is used"
	textNoBranchFromCode      = "qory run: give %s on %s a default branch with a commit, then take the work item in again in Qory Apiary: the fetch through the run's gateway failed: %s; the code is used"
	textTreeFailedFromCode    = "qory run: check git on this machine, the working folder's .git, and each repository and .git above it, then take the work item in again in Qory Apiary: git failed: %s; the code is used"
	textAboveFailedFromCode   = "qory run: check git on this machine, the working folder, and each repository and .git above it, then take the work item in again in Qory Apiary: git failed: %s; the code is used"
	textGitFailedFromCode     = "qory run: check that git on this machine can read the working folder, then take the work item in again in Qory Apiary: git failed: %s; the code is used"
	textLocalFromCode         = "qory run: check that this machine can read and write the working folder, then take the work item in again in Qory Apiary: the step failed: %s; the code is used"
	textConfigKeyFromCode     = "qory run: take the work item in again in Qory Apiary, and start its run in a new empty git working tree, or remove %s from %s first: qory fetches only into a repository whose configuration has only the keys git init writes, since git on this machine would use any other key, such as a filter, a proxy or http.sslVerify; the code is used"
	textConfigNotFileFromCode = "qory run: take the work item in again in Qory Apiary, and start its run in a new empty git working tree: the working folder's %s is not a regular file, and qory fetches only into a repository whose configuration it can read; the code is used"
	textAlternatesFromCode    = "qory run: take the work item in again in Qory Apiary, and start its run in a new empty git working tree: the working folder's .git has %s, which makes git read objects from outside the working folder; the code is used"
	textGitLinkFromCode       = "qory run: take the work item in again in Qory Apiary, and start its run in a new empty git working tree, or remove the link %s first: git would read and write through that link; the code is used"
	textHardLinkFromCode      = "qory run: take the work item in again in Qory Apiary, and start its run in a new empty git working tree, or remove the hard link %s first: a write to it would change another file; the code is used"
	textGraftsFromCode        = "qory run: take the work item in again in Qory Apiary, and start its run in a new empty git working tree: the working folder's .git has info/grafts, which gives commits other parents; the code is used"
	textCommonDirFromCode     = "qory run: take the work item in again in Qory Apiary, and start its run in a new empty git working tree: the working folder's .git has a commondir file, which can point git at another repository; the code is used"
)

// fromCodeLines is the line of a run from a run code for each line of a run from none.
var fromCodeLines = map[string]string{
	textNotFetched:    textNotFetchedFromCode,
	textNotReached:    textNotReachedFromCode,
	textNoBranch:      textNoBranchFromCode,
	textTreeFailed:    textTreeFailedFromCode,
	textAboveFailed:   textAboveFailedFromCode,
	textGitFailed:     textGitFailedFromCode,
	textLocal:         textLocalFromCode,
	textConfigKey:     textConfigKeyFromCode,
	textConfigNotFile: textConfigNotFileFromCode,
	textAlternates:    textAlternatesFromCode,
	textGitLink:       textGitLinkFromCode,
	textHardLink:      textHardLinkFromCode,
	textGrafts:        textGraftsFromCode,
	textCommonDir:     textCommonDirFromCode,
}

// The words of the fetch qory run does. Each is one format string.
const (
	textNoSystem = "qory run: set %[2]s beside %[1]s in the run configuration: %[2]s is the host of the repositories %[1]s lists, and it is not set"
	textNotHost  = "qory run: set %s to a host name, such as github.com"
	textNotPath  = "qory run: make entry %[2]d of %[1]s a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"
	textNested   = "qory run: remove entry %[2]d, %[3]s, or entry %[4]d, %[5]s, from %[1]s: the first is inside the second, and a repository is not cloned inside another"
	textTwice    = "qory run: list %[2]s once in %[1]s, which lists it twice"
	textTooLong  = "qory run: shorten %s to %d bytes or fewer"
	textNotEmpty = "qory run: start the run in an empty git working tree: the working folder contains files already, and the run's repository is fetched only into an empty one"
	// textNotEmptyReads and textNotTreeReads are textNotEmpty and textNotTree of a run
	// from a run code with read targets and no repository in the working folder: the
	// cause alone, since the run is a dispatched one.
	textNotEmptyReads = "qory run: The working folder contains files, so the run did not start; the code is used\n  qory fetches the read-only repositories only into an empty folder."
	textNotTreeReads  = "qory run: The working folder has files and no .git, so the run did not start; the code is used\n  qory fetches the read-only repositories only into an empty folder or an empty git working tree."
	// textSetupTooLong is the stop of [Prepare] on a list over its bound, which qory sets
	// from the documents of a run code: the cause alone.
	textSetupTooLong   = "qory run: The run's repository list is longer than %d bytes, so the setup did not start"
	textNotFetched     = "qory run: add a credential for %s that reads %s to the run configuration, then run again. The fetch of %s through the run's gateway failed: %s"
	textNotReached     = "qory run: check that the run's policy allows %[1]s and that %[1]s is reachable, then run again. The fetch of %[2]s through the run's gateway failed: %[3]s"
	textNoBranch       = "qory run: give %s on %s a default branch with a commit, then run again. The fetch through the run's gateway failed: %s"
	textKept           = "qory run: remove .qory/repositories from the working folder of %s: qory clones the further repositories there, and nothing is cloned over it"
	textQoryNotDir     = "qory run: remove .qory from the working folder of %s: it is a file or a link, where qory clones the further repositories into a folder, and nothing is cloned over it"
	textOtherOwner     = "qory run: list in %[1]s only repositories of owner %[4]s: entry %[2]d is of owner %[3]s, and on %[5]s the repositories of a run are of one owner"
	textNotGitHub      = "qory run: write entry %[2]d of %[1]s as owner/name, the form of a repository on %[3]s"
	textNotTree        = "qory run: start the run in an empty folder outside any git repository, or at the top of a git working tree without commits: the working folder is neither, and qory fetches the run's repository only into one of these"
	textDirLink        = "qory run: start the run in the folder that the link leads to: the working folder is a link, and qory runs git init only in a folder"
	textNoGitPart      = "qory run: start the run in a new empty folder or a new empty git working tree: the working folder's .git has no %s, so git does not take that folder for a repository"
	textExcludeLink    = "qory run: remove the link %s from the working folder: qory writes the exclude file of the repository only where no link leads"
	textExcludeOutside = "qory run: start the run in a git working tree whose exclude file is inside the working folder: the exclude file, %s, is outside it, and qory writes it only inside"
	textGitNotDir      = "qory run: start the run in a git working tree whose .git is a folder: the working folder's .git is a file or a link, which points at a repository elsewhere"
	textHasRemote      = "qory run: start the run in a new empty folder or a new empty git working tree: the working folder's repository has a remote already, and qory fetches the run's repository only into a repository with no remote"
	textTreeFailed     = "qory run: check git on this machine, the working folder's .git, and each repository and .git above it, then run again. git failed: %s"
	textAboveFailed    = "qory run: check git on this machine, the working folder, and each repository and .git above it, then run again. git failed: %s"
	textGitFailed      = "qory run: check that git on this machine can read the working folder, then run again. git failed: %s"
	textLocal          = "qory run: check that this machine can read and write the working folder, then run again. The step failed: %s"
	textConfigKey      = "qory run: start the run in a new empty git working tree, or remove %s from %s, then run again. qory fetches only into a repository whose configuration has only the keys git init writes, since git on this machine would use any other key, such as a filter, a proxy or http.sslVerify"
	textConfigNotFile  = "qory run: start the run in a new empty git working tree, then run again: the working folder's %s is not a regular file, and qory fetches only into a repository whose configuration it can read"
	textAlternates     = "qory run: start the run in a new empty git working tree, then run again: the working folder's .git has %s, which makes git read objects from outside the working folder"
	textGitLink        = "qory run: start the run in a new empty git working tree, or remove the link %s, then run again: git would read and write through that link"
	textHardLink       = "qory run: start the run in a new empty git working tree, or remove the hard link %s, then run again: a write to it would change another file"
	textGrafts         = "qory run: start the run in a new empty git working tree, then run again: the working folder's .git has info/grafts, which gives commits other parents"
	textCommonDir      = "qory run: start the run in a new empty git working tree, then run again: the working folder's .git has a commondir file, which can point git at another repository"
	// The cause of each of these lines alone, for the line after the agent exited.
	causeConfigKey      = "it has %s in %s, and the check stops on each key that neither git init nor the fetch writes, because a key such as a filter, a proxy or http.sslVerify changes what git on this machine does"
	causeConfigNotFile  = "its %s is not a regular file"
	causeAlternates     = "its .git has %s, which makes git read objects from outside the working folder"
	causeGitLink        = "it has the link %s, and git would read and write through that link"
	causeHardLink       = "it has the hard link %s, and a write to it would change another file"
	causeCommonDir      = "its .git has a commondir file, which can point git at another repository"
	causeGrafts         = "its .git has info/grafts, which gives commits other parents, so a push that overwrites a branch would pass as a fast-forward"
	causeGitFailed      = "git failed: %s"
	causeLocal          = "the read of its files failed: %s"
	causeNoGitPart      = "its .git has no %s, so git would take a folder above it for the repository"
	textFetched         = "qory run: fetched %s at %s in %s"
	textFetchedOne      = "qory run: fetched %s at %s and 1 further repository in %s"
	textFetchedMany     = "qory run: fetched %s at %s and %d further repositories in %s"
	textFetchedReadOne  = "qory run: fetched 1 read-only repository in %s"
	textFetchedReads    = "qory run: fetched %d read-only repositories in %s"
	textFetchedAndRead  = "qory run: fetched %s at %s and 1 read-only repository in %s"
	textFetchedAndReads = "qory run: fetched %s at %s and %d read-only repositories in %s"
	textNoCommit        = "qory run: take the work item in again in Qory Apiary: the commit %[1]s that the run's documents set is not on %[2]s on %[3]s. The fetch through the run's gateway failed: %[4]s; the code is used"
	textNoFetchByID     = "qory run: let %[3]s serve a fetch of a commit by its id, such as with git's uploadpack.allowReachableSHA1InWant, then take the work item in again in Qory Apiary: %[3]s does not serve the commit %[1]s of %[2]s by its id, or does not have it. The fetch through the run's gateway failed: %[4]s; the code is used"
	textNotCommitID     = "qory run: ask Qory Apiary's instance admin to check the adapter of the target: the run's documents set the target version %s, and qory fetches a target at a version only when the version is a commit id of 40 or 64 hex digits; the code is used"
	// The lines of a list of a run from a run code that [Target.Repositories] stops on: the
	// cause alone, since the run is a dispatched one. The first %s of textListNotPath and
	// textListNotGitHub is "target" or "read target".
	textListNotPath     = "qory run: A %s is not a repository path, so the run did not start; the code is used\n  The path is %s. A repository path is segments of A-Z, a-z, 0-9, dot, underscore and dash, 1 to 200 characters, with no ., .. or .git segment."
	textListNotGitHub   = "qory run: A %[1]s is not owner/name, so the run did not start; the code is used\n  The path is %[2]s, and a repository on %[3]s has a path of the form owner/name."
	textReadOtherSystem = "qory run: A read target is on another host, so the run did not start; the code is used\n  The read target %[1]s is on %[2]s, and the run's repositories are on %[3]s."
	textReadOtherOwner  = "qory run: A read target is of another owner, so the run did not start; the code is used\n  The read target %[1]s is of owner %[2]s, and on %[4]s the repositories of a run are of one owner, %[3]s."
	textReadNested      = "qory run: A read target is inside another, so the run did not start; the code is used\n  The read target %[1]s is inside the read target %[2]s, and qory clones no repository inside another."
	textKeptReads       = "qory run: The working folder has .qory/repositories, so the run did not start; the code is used\n  qory clones the read-only repositories there, and nothing is cloned over it."
	textQoryReads       = "qory run: The working folder's .qory is not a folder, so the run did not start; the code is used\n  qory clones the read-only repositories into a folder there, and nothing is cloned over a file or a link."
	// textNoDefault, textNoCommits, textNotBranch, textStopped and textCut stand for
	// git's line in [textNotFetched] and [textGitFailed]: a remote with refs that names
	// no default branch, a remote with no refs at all, a default branch that is no
	// branch name qory fetches, a git stopped at the limit of one call, and a git
	// stopped because the preparation was, by a signal or the caller's deadline.
	textNoDefault = "the remote names no default branch"
	textNoCommits = "the repository has no commits"
	textNotBranch = "the default branch the remote names, %s, is not a branch name qory fetches"
	textStopped   = "git %s ran past %s and was stopped"
	textCut       = "git %s was stopped before it finished"
)

const (
	// maxList is the most bytes [EnvRepositories] may hold where a run configuration sets
	// it.
	maxList = 256
	// maxSetupList is the most bytes of [EnvRepositories] that [Prepare] takes: the list
	// that qory builds from the documents of a run code, a target and
	// [contracts.MaxReadTargets] read targets of [maxPath] characters each, with a comma
	// between two.
	maxSetupList = (contracts.MaxReadTargets+1)*(maxPath+1) - 1
	// maxPath is the most characters one repository path may have.
	maxPath = 200
)

// gitLimit bounds each git the preparation runs, within the caller's context.
const gitLimit = 15 * time.Minute

// limitOf is the limit of a git call by its subcommand: [gitLimit] for each. A test
// shortens it for one subcommand alone, so the others never meet it on a slow machine.
var limitOf = func(string) time.Duration { return gitLimit }

// errLimit is the cause of a call's context that ran past its limit.
var errLimit = errors.New("the limit of one git call")

// Repositories is the directory, relative to the working folder, the further
// repositories are cloned under, each at its path.
const Repositories = ".qory/repositories"

// ExcludeLine keeps [Repositories] out of the run repository's git status.
const ExcludeLine = "/" + Repositories + "/"

var (
	hostName = regexp.MustCompile(`^[a-z0-9.-]{1,253}$`)
	segment  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// baseURL replaces https://<system> in front of each repository's path when a test sets
// it, to point the fetches at local repositories. Nothing but a test sets it: the
// production URL is built from the validated system and paths alone, and its scheme is
// always https.
var baseURL = ""

// Target is the target of a run's documents, the place a change would go, and the
// version of it that the documents set: a run code's target.address.system,
// target.address.path and target_version.
type Target struct {
	System, Path, Version string
	// Reads is the address of each read target of the documents that has one, in their
	// order: the places the run only reads. [Target.Repositories] lists them after the
	// target.
	Reads []Read
	// Branch is the branch of the run's work item, nil for none. It is used only when
	// the fetch uses Version.
	Branch *WorkBranch
	// FromCode is a run from a run code, with or without a target: its one-use code is
	// redeemed before the fetch, so a stop of the fetch has the line of [fromCodeLines].
	// The fetch takes its list from the documents, see [Target.Repositories], never from
	// [EnvRepositories].
	FromCode bool
	// Prebuild is a prebuild from a run code: with FromCode, a stop has the line that
	// [prebuildLine] makes, since a prebuild has no work item to take in again.
	Prebuild bool
}

// Read is the address of a read target of a run's documents: a host name and a path.
type Read struct {
	System, Path string
}

// runLine is a stop whose line is text with args, for a run from no run code. [Target.stop]
// formats the line of a run from a run code with the same args.
type runLine struct {
	text string
	args []any
}

func (l *runLine) Error() string { return fmt.Sprintf(l.text, l.args...) }

// lineOf is the [runLine] of text with args.
func lineOf(text string, args ...any) *runLine { return &runLine{text, args} }

// stop is err as the run t fetches for prints it: a [runLine] with a line in
// [fromCodeLines] has that line for a run from a run code, and [prebuildLine]'s for a
// prebuild. Any other error, and any error of another run, stays as it is.
func (t Target) stop(err error) error {
	var line *runLine
	if !t.FromCode || !errors.As(err, &line) {
		return err
	}
	text, ok := fromCodeLines[line.text]
	if !ok {
		return err
	}
	if t.Prebuild {
		text = prebuildLine(text)
	}
	return fmt.Errorf(text, line.args...)
}

// prebuildLine is text, a line of [fromCodeLines], for a prebuild from a run code: its fix
// is to start the run again with a new run code.
func prebuildLine(text string) string {
	return strings.NewReplacer(
		"take the work item in again in Qory Apiary, and start its run in", "start the run again with a new run code, in",
		"then take the work item in again in Qory Apiary", "then start the run again with a new run code",
	).Replace(text)
}

// RunAt fetches the run's repositories into dir, the working folder, on this machine
// through the run's host proxy p, and reports whether it fetched any. The host is the
// one [EnvSystem] names. A run from a run code, at.FromCode, fetches the list of
// [Target.Repositories], from its documents; any other run fetches the list
// [EnvRepositories] names, which [Check] checks. With an empty list it does nothing and
// returns false. The list is checked whole before any fetch. The run's repository, the
// first of the list, goes into the working folder at the head of its default branch, and
// each further one under [Repositories]; a list with no target's repository, see
// [Target.Repositories], puts each one under [Repositories], into a working folder that
// stays an empty repository. Once every repository is fetched, it prints on report the
// line with what it fetched, the run's repository's commit, and how long the fetch took.
// Every error's text is one line, ready for stderr, with no password of p's URL in it.
// Every git it runs starts from base, as [hostgit.WithBase] has it: qory run gives this
// process's environment, and a pool the environment of its commands.
//
// The target's repository is fetched at at.Version, when at sets a version and at.System
// is [EnvSystem]. The version is the commit at which Qory Apiary checked the target for a
// harness file. A version that is not a full commit id, as [CommitID] checks it, stops
// RunAt with [textNotCommitID] before any fetch. The commit is fetched by its id, at
// depth one, and checked out as a local branch with the name of the remote's default
// branch, with no upstream, so the working folder is at the commit. A remote that does
// not have the commit, or serves no fetch of a commit by its id, stops the fetch with
// [textNoCommit] or [textNoFetchByID]. The further repositories are fetched at the head
// of their default branches. A version of a target on another system, which is not one
// of the run's repositories, is not used. With at.Branch set, the commit of the branch in
// each repository is fetched too, and [Switch] checks it out after the compose; each
// further repository is a read target of the documents, and [WorkRepository.ReadOnly]
// marks it, so [Push] pushes the target's repository alone.
//
// A stop of a run with at.FromCode set has the line of [Target.stop]: its run code is used.
func RunAt(ctx context.Context, dir string, base []string, getenv func(string) string, at Target, p proxygit.Proxy, report io.Writer) (bool, error) {
	fetched, err := fetchRepositories(ctx, dir, base, getenv, at, p, report)
	return fetched, at.stop(err)
}

// FetchCommit makes the commit of the repository path on system an object of the git
// folder of dir, a working folder that [RunAt] fetched: when git finds it already, nothing
// is fetched; otherwise it is fetched by its id, at depth one, from the repository's URL
// through the host proxy p, with the git that the push step runs, see [stepGit]. Before
// any git runs, [safeAfterRun] checks dir, as [Push] does: an agent had its .git open for
// writing. It leaves HEAD, the branches and the working tree where they are. A remote that
// does not have the commit, or serves no fetch by id, stops it with [textNoCommit] or
// [textNoFetchByID].
func FetchCommit(ctx context.Context, dir string, base []string, system, path, commit string, p proxygit.Proxy) error {
	if !CommitID(commit) {
		return fmt.Errorf("%s is not a commit id of 40 or 64 hex digits", integration.Printable(exports.Value(commit, false)))
	}
	ctx = hostgit.WithBase(ctx, base)
	if err := safeAfterRun(ctx, dir); err != nil {
		return checkFailed(textFetchNotSafe, WorkRepository{Path: path}, err)
	}
	g := stepGit(&p, "")
	if _, err := g(ctx, dir, "rev-parse", "--verify", "--quiet", commit+"^{commit}"); err == nil {
		return nil
	}
	if _, err := g(ctx, dir, "fetch", "--quiet", "--depth", "1", repoURL(system, path), "--", commit); err != nil {
		return notAtCommit(path, system, commit, err)
	}
	return nil
}

// RunPrebuild is [RunAt] for the prebuild from a run code, of the target at, at the head
// of its default branch: the prebuild fetches the target's repository alone, and a stop
// has the line of [Target.stop] for a prebuild.
func RunPrebuild(ctx context.Context, dir string, base []string, getenv func(string) string, at Target, p proxygit.Proxy, report io.Writer) (bool, error) {
	at.Version, at.Reads, at.Branch, at.FromCode, at.Prebuild = "", nil, nil, true, true
	return RunAt(ctx, dir, base, getenv, at, p, report)
}

// fetchRepositories is [RunAt] with the lines of a run from no run code.
func fetchRepositories(ctx context.Context, dir string, base []string, getenv func(string) string, at Target, p proxygit.Proxy, report io.Writer) (bool, error) {
	ctx = hostgit.WithBase(ctx, base)
	system := getenv(EnvSystem)
	paths, target, err := runList(getenv, at)
	if err != nil || len(paths) == 0 {
		return false, err
	}
	commit := ""
	if at.Version != "" && target && at.System == system && sameRepository(system == ForgeGitHub, paths[0], at.Path) {
		if !CommitID(at.Version) {
			return false, fmt.Errorf(textNotCommitID, integration.Printable(exports.Value(at.Version, false)))
		}
		commit = strings.ToLower(at.Version)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return false, local(err)
	}
	g := hostRunner(&p)
	// LC_ALL=C keeps the line [initFolder] reads from git in English.
	if err := emptyTree(ctx, hostRunnerWith(&p, "", "LC_ALL=C"), dir, target); err != nil {
		return false, err
	}
	start := time.Now()
	further := paths
	branch := at.Branch
	if commit == "" {
		branch = nil
	}
	working, first, shown := "", "", ""
	if target {
		working = paths[0]
		further = paths[1:]
		first = integration.Printable(exports.Value(paths[0], false))
		defaultName, err := fetchRun(ctx, g, dir, system, paths[0], commit)
		if err != nil {
			return false, err
		}
		head, err := g(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return false, notFetched(paths[0], system, err)
		}
		if branch != nil {
			branch.system = system
			branch.Repositories = []WorkRepository{{Path: paths[0], Dir: ".", Commit: strings.TrimSpace(head), DefaultBranch: defaultName}}
			if err := fetchBranchCommit(ctx, g, dir, system, paths[0], branch); err != nil {
				return false, err
			}
			branch.setBase(ctx, g, report, dir, system, paths[0], strings.TrimSpace(head))
		}
		// The line shows the commit HEAD is at, which is commit when it is set.
		shown = shortCommit(strings.TrimSpace(head))
	}
	if len(further) > 0 {
		if err := keepRepositories(dir, working, at.FromCode); err != nil {
			return false, err
		}
		if err := exclude(ctx, g, dir); err != nil {
			return false, err
		}
		for _, path := range further {
			dest := filepath.Join(dir, filepath.FromSlash(Repositories), filepath.FromSlash(path))
			if _, err := g(ctx, dir, "clone", "--quiet", "--depth", "1", "--", repoURL(system, path), dest); err != nil {
				return false, notFetched(path, system, err)
			}
			if branch != nil {
				if err := fetchBranchCommit(ctx, g, dest, system, path, branch); err != nil {
					return false, err
				}
				head, err := g(ctx, dest, "rev-parse", "HEAD")
				if err != nil {
					return false, notFetched(path, system, err)
				}
				rel := filepath.ToSlash(filepath.Join(filepath.FromSlash(Repositories), filepath.FromSlash(path)))
				// The further repositories of a run from a run code are its read targets.
				branch.Repositories = append(branch.Repositories, WorkRepository{Path: path, Dir: rel, Commit: strings.TrimSpace(head), DefaultBranch: cloneBranch(ctx, g, dest), ReadOnly: at.FromCode})
			}
		}
	}
	took := seconds(time.Since(start))
	switch {
	case !target && len(further) == 1:
		fmt.Fprintf(report, textFetchedReadOne+"\n", took)
	case !target:
		fmt.Fprintf(report, textFetchedReads+"\n", len(further), took)
	case len(further) == 0:
		fmt.Fprintf(report, textFetched+"\n", first, shown, took)
	case len(further) == 1 && at.FromCode:
		fmt.Fprintf(report, textFetchedAndRead+"\n", first, shown, took)
	case at.FromCode:
		fmt.Fprintf(report, textFetchedAndReads+"\n", first, shown, len(further), took)
	case len(further) == 1:
		fmt.Fprintf(report, textFetchedOne+"\n", first, shown, took)
	default:
		fmt.Fprintf(report, textFetchedMany+"\n", first, shown, len(further), took)
	}
	return true, nil
}

// runList is the list that the run fetches, from the host [EnvSystem] names: for a run
// from a run code, at.FromCode, the list of [Target.Repositories]; for any other, the
// list [EnvRepositories] names, as [Check] checks it, whose first entry is the run's
// repository. target reports whether the first entry is the run's repository, which goes
// into the working folder.
func runList(getenv func(string) string, at Target) (paths []string, target bool, err error) {
	system := getenv(EnvSystem)
	if at.FromCode {
		return at.Repositories(system)
	}
	list := getenv(EnvRepositories)
	if list == "" {
		return nil, false, nil
	}
	paths, err = Check(system, list)
	return paths, err == nil, err
}

// Repositories is the list of repositories that the run from a run code with the
// documents' target t fetches from system, the host of the run's repositories: the
// target's repository first, when the target is on system, then the repository of each
// read target in t.Reads, in its order. target reports whether the first is the target's.
// A read target whose repository the list has already, under another collection or as the
// target, is listed once. A list with no target's repository has no working repository:
// each entry is a read target. With system "" the list is empty: the read targets are
// places in another system. A target on another system is not listed. A read target on
// another system, or, on github.com, of another owner than the first entry, stops the
// list, as do a path that is not a repository path, as [Check] has it, and a read target
// inside another. Each of these stops is a line of a dispatched run: the cause alone. A
// system that is not a host name stops a list with an entry, with the fix [textNotHost],
// since the run configuration sets the system. The list has no bound on its bytes, which
// [Check] sets for [EnvRepositories].
func (t Target) Repositories(system string) (paths []string, target bool, err error) {
	if system == "" {
		// A run with no host for its repositories, such as one of a workspace that is not
		// a software factory, has no repository: its read targets are places in another
		// system, and nothing is fetched.
		return nil, false, nil
	}
	github := system == ForgeGitHub
	if t.System == system {
		if t.Path == "" || !repositoryPath(t.Path) {
			return nil, false, fmt.Errorf(textListNotPath, "target", shownPath(t.Path))
		}
		if github && strings.Count(t.Path, "/") != 1 {
			return nil, false, fmt.Errorf(textListNotGitHub, "target", shownPath(t.Path), shownPath(system))
		}
		paths, target = []string{t.Path}, true
	}
	for _, r := range t.Reads {
		switch {
		case r.System != system:
			return nil, false, fmt.Errorf(textReadOtherSystem, shownPath(r.Path), shownPath(r.System), shownPath(system))
		case !repositoryPath(r.Path):
			return nil, false, fmt.Errorf(textListNotPath, "read target", shownPath(r.Path))
		case github && strings.Count(r.Path, "/") != 1:
			return nil, false, fmt.Errorf(textListNotGitHub, "read target", shownPath(r.Path), shownPath(system))
		case slices.ContainsFunc(paths, func(p string) bool { return sameRepository(github, p, r.Path) }):
			continue
		}
		if github && len(paths) > 0 {
			owner, _, _ := strings.Cut(r.Path, "/")
			if first, _, _ := strings.Cut(paths[0], "/"); !strings.EqualFold(owner, first) {
				return nil, false, fmt.Errorf(textReadOtherOwner, shownPath(r.Path), shownPath(owner), shownPath(first), shownPath(system))
			}
		}
		// The target's repository goes into the working folder, not under [Repositories],
		// so a read target is compared with the read targets alone.
		reads := paths
		if target {
			reads = paths[1:]
		}
		for _, q := range reads {
			switch {
			case inside(github, r.Path, q):
				return nil, false, fmt.Errorf(textReadNested, shownPath(r.Path), shownPath(q))
			case inside(github, q, r.Path):
				return nil, false, fmt.Errorf(textReadNested, shownPath(q), shownPath(r.Path))
			}
		}
		paths = append(paths, r.Path)
	}
	if len(paths) > 0 && (!hostName.MatchString(system) || strings.HasSuffix(system, ".")) {
		return nil, false, fmt.Errorf(textNotHost, EnvSystem)
	}
	return paths, target, nil
}

// seconds is d as the lines of the preparation give a time: seconds with one decimal
// and an s, such as 3.4s.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// Check takes the system and the comma-separated list of repositories and returns the
// paths, the run's repository first. It refuses a system that is not a host name, a
// list of more than [maxList] bytes, an entry that is not a path of [segment]s joined by
// slashes, of 1 to [maxPath] characters with no ., .. or .git segment, an entry given
// twice, and an entry inside another, the first such pair in entry order. On github.com
// every entry is owner/name, and of the first entry's owner. A list Check returns is
// one the URLs and the folders are built from.
func Check(system, list string) ([]string, error) {
	return checkList(system, list, maxList, fmt.Sprintf(textTooLong, EnvRepositories, maxList))
}

// checkList is [Check] with limit in place of [maxList], and tooLong the line of a list
// over it.
func checkList(system, list string, limit int, tooLong string) ([]string, error) {
	if system == "" {
		return nil, fmt.Errorf(textNoSystem, EnvRepositories, EnvSystem)
	}
	if !hostName.MatchString(system) || strings.HasSuffix(system, ".") {
		return nil, fmt.Errorf(textNotHost, EnvSystem)
	}
	if len(list) > limit {
		return nil, errors.New(tooLong)
	}
	paths := strings.Split(list, ",")
	github := system == ForgeGitHub
	for i, p := range paths {
		if !repositoryPath(p) {
			return nil, fmt.Errorf(textNotPath, EnvRepositories, i+1)
		}
		if slices.ContainsFunc(paths[:i], func(q string) bool { return sameRepository(github, p, q) }) {
			return nil, fmt.Errorf(textTwice, EnvRepositories, integration.Printable(exports.Value(p, false)))
		}
		if github {
			owner, _, ok := strings.Cut(p, "/")
			if !ok || strings.Count(p, "/") != 1 {
				return nil, fmt.Errorf(textNotGitHub, EnvRepositories, i+1, integration.Printable(exports.Value(system, false)))
			}
			if first, _, _ := strings.Cut(paths[0], "/"); !strings.EqualFold(owner, first) {
				return nil, fmt.Errorf(textOtherOwner, EnvRepositories, i+1, integration.Printable(exports.Value(owner, false)), integration.Printable(exports.Value(first, false)), integration.Printable(exports.Value(system, false)))
			}
		}
		// The run's repository goes into the working folder, not under
		// [Repositories], so only the further entries are compared with each other.
		for j := 1; j < i; j++ {
			q := paths[j]
			inner, outer := i, j
			if inside(github, q, p) {
				inner, outer = j, i
			} else if !inside(github, p, q) {
				continue
			}
			return nil, fmt.Errorf(textNested, EnvRepositories, inner+1, integration.Printable(exports.Value(paths[inner], false)), outer+1, integration.Printable(exports.Value(paths[outer], false)))
		}
	}
	return paths, nil
}

// inside reports whether the path inner is below the path outer, compared segment by
// segment: outer's segments start inner's, and inner has more. On github.com, where
// names are not case-sensitive, the segments compare in any case.
func inside(github bool, inner, outer string) bool {
	in, out := strings.Split(inner, "/"), strings.Split(outer, "/")
	if len(in) <= len(out) {
		return false
	}
	return sameRepository(github, strings.Join(in[:len(out)], "/"), outer)
}

// repositoryPath reports whether p is 1 to [maxPath] characters of [segment]s joined by
// slashes, none of them ., .. or .git in any case: a path that stays where it is
// joined and names no git directory.
func repositoryPath(p string) bool {
	if p == "" || len(p) > maxPath {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." || strings.EqualFold(s, ".git") || !segment.MatchString(s) {
			return false
		}
	}
	return true
}

// sameRepository reports whether two paths name one repository: the same path, or on
// github.com, where names are not case-sensitive, the same path in any case.
func sameRepository(github bool, a, b string) bool {
	if github {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// repoURL is the URL of the repository at path on system: https://<system>/<path>.git,
// from values [Check] passed.
func repoURL(system, path string) string {
	base := "https://" + system
	if baseURL != "" {
		base = baseURL
	}
	return base + "/" + path + ".git"
}

// emptyTree stops on a working folder whose .git is not a folder, a file or a link to a
// repository elsewhere, such as a worktree's or a separate git directory's, whose path is
// the host's; on one with a .git and anything else but .qory; and on one with no .git and
// any entry, which is not a working tree. A folder with no entries gets a repository from
// [initFolder]. Then it stops on one whose repository [safeRepository] stops on; then on
// one that is not the top of its working tree, whose repository has a commit, or has a
// remote. A git that fails to find the top of the working tree is [textTreeFailed]: the cause
// can be in the folder's .git or in one above it, and git can print the same line for both. Any
// later failure of git is [textGitFailed]. With target false, a list of read targets
// alone, the first two stops are [textNotEmptyReads] and [textNotTreeReads].
func emptyTree(ctx context.Context, git gitRunner, dir string, target bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return local(err)
	}
	hasGit, other := false, false
	for _, e := range entries {
		switch e.Name() {
		case ".git":
			if !e.IsDir() {
				return errors.New(textGitNotDir)
			}
			hasGit = true
		case checkout.Dir:
		default:
			other = true
		}
	}
	if other && hasGit {
		if !target {
			return errors.New(textNotEmptyReads)
		}
		return errors.New(textNotEmpty)
	}
	if !hasGit {
		if len(entries) > 0 && !target {
			return errors.New(textNotTreeReads)
		}
		if len(entries) > 0 {
			return errors.New(textNotTree)
		}
		if err := initFolder(ctx, git, dir); err != nil {
			return err
		}
	}
	if err := safeRepository(ctx, dir); err != nil {
		return err
	}
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return lineOf(textTreeFailed, integration.Printable(exports.HideURLError(err).Error()))
	}
	// Only the line end is git's: a folder's name can end in a space.
	if !samePath(strings.TrimSuffix(top, "\n"), dir) {
		return errors.New(textNotTree)
	}
	commits, err := git(ctx, dir, "rev-list", "-n", "1", "--all")
	if err != nil {
		return gitFailed(err)
	}
	if strings.TrimSpace(commits) != "" {
		return errors.New(textNotTree)
	}
	remotes, err := git(ctx, dir, "remote")
	if err != nil {
		return gitFailed(err)
	}
	if strings.TrimSpace(remotes) != "" {
		return errors.New(textHasRemote)
	}
	return nil
}

// branchName reports whether branch is a branch name: one that does not start with a
// dash and that git check-ref-format takes after refs/heads/, by git in dir. The error is
// git's when check-ref-format fails in another way than its exit 1, a stop among them.
func branchName(ctx context.Context, git gitRunner, dir, branch string) (bool, error) {
	if strings.HasPrefix(branch, "-") {
		return false, nil
	}
	if _, err := git(ctx, dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		// check-ref-format exits 1 for a name that is no valid ref name; any other
		// failure, a stop among them, is git's.
		var ge *gitError
		if errors.As(err, &ge) && ge.exit == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// BranchName is [branchName] for a caller outside the preparation, with the git of the
// push step, which reaches no remote.
func BranchName(ctx context.Context, dir, branch string) (bool, error) {
	return branchName(ctx, stepGit(noRemote, ""), dir, branch)
}

// initFolder runs git init in dir, a folder with no entries, with no template, so the
// repository has only what git init writes without one. It stops with [textDirLink] on a
// dir that is a link, without following it, and with [textNotTree] on a dir git finds a
// repository for, such as a folder inside a working tree, where git would use the second
// repository in place of the first. Any other failure of git as it looks for a repository
// is [textAboveFailed]. The fetch replaces the unborn branch main with the remote's default
// branch, so its name has no effect on the run; the option keeps it from depending on git's
// version.
func initFolder(ctx context.Context, git gitRunner, dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return local(err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return errors.New(textDirLink)
	}
	_, err = git(ctx, dir, "rev-parse", "--git-dir")
	if err == nil {
		return errors.New(textNotTree)
	}
	var ge *gitError
	if !errors.As(err, &ge) || !strings.Contains(ge.line, "not a git repository (or any") {
		return lineOf(textAboveFailed, integration.Printable(exports.HideURLError(err).Error()))
	}
	if _, err := git(ctx, dir, "init", "--quiet", "--template=", "--initial-branch=main"); err != nil {
		return local(err)
	}
	return nil
}

// cloneBranch is the name of the branch that the clone at dir has checked out, the remote's
// default branch, or "" when git names none or the name is not a branch name: an output
// has it as pushed-base-branch, and a run does not stop for it.
func cloneBranch(ctx context.Context, git gitRunner, dir string) string {
	out, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(out)
	if ok, err := branchName(ctx, git, dir, name); err != nil || !ok || name == "" {
		return ""
	}
	return name
}

// fetchRun fetches the run's repository into dir with git: origin is its URL, and the
// remote's default branch is fetched at depth one and checked out as the local branch of
// its name, tracking origin. With commit set, the commit is fetched by its id at depth
// one in place of the branch, into no ref but FETCH_HEAD, and checked out as the local
// branch of the default branch's name with no upstream; no remote-tracking ref is
// written, since the remote's branch may be at another commit. It returns the default
// branch's name.
func fetchRun(ctx context.Context, git gitRunner, dir, system, path, commit string) (string, error) {
	if _, err := git(ctx, dir, "remote", "add", "--", "origin", repoURL(system, path)); err != nil {
		return "", notFetched(path, system, err)
	}
	out, err := git(ctx, dir, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", notFetched(path, system, err)
	}
	branch := defaultBranch(out)
	if branch == "" {
		// No HEAD to follow: a remote with no refs at all has no commits; one with refs
		// names no default branch.
		refs, err := git(ctx, dir, "ls-remote", "origin")
		if err != nil {
			return "", notFetched(path, system, err)
		}
		if strings.TrimSpace(refs) == "" {
			return "", notFetched(path, system, errors.New(textNoCommits))
		}
		return "", notFetched(path, system, errors.New(textNoDefault))
	}
	// The branch is the remote's word, so it is checked as a branch name, which never
	// starts with a dash, and it follows "--" where git takes it as a refspec.
	notBranch := fmt.Errorf(textNotBranch, integration.Printable(exports.Value(branch, false)))
	if ok, err := branchName(ctx, git, dir, branch); err != nil {
		return "", notFetched(path, system, err)
	} else if !ok {
		return "", notFetched(path, system, notBranch)
	}
	if commit != "" {
		if _, err := git(ctx, dir, "fetch", "--quiet", "--depth", "1", "origin", "--", commit); err != nil {
			return "", notAtCommit(path, system, commit, err)
		}
		if _, err := git(ctx, dir, "checkout", "--quiet", "-b", branch, commit); err != nil {
			return "", notFetched(path, system, err)
		}
		return branch, nil
	}
	refspec := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
	if _, err := git(ctx, dir, "fetch", "--quiet", "--depth", "1", "origin", "--", refspec); err != nil {
		return "", notFetched(path, system, err)
	}
	if _, err := git(ctx, dir, "checkout", "--quiet", "-b", branch, "--track", "origin/"+branch); err != nil {
		return "", notFetched(path, system, err)
	}
	return branch, nil
}

// defaultBranch is the branch HEAD points to in the output of git ls-remote --symref
// origin HEAD, "" when it names none.
func defaultBranch(out string) string {
	for _, line := range strings.Split(out, "\n") {
		ref, ok := strings.CutPrefix(line, "ref: refs/heads/")
		if !ok {
			continue
		}
		if branch, ok := strings.CutSuffix(ref, "\tHEAD"); ok {
			return branch
		}
	}
	return ""
}

// keepRepositories stops on a run repository, path, that has a .qory that is not a
// directory, a file or a link a clone would follow, and on one that has anything at
// [Repositories], tracked or not. With path "", a list with no target's repository, see
// [Target.Repositories], it stops on the same in the working folder. For a run from a run
// code, code, the stops are [textQoryReads] and [textKeptReads], the cause alone.
func keepRepositories(dir, path string, code bool) error {
	shown := integration.Printable(exports.Value(path, false))
	if fi, err := os.Lstat(filepath.Join(dir, checkout.Dir)); err == nil && !fi.IsDir() {
		if code {
			return errors.New(textQoryReads)
		}
		return fmt.Errorf(textQoryNotDir, shown)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return local(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(Repositories))); err == nil {
		if code {
			return errors.New(textKeptReads)
		}
		return fmt.Errorf(textKept, shown)
	} else if !errors.Is(err, os.ErrNotExist) {
		return local(err)
	}
	return nil
}

// exclude adds [ExcludeLine] to the exclude file of the working tree at dir, the one
// git rev-parse --git-path info/exclude names, as [checkout.ExcludeFile] finds it but
// with the environment of every git here, unless the file has that line already. The
// file is read and written through an [os.Root] of dir, and refused when it is outside
// dir or when it, or a folder on the way to it, is a link: no link is followed.
func exclude(ctx context.Context, git gitRunner, dir string) error {
	out, err := git(ctx, dir, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return gitFailed(err)
	}
	file := filepath.Clean(strings.TrimSpace(out))
	if filepath.IsAbs(file) {
		if file, err = filepath.Rel(dir, file); err != nil {
			return local(err)
		}
	}
	shown := filepath.ToSlash(file)
	if !filepath.IsLocal(file) {
		return fmt.Errorf(textExcludeOutside, integration.Printable(shown))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return local(err)
	}
	defer root.Close()
	segments := strings.Split(shown, "/")
	for i := range segments {
		at := filepath.Join(segments[:i+1]...)
		fi, err := root.Lstat(at)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return local(err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf(textExcludeLink, integration.Printable(filepath.ToSlash(at)))
		}
	}
	data, err := root.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return local(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == ExcludeLine {
			return nil
		}
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}
	data = append(data, ExcludeLine+"\n"...)
	if err := root.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return local(err)
	}
	if err := root.WriteFile(file, data, 0o644); err != nil {
		return local(err)
	}
	return nil
}

// gitError is a git that failed: its first line on stderr, or what stopped it, the status
// git exited with, or -1 when git did not exit by itself, and every line it wrote on
// stderr, which a push needs: its first line is the remote's address.
type gitError struct {
	line   string
	exit   int
	output string
}

func (e *gitError) Error() string { return e.line }

// hardened are the options every git here runs with, over the repository's
// configuration, as [proxygit.Hardened] has them. A hook a configuration defines by
// hook.<name>.command is not turned off here: [gitEnv] keeps every configuration but
// the repository's away from git.
//
// Inside the wall, the git program is the image's, found on PATH. In an image built from
// the repository, the preparation is only as trustworthy as that image: the repository's
// code, the build that ran it among it, is inside the agent's trust boundary, and the
// options here keep a repository's configuration from running a program, not a
// program the image holds. On this machine, git is the one [hostgit.Unchecked] finds.
var hardened = proxygit.Hardened

// protocols are the options that let a git on this machine reach a remote, over https
// alone: [proxygit.HTTPSOnly]. Only a test that fetches from local repositories sets it
// otherwise.
var protocols = proxygit.HTTPSOnly

// gitRunner runs one git in dir with args and returns what it printed on stdout, and on
// a failure a [*gitError]: [git] inside the wall, and [hostRunner]'s on this machine.
type gitRunner func(ctx context.Context, dir string, args ...string) (string, error)

// git runs git in dir with args, inside the wall: the image's git from PATH, with the
// [hardened] options and the environment [gitEnv] makes, as [runGit] runs it.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	return runGit(ctx, nil, func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "git", append(slices.Clone(hardened), args...)...)
		cmd.Dir = dir
		cmd.Env = gitEnv(os.Environ())
		return cmd, nil
	}, args[0])
}

// CloneError is a [Clone] that failed: the status git exited with, -1 when git did not
// exit by itself, what git wrote on stderr, which contains the repository's address,
// Stopped, true when the limit of the call or the caller stopped git, and NoGit, true when
// PATH has no git, so git did not start.
type CloneError struct {
	Exit    int
	Output  string
	Stopped bool
	NoGit   bool
}

func (e *CloneError) Error() string { return "git clone failed" }

// Clone clones the repository at path on system, values [Check] passed, at depth one into
// dest, a folder that does not exist, inside the wall: the image's git from PATH, over
// https alone, with the [hardened] options and the environment [gitEnv] makes, so the
// proxy and the certificate authority that the wall gives stay as given, and with
// LC_ALL=C, which keeps git's lines in English. A failure is a [*CloneError].
func Clone(ctx context.Context, system, path, dest string) error {
	var noGit bool
	_, err := runGit(ctx, nil, func(ctx context.Context) (*exec.Cmd, error) {
		args := slices.Concat(hardened, protocols, []string{"clone", "--quiet", "--depth", "1", "--", repoURL(system, path), dest})
		cmd := exec.CommandContext(ctx, "git", args...)
		noGit = errors.Is(cmd.Err, exec.ErrNotFound)
		cmd.Dir = filepath.Dir(dest)
		cmd.Env = append(gitEnv(os.Environ()), "LC_ALL=C")
		return cmd, nil
	}, "clone")
	var g *gitError
	if !errors.As(err, &g) {
		return err
	}
	stopped := g.exit < 0 && g.output == "" && strings.HasPrefix(g.line, "git clone ")
	return &CloneError{Exit: g.exit, Output: g.output, Stopped: stopped, NoGit: noGit}
}

// hostRunner is the [gitRunner] of a fetch on this machine through the run's host proxy
// p: the git [hostgit.Unchecked] finds in the environment [hostgit.Base] of ctx, with
// its options, the [hardened] ones and [protocols], and the environment [proxygit.Env]
// makes for p, which sets the proxy and the CA bundle and reads no configuration but
// the repository's. With [hasRemote] of p, the subcommands of [remoteCommands] run through
// [hostgit.Remote]; every other git has no protocol and no proxy. No error's text
// contains the password of p's URL.
func hostRunner(p *proxygit.Proxy) gitRunner { return hostRunnerWith(p, "") }

// hostRunnerWith is [hostRunner] with input on git's stdin and env added to its
// environment.
func hostRunnerWith(p *proxygit.Proxy, input string, env ...string) gitRunner {
	return hostRunnerOf(p, hasRemote(p), nil, input, env)
}

// hasRemote reports whether a git with p may reach a remote: p is set and is not [noRemote].
func hasRemote(p *proxygit.Proxy) bool { return p != nil && p != noRemote }

// remoteCommands are the git subcommands that reach a remote.
var remoteCommands = []string{"clone", "fetch", "ls-remote", "push"}

// hostRunnerOf is [hostRunnerWith] with options added to git's options, and with remote for
// whether the subcommands of [remoteCommands] run through [hostgit.Remote].
func hostRunnerOf(p *proxygit.Proxy, remote bool, options []string, input string, env []string) gitRunner {
	return func(ctx context.Context, dir string, args ...string) (string, error) {
		return runGit(ctx, p, func(ctx context.Context) (*exec.Cmd, error) {
			cmd, err := hostCommand(ctx, p, remote && slices.Contains(remoteCommands, args[0]), dir, options, env, args)
			if err != nil {
				return nil, err
			}
			cmd.Stdin = strings.NewReader(input)
			return cmd, nil
		}, args[0])
	}
}

// hostCommand is the git of [hostRunnerOf], made by [hostgit.Remote] with remote set. A
// git without remote has no protocol, so it gets the environment of [noRemote] in place
// of p's: the password of p's URL is only in the environment of a git that can use it.
func hostCommand(ctx context.Context, p *proxygit.Proxy, remote bool, dir string, options, env, args []string) (*exec.Cmd, error) {
	command := hostgit.Unchecked
	if remote {
		command = hostgit.Remote
	} else if hasRemote(p) {
		p = noRemote
	}
	cmd, err := command(ctx, dir, hostgit.Base(ctx), slices.Concat(hardened, protocols, options, args)...)
	if err != nil {
		return nil, err
	}
	cmd.Env = append(proxygit.Env(cmd.Env, p), env...)
	return cmd, nil
}

// runGit runs the git make makes, subcommand its first argument, for at most [limitOf]
// subcommand within ctx. git runs in a new process group, and the whole group is
// stopped when the call ends, so a helper git started, such as git-remote-https, never
// holds the call open. It returns what git printed on stdout, and on a failure a
// [*gitError] whose line has the password of p's URL hidden.
func runGit(ctx context.Context, p *proxygit.Proxy, make func(context.Context) (*exec.Cmd, error), subcommand string) (string, error) {
	limit := limitOf(subcommand)
	call, cancel := context.WithTimeoutCause(ctx, limit, errLimit)
	defer cancel()
	cmd, err := make(call)
	if err != nil {
		return "", &gitError{line: proxygit.Hide(err.Error(), p), exit: -1}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A process left holding the output is not waited for past this.
	cmd.WaitDelay = time.Second
	hostgit.Group(cmd)
	err = cmd.Run()
	hostgit.StopGroup(cmd)
	if err != nil {
		switch {
		case errors.Is(context.Cause(call), errLimit):
			return "", &gitError{line: fmt.Sprintf(textStopped, subcommand, limit), exit: -1}
		case ctx.Err() != nil:
			return "", &gitError{line: fmt.Sprintf(textCut, subcommand), exit: -1}
		}
		exit := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		}
		output := proxygit.Hide(stderr.String(), p)
		if line := firstLine(output); line != "" {
			return "", &gitError{line, exit, output}
		}
		return "", &gitError{line: proxygit.Hide(err.Error(), p), exit: exit}
	}
	return stdout.String(), nil
}

// gitEnv is [proxygit.Env] for a git inside the wall: env without the variables that
// point git at another repository or add configuration, with git's prompts off and no
// global or system configuration. The proxy and the CA bundle it names, GIT_SSL_CAINFO,
// stay as given: the wall gives git its proxy and its certificate authority that way.
func gitEnv(env []string) []string { return proxygit.Env(env, nil) }

// firstLine is the first line of s with text in it, trimmed.
func firstLine(s string) string {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			return line
		}
	}
	return ""
}

// notFetched is the line for a fetch of path on system that failed with err, with git's
// line made printable: path and system as [exports.Value] prints them, and git's line
// with a URL's password and each word with an access key secret or a run key hidden, as
// [exports.HideURLError] hides them. The line's fix is by what failed: a credential
// ([textNotFetched]) for a host that answered that it needs one or has no such
// repository, a default branch with a commit ([textNoBranch]) for a remote that has none
// qory fetches, and the run's policy and the host ([textNotReached]) for any other
// failure of git, such as a host the gateway does not reach or a git stopped at its
// limit.
func notFetched(path, system string, err error) error {
	shown := integration.Printable(exports.Value(path, false))
	host := integration.Printable(exports.Value(system, false))
	cause := integration.Printable(exports.HideURLError(err).Error())
	var ge *gitError
	switch {
	case !errors.As(err, &ge):
		return lineOf(textNoBranch, shown, host, cause)
	case answered.MatchString(ge.line):
		return lineOf(textNotFetched, host, shown, shown, cause)
	}
	return lineOf(textNotReached, host, shown, cause)
}

// notAtCommit is the line for a fetch of commit of path on system that failed with err:
// [textNoCommit] when git's line is that the remote does not have the commit,
// [textNoFetchByID] when it is that the remote serves no fetch of an object it did not
// list, which a server that has no setting such as uploadpack.allowReachableSHA1InWant
// returns over git's protocol version 0 or 1 for any commit, and [notFetched] for any
// other failure. The commit shows as its first twelve characters.
func notAtCommit(path, system, commit string, err error) error {
	var ge *gitError
	if !errors.As(err, &ge) {
		return notFetched(path, system, err)
	}
	text := textNoCommit
	switch {
	case notOurRef.MatchString(ge.line):
	case unadvertised.MatchString(ge.line):
		text = textNoFetchByID
	default:
		return notFetched(path, system, err)
	}
	shown := integration.Printable(exports.Value(path, false))
	host := integration.Printable(exports.Value(system, false))
	cause := integration.Printable(exports.HideURLError(err).Error())
	return fmt.Errorf(text, commit[:12], shown, host, cause)
}

// notOurRef and unadvertised match git's lines for a fetch of a commit by its id: a
// remote that does not have the commit, and one that serves no fetch of a commit it did
// not list.
var (
	notOurRef    = regexp.MustCompile(`(?i)not our ref`)
	unadvertised = regexp.MustCompile(`(?i)does not allow request for unadvertised object`)
)

// CommitID reports whether v is a full commit id: forty hex characters for SHA-1, or
// sixty-four for SHA-256, in either case.
func CommitID(v string) bool { return commitID.MatchString(v) }

var commitID = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// answered matches git's line for a host that answered a fetch: that it needs a
// credential, that the credential it got does not read the repository, or that it has
// no such repository, which a host also answers for a private one without a credential,
// and git's line for a path with no repository, as a test's local remote has it.
// A proxy's answer, such as "Received HTTP code 403 from proxy after CONNECT", is not
// one: the run's policy decides it.
var answered = regexp.MustCompile(`(?i)authentication failed|could not read (username|password)|repository .*not found|does not appear to be a git repository|returned error: (401|403|404)|invalid username or password|permission denied`)

// gitFailed is [textGitFailed] with git's line, or what kept git from running, made
// printable.
func gitFailed(err error) error {
	shown := integration.Printable(exports.HideURLError(err).Error())
	return &unsafeError{lineOf(textGitFailed, shown), fmt.Sprintf(causeGitFailed, shown)}
}

// local is [textLocal]: a failure on the machine, not of a fetch.
func local(err error) error {
	shown := integration.Printable(exports.HideError(err).Error())
	return &unsafeError{lineOf(textLocal, shown), fmt.Sprintf(causeLocal, shown)}
}

// The words of a failure of qory run prepare inside the wall that is not of a fetch:
// git that could not read the working folder, and a failure on the machine.
const (
	textPrepareGitFailed = "qory run prepare: git could not read the working folder: %s"
	textPrepareLocal     = "qory run prepare: %s"
)

// gitFailedIn is [gitFailed] for qory run prepare inside the wall.
func gitFailedIn(err error) error {
	return fmt.Errorf(textPrepareGitFailed, integration.Printable(exports.HideURLError(err).Error()))
}

// localIn is [local] for qory run prepare inside the wall.
func localIn(err error) error {
	return fmt.Errorf(textPrepareLocal, integration.Printable(exports.HideError(err).Error()))
}

// samePath reports whether a and b name one directory, once their links are resolved.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// SameRepository reports whether the paths a and b on system are one repository, by the
// rule of [sameRepository].
func SameRepository(system, a, b string) bool {
	return sameRepository(system == ForgeGitHub, a, b)
}
