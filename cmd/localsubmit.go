package cmd

import (
	"bytes"
	"context"
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
}

// localPushChecks are the checks that run before a local run pushes, in the order of their
// registration. With none registered, the step pushes nothing: it fails closed.
var localPushChecks []localPushCheck

// registerPushCheck adds check to the checks that every push of a local run passes. It is the one
// place where a check joins the step.
func registerPushCheck(check localPushCheck) { localPushChecks = append(localPushChecks, check) }

// submitLocal is the submit step of a local run, a run with no run code that works in a copy:
// after the run's commits are in the person's repositories, on the branches in c.landed, it reads
// the hand-off in the copy, runs the push checks on every repository, pushes each branch with the
// person's git configuration, and then runs the submit program of the composed harness once for
// each pushed repository. passed is whether the session ended with exit 0. The program opens a
// draft only for a passed session with a hand-off text, and gets the draft none otherwise. A check
// that refuses ends the step before any push, and a push that fails leaves its repository out of
// the program. Each line goes to w.
func (c *runCopy) submitLocal(ctx context.Context, w io.Writer, program *submitProgram, passed bool) {
	if len(c.landed) == 0 {
		return
	}
	if len(localPushChecks) == 0 {
		fmt.Fprintln(w, textPushChecksMissing)
		return
	}
	given := readHandoffPrinting(w, c.dir.Work())
	var repos []localRepository
	for _, r := range c.repos {
		branch := c.landed[r.Path]
		if branch == "" {
			continue
		}
		repo, err := c.localRepository(ctx, r, branch)
		if err != nil {
			fmt.Fprintf(w, textLocalNoHistory+"\n", shownText(branch), shownError(err))
			return
		}
		if err := preparation.HandoffCommitted(ctx, repo.Dir, repo.System, repo.Path, repo.Start, repo.Head); err != nil {
			fmt.Fprintln(w, shownError(err))
			return
		}
		for _, check := range localPushChecks {
			if err := check(ctx, repo); err != nil {
				fmt.Fprintf(w, textPushRefused+"\n", shownText(repo.Branch), shownError(err))
				return
			}
		}
		repos = append(repos, repo)
	}
	verdict := submission.VerdictNone
	if passed {
		verdict = submission.VerdictPassed
	}
	for _, repo := range repos {
		if out, err := pushBranch(ctx, repo); err != nil {
			fmt.Fprintf(w, textLocalPushFailed+"\n", shownText(repo.Branch), shownText(lastOutputLine(out, err)))
			continue
		}
		document, err := localSubmission(ctx, repo, given, verdict)
		if err != nil {
			fmt.Fprintf(w, textLocalNoHistory+"\n", shownText(repo.Branch), shownError(err))
			continue
		}
		if _, err := program.apply(ctx, w, document); err != nil {
			return
		}
	}
}

// localRepository is the repository r of the run: the commit the run started at, the head of the
// branch that the run's commits landed on, and the system and path of the remote origin.
func (c *runCopy) localRepository(ctx context.Context, r runcopy.Repository, branch string) (localRepository, error) {
	dir := filepath.Join(c.root, filepath.FromSlash(r.Path))
	repo := localRepository{Dir: dir, Start: r.Base, Branch: branch}
	head, err := localGit(ctx, dir, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return repo, err
	}
	repo.Head = strings.TrimSpace(head)
	remote, err := localGit(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return repo, err
	}
	system, path, ok := remoteSystemPath(strings.TrimSpace(remote))
	if !ok {
		return repo, fmt.Errorf("the address %s has no host and path", shownText(strings.TrimSpace(remote)))
	}
	repo.System, repo.Path = system, path
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

// pushBranch pushes the branch of repo to the remote origin with the person's git configuration
// and login, without force and with no input: a command that asks for one fails.
func pushBranch(ctx context.Context, repo localRepository) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repo.Dir, "push", "origin", "refs/heads/"+repo.Branch+":refs/heads/"+repo.Branch)
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
