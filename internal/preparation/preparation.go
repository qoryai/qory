// Package preparation fetches the code of a run into its working folder, and prepares
// the working folder inside the wall before the agent starts. Two variables say where
// the code is: [EnvSystem], the host, and [EnvRepositories], the repositories, the run's
// repository first.
//
// [Run] fetches on this machine, as qory run does once the run is open: the run's
// repository into the working folder, an empty git working tree, and each further one
// under .qory/repositories, every request through the run's host proxy, which adds what
// a host needs, so no token is on the machine or in git's configuration. Every fetch is
// at depth one, over https alone, with no credential helper and no prompt, as package
// [proxygit] sets git up.
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

	"github.com/qoryai/qory/internal/checkout"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/hostgit"
	"github.com/qoryai/qory/internal/integration"
	"github.com/qoryai/qory/internal/places"
	"github.com/qoryai/qory/internal/proxygit"
)

// The variables that say where a run's code is. Their names are each in one constant,
// so a change of name is a change here alone.
const (
	// EnvSystem names the host the repositories are on, such as github.com.
	EnvSystem = "QORY_RUN_SYSTEM"
	// EnvRepositories names the repositories, comma-separated with no space, the run's
	// repository first.
	EnvRepositories = "QORY_RUN_REPOSITORIES"
)

// ForgeGitHub is the one forge whose repository form qory knows: github.com.
const ForgeGitHub = "github.com"

// The words of the fetch qory run does. Each is one format string.
const (
	textNoSystem       = "qory run: set %[2]s beside %[1]s in the run configuration: %[2]s is the host of the repositories %[1]s lists, and it is not set"
	textNotHost        = "qory run: set %s to a host name, such as github.com"
	textNotPath        = "qory run: make entry %[2]d of %[1]s a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"
	textNested         = "qory run: remove entry %[2]d, %[3]s, or entry %[4]d, %[5]s, from %[1]s: the first is inside the second, and a repository is not cloned inside another"
	textTwice          = "qory run: list %[2]s once in %[1]s, which lists it twice"
	textTooLong        = "qory run: shorten %s to 256 bytes or fewer"
	textNotEmpty       = "qory run: start the run in an empty git working tree: the working folder contains files already, and the run's repository is fetched only into an empty one"
	textNotFetched     = "qory run: add a credential for %s that reads %s to the run configuration, then run again. The fetch of %s through the run's gateway failed: %s"
	textNotReached     = "qory run: check that the run's policy allows %[1]s and that %[1]s is reachable, then run again. The fetch of %[2]s through the run's gateway failed: %[3]s"
	textNoBranch       = "qory run: give %s on %s a default branch with a commit, then run again. The fetch through the run's gateway failed: %s"
	textKept           = "qory run: remove .qory/repositories from the working folder of %s: qory clones the further repositories there, and nothing is cloned over it"
	textQoryNotDir     = "qory run: remove .qory from the working folder of %s: it is a file or a link, where qory clones the further repositories into a folder, and nothing is cloned over it"
	textOtherOwner     = "qory run: list in %[1]s only repositories of owner %[4]s: entry %[2]d is of owner %[3]s, and on github.com the repositories of a run are of one owner"
	textNotGitHub      = "qory run: write entry %[2]d of %[1]s as owner/name, the form of a repository on github.com"
	textNotTree        = "qory run: start the run in a git working tree without commits: the working folder is not one, and the run's repository is fetched only into an empty git working tree"
	textExcludeLink    = "qory run: remove the link %s from the working folder: qory writes the exclude file of the repository only where no link leads"
	textExcludeOutside = "qory run: start the run in a git working tree whose exclude file is inside the working folder: the exclude file, %s, is outside it, and qory writes it only inside"
	textGitNotDir      = "qory run: start the run in a git working tree whose .git is a folder: the working folder's .git is a file or a link, which points at a repository elsewhere"
	textHasRemote      = "qory run: start the run in a git working tree with no remote: the working folder's repository has a remote already, and the run's repository is fetched only into an empty git working tree"
	textGitFailed      = "qory run: check that git on this machine can read the working folder, then run again. git failed: %s"
	textLocal          = "qory run: check that this machine can read and write the working folder, then run again. The step failed: %s"
	textPlaceRefused   = "qory run: start the run in a new empty git working tree that no walled run used, then run again. The check of the working folder's git configuration failed: %s"
	textConfigKey      = "qory run: start the run in a new empty git working tree, or remove %s from %s, then run again. qory fetches only into a repository whose configuration has only the keys git init writes, since git on this machine would use any other key, such as a filter, a proxy or http.sslVerify"
	textConfigNotFile  = "qory run: start the run in a new empty git working tree, then run again: the working folder's %s is not a regular file, and qory fetches only into a repository whose configuration it can read"
	textAlternates     = "qory run: start the run in a new empty git working tree, then run again: the working folder's .git has %s, which makes git read objects from outside the working folder"
	textGitLink        = "qory run: start the run in a new empty git working tree, or remove the link %s, then run again: git would read and write through that link"
	textHardLink       = "qory run: start the run in a new empty git working tree, or remove the hard link %s, then run again: a write to it would change another file"
	textCommonDir      = "qory run: start the run in a new empty git working tree, then run again: the working folder's .git has a commondir file, which makes git read the configuration of another repository"
	textFetched        = "qory run: fetched %s at %s in %s"
	textFetchedOne     = "qory run: fetched %s at %s and 1 further repository in %s"
	textFetchedMany    = "qory run: fetched %s at %s and %d further repositories in %s"
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
	// maxList is the most bytes [EnvRepositories] may hold.
	maxList = 256
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

// excludeLine keeps [Repositories] out of the run repository's git status.
const excludeLine = "/" + Repositories + "/"

var (
	hostName = regexp.MustCompile(`^[a-z0-9.-]{1,253}$`)
	segment  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// baseURL replaces https://<system> in front of each repository's path when a test sets
// it, to point the fetches at local repositories. Nothing but a test sets it: the
// production URL is built from the validated system and paths alone, and its scheme is
// always https.
var baseURL = ""

// Run fetches the repositories getenv names into dir, the working folder, on this
// machine through the run's host proxy p, and reports whether it fetched them. With
// [EnvRepositories] unset or empty it does nothing and returns false. The list is
// checked whole before any fetch. Once every repository is fetched, it prints on report
// the line that says what it fetched, the run's repository's commit, and in how long.
// Every error's text is one line, ready for stderr, with no password of p's URL in it.
// Every git it runs starts from base, as [hostgit.WithBase] has it: qory run gives this
// process's environment, and a pool the environment of its commands.
func Run(ctx context.Context, dir string, base []string, getenv func(string) string, p proxygit.Proxy, report io.Writer) (bool, error) {
	ctx = hostgit.WithBase(ctx, base)
	list := getenv(EnvRepositories)
	if list == "" {
		return false, nil
	}
	system := getenv(EnvSystem)
	paths, err := Check(system, list)
	if err != nil {
		return false, err
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return false, local(err)
	}
	g := hostRunner(&p)
	hold, err := emptyTree(ctx, g, dir)
	if err != nil {
		return false, err
	}
	// The working folder stays held until the fetches and the checkout end: a walled run
	// that starts there waits, so it cannot change what [safeRepository] checked.
	defer hold.Release()
	start := time.Now()
	first := integration.Printable(exports.Value(paths[0], false))
	if err := fetchRun(ctx, g, dir, system, paths[0]); err != nil {
		return false, err
	}
	head, err := g(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return false, notFetched(paths[0], system, err)
	}
	commit := strings.TrimSpace(head)
	if len(commit) > 12 {
		commit = commit[:12]
	}
	further := paths[1:]
	if len(further) > 0 {
		if err := keepRepositories(dir, paths[0]); err != nil {
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
		}
	}
	took := seconds(time.Since(start))
	switch len(further) {
	case 0:
		fmt.Fprintf(report, textFetched+"\n", first, commit, took)
	case 1:
		fmt.Fprintf(report, textFetchedOne+"\n", first, commit, took)
	default:
		fmt.Fprintf(report, textFetchedMany+"\n", first, commit, len(further), took)
	}
	return true, nil
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
	if system == "" {
		return nil, fmt.Errorf(textNoSystem, EnvRepositories, EnvSystem)
	}
	if !hostName.MatchString(system) || strings.HasSuffix(system, ".") {
		return nil, fmt.Errorf(textNotHost, EnvSystem)
	}
	if len(list) > maxList {
		return nil, fmt.Errorf(textTooLong, EnvRepositories)
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
				return nil, fmt.Errorf(textNotGitHub, EnvRepositories, i+1)
			}
			if first, _, _ := strings.Cut(paths[0], "/"); !strings.EqualFold(owner, first) {
				return nil, fmt.Errorf(textOtherOwner, EnvRepositories, i+1, integration.Printable(exports.Value(owner, false)), integration.Printable(exports.Value(first, false)))
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

// emptyTree stops on a working folder that holds anything but .git and .qory; then on
// one whose .git is not a folder, a file or a link to a repository elsewhere, such as a
// worktree's or a separate git directory's, whose path is the host's; then on one with
// no .git, which is outside a working tree or nested in one; then on one whose
// repository [safeRepository] stops on; then on one that is not the top of its working
// tree, whose repository has a commit, or has a remote. Otherwise it returns the hold
// [safeRepository] took on the folder, which its caller releases once the fetch ends. A git
// that fails to read the folder is [textGitFailed], not a refusal of the folder.
func emptyTree(ctx context.Context, git gitRunner, dir string) (hold *places.Hold, err error) {
	defer func() {
		if err != nil {
			hold.Release()
			hold = nil
		}
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return hold, local(err)
	}
	hasGit := false
	for _, e := range entries {
		switch e.Name() {
		case ".git":
			if !e.IsDir() {
				return hold, errors.New(textGitNotDir)
			}
			hasGit = true
		case checkout.Dir:
		default:
			return hold, errors.New(textNotEmpty)
		}
	}
	if !hasGit {
		return hold, errors.New(textNotTree)
	}
	if hold, err = safeRepository(ctx, dir); err != nil {
		return nil, err
	}
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return hold, gitFailed(err)
	}
	if !samePath(strings.TrimSpace(top), dir) {
		return hold, errors.New(textNotTree)
	}
	commits, err := git(ctx, dir, "rev-list", "-n", "1", "--all")
	if err != nil {
		return hold, gitFailed(err)
	}
	if strings.TrimSpace(commits) != "" {
		return hold, errors.New(textNotTree)
	}
	remotes, err := git(ctx, dir, "remote")
	if err != nil {
		return hold, gitFailed(err)
	}
	if strings.TrimSpace(remotes) != "" {
		return hold, errors.New(textHasRemote)
	}
	return hold, nil
}

// fetchRun fetches the run's repository into dir with git: origin is its URL, and the
// remote's default branch is fetched at depth one and checked out as the local branch of
// its name, tracking origin.
func fetchRun(ctx context.Context, git gitRunner, dir, system, path string) error {
	if _, err := git(ctx, dir, "remote", "add", "--", "origin", repoURL(system, path)); err != nil {
		return notFetched(path, system, err)
	}
	out, err := git(ctx, dir, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return notFetched(path, system, err)
	}
	branch := defaultBranch(out)
	if branch == "" {
		// No HEAD to follow: a remote with no refs at all has no commits; one with refs
		// names no default branch.
		refs, err := git(ctx, dir, "ls-remote", "origin")
		if err != nil {
			return notFetched(path, system, err)
		}
		if strings.TrimSpace(refs) == "" {
			return notFetched(path, system, errors.New(textNoCommits))
		}
		return notFetched(path, system, errors.New(textNoDefault))
	}
	// The branch is the remote's word, so it is checked as a branch name, which never
	// starts with a dash, and it follows "--" where git takes it as a refspec.
	notBranch := fmt.Errorf(textNotBranch, integration.Printable(exports.Value(branch, false)))
	if strings.HasPrefix(branch, "-") {
		return notFetched(path, system, notBranch)
	}
	if _, err := git(ctx, dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		// check-ref-format exits 1 for a name it refuses; any other failure, a stop
		// among them, is git's.
		var ge *gitError
		if errors.As(err, &ge) && ge.exit == 1 {
			return notFetched(path, system, notBranch)
		}
		return notFetched(path, system, err)
	}
	refspec := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
	if _, err := git(ctx, dir, "fetch", "--quiet", "--depth", "1", "origin", "--", refspec); err != nil {
		return notFetched(path, system, err)
	}
	if _, err := git(ctx, dir, "checkout", "--quiet", "-b", branch, "--track", "origin/"+branch); err != nil {
		return notFetched(path, system, err)
	}
	return nil
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

// keepRepositories refuses a run repository that has a .qory that is not a directory,
// a file or a link a clone would follow, and one that has anything at [Repositories],
// tracked or not.
func keepRepositories(dir, path string) error {
	if fi, err := os.Lstat(filepath.Join(dir, checkout.Dir)); err == nil && !fi.IsDir() {
		return fmt.Errorf(textQoryNotDir, integration.Printable(exports.Value(path, false)))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return local(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(Repositories))); err == nil {
		return fmt.Errorf(textKept, integration.Printable(exports.Value(path, false)))
	} else if !errors.Is(err, os.ErrNotExist) {
		return local(err)
	}
	return nil
}

// exclude adds [excludeLine] to the exclude file of the working tree at dir, the one
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
		if strings.TrimSpace(line) == excludeLine {
			return nil
		}
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}
	data = append(data, excludeLine+"\n"...)
	if err := root.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return local(err)
	}
	if err := root.WriteFile(file, data, 0o644); err != nil {
		return local(err)
	}
	return nil
}

// gitError is a git that failed: its first line on stderr, or what stopped it, and
// the status git exited with, or -1 when git did not exit by itself.
type gitError struct {
	line string
	exit int
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

// hostRunner is the [gitRunner] of a fetch on this machine through the run's host proxy
// p: the git [hostgit.Unchecked] finds in the environment [hostgit.Base] of ctx, with
// its options, the [hardened] ones and [protocols], and the environment [proxygit.Env]
// makes for p, which sets the proxy and the CA bundle and reads no configuration but
// the repository's. No error's text
// contains the password of p's URL.
func hostRunner(p *proxygit.Proxy) gitRunner {
	return func(ctx context.Context, dir string, args ...string) (string, error) {
		return runGit(ctx, p, func(ctx context.Context) (*exec.Cmd, error) {
			full := append(append(slices.Clone(hardened), protocols...), args...)
			cmd, err := hostgit.Unchecked(ctx, dir, hostgit.Base(ctx), full...)
			if err != nil {
				return nil, err
			}
			cmd.Env = proxygit.Env(cmd.Env, p)
			return cmd, nil
		}, args[0])
	}
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
		return "", &gitError{proxygit.Hide(err.Error(), p), -1}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A process left holding the output is not waited for past this.
	cmd.WaitDelay = time.Second
	group(cmd)
	err = cmd.Run()
	stopGroup(cmd)
	if err != nil {
		switch {
		case errors.Is(context.Cause(call), errLimit):
			return "", &gitError{fmt.Sprintf(textStopped, subcommand, limit), -1}
		case ctx.Err() != nil:
			return "", &gitError{fmt.Sprintf(textCut, subcommand), -1}
		}
		exit := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		}
		if line := firstLine(stderr.String()); line != "" {
			return "", &gitError{proxygit.Hide(line, p), exit}
		}
		return "", &gitError{proxygit.Hide(err.Error(), p), exit}
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
		return fmt.Errorf(textNoBranch, shown, host, cause)
	case answered.MatchString(ge.line):
		return fmt.Errorf(textNotFetched, host, shown, shown, cause)
	}
	return fmt.Errorf(textNotReached, host, shown, cause)
}

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
	return fmt.Errorf(textGitFailed, integration.Printable(exports.HideURLError(err).Error()))
}

// local is [textLocal]: a failure on the machine, not of a fetch.
func local(err error) error {
	return fmt.Errorf(textLocal, integration.Printable(exports.HideError(err).Error()))
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
