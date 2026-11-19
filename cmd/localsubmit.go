package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/qoryai/integrations/contracts"
	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runcopy"
	"github.com/qoryai/qory/internal/submission"
)

// The words of the local submit step. Line 1 is the fix, and the cause and the values follow on
// indented lines.
const (
	textPushChecksMissing = "qory run: Push each branch by hand: the key and scope push checks of qory run are not registered.\n  qory pushed nothing and opened no draft; open each draft by hand if you want one."
	textPushChecksBranch  = "\n  Branch: %s in %s."
	textPushCheckStopped       = "qory run: Fix the cause below, then run again: a push check stopped the push.\n  qory pushed nothing and opened no draft.\n  Branch: %s.\n  Cause: %s"
	textLocalPushFailed   = "qory run: Fix the cause below, then run again: the push failed, so qory opened no draft.\n  Branch: %s.\n  Cause: %s"
	textLocalNoHistory    = "qory run: Fix the cause below, then run again: qory could not read the branch or its origin.\n  qory pushed nothing and opened no draft.\n  Branch: %s.\n  Cause: %s"
	textLocalNoCommits    = "qory run: Open a draft by hand if you want one: qory pushed the branch but could not list commits.\n  qory ran no program for that repository, so it opened no draft for it.\n  Branch: %s in %s.\n  Cause: %s"
	textDraftLine         = "Draft: "
	textLocalNoRemote     = "qory run: Set remote origin to a forge repository address, then run again; qory pushed nothing.\n  qory opened no draft.\n  Branch: %s.\n  Cause: %s"
	textLocalPushURLs     = "qory run: Remove the extra push addresses of remote origin, so it has one.\n  The remote origin has %d push addresses, so qory pushed nothing and opened no draft.\n  Branch: %s.\n  List the configured addresses and the file of each with git config --show-origin --get-all remote.origin.pushurl, or with git config --show-origin --get-all remote.origin.url when that prints none.\n  Do not use git remote get-url --push --all: it shows the addresses after the insteadOf rewrites of git.\n  For each file that --show-origin shows (the path after file:), run git config --file <file> --unset-all remote.origin.pushurl in the top folder of the repository. Put a path with spaces in quotes, and edit a file by hand when git prints its path in double quotes. Exit status 5 means that file has no pushurl value. The system file usually needs administrator rights.\n  If --show-origin shows command line: instead of a file, run unset GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS (fish: set -e GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS) in the shell that runs qory, then run qory again.\n  Then run git remote set-url --push origin <address> with the address to keep."
)

// localPushCheck is a check of a push that a local run makes: it gets the repository and the
// branch about to be pushed, and an error stops the push. The check for keys in the commits and
// the check for changes to files that a session may not change are registered with
// [registerPushCheck].
type localPushCheck func(ctx context.Context, repo localRepository) error

// localRepository is the repository a local run pushes: its folder, its system and path, the
// commit the run started at, and the branch, with its head.
type localRepository struct {
	Dir, System, Path string
	Start, Head       string
	Branch            string
	// PushURL is the one address that qory pushes to: the push address of the remote origin, after
	// git's url.<base>.insteadOf and pushInsteadOf rewrites. System and Path come from it.
	PushURL string
}

// The names of the push checks that a local run needs before it pushes. A push proceeds only when
// every one of them is registered.
const (
	// pushCheckKey is the check for a key or a secret value in the run's commits.
	pushCheckKey = "key"
	// pushCheckScope is the check for a change to a file that a session may not change.
	pushCheckScope = "scope"
)

// requiredPushChecks are the names of the checks that a push needs, in the order they run.
var requiredPushChecks = []string{pushCheckKey, pushCheckScope}

// localPushChecks are the registered checks, by name.
var localPushChecks = map[string]localPushCheck{}

// registerPushCheck sets the check with the name, one of [requiredPushChecks]. It is the one place
// where a check joins the step.
func registerPushCheck(name string, check localPushCheck) { localPushChecks[name] = check }

// pushChecks is the checks in the order of [requiredPushChecks], and false when one of them is
// not registered: the step then fails closed.
func pushChecks() ([]localPushCheck, bool) {
	var checks []localPushCheck
	for _, name := range requiredPushChecks {
		check, ok := localPushChecks[name]
		if !ok {
			return nil, false
		}
		checks = append(checks, check)
	}
	return checks, true
}

// localPushConfig is the git configuration, as GIT_CONFIG_* environment entries, that a push gets
// for the push address address, on top of the person's. Tests set it to rewrite the address of a
// forge to a repository on disk with a real url.<base>.insteadOf rule; it is empty otherwise.
var localPushConfig = func(address string) []string { return nil }

// submitLocal is the submit step of a local run, a run with no run code that works in a copy:
// after the run's commits are in the person's repositories, on the branches in c.landed, it reads
// the hand-off in the copy, runs the push checks on every repository, pushes each branch with the
// person's git configuration, and then runs the submit program of the composed harness once for
// each pushed repository. passed is whether the session ended with exit 0. The program opens a
// draft only for a passed session with a hand-off text, and gets the draft none otherwise. A check
// that returns an error ends the step before any push, and a push that fails leaves its repository out of
// the program. Each line goes to w. It returns the address of each draft that a program returned,
// in the order of the repositories.
func (c *runCopy) submitLocal(ctx context.Context, w io.Writer, program *submitProgram, passed bool) (drafts []string) {
	if len(c.landed) == 0 {
		return nil
	}
	checks, ok := pushChecks()
	if !ok {
		fmt.Fprint(w, textPushChecksMissing)
		for _, r := range c.repos {
			if branch := c.landed[r.Path]; branch != "" {
				fmt.Fprintf(w, textPushChecksBranch, shownText(branch), shownText(filepath.Join(c.root, filepath.FromSlash(r.Path))))
			}
		}
		fmt.Fprintln(w)
		return nil
	}
	given := readHandoffPrinting(w, c.dir.Work())
	// Every check of every repository runs before the first push, and every push before the
	// first program.
	var repos []localRepository
	for _, r := range c.repos {
		branch := c.landed[r.Path]
		if branch == "" {
			continue
		}
		repo, err := c.localRepository(ctx, r, branch)
		var many *manyPushURLs
		var plain *notForge
		switch {
		case errors.As(err, &many):
			fmt.Fprintf(w, textLocalPushURLs+"\n", many.n, shownText(branch))
			return nil
		case errors.As(err, &plain):
			fmt.Fprintf(w, textLocalNoRemote+"\n", shownText(branch), plain.Error())
			return nil
		case err != nil:
			fmt.Fprintf(w, textLocalNoHistory+"\n", shownText(branch), shownError(err))
			return nil
		}
		if err := preparation.HandoffCommitted(ctx, repo.Dir, repo.System, repo.Path, repo.Start, repo.Head); err != nil {
			// The text is a line with its cause and the values on indented lines.
			for line := range strings.SplitSeq(err.Error(), "\n") {
				fmt.Fprintln(w, shownErrorText(line))
			}
			return nil
		}
		for _, check := range checks {
			if err := check(ctx, repo); err != nil {
				fmt.Fprintf(w, textPushCheckStopped+"\n", shownText(repo.Branch), shownError(err))
				return nil
			}
		}
		repos = append(repos, repo)
	}
	var pushed []localRepository
	for _, repo := range repos {
		if out, err := pushBranch(ctx, repo); err != nil {
			fmt.Fprintf(w, textLocalPushFailed+"\n", shownText(repo.Branch), shownText(lastOutputLine(out, err)))
			continue
		}
		pushed = append(pushed, repo)
	}
	verdict := submission.VerdictNone
	if passed {
		verdict = submission.VerdictPassed
	}
	for _, repo := range pushed {
		document, err := localSubmission(ctx, repo, given, verdict)
		if err != nil {
			fmt.Fprintf(w, textLocalNoCommits+"\n", shownText(repo.Branch), shownText(repo.Dir), shownError(err))
			continue
		}
		result, err := program.apply(ctx, w, document)
		if err != nil {
			return drafts
		}
		if address := draftAddress(result); address != "" {
			drafts = append(drafts, address)
		}
	}
	return drafts
}

// draftAddress is the address of the draft that the program returned for the one repository of
// document, "" for none: a draft of none, a program that failed, or one whose lines were not all
// valid. The address is the value that [contracts.ParseSubmitLines] accepted: https, on the
// repository's system, of the characters that RFC 3986 allows, so it has no control character.
func draftAddress(document *submission.Submission) string {
	if len(document.Repositories) != 1 {
		return ""
	}
	var draft contracts.SubmittedDraft
	if json.Unmarshal(document.Repositories[0].Draft, &draft) != nil || draft.Number == 0 {
		return ""
	}
	return draft.URL
}

// notForge is the error of a push address that is not a host and a repository path of a forge.
type notForge struct{}

func (*notForge) Error() string { return "the address is not a host and a repository path of a forge" }

// manyPushURLs is the error of a remote origin with more than one push address.
type manyPushURLs struct{ n int }

func (e *manyPushURLs) Error() string {
	return fmt.Sprintf("the remote origin has %d push addresses", e.n)
}

// localRepository is the repository r of the run: the commit the run started at, the head of the
// branch that the run's commits landed on, and the push address of the remote origin with its system
// and path.
func (c *runCopy) localRepository(ctx context.Context, r runcopy.Repository, branch string) (localRepository, error) {
	dir := filepath.Join(c.root, filepath.FromSlash(r.Path))
	repo := localRepository{Dir: dir, Start: r.Base, Branch: branch}
	head, err := localGit(ctx, dir, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return repo, err
	}
	repo.Head = strings.TrimSpace(head)
	// The push address, with git's rewrites applied: the address that a push goes to, which is
	// not always the fetch address.
	out, err := localGit(ctx, dir, "remote", "get-url", "--push", "--all", "origin")
	if err != nil {
		return repo, err
	}
	addresses := strings.Fields(out)
	if len(addresses) != 1 {
		return repo, &manyPushURLs{len(addresses)}
	}
	system, path, ok := remoteSystemPath(addresses[0])
	if !ok {
		// The address is not in the error: it can have a password.
		return repo, &notForge{}
	}
	repo.System, repo.Path, repo.PushURL = system, path, addresses[0]
	return repo, nil
}

// localSubmission is the submission of one repository for the program: the branch, the verdict,
// the hand-off's text, and the repository with its commits, each with the hand-off's step. Its
// draft is the placeholder for a passed session with a text, and none with the reason otherwise.
// The base branch is the branch that the repository's checkout is on, unset for a detached HEAD.
func localSubmission(ctx context.Context, repo localRepository, given *handoff, verdict string) (*submission.Submission, error) {
	out, err := localGit(ctx, repo.Dir, "rev-list", "--reverse", repo.Start+".."+repo.Head)
	if err != nil {
		return nil, err
	}
	base := &submission.Base{Commit: repo.Start}
	if branch, err := localGit(ctx, repo.Dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		base.Branch = strings.TrimSpace(branch)
	}
	text := given.text()
	entry := submission.Repository{System: repo.System, Path: repo.Path, Commit: repo.Head, Base: base, Draft: submission.None(noDraftReason(verdict, text), 0)}
	for commit := range strings.FieldsSeq(out) {
		entry.Commits = append(entry.Commits, submission.Commit{Commit: commit, Step: given.stepOf(commit)})
	}
	return &submission.Submission{Ref: repo.Branch, Verdict: verdict, Text: text, Repositories: []submission.Repository{entry}}, nil
}

// pushArgs is the arguments of the git push of repo: the checked commit, not the branch name that
// could move, to the remote origin by its name. A push to the name goes to what `git remote
// get-url --push --all origin` reports, the address that the checks and the program got; a push
// to that address as an argument would get git's insteadOf and pushInsteadOf rules a second time
// and could land elsewhere. It has no force, no hook of the repository, no submodule and no tag
// that follows the commit.
func pushArgs(repo localRepository) []string {
	return []string{"-C", repo.Dir, "push", "--quiet", "--no-verify", "--no-recurse-submodules", "--no-follow-tags", "--",
		"origin", repo.Head + ":refs/heads/" + repo.Branch}
}

// pushBranch pushes the head that the checks saw to the branch of repo, with the person's git
// configuration and login, and with no input: a command that asks for one fails.
func pushBranch(ctx context.Context, repo localRepository) (string, error) {
	cmd := pushCommand(ctx, repo)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// pushCommand is the git push of repo with the person's environment, which has the person's git
// configuration and login, and no prompt on the terminal.
func pushCommand(ctx context.Context, repo localRepository) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", pushArgs(repo)...)
	cmd.Env = append(localGitEnv(), localPushConfig(repo.PushURL)...)
	return cmd
}

// localGitEnv is the person's environment with GIT_TERMINAL_PROMPT=0, for every git that the local
// step runs: a command that needs a password or a question fails, and nothing is asked.
func localGitEnv() []string {
	return append(slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_TERMINAL_PROMPT=") }), "GIT_TERMINAL_PROMPT=0")
}

// localGit runs git in dir with the person's environment and returns its standard output.
func localGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = localGitEnv()
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], lastOutputLine(stderr.String(), err))
	}
	return out.String(), nil
}

// lastOutputLine is the last line of out that is not blank, or err's text when out has none.
func lastOutputLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return last
	}
	return err.Error()
}

// forgeHost is a host name of a forge: dotted lower-case labels of letters, digits and hyphens, as
// github.com is. A host with no dot, an address and anything with another character is not one.
var forgeHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// forgePath is the path of a repository on a forge: at least two segments of letters, digits and
// . _ ~ -, none of them . or ..
var forgePath = regexp.MustCompile(`^[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)+$`)

// remoteSystemPath is the host and the repository path of a git remote address of a forge:
// https://host/a/b.git, http, ssh and git with a host, and git@host:a/b. It returns an error for any
// other scheme, such as file or ext, an address with no host, and a host or path that is not one of a
// forge, so the checks and the program never get a system such as file.
func remoteSystemPath(remote string) (system, path string, ok bool) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || !slices.Contains([]string{"https", "http", "ssh", "git"}, u.Scheme) {
			return "", "", false
		}
		system, path = u.Hostname(), strings.Trim(u.Path, "/")
	} else if host, rest, found := strings.Cut(remote, ":"); found && !strings.Contains(host, "/") && !strings.HasPrefix(rest, ":") {
		system, path = host[strings.LastIndex(host, "@")+1:], strings.Trim(rest, "/")
	}
	path = strings.TrimSuffix(path, ".git")
	system = strings.ToLower(system)
	if !forgeHost.MatchString(system) || !forgePath.MatchString(path) || slices.Contains(strings.Split(path, "/"), "..") || slices.Contains(strings.Split(path, "/"), ".") {
		return "", "", false
	}
	return system, path, true
}
