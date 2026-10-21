// Package preparation fetches the code of a run into its working folder and runs its
// start commands, the preparation qory run prepare does inside the wall before the
// agent starts. Two variables say where the code is: [EnvSystem], the host, and
// [EnvRepositories], the repositories, the run's repository first. That one is fetched
// into the working folder, an empty git working tree, starting from the image's copy
// at [Seed] when a prebuilt image has one, and each further one is cloned under
// .qory/repositories. [Start] then runs the start commands of the repository's
// development container configuration.
//
// Every fetch runs git from PATH, at depth one, with no credential helper and no prompt:
// the run's proxy adds what a host needs, so no token is on the machine or in git's
// configuration. Every git the preparation runs has hooks off, through
// core.hooksPath, and the file system monitor off; it reads the configuration of the
// working folder's repository alone, none of the user's, the system's or the
// environment's, and no replace object.
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
	"github.com/qoryai/qory/internal/config"
	"github.com/qoryai/qory/internal/exports"
	"github.com/qoryai/qory/internal/integration"
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

// The words of qory run prepare. Each is one format string.
const (
	textNoSystem       = "qory run prepare: %s is set but %s is not; both name where the code is"
	textNotHost        = "qory run prepare: %s is not a host name"
	textNotPath        = "qory run prepare: %s entry %d is not a repository path: segments of A-Z, a-z, 0-9, dot, underscore and dash, with no ., .. or .git segment, 1 to 200 characters"
	textNested         = "qory run prepare: %s entry %d, %s, is inside entry %d, %s; a repository is not cloned inside another"
	textTwice          = "qory run prepare: %s names %s twice"
	textTooLong        = "qory run prepare: %s is more than 256 bytes"
	textNotEmpty       = "qory run prepare: the working folder holds files already; the run's repository is fetched only into an empty git working tree"
	textNotFetched     = "qory run prepare: %s could not be fetched from %s: %s"
	textKept           = "qory run prepare: %s has .qory/repositories already, which qory keeps for the further repositories; nothing is cloned over it"
	textQoryNotDir     = "qory run prepare: %s has .qory as a file or a link, where qory keeps the further repositories in a folder; nothing is cloned over it"
	textOtherOwner     = "qory run prepare: %s entry %d is of owner %s, not %s; on github.com the repositories of a run are of one owner"
	textNotGitHub      = "qory run prepare: %s entry %d is not owner/name, the form of a repository on github.com"
	textNotTree        = "qory run prepare: the working folder is not a git working tree without commits; the run's repository is fetched only into an empty git working tree"
	textExcludeLink    = "qory run prepare: the working folder's %s is a link; qory writes the exclude file of the repository only where no link leads"
	textExcludeOutside = "qory run prepare: the exclude file of the working folder's repository, %s, is outside the working folder; qory writes it only inside"
	textGitNotDir      = "qory run prepare: the working folder's .git is a file or a link, which points at a repository elsewhere; the run's repository is fetched only into an empty git working tree"
	textHasRemote      = "qory run prepare: the working folder's repository has a remote already; the run's repository is fetched only into an empty git working tree"
	textGitFailed      = "qory run prepare: git could not read the working folder: %s"
	textLocal          = "qory run prepare: %s"
	textFetched        = "qory run prepare: fetched %s in %s"
	textFetchedOne     = "qory run prepare: fetched %s and 1 further repository in %s"
	textFetchedMany    = "qory run prepare: fetched %s and %d further repositories in %s"
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

// Run fetches the repositories getenv names into dir, the working folder, and reports
// whether it fetched them. With [EnvRepositories] unset or empty it does nothing and
// returns false. The list is checked whole before any fetch. When seed, a prebuilt
// image's copy of the run's repository such as [Seed], is there, the run's repository
// starts from it, as [fromSeed] has it, and Run prints a line that says so on report.
// Once every repository is fetched, it prints the line that says what it fetched, and
// in how long, the copy included. Every error's text is one line, ready for stderr.
func Run(ctx context.Context, dir, seed string, getenv func(string) string, report io.Writer) (bool, error) {
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
	if err := emptyTree(ctx, dir); err != nil {
		return false, err
	}
	start := time.Now()
	first := integration.Printable(exports.Value(paths[0], false))
	seeded, err := fromSeed(ctx, dir, seed, system, paths[0])
	if err != nil {
		return false, err
	}
	if seeded {
		fmt.Fprintf(report, textSeedReplaced+"\n", first)
	} else if err := fetchRun(ctx, dir, system, paths[0], false); err != nil {
		return false, err
	}
	further := paths[1:]
	if len(further) > 0 {
		if err := keepRepositories(dir, paths[0]); err != nil {
			return false, err
		}
		if err := exclude(ctx, dir); err != nil {
			return false, err
		}
		for _, p := range further {
			dest := filepath.Join(dir, filepath.FromSlash(Repositories), filepath.FromSlash(p))
			if _, err := git(ctx, dir, "clone", "--quiet", "--depth", "1", "--", repoURL(system, p), dest); err != nil {
				return false, notFetched(p, system, err)
			}
		}
	}
	took := seconds(time.Since(start))
	switch len(further) {
	case 0:
		fmt.Fprintf(report, textFetched+"\n", first, took)
	case 1:
		fmt.Fprintf(report, textFetchedOne+"\n", first, took)
	default:
		fmt.Fprintf(report, textFetchedMany+"\n", first, len(further), took)
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
// every entry is owner/name, and of the first entry's owner; that is checked here
// because the selects pattern cannot say it. A list Check returns is one the URLs and
// the folders are built from.
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
	github := system == config.ForgeGitHub
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

// emptyTree refuses a working folder that holds anything but .git and .qory; then one
// whose .git is not a folder, a file or a link to a repository elsewhere, such as a
// worktree's or a separate git directory's, whose path is the host's; then one with no
// .git, which is outside a working tree or nested in one; then one that is not
// the top of its working tree, whose repository has a commit, or has a remote. A git
// that fails to read the folder is [textGitFailed], not a refusal of the folder.
func emptyTree(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return local(err)
	}
	hasGit := false
	for _, e := range entries {
		switch e.Name() {
		case ".git":
			if !e.IsDir() {
				return errors.New(textGitNotDir)
			}
			hasGit = true
		case checkout.Dir:
		default:
			return errors.New(textNotEmpty)
		}
	}
	if !hasGit {
		return errors.New(textNotTree)
	}
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitFailed(err)
	}
	if !samePath(strings.TrimSpace(top), dir) {
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

// fetchRun fetches the run's repository into dir: origin is its URL, and the remote's
// default branch is fetched at depth one and checked out as the local branch of its
// name, tracking origin. With over set, the checkout is over files that are there
// already, the image's copy's working tree: it is by force, so the tracked files
// become the fetched commit's and the untracked and ignored ones stay, as git reset
// --hard leaves them.
func fetchRun(ctx context.Context, dir, system, path string, over bool) error {
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
	checkout := []string{"checkout", "--quiet", "-b", branch, "--track", "origin/" + branch}
	if over {
		checkout = []string{"checkout", "--quiet", "--force", "-B", branch, "--track", "origin/" + branch}
	}
	if _, err := git(ctx, dir, checkout...); err != nil {
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
func exclude(ctx context.Context, dir string) error {
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
// configuration: core.hooksPath names /dev/null, where git finds no hook file;
// core.fsmonitor is empty, which turns the file system monitor off on any git; and the
// credential helper is empty, so none is asked. A hook a configuration defines by
// hook.<name>.command is not turned off here: [gitEnv] keeps every configuration but
// the repository's away from git.
//
// The git program is the image's, found on PATH. In an image built from the
// repository, the preparation is only as trustworthy as that image: the repository's
// code, the build that ran it among it, is inside the agent's trust boundary, and the
// options here keep a repository's configuration from running a program, not a
// program the image holds.
var hardened = []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=", "-c", "credential.helper="}

// git runs git in dir with args, with the [hardened] options, no prompt and no variable
// that would point it at another repository, for at most [limitOf] its subcommand within ctx. git
// runs in a new process group, and the whole group is stopped when the call ends, so a
// helper git started, such as git-remote-https, never holds the call open.
// It returns what git printed on stdout, and on a failure a [*gitError].
func git(ctx context.Context, dir string, args ...string) (string, error) {
	limit := limitOf(args[0])
	call, cancel := context.WithTimeoutCause(ctx, limit, errLimit)
	defer cancel()
	cmd := exec.CommandContext(call, "git", append(slices.Clone(hardened), args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A process left holding the output is not waited for past this.
	cmd.WaitDelay = time.Second
	group(cmd)
	err := cmd.Run()
	stopGroup(cmd)
	if err != nil {
		switch {
		case errors.Is(context.Cause(call), errLimit):
			return "", &gitError{fmt.Sprintf(textStopped, args[0], limit), -1}
		case ctx.Err() != nil:
			return "", &gitError{fmt.Sprintf(textCut, args[0]), -1}
		}
		exit := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exit = ee.ExitCode()
		}
		if line := firstLine(stderr.String()); line != "" {
			return "", &gitError{line, exit}
		}
		return "", &gitError{err.Error(), exit}
	}
	return stdout.String(), nil
}

// repositoryVariables are git's variables that point it at a repository, an index or a
// store other than the one in its directory.
var repositoryVariables = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE"}

// droppedVariables are git's variables that add configuration, name a program git
// runs or a folder it takes programs or templates from, turn off the check of a
// host's certificate, trace what git does, or name replace refs. [gitEnv] drops each,
// and the ones that set what it sets.
var droppedVariables = []string{
	"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM",
	"GIT_EXEC_PATH", "GIT_TEMPLATE_DIR", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_PROXY_COMMAND", "GIT_SSL_NO_VERIFY",
	"GIT_REPLACE_REF_BASE", "GIT_NO_REPLACE_OBJECTS",
	"GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "SSH_ASKPASS",
}

// droppedPrefixes are the starts of the names of git's variables [gitEnv] drops: the
// configuration of GIT_CONFIG_COUNT and git's traces.
var droppedPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_", "GIT_TRACE"}

// gitEnv is env without [repositoryVariables] and [droppedVariables], and with git's
// prompts off, no terminal prompt and no askpass program, no global or system
// configuration, and no replace object: git reads the repository's configuration
// alone. The proxy and the CA bundle it names, GIT_SSL_CAINFO, stay as given: the wall
// gives git its proxy and its certificate authority that way.
func gitEnv(env []string) []string {
	out := make([]string, 0, len(env)+6)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(repositoryVariables, name) || slices.Contains(droppedVariables, name) ||
			slices.ContainsFunc(droppedPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
}

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

// notFetched is [textNotFetched] for path on system, with git's line made printable:
// path and system as [exports.Value] prints them, and git's line with a URL's password
// and each word with an access key secret or a run key hidden, as
// [exports.HideURLError] hides them.
func notFetched(path, system string, err error) error {
	return fmt.Errorf(textNotFetched, integration.Printable(exports.Value(path, false)), integration.Printable(exports.Value(system, false)), integration.Printable(exports.HideURLError(err).Error()))
}

// gitFailed is [textGitFailed] with git's line, or what kept git from running, made
// printable.
func gitFailed(err error) error {
	return fmt.Errorf(textGitFailed, integration.Printable(exports.HideURLError(err).Error()))
}

// local is [textLocal]: a failure on the machine, not of a fetch.
func local(err error) error {
	return fmt.Errorf(textLocal, integration.Printable(exports.HideError(err).Error()))
}

// samePath reports whether a and b name one directory, once their links are resolved.
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
