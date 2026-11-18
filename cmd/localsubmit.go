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
	"strings"

	"github.com/qoryai/qory/internal/preparation"
	"github.com/qoryai/qory/internal/runcopy"
	"github.com/qoryai/qory/internal/submission"
)

// The words of the local submit step. Line 1 of each states the cause, and the values follow
// on indented lines.
const (
	textPushChecksMissing = "qory run: The push checks are not available yet, so qory pushed nothing and opened no draft."
	textPushRefused       = "qory run: A push check refused the push, so qory pushed nothing and opened no draft.\n  Branch: %s.\n  Cause: %s"
	textLocalPushFailed   = "qory run: The push of the branch failed, so qory opened no draft.\n  Branch: %s.\n  Cause: %s"
	textLocalNoHistory    = "qory run: qory could not read the branch or its origin, so it pushed nothing and opened no draft.\n  Branch: %s.\n  Cause: %s"
	textDraftLine         = "Draft: "
	textLocalPushURLs     = "qory run: The remote origin has %d push addresses, so qory pushed nothing and opened no draft.\n  Branch: %s.\n  Fix: keep one with git remote set-url --push origin <address>."
)

// localPushCheck is a check of a push that a local run makes: it gets the repository and the
// branch about to be pushed, and an error refuses the push. The check for keys in the commits and
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

// localPushAddress is the address that git gets for a push. Tests set it to reach a repository on
// disk for an address of a forge; it is the identity otherwise.
var localPushAddress = func(address string) string { return address }

// submitLocal is the submit step of a local run, a run with no run code that works in a copy:
// after the run's commits are in the person's repositories, on the branches in c.landed, it reads
// the hand-off in the copy, runs the push checks on every repository, pushes each branch with the
// person's git configuration, and then runs the submit program of the composed harness once for
// each pushed repository. passed is whether the session ended with exit 0. The program opens a
// draft only for a passed session with a hand-off text, and gets the draft none otherwise. A check
// that refuses ends the step before any push, and a push that fails leaves its repository out of
// the program. Each line goes to w. It returns the address of each draft that a program returned,
// in the order of the repositories.
func (c *runCopy) submitLocal(ctx context.Context, w io.Writer, program *submitProgram, passed bool) (drafts []string) {
	if len(c.landed) == 0 {
		return nil
	}
	checks, ok := pushChecks()
	if !ok {
		fmt.Fprintln(w, textPushChecksMissing)
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
		switch {
		case errors.As(err, &many):
			fmt.Fprintf(w, textLocalPushURLs+"\n", many.n, shownText(branch))
			return nil
		case err != nil:
			fmt.Fprintf(w, textLocalNoHistory+"\n", shownText(branch), shownError(err))
			return nil
		}
		if err := preparation.HandoffCommitted(ctx, repo.Dir, repo.System, repo.Path, repo.Start, repo.Head); err != nil {
			fmt.Fprintln(w, shownError(err))
			return nil
		}
		for _, check := range checks {
			if err := check(ctx, repo); err != nil {
				fmt.Fprintf(w, textPushRefused+"\n", shownText(repo.Branch), shownError(err))
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
			fmt.Fprintf(w, textLocalNoHistory+"\n", shownText(repo.Branch), shownError(err))
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
// valid. The address is the value that [parseSubmitLines] accepted: https, on the repository's
// system, of the characters that RFC 3986 allows, so it has no control character.
func draftAddress(document *submission.Submission) string {
	if len(document.Repositories) != 1 {
		return ""
	}
	var draft pullRequest
	if json.Unmarshal(document.Repositories[0].Draft, &draft) != nil || draft.Number == 0 {
		return ""
	}
	return draft.URL
}

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
		return repo, fmt.Errorf("the address %s has no host and path", shownText(addresses[0]))
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
// could move, to the one address that the checks and the program got. It has no force, no hook of
// the repository, no submodule and no tag that follows the commit.
func pushArgs(repo localRepository) []string {
	return []string{"-C", repo.Dir, "push", "--quiet", "--no-verify", "--no-recurse-submodules", "--no-follow-tags", "--",
		localPushAddress(repo.PushURL), repo.Head + ":refs/heads/" + repo.Branch}
}

// pushBranch pushes the head that the checks saw to the branch of repo, with the person's git
// configuration and login, and with no input: a command that asks for one fails.
func pushBranch(ctx context.Context, repo localRepository) (string, error) {
	cmd := exec.CommandContext(ctx, "git", pushArgs(repo)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// localGit runs git in dir with the person's environment and returns its standard output.
func localGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
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

// remoteSystemPath is the host and the repository path of a git remote address: https://host/a/b.git,
// ssh://git@host/a/b and git@host:a/b.
func remoteSystemPath(remote string) (system, path string, ok bool) {
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.Host != "" {
		system, path = u.Hostname(), strings.Trim(u.Path, "/")
	} else if host, rest, found := strings.Cut(remote, ":"); found && !strings.Contains(host, "/") {
		system, path = host[strings.LastIndex(host, "@")+1:], strings.Trim(rest, "/")
	}
	path = strings.TrimSuffix(path, ".git")
	return strings.ToLower(system), path, system != "" && path != ""
}
