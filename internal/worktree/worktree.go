// Package worktree adds, removes and lists the linked worktrees of a repository, and
// prepares a new one the way the repository's qory.yaml says: files linked or copied from
// the main checkout, or from anywhere on the machine, and commands run in the new
// worktree. What it does with git is plain: the remote fetched first, so the base and
// the branch are the remote's; a worktree per branch, beside the main checkout unless
// the configuration says where else; a new branch cut off a base with no upstream on it,
// and its upstream set to a remote branch of its own name, whether that branch exists
// yet or not, so a push from the worktree creates or updates that branch and never
// touches the base. The base is recorded in the branch's git config, so a later add can
// move the branch onto another one and a remove can tell whether the branch holds
// anything of its own, which decides whether it goes with the worktree quietly or after
// a question.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options is what an add or a remove is told, from the configuration and the flags.
type Options struct {
	// Dir is where worktrees go, relative to the main checkout unless absolute.
	Dir string
	// Name is the directory name template: {branch} with each slash made a dash, {repo}
	// the main checkout's directory name.
	Name string
	// Base is the ref a new branch starts from, "" for the remote's HEAD branch, else the
	// main checkout's current branch.
	Base string
	// Onto says Base was asked for on the command line, so a branch that already exists
	// is moved or rebased onto it after Ask, instead of left where it is.
	Onto bool
	// Rebase answers the question Onto asks with yes.
	Rebase bool
	// Offline skips the fetch an add starts with, so the refs already there are used.
	Offline bool
	// Timeout is the longest the fetch, or the push a remove offers, may run; 0 for no
	// limit.
	Timeout time.Duration
	// Attach is a branch of the remote the worktree attaches to, from [RemoteBranch] or
	// [PullRequest]; its zero value attaches to nothing. The branch is fetched, checked
	// out under the name Attach.Name gives and set to track it.
	Attach Remote
	// As names the worktree instead of its branch: it is what {branch} in Name becomes,
	// "" for the branch.
	As string
	// PullRef is the ref the remote publishes a pull request's head under, {n} for its
	// number, "" to probe [PullRefs].
	PullRef string
	// Link are paths linked into the new worktree.
	Link []Path
	// Copy are paths copied once into the new worktree.
	Copy []Path
	// Add are commands run in the new worktree after the links and copies, in order.
	Add []string
	// Remove are commands run in a worktree before it is removed, in order.
	Remove []string
	// Force removes a worktree with uncommitted changes.
	Force bool
	// KeepBranch keeps the branch after the worktree is removed; the default deletes it.
	KeepBranch bool
	// DeleteBranch deletes the branch even when it holds commits nothing else does,
	// without asking.
	DeleteBranch bool
	// Ask puts a question to the user and returns the answer, trimmed and lower-cased,
	// "" for none. It is nil when nobody is there to answer, and a question is then an
	// error naming the flag that answers it.
	Ask func(question string) (string, error)
	// Output is where the commands' output goes as they run; nil keeps it, and a command
	// that fails carries what it printed in its [*RunError].
	Output io.Writer
	// Trace is told each git command as it runs, and how the commits a branch holds of
	// its own are counted; nil for none.
	Trace func(line string)
}

// Path is one thing brought into a new worktree: From is where it comes from, relative
// to the main checkout unless absolute, and To is where it goes, relative to the
// worktree. A path named alone in the configuration has the two the same.
type Path struct {
	From, To string
}

// Outside reports whether the path comes from outside the main checkout.
func (p Path) Outside() bool { return filepath.IsAbs(p.From) }

// Added is what [Add] did.
type Added struct {
	// Path is the worktree's absolute path.
	Path string
	// Branch is the branch checked out in it.
	Branch string
	// How says where the branch came from: "new off <base>", "local", "remote", "fetched
	// from <remote>" for Options.Attach, or "already there" for a worktree that was
	// already there on that branch, and what Options.Onto did with it after a semicolon.
	How string
	// Upstream is the remote branch the worktree pushes to, "" without a remote or when
	// Head is set.
	Upstream string
	// Head is the remote's ref the branch pulls from when it is a pull request's head that
	// no branch of the remote holds, a fork's or a deleted one, as "<remote> <ref>"; a
	// push from the worktree goes nowhere then. It is "" otherwise.
	Head string
	// Linked and Copied are the paths brought in.
	Linked, Copied []Path
	// Kept are the paths of Link and Copy already present in the worktree, left as they
	// were, a dangling link among them.
	Kept []Path
	// Missing are the paths of Link and Copy whose From is not there.
	Missing []Path
	// Ran are the commands run, in order.
	Ran []string
}

// Removed is what [Remove] did.
type Removed struct {
	// Path is the removed worktree's absolute path, Branch its branch.
	Path, Branch string
	// Main is the main checkout, where a shell goes next.
	Main string
	// Ran are the commands run before the removal, in order.
	Ran []string
	// Own is how many commits the branch held that nothing else did: no remote branch,
	// not the main checkout, not the base it was cut from.
	Own int
	// Pushed says the branch was pushed to Upstream before it was deleted.
	Pushed bool
	// Upstream is the remote branch of the branch's name, "" without a remote.
	Upstream string
	// BranchDeleted says whether the branch went with the worktree.
	BranchDeleted bool
}

// Entry is one worktree of the repository.
type Entry struct {
	// Path is absolute; Branch is "" for a detached head.
	Path, Branch string
	// Main marks the main checkout.
	Main bool
	// Composed says whether a qory compose report is in it, and Report is that report's
	// path, "" when there is none.
	Composed bool
	Report   string
}

// RunError is a command of Options.Add or Options.Remove that failed. The worktree is
// kept as it is, so the command can be repaired and the verb run again.
type RunError struct {
	// Command is the command as the configuration wrote it, Path the worktree it ran in.
	Command, Path string
	// Verb is the verb that ran it, "add" or "remove".
	Verb string
	// Output is what the command printed, when Options.Output did not take it as it ran.
	Output string
	// Err is the error the command ended with.
	Err error
}

func (e *RunError) Error() string {
	msg := fmt.Sprintf("%s in %s: %v; the worktree is kept, repair the command and %s again", e.Command, e.Path, e.Err, e.Verb)
	if out := strings.TrimSpace(e.Output); out != "" {
		msg += "; it printed:\n" + out
	}
	return msg
}

func (e *RunError) Unwrap() error { return e.Err }

// Remote is a branch of the remote an add attaches a worktree to, instead of a branch of
// the worktree's own: found by [RemoteBranch] or [PullRequest], fetched, checked out and
// tracked by [Add].
type Remote struct {
	// Branch is the branch on the remote, "" for a pull request whose head no branch of
	// the remote holds: one from a fork, or one whose branch is deleted.
	Branch string
	// PR is the pull request's number, 0 for a plain branch.
	PR int
	// Head is the ref the remote publishes the pull request's head under, "" for a
	// plain branch.
	Head string
}

// PullRefs are the refs a remote publishes a pull request's head under, {n} for its
// number: GitHub's and Forgejo's, GitLab's, Bitbucket Server's. [PullRequest] probes
// them in order when Options.PullRef names none.
var PullRefs = []string{"refs/pull/{n}/head", "refs/merge-requests/{n}/head", "refs/pull-requests/{n}/from"}

// attached reports whether r names anything.
func (r Remote) attached() bool { return r.Branch != "" || r.PR != 0 }

// Name is the local branch the remote's is checked out as: its own name, or pr-<n> for
// a pull request whose head no branch of the remote holds.
func (r Remote) Name() string {
	if r.Branch != "" {
		return r.Branch
	}
	return fmt.Sprintf("pr-%d", r.PR)
}

// ref is the ref fetched from the remote, and what the local branch merges from.
func (r Remote) ref() string {
	if r.Branch != "" {
		return "refs/heads/" + r.Branch
	}
	return r.Head
}

// tracking is the remote-tracking ref the fetch lands in: <remote>/<branch> for a branch,
// the pull request's ref under <remote> otherwise, as refs/remotes/origin/pull/7/head.
func (r Remote) tracking(remote string) string {
	if r.Branch != "" {
		return "refs/remotes/" + remote + "/" + r.Branch
	}
	return "refs/remotes/" + remote + "/" + strings.TrimPrefix(r.Head, "refs/")
}

// how is what the branch row says of an attach: where it was fetched from, and the pull
// request it is.
func (r Remote) how(remote string) string {
	if r.PR != 0 {
		return fmt.Sprintf("pull request #%d, fetched from %s", r.PR, remote)
	}
	return "fetched from " + remote
}

// RemoteBranch finds branch on the repository's remote, for [Add] to attach a worktree
// to. It asks the remote, so a branch pushed from elsewhere is found without a fetch, and
// one the remote does not have is an error saying so. Options.Timeout bounds the
// question and Options.Trace is told it.
func RemoteBranch(main, branch string, o Options) (Remote, error) {
	if err := checkName(branch); err != nil {
		return Remote{}, err
	}
	remote, refs, err := o.runner(main).lsRemote("refs/heads/" + branch)
	if err != nil {
		return Remote{}, err
	}
	if _, ok := refs["refs/heads/"+branch]; !ok {
		return Remote{}, fmt.Errorf("%s has no branch %s; qory worktree add %s cuts a new one", remote, branch, branch)
	}
	return Remote{Branch: branch}, nil
}

// PullRequest finds pull request n on the repository's remote, for [Add] to attach a
// worktree to: the ref the remote publishes its head under, Options.PullRef with {n}
// for the number or each of [PullRefs] in turn when that is "", and the branch of the remote at the
// same commit, which is the pull request's branch when the remote holds it. No hosting
// API is asked, so any host that publishes the head as a ref serves; one that does not
// is an error naming what was looked for. A head that two branches hold is an error
// too, since which one to track is a guess. Options.Timeout bounds the question and
// Options.Trace is told it.
func PullRequest(main string, n int, o Options) (Remote, error) {
	if n < 1 {
		return Remote{}, fmt.Errorf("%d is not a pull request number", n)
	}
	patterns := PullRefs
	if o.PullRef != "" {
		patterns = []string{o.PullRef}
	}
	heads := make([]string, len(patterns))
	for i, p := range patterns {
		heads[i] = strings.ReplaceAll(p, "{n}", strconv.Itoa(n))
	}
	remote, refs, err := o.runner(main).lsRemote(append(heads, "refs/heads/*")...)
	if err != nil {
		return Remote{}, err
	}
	r := Remote{PR: n}
	var sha string
	for _, head := range heads {
		if s, ok := refs[head]; ok {
			r.Head, sha = head, s
			break
		}
	}
	if r.Head == "" {
		return r, fmt.Errorf("pull request %d not found on %s; looked for %s. worktree.pr in qory.yaml names the ref your host uses, with {n} for the number; a host that publishes none takes --branch with the pull request's branch", n, remote, strings.Join(heads, ", "))
	}
	var branches []string
	for name, s := range refs {
		if b, ok := strings.CutPrefix(name, "refs/heads/"); ok && s == sha {
			branches = append(branches, b)
		}
	}
	sort.Strings(branches)
	switch len(branches) {
	case 0:
	case 1:
		r.Branch = branches[0]
	default:
		return r, fmt.Errorf("pull request %d is at the tip of %d branches of %s, %s; --branch says which to attach to", n, len(branches), remote, strings.Join(branches, ", "))
	}
	return r, nil
}

// lsRemote asks the repository's remote for the refs matching the patterns and returns
// the remote's name and the refs it has, ref to commit.
func (r runner) lsRemote(patterns ...string) (string, map[string]string, error) {
	remote := r.remoteOf()
	if remote == "" {
		return "", nil, errors.New("the repository has no remote to attach to")
	}
	out, err := r.net(r.main, append([]string{"ls-remote", "--refs", remote}, patterns...)...)
	if err != nil {
		return remote, nil, fmt.Errorf("git ls-remote %s: %w", remote, err)
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if sha, ref, ok := strings.Cut(line, "\t"); ok {
			refs[ref] = sha
		}
	}
	return remote, refs, nil
}

// checkName refuses what git would not take as a branch name.
func checkName(branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " ~^:?*[\\") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "..") {
		return fmt.Errorf("%q is not a branch name", branch)
	}
	return nil
}

// Main returns the main checkout of the repository holding dir: the first worktree git
// lists, which is the one with the repository's own git directory.
func Main(dir string) (string, error) {
	out, err := runner{}.git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git working tree", dir)
	}
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			return path, nil
		}
	}
	return "", fmt.Errorf("git lists no worktree for %s", dir)
}

// List returns the repository's worktrees, the main checkout first.
func List(main string) ([]Entry, error) {
	out, err := runner{}.git(main, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	var cur *Entry
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			entries = append(entries, Entry{Path: strings.TrimPrefix(line, "worktree ")})
			cur = &entries[len(entries)-1]
		case strings.HasPrefix(line, "branch ") && cur != nil:
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	for i := range entries {
		entries[i].Main = i == 0
		report := filepath.Join(entries[i].Path, ".qory", "harness-report.json")
		if _, err := os.Stat(report); err == nil {
			entries[i].Composed, entries[i].Report = true, report
		}
	}
	return entries, nil
}

// PathFor is the directory the worktree called name gets, from Options.Dir and
// Options.Name, absolute. The name is the branch, or Options.As when that is set.
func PathFor(main, name string, o Options) string {
	dir := o.Dir
	if dir == "" {
		dir = ".."
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(main, dir)
	}
	tmpl := o.Name
	if tmpl == "" {
		tmpl = "wt-{branch}"
	}
	tmpl = strings.ReplaceAll(tmpl, "{branch}", strings.ReplaceAll(name, "/", "-"))
	tmpl = strings.ReplaceAll(tmpl, "{repo}", filepath.Base(main))
	return filepath.Clean(filepath.Join(dir, tmpl))
}

// Add makes the worktree of branch for the repository whose main checkout is main, and
// prepares it. The remote is fetched first, unless Options.Offline, so the base is the
// remote's tip and a branch pushed from elsewhere is found; a fetch that fails is an
// error, never a quiet fall back to the refs already there. A worktree already on the
// branch is reused, wherever it is, and so is a directory already at the path when it is
// a worktree on that branch; one on another branch is refused. The branch is checked out
// when it exists locally, tracked when it exists on the remote, and cut off the base
// otherwise, with its upstream set to the remote branch of its own name and the base it
// was cut from recorded in its git config. With Options.Attach, the remote's branch is
// fetched and the worktree tracks it: a new local branch starts at it, one that exists
// is fast-forwarded to it when that is possible, and a pull request's head that no
// branch of the remote holds is pulled from its ref and pushed nowhere. With
// Options.Onto, a branch that existed is moved or rebased onto Options.Base, see
// [runner.onto]. Then every Options.Link is linked and every Options.Copy copied into
// the worktree, each skipped with a note when its destination is already there or its
// source is not, and every Options.Add is run in the worktree with QORY_WORKTREE,
// QORY_MAIN, QORY_BRANCH and, when the branch's base is recorded, QORY_BASE set; the
// first failing command is a [*RunError].
func Add(main, branch string, o Options) (Added, error) {
	name := branch
	if o.As != "" {
		name = o.As
	}
	a := Added{Path: PathFor(main, name, o), Branch: branch}
	if err := checkName(branch); err != nil {
		return a, err
	}
	if err := checkName(name); err != nil {
		return a, fmt.Errorf("%q is not a worktree name", name)
	}
	r := o.runner(main)
	remote := r.remoteOf()
	attach := o.Attach.attached()
	if attach && remote == "" {
		return a, errors.New("the repository has no remote to attach to")
	}
	if remote != "" && !o.Offline {
		if _, err := r.net(main, "fetch", "--quiet", remote); err != nil {
			return a, fmt.Errorf("git fetch %s: %w; add --offline to go on with the refs already fetched", remote, err)
		}
	}
	if attach {
		if _, err := r.net(main, "fetch", "--quiet", remote, "+"+o.Attach.ref()+":"+o.Attach.tracking(remote)); err != nil {
			return a, fmt.Errorf("git fetch %s %s: %w", remote, o.Attach.ref(), err)
		}
	}
	// A base that resolves to nothing is a mistake on every path, not only the one
	// that cuts a new branch off it.
	if o.Base != "" && !r.resolves(o.Base) {
		return a, fmt.Errorf("base %s is not a branch, tag or commit of this repository", o.Base)
	}
	// A branch checked out somewhere is that worktree, whatever the name says; git
	// refuses a second checkout of one branch, and the main checkout is no worktree.
	if entries, err := List(main); err == nil {
		for _, e := range entries {
			if e.Branch != branch {
				continue
			}
			if e.Main {
				return a, fmt.Errorf("%s is checked out in the main checkout, %s; a worktree is for another branch", branch, main)
			}
			a.Path = e.Path
		}
	}
	existed := true
	if info, err := os.Stat(a.Path); err == nil {
		if !info.IsDir() {
			return a, fmt.Errorf("%s is a file, where the worktree of %s would go", a.Path, branch)
		}
		on, err := r.git(a.Path, "branch", "--show-current")
		if err != nil {
			return a, fmt.Errorf("%s exists and is not a worktree of this repository", a.Path)
		}
		if on != branch {
			return a, fmt.Errorf("%s exists on branch %s, not %s; remove it or pick another branch", a.Path, on, branch)
		}
		a.How = "already there"
	} else {
		if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
			return a, err
		}
		local := r.refExists("refs/heads/" + branch)
		onRemote := remote != "" && r.refExists("refs/remotes/"+remote+"/"+branch)
		switch {
		case local:
			if _, err := r.git(main, "worktree", "add", a.Path, branch); err != nil {
				return a, fmt.Errorf("git worktree add: %w", err)
			}
			a.How = "local"
		case attach:
			if _, err := r.git(main, "worktree", "add", "--no-track", "-b", branch, a.Path, o.Attach.tracking(remote)); err != nil {
				return a, fmt.Errorf("git worktree add -b %s at %s: %w", branch, o.Attach.tracking(remote), err)
			}
			a.How = o.Attach.how(remote)
		case onRemote:
			if _, err := r.git(main, "worktree", "add", "--track", "-b", branch, a.Path, remote+"/"+branch); err != nil {
				return a, fmt.Errorf("git worktree add: %w", err)
			}
			a.How = "remote"
		default:
			base, err := r.baseRef(remote, o.Base)
			if err != nil {
				return a, err
			}
			if _, err := r.git(main, "worktree", "add", "--no-track", "-b", branch, a.Path, base); err != nil {
				return a, fmt.Errorf("git worktree add -b %s off %s: %w", branch, base, err)
			}
			if err := r.recordBase(a.Path, branch, base); err != nil {
				return a, err
			}
			a.How = "new off " + base
			existed = false
		}
	}
	if remote != "" {
		// The upstream is the branch's own name on the remote, set before that branch
		// exists there, so a push creates it and a pull, once it does, reads it. An
		// attached pull request that no branch of the remote holds pulls from the
		// pull request's ref instead, which nothing pushes to.
		merge := "refs/heads/" + branch
		if attach {
			merge = o.Attach.ref()
		}
		if _, err := r.git(a.Path, "config", "branch."+branch+".remote", remote); err != nil {
			return a, err
		}
		if _, err := r.git(a.Path, "config", "branch."+branch+".merge", merge); err != nil {
			return a, err
		}
		if attach && o.Attach.Branch == "" {
			a.Head = remote + " " + o.Attach.Head
		} else {
			a.Upstream = remote + "/" + branch
		}
	}
	if attach && a.How == "local" {
		a.How = o.Attach.how(remote) + "; " + r.fastForward(a.Path, o.Attach.tracking(remote), remote)
	}
	if existed && o.Onto && o.Base != "" {
		if err := r.onto(remote, &a, o); err != nil {
			return a, err
		}
	}
	for _, p := range o.Link {
		switch state := bring(main, a.Path, p, true); state {
		case brought:
			a.Linked = append(a.Linked, p)
		case kept:
			a.Kept = append(a.Kept, p)
		case missing:
			a.Missing = append(a.Missing, p)
		default:
			return a, state.err
		}
	}
	for _, p := range o.Copy {
		switch state := bring(main, a.Path, p, false); state {
		case brought:
			a.Copied = append(a.Copied, p)
		case kept:
			a.Kept = append(a.Kept, p)
		case missing:
			a.Missing = append(a.Missing, p)
		default:
			return a, state.err
		}
	}
	env := hookEnv(a.Path, main, branch, r.recordedBase(branch))
	for _, command := range o.Add {
		if out, err := run(command, a.Path, env, o.Output); err != nil {
			return a, &RunError{Command: command, Path: a.Path, Verb: "add", Output: out, Err: err}
		}
		a.Ran = append(a.Ran, command)
	}
	return a, nil
}

// onto puts the branch of a, which existed before this add, onto o.Base. The branch's
// own commits are the ones past its recorded base, or past the default base when none
// was recorded: with none, the branch is reset to o.Base; with some, they are rebased
// onto it. Either way the user is asked first, unless o.Rebase answers yes, and a no
// keeps the branch where it is, which a.How says. A worktree with uncommitted changes is
// refused, a rebase that stops at a conflict is aborted and the branch left as it was,
// and a branch that already holds o.Base is left alone. The recorded base may be a
// branch that moved on, so --base with the same name rebases onto where it is now.
func (r runner) onto(remote string, a *Added, o Options) error {
	if status, _ := r.git(a.Path, "status", "--porcelain", "--untracked-files=no"); status != "" {
		return fmt.Errorf("%s has uncommitted changes, so %s stays where it is; commit or stash them to move it onto %s", a.Path, a.Branch, o.Base)
	}
	old := r.recordedBase(a.Branch)
	if old == "" {
		var err error
		if old, err = r.baseRef(remote, ""); err != nil {
			return err
		}
	}
	// A branch that holds the base already sits on it, or on top of it; there is
	// nothing to move. The recorded base may be a branch that moved on since, so the
	// question is asked of the branch's own history, not of the two bases' tips.
	if _, err := r.git(a.Path, "merge-base", "--is-ancestor", o.Base, "HEAD"); err == nil {
		a.How += "; on " + o.Base + " already"
		return r.recordBase(a.Path, a.Branch, o.Base)
	}
	out, err := r.git(a.Path, "rev-list", "--count", old+"..HEAD")
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(out)
	yes := o.Rebase
	if !yes {
		if o.Ask == nil {
			return fmt.Errorf("%s exists off %s, so --base %s needs an answer: add --rebase to move it onto %s, or leave --base out to keep it", a.Branch, old, o.Base, o.Base)
		}
		question := fmt.Sprintf("%s sits on %s with no commits of its own. Move it onto %s? [Y/n] ", a.Branch, old, o.Base)
		if n > 0 {
			question = fmt.Sprintf("%s holds %d commit%s off %s. Rebase %s onto %s? [y/N] ", a.Branch, n, plural(n), old, map[bool]string{true: "them", false: "it"}[n > 1], o.Base)
		}
		answer, err := o.Ask(question)
		if err != nil {
			return err
		}
		yes = answer == "y" || answer == "yes" || (n == 0 && answer == "")
	}
	if !yes {
		a.How += "; kept on " + old + ", not moved onto " + o.Base
		return nil
	}
	if n == 0 {
		if _, err := r.git(a.Path, "reset", "--hard", "--quiet", o.Base); err != nil {
			return fmt.Errorf("git reset --hard %s: %w", o.Base, err)
		}
		a.How = "moved onto " + o.Base + "; was on " + old
	} else {
		if _, err := r.git(a.Path, "rebase", "--quiet", "--onto", o.Base, old, a.Branch); err != nil {
			r.git(a.Path, "rebase", "--abort")
			return fmt.Errorf("rebasing %s onto %s stops at a conflict, so it stays on %s; run git rebase --onto %s %s in %s and resolve it there", a.Branch, o.Base, old, o.Base, old, a.Path)
		}
		a.How = fmt.Sprintf("rebased onto %s; %d commit%s, was on %s", o.Base, n, plural(n), old)
	}
	return r.recordBase(a.Path, a.Branch, o.Base)
}

// fastForward brings the local branch checked out at path up to the remote-tracking ref
// when that is a fast-forward, and says what it did: the branch was moved, was at the
// ref already, is ahead of it, or has diverged from it and is left as it is. The ref
// is shown as the remote shows it, origin/feature.
func (r runner) fastForward(path, tracking, remote string) string {
	shown := strings.TrimPrefix(tracking, "refs/remotes/")
	before, _ := r.git(path, "rev-parse", "HEAD")
	if _, err := r.git(path, "merge", "--ff-only", "--quiet", tracking); err != nil {
		return "local branch diverged from " + shown + "; git pull merges"
	}
	after, _ := r.git(path, "rev-parse", "HEAD")
	switch {
	case before != after:
		return "local branch fast-forwarded to " + shown
	case before == r.mustRev(path, tracking):
		return "local branch at " + shown
	default:
		return "local branch ahead of " + shown
	}
}

// mustRev is the commit ref names, "" when it names none.
func (r runner) mustRev(dir, ref string) string {
	sha, _ := r.git(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return sha
}

// Remove takes the worktree at path away, after Options.Remove ran in it. It refuses the
// main checkout and a worktree with uncommitted changes to tracked files, the latter
// unless Options.Force. The branch goes with the worktree unless Options.KeepBranch:
// quietly when every commit of it is on a remote branch, in the main checkout or on the
// base it was cut from, and after a question otherwise, whose answers are to push the
// branch first, keep it, delete it anyway, or stop; Options.DeleteBranch answers delete.
func Remove(main, path string, o Options) (Removed, error) {
	r := runner{main: main, timeout: o.Timeout, trace: o.Trace}
	rm := Removed{Path: path, Main: main}
	if path == main {
		return rm, fmt.Errorf("%s is the main checkout, not a worktree", path)
	}
	branch, err := r.git(path, "branch", "--show-current")
	if err != nil {
		return rm, fmt.Errorf("%s is not a worktree of this repository", path)
	}
	rm.Branch = branch
	if remote := r.remoteOf(); remote != "" {
		rm.Upstream = remote + "/" + branch
	}
	if !o.Force {
		if status, _ := r.git(path, "status", "--porcelain", "--untracked-files=no"); status != "" {
			return rm, fmt.Errorf("%s has uncommitted changes; commit or stash them, or remove with --force", path)
		}
	}
	var last string
	if branch != "" {
		rm.Own, last = r.own(path, branch)
	}
	deleteBranch := branch != "" && !o.KeepBranch
	if deleteBranch {
		if rm.Own > 0 && !o.DeleteBranch {
			held := fmt.Sprintf("branch %s holds %d commit%s no remote branch, the main checkout or its base holds", branch, rm.Own, plural(rm.Own))
			if o.Ask == nil {
				return rm, fmt.Errorf("%s; push them, or remove with --keep-branch or --delete-branch", held)
			}
			answer, err := o.Ask(fmt.Sprintf("%s (last: %q).\n  [p]ush it and delete, [k]eep it, [d]elete it anyway (git reflog finds the commits for 30 days), or [a]bort? [p/k/d/A] ", held, last))
			if err != nil {
				return rm, err
			}
			switch answer {
			case "p", "push":
				if rm.Upstream == "" {
					return rm, errors.New("no remote to push to")
				}
				if _, err := r.net(path, "push", "--quiet", "--set-upstream", strings.SplitN(rm.Upstream, "/", 2)[0], branch); err != nil {
					return rm, fmt.Errorf("git push: %w", err)
				}
				rm.Pushed = true
			case "k", "keep":
				deleteBranch = false
			case "d", "delete":
			default:
				return rm, errors.New("nothing removed")
			}
		}
	}
	env := hookEnv(path, main, branch, r.recordedBase(branch))
	for _, command := range o.Remove {
		if out, err := run(command, path, env, o.Output); err != nil {
			return rm, &RunError{Command: command, Path: path, Verb: "remove", Output: out, Err: err}
		}
		rm.Ran = append(rm.Ran, command)
	}
	// git's own remove refuses untracked files, which a prepared worktree always has,
	// the links and the composed tree say; the checks above are the guard, so git is
	// told to go ahead.
	if _, err := r.git(main, "worktree", "remove", "--force", path); err != nil {
		return rm, fmt.Errorf("git worktree remove: %w", err)
	}
	if deleteBranch {
		// -D, since the check above is stricter than git's own, which wants the branch
		// merged into its upstream or HEAD and knows nothing of the base or other remotes.
		if _, err := r.git(main, "branch", "-D", branch); err != nil {
			return rm, fmt.Errorf("the worktree is removed, and git branch -D %s failed: %w", branch, err)
		}
		rm.BranchDeleted = true
	}
	return rm, nil
}

// Find returns the worktree the name means: a path, or a branch checked out in one.
func Find(main, name string) (Entry, error) {
	entries, err := List(main)
	if err != nil {
		return Entry{}, err
	}
	if abs, err := filepath.Abs(name); err == nil {
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		for _, e := range entries {
			if e.Path == abs {
				return e, nil
			}
		}
	}
	for _, e := range entries {
		if e.Branch == name && e.Branch != "" {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("no worktree is at %s or on a branch of that name; qory worktree list shows them", name)
}

// own counts the commits of the worktree's branch that nothing else holds: no remote
// ref, not the main checkout's HEAD, not the base the branch was cut from. It returns the
// count and the subject of the newest one. A branch whose every commit is elsewhere is
// safe to delete, and this is the count that says so. The trace is told what the count
// leaves out and what it found.
func (r runner) own(path, branch string) (int, string) {
	args := []string{"rev-list", "HEAD", "--not", "--remotes"}
	notOn := []string{"every remote branch"}
	if head, err := r.git(r.main, "rev-parse", "HEAD"); err == nil {
		args = append(args, head)
		notOn = append(notOn, "the main checkout's HEAD "+short(head))
	}
	if base := r.recordedBase(branch); base != "" {
		args = append(args, base)
		notOn = append(notOn, "its recorded base "+base)
	} else {
		notOn = append(notOn, "no base, since none is recorded for it")
	}
	r.say("counting the commits of %s that are not on %s", branch, strings.Join(notOn, ", "))
	out, err := r.git(path, args...)
	if err != nil || out == "" {
		r.say("%s holds no commit of its own", branch)
		return 0, ""
	}
	commits := strings.Split(out, "\n")
	subject, _ := r.git(path, "log", "-1", "--format=%s", commits[0])
	r.say("%s holds %d commit%s of its own, the newest %s %q", branch, len(commits), plural(len(commits)), short(commits[0]), subject)
	return len(commits), subject
}

// short is the first seven characters of a commit hash.
func short(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// baseKey is the git config key, under branch.<name>, that holds the base a branch was
// cut from, so a later add knows what to rebase and a remove what the branch added.
const baseKey = "qory-base"

// recordBase writes base as the branch's recorded base.
func (r runner) recordBase(dir, branch, base string) error {
	_, err := r.git(dir, "config", "branch."+branch+"."+baseKey, base)
	return err
}

// recordedBase is the base recorded for the branch, "" when none was or it no longer
// resolves.
func (r runner) recordedBase(branch string) string {
	base, err := r.git(r.main, "config", "--get", "branch."+branch+"."+baseKey)
	if err != nil || base == "" || !r.resolves(base) {
		return ""
	}
	return base
}

// resolves reports whether ref names a commit of the repository.
func (r runner) resolves(ref string) bool {
	_, err := r.git(r.main, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// baseRef resolves the ref a new branch starts from: the one given, else the remote's
// HEAD branch as <remote>/<branch>, else the main checkout's current branch. A given ref
// that does not resolve is an error naming it.
func (r runner) baseRef(remote, base string) (string, error) {
	if base != "" {
		if !r.resolves(base) {
			return "", fmt.Errorf("base %s is not a branch, tag or commit of this repository", base)
		}
		return base, nil
	}
	if remote != "" {
		if head, err := r.git(r.main, "symbolic-ref", "--quiet", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil && head != "" {
			return head, nil
		}
	}
	current, err := r.git(r.main, "branch", "--show-current")
	if err != nil || current == "" {
		return "", errors.New("no base: the remote has no HEAD branch and the main checkout is on no branch; give --base")
	}
	if !r.resolves(current) {
		return "", fmt.Errorf("%s has no commit yet; a worktree branch starts from a commit, so commit once and add again", current)
	}
	return current, nil
}

// remoteOf is the repository's remote, origin when it has one, else the first, "" for none.
func (r runner) remoteOf() string {
	out, err := r.git(r.main, "remote")
	if err != nil || out == "" {
		return ""
	}
	remotes := strings.Split(out, "\n")
	for _, name := range remotes {
		if name == "origin" {
			return name
		}
	}
	return remotes[0]
}

// refExists reports whether the fully qualified ref exists.
func (r runner) refExists(ref string) bool {
	_, err := r.git(r.main, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// bringState is what [bring] did with one path.
type bringState struct {
	kind int
	err  error
}

var (
	brought = bringState{kind: 1}
	kept    = bringState{kind: 2}
	missing = bringState{kind: 3}
)

// bring links, or copies, p into the worktree: a symlink for a link, relative when the
// source is in the main checkout and absolute when it is outside it, a file or
// directory copy otherwise. A destination already in the worktree is kept, a dangling
// link too, whatever became of the source; a source that is not there is missing.
func bring(main, wt string, p Path, link bool) bringState {
	src := p.From
	if !p.Outside() {
		src = filepath.Join(main, p.From)
	}
	dst := filepath.Join(wt, p.To)
	if _, err := os.Lstat(dst); err == nil {
		return kept
	}
	if _, err := os.Lstat(src); err != nil {
		return missing
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return bringState{err: err}
	}
	if link {
		target := src
		if !p.Outside() {
			if rel, err := filepath.Rel(filepath.Dir(dst), src); err == nil {
				target = rel
			}
		}
		if err := os.Symlink(target, dst); err != nil {
			return bringState{err: err}
		}
		return brought
	}
	if err := copyPath(src, dst); err != nil {
		return bringState{err: err}
	}
	return brought
}

// copyPath copies a file, or a directory with everything below it, keeping modes.
func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	items, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := copyPath(filepath.Join(src, it.Name()), filepath.Join(dst, it.Name())); err != nil {
			return err
		}
	}
	return nil
}

// hookEnv is the environment a configured command runs with: the process's own, then
// the worktree, the main checkout, the branch and, when one is recorded, the base.
func hookEnv(dir, main, branch, base string) []string {
	env := append(os.Environ(), "QORY_WORKTREE="+dir, "QORY_MAIN="+main, "QORY_BRANCH="+branch)
	if base != "" {
		env = append(env, "QORY_BASE="+base)
	}
	return env
}

// run executes one configured command through the shell in dir with env. Its output
// goes to out as it runs, or is kept and returned when out is nil, so a failure can
// show it.
func run(command, dir string, env []string, out io.Writer) (string, error) {
	c := exec.Command("sh", "-c", command)
	c.Dir = dir
	c.Env = env
	if out != nil {
		c.Stdout, c.Stderr = out, out
		return "", c.Run()
	}
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	return buf.String(), err
}

// runner runs git for one add or remove: in the repository whose main checkout is main,
// with the timeout for the commands that reach the remote, and each command told to
// trace. The zero runner serves the verbs that take no options.
type runner struct {
	main    string
	timeout time.Duration
	trace   func(string)
}

// runner is the runner for the repository whose main checkout is main, with o's timeout
// and trace.
func (o Options) runner(main string) runner {
	return runner{main: main, timeout: o.Timeout, trace: o.Trace}
}

// git runs one git command in dir and returns its trimmed output; a failure carries the
// command's own message.
func (r runner) git(dir string, args ...string) (string, error) {
	return r.run(dir, 0, args...)
}

// net is [runner.git] for a command that reaches the remote, bounded by the timeout; one
// running past it is stopped and the error says so.
func (r runner) net(dir string, args ...string) (string, error) {
	return r.run(dir, r.timeout, args...)
}

func (r runner) run(dir string, timeout time.Duration, args ...string) (string, error) {
	if r.trace != nil {
		line := "git " + strings.Join(args, " ")
		if dir != r.main {
			where := dir
			if rel, err := filepath.Rel(r.main, dir); err == nil {
				where = rel
			}
			line += "  (in " + where + ")"
		}
		r.trace(line)
	}
	ctx, cancel := context.Background(), func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	out, err := c.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("ran past %s and was stopped", timeout)
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", errors.New(strings.TrimSpace(string(exit.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// say tells the trace one line of reasoning, when there is one.
func (r runner) say(format string, args ...any) {
	if r.trace != nil {
		r.trace(fmt.Sprintf(format, args...))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
